package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// reasoningYAML is a one-node workflow whose node sets `reasoning: <value>`;
// value "" leaves the key out.
func reasoningYAML(value string) string {
	y := "name: t\nnodes:\n  think:\n    type: agent\n    prompt: go\n"
	if value != "" {
		y += "    reasoning: " + value + "\n"
	}
	return y
}

// TestReasoning_EveryStartPathChecksIt: a level in models.ReasoningLevels (or
// none at all) is accepted by Validate and InitWorkflow and reaches the
// dispatch node; anything else, an empty string and a null among them, is
// refused by both, naming the node.
func TestReasoning_EveryStartPathChecksIt(t *testing.T) {
	values := append([]string{""}, models.ReasoningLevels...)
	values = append(values, "extreme", "HIGH", "true", "3", `""`, "~")
	for _, v := range values {
		t.Run(fmt.Sprintf("%q", v), func(t *testing.T) {
			valid := v == "" || slices.Contains(models.ReasoningLevels, v)
			path := filepath.Join(t.TempDir(), "wf.yaml")
			if err := os.WriteFile(path, []byte(reasoningYAML(v)), 0o644); err != nil {
				t.Fatal(err)
			}

			errs, _, err := Validate(path)
			if err != nil {
				t.Fatal(err)
			}
			flagged := slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, `node "think": reasoning`) })
			if flagged == valid {
				t.Errorf("Validate flagged = %v, want %v (errors %v)", flagged, !valid, errs)
			}

			store := newWFStore(t)
			runID, err := InitWorkflow(store.Workflows(), path, nil)
			if !valid {
				if err == nil || !strings.Contains(err.Error(), `node "think": reasoning`) {
					t.Fatalf("InitWorkflow err = %v, want a refusal naming the node", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("InitWorkflow: %v", err)
			}
			nodes, err := GetNextNodes(store.Workflows(), runID)
			if err != nil || len(nodes) != 1 {
				t.Fatalf("GetNextNodes = %v, %v", nodes, err)
			}
			if nodes[0].Reasoning != v {
				t.Errorf("dispatch reasoning = %q, want %q", nodes[0].Reasoning, v)
			}
		})
	}
}
