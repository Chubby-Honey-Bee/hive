package runner

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// newDreamerTestStore returns an initialised temp store.
func newDreamerTestStore(t *testing.T) *db.Store {
	t.Helper()
	store := newTempStore(t) // defined in runner_test.go
	return store
}

// newDreamerRC builds a minimal runtimeContext for dreamer-node tests.
func newDreamerRC(store *db.Store, runID int64, dryRun bool) *runtimeContext {
	return &runtimeContext{
		ctx:   context.Background(),
		cfg:   Config{DryRun: dryRun},
		store: store,
		runID: runID,
		logf:  makeLogf(&bytes.Buffer{}),
		res:   &Result{},
		resMu: sync.Mutex{},
	}
}

// seedDreamerRun creates a workflow_run row with a single dreamer node.
func seedDreamerRun(t *testing.T, store *db.Store, nodeName string) int64 {
	t.Helper()
	// Minimal definition YAML — CompleteNode parses it to merge outputs.
	defnYAML := "name: dreamer-test\nnodes:\n  " + nodeName + ":\n    type: agent\n    archetype: dreamer\n"
	runID, err := store.Workflows().CreateWorkflowRun(
		"dreamer-test", 1, defnYAML, "{}",
		[]db.NodeSeed{{Name: nodeName, Type: "agent"}},
	)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return runID
}

// TestExecuteDreamerNode_DryRun verifies the dry-run branch: node is
// marked completed with dry_run=true without touching the dreamer loop.
func TestExecuteDreamerNode_DryRun(t *testing.T) {
	store := newDreamerTestStore(t)
	const nodeName = "dreamer-1"
	runID := seedDreamerRun(t, store, nodeName)

	rc := newDreamerRC(store, runID, true /* dryRun */)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Archetype: "dreamer"}

	stop := rc.executeDreamerNode(node)
	if stop {
		t.Errorf("expected stop=false for dry-run path, got true")
	}

	// Confirm the node row was marked completed (MarkNodeCompleted was called).
	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatalf("GetWorkflowNodeStates: %v", err)
	}
	if len(states) == 0 {
		t.Fatal("no node states found")
	}
	found := false
	for _, s := range states {
		if s.NodeName == nodeName && s.Status == "completed" {
			found = true
		}
	}
	if !found {
		t.Errorf("node %q was not marked completed after dry-run; states=%v", nodeName, states)
	}

	// NodesRun must NOT be incremented in the dry-run path.
	if rc.res.NodesRun != 0 {
		t.Errorf("NodesRun=%d after dry-run, want 0", rc.res.NodesRun)
	}
}

// TestExecuteDreamerNode_HappyPath verifies that the dreamer loop runs,
// the node is marked completed, and NodesRun is incremented.
func TestExecuteDreamerNode_HappyPath(t *testing.T) {
	store := newDreamerTestStore(t)
	const nodeName = "dreamer-2"
	runID := seedDreamerRun(t, store, nodeName)

	rc := newDreamerRC(store, runID, false /* dryRun */)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Archetype: "dreamer"}

	stop := rc.executeDreamerNode(node)
	if stop {
		t.Errorf("expected stop=false on happy path, got true")
	}

	// Node must be completed.
	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatalf("GetWorkflowNodeStates: %v", err)
	}
	var found bool
	for _, s := range states {
		if s.NodeName == nodeName && s.Status == "completed" {
			found = true
		}
	}
	if !found {
		t.Errorf("node %q not completed after happy path; states=%v", nodeName, states)
	}

	// NodesRun MUST be incremented exactly once.
	if rc.res.NodesRun != 1 {
		t.Errorf("NodesRun=%d, want 1", rc.res.NodesRun)
	}
}

// TestExecuteDreamerNode_WithForagerName ensures the ForagerName branch
// (Comb vantage write) executes without error.
func TestExecuteDreamerNode_WithForagerName(t *testing.T) {
	store := newDreamerTestStore(t)
	const nodeName = "dreamer-3"
	runID := seedDreamerRun(t, store, nodeName)

	rc := newDreamerRC(store, runID, false /* dryRun */)
	node := workflow.DispatchNode{
		Node:        nodeName,
		Type:        "agent",
		Archetype:   "dreamer",
		ForagerName: "dreamer", // triggers Comb forager-vantage write
	}

	stop := rc.executeDreamerNode(node)
	if stop {
		t.Errorf("expected stop=false with ForagerName set, got true")
	}
	if rc.res.NodesRun != 1 {
		t.Errorf("NodesRun=%d, want 1", rc.res.NodesRun)
	}
	// abstain: the dreamer holds no position a resonates bond could match.
	if row, err := store.Comb().Get("forager:dreamer"); err != nil || row == nil || row.Confidence != 30 {
		t.Errorf("dreamer vantage = %+v, %v; want abstain (confidence 30)", row, err)
	}
}

