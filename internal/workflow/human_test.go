package workflow_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

func testStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.NewStore(filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	return store
}

// startRun starts a run from defn as `chb workflow init` does.
func startRun(t *testing.T, repo *db.WorkflowsRepo, defn string) int64 {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flow.yaml")
	if err := os.WriteFile(path, []byte(defn), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := workflow.InitWorkflow(repo, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// runView is a run's row, its node states and its state map.
type runView struct {
	run   *db.WorkflowRun
	nodes []db.WorkflowNodeState
	state map[string]any
}

func readRun(t *testing.T, repo *db.WorkflowsRepo, id int64) runView {
	t.Helper()
	run, err := repo.GetWorkflowRun(id)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := repo.GetWorkflowNodeStates(id)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{}
	if run.StateJSON != "" {
		if err := json.Unmarshal([]byte(run.StateJSON), &state); err != nil {
			t.Fatal(err)
		}
	}
	return runView{run: run, nodes: nodes, state: state}
}

// parkReview puts node into waiting_human directly.
func parkReview(t *testing.T, store *db.Store, id int64, node string) {
	t.Helper()
	if _, err := store.WriteDB.Exec(
		`UPDATE workflow_node_states SET status='waiting_human' WHERE run_id=? AND node_name=?`, id, node); err != nil {
		t.Fatal(err)
	}
}

const oneReview = `name: t
nodes:
  h:
    type: human_review
`

func TestResume_NoWaitingHuman(t *testing.T) {
	store := testStore(t)
	id := startRun(t, store.Workflows(), `name: t
nodes:
  a:
    type: agent
    prompt: a
`)
	err := workflow.ResumeHumanReview(store.Workflows(), id, "approve", "")
	if err == nil {
		t.Fatal("expected error when no human-waiting node exists")
	}
	if !strings.Contains(err.Error(), "no nodes waiting for human review") {
		t.Errorf("error = %v; expected 'no nodes waiting for human review'", err)
	}
}

func TestResume_RunNotFound(t *testing.T) {
	store := testStore(t)
	if err := workflow.ResumeHumanReview(store.Workflows(), 99999, "approve", ""); err == nil {
		t.Fatal("expected error for missing run")
	}
}

// Approve completes the node, puts the run back to running, and persists the
// decision and the feedback in state.
func TestResume_HappyPathApprove(t *testing.T) {
	store := testStore(t)
	repo := store.Workflows()
	id := startRun(t, repo, oneReview)
	parkReview(t, store, id, "h")
	if err := workflow.ResumeHumanReview(repo, id, "approve", "looks good"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	st := readRun(t, repo, id)
	if st.run.Status != "running" {
		t.Errorf("run status = %q, want running", st.run.Status)
	}
	if len(st.nodes) != 1 || st.nodes[0].Status != "completed" {
		t.Errorf("nodes = %+v, want h completed", st.nodes)
	}
	if st.state["human_decision"] != "approve" {
		t.Errorf("decision = %v; want approve", st.state["human_decision"])
	}
	if st.state["human_feedback"] != "looks good" {
		t.Errorf("feedback = %v; want 'looks good'", st.state["human_feedback"])
	}
}

func TestResume_RejectMarksFailed(t *testing.T) {
	store := testStore(t)
	repo := store.Workflows()
	id := startRun(t, repo, oneReview)
	parkReview(t, store, id, "h")
	if err := workflow.ResumeHumanReview(repo, id, "reject", "needs work"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	st := readRun(t, repo, id)
	if len(st.nodes) != 1 {
		t.Fatalf("expected 1 node; got %d", len(st.nodes))
	}
	if st.nodes[0].Status != "failed" {
		t.Errorf("status = %q; want 'failed' for reject", st.nodes[0].Status)
	}
}

// parkedReview starts a run whose human_review node `review` is parked, with
// a sibling `sib` dispatched in the same wave and still running.
func parkedReview(t *testing.T, repo *db.WorkflowsRepo, defn string) int64 {
	t.Helper()
	id := startRun(t, repo, defn)
	next, err := workflow.GetNextNodes(repo, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 2 {
		t.Fatalf("next = %+v, want review and sib in one wave", next)
	}
	if err := workflow.PauseForHuman(repo, id, "review"); err != nil {
		t.Fatal(err)
	}
	return id
}

const reviewWithSibling = `name: t
version: 1
nodes:
  review:
    type: human_review
    state_updates: {reviewed_by: "{human_decision}"}
  sib:
    type: agent
    prompt: s
    outputs: [kb]
`

// Only approve, reject and redirect answer a review: anything else is
// refused and changes nothing, since a failed gate would fail the run with
// its successor stranded, past undoing.
func TestResume_RefusesAnUnknownDecision(t *testing.T) {
	for _, decision := range []string{"aprove", "", "deny", "Approve"} {
		store := testStore(t)
		repo := store.Workflows()
		id := parkedReview(t, repo, reviewWithSibling)
		err := workflow.ResumeHumanReview(repo, id, decision, "")
		if err == nil || !strings.Contains(err.Error(), "approve, reject or redirect") {
			t.Errorf("decision %q: err = %v, want it refused", decision, err)
		}
		st := readRun(t, repo, id)
		for _, n := range st.nodes {
			if n.NodeName == "review" && n.Status != "waiting_human" {
				t.Errorf("decision %q: review = %q, want still waiting_human", decision, n.Status)
			}
		}
		if st.run.Status != "paused" {
			t.Errorf("decision %q: run = %q, want still paused", decision, st.run.Status)
		}
		if _, ok := st.state["human_decision"]; ok {
			t.Errorf("decision %q: state gained human_decision %v", decision, st.state["human_decision"])
		}
	}
}

// writeHook runs before, once, the first write that could carry state:
// a node completion or a transactional finish. Whatever Resume read before
// that point, the hook's commit lands after it.
type writeHook struct {
	*db.WorkflowsRepo
	before func()
}

func (r *writeHook) fire() {
	if f := r.before; f != nil {
		r.before = nil
		f()
	}
}

func (r *writeHook) MarkNodeCompleted(runID int64, nodeName, outputsJSON, completedAt string) error {
	r.fire()
	return r.WorkflowsRepo.MarkNodeCompleted(runID, nodeName, outputsJSON, completedAt)
}

func (r *writeHook) FinishNodeInTx(runID int64, nodeName, status string, merge func(*db.WorkflowRun) (string, string, error)) error {
	r.fire()
	return r.WorkflowsRepo.FinishNodeInTx(runID, nodeName, status, merge)
}

// Resume merges into state_json under the write lock, so a sibling that
// commits after Resume's reads and before its write keeps its outputs in
// state.
func TestResume_KeepsASiblingsOutputs(t *testing.T) {
	store := testStore(t)
	inner := store.Workflows()
	id := parkedReview(t, inner, reviewWithSibling)
	repo := &writeHook{WorkflowsRepo: inner, before: func() {
		if err := workflow.CompleteNode(inner, id, "sib", map[string]any{"kb": "B"}); err != nil {
			t.Fatal(err)
		}
	}}
	if err := workflow.ResumeHumanReview(repo, id, "approve", "ok"); err != nil {
		t.Fatal(err)
	}
	if repo.before != nil {
		t.Fatal("Resume wrote nothing through the repo")
	}
	st := readRun(t, inner, id)
	if st.state["kb"] != "B" || st.state["human_decision"] != "approve" || st.state["human_feedback"] != "ok" {
		t.Errorf("state = %v, want kb=B, human_decision=approve and human_feedback=ok", st.state)
	}
}

// Resume completed the node without its state_updates.
func TestResume_AppliesStateUpdatesOnlyWhenTheNodeCompletes(t *testing.T) {
	for _, decision := range []string{"approve", "redirect", "reject"} {
		store := testStore(t)
		repo := store.Workflows()
		id := parkedReview(t, repo, reviewWithSibling)
		if err := workflow.ResumeHumanReview(repo, id, decision, "fb"); err != nil {
			t.Fatal(err)
		}
		st := readRun(t, repo, id)
		got, applied := st.state["reviewed_by"]
		if decision == "reject" {
			if applied {
				t.Errorf("reject failed the node but applied its state_updates: reviewed_by=%v", got)
			}
		} else if got != decision {
			t.Errorf("%s: reviewed_by = %v, want %q", decision, got, decision)
		}
		if st.state["human_decision"] != decision || st.state["human_feedback"] != "fb" {
			t.Errorf("%s: state = %v", decision, st.state)
		}
	}
}

// In the manual loop the review node was handed out as running, and
// `workflow resume` then refused it: nothing was waiting.
func TestResume_AnswersAReviewReachedInTheManualLoop(t *testing.T) {
	store := testStore(t)
	repo := store.Workflows()
	id := startRun(t, repo, `name: t
version: 1
nodes:
  a: {type: agent, prompt: a}
  review: {type: human_review}
  b: {type: agent, prompt: b}
edges:
  - {from: a, to: review}
  - {from: review, to: b}
`)
	names := func() []string {
		t.Helper()
		next, err := workflow.GetNextNodesManual(repo, id)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range next {
			out = append(out, n.Node)
		}
		return out
	}
	if got := names(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("next = %v, want [a]", got)
	}
	if err := workflow.CompleteNode(repo, id, "a", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got := names(); len(got) != 0 {
		t.Fatalf("next = %v, want nothing while the review waits", got)
	}
	if err := workflow.ResumeHumanReview(repo, id, "redirect", "go left"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := names(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("after resume, next = %v, want [b]", got)
	}
}

func TestResume_RedirectAlsoCompletes(t *testing.T) {
	store := testStore(t)
	repo := store.Workflows()
	id := startRun(t, repo, oneReview)
	parkReview(t, store, id, "h")
	if err := workflow.ResumeHumanReview(repo, id, "redirect", "try harder"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if st := readRun(t, repo, id); st.nodes[0].Status != "completed" {
		t.Errorf("status = %q; want completed (redirect treats as completed)", st.nodes[0].Status)
	}
}

func TestResume_AfterDBClose(t *testing.T) {
	store := testStore(t)
	repo := store.Workflows()
	id := startRun(t, repo, oneReview)
	parkReview(t, store, id, "h")
	store.Close()
	if err := workflow.ResumeHumanReview(repo, id, "approve", ""); err == nil {
		t.Error("expected error after DB close")
	}
}

// A human_review node parks: it holds its successors pending, pauses the
// run, and `workflow resume` moves it on.
func TestHumanReview_PausesRunAndResumes(t *testing.T) {
	const defn = `name: hr
version: 1
nodes:
  first:
    type: agent
    prompt: do the thing
    outputs: [result]
  gate:
    type: human_review
  after:
    type: agent
    prompt: "carry on from {result}"
edges:
  - from: first
    to: gate
  - from: gate
    to: after
`
	dir := t.TempDir()
	path := filepath.Join(dir, "hr.yaml")
	if err := os.WriteFile(path, []byte(defn), 0o644); err != nil {
		t.Fatal(err)
	}
	store := testStore(t)
	repo := store.Workflows()

	runID, err := workflow.InitWorkflow(repo, path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.CompleteNode(repo, runID, "first", map[string]any{"result": "ok"}); err != nil {
		t.Fatal(err)
	}

	// The gate is dispatchable; parking it pauses the run.
	next, err := workflow.GetNextNodes(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Node != "gate" {
		t.Fatalf("next = %+v, want the gate", next)
	}
	if err := workflow.PauseForHuman(repo, runID, "gate"); err != nil {
		t.Fatal(err)
	}

	run, err := repo.GetWorkflowRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "paused" {
		t.Fatalf("run status = %q, want paused", run.Status)
	}
	// Paused: no work is handed out, and the successor has NOT run.
	next, err = workflow.GetNextNodes(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 0 {
		t.Fatalf("a paused run dispatched %+v", next)
	}
	if statusOf(t, repo, runID, "after") != "pending" {
		t.Fatalf("the successor ran while the gate was waiting")
	}

	// Approving completes the gate, returns the run to running, and the
	// successor becomes dispatchable with the upstream output in scope.
	if err := workflow.ResumeHumanReview(repo, runID, "approve", "looks good"); err != nil {
		t.Fatal(err)
	}
	run, _ = repo.GetWorkflowRun(runID)
	if run.Status != "running" {
		t.Fatalf("after resume, run status = %q, want running", run.Status)
	}
	next, err = workflow.GetNextNodes(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Node != "after" {
		t.Fatalf("after resume, next = %+v, want the successor", next)
	}
	if got := next[0].ResolvedPrompt; got != "carry on from ok" {
		t.Errorf("successor prompt = %q; upstream output should still resolve", got)
	}
	var _ workflow.Store = repo
}

func statusOf(t *testing.T, repo *db.WorkflowsRepo, runID int64, node string) string {
	t.Helper()
	states, err := repo.GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.NodeName == node {
			return s.Status
		}
	}
	t.Fatalf("node %q not found", node)
	return ""
}
