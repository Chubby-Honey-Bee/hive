package mss

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ValidateGuaranteeDeps checks that a guarantee's depends_on_ids:
//  1. Is non-empty (traceability)
//  2. All referenced IDs exist
//  3. None of the referenced findings are labeled 'unknown' (no laundering),
//     directly or anywhere down the chain
//
// Lean4: `WriteCheckPasses` and `UpdateCheckPasses` (MSS/Preservation.lean)
// state checks 1-3 one hop deep, and the preservation theorems rest on them.
// The transitive walk, WalkDeps, is this function's and the audit's; Lean
// states nothing about it.
func ValidateGuaranteeDeps(db *sql.DB, findingID int64, depsJSON string) error {
	deps, err := guaranteeDeps(findingID, depsJSON)
	if err != nil {
		return err
	}
	if err := checkDirectDeps(db, findingID, deps); err != nil {
		return err
	}
	// Transitive closure check: walk the full dependency chain
	return transitiveUnknownCheck(db, findingID, deps)
}

// guaranteeDeps parses a guarantee's depends_on_ids, refusing a list that
// does not read or names nothing (check 1).
func guaranteeDeps(findingID int64, depsJSON string) ([]int64, error) {
	var deps []int64
	if err := json.Unmarshal([]byte(depsJSON), &deps); err != nil || len(deps) == 0 {
		return nil, &MSSError{
			FindingID: findingID,
			Label:     Guarantee,
			Reason:    "guarantee requires non-empty depends_on_ids",
			Err:       ErrMissingDeps,
		}
	}
	return deps, nil
}

// checkDirectDeps refuses a guarantee's dependency that does not exist
// (check 2), then one labelled unknown (check 3, immediate deps).
func checkDirectDeps(db *sql.DB, findingID int64, deps []int64) error {
	found, err := depLabels(db, deps)
	if err != nil {
		return err
	}
	if err := missingDep(findingID, Guarantee, deps, found); err != nil {
		return err
	}
	return unknownDep(findingID, deps, found)
}

// unknownDep is the laundering error for a dependency in found labelled
// unknown.
func unknownDep(findingID int64, deps []int64, found map[int64]string) error {
	for id, label := range found {
		if label == string(Unknown) {
			return &MSSError{
				FindingID:    findingID,
				DependsOnIDs: deps,
				Label:        Guarantee,
				Reason:       fmt.Sprintf("depends on unknown finding %d", id),
				Err:          ErrLaundering,
			}
		}
	}
	return nil
}

// ValidateDepsExist checks that every id in deps names an existing finding.
// Lean4: `WriteCheckPasses` and `UpdateCheckPasses` require it of every
// label, not only of guarantees; `write_preserves_deps_valid` and
// `write_preserves_no_laundering` rest on it.
func ValidateDepsExist(db *sql.DB, findingID int64, label Label, deps []int64) error {
	found, err := depLabels(db, deps)
	if err != nil {
		return err
	}
	return missingDep(findingID, label, deps, found)
}

// depLabels reads, in one query, the label of every finding in deps that
// exists.
func depLabels(db *sql.DB, deps []int64) (map[int64]string, error) {
	found := make(map[int64]string, len(deps))
	if len(deps) == 0 {
		return found, nil
	}
	in, args := inPlaceholders(deps)
	rows, err := db.Query(
		fmt.Sprintf("SELECT id, mss_label FROM findings WHERE id IN %s", in),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("query deps: %w", err)
	}
	defer rows.Close()
	if err := scanLabels(rows, found); err != nil {
		return nil, err
	}
	return found, nil
}

// scanLabels reads each (id, mss_label) row of rows into found.
func scanLabels(rows *sql.Rows, found map[int64]string) error {
	for rows.Next() {
		var id int64
		var label string
		if err := rows.Scan(&id, &label); err != nil {
			return fmt.Errorf("scan dep: %w", err)
		}
		found[id] = label
	}
	// Without this, a read that fails partway makes a dependency that exists
	// look missing, and the write is rejected for the wrong reason.
	if err := rows.Err(); err != nil {
		return fmt.Errorf("dependency lookup did not complete: %w", err)
	}
	return nil
}

// inPlaceholders is "(?,?,…)", one placeholder per id, with the ids as its
// arguments.
func inPlaceholders(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", args
}

// missingDep is the error for the first id in deps that depLabels did not
// find: one shape for every label.
func missingDep(findingID int64, label Label, deps []int64, found map[int64]string) error {
	for _, id := range deps {
		if _, ok := found[id]; !ok {
			return &MSSError{
				FindingID:    findingID,
				DependsOnIDs: deps,
				Label:        label,
				Reason:       fmt.Sprintf("depends_on_ids references non-existent finding %d", id),
				Err:          ErrMissingDeps,
			}
		}
	}
	return nil
}

