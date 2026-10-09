package db

import (
	"path/filepath"
	"testing"
)

// TestStoreTypedAccessors_AllNonNil verifies that NewStore wires up
// every per-entity repo. Acts as a guard so a future Store change
// can't silently drop a repo from the constructor.
func TestStoreTypedAccessors_AllNonNil(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "accessors.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		got  any
	}{
		{"Findings", store.Findings()},
		{"Gaps", store.Gaps()},
		{"Conflicts", store.Conflicts()},
		{"Sources", store.Sources()},
		{"Evaluations", store.Evaluations()},
		{"Dimensions", store.Dimensions()},
		{"Followups", store.Followups()},
		{"AgentRuns", store.AgentRuns()},
		{"Signals", store.Signals()},
		{"Workflows", store.Workflows()},
		{"ForagerBonds", store.ForagerBonds()},
	}
	for _, tc := range cases {
		if tc.got == nil {
			t.Errorf("%s() returned nil — repo not wired in NewStore", tc.name)
		}
	}
}

// TestRepoMethodsPromoteOntoStore is the structural guarantee the repo
// split rests on: store.Findings().AddFinding(...) must work transparently
// because the embedded *FindingsRepo promotes it. If embedding ever breaks
// (e.g., someone deletes the embedded field), this test catches it.
func TestRepoMethodsPromoteOntoStore(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "promotion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}

	// Promoted from FindingsRepo
	id, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "test", MSSLabel: "definition", Finding: "promoted",
	})
	if err != nil || id == 0 {
		t.Errorf("store.AddFinding (promoted) failed: id=%d err=%v", id, err)
	}

	// Promoted from GapsRepo
	if err := store.Gaps().AddGap(1, "test", "x", "important", nil, nil, nil, nil); err != nil {
		t.Errorf("store.AddGap (promoted) failed: %v", err)
	}

	// Promoted from DimensionsRepo
	if err := store.Dimensions().AddDimension("d1_test", "x", `["a","b"]`); err != nil {
		t.Errorf("store.AddDimension (promoted) failed: %v", err)
	}
	dims, _ := store.Dimensions().GetDimensions()
	if len(dims) != 1 {
		t.Errorf("expected 1 dimension via promoted GetDimensions, got %d", len(dims))
	}
}

// TestTypedAccessorsAndPromotionAreEquivalent ensures store.Findings()
// returns the same repo whose methods get promoted. Both paths must
// hit the same DB rows.
func TestTypedAccessorsAndPromotionAreEquivalent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "equiv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}

	// Add via promoted method.
	id1, _ := store.Findings().AddFinding(&Finding{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "promoted"})
	// Add via typed accessor.
	id2, _ := store.Findings().AddFinding(&Finding{Wave: 1, Agent: "b", MSSLabel: "definition", Finding: "typed"})

	if id1 == 0 || id2 == 0 || id1 == id2 {
		t.Errorf("expected two distinct positive ids, got %d and %d", id1, id2)
	}

	// Both rows visible through either path.
	rows := store.Findings().probeAll(t)
	if len(rows) != 2 {
		t.Errorf("expected 2 rows after both inserts, got %d", len(rows))
	}
}

// probeAll is a test helper exercising the read path through the typed accessor.
func (r *FindingsRepo) probeAll(t *testing.T) []map[string]any {
	t.Helper()
	rows, err := r.Probe(map[string]any{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestGetByMSSLabel covers the happy path (filter by label returns only matching rows)
// and edge cases (unknown label returns empty, empty DB returns empty).
func TestGetByMSSLabel(t *testing.T) {
	cases := []struct {
		name       string
		inserts    []struct{ label, text string }
		queryLabel string
		wantCount  int
	}{
		{
			name:       "empty DB returns empty slice",
			inserts:    nil,
			queryLabel: "definition",
			wantCount:  0,
		},
		{
			name: "returns only matching label",
			inserts: []struct{ label, text string }{
				{"definition", "def finding 1"},
				{"definition", "def finding 2"},
				{"assumption", "assumption finding"},
				{"unknown", "unknown finding"},
			},
			queryLabel: "definition",
			wantCount:  2,
		},
		{
			name: "assumption label returns only assumptions",
			inserts: []struct{ label, text string }{
				{"definition", "def finding"},
				{"assumption", "assumption finding 1"},
				{"assumption", "assumption finding 2"},
			},
			queryLabel: "assumption",
			wantCount:  2,
		},
		{
			name: "unknown label with no matches returns empty",
			inserts: []struct{ label, text string }{
				{"definition", "def finding"},
			},
			queryLabel: "unknown",
			wantCount:  0,
		},
		{
			name: "nonexistent label returns empty without error",
			inserts: []struct{ label, text string }{
				{"definition", "def finding"},
			},
			queryLabel: "notavalidlabel",
			wantCount:  0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			for _, ins := range tc.inserts {
				_, err := store.Findings().AddFinding(&Finding{
					Wave: 1, Agent: "test-agent",
					MSSLabel: ins.label,
					Finding:  ins.text,
				})
				if err != nil {
					t.Fatalf("AddFinding(%s): %v", ins.label, err)
				}
			}

			rows, err := store.Findings().GetByMSSLabel(tc.queryLabel)
			if err != nil {
				t.Fatalf("GetByMSSLabel(%q): unexpected error: %v", tc.queryLabel, err)
			}
			if len(rows) != tc.wantCount {
				t.Errorf("GetByMSSLabel(%q): got %d rows, want %d", tc.queryLabel, len(rows), tc.wantCount)
			}
			// Verify every returned row has the expected mss_label.
			for i, row := range rows {
				if got, ok := row["mss_label"]; !ok {
					t.Errorf("row %d missing mss_label key", i)
				} else if got != tc.queryLabel {
					t.Errorf("row %d: mss_label=%v, want %q", i, got, tc.queryLabel)
				}
			}
		})
	}
}
