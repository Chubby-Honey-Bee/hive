package workflow

import (
	"errors"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// skipBranchStubRepo is a minimal Store stub for testing skipBranch.
// Only MarkNodeSkipped is meaningful; all other methods panic to surface
// accidental calls.
type skipBranchStubRepo struct {
	// skipped collects (nodeName) calls in order.
	skipped []string
	// runIDs collects the runID of each MarkNodeSkipped call.
	runIDs []int64
	// errOnSkip, if non-nil, is returned by MarkNodeSkipped.
	errOnSkip error
}

func (r *skipBranchStubRepo) MarkNodeSkipped(runID int64, nodeName, completedAt string) error {
	r.skipped = append(r.skipped, nodeName)
	r.runIDs = append(r.runIDs, runID)
	return r.errOnSkip
}

func (r *skipBranchStubRepo) MarkNodeWaitingHuman(runID int64, nodeName, startedAt string) error {
	return nil
}

func (r *skipBranchStubRepo) MarkRunPaused(runID int64) error { return nil }

func (r *skipBranchStubRepo) MarkRunFailed(runID int64, completedAt string) error { return nil }
func (r *skipBranchStubRepo) ResetNodeForLoop(runID int64, nodeName string) error { return nil }

// ── stub no-ops for the rest of the interface ──────────────────────────────

func (r *skipBranchStubRepo) CreateWorkflowRun(name string, version int, defnYAML, inputsJSON string, nodes []db.NodeSeed) (int64, error) {
	panic("unexpected call")
}
func (r *skipBranchStubRepo) GetWorkflowRun(runID int64) (*db.WorkflowRun, error) {
	panic("unexpected call")
}
func (r *skipBranchStubRepo) GetWorkflowNodeStates(runID int64) ([]db.WorkflowNodeState, error) {
	panic("unexpected call")
}
func (r *skipBranchStubRepo) MarkNodeRunning(runID int64, nodeName, startedAt string) error {
	panic("unexpected call")
}
func (r *skipBranchStubRepo) MarkNodeCompleted(runID int64, nodeName, outputsJSON, completedAt string) error {
	panic("unexpected call")
}
func (r *skipBranchStubRepo) MarkNodeFailed(runID int64, nodeName, errMsg, completedAt string, retryable bool) error {
	panic("unexpected call")
}
func (r *skipBranchStubRepo) MarkRunCompleted(runID int64, completedAt string) error {
	panic("unexpected call")
}
func (r *skipBranchStubRepo) MarkRunRunning(runID int64) error {
	panic("unexpected call")
}
func (r *skipBranchStubRepo) FindWaitingHumanNode(runID int64) (string, error) {
	panic("unexpected call")
}
func (r *skipBranchStubRepo) RecordDecision(runID int64, nodeName, condition, evaluatedValue, branchTaken, evalErr string) error {
	panic("unexpected call")
}

func (r *skipBranchStubRepo) ReleaseNode(runID int64, nodeName string) error {
	panic("unexpected call")
}
func (r *skipBranchStubRepo) FinishNodeInTx(runID int64, nodeName, status string, merge func(*db.WorkflowRun) (string, string, error)) error {
	panic("unexpected call")
}

// ── compile-time interface check ───────────────────────────────────────────
var _ Store = (*skipBranchStubRepo)(nil)

// ── helpers ────────────────────────────────────────────────────────────────

func pendingState() map[string]any   { return map[string]any{"status": "pending"} }
func completedState() map[string]any { return map[string]any{"status": "completed"} }
func runningState() map[string]any   { return map[string]any{"status": "running"} }

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// ── tests ──────────────────────────────────────────────────────────────────

// TestSkipBranch_StartNodeAlwaysSkipped verifies that the startNode is always
// marked skipped regardless of incoming edges.
func TestSkipBranch_StartNodeAlwaysSkipped(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"A": pendingState(),
	}
	incoming := map[string]map[string]bool{}

	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	if !contains(repo.skipped, "A") {
		t.Errorf("startNode 'A' should be skipped; got %v", repo.skipped)
	}
	if nodeStates["A"]["status"] != "skipped" {
		t.Errorf("nodeStates['A'] status should be 'skipped'; got %v", nodeStates["A"]["status"])
	}
}

// TestSkipBranch_NodeAlreadyNonPending verifies that a non-pending startNode
// is not re-marked and MarkNodeSkipped is not called for it.
func TestSkipBranch_NodeAlreadyNonPending(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"A": completedState(),
	}
	incoming := map[string]map[string]bool{}

	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	if contains(repo.skipped, "A") {
		t.Errorf("completed node 'A' should NOT be skipped; got %v", repo.skipped)
	}
}

// TestSkipBranch_NilNodeState verifies that a startNode with nil state is
// treated as non-pending and not skipped.
func TestSkipBranch_NilNodeState(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"A": nil,
	}
	incoming := map[string]map[string]bool{}

	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	if contains(repo.skipped, "A") {
		t.Errorf("nil-state node should NOT be skipped")
	}
}

