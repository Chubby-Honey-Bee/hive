package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Chubby-Honey-Bee/hive/internal/citations"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// newVerifyCitationsCmd implements `chb verify-citations`.
//
// Walks every finding's source_urls, extracts DOIs, hits Unpaywall (or
// the cache for repeats), and reports the open-access status per
// finding. Findings whose only DOIs resolve to paywalled / missing
// sources are flagged for the MSS no-laundering audit, which lists them
// under oa_citation_downgrades. Nothing re-labels them automatically —
// the list is candidates, and changing a claim's label is a deliberate
// act. Act on one with `chb db-write update_finding`.
func newVerifyCitationsCmd() *cobra.Command {
	var o verifyCitationsOptions
	cmd := &cobra.Command{
		Use:   "verify-citations",
		Short: "Verify findings cite open-access DOIs (Unpaywall lookup)",
		Long: `Walks every finding's source_urls, extracts DOIs, and queries
Unpaywall to confirm each DOI is open-access. Results are cached in the
citation_oa_cache table; --stale controls how long cache entries are
trusted before re-checking (default 90 days).

Unpaywall requires a contact address on every request: set
HIVE_UNPAYWALL_EMAIL to one you read. Without it, the run reads the cache
only, as --offline does, and says so.

Use --offline to skip the HTTP path entirely (audit-only mode that
reports cache state without making network calls). Use --json for a
machine-readable summary.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd.Context())
		},
	}
	cmd.Flags().BoolVar(&o.jsonOut, "json", false, "emit a JSON summary instead of human-readable text")
	cmd.Flags().DurationVar(&o.stale, "stale", 90*24*time.Hour, "cache TTL — re-verify entries older than this")
	cmd.Flags().BoolVar(&o.offline, "offline", false, "audit-only: never hit the network; cache misses are reported as unverified")
	cmd.Flags().IntVar(&o.limit, "limit", 0, "if >0, only process the first N findings (with DOIs)")
	cmd.Flags().DurationVar(&o.budget, "budget", 2*time.Minute, "wall-clock ceiling for the whole run; 0 disables it. DOIs not reached are reported unverified")
	return cmd
}

// verifyCitationsOptions holds the flags of chb verify-citations.
type verifyCitationsOptions struct {
	jsonOut bool
	stale   time.Duration
	offline bool
	limit   int
	budget  time.Duration
}

// run walks the findings' DOIs and reports their open-access status. --limit
// bounds how many findings are walked, not how long the walk takes: each DOI
// is verified serially against a 10s per-request timeout, so N findings x M
// DOIs would have no ceiling of their own. The budget is the ceiling. Work
// done before it expires is kept and reported; the rest is counted as
// unverified, which is what it is.
func (o *verifyCitationsOptions) run(ctx context.Context) error {
	ctx, cancel := withCitationBudget(ctx, o.budget)
	defer cancel()
	s, err := db.NewStore(dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Init(); err != nil {
		return err
	}
	v := citations.NewHTTPVerifier()
	o.offline = o.cacheOnly(v, os.Stderr)
	w := &citationWalk{opts: o, verifier: v, cache: s.CitationOA()}
	if err := w.walk(ctx, s.ReadDB); err != nil {
		return err
	}
	return w.report()
}

// cacheOnly reports whether the run reads the cache only: under --offline,
// or when v has no contact address, which Unpaywall requires. It says the
// second on stderr, naming the variable that sets one.
func (o *verifyCitationsOptions) cacheOnly(v *citations.HTTPVerifier, stderr io.Writer) bool {
	if o.offline {
		return true
	}
	if v.Email != "" {
		return false
	}
	fmt.Fprintf(stderr, "verify-citations: %v. Reading the cache only, as --offline does.\n", citations.ErrNoContact)
	return true
}

// withCitationBudget bounds ctx by budget, when budget is positive.
func withCitationBudget(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget > 0 {
		return context.WithTimeout(ctx, budget)
	}
	return ctx, func() {}
}

// citationCheck is one finding's tally of open-access, paywalled and
// unverified DOIs.
type citationCheck struct {
	ID         int64    `json:"id"`
	Label      string   `json:"mss_label"`
	DOIs       []string `json:"dois"`
	OACount    int      `json:"oa_count"`
	Paywalled  int      `json:"paywalled_count"`
	Unverified int      `json:"unverified_count"`
}

// doiStatus is what a lookup establishes about one DOI.
type doiStatus int

const (
	doiUnverified doiStatus = iota
	doiOpenAccess
	doiPaywalled
)

// add counts one DOI of the finding under its status.
func (fc *citationCheck) add(s doiStatus) {
	switch s {
	case doiOpenAccess:
		fc.OACount++
	case doiPaywalled:
		fc.Paywalled++
	default:
		fc.Unverified++
	}
}

// citationWalk is one pass of verify-citations over the findings: the
// verifier and the cache it consults, and what it has found so far.
type citationWalk struct {
	opts        *verifyCitationsOptions
	verifier    *citations.HTTPVerifier
	cache       *db.CitationOARepo
	processed   int
	budgetSpent bool
	checks      []citationCheck
}

// walk tallies the DOIs of every finding that cites one, up to --limit
// findings.
func (w *citationWalk) walk(ctx context.Context, conn *sql.DB) error {
	rows, err := conn.Query(
		`SELECT id, mss_label, COALESCE(source_urls, '') FROM findings ORDER BY id`)
	if err != nil {
		return fmt.Errorf("query findings: %w", err)
	}
	defer rows.Close()
	return w.walkRows(ctx, rows)
}

// walkRows tallies the findings rows holds until --limit findings with DOIs
// are done.
func (w *citationWalk) walkRows(ctx context.Context, rows *sql.Rows) error {
	for !w.limitReached() && rows.Next() {
		if err := w.checkRow(ctx, rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// limitReached reports whether --limit findings with DOIs are done.
func (w *citationWalk) limitReached() bool {
	return w.opts.limit > 0 && w.processed >= w.opts.limit
}

// checkRow tallies the finding in the current row, when it cites a DOI. A
// spent budget does not stop the walk: findings not yet reached are still
// reported, from the cache or as unverified.
func (w *citationWalk) checkRow(ctx context.Context, rows *sql.Rows) error {
	var id int64
	var label, src string
	if err := rows.Scan(&id, &label, &src); err != nil {
		return err
	}
	dois := citations.ExtractDOIs(src)
	if len(dois) == 0 {
		return nil
	}
	w.processed++
	fc := citationCheck{ID: id, Label: label, DOIs: dois}
	for _, doi := range dois {
		fc.add(w.statusOf(ctx, doi))
	}
	w.checks = append(w.checks, fc)
	return nil
}

// statusOf is the status of one DOI: from a cache entry younger than
// --stale, else unverified offline or once the budget is spent, else from
// Unpaywall.
func (w *citationWalk) statusOf(ctx context.Context, doi string) doiStatus {
	key := strings.ToLower(strings.TrimSpace(doi))
	if s, ok := w.cachedStatus(key); ok {
		return s
	}
	if w.opts.offline {
		return doiUnverified
	}
	if ctx.Err() != nil {
		w.budgetSpent = true
		return doiUnverified
	}
	return w.lookup(ctx, key, doi)
}

// cachedStatus is the status the cache holds for key, when its entry is
// younger than --stale.
func (w *citationWalk) cachedStatus(key string) (doiStatus, bool) {
	cached, err := w.cache.Get(key, "unpaywall")
	if err != nil || !w.fresh(cached) {
		return doiUnverified, false
	}
	return cachedDOIStatus(cached), true
}

// fresh reports whether a cache entry is younger than --stale.
func (w *citationWalk) fresh(e *db.CitationOAEntry) bool {
	return w.opts.stale > 0 && time.Since(e.VerifiedAt) < w.opts.stale
}

// cachedDOIStatus is the status a cache entry records.
func cachedDOIStatus(e *db.CitationOAEntry) doiStatus {
	switch {
	case e.Error.Valid && e.Error.String != "":
		return doiUnverified
	case e.IsOA:
		return doiOpenAccess
	}
	return doiPaywalled
}

// lookup asks Unpaywall about doi and caches the answer under key.
func (w *citationWalk) lookup(ctx context.Context, key, doi string) doiStatus {
	res, err := w.verifier.Verify(ctx, doi)
	cut := ctx.Err() != nil
	if cut {
		w.budgetSpent = true
	}
	if !unpaywallAnswered(res, err, cut) {
		return doiUnverified
	}
	_ = w.cache.Upsert(citationCacheEntry(key, res))
	return verifiedDOIStatus(res)
}

// unpaywallAnswered reports whether a lookup came back with Unpaywall's
// answer. When the budget cut the lookup short, an error in the result is
// the deadline, not Unpaywall's answer, so it is not cached: a cached
// deadline would make a re-run skip this DOI for the whole --stale window.
func unpaywallAnswered(res *citations.VerifyResult, err error, cut bool) bool {
	if err != nil || res == nil {
		return false
	}
	return !cut || res.Error == ""
}

// citationCacheEntry is the cache entry for Unpaywall's answer res.
func citationCacheEntry(key string, res *citations.VerifyResult) *db.CitationOAEntry {
	return &db.CitationOAEntry{
		DOI:        key,
		Source:     "unpaywall",
		IsOA:       res.IsOA,
		OAURL:      sqlNullStr(res.OAURL),
		License:    sqlNullStr(res.License),
		HostType:   sqlNullStr(res.HostType),
		Title:      sqlNullStr(res.Title),
		Year:       sqlNullInt(res.Year),
		Error:      sqlNullStr(res.Error),
		VerifiedAt: time.Now().UTC(),
	}
}

// verifiedDOIStatus is the status Unpaywall's answer res gives.
func verifiedDOIStatus(res *citations.VerifyResult) doiStatus {
	switch {
	case res.Error != "":
		return doiUnverified
	case res.IsOA:
		return doiOpenAccess
	}
	return doiPaywalled
}

// report prints the tallies and the cache's state, as one JSON object with
// --json.
func (w *citationWalk) report() error {
	oa, paywalled, unverified, _ := w.cache.CountByOA()
	if w.opts.jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"findings_checked":    len(w.checks),
			"budget_exhausted":    w.budgetSpent,
			"checks":              w.checks,
			"cache_total_oa":      oa,
			"cache_total_paywall": paywalled,
			"cache_total_unverif": unverified,
		})
	}
	fmt.Printf("Verified citations across %d findings\n\n", len(w.checks))
	if w.budgetSpent {
		fmt.Printf("  the %s budget ran out — the remaining DOIs are counted unverified, not paywalled.\n"+
			"  Re-run to continue; results already cached are not re-fetched.\n\n", w.opts.budget)
	}
	fmt.Printf("  cache state: oa=%d paywalled=%d unverified=%d\n\n", oa, paywalled, unverified)
	for _, fc := range w.checks {
		fmt.Printf("  finding %d (%s):\n", fc.ID, fc.Label)
		fmt.Printf("    dois=%d oa=%d paywalled=%d unverified=%d\n",
			len(fc.DOIs), fc.OACount, fc.Paywalled, fc.Unverified)
	}
	return nil
}

// sqlNullStr is s as a nullable column value: NULL when s is empty.
func sqlNullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// sqlNullInt is i as a nullable column value: NULL when i is 0.
func sqlNullInt(i int) sql.NullInt64 {
	if i == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(i), Valid: true}
}
