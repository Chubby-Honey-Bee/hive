package foragers

import (
	"fmt"
	"strings"
)

// ValidateSwarm runs four checks across a chosen swarm:
//
//  1. Every forager's bonds reference a forager present in the swarm.
//     A bond pointing at a forager outside the swarm can't fire.
//  2. The bond graph (cites + contradicts edges) is acyclic.
//     Resonates bonds are excluded — they don't constrain dispatch
//     ordering; they are emitted as `resonates:` pairs that the runner
//     registers with the live ∇ quorum sensor, which records each
//     bonded-verdict convergence (forager_bonds + nabla signal).
//  3. No bond of any kind onto a dreamer in the swarm, and none declared
//     by one. Dreamers run AFTER queen (they ripen the closed Comb),
//     so their verdicts cannot be substituted into a lens prompt at
//     dispatch time. A bond like `contradicts: dreamer` from a lens
//     produces an unsatisfiable workflow edge (graph error:
//     "edge references unknown source: forager-<dreamer-name>"). A
//     dreamer's own cites/contradicts bond has no edge or digest to
//     render (the dreamer runs through internal/dreamer.Run, not a
//     prompt), so it is refused rather than dropped. A `resonates` pair
//     with a dreamer on either side can never fire: the dreamer's
//     verdict is always abstain, which the ∇ sensor never counts, yet
//     the lens prompt would promise a convergence. The philosophical
//     contrast belongs in persona prose.
//  4. No synthesizer-archetype forager. The generator always adds the
//     `queen` node, so a synthesizer named in the swarm would become a
//     second Queen — a `forager-queen` lens node beside the real one.
//
// Returns a single error listing every problem detected. Generation
// uses this; dispatch shortcuts past it because Filter already loads
// pre-normalised foragers.
func ValidateSwarm(swarm []Forager) error {
	var problems []string
	for _, check := range swarmChecks {
		problems = append(problems, check(swarm)...)
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("swarm validation failed:\n  - %s",
		strings.Join(problems, "\n  - "))
}

// swarmChecks are ValidateSwarm's checks, in the order their problems are
// listed.
var swarmChecks = []func(swarm []Forager) []string{
	missingBondTargets,
	bondCycleProblems,
	dreamerBondProblems,
	contractProblems,
	synthesizerProblems,
}

// missingBondTargets is check (1): bond targets present in the swarm —
// only required for bonds that produce a workflow edge or a prompt
// substitution. `resonates` is a runtime quorum-sensor registration only
// (no edge, no substitution), so it tolerates absent targets. `cites` and
// `contradicts` produce real edges + prompt substitutions, so the target
// must be present.
func missingBondTargets(swarm []Forager) []string {
	present := make(map[string]struct{}, len(swarm))
	for _, w := range swarm {
		present[w.Name] = struct{}{}
	}
	var problems []string
	for _, w := range swarm {
		problems = append(problems, missingTargets(w, present)...)
	}
	return problems
}

// missingTargets lists w's edge-making bonds whose target is not present.
func missingTargets(w Forager, present map[string]struct{}) []string {
	var problems []string
	for _, b := range w.Bonds {
		if b.Kind == BondResonates {
			continue // sensor-only; absent target is fine
		}
		if _, ok := present[b.To]; !ok {
			problems = append(problems, fmt.Sprintf(
				"forager %q has %s bond to %q, but %q is not in this swarm",
				w.Name, b.Kind, b.To, b.To,
			))
		}
	}
	return problems
}

// bondCycleProblems is check (2): cycle detection on the cites +
// contradicts subgraph.
func bondCycleProblems(swarm []Forager) []string {
	if cycle := findBondCycle(swarm); cycle != nil {
		return []string{fmt.Sprintf(
			"bond cycle detected: %s",
			strings.Join(cycle, " → "),
		)}
	}
	return nil
}

// dreamerBondProblems is check (3): no bond of any kind onto a dreamer or
// from one. Dreamers run after queen; their verdicts aren't available to
// lens prompts, and a dreamer takes no upstream digest. A dreamer's verdict
// is always abstain, so a resonates pair with one could never fire.
func dreamerBondProblems(swarm []Forager) []string {
	archetypes := make(map[string]string, len(swarm))
	for _, w := range swarm {
		archetypes[w.Name] = w.Archetype
	}
	var problems []string
	for _, w := range swarm {
		problems = append(problems, dreamerBondsOf(w, archetypes)...)
	}
	return problems
}

// dreamerBondsOf lists w's bonds that check (3) refuses: every bond of a
// dreamer, and any other forager's bonds onto one.
func dreamerBondsOf(w Forager, archetypes map[string]string) []string {
	if w.Archetype == ArchetypeDreamer {
		return ownDreamerBonds(w)
	}
	return bondsOntoDreamers(w, archetypes)
}

// ownDreamerBonds lists every bond a dreamer declares.
func ownDreamerBonds(w Forager) []string {
	var problems []string
	for _, b := range w.Bonds {
		problems = append(problems, fmt.Sprintf(
			"dreamer %q declares a %s bond to %q — a dreamer runs after "+
				"queen, takes no upstream verdict and always abstains, so "+
				"no bond of its own can render or fire; move the contrast "+
				"into the persona prose",
			w.Name, b.Kind, b.To,
		))
	}
	return problems
}

// bondsOntoDreamers lists w's bonds onto a dreamer in the swarm.
func bondsOntoDreamers(w Forager, archetypes map[string]string) []string {
	var problems []string
	for _, b := range w.Bonds {
		if archetypes[b.To] != ArchetypeDreamer {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"forager %q has %s bond to dreamer %q — dreamers run after "+
				"queen and always abstain, so a bond with one can neither "+
				"feed a lens prompt nor fire ∇; move the philosophical "+
				"contrast into the persona prose",
			w.Name, b.Kind, b.To,
		))
	}
	return problems
}

