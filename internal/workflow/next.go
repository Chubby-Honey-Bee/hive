package workflow

import (
	"errors"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/schema"
)

// GetNextNodes returns the next dispatchable nodes for a workflow run.
func GetNextNodes(repo Store, runID int64) ([]DispatchNode, error) {
	p, err := openPass(repo, runID)
	if p == nil {
		return nil, err
	}
	ready, err := p.readyNodes()
	if err != nil {
		return nil, err
	}
	if len(ready) == 0 {
		return nil, finalizeIfTerminal(repo, runID, p.defn, p.incoming, p.nodeStates)
	}
	return p.dispatch(ready)
}

// GetNextNodesManual is GetNextNodes for a caller that hands the nodes to a
// person or a script (`chb workflow next`) instead of dispatching them. A ready human_review node is parked with PauseForHuman,
// as the runner parks it, rather than returned as running, so
// `chb workflow resume` can answer it.
func GetNextNodesManual(repo Store, runID int64) ([]DispatchNode, error) {
	ready, err := GetNextNodes(repo, runID)
	if err != nil {
		return nil, err
	}
	if err := parkHumanReviews(repo, runID, ready); err != nil {
		return nil, err
	}
	return withoutHumanReviews(ready), nil
}

// parkHumanReviews parks each human_review node of ready, in order, and
// stops at the first that cannot be parked.
func parkHumanReviews(repo Store, runID int64, ready []DispatchNode) error {
	for _, d := range ready {
		if !isHumanReview(d) {
			continue
		}
		if err := PauseForHuman(repo, runID, d.Node); err != nil {
			return err
		}
	}
	return nil
}

// withoutHumanReviews is ready less its human_review nodes; never nil.
func withoutHumanReviews(ready []DispatchNode) []DispatchNode {
	out := make([]DispatchNode, 0, len(ready))
	for _, d := range ready {
		if !isHumanReview(d) {
			out = append(out, d)
		}
	}
	return out
}

func isHumanReview(d DispatchNode) bool {
	return d.Type == "human_review"
}

// runView is what the engine reads of a run while it works out which nodes
// come next: its definition and state, its node statuses, and its graph over
// forward edges with the loops its back edges close.
type runView struct {
	repo       Store
	runID      int64
	defn       map[string]any
	state      map[string]any
	nodeStates map[string]map[string]any
	incoming   map[string]map[string]bool
	outgoing   map[string][]Edge
	loops      loopSet
}

// nodes is the definition's node map.
func (v *runView) nodes() map[string]any {
	nodes, _ := v.defn["nodes"].(map[string]any)
	return nodes
}

// pass is one GetNextNodes call over a running run: the run as the engine
// reads it, and the output schemas the nodes it hands out carry.
type pass struct {
	runView
	schemas map[string]*schema.Schema
}

// openPass reads a run for GetNextNodes. It returns no pass, and no error,
// for a run that is not running.
func openPass(repo Store, runID int64) (*pass, error) {
	run, err := repo.GetWorkflowRun(runID)
	if err != nil {
		return nil, fmt.Errorf("no workflow run %d: %w", runID, err)
	}
	if run.Status != "running" {
		return nil, nil
	}
	return newPass(repo, runID, run)
}

// newPass reads a running run's definition, output schemas, state, node
// statuses and graph.
func newPass(repo Store, runID int64, run *db.WorkflowRun) (*pass, error) {
	defn, err := LoadYAMLString(run.DefinitionYAML)
	if err != nil {
		return nil, err
	}
	schemas, err := OutputSchemas(run.DefinitionYAML)
	if err != nil {
		return nil, fmt.Errorf("run %d: %w", runID, err)
	}
	p := &pass{runView: runView{repo: repo, runID: runID, defn: defn, state: decodeState(run.StateJSON)}, schemas: schemas}
	p.nodeStates = loadNodeStateMap(repo, runID)
	if err := p.buildGraph(); err != nil {
		return nil, err
	}
	return p, nil
}

// buildGraph builds the run's graph. Readiness counts forward edges only. A
// loop's back edge comes from the decision that closes it, which cannot
// finish before the loop's head has run; counted, it would leave the head
// waiting on its own tail, and every loop would stop at its first node.
func (v *runView) buildGraph() error {
	incoming, outgoing, err := BuildGraph(v.defn)
	if err != nil {
		return err
	}
	back := backEdges(incoming, outgoing)
	v.incoming, v.outgoing, v.loops = withoutBackEdges(incoming, back), outgoing, loopBodies(outgoing, back)
	return nil
}

