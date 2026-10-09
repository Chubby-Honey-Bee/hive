package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/citations"
	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// MSSAudit pass implementations + orchestrator. Each pass function is
// independent and returns its violation slice; RunAudit composes them
// into the full MSSAuditResult. Store.MSSAudit is a one-line delegation to
// it.

// RunAudit executes all six MSS criteria passes against rdb and returns
// the composed result. Store.MSSAudit runs it on the store's read pool.
func RunAudit(rdb *sql.DB) (*MSSAuditResult, error) {
	result := &MSSAuditResult{
		LaunderingViolations:  []map[string]any{},
		UntraceableGuarantees: []map[string]any{},
		DependencyCycles:      []map[string]any{},
		PartitionViolations:   []map[string]any{},
		RedundancyCandidates:  []map[string]any{},
		OACitationDowngrades:  []map[string]any{},
		LabelDistribution:     make(map[string]int),
	}
	for _, pass := range auditPasses {
		if err := pass.run(rdb, result); err != nil {
			return nil, fmt.Errorf("%s: %w", pass.name, err)
		}
	}
	result.Integrity = integrityOf(result)
	return result, nil
}

// auditPass is one RunAudit pass: the name its error carries, and how it
// fills the result.
type auditPass struct {
	name string
	run  func(rdb *sql.DB, result *MSSAuditResult) error
}

// auditPasses are RunAudit's passes, in the order they run.
var auditPasses = []auditPass{
	{"label distribution", func(rdb *sql.DB, result *MSSAuditResult) error {
		return auditLabelDistribution(rdb, result.LabelDistribution)
	}},
	{"untraceable", listPass(auditUntraceableGuarantees, func(r *MSSAuditResult) *[]map[string]any { return &r.UntraceableGuarantees })},
	{"laundering", listPass(auditLaundering, func(r *MSSAuditResult) *[]map[string]any { return &r.LaunderingViolations })},
	{"cycles", listPass(auditDependencyCycles, func(r *MSSAuditResult) *[]map[string]any { return &r.DependencyCycles })},
	{"partition", listPass(auditPartition, func(r *MSSAuditResult) *[]map[string]any { return &r.PartitionViolations })},
	{"redundancy", listPass(auditRedundancy, func(r *MSSAuditResult) *[]map[string]any { return &r.RedundancyCandidates })},
	// Open-access citation pass: surface guarantees whose only DOI
	// sources are paywalled / unverified. Soft-failure (warning, not
	// integrity FAIL) so a research run can close while flagging
	// claims that need OA replacements.
	{"oa citations", listPass(auditOACitations, func(r *MSSAuditResult) *[]map[string]any { return &r.OACitationDowngrades })},
}

// listPass is a pass that stores the list an audit returns in the result
// field it names.
func listPass(audit func(*sql.DB) ([]map[string]any, error), field func(*MSSAuditResult) *[]map[string]any) func(*sql.DB, *MSSAuditResult) error {
	return func(rdb *sql.DB, result *MSSAuditResult) error {
		rows, err := audit(rdb)
		if err != nil {
			return err
		}
		*field(result) = rows
		return nil
	}
}

// integrityOf is PASS only if there are zero hard violations. Redundancy
// candidates and OA-citation downgrades are warnings, not failures.
func integrityOf(result *MSSAuditResult) string {
	if hasHardViolations(result) {
		return "FAIL"
	}
	return "PASS"
}

// hasHardViolations reports any laundering, untraceable guarantee,
// dependency cycle or partition violation.
func hasHardViolations(result *MSSAuditResult) bool {
	return len(result.LaunderingViolations) > 0 ||
		len(result.UntraceableGuarantees) > 0 ||
		len(result.DependencyCycles) > 0 ||
		len(result.PartitionViolations) > 0
}

