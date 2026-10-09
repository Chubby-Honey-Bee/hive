package runner

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	calib "github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// seedDriftedDomain writes a definition at d1=2 and n guarantees on it,
// hits of them confirmed and the rest refuted by a human: at 10 of 12 the
// guarantee domain is calibrated and under the floor.
func seedDriftedDomain(t *testing.T, store *db.Store, n, hits int) {
	t.Helper()
	d1 := 2
	def, err := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "the base", D1: &d1})
	if err != nil {
		t.Fatal(err)
	}
	deps := "[" + strconv.FormatInt(def, 10) + "]"
	for i := 0; i < n; i++ {
		g, err := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "guarantee", Finding: "claim " + strings.Repeat("x", i+1), D1: &d1, DependsOnIDs: &deps})
		if err != nil {
			t.Fatal(err)
		}
		res := calib.Confirmed
		if i >= hits {
			res = calib.Refuted
		}
		if _, err := calib.Record(store, calib.Outcome{SubjectKind: calib.SubjectFinding, FindingID: g, Resolution: res, Source: calib.SourceHuman}); err != nil {
			t.Fatal(err)
		}
	}
}

func calibrateTicks(t *testing.T, store *db.Store) int {
	t.Helper()
	counts, err := store.TimeWheel().CountByKind()
	if err != nil {
		t.Fatal(err)
	}
	return counts[db.TickCalibrate]
}

func calibNodeRow(t *testing.T, store *db.Store, runID int64, name string) (status, rationale string, outputs map[string]any) {
	t.Helper()
	var outputsJSON, rat *string
	if err := store.ReadDB.QueryRow(
		`SELECT status, rationale, outputs_json FROM workflow_node_states WHERE run_id=? AND node_name=?`, runID, name,
	).Scan(&status, &rat, &outputsJSON); err != nil {
		t.Fatalf("node %s: %v", name, err)
	}
	if rat != nil {
		rationale = *rat
	}
	if outputsJSON != nil {
		_ = json.Unmarshal([]byte(*outputsJSON), &outputs)
	}
	return status, rationale, outputs
}

