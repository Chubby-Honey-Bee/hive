// Alarm-pheromone cascade engine. The domain logic (the BFS, its terminal
// conditions, the gap descriptions) lives here, and the SQL primitives sit
// behind the CascadeStore interface, so the algorithm is testable in
// isolation.

package mss

import (
	"fmt"
	"unicode/utf8"
)

// DependencyEdge is the minimum information the cascade BFS needs about
// each row that has a non-empty depends_on_ids list. Mirrors the type
// in internal/db so callers can pass the value through without copying
// fields by hand.
type DependencyEdge struct {
	ID      int64
	Label   string
	D1, D2  *int
	D3, D4  *int
	Wave    int
	Finding string
	Deps    []int64
}

// CascadeStore is the narrow storage contract the cascade engine needs.
// Three orthogonal capabilities: load the graph, revert a row, record
// the resulting gap+signal. Implementing this interface is what makes a
// store cascade-capable; *db.Store satisfies it (compile-time assertion
// in internal/db).
type CascadeStore interface {
	LoadDependencyEdges() ([]DependencyEdge, error)
	RevertFindingToUnknown(findingID int64) error
	RecordCascadeGap(wave int, description string, d1, d2, d3, d4 *int) error
	EmitAlarmSignal(sourceID int64, d1, d2, d3, d4 *int, payload map[string]any, wave int) error
}

// RunCascade implements the alarm-pheromone cascade: BFS through the
// reverse dependency graph, reverting guarantees that transitively
// depend on a contradicted finding. Aborts on any write failure and
// returns the set of findings reverted so far together with the error.
//
// Encountering an `unknown` along the way doesn't revert it (already
// in the terminal state) but does propagate the cascade past it —
// dependents of an unknown still need re-investigation.
func RunCascade(store CascadeStore, triggerID int64) ([]int64, error) {
	edges, err := store.LoadDependencyEdges()
	if err != nil {
		return nil, err
	}
	c := &cascade{
		store:       store,
		triggerID:   triggerID,
		reverseDeps: buildReverseGraph(edges),
		queue:       []int64{triggerID},
		visited:     make(map[int64]bool),
	}
	return c.run()
}

// cascade is one run of the alarm-pheromone cascade: the reverse graph it
// walks, its BFS queue and visited set, and the findings reverted so far.
type cascade struct {
	store       CascadeStore
	triggerID   int64
	reverseDeps map[int64][]DependencyEdge
	queue       []int64
	visited     map[int64]bool
	reverted    []int64
}

// run walks the queue breadth first, and returns the findings reverted so
// far with the first write failure.
func (c *cascade) run() ([]int64, error) {
	for len(c.queue) > 0 {
		current := c.queue[0]
		c.queue = c.queue[1:]
		if c.visited[current] {
			continue
		}
		c.visited[current] = true
		if err := c.visitDependents(current); err != nil {
			return c.reverted, err
		}
	}
	return c.reverted, nil
}

// visitDependents visits each finding that depends on current directly.
func (c *cascade) visitDependents(current int64) error {
	for _, dep := range c.reverseDeps[current] {
		if err := c.visit(dep); err != nil {
			return err
		}
	}
	return nil
}

// visit reverts dep unless it is visited or unknown already, and queues it
// unless it is visited.
func (c *cascade) visit(dep DependencyEdge) error {
	if c.visited[dep.ID] {
		return nil
	}
	if dep.Label == string(Unknown) {
		// Already retracted — propagate the cascade through it
		// without re-reverting.
		c.queue = append(c.queue, dep.ID)
		return nil
	}
	if err := revertOne(c.store, dep, c.triggerID); err != nil {
		return err
	}
	c.reverted = append(c.reverted, dep.ID)
	c.queue = append(c.queue, dep.ID)
	return nil
}

// buildReverseGraph inverts the forward dependency edges into a map
// keyed by dependency target — i.e., "if finding X is contradicted,
// who depends on it transitively?".
func buildReverseGraph(edges []DependencyEdge) map[int64][]DependencyEdge {
	out := make(map[int64][]DependencyEdge, len(edges))
	for _, e := range edges {
		for _, depID := range e.Deps {
			out[depID] = append(out[depID], e)
		}
	}
	return out
}

// revertOne performs the three writes for a single cascaded revert.
// Order matters: revert label first so concurrent readers see the
// retracted state before they see the gap or signal that explains why.
func revertOne(store CascadeStore, dep DependencyEdge, triggerID int64) error {
	if err := store.RevertFindingToUnknown(dep.ID); err != nil {
		return err
	}
	if err := store.RecordCascadeGap(dep.Wave, cascadeGapDescription(dep, triggerID), dep.D1, dep.D2, dep.D3, dep.D4); err != nil {
		return err
	}
	payload := map[string]any{
		"reverted_from":      dep.Label,
		"trigger_finding_id": triggerID,
	}
	return store.EmitAlarmSignal(dep.ID, dep.D1, dep.D2, dep.D3, dep.D4, payload, dep.Wave)
}

// cascadeGapDescription is the description of the gap a cascaded revert of
// dep opens, with the first 80 bytes of its finding.
func cascadeGapDescription(dep DependencyEdge, triggerID int64) string {
	preview := clip(dep.Finding, 80)
	return fmt.Sprintf(
		"Alarm cascade: reverted %s (finding %d) due to contradicted dependency (finding %d): %s",
		dep.Label, dep.ID, triggerID, preview,
	)
}

// clip is s cut to at most n bytes, backing off to a rune boundary so the
// cut splits no multi-byte character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