// truncateBytes is s cut to at most n bytes, backing off to a rune boundary
// so the cut splits no multi-byte character.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// auditOACitations finds guarantees whose source_urls cite at least
// one DOI, but where every cited DOI is paywalled or unverified
// according to citation_oa_cache. They are reported as a warning and
// nothing more: no code path mutates mss_label or emits a gap from this
// list, and there is no downgrade command. Re-labelling a claim is a
// deliberate act — `chb db-write update_finding`.
//
// Findings citing zero DOIs are out of scope (the policy applies only
// when scholarly literature is invoked); they keep their guarantee
// label and the no-laundering check covers them via the existing path.
func auditOACitations(rdb *sql.DB) ([]map[string]any, error) {
	guarantees, err := doiCitingGuarantees(rdb)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, g := range guarantees {
		if entry, flagged := g.oaDowngrade(rdb); flagged {
			out = append(out, entry)
		}
	}
	return out, nil
}

// citedGuarantee is a guarantee and the DOIs its source_urls cite.
type citedGuarantee struct {
	id      int64
	finding string
	dois    []string
}

// doiCitingGuarantees reads every guarantee with the DOIs it cites.
func doiCitingGuarantees(rdb *sql.DB) ([]citedGuarantee, error) {
	rows, err := rdb.Query(
		`SELECT id, finding, COALESCE(source_urls, '') FROM findings WHERE mss_label = 'guarantee'`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []citedGuarantee
	for rows.Next() {
		var g citedGuarantee
		var src string
		if err := rows.Scan(&g.id, &g.finding, &src); err != nil {
			return nil, err
		}
		g.dois = citations.ExtractDOIs(src)
		out = append(out, g)
	}
	return out, rows.Err()
}

// oaDowngrade is the warning for a guarantee that cites DOIs, none of them
// verified open access.
func (g citedGuarantee) oaDowngrade(rdb *sql.DB) (map[string]any, bool) {
	if len(g.dois) == 0 {
		return nil, false
	}
	tally := tallyDOIs(rdb, g.dois)
	if tally.anyOA {
		return nil, false
	}
	return map[string]any{
		"guarantee_id":   g.id,
		"finding":        truncateBytes(g.finding, 80),
		"dois_total":     len(g.dois),
		"dois_paywalled": tally.paywalled,
		"dois_unverif":   tally.unverified,
	}, true
}

// oaStatus is what citation_oa_cache records of a DOI.
type oaStatus int

const (
	oaUnverified oaStatus = iota // no cache row, or one with an error
	oaPaywalled
	oaOpen
)

// doiTally counts a guarantee's DOIs by status, up to the first open one.
type doiTally struct {
	anyOA                 bool
	paywalled, unverified int
}

// tallyDOIs checks the cache for each DOI, stopping at the first verified
// open-access one.
func tallyDOIs(rdb *sql.DB, dois []string) doiTally {
	var t doiTally
	for _, doi := range dois {
		if t.add(cachedOAStatus(rdb, doi)) {
			break
		}
	}
	return t
}

// add counts one DOI and reports whether it is open access.
func (t *doiTally) add(status oaStatus) bool {
	switch status {
	case oaOpen:
		t.anyOA = true
		return true
	case oaUnverified:
		t.unverified++
	default:
		t.paywalled++
	}
	return false
}

// cachedOAStatus is the status the Unpaywall cache records for a DOI.
func cachedOAStatus(rdb *sql.DB, doi string) oaStatus {
	var isOA int
	var errStr string
	err := rdb.QueryRow(
		`SELECT is_oa, COALESCE(error,'') FROM citation_oa_cache
			 WHERE doi = ? AND source = 'unpaywall'`, doi).Scan(&isOA, &errStr)
	switch {
	case err != nil || errStr != "":
		return oaUnverified
	case isOA == 1:
		return oaOpen
	}
	return oaPaywalled
}

// auditLabelDistribution populates the label-distribution map. Cheap
// O(1) groupby — kept first because the rest of the audit reads it
// implicitly when reporters truncate display strings.
func auditLabelDistribution(rdb *sql.DB, dest map[string]int) error {
	rows, err := rdb.Query("SELECT mss_label, COUNT(*) FROM findings GROUP BY mss_label")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		var cnt int
		if err := rows.Scan(&label, &cnt); err != nil {
			return err
		}
		dest[label] = cnt
	}
	return rows.Err()
}

