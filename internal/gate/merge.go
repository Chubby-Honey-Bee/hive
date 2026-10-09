package gate

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// MergeStats holds the results of a merge operation.
type MergeStats struct {
	TotalFindings    int `json:"total_findings"`
	CoordinateGroups int `json:"coordinate_groups"`
	// NearDuplicates counts findings that joined an existing cluster rather
	// than starting one. Nothing is deleted: `swarm-merge` scores
	// near-duplicates, it removes no row.
	NearDuplicates     int            `json:"near_duplicates"`
	ConvergenceUpdates map[string]int `json:"convergence_updates"`
	ConflictsDetected  int            `json:"conflicts_detected"`
}

// MergeFindings groups findings by CDE coordinate, clusters near-duplicates
// within each coordinate cell, and scores each cluster's convergence from the
// agents that corroborate it. It writes convergence_count and
// convergence_level; it never deletes a finding.
func MergeFindings(store *db.Store, wave *int, minAgentsHigh, minAgentsMedium int, dedupThreshold float64) (*MergeStats, error) {
	findings, err := loadMergeFindings(store, wave)
	if err != nil {
		return nil, err
	}
	if len(findings) == 0 {
		return &MergeStats{ConvergenceUpdates: map[string]int{}}, nil
	}
	m := &merger{
		store:          store,
		rule:           convergenceRule{minAgentsHigh: minAgentsHigh, minAgentsMedium: minAgentsMedium},
		dedupThreshold: dedupThreshold,
	}
	return m.mergeCells(findings)
}

// loadMergeFindings reads the findings MergeFindings scores: the wave's, or
// every finding when wave is nil, in coordinate order.
func loadMergeFindings(store *db.Store, wave *int) ([]findingForMerge, error) {
	q := "SELECT * FROM findings"
	var args []any
	if wave != nil {
		q += " WHERE wave = ?"
		args = append(args, *wave)
	}
	q += " ORDER BY d1, d2, d3, d4, d5, created_at, id"

	// WASP scan detector: this groups findings by coordinate in Go after
	// pulling the table — a full scan when no wave predicate narrows it
	// (Q5). `chb wasp-scan-report` shows it to an operator; nothing feeds
	// it to a forager. Best-effort; never affects the merge.
	store.RecordScanIfFullScan(q, "findings", args...)

	rows, err := store.ReadDB.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query findings: %w", err)
	}
	defer rows.Close()
	return scanMergeFindings(rows)
}

// mergeColumnSetters map a findings column onto the findingForMerge field
// it fills.
var mergeColumnSetters = map[string]func(f *findingForMerge, v any){
	"id": func(f *findingForMerge, v any) { f.id = toInt64(v) },
	"agent": func(f *findingForMerge, v any) {
		if s, ok := v.(string); ok {
			f.agent = s
		}
	},
	"finding": func(f *findingForMerge, v any) {
		if s, ok := v.(string); ok {
			f.finding = s
		}
	},
	"source_urls": func(f *findingForMerge, v any) {
		if s, ok := v.(string); ok {
			f.sourceURLs = &s
		}
	},
	"wave": func(f *findingForMerge, v any) { f.wave = int(toInt64(v)) },
	"d1":   func(f *findingForMerge, v any) { f.d1 = int64PtrFromAny(v) },
	"d2":   func(f *findingForMerge, v any) { f.d2 = int64PtrFromAny(v) },
	"d3":   func(f *findingForMerge, v any) { f.d3 = int64PtrFromAny(v) },
	"d4":   func(f *findingForMerge, v any) { f.d4 = int64PtrFromAny(v) },
	"d5":   func(f *findingForMerge, v any) { f.d5 = int64PtrFromAny(v) },
}

// scanMergeFindings reads every row of a SELECT * over findings.
func scanMergeFindings(rows *sql.Rows) ([]findingForMerge, error) {
	cols, _ := rows.Columns()
	var findings []findingForMerge
	for rows.Next() {
		f, err := scanMergeFinding(rows, cols)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query findings: %w", err)
	}
	return findings, nil
}

// scanMergeFinding reads the current row into a findingForMerge.
func scanMergeFinding(rows *sql.Rows, cols []string) (findingForMerge, error) {
	vals, err := scanValues(rows, len(cols))
	if err != nil {
		return findingForMerge{}, fmt.Errorf("scan finding: %w", err)
	}
	var f findingForMerge
	f.setColumns(cols, vals)
	return f, nil
}

