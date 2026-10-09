package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/useragent"
	"github.com/spf13/cobra"
)

// ─── chb validate-sources (validate-sources.py) ─────────────

func newValidateSourcesCmd() *cobra.Command {
	var wave int
	var quick bool

	cmd := &cobra.Command{
		Use:   "validate-sources",
		Short: "Validate the source URLs findings cite and the registered sources",
		Long: `Checks every URL in the findings' source_urls and every registered source,
or with --wave only those of that wave: the set the gate checks. Each verdict
is recorded in the sources table under the URL checked. URLs that differ only
in host case or a fragment send the same request, so it is made once and its
verdict recorded under each. With --wave, a URL with no wave on record takes
that wave.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var scope *int
			if cmd.Flags().Changed("wave") {
				scope = &wave
			}
			return validateSources(scope, quick)
		},
	}
	cmd.Flags().IntVar(&wave, "wave", 0, "only validate the sources of this wave")
	cmd.Flags().BoolVar(&quick, "quick", false, "parse and count only, skip HTTP checks")
	return cmd
}

// validateSources reports the URLs the findings in scope cite (every wave
// when scope is nil) and, unless quick, checks each source the gate looks
// up and records its verdict.
func validateSources(scope *int, quick bool) error {
	groups, err := reportSourceURLs(scope)
	if err != nil {
		return err
	}
	if quick {
		fmt.Println("\n[--quick mode] Skipping HTTP validation.")
		return nil
	}
	return checkSources(groups, scope)
}

// reportSourceURLs counts the URLs the findings in scope cite, groups the
// URLs the gate checks by the request each sends, and prints the
// extraction report. It returns the groups.
func reportSourceURLs(scope *int) ([][]string, error) {
	rows, err := scopeFindings(scope)
	if err != nil {
		return nil, err
	}
	totalURLs, findingsWithZero := countFindingURLs(rows)
	// The set the gate checks, so every source it looks up has a
	// verdict once this has run over the wave.
	scopeURLs, err := store.Sources().WaveURLs(scope)
	if err != nil {
		return nil, err
	}
	groups := groupByRequest(scopeURLs)

	fmt.Println("\n--- URL Extraction Report ---")
	fmt.Printf("Findings scanned:       %d\n", len(rows))
	fmt.Printf("Total URLs extracted:    %d\n", totalURLs)
	fmt.Printf("Unique URLs (deduped):   %d  (registered sources included)\n", len(groups))
	fmt.Printf("Findings with zero URLs: %d\n", findingsWithZero)
	return groups, nil
}

// scopeFindings prints the scope and reads the source_urls of the findings
// in it.
func scopeFindings(scope *int) ([]map[string]any, error) {
	query := "SELECT id, source_urls FROM findings"
	var qargs []any
	if scope != nil {
		query += " WHERE wave = ?"
		qargs = append(qargs, *scope)
		fmt.Printf("Scope: wave %d\n", *scope)
	} else {
		fmt.Println("Scope: all waves")
	}
	rows, err := db.QueryToMaps(store.ReadDB, query, qargs...)
	if err != nil {
		return nil, fmt.Errorf("read findings: %w", err)
	}
	return rows, nil
}

// countFindingURLs counts the URLs the findings cite, and the findings that
// cite none.
func countFindingURLs(rows []map[string]any) (total, withZero int) {
	for _, row := range rows {
		urls := db.ExtractSourceURLs(anyStr(row["source_urls"]))
		total += len(urls)
		if len(urls) == 0 {
			withZero++
		}
	}
	return total, withZero
}

// checkSources checks each request group and prints the count of each
// verdict.
func checkSources(groups [][]string, scope *int) error {
	if len(groups) == 0 {
		fmt.Println("\nNo URLs to validate.")
		return nil
	}
	stats, err := checkSourceGroups(groups, scope)
	if err != nil {
		return err
	}
	printSourceValidationReport(stats)
	return nil
}

// sourceStatusSymbols marks each verdict in the per-URL listing.
var sourceStatusSymbols = map[string]string{
	"live": "+", "dead": "X", "redirect": "~",
	"blocked": "!", "unreachable": "?",
}

// checkSourceGroups checks the first URL of each group, prints its verdict
// and records the verdict under every URL in the group. It returns the
// count of each verdict.
func checkSourceGroups(groups [][]string, scope *int) (map[string]int, error) {
	stats := map[string]int{"live": 0, "dead": 0, "redirect": 0, "blocked": 0, "unreachable": 0}
	fmt.Printf("\nValidating %d URLs...\n\n", len(groups))

	client := &http.Client{Timeout: 10 * time.Second}

	for _, group := range groups {
		status, code := checkURL(client, group[0])
		stats[status]++
		printSourceVerdict(group[0], status, code)
		if err := recordSourceVerdict(group, scope, status, code); err != nil {
			return nil, err
		}
	}
	return stats, nil
}

// printSourceVerdict prints one checked URL's line of the listing.
func printSourceVerdict(u, status string, code int) {
	codeStr := "---"
	if code > 0 {
		codeStr = fmt.Sprintf("%d", code)
	}
	fmt.Printf("  [%s] %3s  %s\n", sourceStatusSymbols[status], codeStr, clip(u, 80))
}

// recordSourceVerdict records the verdict under every URL that sent this
// request, since the gate looks each URL up as it was cited. A row with no
// wave takes the scanned one; a row with a wave keeps it.
func recordSourceVerdict(group []string, scope *int, status string, code int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, v := range group {
		if _, err := store.WriteDB.Exec(
			`INSERT INTO sources (url, wave, validation_status, http_status, validated_at)
						 VALUES (?,?,?,?,?)
						 ON CONFLICT(url) DO UPDATE SET
						   wave              = COALESCE(sources.wave, excluded.wave),
						   validation_status = excluded.validation_status,
						   http_status       = excluded.http_status,
						   validated_at      = excluded.validated_at`,
			v, scope, status, code, now,
		); err != nil {
			return fmt.Errorf("record validation for %s: %w", v, err)
		}
	}
	return nil
}

// printSourceValidationReport prints the count of each verdict.
func printSourceValidationReport(stats map[string]int) {
	fmt.Println("\n--- Validation Report ---")
	fmt.Printf("Live:        %d\n", stats["live"])
	fmt.Printf("Redirect:    %d\n", stats["redirect"])
	fmt.Printf("Dead:        %d\n", stats["dead"])
	fmt.Printf("Blocked:     %d  (refused the bot; says nothing about the document)\n", stats["blocked"])
	fmt.Printf("Unreachable: %d  (DNS, connection or TLS failure)\n", stats["unreachable"])
	fmt.Printf("Total:       %d\n",
		stats["live"]+stats["dead"]+stats["redirect"]+stats["blocked"]+stats["unreachable"])
}

// groupByRequest groups the URLs that send the same request, so each request
// is made once and its verdict recorded under every URL in the group. Only an
// http or https URL with a host joins a group, keyed on everything but host
// case and the fragment, which the server never sees. Scheme, path, a
// trailing slash and the query can each change the answer, so they stay in
// the key. Any other string is a group of its own: it cannot be fetched, and
// its "unreachable" must not stand for a URL that can. Groups and their
// members are sorted.
func groupByRequest(urls []string) [][]string {
	byKey := make(map[string][]string)
	var groups [][]string
	for _, u := range urls {
		key, ok := sourceRequestKey(u)
		if !ok {
			groups = append(groups, []string{u})
			continue
		}
		byKey[key] = append(byKey[key], u)
	}
	for _, g := range byKey {
		sort.Strings(g)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}

// sourceRequestKey is the request an http or https URL with a host sends:
// the URL less host case and the fragment. It reports false for any other
// string.
func sourceRequestKey(u string) (string, bool) {
	p, err := url.Parse(u)
	if err != nil || !fetchableSourceURL(p) {
		return "", false
	}
	p.Host = strings.ToLower(p.Host)
	p.Fragment, p.RawFragment = "", ""
	return p.String(), true
}

// fetchableSourceURL reports whether p is an http or https URL with a host.
func fetchableSourceURL(p *url.URL) bool {
	return (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

// checkURL classifies one source URL.
//
// Statuses: live, redirect, blocked, dead, unreachable. "blocked" exists
// because a 401/403/429 says something about the bot, not the document, and
// calling those dead would fail the gate on citations that are perfectly
// good. A transport failure is "unreachable", not "timeout": a DNS failure,
// a refused connection and a TLS error are not timeouts.
func checkURL(client *http.Client, rawURL string) (string, int) {
	resp, err := fetchSource(client, rawURL)
	if err != nil {
		return "unreachable", 0
	}
	defer resp.Body.Close()
	return classifySourceStatus(resp.StatusCode), resp.StatusCode
}

// fetchSource asks for the URL with HEAD. A 405 is the server declining
// HEAD, not a verdict on the URL, and a 403 is often HEAD-specific bot
// filtering, so on either it asks again properly, with GET.
func fetchSource(client *http.Client, rawURL string) (*http.Response, error) {
	resp, err := sourceRequest(client, "HEAD", rawURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 405 && resp.StatusCode != 403 {
		return resp, nil
	}
	resp.Body.Close()
	return sourceRequest(client, "GET", rawURL)
}

// sourceRequest sends one request for the URL as validate-sources.
func sourceRequest(client *http.Client, method, rawURL string) (*http.Response, error) {
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", useragent.For("validate-sources"))
	return client.Do(req)
}

// sourceStatusByCode holds the verdict of each status code that has one of
// its own: 404 and 410 are dead; 401, 403 and 429 are blocked — the bot was
// refused, and the document is not known to be gone.
var sourceStatusByCode = map[int]string{
	404: "dead", 410: "dead",
	401: "blocked", 403: "blocked", 429: "blocked",
}

// classifySourceStatus is the verdict a status code gives.
func classifySourceStatus(code int) string {
	if status, ok := sourceStatusByCode[code]; ok {
		return status
	}
	return sourceStatusByClass(code / 100)
}

// sourceStatusByClass is the verdict of a status code by its class: a 5xx
// is blocked, since a 502 says nothing about the document either; a 3xx a
// redirect; a 2xx live; anything else dead.
func sourceStatusByClass(class int) string {
	switch {
	case class >= 5:
		return "blocked"
	case class == 3:
		return "redirect"
	case class == 2:
		return "live"
	}
	return "dead"
}