// contractProblems is check (4): an output contract that does not parse:
// the node would carry no schema, or the wrong one.
func contractProblems(swarm []Forager) []string {
	var problems []string
	for _, w := range swarm {
		if w.ContractErr != nil {
			problems = append(problems, fmt.Sprintf("forager %q: %v", w.Name, w.ContractErr))
		}
	}
	return problems
}

// synthesizerProblems is check (5): no synthesizer in the lens swarm: every
// swarm already runs Queen.
func synthesizerProblems(swarm []Forager) []string {
	var problems []string
	for _, w := range swarm {
		if w.Archetype == ArchetypeSynthesizer {
			problems = append(problems, fmt.Sprintf(
				"forager %q is a synthesizer; every swarm already runs Queen, so it cannot also be a lens",
				w.Name,
			))
		}
	}
	return problems
}

// bondColour is a forager's state in findBondCycle's depth-first search;
// the zero value is unvisited.
type bondColour int

const (
	bondGrey  bondColour = iota + 1 // on the stack
	bondBlack                       // fully explored
)

// findBondCycle returns the names along a cycle in the cites+
// contradicts edge graph, or nil if the graph is acyclic. Resonates
// bonds are excluded — they're a runtime correlation, not an edge.
//
// Standard DFS three-colour algorithm. White=0 unvisited,
// Grey=1 in stack, Black=2 fully explored. A back-edge to a Grey
// node is a cycle.
func findBondCycle(swarm []Forager) []string {
	s := &bondCycleSearch{adj: bondEdgeMap(swarm), col: make(map[string]bondColour, len(swarm))}
	for _, w := range swarm {
		if s.dfs(w.Name) {
			return s.found
		}
	}
	return nil
}

// bondEdgeMap maps each forager to the targets of its cites and
// contradicts bonds.
func bondEdgeMap(swarm []Forager) map[string][]string {
	adj := make(map[string][]string, len(swarm))
	for _, w := range swarm {
		for _, b := range w.Bonds {
			if b.Kind == BondResonates {
				continue
			}
			adj[w.Name] = append(adj[w.Name], b.To)
		}
	}
	return adj
}

// bondCycleSearch is findBondCycle's search state.
type bondCycleSearch struct {
	adj   map[string][]string
	col   map[string]bondColour
	stack []string
	found []string
}

// dfs visits node and reports whether a cycle is reachable from it.
func (s *bondCycleSearch) dfs(node string) bool {
	switch s.col[node] {
	case bondGrey:
		s.recordCycle(node)
		return true
	case bondBlack:
		return false
	}
	s.col[node] = bondGrey
	s.stack = append(s.stack, node)
	if s.cycleBeyond(node) {
		return true
	}
	s.stack = s.stack[:len(s.stack)-1]
	s.col[node] = bondBlack
	return false
}

// cycleBeyond reports whether a cycle is reachable through node's edges.
func (s *bondCycleSearch) cycleBeyond(node string) bool {
	for _, next := range s.adj[node] {
		if s.dfs(next) {
			return true
		}
	}
	return false
}

// recordCycle records the cycle a back-edge to the grey ancestor node
// closes: the stack from that ancestor onward, and the ancestor again.
func (s *bondCycleSearch) recordCycle(node string) {
	for i, n := range s.stack {
		if n == node {
			s.found = append(append([]string{}, s.stack[i:]...), node)
			return
		}
	}
}
