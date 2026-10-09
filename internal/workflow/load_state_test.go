package workflow

import (
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func TestLoadNodeStateMap_DBError(t *testing.T) {
	store := newWFStore(t)
	store.Close()
	got := loadNodeStateMap(store.Workflows(), 1)
	if got != nil {
		t.Errorf("expected nil on DB error; got %v", got)
	}
}

func TestLoadNodeStateMap_HappyPath(t *testing.T) {
	store := newWFStore(t)
	id, err := store.Workflows().CreateWorkflowRun("t", 1, "name: t", "{}", []db.NodeSeed{
		{Name: "a", Type: "agent"},
		{Name: "b", Type: "decision"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := loadNodeStateMap(store.Workflows(), id)
	if len(got) != 2 {
		t.Errorf("len = %d; want 2", len(got))
	}
	if got["a"]["node_type"] != "agent" {
		t.Errorf("a.node_type = %v; want agent", got["a"]["node_type"])
	}
}

func TestLoadNodeStateMap_WithFailedNodes(t *testing.T) {
	store := newWFStore(t)
	id, err := store.Workflows().CreateWorkflowRun("t", 1, "name: t", "{}", []db.NodeSeed{
		{Name: "a", Type: "agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Workflows().MarkNodeFailed(id, "a", "boom", "2026-01-01T00:00:00Z", false); err != nil {
		t.Fatal(err)
	}
	got := loadNodeStateMap(store.Workflows(), id)
	if errPtr, ok := got["a"]["error"].(*string); !ok || errPtr == nil || *errPtr != "boom" {
		t.Errorf("a.error = %v; want pointer to 'boom'", got["a"]["error"])
	}
}
