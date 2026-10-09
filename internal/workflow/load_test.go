package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadYAML_FileNotFound(t *testing.T) {
	_, err := LoadYAML("/nonexistent/path/foo.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadYAML_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	content := `name: test
version: 1
inputs: []
nodes:
  a:
    type: agent
    agent: x
    model: haiku
    prompt: 'go'
    outputs: [ok]
    accept:
      - "outputs.ok == true"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	defn, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if defn["name"] != "test" {
		t.Errorf("name = %v; want test", defn["name"])
	}
}

func TestLoadYAML_BadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("\tnot:valid:yaml\t::"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadYAML(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadYAMLString_Empty(t *testing.T) {
	defn, err := LoadYAMLString("")
	if err != nil {
		t.Fatalf("LoadYAMLString empty: %v", err)
	}
	if defn != nil {
		t.Errorf("got %v; want nil", defn)
	}
}

func TestLoadYAMLString_TopLevelList(t *testing.T) {
	// Top-level list is a parse-but-wrong-shape. Should error since
	// the target is map[string]any.
	_, err := LoadYAMLString("- a\n- b\n")
	if err == nil {
		t.Fatal("expected error for top-level list")
	}
}

func TestLoadYAMLString_HappyPath(t *testing.T) {
	yaml := `name: foo
version: 2
nodes:
  x:
    type: agent
`
	defn, err := LoadYAMLString(yaml)
	if err != nil {
		t.Fatalf("LoadYAMLString: %v", err)
	}
	if defn["name"] != "foo" {
		t.Errorf("name = %v; want foo", defn["name"])
	}
}
