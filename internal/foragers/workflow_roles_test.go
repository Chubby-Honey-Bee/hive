package foragers

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// Every node that calls a model names its role, so a routing profile routes
// it: scope, each lens, the evaluator, the follow-up fan and Queen. The
// human gate and a dreamer call none and name none.
func TestGenerateWorkflow_Roles(t *testing.T) {
	all, err := Load(filepath.Join("..", "..", "foragers"))
	if err != nil {
		t.Fatal(err)
	}
	swarm, err := Filter(all, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	synth, _ := ByName(all, "queen")
	text, err := GenerateWorkflow(swarm, WorkflowOptions{Scope: true, Evaluate: true, HumanGate: true, Synthesizer: synth})
	if err != nil {
		t.Fatal(err)
	}
	defn, err := workflow.LoadYAMLString(text)
	if err != nil {
		t.Fatal(err)
	}
	want := func(name string) string {
		switch {
		case name == "scope", name == "queen":
			return name
		case name == "swarm-evaluate":
			return "evaluate"
		case name == "swarm-followup":
			return "followup"
		case strings.HasPrefix(name, "forager-"):
			return "lens"
		}
		return ""
	}
	lensCount := 0
	for name, raw := range defn["nodes"].(map[string]any) {
		role, _ := raw.(map[string]any)["role"].(string)
		if w := want(name); role != w {
			t.Errorf("node %s role %q, want %q", name, role, w)
		}
		if role == "lens" {
			lensCount++
		}
	}
	lenses, _ := SplitByArchetype(swarm)
	if lensCount != len(lenses) {
		t.Errorf("%d lens nodes, want %d", lensCount, len(lenses))
	}
	if err := workflow.CheckNodeFields(defn); err != nil {
		t.Errorf("the generated roles are refused: %v", err)
	}
}
