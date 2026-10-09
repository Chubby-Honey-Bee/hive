package db

import (
	"path/filepath"
	"testing"
)

// freshScanStore opens a real file-backed store (EXPLAIN QUERY PLAN needs
// a genuine SQLite planner, not a mock) with the schema initialized.
func freshScanStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "scan.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return s
}

func TestClassifyScan(t *testing.T) {
	s := freshScanStore(t)

	// A bare SELECT over findings with no leading-index predicate is a
	// full SCAN. (findings has composite indexes on d1.., wave, mss_label —
	// none usable here.)
	scanned, detail, err := ClassifyScan(s.ReadDB, "SELECT * FROM findings", "findings")
	if err != nil {
		t.Fatalf("ClassifyScan(full): %v", err)
	}
	if !scanned {
		t.Errorf("expected full SELECT * FROM findings to be a SCAN; detail=%q", detail)
	}

	// A query that filters on an indexed leading column resolves to an
	// indexed SEARCH, not a scan.
	scanned2, detail2, err := ClassifyScan(s.ReadDB,
		"SELECT * FROM findings WHERE mss_label = 'guarantee'", "findings")
	if err != nil {
		t.Fatalf("ClassifyScan(indexed): %v", err)
	}
	if scanned2 {
		t.Errorf("expected mss_label predicate to use idx_findings_mss (SEARCH), got SCAN; detail=%q", detail2)
	}
}

func TestRecordScanIfFullScan(t *testing.T) {
	s := freshScanStore(t)

	// A full scan is recorded.
	s.RecordScanIfFullScan("SELECT * FROM findings", "findings")
	// Repeat the same shape — must dedupe (one row, hit_count bumps).
	s.RecordScanIfFullScan("SELECT  *  FROM   findings", "findings") // whitespace differs → same shape
	// A second scanning shape.
	s.RecordScanIfFullScan("SELECT id, depends_on_ids FROM findings", "findings")
	// An indexed query — NOT a scan, must not be recorded.
	s.RecordScanIfFullScan("SELECT * FROM findings WHERE mss_label = 'guarantee'", "findings")

	events, err := s.ScanEvents()
	if err != nil {
		t.Fatalf("ScanEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 distinct scan shapes, got %d: %+v", len(events), events)
	}
	// Most hits sort first.
	if events[0].HitCount != 2 || events[1].HitCount != 1 {
		t.Errorf("expected hit counts [2 1], got [%d %d]", events[0].HitCount, events[1].HitCount)
	}
}

// A read with `?` placeholders is classified with its own arguments. With
// none bound the driver refused it ("missing argument"), so no parameterised
// read was ever classified, and one that scanned went unrecorded.
func TestClassifyScan_Parameterised(t *testing.T) {
	s := freshScanStore(t)
	scanned, detail, err := ClassifyScan(s.ReadDB, "SELECT * FROM findings WHERE d6 = ?", "findings", 1)
	if err != nil || !scanned {
		t.Fatalf("d6 = ?: scanned=%v detail=%q err=%v; want a scan (no index covers d6)", scanned, detail, err)
	}
	scanned, detail, err = ClassifyScan(s.ReadDB, "SELECT * FROM findings WHERE wave = ? ORDER BY d1, d2, d3, d4, d5, created_at, id", "findings", 1)
	if err != nil || scanned {
		t.Fatalf("the merge's wave read: scanned=%v detail=%q err=%v; want a SEARCH", scanned, detail, err)
	}

	s.RecordScanIfFullScan("SELECT * FROM findings WHERE d6 = ?", "findings", 1)
	s.RecordScanIfFullScan("SELECT * FROM findings WHERE wave = ?", "findings", 1)
	events, err := s.ScanEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].QueryShape != "SELECT * FROM findings WHERE d6 = ?" {
		t.Fatalf("scan_events = %+v; want the d6 shape alone", events)
	}
}

// Every read the workload table in cde-mss.md says an index serves must
// still be a bounded SEARCH. idx_cde_d1 and idx_findings_wave_agent were
// removed as redundant: a d1-only probe must stay a search through the
// composite coordinate index's leading column.
func TestWorkloadReadsAreIndexed(t *testing.T) {
	s := freshScanStore(t)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"SELECT * FROM findings WHERE d1 = 0 ORDER BY convergence_count DESC, created_at DESC LIMIT 500", nil},
		{"SELECT * FROM findings WHERE d1 = 0 AND d2 = 1 AND d3 = 2", nil},
		{"SELECT * FROM findings WHERE wave = 1", nil},
		{"SELECT id, mss_label FROM findings WHERE id IN (?, ?)", []any{1, 2}}, // Q3
		{"SELECT * FROM findings WHERE mss_label = 'guarantee'", nil},
		{"SELECT * FROM findings WHERE agent IN ('a', 'b')", nil},
		{"SELECT id FROM findings WHERE depends_on_ids IS NOT NULL", nil},
	} {
		scanned, detail, err := ClassifyScan(s.ReadDB, q.sql, "findings", q.args...)
		if err != nil {
			t.Fatalf("ClassifyScan(%q): %v", q.sql, err)
		}
		if scanned {
			t.Errorf("%q scans (%s); the workload table says an index serves it", q.sql, detail)
		}
	}
}

// Q10 is not a SEARCH: it walks idx_findings_convergence_created in order and
// LIMIT stops it. Probes whose filters no index serves (d6–d8,
// convergence_level, coordinates not starting at d1) take the same walk.
func TestWorkloadQ10IsAnOrderedIndexWalk(t *testing.T) {
	s := freshScanStore(t)
	for _, where := range []string{"1=1", "d6 = 1", "convergence_level = 'high'", "d2 = 1", "d3 = 1"} {
		q := "SELECT * FROM findings WHERE " + where + " ORDER BY convergence_count DESC, created_at DESC LIMIT 500"
		scanned, detail, err := ClassifyScan(s.ReadDB, q, "findings")
		if err != nil {
			t.Fatalf("ClassifyScan(%q): %v", q, err)
		}
		if !scanned || detail != "SCAN findings USING INDEX idx_findings_convergence_created" {
			t.Errorf("%q: scanned=%v detail=%q; want the walk of idx_findings_convergence_created", q, scanned, detail)
		}
	}
}
