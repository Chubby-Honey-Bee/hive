package mss

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newAuditDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// findings table without the CHECK constraint so we can test
	// PartitionAudit detecting label corruption.
	if _, err := db.Exec(`CREATE TABLE findings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mss_label TEXT,
		finding TEXT NOT NULL DEFAULT '',
		d1 INTEGER, d2 INTEGER, d3 INTEGER, d4 INTEGER, d5 INTEGER
	)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	return db
}

func TestPartitionAudit_AllValid(t *testing.T) {
	db := newAuditDB(t)
	for _, label := range []string{"definition", "guarantee", "assumption", "unknown"} {
		if _, err := db.Exec("INSERT INTO findings(mss_label, finding) VALUES(?, 'x')", label); err != nil {
			t.Fatalf("insert %s: %v", label, err)
		}
	}
	got, err := PartitionAudit(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no violations, got %v", got)
	}
}

func TestPartitionAudit_DetectsBogusLabel(t *testing.T) {
	db := newAuditDB(t)
	db.Exec("INSERT INTO findings(mss_label, finding) VALUES('definition', 'ok')")
	db.Exec("INSERT INTO findings(mss_label, finding) VALUES('TYPO', 'broken row')")
	db.Exec("INSERT INTO findings(mss_label, finding) VALUES(NULL, 'null label')")

	got, err := PartitionAudit(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 violations, got %d: %+v", len(got), got)
	}
	// Both rows should be flagged
	labels := map[string]bool{}
	for _, v := range got {
		labels[v.Label] = true
	}
	if !labels["TYPO"] || !labels[""] {
		t.Errorf("missing expected violations: %v", labels)
	}
}

func TestIndependenceAudit_DetectsNearDuplicate(t *testing.T) {
	db := newAuditDB(t)
	// Two assumptions at the same coord with very similar text.
	db.Exec(`INSERT INTO findings(mss_label, finding, d1, d2, d3, d4, d5)
		VALUES('assumption', 'manufacturing cost per unit including packaging materials', 1, 2, 0, 0, 0)`)
	db.Exec(`INSERT INTO findings(mss_label, finding, d1, d2, d3, d4, d5)
		VALUES('assumption', 'manufacturing cost per unit including packaging materials extra', 1, 2, 0, 0, 0)`)
	// And a clearly independent assumption at the same coord.
	db.Exec(`INSERT INTO findings(mss_label, finding, d1, d2, d3, d4, d5)
		VALUES('assumption', 'shipping insurance is offered annually', 1, 2, 0, 0, 0)`)

	got, err := IndependenceAudit(db, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatalf("expected at least one redundancy candidate, got none")
	}
	if got[0].Similarity < 0.7 {
		t.Errorf("similarity below threshold: %v", got[0].Similarity)
	}
}

func TestIndependenceAudit_DifferentCoordinatesNotCompared(t *testing.T) {
	db := newAuditDB(t)
	// Same exact text but different coords — must NOT be flagged.
	db.Exec(`INSERT INTO findings(mss_label, finding, d1, d2, d3, d4, d5)
		VALUES('assumption', 'identical text identical text identical text', 0, 0, 0, 0, 0)`)
	db.Exec(`INSERT INTO findings(mss_label, finding, d1, d2, d3, d4, d5)
		VALUES('assumption', 'identical text identical text identical text', 1, 1, 1, 1, 1)`)

	got, err := IndependenceAudit(db, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("different coords must not be compared: got %v", got)
	}
}

func TestIndependenceAudit_NonAssumptionsIgnored(t *testing.T) {
	db := newAuditDB(t)
	// Definitions at the same coord are out of scope for Independence
	// (Independence is an MSS rule about assumptions only).
	db.Exec(`INSERT INTO findings(mss_label, finding, d1, d2, d3, d4, d5)
		VALUES('definition', 'identical text identical text identical text', 0, 0, 0, 0, 0)`)
	db.Exec(`INSERT INTO findings(mss_label, finding, d1, d2, d3, d4, d5)
		VALUES('definition', 'identical text identical text identical text', 0, 0, 0, 0, 0)`)

	got, err := IndependenceAudit(db, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("definitions should be excluded from Independence audit, got %v", got)
	}
}

func TestIndependenceAudit_BelowThreshold(t *testing.T) {
	db := newAuditDB(t)
	db.Exec(`INSERT INTO findings(mss_label, finding, d1, d2, d3, d4, d5)
		VALUES('assumption', 'shipping insurance covers loss', 0, 0, 0, 0, 0)`)
	db.Exec(`INSERT INTO findings(mss_label, finding, d1, d2, d3, d4, d5)
		VALUES('assumption', 'returns rate exceeds twenty percent annually', 0, 0, 0, 0, 0)`)

	got, err := IndependenceAudit(db, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("disjoint assumptions should not be flagged, got %v", got)
	}
}

func TestContentSimilarity_Identical(t *testing.T) {
	a := "manufacturing cost per unit"
	if got := contentSimilarity(a, a); got != 1.0 {
		t.Errorf("identical → 1.0, got %v", got)
	}
}

func TestContentSimilarity_Disjoint(t *testing.T) {
	if got := contentSimilarity("apple banana cherry date", "engine motor turbine"); got != 0 {
		t.Errorf("disjoint → 0, got %v", got)
	}
}

func TestContentSimilarity_EmptyAfterTokenize(t *testing.T) {
	// Only short words after lowercasing — tokenizer drops everything ≤3 chars.
	if got := contentSimilarity("a b c", "a b c"); got != 0 {
		t.Errorf("all-short-words inputs → 0 (no content tokens), got %v", got)
	}
}

// minimal-schema in-memory DB for partition / independence tests.
func partitionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE findings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mss_label TEXT,
		finding TEXT,
		d1 INTEGER, d2 INTEGER, d3 INTEGER, d4 INTEGER, d5 INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPartitionAudit_EmptyDB(t *testing.T) {
	db := partitionTestDB(t)
	got, err := PartitionAudit(db)
	if err != nil {
		t.Fatalf("PartitionAudit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d; want 0", len(got))
	}
}

func TestPartitionAudit_AllValidLabels(t *testing.T) {
	db := partitionTestDB(t)
	for _, label := range []string{"definition", "guarantee", "assumption", "unknown"} {
		if _, err := db.Exec("INSERT INTO findings(mss_label, finding) VALUES (?, ?)", label, "x"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := PartitionAudit(db)
	if err != nil {
		t.Fatalf("PartitionAudit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no violations; got %v", got)
	}
}

func TestPartitionAudit_InvalidLabel(t *testing.T) {
	db := partitionTestDB(t)
	if _, err := db.Exec("INSERT INTO findings(mss_label, finding) VALUES (?, ?)", "totally-bogus", "x"); err != nil {
		t.Fatal(err)
	}
	got, err := PartitionAudit(db)
	if err != nil {
		t.Fatalf("PartitionAudit: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len = %d; want 1", len(got))
	}
	if got[0].Label != "totally-bogus" {
		t.Errorf("label = %q; want totally-bogus", got[0].Label)
	}
}

func TestPartitionAudit_NullLabel(t *testing.T) {
	db := partitionTestDB(t)
	if _, err := db.Exec("INSERT INTO findings(mss_label, finding) VALUES (NULL, 'x')"); err != nil {
		t.Fatal(err)
	}
	got, err := PartitionAudit(db)
	if err != nil {
		t.Fatalf("PartitionAudit: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len = %d; want 1", len(got))
	}
	if got[0].Label != "" {
		t.Errorf("label = %q; want empty (NULL)", got[0].Label)
	}
}

func TestPartitionAudit_DBError(t *testing.T) {
	db := partitionTestDB(t)
	db.Close()
	_, err := PartitionAudit(db)
	if err == nil {
		t.Error("expected error from closed DB")
	}
}
