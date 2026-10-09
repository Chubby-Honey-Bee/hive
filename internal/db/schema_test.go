package db

import (
	"database/sql"
	"fmt"
	"testing"
)

// openSchemaInitMemDB opens a fresh in-memory SQLite database for
// initSchema-level tests. Uses t.Name() so each test gets an isolated store.
func openSchemaInitMemDB(t *testing.T) *sql.DB {
	t.Helper()
	uri := fmt.Sprintf("file:schemainit_%s?mode=memory&cache=shared", t.Name())
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestInitSchema_HappyPath verifies that initSchema on a fresh in-memory DB
// creates all expected tables and returns nil.
func TestInitSchema_HappyPath(t *testing.T) {
	db := openSchemaInitMemDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema on fresh DB: %v", err)
	}
	for _, table := range []string{
		"findings", "gaps", "conflicts", "sources", "evaluations",
		"agent_runs", "wave_gates", "workflow_runs", "workflow_node_states",
		"signals", "hive_state", "comb_state", "time_wheel", "comb_revisions",
		"forager_bonds", "ripen_log", "comb_embeddings",
		"workflow_decisions", "workflow_repairs",
	} {
		exists, err := tableExists(db, table)
		if err != nil {
			t.Fatalf("tableExists(%q): %v", table, err)
		}
		if !exists {
			t.Errorf("expected table %q to exist after initSchema", table)
		}
	}
}

// TestInitSchema_Idempotent verifies that calling initSchema twice on the
// same DB is a no-op on the second call (CREATE TABLE IF NOT EXISTS semantics).
func TestInitSchema_Idempotent(t *testing.T) {
	db := openSchemaInitMemDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("first initSchema: %v", err)
	}
	if err := initSchema(db); err != nil {
		t.Fatalf("second initSchema (idempotency): %v", err)
	}
}

// The version stamp makes chb refuse a database a newer chb wrote, rather
// than create its own tables beside ones it does not understand, and a
// current database reopens cleanly.
func TestInitSchema_VersionStamp(t *testing.T) {
	db := openSchemaInitMemDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != SchemaVersion {
		t.Fatalf("user_version = %d after init, want %d", v, SchemaVersion)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := initSchema(db); err == nil {
		t.Fatal("initSchema accepted a database stamped newer than this binary")
	}
}

// Test helper. Production code stopped needing this when the migration
// chain was removed; tests still use it to assert on the schema.

func tableExists(db *sql.DB, name string) (bool, error) {
	var got string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
	).Scan(&got)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == name, nil
}

// A failing statement is named by its first 60 bytes, or whole when shorter:
// slicing every statement at 60 panicked on the 56-byte comb index.
func TestStatementHead_AShortStatementIsNamedWhole(t *testing.T) {
	short := "CREATE INDEX IF NOT EXISTS idx_comb_d1 ON comb_state(d1)"
	if got := statementHead(short); got != short {
		t.Errorf("statementHead(%q) = %q, want it whole", short, got)
	}
	long := short + short
	if got := statementHead(long); got != long[:60] {
		t.Errorf("statementHead of a %d-byte statement = %q, want its first 60 bytes", len(long), got)
	}
}