// TestSkipBranch_MissingNodeState verifies that a startNode absent from
// nodeStates is treated as non-pending.
func TestSkipBranch_MissingNodeState(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{}
	incoming := map[string]map[string]bool{}

	skipBranch(repo, 1, "ghost", "", nodeStates, incoming)

	if contains(repo.skipped, "ghost") {
		t.Errorf("absent node should NOT be skipped")
	}
}

// TestSkipBranch_MarkNodeSkippedError verifies that a repo error in
// MarkNodeSkipped does not panic — it only logs to stderr.
func TestSkipBranch_MarkNodeSkippedError(t *testing.T) {
	repo := &skipBranchStubRepo{errOnSkip: errors.New("db unavailable")}
	nodeStates := map[string]map[string]any{
		"A": pendingState(),
	}
	incoming := map[string]map[string]bool{}

	// Must not panic.
	skipBranch(repo, 42, "A", "", nodeStates, incoming)

	// The in-memory state IS updated even when the DB write fails.
	if nodeStates["A"]["status"] != "skipped" {
		t.Errorf("in-memory status should be 'skipped' even on repo error; got %v", nodeStates["A"]["status"])
	}
}

// TestSkipBranch_VisitedDeduplication verifies that a node queued multiple
// times (e.g. via two incoming edges) is only skipped once.
func TestSkipBranch_VisitedDeduplication(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"A": pendingState(),
	}
	incoming := map[string]map[string]bool{}

	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	count := 0
	for _, n := range repo.skipped {
		if n == "A" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("node 'A' should be skipped exactly once; got %d times", count)
	}
}

// A start node whose only predecessor has no recorded state is skipped: a
// predecessor with no state is not live.
func TestSkipBranch_StartNodeWithAStatelessPredecessorIsSkipped(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"A": pendingState(),
	}
	incoming := map[string]map[string]bool{
		"A": {"decision": true},
	}

	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	if nodeStates["A"]["status"] != "skipped" {
		t.Errorf("A (dead branch root) should be skipped")
	}
}

// ── descendant traversal (the BFS fan-out) ─────────────

// TestSkipBranch_LinearChain_DescendantAlsoSkipped verifies the core BFS
// invariant: when A→B and A is the dead branch root, B (with no other live
// predecessor) is also marked skipped.
func TestSkipBranch_LinearChain_DescendantAlsoSkipped(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"A": pendingState(),
		"B": pendingState(),
	}
	incoming := map[string]map[string]bool{
		"B": {"A": true},
	}

	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	if !contains(repo.skipped, "A") {
		t.Errorf("startNode 'A' should be skipped; got %v", repo.skipped)
	}
	if !contains(repo.skipped, "B") {
		t.Errorf("descendant 'B' (only dead predecessor) should also be skipped; got %v", repo.skipped)
	}
	if nodeStates["B"]["status"] != "skipped" {
		t.Errorf("nodeStates['B'] status should be 'skipped'; got %v", nodeStates["B"]["status"])
	}
}

// TestSkipBranch_DescendantWithPendingPredecessorPreserved verifies that a
// descendant node reachable from both the dead branch and a still-pending
// sibling stays pending.
// Graph: decision→A (dead), decision→B (pending), A→C, B→C
func TestSkipBranch_DescendantWithPendingPredecessorPreserved(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"A": pendingState(),
		"B": pendingState(), // live sibling
		"C": pendingState(),
	}
	incoming := map[string]map[string]bool{
		"C": {"A": true, "B": true},
	}

	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	if !contains(repo.skipped, "A") {
		t.Errorf("startNode 'A' should be skipped")
	}
	if contains(repo.skipped, "C") {
		t.Errorf("node 'C' must NOT be skipped — 'B' (pending) is still live; got %v", repo.skipped)
	}
	if nodeStates["C"]["status"] == "skipped" {
		t.Errorf("nodeStates['C'] status must stay pending; got %v", nodeStates["C"]["status"])
	}
}

// TestSkipBranch_DescendantWithRunningPredecessorPreserved verifies that a
// running predecessor keeps the descendant pending.
func TestSkipBranch_DescendantWithRunningPredecessorPreserved(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"A": pendingState(),
		"B": runningState(), // live running
		"C": pendingState(),
	}
	incoming := map[string]map[string]bool{
		"C": {"A": true, "B": true},
	}

	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	if contains(repo.skipped, "C") {
		t.Errorf("node 'C' must NOT be skipped — 'B' is running; got %v", repo.skipped)
	}
}

// TestSkipBranch_DescendantWithCompletedUnvisitedPredecessorPreserved checks
// that a completed predecessor that is NOT on the dead branch keeps the
// descendant alive.
func TestSkipBranch_DescendantWithCompletedUnvisitedPredecessorPreserved(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"A": pendingState(),
		"B": completedState(), // already finished via live path
		"C": pendingState(),
	}
	incoming := map[string]map[string]bool{
		"C": {"A": true, "B": true},
	}

	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	if contains(repo.skipped, "C") {
		t.Errorf("node 'C' must NOT be skipped — 'B' (completed, unvisited) is an alternate predecessor; got %v", repo.skipped)
	}
}