// TestDispatchDreamer_HappyPath calls dispatchDreamer directly with a
// fresh empty store. All five passes run on a clean DB; the function
// must return a non-nil outcome and nil error.
func TestDispatchDreamer_HappyPath(t *testing.T) {
	store := newDreamerTestStore(t)
	out, err := dispatchDreamer(context.Background(), store)
	if err != nil {
		t.Fatalf("dispatchDreamer: unexpected error: %v", err)
	}
	if out == nil {
		t.Fatal("dispatchDreamer returned nil outcome")
	}
	// Five standard passes should have been attempted on a fresh DB.
	if len(out.PassNames) == 0 {
		t.Error("PassNames is empty; expected at least one pass name")
	}
	// Clean DB: no halt expected.
	if out.Halted {
		t.Errorf("Halted=true on clean DB; HaltReason=%q", out.HaltReason)
	}
}

// TestDispatchDreamer_ErrHaltedSuppressed verifies that when dreamer.Run
// returns ErrHalted (QMP trigger from a laundering violation),
// dispatchDreamer converts it to a nil error and surfaces the halt via
// outcome.Halted + outcome.HaltReason. The violation is injected via raw
// SQL to bypass Go-level write-time enforcement.
func TestDispatchDreamer_ErrHaltedSuppressed(t *testing.T) {
	store := newDreamerTestStore(t)

	// Insert an unknown finding — the laundering anchor.
	_, err := store.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, mss_label, finding) VALUES (1, 'test', 'unknown', 'raw unknown fact')`,
	)
	if err != nil {
		t.Fatalf("insert unknown finding: %v", err)
	}
	var unknownID int64
	if err := store.WriteDB.QueryRow(`SELECT last_insert_rowid()`).Scan(&unknownID); err != nil {
		t.Fatalf("get unknown ID: %v", err)
	}

	// Insert a guarantee whose dependency chain reaches the unknown above.
	// This bypasses Go-level MSS enforcement so the violation lives in DB.
	depsJSON := fmt.Sprintf("[%d]", unknownID)
	_, err = store.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, mss_label, finding, depends_on_ids) VALUES (1, 'test', 'guarantee', 'derived claim', ?)`,
		depsJSON,
	)
	if err != nil {
		t.Fatalf("insert guarantee finding: %v", err)
	}

	// QMP should detect laundering, halt cleanly, and dispatchDreamer must
	// suppress dreamer.ErrHalted — returning nil error and Halted=true.
	out, err := dispatchDreamer(context.Background(), store)
	if err != nil {
		t.Fatalf("dispatchDreamer returned non-nil error; ErrHalted must be suppressed: %v", err)
	}
	if out == nil {
		t.Fatal("dispatchDreamer returned nil outcome on QMP halt")
	}
	if !out.Halted {
		t.Error("Halted=false after QMP trigger; want true")
	}
	if out.HaltReason == "" {
		t.Error("HaltReason is empty after QMP halt; want non-empty")
	}
}

// A dreamer that halted names the halt in its vantage.
func TestExecuteDreamerNode_HaltedVantageAbstains(t *testing.T) {
	store := newDreamerTestStore(t)
	res, err := store.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, mss_label, finding) VALUES (1, 'test', 'unknown', 'raw unknown fact')`,
	)
	if err != nil {
		t.Fatalf("insert unknown finding: %v", err)
	}
	unknownID, _ := res.LastInsertId()
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, mss_label, finding, depends_on_ids) VALUES (1, 'test', 'guarantee', 'derived claim', ?)`,
		fmt.Sprintf("[%d]", unknownID),
	); err != nil {
		t.Fatalf("insert guarantee finding: %v", err)
	}

	const nodeName = "dreamer-halt"
	runID := seedDreamerRun(t, store, nodeName)
	rc := newDreamerRC(store, runID, false)
	rc.executeDreamerNode(workflow.DispatchNode{Node: nodeName, Type: "agent", Archetype: "dreamer", ForagerName: "dreamer"})

	row, err := store.Comb().Get("forager:dreamer")
	if err != nil || row == nil {
		t.Fatalf("forager vantage: row=%v err=%v", row, err)
	}
	if row.Confidence != 30 || !strings.Contains(row.Narrative, "halted (laundering)") {
		t.Errorf("halted dreamer vantage: confidence=%d narrative=%q, want 30 and a halt", row.Confidence, row.Narrative)
	}
}

// TestExecuteDreamerNode_CompleteNodeFails covers the branch where
// CompleteNode returns an error (nonexistent run ID). The dreamer loop
// itself still runs successfully; only the output recording fails.
// The function must return false (not stop) and must not panic.
func TestExecuteDreamerNode_CompleteNodeFails(t *testing.T) {
	store := newDreamerTestStore(t)

	const badRunID = int64(999999)
	rc := newDreamerRC(store, badRunID, false /* dryRun */)
	node := workflow.DispatchNode{Node: "dreamer-orphan", Type: "agent", Archetype: "dreamer"}

	stop := rc.executeDreamerNode(node)
	if stop {
		t.Errorf("expected stop=false even when CompleteNode fails, got true")
	}

	// NodesRun must NOT be incremented — we never reached that line.
	if rc.res.NodesRun != 0 {
		t.Errorf("NodesRun=%d after CompleteNode failure, want 0", rc.res.NodesRun)
	}
}