// GuaranteesRestingOn returns, ascending, the ids of the guarantees that
// depend on findingID directly or through a chain of findings of any label:
// the guarantees that relabelling it unknown would launder. It walks the
// reverse of the graph the audit's BFS walks forward.
func GuaranteesRestingOn(db *sql.DB, findingID int64) ([]int64, error) {
	g, err := readDependents(db)
	if err != nil {
		return nil, err
	}
	return g.restingOn(findingID), nil
}

// dependentGraph is the reverse dependency graph: each finding's label, and
// the findings that depend on each finding directly.
type dependentGraph struct {
	labels     map[int64]string
	dependents map[int64][]int64
}

// readDependents reads the reverse dependency graph of every finding whose
// depends_on_ids reads.
func readDependents(db *sql.DB) (*dependentGraph, error) {
	rows, err := db.Query("SELECT id, mss_label, depends_on_ids FROM findings WHERE depends_on_ids IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("query dependents: %w", err)
	}
	defer rows.Close()
	return scanDependents(rows)
}

// scanDependents reads the graph from rows of (id, mss_label,
// depends_on_ids).
func scanDependents(rows *sql.Rows) (*dependentGraph, error) {
	g := &dependentGraph{labels: make(map[int64]string), dependents: make(map[int64][]int64)}
	for rows.Next() {
		if err := g.add(rows); err != nil {
			return nil, err
		}
	}
	// A partial read can miss the guarantee that rests on the finding, and
	// the relabel would then launder it in silence.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dependents read did not complete, so no-laundering is unproven: %w", err)
	}
	return g, nil
}

// add scans one finding from rows into the graph; one whose depends_on_ids
// does not read is left out.
func (g *dependentGraph) add(rows *sql.Rows) error {
	var id int64
	var label, depsJSON string
	if err := rows.Scan(&id, &label, &depsJSON); err != nil {
		return fmt.Errorf("scan dependent: %w", err)
	}
	var deps []int64
	if json.Unmarshal([]byte(depsJSON), &deps) != nil {
		return nil
	}
	g.labels[id] = label
	for _, d := range deps {
		g.dependents[d] = append(g.dependents[d], id)
	}
	return nil
}

// restingOn is, ascending, the guarantees reachable from findingID through
// its dependents, breadth first.
func (g *dependentGraph) restingOn(findingID int64) []int64 {
	visited := map[int64]bool{findingID: true}
	queue := append([]int64(nil), g.dependents[findingID]...)
	var resting []int64
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		if g.labels[id] == string(Guarantee) {
			resting = append(resting, id)
		}
		queue = append(queue, g.dependents[id]...)
	}
	slices.Sort(resting)
	return resting
}

// transitiveUnknownCheck walks the full dependency graph reachable from
// initialDeps and returns ErrLaundering if any finding it reaches is
// 'unknown'. The walk is WalkDeps, the one the MSS audit runs, so it follows
// every node's dependencies whatever its label: guarantee → assumption →
// unknown is refused here, at write, as the audit refuses it at the gate.
//
// The walk goes a level at a time, one query per level: the first level is
// initialDeps themselves, which checkDirectDeps has already found not
// unknown, and each next level the dependencies of the one before that the
// walk has not met.
func transitiveUnknownCheck(db *sql.DB, findingID int64, initialDeps []int64) error {
	return WalkDeps(initialDeps, levelReader(db), func(n DepNode) error {
		if n.Label != string(Unknown) {
			return nil
		}
		return &MSSError{
			FindingID:    findingID,
			DependsOnIDs: initialDeps,
			Label:        Guarantee,
			Reason:       fmt.Sprintf("transitively depends on unknown finding %d", n.ID),
			Err:          ErrLaundering,
		}
	})
}

// levelReader reads one level of the walk from db with one query, in the
// order the query returns the rows.
func levelReader(db *sql.DB) func(ids []int64) ([]DepNode, error) {
	return func(ids []int64) ([]DepNode, error) {
		in, args := inPlaceholders(ids)
		rows, err := db.Query(
			fmt.Sprintf("SELECT id, mss_label, depends_on_ids FROM findings WHERE id IN %s", in),
			args...,
		)
		if err != nil {
			return nil, fmt.Errorf("query transitive deps: %w", err)
		}
		defer rows.Close()
		return scanLevel(rows)
	}
}

// scanLevel reads one level's (id, mss_label, depends_on_ids) rows.
func scanLevel(rows *sql.Rows) ([]DepNode, error) {
	var level []DepNode
	for rows.Next() {
		var n DepNode
		var depsJSON sql.NullString
		if err := rows.Scan(&n.ID, &n.Label, &depsJSON); err != nil {
			return nil, fmt.Errorf("scan transitive dep: %w", err)
		}
		n.Deps = subDeps(depsJSON)
		level = append(level, n)
	}
	// A read that fails partway leaves rows.Next() returning false, which
	// is indistinguishable from a clean finish. Unchecked, the level is
	// silently short and this walk reports no laundering for a graph it
	// never finished reading — fail-open in the one check the gate exists
	// for. Surface it instead.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("transitive dep scan did not complete, so no-laundering is unproven: %w", err)
	}
	return level, nil
}

