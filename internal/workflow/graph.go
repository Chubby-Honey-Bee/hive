package workflow

import (
	"fmt"
	"maps"
	"slices"
	"sort"
)

// Edge is a directed edge with an optional condition.
type Edge struct {
	Target    string
	Condition string
}

// BuildGraph builds adjacency structures from a workflow definition.
// Returns incoming (predecessors) and outgoing (successors with conditions).
// A decision node also gets an edge to each branch target that names a node
// and that no explicit edge reaches: its true branch on its condition, its
// false branch on the negated condition.
func BuildGraph(defn map[string]any) (incoming map[string]map[string]bool, outgoing map[string][]Edge, err error) {
	nodes, _ := defn["nodes"].(map[string]any)
	edges, _ := defn["edges"].([]any)
	g := newGraph(nodes)
	if err = g.addEdges(edges); err != nil {
		return nil, nil, err
	}
	for name, raw := range nodes {
		g.addDecisionEdges(name, raw)
	}
	return g.incoming, g.outgoing, nil
}

// graph is the adjacency BuildGraph builds over a definition's nodes.
type graph struct {
	nodes    map[string]any
	incoming map[string]map[string]bool
	outgoing map[string][]Edge
}

// newGraph is a graph with every node and no edge.
func newGraph(nodes map[string]any) graph {
	g := graph{nodes: nodes, incoming: make(map[string]map[string]bool), outgoing: make(map[string][]Edge)}
	for name := range nodes {
		g.incoming[name] = make(map[string]bool)
		g.outgoing[name] = nil
	}
	return g
}

// addEdges adds the definition's explicit edges. An entry that is not a
// mapping is skipped; an edge naming a node that does not exist is an error.
func (g graph) addEdges(edges []any) error {
	for _, e := range edges {
		edge, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if err := g.addEdge(edge); err != nil {
			return err
		}
	}
	return nil
}

func (g graph) addEdge(edge map[string]any) error {
	src, _ := edge["from"].(string)
	dst, _ := edge["to"].(string)
	cond, _ := edge["condition"].(string)
	if _, ok := g.nodes[src]; !ok {
		return fmt.Errorf("edge references unknown source: %q", src)
	}
	if _, ok := g.nodes[dst]; !ok {
		return fmt.Errorf("edge references unknown target: %q", dst)
	}
	g.link(src, dst, cond)
	return nil
}

func (g graph) link(src, dst, cond string) {
	g.outgoing[src] = append(g.outgoing[src], Edge{Target: dst, Condition: cond})
	g.incoming[dst][src] = true
}

// addDecisionEdges adds a decision node's implicit edges to its branches.
// The targets explicit edges reach are read once, before either branch is
// added.
func (g graph) addDecisionEdges(name string, raw any) {
	node, _ := raw.(map[string]any)
	if t, _ := node["type"].(string); t != "decision" {
		return
	}
	cond, _ := node["condition"].(string)
	explicit := g.targetsOf(name)
	g.addBranch(name, node["true_edge"], cond, explicit)
	g.addBranch(name, node["false_edge"], negation(cond), explicit)
}

// addBranch adds a decision's edge to a branch target, unless the branch
// names no node or an explicit edge already reaches it.
func (g graph) addBranch(decision string, branch any, cond string, explicit map[string]bool) {
	target, _ := branch.(string)
	if _, isNode := g.nodes[target]; target == "" || explicit[target] || !isNode {
		return
	}
	g.link(decision, target, cond)
}

func (g graph) targetsOf(name string) map[string]bool {
	targets := make(map[string]bool)
	for _, e := range g.outgoing[name] {
		targets[e.Target] = true
	}
	return targets
}

// negation is the condition of a decision's false branch: its condition
// negated, or false when it has none.
func negation(cond string) string {
	if cond == "" {
		return "false"
	}
	return "!(" + cond + ")"
}

func findStartNodes(incoming map[string]map[string]bool) []string {
	var starts []string
	for name, preds := range incoming {
		if len(preds) == 0 {
			starts = append(starts, name)
		}
	}
	return starts
}

// The colours of a node in a depth-first walk: not reached yet, on the
// walk's stack, finished.
const (
	white = iota
	grey
	black
)

// backEdges returns the edges that close a loop: in a depth-first walk from
// the start nodes (then any node not yet reached), an edge into a node still
// on the walk's stack. Nodes and edges are visited in sorted order, so the
// same workflow always yields the same set.
func backEdges(incoming map[string]map[string]bool, outgoing map[string][]Edge) map[[2]string]bool {
	w := backEdgeWalk{outgoing: outgoing, colour: make(map[string]int, len(outgoing)), back: map[[2]string]bool{}}
	starts := findStartNodes(incoming)
	sort.Strings(starts)
	for _, n := range append(starts, slices.Sorted(maps.Keys(outgoing))...) {
		if w.colour[n] == white {
			w.visit(n)
		}
	}
	return w.back
}

// backEdgeWalk is the depth-first walk backEdges makes, and the back edges
// it has found.
type backEdgeWalk struct {
	outgoing map[string][]Edge
	colour   map[string]int
	back     map[[2]string]bool
}

func (w backEdgeWalk) visit(n string) {
	w.colour[n] = grey
	for _, t := range sortedTargets(w.outgoing[n]) {
		switch w.colour[t] {
		case white:
			w.visit(t)
		case grey:
			w.back[[2]string{n, t}] = true
		}
	}
	w.colour[n] = black
}

