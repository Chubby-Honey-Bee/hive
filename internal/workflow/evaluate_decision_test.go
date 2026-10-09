package workflow

import (
	"errors"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// evalDecisionStubRepo is a minimal Store stub for testing
// evaluateDecision. It records MarkNodeCompleted, MarkNodeSkipped, and
// RecordDecision calls and lets callers inject errors.
type evalDecisionStubRepo struct {
	// recorded calls
	completedNodes  []string
	skippedNodes    []string
	decisionsRecord []decisionRecord

	// injected errors
	errMarkNodeCompleted error
	errRecordDecision    error
	errMarkNodeSkipped   error
}

type decisionRecord struct {
	nodeName       string
	condition      string
	evaluatedValue string
	branchTaken    string
	evalErr        string
}

func (r *evalDecisionStubRepo) MarkNodeCompleted(runID int64, nodeName, outputsJSON, completedAt string) error {
	r.completedNodes = append(r.completedNodes, nodeName)
	return r.errMarkNodeCompleted
}

func (r *evalDecisionStubRepo) MarkNodeSkipped(runID int64, nodeName, completedAt string) error {
	r.skippedNodes = append(r.skippedNodes, nodeName)
	return r.errMarkNodeSkipped
}

func (r *evalDecisionStubRepo) MarkNodeWaitingHuman(runID int64, nodeName, startedAt string) error {
	return nil
}

func (r *evalDecisionStubRepo) MarkRunPaused(runID int64) error { return nil }

func (r *evalDecisionStubRepo) MarkRunFailed(runID int64, completedAt string) error { return nil }
func (r *evalDecisionStubRepo) ResetNodeForLoop(runID int64, nodeName string) error { return nil }

func (r *evalDecisionStubRepo) RecordDecision(runID int64, nodeName, condition, evaluatedValue, branchTaken, evalErr string) error {
	r.decisionsRecord = append(r.decisionsRecord, decisionRecord{
		nodeName:       nodeName,
		condition:      condition,
		evaluatedValue: evaluatedValue,
		branchTaken:    branchTaken,
		evalErr:        evalErr,
	})
	return r.errRecordDecision
}

// ── no-op stubs for remaining Store methods ───────────────────────────────

func (r *evalDecisionStubRepo) CreateWorkflowRun(name string, version int, defnYAML, inputsJSON string, nodes []db.NodeSeed) (int64, error) {
	panic("unexpected call")
}
func (r *evalDecisionStubRepo) GetWorkflowRun(runID int64) (*db.WorkflowRun, error) {
	panic("unexpected call")
}
func (r *evalDecisionStubRepo) GetWorkflowNodeStates(runID int64) ([]db.WorkflowNodeState, error) {
	panic("unexpected call")
}
func (r *evalDecisionStubRepo) MarkNodeRunning(runID int64, nodeName, startedAt string) error {
	panic("unexpected call")
}
func (r *evalDecisionStubRepo) MarkNodeFailed(runID int64, nodeName, errMsg, completedAt string, retryable bool) error {
	panic("unexpected call")
}
func (r *evalDecisionStubRepo) MarkRunCompleted(runID int64, completedAt string) error {
	panic("unexpected call")
}
func (r *evalDecisionStubRepo) MarkRunRunning(runID int64) error {
	panic("unexpected call")
}
func (r *evalDecisionStubRepo) FindWaitingHumanNode(runID int64) (string, error) {
	panic("unexpected call")
}

func (r *evalDecisionStubRepo) ReleaseNode(runID int64, nodeName string) error {
	panic("unexpected call")
}
func (r *evalDecisionStubRepo) FinishNodeInTx(runID int64, nodeName, status string, merge func(*db.WorkflowRun) (string, string, error)) error {
	panic("unexpected call")
}

var _ Store = (*evalDecisionStubRepo)(nil)

// ── helper builders ───────────────────────────────────────────────────────

func decisionNode(condition, trueEdge, falseEdge string) map[string]any {
	n := map[string]any{"type": "decision"}
	if condition != "" {
		n["condition"] = condition
	}
	if trueEdge != "" {
		n["true_edge"] = trueEdge
	}
	if falseEdge != "" {
		n["false_edge"] = falseEdge
	}
	return n
}

// checkDefn wraps a decision node as the definition's node "check".
func checkDefn(node map[string]any) map[string]any {
	return map[string]any{"nodes": map[string]any{"check": node}}
}

// ── tests ─────────────────────────────────────────────────────────────────

// TestEvaluateDecision_PredecessorNotCompleted verifies that evaluateDecision
// returns false (leaves the node pending) when a predecessor is still running.
func TestEvaluateDecision_PredecessorNotCompleted(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	state := map[string]any{"score": 90}
	nodeStates := map[string]map[string]any{
		"upstream": {"status": "running"},
		"check":    {"status": "pending"},
	}
	incoming := map[string]map[string]bool{
		"check": {"upstream": true},
	}
	node := decisionNode("score >= 80", "finalize", "retry")

	got := evaluateDecision(repo, 1, "check", checkDefn(node), state, nodeStates, incoming, nil, nil)

	if got {
		t.Fatal("expected false when predecessor is not completed")
	}
	if len(repo.completedNodes) != 0 {
		t.Errorf("expected no MarkNodeCompleted calls; got %v", repo.completedNodes)
	}
}

// TestEvaluateDecision_PredecessorNil verifies that a nil predecessor state
// also causes evaluateDecision to return false.
func TestEvaluateDecision_PredecessorNil(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	state := map[string]any{}
	nodeStates := map[string]map[string]any{
		"upstream": nil,
		"check":    {"status": "pending"},
	}
	incoming := map[string]map[string]bool{
		"check": {"upstream": true},
	}
	node := decisionNode("true", "a", "b")

	got := evaluateDecision(repo, 1, "check", checkDefn(node), state, nodeStates, incoming, nil, nil)
	if got {
		t.Fatal("expected false when predecessor state is nil")
	}
}

// TestEvaluateDecision_PredecessorSkipped verifies that a skipped predecessor
// is treated as ready (skipped counts as done).
func TestEvaluateDecision_PredecessorSkipped(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	state := map[string]any{"ok": true}
	nodeStates := map[string]map[string]any{
		"upstream": {"status": "skipped"},
		"check":    {"status": "pending"},
		"yes":      {"status": "pending"},
	}
	incoming := map[string]map[string]bool{
		"check": {"upstream": true},
	}
	node := decisionNode("ok", "yes", "")

	got := evaluateDecision(repo, 1, "check", checkDefn(node), state, nodeStates, incoming, nil, nil)
	if !got {
		t.Fatal("expected true when predecessor is skipped")
	}
}

// TestEvaluateDecision_ConditionTrueSkipsFalseEdge verifies the happy path:
// condition true → true_edge taken, false_edge skipped.
func TestEvaluateDecision_ConditionTrueSkipsFalseEdge(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	state := map[string]any{"score": 90}
	nodeStates := map[string]map[string]any{
		"upstream": {"status": "completed"},
		"check":    {"status": "pending"},
		"finalize": {"status": "pending"},
		"retry":    {"status": "pending"},
	}
	incoming := map[string]map[string]bool{
		"check": {"upstream": true},
	}
	node := decisionNode("score >= 80", "finalize", "retry")

	got := evaluateDecision(repo, 1, "check", checkDefn(node), state, nodeStates, incoming, nil, nil)

	if !got {
		t.Fatal("expected true (node resolved)")
	}
	if nodeStates["check"]["status"] != "completed" {
		t.Errorf("check node status should be 'completed'; got %v", nodeStates["check"]["status"])
	}
	if nodeStates["retry"]["status"] != "skipped" {
		t.Errorf("retry (false_edge) should be 'skipped'; got %v", nodeStates["retry"]["status"])
	}
	// RecordDecision should record "true"
	if len(repo.decisionsRecord) == 0 {
		t.Fatal("expected RecordDecision to be called")
	}
	rec := repo.decisionsRecord[len(repo.decisionsRecord)-1]
	if rec.evaluatedValue != "true" {
		t.Errorf("expected evaluatedValue=true; got %q", rec.evaluatedValue)
	}
	if rec.branchTaken != "finalize" {
		t.Errorf("expected branchTaken=finalize; got %q", rec.branchTaken)
	}
}

// TestEvaluateDecision_ConditionFalseSkipsTrueEdge verifies the sad path:
// condition false → false_edge taken, true_edge skipped.
func TestEvaluateDecision_ConditionFalseSkipsTrueEdge(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	state := map[string]any{"score": 50}
	nodeStates := map[string]map[string]any{
		"upstream": {"status": "completed"},
		"check":    {"status": "pending"},
		"finalize": {"status": "pending"},
		"retry":    {"status": "pending"},
	}
	incoming := map[string]map[string]bool{
		"check": {"upstream": true},
	}
	node := decisionNode("score >= 80", "finalize", "retry")

	got := evaluateDecision(repo, 1, "check", checkDefn(node), state, nodeStates, incoming, nil, nil)

	if !got {
		t.Fatal("expected true (node resolved)")
	}
	if nodeStates["finalize"]["status"] != "skipped" {
		t.Errorf("finalize (true_edge) should be 'skipped'; got %v", nodeStates["finalize"]["status"])
	}
	rec := repo.decisionsRecord[len(repo.decisionsRecord)-1]
	if rec.evaluatedValue != "false" {
		t.Errorf("expected evaluatedValue=false; got %q", rec.evaluatedValue)
	}
	if rec.branchTaken != "retry" {
		t.Errorf("expected branchTaken=retry; got %q", rec.branchTaken)
	}
}

// TestEvaluateDecision_EmptyConditionDefaultsTrue verifies that an absent
// condition field defaults to "true" and takes the true_edge.
func TestEvaluateDecision_EmptyConditionDefaultsTrue(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	state := map[string]any{}
	nodeStates := map[string]map[string]any{
		"check": {"status": "pending"},
		"yes":   {"status": "pending"},
		"no":    {"status": "pending"},
	}
	incoming := map[string]map[string]bool{}
	// no "condition" key in node
	node := map[string]any{"type": "decision", "true_edge": "yes", "false_edge": "no"}

	got := evaluateDecision(repo, 1, "check", checkDefn(node), state, nodeStates, incoming, nil, nil)

	if !got {
		t.Fatal("expected true when condition is empty (defaults to 'true')")
	}
	if nodeStates["no"]["status"] != "skipped" {
		t.Errorf("false_edge 'no' should be skipped; got %v", nodeStates["no"]["status"])
	}
}

// TestEvaluateDecision_InvalidConditionReturnsFalse verifies that an invalid
// SafeEval expression leaves the decision node pending and returns false.
// RecordDecision is called with a non-empty evalErr.
func TestEvaluateDecision_InvalidConditionReturnsFalse(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	state := map[string]any{}
	nodeStates := map[string]map[string]any{
		"check": {"status": "pending"},
	}
	incoming := map[string]map[string]bool{}
	// Reference an undefined variable to force a SafeEval error.
	node := decisionNode("undefined_var > 5", "yes", "no")

	got := evaluateDecision(repo, 1, "check", checkDefn(node), state, nodeStates, incoming, nil, nil)

	if got {
		t.Fatal("expected false when condition fails to evaluate")
	}
	if nodeStates["check"]["status"] != "pending" {
		t.Errorf("decision node should remain pending on eval error; got %v", nodeStates["check"]["status"])
	}
	// RecordDecision should be called with "<error>" evaluated value and a non-empty evalErr.
	if len(repo.decisionsRecord) == 0 {
		t.Fatal("expected RecordDecision to be called on eval error")
	}
	rec := repo.decisionsRecord[0]
	if rec.evaluatedValue != "<error>" {
		t.Errorf("expected evaluatedValue='<error>'; got %q", rec.evaluatedValue)
	}
	if rec.evalErr == "" {
		t.Errorf("expected non-empty evalErr; got empty")
	}
}

// TestEvaluateDecision_MarkNodeCompletedError verifies that a repo error from
// MarkNodeCompleted does not prevent evaluateDecision from returning true.
func TestEvaluateDecision_MarkNodeCompletedError(t *testing.T) {
	repo := &evalDecisionStubRepo{errMarkNodeCompleted: errors.New("db down")}
	state := map[string]any{"ok": true}
	nodeStates := map[string]map[string]any{
		"check": {"status": "pending"},
		"yes":   {"status": "pending"},
	}
	incoming := map[string]map[string]bool{}
	node := decisionNode("ok", "yes", "")

	got := evaluateDecision(repo, 1, "check", checkDefn(node), state, nodeStates, incoming, nil, nil)

	// Even if persist fails, the function should return true (best-effort).
	if !got {
		t.Fatal("expected true even when MarkNodeCompleted fails")
	}
}

// TestEvaluateDecision_RecordDecisionError verifies that a RecordDecision error
// is swallowed and evaluateDecision still returns true.
func TestEvaluateDecision_RecordDecisionError(t *testing.T) {
	repo := &evalDecisionStubRepo{errRecordDecision: errors.New("record error")}
	state := map[string]any{"x": 1}
	nodeStates := map[string]map[string]any{
		"check": {"status": "pending"},
		"yes":   {"status": "pending"},
	}
	incoming := map[string]map[string]bool{}
	node := decisionNode("x > 0", "yes", "")

	got := evaluateDecision(repo, 1, "check", checkDefn(node), state, nodeStates, incoming, nil, nil)

	if !got {
		t.Fatal("expected true even when RecordDecision fails")
	}
}

// ── resolveDecisionNodes tests ────────────────────────────────────────────

// TestResolveDecisionNodes_NoNodes verifies that a defn with no "nodes" key
// is a no-op and does not panic.
func TestResolveDecisionNodes_NoNodes(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	defn := map[string]any{} // no "nodes" key
	resolveDecisionNodes(repo, 1, defn, map[string]any{}, map[string]map[string]any{}, map[string]map[string]bool{}, nil, nil)
	if len(repo.decisionsRecord) != 0 {
		t.Errorf("expected no decisions; got %d", len(repo.decisionsRecord))
	}
}

// TestResolveDecisionNodes_NonMapNode verifies that node entries that are not
// map[string]any are silently skipped.
func TestResolveDecisionNodes_NonMapNode(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	defn := map[string]any{
		"nodes": map[string]any{
			"bad-node": "not-a-map",
		},
	}
	resolveDecisionNodes(repo, 1, defn, map[string]any{}, map[string]map[string]any{}, map[string]map[string]bool{}, nil, nil)
	if len(repo.decisionsRecord) != 0 {
		t.Errorf("expected no decisions; got %d", len(repo.decisionsRecord))
	}
}

// TestResolveDecisionNodes_NonDecisionNode verifies that nodes whose type is
// not "decision" are skipped entirely.
func TestResolveDecisionNodes_NonDecisionNode(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	defn := map[string]any{
		"nodes": map[string]any{
			"agent-1": map[string]any{"type": "agent"},
		},
	}
	nodeStates := map[string]map[string]any{
		"agent-1": {"status": "pending"},
	}
	resolveDecisionNodes(repo, 1, defn, map[string]any{}, nodeStates, map[string]map[string]bool{}, nil, nil)
	if len(repo.decisionsRecord) != 0 {
		t.Errorf("expected no decisions; got %d", len(repo.decisionsRecord))
	}
}

// TestResolveDecisionNodes_DecisionNotPending verifies that a decision node
// whose status is already "completed" is skipped.
func TestResolveDecisionNodes_DecisionNotPending(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	defn := map[string]any{
		"nodes": map[string]any{
			"check": decisionNode("true", "yes", "no"),
		},
	}
	nodeStates := map[string]map[string]any{
		"check": {"status": "completed"},
	}
	resolveDecisionNodes(repo, 1, defn, map[string]any{}, nodeStates, map[string]map[string]bool{}, nil, nil)
	if len(repo.decisionsRecord) != 0 {
		t.Errorf("expected no decisions when node already completed; got %d", len(repo.decisionsRecord))
	}
}

// TestResolveDecisionNodes_NilNodeState verifies that a nil nodeState entry
// for a decision node causes it to be skipped.
func TestResolveDecisionNodes_NilNodeState(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	defn := map[string]any{
		"nodes": map[string]any{
			"check": decisionNode("true", "yes", "no"),
		},
	}
	nodeStates := map[string]map[string]any{
		"check": nil,
	}
	resolveDecisionNodes(repo, 1, defn, map[string]any{}, nodeStates, map[string]map[string]bool{}, nil, nil)
	if len(repo.decisionsRecord) != 0 {
		t.Errorf("expected no decisions when nodeState is nil; got %d", len(repo.decisionsRecord))
	}
}

// TestResolveDecisionNodes_HappyPath verifies that a pending decision node
// with all predecessors complete is resolved by resolveDecisionNodes.
func TestResolveDecisionNodes_HappyPath(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	defn := map[string]any{
		"nodes": map[string]any{
			"check": decisionNode("score >= 80", "finalize", "retry"),
		},
	}
	state := map[string]any{"score": 90}
	nodeStates := map[string]map[string]any{
		"check":    {"status": "pending"},
		"finalize": {"status": "pending"},
		"retry":    {"status": "pending"},
	}
	resolveDecisionNodes(repo, 1, defn, state, nodeStates, map[string]map[string]bool{}, nil, nil)

	if nodeStates["check"]["status"] != "completed" {
		t.Errorf("check should be completed; got %v", nodeStates["check"]["status"])
	}
	if len(repo.decisionsRecord) == 0 {
		t.Fatal("expected RecordDecision to be called")
	}
	if nodeStates["retry"]["status"] != "skipped" {
		t.Errorf("retry (false_edge) should be skipped; got %v", nodeStates["retry"]["status"])
	}
}

// TestResolveDecisionNodes_ChainedDecisions verifies that the changed-loop
// causes multiple sequential decision nodes to be resolved across iterations.
// check2 cannot fire until check1 completes; after check1 fires (changed=true),
// the loop re-scans and fires check2.
func TestResolveDecisionNodes_ChainedDecisions(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	defn := map[string]any{
		"nodes": map[string]any{
			"check1": decisionNode("a > 0", "check2", "skip"),
			"check2": decisionNode("b > 0", "done", "fail"),
		},
	}
	state := map[string]any{"a": 1, "b": 2}
	nodeStates := map[string]map[string]any{
		"check1": {"status": "pending"},
		"check2": {"status": "pending"},
		"done":   {"status": "pending"},
		"fail":   {"status": "pending"},
		"skip":   {"status": "pending"},
	}
	incoming := map[string]map[string]bool{
		"check2": {"check1": true},
	}
	resolveDecisionNodes(repo, 1, defn, state, nodeStates, incoming, nil, nil)

	if nodeStates["check1"]["status"] != "completed" {
		t.Errorf("check1 should be completed; got %v", nodeStates["check1"]["status"])
	}
	if nodeStates["check2"]["status"] != "completed" {
		t.Errorf("check2 should be completed; got %v", nodeStates["check2"]["status"])
	}
	if len(repo.decisionsRecord) != 2 {
		t.Errorf("expected 2 decisions recorded; got %d", len(repo.decisionsRecord))
	}
}

// TestEvaluateDecision_NoPredecessors verifies that a decision node with no
// incoming edges is immediately evaluable.
func TestEvaluateDecision_NoPredecessors(t *testing.T) {
	repo := &evalDecisionStubRepo{}
	state := map[string]any{"flag": false}
	nodeStates := map[string]map[string]any{
		"check": {"status": "pending"},
		"yes":   {"status": "pending"},
		"no":    {"status": "pending"},
	}
	incoming := map[string]map[string]bool{} // no predecessors
	node := decisionNode("flag", "yes", "no")

	got := evaluateDecision(repo, 1, "check", checkDefn(node), state, nodeStates, incoming, nil, nil)
	if !got {
		t.Fatal("expected true when no predecessors block evaluation")
	}
	// flag==false → false_edge "no" is taken; true_edge "yes" is skipped.
	if nodeStates["yes"]["status"] != "skipped" {
		t.Errorf("true_edge 'yes' should be skipped; got %v", nodeStates["yes"]["status"])
	}
}

// TestEvaluateDecision_TableDriven covers condition evaluation and branch
// selection across multiple scenarios in a compact form.
func TestEvaluateDecision_TableDriven(t *testing.T) {
	tests := []struct {
		name         string
		condition    string
		state        map[string]any
		trueEdge     string
		falseEdge    string
		wantResolved bool
		wantSkipped  string // which edge should be skipped (empty = neither)
	}{
		{
			name:         "numeric-gte-true",
			condition:    "score >= 80",
			state:        map[string]any{"score": 85},
			trueEdge:     "accept",
			falseEdge:    "reject",
			wantResolved: true,
			wantSkipped:  "reject",
		},
		{
			name:         "numeric-gte-false",
			condition:    "score >= 80",
			state:        map[string]any{"score": 79},
			trueEdge:     "accept",
			falseEdge:    "reject",
			wantResolved: true,
			wantSkipped:  "accept",
		},
		{
			name:         "string-eq-true",
			condition:    `verdict == "PASS"`,
			state:        map[string]any{"verdict": "PASS"},
			trueEdge:     "done",
			falseEdge:    "retry",
			wantResolved: true,
			wantSkipped:  "retry",
		},
		{
			name:         "string-eq-false",
			condition:    `verdict == "PASS"`,
			state:        map[string]any{"verdict": "FAIL"},
			trueEdge:     "done",
			falseEdge:    "retry",
			wantResolved: true,
			wantSkipped:  "done",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &evalDecisionStubRepo{}
			nodeStates := map[string]map[string]any{
				"check":     {"status": "pending"},
				tc.trueEdge: {"status": "pending"},
			}
			if tc.falseEdge != "" {
				nodeStates[tc.falseEdge] = map[string]any{"status": "pending"}
			}
			incoming := map[string]map[string]bool{}
			node := decisionNode(tc.condition, tc.trueEdge, tc.falseEdge)

			got := evaluateDecision(repo, 1, "check", checkDefn(node), tc.state, nodeStates, incoming, nil, nil)

			if got != tc.wantResolved {
				t.Errorf("returned %v, want %v", got, tc.wantResolved)
			}
			if tc.wantSkipped != "" {
				if nodeStates[tc.wantSkipped]["status"] != "skipped" {
					t.Errorf("node %q should be skipped; got %v", tc.wantSkipped, nodeStates[tc.wantSkipped]["status"])
				}
			}
		})
	}
}
