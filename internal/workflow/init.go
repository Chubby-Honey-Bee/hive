package workflow

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// InitWorkflow creates a new workflow run in the database. It takes a
// Store, the engine's narrow storage interface; the *db.WorkflowsRepo a
// store's Workflows returns satisfies it.
func InitWorkflow(repo Store, yamlPath string, inputs map[string]any) (int64, error) {
	rawYAML, err := os.ReadFile(yamlPath)
	if err != nil {
		return 0, fmt.Errorf("read workflow: %w", err)
	}
	defn, err := LoadYAMLString(string(rawYAML))
	if err != nil {
		return 0, err
	}
	if err := startProblem(defn, string(rawYAML), inputs); err != nil {
		return 0, err
	}
	inputsJSON, _ := json.Marshal(inputs)
	return repo.CreateWorkflowRun(runName(defn), runVersion(defn), string(rawYAML), string(inputsJSON), nodeSeeds(defn))
}

// startProblem runs the checks a run must pass before it starts, in order,
// and returns the first one that fails: the node fields, the required
// inputs, the reasoning levels and the output schemas.
func startProblem(defn map[string]any, text string, inputs map[string]any) error {
	checks := []func() error{
		func() error { return CheckNodeFields(defn) },
		func() error { return checkRequiredInputs(defn, inputs) },
		func() error { return CheckReasoning(defn) },
		func() error { _, err := OutputSchemas(text); return err },
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// runName is the workflow's name, unnamed when it gives none.
func runName(defn map[string]any) string {
	if name, _ := defn["name"].(string); name != "" {
		return name
	}
	return "unnamed"
}

// runVersion is the workflow's version, 1 when it gives no positive one.
func runVersion(defn map[string]any) int {
	if n := int(toFloat(defn["version"])); n > 0 {
		return n
	}
	return 1
}

// nodeSeeds are the node rows a new run starts with, one per node, in name
// order, so the rows list a definition's nodes the same way in every run.
func nodeSeeds(defn map[string]any) []db.NodeSeed {
	nodes, _ := defn["nodes"].(map[string]any)
	seeds := make([]db.NodeSeed, 0, len(nodes))
	for _, name := range slices.Sorted(maps.Keys(nodes)) {
		node, ok := nodes[name].(map[string]any)
		if !ok {
			continue
		}
		seeds = append(seeds, db.NodeSeed{Name: name, Type: nodeType(node)})
	}
	return seeds
}

// checkRequiredInputs refuses inputs that lack a variable the workflow's
// `inputs:` declares. Every way of starting a run goes through InitWorkflow,
// which calls it.
func checkRequiredInputs(defn map[string]any, inputs map[string]any) error {
	required, _ := defn["inputs"].([]any)
	for _, r := range required {
		name, _ := r.(string)
		if name == "" {
			continue
		}
		if _, ok := inputs[name]; !ok {
			return fmt.Errorf("missing required input: %s", name)
		}
	}
	return nil
}

// CheckReasoning refuses a workflow whose node sets `reasoning:` to anything
// but one of models.ReasoningLevels. Every way of starting a run calls it, so
// an unknown level never reaches a provider. Nodes are checked in name order.
func CheckReasoning(defn map[string]any) error {
	nodes, _ := defn["nodes"].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(nodes)) {
		node, _ := nodes[name].(map[string]any)
		if err := checkNodeReasoning(name, node); err != nil {
			return err
		}
	}
	return nil
}

// checkNodeReasoning refuses one node's `reasoning:` when it is set to
// anything but one of models.ReasoningLevels. A value YAML reads as no
// string, such as true or 3, is checked as written, and none is a level.
func checkNodeReasoning(name string, node map[string]any) error {
	raw, ok := node["reasoning"]
	if !ok {
		return nil
	}
	if err := models.CheckReasoningLevel(fmt.Sprint(raw)); err != nil {
		return fmt.Errorf("node %q: %w", name, err)
	}
	return nil
}
