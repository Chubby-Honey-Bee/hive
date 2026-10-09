package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRecordCascadeGap covers the happy paths and key branches of RecordCascadeGap.
func TestRecordCascadeGap(t *testing.T) {
	tests := []struct {
		name        string
		wave        int
		description string
		d1, d2, d3  *int
		d4          *int
	}{
		{
			name:        "all coordinates provided",
			wave:        1,
			description: "dep invalidated by cascade",
			d1:          intPtr(1),
			d2:          intPtr(2),
			d3:          intPtr(3),
			d4:          intPtr(4),
		},
		{
			name:        "nil coordinates",
			wave:        2,
			description: "cascade gap with no coords",
			d1:          nil,
			d2:          nil,
			d3:          nil,
			d4:          nil,
		},
		{
			name:        "partial coordinates",
			wave:        3,
			description: "partial coord gap",
			d1:          intPtr(7),
			d2:          nil,
			d3:          intPtr(5),
			d4:          nil,
		},
		{
			name:        "wave zero",
			wave:        0,
			description: "wave zero edge case",
			d1:          nil,
			d2:          nil,
			d3:          nil,
			d4:          nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)

			err := s.RecordCascadeGap(tc.wave, tc.description, tc.d1, tc.d2, tc.d3, tc.d4)
			if err != nil {
				t.Fatalf("RecordCascadeGap returned unexpected error: %v", err)
			}

			// Verify the row was inserted with the expected fields.
			var wave int
			var agent, description, priority string
			row := s.ReadDB.QueryRow(
				"SELECT wave, agent, description, priority FROM gaps WHERE description=?",
				tc.description,
			)
			if err := row.Scan(&wave, &agent, &description, &priority); err != nil {
				t.Fatalf("scan gap row: %v", err)
			}
			if wave != tc.wave {
				t.Errorf("wave: got %d, want %d", wave, tc.wave)
			}
			if agent != "hive-alarm" {
				t.Errorf("agent: got %q, want %q", agent, "hive-alarm")
			}
			if description != tc.description {
				t.Errorf("description: got %q, want %q", description, tc.description)
			}
			if priority != "critical" {
				t.Errorf("priority: got %q, want %q", priority, "critical")
			}
		})
	}
}

// TestRecordCascadeGap_ErrorPath verifies that a write error is propagated
// and wrapped. We trigger it by closing the write connection before calling.
func TestRecordCascadeGap_ErrorPath(t *testing.T) {
	s := newTestStore(t)
	// Close the write DB so the INSERT will fail.
	s.WriteDB.Close()

	err := s.RecordCascadeGap(1, "should fail", intPtr(0), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error after closing WriteDB, got nil")
	}
}

