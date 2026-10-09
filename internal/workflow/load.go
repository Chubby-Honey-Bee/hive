package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadYAML loads a workflow definition from a YAML file.
func LoadYAML(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow: %w", err)
	}
	return LoadYAMLString(string(data))
}

// LoadYAMLString parses a YAML string into a map.
func LoadYAMLString(text string) (map[string]any, error) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(text), &m); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	return m, nil
}

// ListWorkflows lists available workflow YAML files.
func ListWorkflows(dir string) ([]map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	for _, e := range entries {
		if !isWorkflowFile(e.Name()) {
			continue
		}
		result = append(result, workflowEntry(dir, e.Name()))
	}
	return result, nil
}

// isWorkflowFile reports whether a file name has a YAML extension.
func isWorkflowFile(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// workflowEntry is ListWorkflows' entry for one file: its name, version and
// node count, or the error that kept it from loading. A workflow with no
// name is named after its file.
func workflowEntry(dir, file string) map[string]any {
	defn, err := LoadYAML(filepath.Join(dir, file))
	if err != nil {
		return map[string]any{"file": file, "error": err.Error()}
	}
	name, _ := defn["name"].(string)
	if name == "" {
		name = strings.TrimSuffix(file, filepath.Ext(file))
	}
	nodes, _ := defn["nodes"].(map[string]any)
	return map[string]any{
		"file":    file,
		"name":    name,
		"version": defn["version"],
		"nodes":   len(nodes),
	}
}