// subDeps is the dependencies a depends_on_ids column lists: none when it
// is NULL or empty or does not read.
func subDeps(depsJSON sql.NullString) []int64 {
	if !depsJSON.Valid || depsJSON.String == "" {
		return nil
	}
	var deps []int64
	if json.Unmarshal([]byte(depsJSON.String), &deps) != nil {
		return nil
	}
	return deps
}

// CheckCycle performs DFS cycle detection on the dependency graph
// with a proposed update applied. Returns an error naming the cycle if one
// would be introduced; label is the finding's label after the update.
//
// Lean4: M9 `update_preserves_acyclic` proves that an update keeps `Acyclic`
// when no new dependency reaches the finding in the old graph; this check
// enforces that premise for every label by searching the updated graph from
// the finding.
//
// Acyclicity proves nothing about laundering: g→a→u, with g a guarantee, a
// an assumption and u unknown, is acyclic and passes the one-hop check while
// laundering transitively. transitiveUnknownCheck, which walks through
// assumptions as well as guarantees, is what refuses that chain.
func CheckCycle(db *sql.DB, findingID int64, label Label, newDeps []int64) error {
	graph, err := readDepGraph(db)
	if err != nil {
		return err
	}
	// Apply proposed update
	graph[findingID] = newDeps

	// DFS from the updated node, keeping the path so the cycle can be named.
	f := &cycleFinder{graph: graph, visited: make(map[int64]bool), onPath: make(map[int64]int)}
	cycle := f.find(findingID)
	if cycle == nil {
		return nil
	}
	return &MSSError{
		FindingID:    findingID,
		DependsOnIDs: newDeps,
		Label:        label,
		Reason:       "would create dependency cycle " + nameCycle(cycle),
		Err:          ErrCycleDetected,
	}
}

// readDepGraph reads the dependency graph: each finding's dependencies,
// when it lists any.
func readDepGraph(db *sql.DB) (map[int64][]int64, error) {
	rows, err := db.Query("SELECT id, depends_on_ids FROM findings WHERE depends_on_ids IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("query dep graph: %w", err)
	}
	defer rows.Close()
	return scanDepGraph(rows)
}

// scanDepGraph reads the graph from rows of (id, depends_on_ids).
func scanDepGraph(rows *sql.Rows) (map[int64][]int64, error) {
	graph := make(map[int64][]int64)
	for rows.Next() {
		if id, deps, ok := scanEdges(rows); ok {
			graph[id] = deps
		}
	}
	// A partial graph cannot prove acyclicity: the edge that closes the cycle
	// may be the row the read never delivered.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dependency graph read did not complete, so acyclicity is unproven: %w", err)
	}
	return graph, nil
}

// scanEdges scans one finding's dependencies from rows; ok is false when
// the row does not scan, or its depends_on_ids does not read or lists none.
func scanEdges(rows *sql.Rows) (int64, []int64, bool) {
	var id int64
	var depsJSON string
	if err := rows.Scan(&id, &depsJSON); err != nil {
		return 0, nil, false
	}
	var deps []int64
	if json.Unmarshal([]byte(depsJSON), &deps) != nil || len(deps) == 0 {
		return 0, nil, false
	}
	return id, deps, true
}

// cycleFinder is a depth-first search of graph that keeps its path, so the
// cycle it finds can be named.
type cycleFinder struct {
	graph   map[int64][]int64
	visited map[int64]bool
	onPath  map[int64]int // node → its index in path
	path    []int64
}

// find is the first cycle the search meets from node: the path from the
// node that closes it, back to that node again. nil when there is none.
func (f *cycleFinder) find(node int64) []int64 {
	if i, ok := f.onPath[node]; ok {
		return append(append([]int64(nil), f.path[i:]...), node)
	}
	if f.visited[node] {
		return nil
	}
	f.visited[node] = true
	f.onPath[node] = len(f.path)
	f.path = append(f.path, node)
	if cycle := f.throughNeighbors(node); cycle != nil {
		return cycle
	}
	f.path = f.path[:len(f.path)-1]
	delete(f.onPath, node)
	return nil
}

// throughNeighbors is the first cycle the search meets from node's
// neighbors, in order.
func (f *cycleFinder) throughNeighbors(node int64) []int64 {
	for _, neighbor := range f.graph[node] {
		if cycle := f.find(neighbor); cycle != nil {
			return cycle
		}
	}
	return nil
}

// nameCycle is the ids of cycle joined by " → ".
func nameCycle(cycle []int64) string {
	ids := make([]string, len(cycle))
	for i, n := range cycle {
		ids[i] = strconv.FormatInt(n, 10)
	}
	return strings.Join(ids, " → ")
}