// auditUntraceableGuarantees finds guarantees whose depends_on_ids is
// NULL / 'null' / '[]' / ” — i.e., a guarantee with no proof chain at all.
// Schema CHECK alone cannot detect this; it requires NULL / empty-array / empty-string
// inspection. The empty string check is defensive against external SQLite access.
func auditUntraceableGuarantees(rdb *sql.DB) ([]map[string]any, error) {
	const q = `SELECT id, finding, agent FROM findings
		WHERE mss_label='guarantee'
		  AND (depends_on_ids IS NULL OR depends_on_ids='null' OR depends_on_ids='[]' OR depends_on_ids='')`
	rows, err := rdb.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		entry, err := untraceableEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// untraceableEntry reads one untraceable guarantee.
func untraceableEntry(rows *sql.Rows) (map[string]any, error) {
	var id int64
	var finding, agent string
	if err := rows.Scan(&id, &finding, &agent); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "finding": truncateBytes(finding, 100), "agent": agent}, nil
}

// launderNode is one finding as the laundering pass walks it: its label,
// its text and its dependencies.
type launderNode struct {
	label   string
	finding string
	depIDs  []int64
}

// auditLaundering finds (guarantee, unknown) dependency pairs — the
// canonical MSS no-laundering violation. Transitive detection: for each
// guarantee, walk the full dependency chain (mss.WalkDeps, the walk the
// write-time check runs) so that guarantee→guarantee→unknown chains are
// caught in addition to direct guarantee→unknown links.
func auditLaundering(rdb *sql.DB) ([]map[string]any, error) {
	nodes, err := loadLaunderNodes(rdb)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for id, ni := range nodes {
		out = append(out, launderedUnknowns(nodes, id, ni)...)
	}
	return out, nil
}