// readyNodes resolves the run's decisions and returns the pending nodes that
// are ready.
//
// A node every one of whose incoming edges evaluated false is skipped,
// exactly as a decision node's not-taken branch is, and the skip cascades
// through the descendants that have no other live predecessor. A skip can
// leave another node with every edge false, or let a decision evaluate or be
// skipped, so decisions are resolved again and the rounds repeat until none
// is blocked. A loop reset in that resolve can return a skipped node to
// pending, and it can be blocked again: a loop whose head is skipped while
// its closing decision, fed from outside the loop, still loops back. Such a
// node is not skipped twice in one call; it stays pending and the run is
// settled by GetNextNodes. So each round skips a node not skipped before in
// this call, and the repetition ends. A resolution that a loop running no
// node keeps returning to fails the call (resolveDecisionNodes).
func (p *pass) readyNodes() ([]string, error) {
	skippedHere := map[string]bool{}
	for {
		ready, done, err := p.round(skippedHere)
		if err != nil || done {
			return ready, err
		}
	}
}

// round resolves the run's decisions and finds the ready nodes. It reports
// done when no blocked node is left that this call has not skipped, and
// otherwise skips those.
func (p *pass) round(skippedHere map[string]bool) ([]string, bool, error) {
	if err := p.settleDecisions(); err != nil {
		return nil, false, err
	}
	ready, blocked := p.findReady()
	fresh := unskipped(blocked, skippedHere)
	if len(fresh) == 0 {
		return ready, true, nil
	}
	return nil, false, p.skipBlocked(fresh, skippedHere)
}

// settleDecisions resolves the run's pending decisions and reloads the node
// statuses the resolutions wrote.
func (p *pass) settleDecisions() error {
	err := resolveDecisionNodes(p.repo, p.runID, p.defn, p.state, p.nodeStates, p.incoming, p.outgoing, p.loops)
	p.nodeStates = loadNodeStateMap(p.repo, p.runID)
	return err
}

func (p *pass) findReady() (ready, blocked []string) {
	return findReadyNodes(p.defn, p.state, p.nodeStates, p.incoming, p.outgoing)
}

// unskipped is the blocked nodes this call has not skipped yet.
func unskipped(blocked []string, skippedHere map[string]bool) []string {
	var fresh []string
	for _, name := range blocked {
		if !skippedHere[name] {
			fresh = append(fresh, name)
		}
	}
	return fresh
}

// skipBlocked marks each node of fresh skipped, here and in the store,
// cascades the skip through its successors (skipBranch), and reloads the
// node statuses the skips wrote.
func (p *pass) skipBlocked(fresh []string, skippedHere map[string]bool) error {
	now := timestamp()
	for _, name := range fresh {
		skippedHere[name] = true
		p.nodeStates[name]["status"] = "skipped"
		if err := p.repo.MarkNodeSkipped(p.runID, name, now); err != nil {
			return fmt.Errorf("run %d: skip %q: %w", p.runID, name, err)
		}
		for _, e := range p.outgoing[name] {
			skipBranch(p.repo, p.runID, e.Target, name, p.nodeStates, p.incoming)
		}
	}
	p.nodeStates = loadNodeStateMap(p.repo, p.runID)
	return nil
}

// dispatch builds the DispatchNode for each ready node, in order, and claims
// it; a node it cannot claim is left out.
func (p *pass) dispatch(ready []string) ([]DispatchNode, error) {
	now := timestamp()
	var out []DispatchNode
	for _, name := range ready {
		d, claimed, err := p.claimNode(name, now)
		if err != nil {
			return nil, err
		}
		if claimed {
			out = append(out, d)
		}
	}
	return out, nil
}

// claimNode builds a ready node's DispatchNode and claims the node. It
// reports false for a name the definition does not hold as a node, and for a
// node another driver of this run claimed first.
func (p *pass) claimNode(name, now string) (DispatchNode, bool, error) {
	node, ok := p.nodes()[name].(map[string]any)
	if !ok {
		return DispatchNode{}, false, nil
	}
	d := p.dispatchNode(name, node)
	// The claim is taken only from pending. A node another driver of
	// this run claimed after the read above is theirs to dispatch.
	if err := p.repo.MarkNodeRunning(p.runID, name, now); err != nil {
		if errors.Is(err, db.ErrNodeNotPending) {
			return DispatchNode{}, false, nil
		}
		return DispatchNode{}, false, err
	}
	return d, true, nil
}
