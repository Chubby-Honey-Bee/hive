package foragers

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// swarm-followup fans over the gaps swarm-evaluate returns. A reply with no
// gaps key would leave the fan nothing under fan_source and run it once on
// the literal {gap}, so swarm-evaluate's accept: refuses a reply without
// gaps, and an on_reject: block repairs it. An empty list passes. The edges
// out of the evaluator test its gaps, so its verdict is computed: with none,
// the follow-up fan is skipped and queen is next; with some, the fan is next
// and dispatches the first Followups of them.
func TestGenerateWorkflow_EvaluateRequiresGaps(t *testing.T) {
	swarm := []Forager{
		{Name: "optimist", Title: "The Optimist", Description: "x", Body: "y"},
		{Name: "skeptic", Title: "The Skeptic", Description: "x", Body: "y"},
	}
	yamlText, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "eval-gaps", Evaluate: true})
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	defn, err := workflow.LoadYAMLString(yamlText)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if workflow.NodeOnRejectBlock(defn, "swarm-evaluate") == nil {
		t.Error("swarm-evaluate has no on_reject: block, so a rejected reply is not repaired")
	}
	path := filepath.Join(t.TempDir(), "swarm.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		outputs map[string]any
		accept  bool
	}{
		{"no gaps key", map[string]any{"coverage": 5.0}, false},
		{"null gaps", map[string]any{"coverage": 5.0, "gaps": nil}, false},
		{"empty gaps", map[string]any{"coverage": 5.0, "gaps": []any{}}, true},
		{"two gaps", map[string]any{"coverage": 3.0, "gaps": []any{"cost?", "risk?"}}, true},
		{"five gaps", map[string]any{"coverage": 2.0, "gaps": []any{"a?", "b?", "c?", "d?", "e?"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, err := db.NewStore(filepath.Join(t.TempDir(), "wf.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Init(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			repo := store.Workflows()
			runID, err := workflow.InitWorkflow(repo, path, map[string]any{"question": "q", "context": ""})
			if err != nil {
				t.Fatalf("init: %v", err)
			}
			for _, w := range swarm {
				if err := workflow.CompleteNode(repo, runID, "forager-"+w.Name, map[string]any{"verdict": "support"}); err != nil {
					t.Fatalf("complete forager-%s: %v", w.Name, err)
				}
			}

			err = workflow.CompleteNode(repo, runID, "swarm-evaluate", c.outputs)
			var rej *workflow.AcceptRejection
			if !c.accept {
				if !errors.As(err, &rej) {
					t.Fatalf("CompleteNode = %v, want an accept rejection", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CompleteNode: %v", err)
			}
			next, err := workflow.GetNextNodes(repo, runID)
			if err != nil {
				t.Fatal(err)
			}
			gaps := c.outputs["gaps"].([]any)
			want := "swarm-followup"
			if len(gaps) == 0 {
				want = "queen"
			}
			if len(next) != 1 || next[0].Node != want {
				t.Fatalf("next = %+v, want %s", next, want)
			}
			if len(gaps) == 0 {
				if st := nodeStatus(t, store, runID, "swarm-followup"); st != "skipped" {
					t.Errorf("swarm-followup is %s with no gaps, want skipped", st)
				}
				return
			}
			k := WorkflowOptions{}.WithDefaults().Followups
			var items []string
			for _, g := range gaps[:min(len(gaps), k)] {
				items = append(items, g.(string))
			}
			if !reflect.DeepEqual(next[0].FanItems, items) {
				t.Errorf("swarm-followup fan items %v, want the first %d of %v", next[0].FanItems, k, gaps)
			}
		})
	}
}

func nodeStatus(t *testing.T, store *db.Store, runID int64, node string) string {
	t.Helper()
	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.NodeName == node {
			return s.Status
		}
	}
	t.Fatalf("no node %s in run %d", node, runID)
	return ""
}