// loadLaunderNodes loads every finding once, by id, for the walk to look up.
func loadLaunderNodes(rdb *sql.DB) (map[int64]*launderNode, error) {
	rows, err := rdb.Query(
		"SELECT id, mss_label, finding, COALESCE(depends_on_ids,'[]') FROM findings",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make(map[int64]*launderNode)
	for rows.Next() {
		id, ni, err := scanLaunderNode(rows)
		if err != nil {
			return nil, err
		}
		nodes[id] = ni
	}
	return nodes, rows.Err()
}

// scanLaunderNode reads one finding; malformed dependencies read as none.
func scanLaunderNode(rows *sql.Rows) (int64, *launderNode, error) {
	var id int64
	var label, finding, depsJSON string
	if err := rows.Scan(&id, &label, &finding, &depsJSON); err != nil {
		return 0, nil, err
	}
	ni := &launderNode{label: label, finding: finding}
	_ = json.Unmarshal([]byte(depsJSON), &ni.depIDs)
	return id, ni, nil
}

// guaranteeWithDeps reports whether a finding is a guarantee that names
// dependencies.
func guaranteeWithDeps(ni *launderNode) bool {
	return ni.label == "guarantee" && len(ni.depIDs) > 0
}

// launderedUnknowns reports every unknown a guarantee's dependency closure
// reaches, in the order mss.WalkDeps reaches them.
func launderedUnknowns(nodes map[int64]*launderNode, id int64, ni *launderNode) []map[string]any {
	if !guaranteeWithDeps(ni) {
		return nil
	}
	var out []map[string]any
	readLevel := func(ids []int64) ([]mss.DepNode, error) { return launderLevel(nodes, ids), nil }
	// Neither the read nor the visit can fail, so the walk cannot.
	_ = mss.WalkDeps(ni.depIDs, readLevel, func(dep mss.DepNode) error {
		if dep.Label == "unknown" {
			out = append(out, map[string]any{
				"guarantee_id":          id,
				"guarantee":             truncateBytes(ni.finding, 80),
				"depends_on_unknown_id": dep.ID,
				"unknown":               truncateBytes(nodes[dep.ID].finding, 80),
			})
		}
		return nil
	})
	return out
}

// launderLevel is one level of the walk: the findings among ids that exist,
// in the order of ids.
func launderLevel(nodes map[int64]*launderNode, ids []int64) []mss.DepNode {
	level := make([]mss.DepNode, 0, len(ids))
	for _, id := range ids {
		if n, ok := nodes[id]; ok {
			level = append(level, mss.DepNode{ID: id, Label: n.label, Deps: n.depIDs})
		}
	}
	return level
}

// auditDependencyCycles runs DFS over the dependency graph and emits
// one entry per cycle found. The DFS state is captured in a private
// cycleScanner so the recursive helper has no globals.
func auditDependencyCycles(rdb *sql.DB) ([]map[string]any, error) {
	graph, err := loadDependencyGraph(rdb)
	if err != nil {
		return nil, err
	}
	scanner := newCycleScanner(graph)
	for id := range graph {
		scanner.visit(id)
	}
	if scanner.cycles == nil {
		return []map[string]any{}, nil
	}
	return scanner.cycles, nil
}

// loadDependencyGraph builds the id → [dep_ids] map from the findings table.
// Skips rows whose depends_on_ids is malformed: the audit is best effort
// over a corrupt row, as the cascade is.
func loadDependencyGraph(rdb *sql.DB) (map[int64][]int64, error) {
	rows, err := rdb.Query("SELECT id, depends_on_ids FROM findings WHERE depends_on_ids IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64][]int64)
	for rows.Next() {
		if err := addDependencyRow(rows, out); err != nil {
			return nil, err
		}
	}
	return out, rows.Err()
}

// addDependencyRow reads one finding's dependencies into the graph, when
// they parse and name any.
func addDependencyRow(rows *sql.Rows, graph map[int64][]int64) error {
	var id int64
	var depsJSON string
	if err := rows.Scan(&id, &depsJSON); err != nil {
		return err
	}
	var deps []int64
	if json.Unmarshal([]byte(depsJSON), &deps) == nil && len(deps) > 0 {
		graph[id] = deps
	}
	return nil
}

// cycleScanner threads DFS state. white/gray/black coloring is the
// standard cycle-detection technique; gray-on-stack ⇒ back-edge ⇒ cycle.
type cycleScanner struct {
	graph  map[int64][]int64
	color  map[int64]int
	path   []int64
	cycles []map[string]any
}

const (
	cycleWhite = 0
	cycleGray  = 1
	cycleBlack = 2
)

func newCycleScanner(graph map[int64][]int64) *cycleScanner {
	color := make(map[int64]int, len(graph))
	for id := range graph {
		color[id] = cycleWhite
	}
	return &cycleScanner{graph: graph, color: color}
}

func (s *cycleScanner) visit(node int64) {
	if s.color[node] != cycleWhite {
		return
	}
	s.color[node] = cycleGray
	s.path = append(s.path, node)
	for _, neighbor := range s.graph[node] {
		s.follow(neighbor)
	}
	s.path = s.path[:len(s.path)-1]
	s.color[node] = cycleBlack
}

// follow takes the edge to neighbor: a gray neighbor closes a cycle, a
// white one is visited, and one outside the graph is left alone.
func (s *cycleScanner) follow(neighbor int64) {
	color, present := s.color[neighbor]
	if !present {
		return
	}
	switch color {
	case cycleGray:
		s.recordCycle(neighbor)
	case cycleWhite:
		s.visit(neighbor)
	}
}

func (s *cycleScanner) recordCycle(backEdgeTarget int64) {
	start := 0
	for i, n := range s.path {
		if n == backEdgeTarget {
			start = i
			break
		}
	}
	cycle := make([]int64, len(s.path[start:]))
	copy(cycle, s.path[start:])
	cycle = append(cycle, backEdgeTarget)
	s.cycles = append(s.cycles, map[string]any{
		"cycle":       cycle,
		"description": "Circular dependency: " + formatCyclePath(cycle),
	})
}

func formatCyclePath(cycle []int64) string {
	out := ""
	for i, n := range cycle {
		if i > 0 {
			out += " → "
		}
		out += fmt.Sprintf("%d", n)
	}
	return out
}

// auditPartition delegates to the mss package's PartitionAudit and
// formats the result for the JSON response.
func auditPartition(rdb *sql.DB) ([]map[string]any, error) {
	pviols, err := mss.PartitionAudit(rdb)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(pviols))
	for _, v := range pviols {
		out = append(out, map[string]any{
			"id":      v.FindingID,
			"label":   v.Label,
			"finding": truncateBytes(v.FindingText, 80),
		})
	}
	return out, nil
}

// auditRedundancy delegates to mss.IndependenceAudit and formats output.
func auditRedundancy(rdb *sql.DB) ([]map[string]any, error) {
	rcands, err := mss.IndependenceAudit(rdb, independenceThreshold)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rcands))
	for _, c := range rcands {
		out = append(out, map[string]any{
			"finding_a_id": c.IDA,
			"finding_b_id": c.IDB,
			"similarity":   c.Similarity,
			"finding_a":    truncateBytes(c.FindingA, 80),
			"finding_b":    truncateBytes(c.FindingB, 80),
		})
	}
	return out, nil
}