// scanValues reads the current row's n column values.
func scanValues(rows *sql.Rows, n int) ([]any, error) {
	vals := make([]any, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	return vals, rows.Scan(ptrs...)
}

// setColumns fills the fields the named columns map onto.
func (f *findingForMerge) setColumns(cols []string, vals []any) {
	for i, col := range cols {
		if setter, ok := mergeColumnSetters[col]; ok {
			setter(f, vals[i])
		}
	}
}

// convergenceRule maps an effective agent count to a convergence level.
type convergenceRule struct {
	minAgentsHigh, minAgentsMedium int
}

// level is the convergence level an effective count reaches.
func (r convergenceRule) level(effective int) string {
	switch {
	case effective >= r.minAgentsHigh:
		return "high"
	case effective >= r.minAgentsMedium:
		return "medium"
	}
	return "low"
}

// merger is one MergeFindings pass: where it writes, its rule, and the
// stats it fills.
type merger struct {
	store          *db.Store
	rule           convergenceRule
	dedupThreshold float64
	stats          *MergeStats
}

// mergeCells scores every coordinate cell of findings.
func (m *merger) mergeCells(findings []findingForMerge) (*MergeStats, error) {
	groups := groupMergeFindings(findings)
	m.stats = &MergeStats{
		TotalFindings:      len(findings),
		CoordinateGroups:   len(groups),
		ConvergenceUpdates: map[string]int{"high": 0, "medium": 0, "low": 0},
	}
	for _, group := range groups {
		if err := m.mergeCell(group); err != nil {
			return m.stats, err
		}
	}
	return m.stats, nil
}

// groupMergeFindings groups findings by CDE coordinate vector.
func groupMergeFindings(findings []findingForMerge) map[coordKey][]findingForMerge {
	groups := make(map[coordKey][]findingForMerge)
	for _, f := range findings {
		key := keyOf(f.d1, f.d2, f.d3, f.d4, f.d5)
		groups[key] = append(groups[key], f)
	}
	return groups
}

// mergeCell clusters one coordinate cell, scores each cluster, and records
// the numeric conflicts between the clusters' representatives.
//
// Convergence is per finding, not per coordinate cell: two agents making
// unrelated claims at the same coordinate do not agree. The dedup pass
// decides which findings are "the same finding", so that cluster is the unit
// scored.
func (m *merger) mergeCell(group []findingForMerge) error {
	clusters, dupes := clusterFindings(group, m.dedupThreshold)
	m.stats.NearDuplicates += dupes

	for _, cluster := range clusters {
		if err := m.scoreCluster(cluster); err != nil {
			return err
		}
	}

	// Detect numeric conflicts between the cluster representatives.
	n, err := detectConflicts(m.store, representatives(clusters))
	if err != nil {
		return err
	}
	m.stats.ConflictsDetected += n
	return nil
}

// scoreCluster writes one cluster's convergence onto each of its findings.
//
// The stored count is the effective number the level comes from, not the
// number of agents: three agents citing one source store a count of 1 beside
// level low, so the probe order and the quorum threshold read the count the
// level reflects.
func (m *merger) scoreCluster(cluster []findingForMerge) error {
	effective := effectiveConvergence(cluster)
	level := m.rule.level(effective)
	for _, f := range cluster {
		if _, err := m.store.WriteDB.Exec(
			"UPDATE findings SET convergence_count=?, convergence_level=? WHERE id=?",
			effective, level, f.id,
		); err != nil {
			// A silent failure here leaves a finding at its previous
			// convergence, which the quorum rule then trusts.
			return fmt.Errorf("update convergence for finding %d: %w", f.id, err)
		}
	}
	m.stats.ConvergenceUpdates[level] += len(cluster)
	return nil
}

// effectiveConvergence is a cluster's corroboration: its distinct agents,
// capped by the distinct source domains they cite, and at least 1.
func effectiveConvergence(cluster []findingForMerge) int {
	return max(min(distinctAgents(cluster), sourceDiversity(cluster)), 1)
}

// distinctAgents counts the agents that wrote a cluster's findings.
func distinctAgents(cluster []findingForMerge) int {
	agents := make(map[string]bool)
	for _, f := range cluster {
		agents[f.agent] = true
	}
	return len(agents)
}

// representatives are the first member of each cluster.
func representatives(clusters [][]findingForMerge) []findingForMerge {
	reps := make([]findingForMerge, 0, len(clusters))
	for _, cluster := range clusters {
		reps = append(reps, cluster[0])
	}
	return reps
}

// clusterFindings groups near-duplicate findings (Jaccard similarity above
// threshold) within one coordinate cell. Each cluster is one distinct claim,
// its first member the representative. Returns the clusters and the number of
// findings that joined an existing one.
func clusterFindings(group []findingForMerge, threshold float64) ([][]findingForMerge, int) {
	var clusters [][]findingForMerge
	dupes := 0
	for _, f := range group {
		if i := matchingCluster(clusters, f, threshold); i >= 0 {
			clusters[i] = append(clusters[i], f)
			dupes++
			continue
		}
		clusters = append(clusters, []findingForMerge{f})
	}
	return clusters, dupes
}

// matchingCluster is the index of the first cluster whose representative f
// nearly duplicates, or -1.
func matchingCluster(clusters [][]findingForMerge, f findingForMerge, threshold float64) int {
	for i, c := range clusters {
		if jaccardSimilarity(f.finding, c[0].finding) > threshold {
			return i
		}
	}
	return -1
}

// detectConflicts records conflicts for numeric divergences within seen and
// returns the count newly recorded.
func detectConflicts(store *db.Store, seen []findingForMerge) (int, error) {
	if len(seen) <= 1 {
		return 0, nil
	}
	return detectNumericConflicts(store, seen)
}

// numericConflict is one divergence detectNumericConflicts records: the
// pair, in seen order, and its description.
type numericConflict struct {
	a, b findingForMerge
	desc string
}

// detectNumericConflicts records a conflict for any pair of findings whose
// same-typed numeric values diverge by more than 20%, once per pair and
// description. Returns the count newly recorded.
func detectNumericConflicts(store *db.Store, seen []findingForMerge) (int, error) {
	count := 0
	for _, c := range numericConflicts(seen) {
		inserted, err := store.Conflicts().RecordOnce(max(c.a.wave, c.b.wave), c.a.id, c.b.id, c.desc)
		if err != nil {
			return count, err
		}
		if inserted {
			count++
		}
	}
	return count, nil
}

// numericConflicts lists the divergences between every pair in seen, pairs
// in order, then each pair's numbers in order.
func numericConflicts(seen []findingForMerge) []numericConflict {
	var out []numericConflict
	for i := range seen {
		numsI := extractNumbers(seen[i].finding)
		if len(numsI) == 0 {
			continue
		}
		for j := i + 1; j < len(seen); j++ {
			out = append(out, pairDivergences(seen[i], seen[j], numsI)...)
		}
	}
	return out
}

// pairDivergences lists the divergences between a's numbers numsA and b's.
func pairDivergences(a, b findingForMerge, numsA []numVal) []numericConflict {
	numsB := extractNumbers(b.finding)
	var out []numericConflict
	for _, na := range numsA {
		for _, nb := range numsB {
			if desc, ok := numericConflictDesc(na, nb); ok {
				out = append(out, numericConflict{a, b, desc})
			}
		}
	}
	return out
}

// numericConflictDesc describes two positive numbers of one type that
// diverge by more than 20%.
func numericConflictDesc(ni, nj numVal) (string, bool) {
	if ni.typ != nj.typ || math.Min(ni.val, nj.val) <= 0 {
		return "", false
	}
	divergence := math.Abs(ni.val-nj.val) / math.Max(ni.val, nj.val)
	if divergence > 0.2 {
		return fmt.Sprintf("Numeric divergence (%s): %.2f vs %.2f (%.0f%% difference)",
			ni.typ, ni.val, nj.val, divergence*100), true
	}
	return "", false
}

type numVal struct {
	typ string
	val float64
}

var dollarRe = regexp.MustCompile(`\$[\d,]+\.?\d*`)
var percentRe = regexp.MustCompile(`([\d.]+)%`)

func extractNumbers(text string) []numVal {
	var result []numVal
	for _, m := range dollarRe.FindAllString(text, -1) {
		s := strings.ReplaceAll(strings.TrimPrefix(m, "$"), ",", "")
		var v float64
		fmt.Sscanf(s, "%f", &v)
		result = append(result, numVal{"dollar", v})
	}
	for _, m := range percentRe.FindAllStringSubmatch(text, -1) {
		var v float64
		fmt.Sscanf(m[1], "%f", &v)
		result = append(result, numVal{"percent", v})
	}
	return result
}

func jaccardSimilarity(a, b string) float64 {
	clean := regexp.MustCompile(`[^\w\s]`)
	wordsA := toWordSet(clean.ReplaceAllString(strings.ToLower(a), ""))
	wordsB := toWordSet(clean.ReplaceAllString(strings.ToLower(b), ""))
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0.0
	}
	return float64(len(sharedWords(wordsA, wordsB))) / float64(unionSize(wordsA, wordsB))
}