// TestRevertFindingToUnknown covers the happy path and error paths of
// RevertFindingToUnknown.
func TestRevertFindingToUnknown(t *testing.T) {
	t.Run("happy path: guarantee reverted to unknown", func(t *testing.T) {
		s := newTestStore(t)
		// Insert a dependency first so the guarantee can reference it.
		depID := addTestFinding(t, s, "definition", "base definition", nil)
		id := addTestFinding(t, s, "guarantee", "some guarantee", []int64{depID})

		if err := s.RevertFindingToUnknown(id); err != nil {
			t.Fatalf("RevertFindingToUnknown returned unexpected error: %v", err)
		}

		// Verify the row was updated.
		var label string
		var depsJSON *string
		row := s.ReadDB.QueryRow("SELECT mss_label, depends_on_ids FROM findings WHERE id=?", id)
		if err := row.Scan(&label, &depsJSON); err != nil {
			t.Fatalf("scan finding: %v", err)
		}
		if label != "unknown" {
			t.Errorf("mss_label: got %q, want %q", label, "unknown")
		}
		if depsJSON != nil {
			t.Errorf("depends_on_ids: got %v, want nil", depsJSON)
		}
	})

	t.Run("assumption reverted to unknown", func(t *testing.T) {
		s := newTestStore(t)
		id := addTestFinding(t, s, "assumption", "some assumption", nil)

		if err := s.RevertFindingToUnknown(id); err != nil {
			t.Fatalf("RevertFindingToUnknown returned unexpected error: %v", err)
		}

		var label string
		row := s.ReadDB.QueryRow("SELECT mss_label FROM findings WHERE id=?", id)
		if err := row.Scan(&label); err != nil {
			t.Fatalf("scan finding: %v", err)
		}
		if label != "unknown" {
			t.Errorf("mss_label: got %q, want %q", label, "unknown")
		}
	})

	t.Run("non-existent id: no error (0 rows affected is ok)", func(t *testing.T) {
		s := newTestStore(t)
		// ID 9999 does not exist; UPDATE affects 0 rows but should not error.
		if err := s.RevertFindingToUnknown(9999); err != nil {
			t.Fatalf("RevertFindingToUnknown on missing id returned error: %v", err)
		}
	})

	t.Run("error path: closed write DB", func(t *testing.T) {
		s := newTestStore(t)
		s.WriteDB.Close()

		err := s.RevertFindingToUnknown(1)
		if err == nil {
			t.Fatal("expected error after closing WriteDB, got nil")
		}
	})
}

// TestEmitAlarmSignal covers the happy paths and error paths of EmitAlarmSignal.
func TestEmitAlarmSignal(t *testing.T) {
	tests := []struct {
		name      string
		sourceID  int64
		d1, d2    *int
		d3, d4    *int
		payload   map[string]any
		wave      int
		wantErr   bool
		errSubstr string
	}{
		{
			name:     "happy path with all coordinates",
			sourceID: 1,
			d1:       intPtr(1),
			d2:       intPtr(2),
			d3:       intPtr(3),
			d4:       intPtr(4),
			payload:  map[string]any{"reason": "dep contradicted", "dep_id": 7},
			wave:     1,
		},
		{
			name:     "happy path nil coordinates",
			sourceID: 2,
			d1:       nil,
			d2:       nil,
			d3:       nil,
			d4:       nil,
			payload:  map[string]any{"reason": "cascade alarm"},
			wave:     2,
		},
		{
			name:     "happy path empty payload",
			sourceID: 3,
			d1:       intPtr(0),
			d2:       nil,
			d3:       nil,
			d4:       nil,
			payload:  map[string]any{},
			wave:     3,
		},
		{
			name:     "happy path nil payload marshals to null",
			sourceID: 4,
			d1:       nil,
			d2:       nil,
			d3:       nil,
			d4:       nil,
			payload:  nil,
			wave:     4,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)

			err := s.EmitAlarmSignal(tc.sourceID, tc.d1, tc.d2, tc.d3, tc.d4, tc.payload, tc.wave)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errSubstr)
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("expected error %q to contain %q", err.Error(), tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("EmitAlarmSignal returned unexpected error: %v", err)
			}

			// Verify the row was inserted with the expected fields.
			var sigType, srcType string
			var srcID, wave int64
			row := s.ReadDB.QueryRow(
				`SELECT signal_type, source_type, source_id, wave
				 FROM signals WHERE source_id=? AND wave=?`,
				tc.sourceID, tc.wave,
			)
			if err := row.Scan(&sigType, &srcType, &srcID, &wave); err != nil {
				t.Fatalf("scan signal row: %v", err)
			}
			if sigType != "alarm" {
				t.Errorf("signal_type: got %q, want %q", sigType, "alarm")
			}
			if srcType != "finding" {
				t.Errorf("source_type: got %q, want %q", srcType, "finding")
			}
			if srcID != tc.sourceID {
				t.Errorf("source_id: got %d, want %d", srcID, tc.sourceID)
			}
			if wave != int64(tc.wave) {
				t.Errorf("wave: got %d, want %d", wave, tc.wave)
			}
		})
	}
}