// The node runs the recompute under a calibrate tick and completes with
// calibration_drift_count and lowest_calibrated_lens; its rationale is the
// recompute's summary. A scope counts only that scope's drift; dry run
// completes without a tick.
func TestExecuteNode_CalibrateRunsTheRecompute(t *testing.T) {
	store := newTempStore(t)
	seedDriftedDomain(t, store, 12, 10)
	yamlStr := "name: cal\nnodes:\n  cal:\n    type: calibrate\n    outputs: [calibration_drift_count, lowest_calibrated_lens]\nedges: []\n"
	runID := seedExecuteNodeRun(t, store, "cal", "calibrate", yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	rc := buildExecuteNodeRC(t, store, runID, defn, &stubBackend{})

	if stop := rc.executeNode(workflow.DispatchNode{Node: "cal", Type: "calibrate"}); stop {
		t.Fatal("stop = true")
	}
	status, rationale, outputs := calibNodeRow(t, store, runID, "cal")
	if status != "completed" {
		t.Fatalf("status = %s", status)
	}
	// Global and d1=2 both hold the 12 outcomes at 10/12: two floor drifts.
	if outputs["calibration_drift_count"] != float64(2) || outputs["lowest_calibrated_lens"] != "" {
		t.Fatalf("outputs = %v, want drift 2 and no calibrated lens", outputs)
	}
	if calibrateTicks(t, store) != 1 {
		t.Fatalf("calibrate ticks = %d, want 1", calibrateTicks(t, store))
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(rationale), &summary); err != nil {
		t.Fatalf("rationale %q: %v", rationale, err)
	}
	if summary["tick_id"].(float64) == 0 || summary["skipped"] != false || len(summary["drift"].([]any)) != 2 || summary["new_outcomes"].(float64) != 12 {
		t.Fatalf("rationale = %v", summary)
	}
	if rc.res.NodesRun != 1 {
		t.Fatalf("NodesRun = %d", rc.res.NodesRun)
	}
	var state map[string]any
	run, err := store.Workflows().GetWorkflowRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(run.StateJSON), &state); err != nil {
		t.Fatal(err)
	}
	if state["calibration_drift_count"] != float64(2) {
		t.Fatalf("state = %v", state)
	}

	// Nothing new since the tick: the second run opens no tick and still
	// reports the standing floor drift.
	runID2 := seedExecuteNodeRun(t, store, "cal", "calibrate", yamlStr)
	rc2 := buildExecuteNodeRC(t, store, runID2, defn, &stubBackend{})
	rc2.executeNode(workflow.DispatchNode{Node: "cal", Type: "calibrate"})
	status, rationale, outputs = calibNodeRow(t, store, runID2, "cal")
	if status != "completed" || outputs["calibration_drift_count"] != float64(2) || calibrateTicks(t, store) != 1 {
		t.Fatalf("nothing new: status %s outputs %v ticks %d", status, outputs, calibrateTicks(t, store))
	}
	if err := json.Unmarshal([]byte(rationale), &summary); err != nil || summary["skipped"] != true {
		t.Fatalf("nothing new: rationale = %s (%v)", rationale, err)
	}

	// A scope counts its own drift: d1=9 has none, d1=2 has one; rebuild
	// opens a tick.
	for _, c := range []struct {
		scope string
		want  float64
	}{{"d1=9", 0}, {"d1=2", 1}, {"", 1}} {
		id := seedExecuteNodeRun(t, store, "cal", "calibrate", yamlStr)
		rcs := buildExecuteNodeRC(t, store, id, defn, &stubBackend{})
		rcs.executeNode(workflow.DispatchNode{Node: "cal", Type: "calibrate", Scope: c.scope, ScopeSet: true, Rebuild: true})
		if _, _, outputs = calibNodeRow(t, store, id, "cal"); outputs["calibration_drift_count"] != c.want {
			t.Fatalf("scope %q: outputs = %v, want drift %v", c.scope, outputs, c.want)
		}
	}
	if calibrateTicks(t, store) != 4 {
		t.Fatalf("calibrate ticks = %d, want 4 after three rebuilds", calibrateTicks(t, store))
	}

	// Dry run: completed with no recompute and no tick.
	id := seedExecuteNodeRun(t, store, "cal", "calibrate", yamlStr)
	rcd := buildExecuteNodeRC(t, store, id, defn, &stubBackend{})
	rcd.cfg.DryRun = true
	rcd.executeNode(workflow.DispatchNode{Node: "cal", Type: "calibrate", Rebuild: true})
	if status, _, outputs = calibNodeRow(t, store, id, "cal"); status != "completed" || outputs["dry_run"] != true || calibrateTicks(t, store) != 4 {
		t.Fatalf("dry run: status %s outputs %v ticks %d", status, outputs, calibrateTicks(t, store))
	}
}

