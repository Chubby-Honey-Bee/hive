package workflow

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func newWFStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.NewStore(filepath.Join(t.TempDir(), "wf.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestInitWorkflow_FileNotFound(t *testing.T) {
	store := newWFStore(t)
	_, err := InitWorkflow(store.Workflows(), "/nonexistent.yaml", nil)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestInitWorkflow_BadYAML(t *testing.T) {
	store := newWFStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("\t::"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := InitWorkflow(store.Workflows(), path, nil)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestInitWorkflow_MissingRequiredInput(t *testing.T) {
	store := newWFStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(`name: t
inputs: [must_be_present]
nodes:
  a:
    type: agent
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := InitWorkflow(store.Workflows(), path, map[string]any{"unrelated": 1})
	if err == nil {
		t.Fatal("expected error for missing required input")
	}
}

func TestInitWorkflow_HappyPath(t *testing.T) {
	store := newWFStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(`name: t
version: 2
inputs: [foo]
nodes:
  a:
    type: agent
`), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := InitWorkflow(store.Workflows(), path, map[string]any{"foo": "bar"})
	if err != nil {
		t.Fatalf("InitWorkflow: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d; want >0", id)
	}
}

func TestGetNextNodes_RunNotFound(t *testing.T) {
	store := newWFStore(t)
	_, err := GetNextNodes(store.Workflows(), 99999)
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}

func TestGetNextNodes_NonRunningStatus(t *testing.T) {
	store := newWFStore(t)
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, `name: t
nodes:
  a:
    type: agent
`, `{}`, []db.NodeSeed{{Name: "a", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	// Mark the run completed so GetNextNodes short-circuits.
	if _, err := store.WriteDB.Exec(
		`UPDATE workflow_runs SET status='completed' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	got, err := GetNextNodes(store.Workflows(), id)
	if err != nil {
		t.Fatalf("GetNextNodes: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for completed run; got %v", got)
	}
}

// InitWorkflow inserts the node rows in name order, so `chb workflow
// status`, which lists them in row order, lists one definition's nodes in
// the same order on every run.
func TestInitWorkflow_SeedsNodesInNameOrder(t *testing.T) {
	repo := newWFStore(t).Workflows()
	const defn = `name: t
nodes:
  forage: {type: agent, prompt: f}
  brood: {type: agent, prompt: b}
  ripen: {type: agent, prompt: r}
  cap: {type: agent, prompt: c}
  dance: {type: agent, prompt: d}
  allot: {type: agent, prompt: a}
`
	want := []string{"allot", "brood", "cap", "dance", "forage", "ripen"}
	for i := range 5 {
		states, err := repo.GetWorkflowNodeStates(initYAML(t, repo, defn, nil))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, s := range states {
			got = append(got, s.NodeName)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("init %d lists nodes %v, want %v", i+1, got, want)
		}
	}
}