// TestSkipBranch_NilPredecessorStateInDescendant verifies that a predecessor
// with a nil nodeStates entry is treated as non-live (not counted as hasOtherLive).
// The descendant with only nil + dead predecessors should still be skipped.
func TestSkipBranch_NilPredecessorStateInDescendant(t *testing.T) {
	repo := &skipBranchStubRepo{}
	// "B" is listed as a predecessor of C but has no nodeStates entry → nil.
	nodeStates := map[string]map[string]any{
		"A": pendingState(),
		"C": pendingState(),
		// "B" intentionally absent → nil lookup
	}
	incoming := map[string]map[string]bool{
		"C": {"A": true, "B": true},
	}

	// Must not panic.
	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	// B has nil state → not counted as live; A is dead → C should be skipped.
	if !contains(repo.skipped, "C") {
		t.Errorf("node 'C' should be skipped when all non-nil predecessors are dead; got %v", repo.skipped)
	}
}

// TestSkipBranch_ThreeNodeChain verifies BFS propagation through a longer chain
// A→B→C where all nodes have only the dead-branch predecessor.
func TestSkipBranch_ThreeNodeChain(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"A": pendingState(),
		"B": pendingState(),
		"C": pendingState(),
	}
	incoming := map[string]map[string]bool{
		"B": {"A": true},
		"C": {"B": true},
	}

	skipBranch(repo, 1, "A", "", nodeStates, incoming)

	for _, want := range []string{"A", "B", "C"} {
		if !contains(repo.skipped, want) {
			t.Errorf("node %q should be skipped in a dead chain; skipped=%v", want, repo.skipped)
		}
	}
}

// TestSkipBranch_RunIDPassedToRepo verifies that the runID provided to
// skipBranch is the one forwarded to MarkNodeSkipped.
func TestSkipBranch_RunIDPassedToRepo(t *testing.T) {
	// The stub records the run id, so the test checks the id it is called
	// with.
	const wantRunID int64 = 9999
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{"target": pendingState()}

	skipBranch(repo, wantRunID, "target", "", nodeStates, map[string]map[string]bool{})

	if !contains(repo.skipped, "target") {
		t.Fatalf("MarkNodeSkipped was not called for 'target'")
	}
	for _, got := range repo.runIDs {
		if got != wantRunID {
			t.Errorf("MarkNodeSkipped received run id %d, want %d", got, wantRunID)
		}
	}
}

// TestSkipBranch_DiamondPatternKeepsConvergenceLive: a node reachable via
// two branches of a decision (here, synthesize is downstream of both the
// COMPLETE shortcut and the wave2 path) stays pending when its dead-branch
// predecessor is the calling decision.
//
// Graph:
//
//	check-complete (decision, completed)
//	   ├── synthesize  ← startNode of dead branch
//	   └── wave2 ── re-eval ── check-complete-2 ── synthesize
//
// When skipBranch(synthesize, decisionNode="check-complete") fires
// because the wave2 branch was chosen, synthesize MUST stay pending —
// it has another live ancestor path through wave2 → re-eval →
// check-complete-2.
func TestSkipBranch_DiamondPatternKeepsConvergenceLive(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"check-complete":   completedState(),
		"synthesize":       pendingState(),
		"wave2":            pendingState(),
		"re-eval":          pendingState(),
		"check-complete-2": pendingState(),
	}
	// synthesize has TWO incoming edges: from check-complete (the
	// COMPLETE shortcut) and from check-complete-2 (the wave2 path).
	incoming := map[string]map[string]bool{
		"synthesize":       {"check-complete": true, "check-complete-2": true},
		"wave2":            {"check-complete": true},
		"re-eval":          {"wave2": true},
		"check-complete-2": {"re-eval": true},
	}

	// check-complete picked the false branch (wave2), so skipBranch
	// is called on the not-taken trueEdge=synthesize.
	skipBranch(repo, 1, "synthesize", "check-complete", nodeStates, incoming)

	if contains(repo.skipped, "synthesize") {
		t.Errorf("synthesize should NOT be skipped — it's still reachable via wave2 path; got %v", repo.skipped)
	}
	if nodeStates["synthesize"]["status"] != "pending" {
		t.Errorf("synthesize status should stay 'pending'; got %v", nodeStates["synthesize"]["status"])
	}
}

// TestSkipBranch_DiamondWithoutAlternativePathStillSkips: when the
// dead-branch's startNode has no other live predecessor, it is skipped.
// (Without this the not-taken branch would wrongly stay pending.)
func TestSkipBranch_DiamondWithoutAlternativePathStillSkips(t *testing.T) {
	repo := &skipBranchStubRepo{}
	nodeStates := map[string]map[string]any{
		"check-complete": completedState(),
		"dead-branch":    pendingState(),
	}
	incoming := map[string]map[string]bool{
		"dead-branch": {"check-complete": true},
	}

	skipBranch(repo, 1, "dead-branch", "check-complete", nodeStates, incoming)

	if !contains(repo.skipped, "dead-branch") {
		t.Errorf("dead-branch should be skipped (no alternative live predecessor); got %v", repo.skipped)
	}
}
