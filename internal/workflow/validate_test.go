package workflow

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestValidate_FileNotFound(t *testing.T) {
	_, _, err := Validate("/missing/path.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestValidate_MissingName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(`nodes:
  a:
    type: agent
    prompt: "go"
    accept:
      - "outputs.ok == true"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, _, err := Validate(path)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	found := false
	for _, e := range errs {
		if containsValid(e, "Missing 'name'") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing-name error; got %v", errs)
	}
}

func TestValidate_NoNodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(`name: t
`), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, _, err := Validate(path)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	found := false
	for _, e := range errs {
		if containsValid(e, "No nodes defined") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected no-nodes error; got %v", errs)
	}
}

func TestValidate_InvalidNodeType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(`name: t
nodes:
  a:
    type: bogus_type
`), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, _, err := Validate(path)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	found := false
	for _, e := range errs {
		if containsValid(e, "invalid type") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected invalid-type error; got %v", errs)
	}
}

func TestValidate_DecisionMissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(`name: t
nodes:
  d:
    type: decision
`), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, _, err := Validate(path)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, want := range []string{"condition", "true_edge", "false_edge"} {
		found := false
		for _, e := range errs {
			if containsValid(e, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("expected decision-missing-%s error; got %v", want, errs)
		}
	}
}

func TestValidate_AgentMissingPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(`name: t
nodes:
  a:
    type: agent
`), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, _, err := Validate(path)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	found := false
	for _, e := range errs {
		if containsValid(e, "missing 'prompt'") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing-prompt error; got %v", errs)
	}
}

func TestValidate_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(`name: t
nodes:
  start:
    type: agent
    prompt: "do the thing"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, warns, err := Validate(path)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("expected no errors; got %v", errs)
	}
	_ = warns
}

func TestBuildGraph_ExplicitEdges(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"a": map[string]any{"type": "agent"},
			"b": map[string]any{"type": "agent"},
		},
		"edges": []any{
			map[string]any{"from": "a", "to": "b"},
		},
	}
	incoming, outgoing, err := BuildGraph(defn)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if len(incoming["b"]) != 1 || !incoming["b"]["a"] {
		t.Errorf("incoming[b] = %v; want {a:true}", incoming["b"])
	}
	if len(outgoing["a"]) != 1 || outgoing["a"][0].Target != "b" {
		t.Errorf("outgoing[a] = %v; want one edge to b", outgoing["a"])
	}
}

func TestBuildGraph_UnknownEdgeSource(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{"a": map[string]any{"type": "agent"}},
		"edges": []any{map[string]any{"from": "ghost", "to": "a"}},
	}
	_, _, err := BuildGraph(defn)
	if err == nil {
		t.Fatal("expected error for unknown source")
	}
}

func TestBuildGraph_UnknownEdgeTarget(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{"a": map[string]any{"type": "agent"}},
		"edges": []any{map[string]any{"from": "a", "to": "ghost"}},
	}
	_, _, err := BuildGraph(defn)
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
}

// containsValid is a tiny substring check kept local to avoid pulling in strings
// from each test file via dot-imports.
func containsValid(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Validate reported each node's problems in map order, so one definition's
// report came out in a different order on each run. It reports them in
// name order.
func TestValidate_ReportsNodesInNameOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wf.yaml")
	if err := os.WriteFile(path, []byte(`name: t
nodes:
  comb: {type: agent}
  alarm: {type: agent}
  brood: {type: agent}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`Agent node "alarm": missing 'prompt'`,
		`Agent node "brood": missing 'prompt'`,
		`Agent node "comb": missing 'prompt'`,
	}
	for i := range 10 {
		errs, _, err := Validate(path)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(errs, want) {
			t.Fatalf("validation %d reported %v, want %v", i+1, errs, want)
		}
	}
}