// TestEmitAlarmSignal_UnmarshalablePayload verifies that a payload containing
// a non-JSON-serializable value (e.g. a channel) causes the marshal error
// path to be exercised and returns a wrapped error.
func TestEmitAlarmSignal_UnmarshalablePayload(t *testing.T) {
	s := newTestStore(t)
	// A channel cannot be marshalled to JSON — triggers the json.Marshal error branch.
	payload := map[string]any{"bad": make(chan int)}
	err := s.EmitAlarmSignal(1, nil, nil, nil, nil, payload, 1)
	if err == nil {
		t.Fatal("expected marshal error, got nil")
	}
	if !strings.Contains(err.Error(), "marshal alarm payload") {
		t.Errorf("expected error to contain %q, got: %v", "marshal alarm payload", err)
	}
}

// TestEmitAlarmSignal_WriteDBClosed verifies that a DB write error is propagated
// and wrapped with the expected prefix.
func TestEmitAlarmSignal_WriteDBClosed(t *testing.T) {
	s := newTestStore(t)
	s.WriteDB.Close()

	err := s.EmitAlarmSignal(1, nil, nil, nil, nil, map[string]any{"k": "v"}, 1)
	if err == nil {
		t.Fatal("expected error after closing WriteDB, got nil")
	}
	if !strings.Contains(err.Error(), "emit alarm signal") {
		t.Errorf("expected error to contain %q, got: %v", "emit alarm signal", err)
	}
}

func TestLoadDependencyEdges_OnlyReturnsWithDeps(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "deps.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	defID, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", D1: &d1, Finding: "base",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "guarantee", D1: &d1,
		Finding: "derived",
		// We have to set DependsOnIDs as JSON string.
	}); err != nil {
		// Expected to fail because guarantees need deps.
		_ = err
	}

	// Add another with deps via direct SQL.
	if _, err := store.WriteDB.Exec(
		`UPDATE findings SET depends_on_ids = ? WHERE id = ?`,
		`[1]`, defID); err != nil {
		t.Fatal(err)
	}

	edges, err := store.LoadDependencyEdges()
	if err != nil {
		t.Fatalf("LoadDependencyEdges: %v", err)
	}
	// Only rows with non-NULL depends_on_ids return.
	if len(edges) == 0 {
		t.Errorf("expected at least 1 edge")
	}
}

func TestLoadDependencyEdges_MalformedDepsJSONSkipped(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "deps.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	id, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", D1: &d1, Finding: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Set depends_on_ids to malformed JSON.
	if _, err := store.WriteDB.Exec(
		`UPDATE findings SET depends_on_ids = ? WHERE id = ?`,
		`not-json`, id); err != nil {
		t.Fatal(err)
	}

	edges, err := store.LoadDependencyEdges()
	if err != nil {
		t.Fatalf("LoadDependencyEdges: %v", err)
	}
	// The malformed row was skipped (best-effort).
	for _, e := range edges {
		if e.ID == id {
			t.Errorf("malformed-deps row should be skipped; got %v", e)
		}
	}
}

func TestLoadDependencyEdges_EmptyDB(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	edges, err := store.LoadDependencyEdges()
	if err != nil {
		t.Fatalf("LoadDependencyEdges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("len = %d; want 0", len(edges))
	}
}

// TestLoadDependencyEdges_QueryError covers the error path when the initial
// Query call fails (e.g. the read connection has been closed).
func TestLoadDependencyEdges_QueryError(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "qerr.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	// Close the read connection so the SQL query will fail.
	store.ReadDB.Close()
	defer store.Close()

	_, err = store.LoadDependencyEdges()
	if err == nil {
		t.Fatal("expected error from closed ReadDB, got nil")
	}
	if !strings.Contains(err.Error(), "load dependency edges") {
		t.Errorf("expected error to contain %q, got: %v", "load dependency edges", err)
	}
}