func toWordSet(s string) map[string]bool {
	m := make(map[string]bool)
	for _, w := range strings.Fields(s) {
		if w != "" {
			m[w] = true
		}
	}
	return m
}

type findingForMerge struct {
	id                 int64
	agent              string
	finding            string
	sourceURLs         *string
	wave               int
	d1, d2, d3, d4, d5 *int64
}

func sourceDiversity(findings []findingForMerge) int {
	domains := make(map[string]bool)
	urlRe := regexp.MustCompile(`https?://[^\s),\]]+`)
	for _, f := range findings {
		if f.sourceURLs == nil {
			continue
		}
		for _, u := range citedURLs(*f.sourceURLs, urlRe) {
			addHost(domains, u)
		}
	}
	return len(domains)
}

// citedURLs reads source_urls as a JSON array, else extracts the URLs in it
// with urlRe.
func citedURLs(sourceURLs string, urlRe *regexp.Regexp) []string {
	var urls []string
	if json.Unmarshal([]byte(sourceURLs), &urls) == nil {
		return urls
	}
	return urlRe.FindAllString(sourceURLs, -1)
}

// addHost adds a URL's host to domains when the URL parses with one.
func addHost(domains map[string]bool, u string) {
	if parsed, err := url.Parse(u); err == nil && parsed.Host != "" {
		domains[parsed.Host] = true
	}
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

func int64PtrFromAny(v any) *int64 {
	if v == nil {
		return nil
	}
	n := toInt64(v)
	return &n
}

func deref(p *int64) int64 {
	if p == nil {
		return -1 // sentinel for nil grouping
	}
	return *p
}
