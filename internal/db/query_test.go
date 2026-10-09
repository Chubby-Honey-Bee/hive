package db

import (
	"database/sql"
	"testing"
)

func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestQueryToMaps(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(db *sql.DB) error
		query   string
		args    []any
		wantLen int
		wantNil bool
	}{
		{
			name: "happy path returns rows",
			setup: func(db *sql.DB) error {
				_, err := db.Exec(`CREATE TABLE t (id INTEGER, name TEXT)`)
				if err != nil {
					return err
				}
				_, err = db.Exec(`INSERT INTO t VALUES (1, 'alpha'), (2, 'beta')`)
				return err
			},
			query:   `SELECT id, name FROM t ORDER BY id`,
			wantLen: 2,
		},
		{
			name: "parameterized query filters correctly",
			setup: func(db *sql.DB) error {
				_, err := db.Exec(`CREATE TABLE u (id INTEGER, name TEXT)`)
				if err != nil {
					return err
				}
				_, err = db.Exec(`INSERT INTO u VALUES (1, 'alpha'), (2, 'beta')`)
				return err
			},
			query:   `SELECT id, name FROM u WHERE id = ?`,
			args:    []any{1},
			wantLen: 1,
		},
		{
			name: "empty result set returns empty slice",
			setup: func(db *sql.DB) error {
				_, err := db.Exec(`CREATE TABLE v (id INTEGER)`)
				return err
			},
			query:   `SELECT id FROM v`,
			wantLen: 0,
		},
		{
			name:    "invalid SQL returns nil",
			setup:   func(db *sql.DB) error { return nil },
			query:   `THIS IS NOT SQL`,
			wantNil: true,
		},
		{
			name:    "query against non-existent table returns nil",
			setup:   func(db *sql.DB) error { return nil },
			query:   `SELECT * FROM does_not_exist`,
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openMemDB(t)
			if err := tc.setup(db); err != nil {
				t.Fatalf("setup: %v", err)
			}
			got, err := QueryToMaps(db, tc.query, tc.args...)
			if tc.wantNil {
				if got != nil || err == nil {
					t.Fatalf("expected nil and an error, got %v, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("QueryToMaps: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("expected %d rows, got %d: %v", tc.wantLen, len(got), got)
			}
		})
	}
}

func TestQueryToMaps_ColumnValues(t *testing.T) {
	db := openMemDB(t)
	_, err := db.Exec(`CREATE TABLE kv (k TEXT, v INTEGER)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO kv VALUES ('foo', 42)`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := QueryToMaps(db, `SELECT k, v FROM kv`)
	if err != nil {
		t.Fatalf("QueryToMaps: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	if _, ok := row["k"]; !ok {
		t.Error("expected key 'k' in result map")
	}
	if _, ok := row["v"]; !ok {
		t.Error("expected key 'v' in result map")
	}
}