// MSSAuditResult is what the integrity check on MSS labels finds
// (RunAudit, Store.MSSAudit).
//
// Audited criteria (per chronomancy.io's MSS framework):
//   - Partition: PartitionViolations (rows whose label isn't one of four)
//   - Traceability: UntraceableGuarantees + DependencyCycles
//   - No-laundering: LaunderingViolations
//   - Independence: RedundancyCandidates (heuristic — same-coord
//     assumptions with high text similarity)
//
// All four are checked.
type MSSAuditResult struct {
	LaunderingViolations  []map[string]any `json:"laundering_violations"`
	UntraceableGuarantees []map[string]any `json:"untraceable_guarantees"`
	DependencyCycles      []map[string]any `json:"dependency_cycles"`
	PartitionViolations   []map[string]any `json:"partition_violations"`
	RedundancyCandidates  []map[string]any `json:"redundancy_candidates"`
	// OACitationDowngrades lists guarantees whose only DOI sources resolve
	// to paywalled / unverified per the citation_oa_cache. Reported as a
	// *warning* (it does not flip Integrity to FAIL) so a run can close
	// cleanly while researchers chase OA replacements.
	//
	// Nothing downgrades them, whatever the name says: the list is
	// candidates, not actions. No code path mutates mss_label from this, and
	// re-labelling a claim is a deliberate act, not a side effect of an
	// audit. Act on it with `chb db-write update_finding`.
	OACitationDowngrades []map[string]any `json:"oa_citation_downgrades"`
	LabelDistribution    map[string]int   `json:"label_distribution"`
	Integrity            string           `json:"integrity"`
}

// independenceThreshold is the Jaccard similarity above which two
// same-coordinate assumptions are flagged as a redundancy candidate.
// 0.7 chosen empirically: below this, agents disagree on phrasing more
// than they agree on semantics.
const independenceThreshold = 0.7

// MSSAudit runs RunAudit on the store's read pool. Every audit outside this
// package goes through it.
func (s *Store) MSSAudit() (*MSSAuditResult, error) {
	return RunAudit(s.ReadDB)
}
