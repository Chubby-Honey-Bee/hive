package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// An item with an unknown _type fails the ingest, and the findings directory
// defaults to findings/ beside the database.
func TestIngestFindings_UnknownTypeFailsAndDirFollowsTheDB(t *testing.T) {
	dir := t.TempDir()
	s, err := db.NewStore(filepath.Join(dir, "hive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	prevStore, prevPath := store, dbPath
	store, dbPath = s, filepath.Join(dir, "hive.db")
	t.Cleanup(func() { store, dbPath = prevStore, prevPath })

	if err := os.MkdirAll(filepath.Join(dir, "findings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "findings", "a.json"), []byte(`[{"_type":"gapp","description":"typo"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := newIngestFindingsCmd()
	cmd.SetArgs(nil)
	stdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	err = cmd.Execute()
	os.Stdout = stdout
	if err == nil {
		t.Fatal("an unknown _type was ingested without error")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "findings")) {
		t.Errorf("error %q does not name the findings/ beside the database", err)
	}
}
