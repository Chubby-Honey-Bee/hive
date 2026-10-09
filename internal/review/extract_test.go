package review

import (
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func reviewTestStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.NewStore(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestExtract_NilDB(t *testing.T) {
	_, err := Extract(nil, "audit-", 0)
	if err == nil {
		t.Fatal("expected error for nil DB")
	}
}

func TestExtract_DefaultPrefix(t *testing.T) {
	store := reviewTestStore(t)
	if _, err := Extract(store.ReadConn(), "", 0); err != nil {
		t.Fatalf("Extract empty prefix: %v", err)
	}
}

func TestExtract_NoMatchingRows(t *testing.T) {
	store := reviewTestStore(t)
	got, err := Extract(store.ReadConn(), "ghost-", 0)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got.ByLens) != 0 || len(got.AllFindings) != 0 {
		t.Errorf("expected empty aggregate; got %+v", got)
	}
	if len(got.ParseFailures) != 0 {
		t.Errorf("ParseFailures = %v; want empty", got.ParseFailures)
	}
}

func TestExtract_WithRows(t *testing.T) {
	store := reviewTestStore(t)
	// Create a workflow_run + nodes with rationale.
	id, err := store.Workflows().CreateWorkflowRun("t", 1, "name: t", "{}", []db.NodeSeed{
		{Name: "audit-v1", Type: "agent"},
		{Name: "audit-v2", Type: "agent"},
		{Name: "audit-broken", Type: "agent"},
		{Name: "non-audit-node", Type: "agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = id

	// Two parsable rationales + one unparsable + one not-audit (ignored).
	rationales := []struct {
		node, rationale string
	}{
		{"audit-v1", "```json\n" + `{"lens":"v1","verdict":"clean","findings":[{"file":"x.go","line":1,"severity":"high","issue":"i","fix":"f"}]}` + "\n```"},
		{"audit-v2", "```json\n" + `{"lens":"v2","verdict":"issues","findings":[{"file":"y.go","line":5,"severity":"medium","issue":"j","fix":"g"}]}` + "\n```"},
		{"audit-broken", "no json here"},
	}
	for _, r := range rationales {
		if _, err := store.WriteDB.Exec(
			`UPDATE workflow_node_states SET rationale = ? WHERE node_name = ?`,
			r.rationale, r.node); err != nil {
			t.Fatal(err)
		}
	}

	agg, err := Extract(store.ReadConn(), "audit-", 0)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(agg.ByLens) != 2 {
		t.Errorf("len(ByLens) = %d; want 2", len(agg.ByLens))
	}
	if len(agg.AllFindings) != 2 {
		t.Errorf("len(AllFindings) = %d; want 2", len(agg.AllFindings))
	}
	if len(agg.ParseFailures) != 1 || agg.ParseFailures[0] != "audit-broken" {
		t.Errorf("ParseFailures = %v; want [audit-broken]", agg.ParseFailures)
	}
	if agg.Totals["high"] != 1 {
		t.Errorf("Totals[high] = %d; want 1", agg.Totals["high"])
	}
	if agg.Totals["medium"] != 1 {
		t.Errorf("Totals[medium] = %d; want 1", agg.Totals["medium"])
	}
	// Should be sorted high before medium.
	if agg.AllFindings[0].Severity != "high" {
		t.Errorf("AllFindings[0].Severity = %q; want high", agg.AllFindings[0].Severity)
	}
}

// Two reviews in one database: the second must not resurrect the first's
// findings. Zero selects the most recent run; an explicit id selects that run.
func TestExtract_ScopedToOneRun(t *testing.T) {
	store := reviewTestStore(t)
	seed := func(node, lens, file string) int64 {
		t.Helper()
		id, err := store.Workflows().CreateWorkflowRun("t", 1, "name: t", "{}", []db.NodeSeed{{Name: node, Type: "agent"}})
		if err != nil {
			t.Fatal(err)
		}
		r := "```json\n" + `{"lens":"` + lens + `","verdict":"issues","findings":[{"file":"` + file + `","line":1,"severity":"high","issue":"i","fix":"f"}]}` + "\n```"
		if _, err := store.WriteDB.Exec(`UPDATE workflow_node_states SET rationale = ? WHERE run_id = ? AND node_name = ?`, r, id, node); err != nil {
			t.Fatal(err)
		}
		return id
	}
	run1 := seed("audit-one", "one", "first.go")
	run2 := seed("audit-two", "two", "second.go")

	latest, err := Extract(store.ReadConn(), "audit-", 0)
	if err != nil {
		t.Fatal(err)
	}
	if latest.RunID != run2 || len(latest.AllFindings) != 1 || latest.AllFindings[0].File != "second.go" {
		t.Errorf("default should read only the latest run (%d): got run %d, findings %+v", run2, latest.RunID, latest.AllFindings)
	}
	first, err := Extract(store.ReadConn(), "audit-", run1)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != run1 || len(first.AllFindings) != 1 || first.AllFindings[0].File != "first.go" {
		t.Errorf("explicit run %d: got run %d, findings %+v", run1, first.RunID, first.AllFindings)
	}
}