func sortedTargets(edges []Edge) []string {
	targets := make([]string, 0, len(edges))
	for _, e := range edges {
		targets = append(targets, e.Target)
	}
	sort.Strings(targets)
	return targets
}

// withoutBackEdges copies incoming with every back-edge predecessor removed.
func withoutBackEdges(incoming map[string]map[string]bool, back map[[2]string]bool) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(incoming))
	for n, preds := range incoming {
		out[n] = make(map[string]bool, len(preds))
		for p := range preds {
			if !back[[2]string{p, n}] {
				out[n][p] = true
			}
		}
	}
	return out
}

// loopSet maps a back edge (the decision closing a loop, the loop's head) to
// the loop's body: every node on a forward path from the head to the
// decision, both included, sorted.
type loopSet map[[2]string][]string

func loopBodies(outgoing map[string][]Edge, back map[[2]string]bool) loopSet {
	reverse := map[string][]string{}
	for n := range outgoing {
		for _, t := range forwardTargets(n, outgoing, back) {
			reverse[t] = append(reverse[t], n)
		}
	}
	loops := loopSet{}
	for edge := range back {
		loops[edge] = loopBody(edge, outgoing, back, reverse)
	}
	return loops
}

// loopBody is the body of the loop a back edge closes: the nodes reachable
// from its head over forward edges that reach its decision over forward
// edges, sorted.
func loopBody(edge [2]string, outgoing map[string][]Edge, back map[[2]string]bool, reverse map[string][]string) []string {
	decision, head := edge[0], edge[1]
	fromHead := forwardReach(head, outgoing, back)
	inBody := map[string]bool{}
	stack := []string{decision}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if inBody[n] || !fromHead[n] {
			continue
		}
		inBody[n] = true
		stack = append(stack, reverse[n]...)
	}
	return slices.Sorted(maps.Keys(inBody))
}

// forwardReach is every node reachable from head over forward edges, head
// included.
func forwardReach(head string, outgoing map[string][]Edge, back map[[2]string]bool) map[string]bool {
	seen := map[string]bool{}
	stack := []string{head}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		seen[n] = true
		stack = append(stack, forwardTargets(n, outgoing, back)...)
	}
	return seen
}

// forwardTargets is the targets of n's edges that are not back edges, in
// edge order.
func forwardTargets(n string, outgoing map[string][]Edge, back map[[2]string]bool) []string {
	var targets []string
	for _, e := range outgoing[n] {
		if !back[[2]string{n, e.Target}] {
			targets = append(targets, e.Target)
		}
	}
	return targets
}

// findCycle returns one cycle on which no node is cut, as a node path, or
// nil. A cut node is walked into but not out of.
//
// Not every cycle is a defect: `check → retry → check`, where check is a
// decision, is the engine's retry loop and leaves by its other edge. A cycle
// with no decision on it has no exit at all: it would initialise and run
// until the iteration cap with nothing to show. So with the decision nodes
// cut, whatever loop survives is one nothing can break. With every other
// node cut, what survives is a loop made only of decisions, which runs no
// node.
//
// Plain white/grey/black DFS over the reduced graph: a node reached while
// still grey is a back edge, and the grey stack from it is the cycle. Roots
// are walked in sorted order so the reported cycle is the same one every run
// — a validator that names a different node each time is hard to act on.
func findCycle(outgoing map[string][]Edge, cut map[string]bool) []string {
	w := &cycleWalk{outgoing: outgoing, cut: cut, colour: make(map[string]int, len(outgoing))}
	for _, n := range slices.Sorted(maps.Keys(outgoing)) {
		if w.colour[n] != white {
			continue
		}
		if cyc := w.visit(n); cyc != nil {
			return cyc
		}
	}
	return nil
}

// cycleWalk is the depth-first walk findCycle makes: each node's colour and
// the stack of grey nodes.
type cycleWalk struct {
	outgoing map[string][]Edge
	cut      map[string]bool
	colour   map[string]int
	stack    []string
}

// visit walks from n and returns the first cycle it closes, or nil.
func (w *cycleWalk) visit(n string) []string {
	w.colour[n] = grey
	w.stack = append(w.stack, n)
	for _, e := range w.edgesFrom(n) {
		if cyc := w.follow(e.Target); cyc != nil {
			return cyc
		}
	}
	w.stack = w.stack[:len(w.stack)-1]
	w.colour[n] = black
	return nil
}

// follow takes an edge into t: a node not reached yet is walked, and a node
// on the stack closes a cycle.
func (w *cycleWalk) follow(t string) []string {
	switch w.colour[t] {
	case white:
		return w.visit(t)
	case grey:
		return w.cycleTo(t)
	}
	return nil
}

// cycleTo is the cycle an edge into t, a node on the stack, closes: the
// stack from t, then t again.
func (w *cycleWalk) cycleTo(t string) []string {
	for i, s := range w.stack {
		if s == t {
			return append(append([]string(nil), w.stack[i:]...), t)
		}
	}
	return []string{t, t}
}

// edgesFrom is n's edges sorted by target. A cut node is treated as having
// no outgoing edges, so no cycle passes through it.
func (w *cycleWalk) edgesFrom(n string) []Edge {
	if w.cut[n] {
		return nil
	}
	edges := append([]Edge(nil), w.outgoing[n]...)
	sort.Slice(edges, func(i, j int) bool { return edges[i].Target < edges[j].Target })
	return edges
}
