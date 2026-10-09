package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// joinSwarm is two inputs fanning into a join, which one node follows.
func joinSwarm(join string) string {
	line := ""
	if join != "" {
		line = "    join: " + join + "\n"
	}
	return `name: join
nodes:
  a: {type: agent, prompt: a}
  b: {type: agent, prompt: b}
  j:
    type: agent
    prompt: j
` + line + `  after: {type: agent, prompt: after}
edges:
  - {from: a, to: j}
  - {from: b, to: j}
  - {from: j, to: after}
`
}

// settle finishes a node the way the runner does for each outcome.
func settle(t *testing.T, repo interface {
	MarkNodeRejected(int64, string, string, string) error
	MarkNodeFailed(int64, string, string, string, bool) error
}, runID int64, node, outcome string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	var err error
	switch outcome {
	case "rejected":
		err = repo.MarkNodeRejected(runID, node, "accept rejected", now)
	case "failed":
		err = repo.MarkNodeFailed(runID, node, "boom", now, false)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// A node with `join: settled` runs once each input has finished, one that
// failed or was rejected included, provided one completed. A failure it ran
// past does not fail the run once it completes; without the join, the same
// failure strands the node and fails the run.
func TestJoinSettled(t *testing.T) {
	cases := []struct {
		name     string
		join     string
		b        string // b's outcome: completed, rejected or failed
		a        string
		ready    bool   // j is handed out
		runAfter string // the run's status once every node is final
	}{
		{"settled past a rejected input", "settled", "rejected", "completed", true, "completed"},
		{"settled past a failed input", "settled", "failed", "completed", true, "completed"},
		{"settled with no input completed", "settled", "failed", "rejected", false, "failed"},
		{"default join and a rejected input", "", "rejected", "completed", false, "failed"},
		{"default join, both completed", "", "completed", "completed", true, "completed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newTestStore(t)
			repo := store.Workflows()
			runID, err := InitWorkflow(repo, writeWorkflow(t, joinSwarm(c.join)), nil)
			if err != nil {
				t.Fatal(err)
			}
			for node, outcome := range map[string]string{"a": c.a, "b": c.b} {
				if outcome == "completed" {
					if err := CompleteNode(repo, runID, node, map[string]any{"x": node}); err != nil {
						t.Fatal(err)
					}
					continue
				}
				settle(t, repo, runID, node, outcome)
			}
			next, err := GetNextNodes(repo, runID)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(next) == 1 && next[0].Node == "j"; got != c.ready {
				t.Fatalf("next = %+v, want j handed out = %v", next, c.ready)
			}
			if c.ready {
				for _, n := range []string{"j", "after"} {
					if err := CompleteNode(repo, runID, n, map[string]any{"y": n}); err != nil {
						t.Fatal(err)
					}
					if _, err := GetNextNodes(repo, runID); err != nil {
						t.Fatal(err)
					}
				}
			}
			run, _ := repo.GetWorkflowRun(runID)
			if run.Status != c.runAfter {
				t.Errorf("run is %s, want %s", run.Status, c.runAfter)
			}
		})
	}
}

// A settled join that itself fails leaves its inputs' failures unabsorbed:
// the run fails.
func TestJoinSettled_FailedJoinFailsTheRun(t *testing.T) {
	store := newTestStore(t)
	repo := store.Workflows()
	runID, err := InitWorkflow(repo, writeWorkflow(t, joinSwarm("settled")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompleteNode(repo, runID, "a", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	settle(t, repo, runID, "b", "rejected")
	if next, err := GetNextNodes(repo, runID); err != nil || len(next) != 1 {
		t.Fatalf("next = %+v, %v; want j", next, err)
	}
	settle(t, repo, runID, "j", "failed")
	if _, err := GetNextNodes(repo, runID); err != nil {
		t.Fatal(err)
	}
	run, _ := repo.GetWorkflowRun(runID)
	if run.Status != "failed" {
		t.Errorf("run is %s, want failed", run.Status)
	}
}

// A settled join whose every input was skipped is skipped too, and the run
// completes. The skip cascade reaches j and b in map order, and meeting j
// while b is still pending would leave j pending, so the walk repeats until
// it settles.
func TestJoinSettled_EveryInputSkipped(t *testing.T) {
	const src = `name: t
nodes:
  start: {type: agent, prompt: s}
  d:
    type: decision
    condition: "x == 1"
    true_edge: a
    false_edge: z
  a: {type: agent, prompt: a}
  b: {type: agent, prompt: b}
  j:
    type: agent
    prompt: j
    join: settled
  z: {type: agent, prompt: z}
edges:
  - {from: start, to: d}
  - {from: d, to: a}
  - {from: d, to: z}
  - {from: a, to: b}
  - {from: a, to: j}
  - {from: b, to: j}
`
	for i := 0; i < 20; i++ {
		repo := newTestStore(t).Workflows()
		runID, err := InitWorkflow(repo, writeWorkflow(t, src), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := CompleteNode(repo, runID, "start", map[string]any{"x": 0}); err != nil {
			t.Fatal(err)
		}
		next, err := GetNextNodes(repo, runID)
		if err != nil || len(next) != 1 || next[0].Node != "z" {
			t.Fatalf("run %d: next = %+v, %v; want z", i, next, err)
		}
		if err := CompleteNode(repo, runID, "z", map[string]any{"y": 1}); err != nil {
			t.Fatal(err)
		}
		if next, err := GetNextNodes(repo, runID); err != nil || len(next) != 0 {
			t.Fatalf("run %d: next = %+v, %v; want nothing", i, next, err)
		}
		for _, n := range []string{"a", "b", "j"} {
			if s := nodeStatus(t, repo, runID, n); s != "skipped" {
				t.Fatalf("run %d: %s is %s, want skipped", i, n, s)
			}
		}
		if run, _ := repo.GetWorkflowRun(runID); run.Status != "completed" {
			t.Fatalf("run %d is %s, want completed", i, run.Status)
		}
	}
}

// join: takes one value.
func TestCheckNodeFields_Join(t *testing.T) {
	if err := CheckNodeFields(mustDefn(t, joinSwarm("settled"))); err != nil {
		t.Errorf("join: settled refused: %v", err)
	}
	err := CheckNodeFields(mustDefn(t, joinSwarm("any")))
	if err == nil || !strings.Contains(err.Error(), "join: any") {
		t.Errorf("CheckNodeFields = %v, want join: any refused", err)
	}
}

// The overflow of a fan with fan_limit is written as soon as a node sets
// the fan's source, so it is in state however the fan ends. Here the fan
// fails and the node after it, which joins settled, reads every item past
// the limit.
func TestFanOverflow_WrittenWhenTheSourceIsSet(t *testing.T) {
	const limit = 2
	src := `name: t
nodes:
  eval: {type: agent, prompt: e, outputs: [gaps]}
  fan:
    type: parallel_fan
    prompt_template: "look at {gap}"
    fan_source: gaps
    fan_placeholder: "{gap}"
    fan_limit: 2
    fan_overflow: rest
  queen:
    type: agent
    join: settled
    prompt: "rest: {rest}"
edges:
  - {from: eval, to: fan}
  - {from: eval, to: queen}
  - {from: fan, to: queen}
`
	gaps := []any{"g1", "g2", "g3", "g4", "g5"}
	store := newTestStore(t)
	repo := store.Workflows()
	runID, err := InitWorkflow(repo, writeWorkflow(t, src), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompleteNode(repo, runID, "eval", map[string]any{"gaps": gaps}); err != nil {
		t.Fatal(err)
	}
	run, _ := repo.GetWorkflowRun(runID)
	var state map[string]any
	_ = json.Unmarshal([]byte(run.StateJSON), &state)
	want := append([]any{}, gaps[limit:]...)
	if !reflect.DeepEqual(state["rest"], want) {
		t.Fatalf("state rest after the source was set = %v, want %v", state["rest"], want)
	}

	if next, err := GetNextNodes(repo, runID); err != nil || len(next) != 1 || next[0].Node != "fan" {
		t.Fatalf("next = %+v, %v; want fan", next, err)
	}
	if err := FailNode(repo, runID, "fan", "parallel_fan failed"); err != nil {
		t.Fatal(err)
	}
	next, err := GetNextNodes(repo, runID)
	if err != nil || len(next) != 1 || next[0].Node != "queen" {
		t.Fatalf("next = %+v, %v; want queen past the failed fan", next, err)
	}
	wantJSON, _ := json.Marshal(want)
	if got := next[0].ResolvedPrompt; got != "rest: "+string(wantJSON) {
		t.Errorf("queen prompt %q, want the items past the limit %s", got, wantJSON)
	}
}