// An accept: over the node's outputs rejects it, terminally, as a command
// node is rejected: the same ledger gives the same scores.
func TestExecuteNode_CalibrateAcceptRejects(t *testing.T) {
	store := newTempStore(t)
	seedDriftedDomain(t, store, 12, 10)
	yamlStr := "name: cal\nnodes:\n  cal:\n    type: calibrate\n    accept: [\"outputs.calibration_drift_count == 0\"]\nedges: []\n"
	runID := seedExecuteNodeRun(t, store, "cal", "calibrate", yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	rc := buildExecuteNodeRC(t, store, runID, defn, &stubBackend{})
	rc.executeNode(workflow.DispatchNode{Node: "cal", Type: "calibrate"})
	status, rationale, _ := calibNodeRow(t, store, runID, "cal")
	if status != "rejected" || !strings.Contains(rationale, "accept rejected") {
		t.Fatalf("status = %s, rationale %q", status, rationale)
	}
	if rc.res.NodesRun != 0 {
		t.Fatalf("NodesRun = %d", rc.res.NodesRun)
	}
}

// Through Run: the shipped shape, calibrate → decision → a command node on
// drift. With the domain drifted the report branch runs and the research
// branch is skipped; the node spent no tokens.
func TestRun_CalibrateNodeDrivesTheDecision(t *testing.T) {
	t.Setenv(commandHelperEnv, "1")
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(`
name: calibrated
version: 1
nodes:
  calibrate:
    type: calibrate
    outputs: [calibration_drift_count, lowest_calibrated_lens]
  drifted:
    type: decision
    condition: "calibration_drift_count > 0"
    true_edge: report-drift
    false_edge: research
  report-drift:
    type: command
    argv: [chb, print, '{"reported": true}']
    outputs_from: stdout_json
    outputs: [reported]
  research:
    type: command
    argv: [chb, print, '{"researched": true}']
    outputs_from: stdout_json
    outputs: [researched]
edges:
  - {from: calibrate, to: drifted}
  - {from: drifted, to: report-drift, condition: "calibration_drift_count > 0"}
  - {from: drifted, to: research, condition: "calibration_drift_count == 0"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newTempStore(t)
	seedDriftedDomain(t, store, 12, 10)
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML: wf, ProjectDir: dir, DBPath: store.Path, MaxIterations: 20, Backend: &stubBackend{}, Log: io.Discard,
	})
	if err != nil || res == nil {
		t.Fatalf("Run: %v (%+v)", err, res)
	}
	status, _, outputs := calibNodeRow(t, store, res.RunID, "calibrate")
	if status != "completed" || outputs["calibration_drift_count"] != float64(2) {
		t.Fatalf("calibrate: %s %v", status, outputs)
	}
	if s, _, _ := calibNodeRow(t, store, res.RunID, "report-drift"); s != "completed" {
		t.Fatalf("report-drift = %s, want completed", s)
	}
	if s, _, _ := calibNodeRow(t, store, res.RunID, "research"); s != "skipped" {
		t.Fatalf("research = %s, want skipped", s)
	}
	if calibrateTicks(t, store) != 1 {
		t.Fatalf("calibrate ticks = %d", calibrateTicks(t, store))
	}
	var tokens int64
	store.ReadDB.QueryRow(`SELECT COALESCE(SUM(tokens_in + tokens_out), 0) FROM workflow_node_states WHERE run_id=?`, res.RunID).Scan(&tokens)
	if tokens != 0 {
		t.Fatalf("the run spent %d tokens", tokens)
	}
}

// A calibrate node its cancelled run reaches does not start the recompute,
// which takes no context and would run to the end: the node goes back to
// pending with no attempt counted, and no calibrate tick opens.
func TestExecuteNode_CalibrateInACancelledRunDoesNotRecompute(t *testing.T) {
	store := newTempStore(t)
	seedDriftedDomain(t, store, 12, 10)
	yamlStr := "name: cal\nnodes:\n  cal:\n    type: calibrate\nedges: []\n"
	runID := seedExecuteNodeRun(t, store, "cal", "calibrate", yamlStr)
	if err := store.Workflows().MarkNodeRunning(runID, "cal", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	defn, _ := workflow.LoadYAMLString(yamlStr)
	rc := buildExecuteNodeRC(t, store, runID, defn, &stubBackend{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rc.ctx = ctx
	rc.executeNode(workflow.DispatchNode{Node: "cal", Type: "calibrate"})
	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatal(err)
	}
	if s := states[0]; s.Status != "pending" || s.Attempt != 0 {
		t.Errorf("the node is %s at attempt %d, want pending at attempt 0", s.Status, s.Attempt)
	}
	if n := calibrateTicks(t, store); n != 0 {
		t.Errorf("calibrate ticks = %d, want 0: the recompute ran", n)
	}
}
