package cde

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

// findingsDB makes an in-memory DB with a minimal findings table + the
// dimensions table the suggester probes for the next free slot.
func findingsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE findings (
		id INTEGER PRIMARY KEY, d1 INTEGER, d2 INTEGER, d3 INTEGER, d4 INTEGER, d5 INTEGER,
		mss_label TEXT, finding TEXT)`); err != nil {
		t.Fatalf("create findings: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE dimensions (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("create dimensions: %v", err)
	}
	return db
}

func addFinding(t *testing.T, db *sql.DB, d1 int, label string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO findings (d1, mss_label, finding) VALUES (?, ?, 'x')`, d1, label); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestSuggestAxes_SplitCellIsCandidate(t *testing.T) {
	db := findingsDB(t)
	// Same coordinate (d1=1), two different labels → split cell.
	addFinding(t, db, 1, "definition")
	addFinding(t, db, 1, "assumption")

	cands, err := SuggestAxes(db, 2)
	if err != nil {
		t.Fatalf("SuggestAxes: %v", err)
	}
	if len(cands) != 1 || cands[0].Attribute != "mss_label" {
		t.Fatalf("expected 1 mss_label candidate, got %+v", cands)
	}
	if cands[0].SplitCells != 1 {
		t.Errorf("expected SplitCells=1, got %d", cands[0].SplitCells)
	}
	// Slots are positional — ValidateCoords maps dimensions-table row N onto
	// dN — so with nothing registered the next one lands at d1.
	if cands[0].ProposedSlot != "d1" {
		t.Errorf("proposed slot %q; want d1, the slot the next registration actually takes",
			cands[0].ProposedSlot)
	}
}

// The proposal has to name the slot a registration will really occupy.
func TestSuggestAxes_ProposedSlotIsThePositionalNext(t *testing.T) {
	db := findingsDB(t)
	addFinding(t, db, 1, "definition")
	addFinding(t, db, 1, "assumption")
	for i, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := db.Exec(`INSERT INTO dimensions (name) VALUES (?)`, name); err != nil {
			t.Fatal(err)
		}
		cands, err := SuggestAxes(db, 2)
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("d%d", i+2)
		if len(cands) == 0 || cands[0].ProposedSlot != want {
			t.Errorf("after %d dimensions the proposal was %v; want %s", i+1, cands, want)
		}
	}
}

func TestSuggestAxes_NoSplitNoCandidate(t *testing.T) {
	db := findingsDB(t)
	// Each coordinate holds a single label → coordinates fully distinguish;
	// no axis candidate.
	addFinding(t, db, 1, "definition")
	addFinding(t, db, 1, "definition")
	addFinding(t, db, 2, "assumption")
	addFinding(t, db, 2, "assumption")

	cands, err := SuggestAxes(db, 2)
	if err != nil {
		t.Fatalf("SuggestAxes: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("expected no candidates when coordinates fully distinguish labels, got %+v", cands)
	}
}

func TestSuggestAxes_BelowEvidenceFloorIgnored(t *testing.T) {
	db := findingsDB(t)
	// A split cell, but only 1 finding per label and a high floor → ignored.
	addFinding(t, db, 1, "definition")
	addFinding(t, db, 1, "assumption")

	cands, err := SuggestAxes(db, 5) // floor higher than the 2-finding cell
	if err != nil {
		t.Fatalf("SuggestAxes: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("expected the sparse cell to be filtered by min-evidence, got %+v", cands)
	}
}

func TestFormatAxisCandidates_EmptyIsHonest(t *testing.T) {
	got := FormatAxisCandidates(nil)
	if got == "" {
		t.Fatal("expected an honest 'none' line, got empty string")
	}
}
