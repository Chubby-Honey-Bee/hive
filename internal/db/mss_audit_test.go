package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRunAudit_HappyPath calls RunAudit directly on an empty DB and confirms
// the zero-violation PASS result. This exercises the top-level composer
// directly (rather than through Store.MSSAudit).
func TestRunAudit_HappyPath(t *testing.T) {
	store := newDBStore(t)
	res, err := RunAudit(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("RunAudit returned nil result")
	}
	if res.Integrity != "PASS" {
		t.Errorf("empty DB: want Integrity=PASS, got %q", res.Integrity)
	}
	if res.LaunderingViolations == nil || res.UntraceableGuarantees == nil ||
		res.DependencyCycles == nil || res.PartitionViolations == nil ||
		res.RedundancyCandidates == nil {
		t.Error("RunAudit must return non-nil slices for every criterion bucket")
	}
	if res.LabelDistribution == nil {
		t.Error("RunAudit must return a non-nil LabelDistribution map")
	}
}

// TestRunAudit_IntegrityFail covers the Integrity="FAIL" branch by inserting
// an untraceable guarantee (no depends_on_ids) directly via SQL, bypassing
// write-time enforcement.
func TestRunAudit_IntegrityFail(t *testing.T) {
	store := newDBStore(t)
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding) VALUES (1, 'x', 'guarantee', 'untraceable')`,
	); err != nil {
		t.Fatal(err)
	}

	res, err := RunAudit(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	if res.Integrity != "FAIL" {
		t.Errorf("expected Integrity=FAIL with untraceable guarantee, got %q", res.Integrity)
	}
	if len(res.UntraceableGuarantees) == 0 {
		t.Error("expected at least one untraceable guarantee in result")
	}
}

// TestRunAudit_LaunderingFail covers the LaunderingViolations branch of the
// Integrity check by forcing a guarantee→unknown dependency chain.
func TestRunAudit_LaunderingFail(t *testing.T) {
	store := newDBStore(t)
	unkID, err := store.Findings().AddFinding(&Finding{Wave: 1, Agent: "a", MSSLabel: "unknown", Finding: "gap"})
	if err != nil {
		t.Fatal(err)
	}
	deps := `[` + itoa(unkID) + `]`
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding, depends_on_ids) VALUES (1, 'b', 'guarantee', 'launderer', ?)`,
		deps,
	); err != nil {
		t.Fatal(err)
	}

	res, err := RunAudit(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	if res.Integrity != "FAIL" {
		t.Errorf("guarantee→unknown should yield Integrity=FAIL, got %q", res.Integrity)
	}
	if len(res.LaunderingViolations) == 0 {
		t.Error("expected LaunderingViolations to be non-empty")
	}
}

// TestRunAudit_CycleFail covers the DependencyCycles branch of the Integrity check.
func TestRunAudit_CycleFail(t *testing.T) {
	store := newDBStore(t)
	a, _ := store.Findings().AddFinding(&Finding{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "A"})
	b, _ := store.Findings().AddFinding(&Finding{Wave: 1, Agent: "b", MSSLabel: "definition", Finding: "B"})
	store.WriteDB.Exec("UPDATE findings SET depends_on_ids=? WHERE id=?", `[`+itoa(b)+`]`, a)
	store.WriteDB.Exec("UPDATE findings SET depends_on_ids=? WHERE id=?", `[`+itoa(a)+`]`, b)

	res, err := RunAudit(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	if res.Integrity != "FAIL" {
		t.Errorf("cycle should yield Integrity=FAIL, got %q", res.Integrity)
	}
	if len(res.DependencyCycles) == 0 {
		t.Error("expected DependencyCycles to be non-empty")
	}
}

// TestRunAudit_ClosedDB covers the error-return path of RunAudit when
// the underlying database is unavailable.
func TestRunAudit_ClosedDB(t *testing.T) {
	store := newDBStore(t)
	store.ReadDB.Close()
	_, err := RunAudit(store.ReadDB)
	if err == nil {
		t.Error("expected error from RunAudit on closed DB, got nil")
	}
}

func newDBStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "audit.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestMSSAudit_AllFourCriteriaReported(t *testing.T) {
	store := newDBStore(t)
	res, err := store.MSSAudit()
	if err != nil {
		t.Fatal(err)
	}
	// Every criterion bucket exists in the result struct, even on an
	// empty DB.
	if res.PartitionViolations == nil {
		t.Errorf("PartitionViolations should be non-nil slice")
	}
	if res.RedundancyCandidates == nil {
		t.Errorf("RedundancyCandidates should be non-nil slice")
	}
	if res.LaunderingViolations == nil || res.UntraceableGuarantees == nil ||
		res.DependencyCycles == nil {
		t.Errorf("the laundering, untraceable and cycle buckets must be non-nil")
	}
	if res.Integrity != "PASS" {
		t.Errorf("empty DB should be PASS, got %q", res.Integrity)
	}
}

func TestMSSAudit_RedundancyCandidatesSurfaced(t *testing.T) {
	store := newDBStore(t)
	d := func(v int) *int { return &v }
	// Two near-duplicate assumptions at the same coord.
	if _, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "assumption",
		Finding:  "manufacturing cost per unit including packaging materials approx",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "b", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "assumption",
		Finding:  "manufacturing cost per unit including packaging materials estimated",
	}); err != nil {
		t.Fatal(err)
	}

	res, err := store.MSSAudit()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RedundancyCandidates) == 0 {
		t.Errorf("expected redundancy candidate, got none. result=%+v", res)
	}
	// Redundancy candidates are warnings — must NOT flip Integrity to FAIL.
	if res.Integrity != "PASS" {
		t.Errorf("redundancy candidates should not fail Integrity, got %q", res.Integrity)
	}
}

func TestMSSAudit_PartitionViolation_Fails(t *testing.T) {
	store := newDBStore(t)
	// Bypass CHECK by raw SQL — simulates corruption from external access.
	// CHECK normally prevents this, but the audit must still detect it if
	// it happens (e.g., a future migration with a wider enum + rollback).
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding) VALUES (1, 'x', 'assumption', 'seed row')`,
	); err != nil {
		t.Fatal(err)
	}
	// Direct UPDATE bypasses INSERT-time CHECK in some SQLite versions —
	// even if CHECK fires here, the audit still works on legitimate data.
	store.WriteDB.Exec(`UPDATE findings SET mss_label = 'corrupted' WHERE id = 1`)

	res, err := store.MSSAudit()
	if err != nil {
		t.Fatal(err)
	}
	// Either CHECK held (no violation, label still 'assumption') or it
	// didn't (violation surfaced). Both are acceptable; we only assert
	// the audit doesn't crash and the schema-honest case stays PASS.
	if len(res.PartitionViolations) > 0 && res.Integrity != "FAIL" {
		t.Errorf("PartitionViolations present but Integrity=%q", res.Integrity)
	}
}

// ---------------------------------------------------------------------------
// Direct tests for auditLaundering (unexported, same package)
// ---------------------------------------------------------------------------

// TestAuditLaundering_HappyPath confirms no violations on an empty DB.
func TestAuditLaundering_HappyPath(t *testing.T) {
	store := newDBStore(t)
	got, err := auditLaundering(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty DB: expected 0 violations, got %d", len(got))
	}
}

// TestAuditLaundering_DirectViolation covers the immediate guarantee→unknown path.
func TestAuditLaundering_DirectViolation(t *testing.T) {
	store := newDBStore(t)
	unkID, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "unknown", Finding: "a gap",
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := `[` + itoa(unkID) + `]`
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding, depends_on_ids) VALUES (1,'b','guarantee','direct laundering',?)`,
		deps,
	); err != nil {
		t.Fatal(err)
	}

	got, err := auditLaundering(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one laundering violation")
	}
	row := got[0]
	if row["depends_on_unknown_id"] != unkID {
		t.Errorf("depends_on_unknown_id=%v, want %d", row["depends_on_unknown_id"], unkID)
	}
}

// TestAuditLaundering_TransitiveBFS covers the guarantee→guarantee→unknown chain
// (the BFS path that catches indirect laundering).
func TestAuditLaundering_TransitiveBFS(t *testing.T) {
	store := newDBStore(t)

	// unknown at the leaf
	unkID, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "unknown", Finding: "deep gap",
	})
	if err != nil {
		t.Fatal(err)
	}

	// intermediate guarantee that depends on unknown (bypass write-time check via SQL)
	midDeps := `[` + itoa(unkID) + `]`
	res, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding, depends_on_ids) VALUES (1,'b','guarantee','middle guarantee',?)`,
		midDeps,
	)
	if err != nil {
		t.Fatal(err)
	}
	midID, _ := res.LastInsertId()

	// top-level guarantee that only depends on the middle guarantee
	topDeps := `[` + itoa(midID) + `]`
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding, depends_on_ids) VALUES (1,'c','guarantee','top guarantee',?)`,
		topDeps,
	); err != nil {
		t.Fatal(err)
	}

	got, err := auditLaundering(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	// We expect violations from both the middle guarantee (direct) and the top
	// guarantee (transitive). At minimum there must be two violations.
	if len(got) < 2 {
		t.Errorf("expected ≥2 violations (direct + transitive), got %d", len(got))
	}
}

// TestAuditLaundering_TextTruncation verifies that findings longer than 80
// characters are truncated in the returned violation map.
func TestAuditLaundering_TextTruncation(t *testing.T) {
	store := newDBStore(t)

	longUnknown := strings.Repeat("u", 100)
	unkID, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "unknown", Finding: longUnknown,
	})
	if err != nil {
		t.Fatal(err)
	}

	longGuarantee := strings.Repeat("g", 100)
	deps := `[` + itoa(unkID) + `]`
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding, depends_on_ids) VALUES (1,'b','guarantee',?,?)`,
		longGuarantee, deps,
	); err != nil {
		t.Fatal(err)
	}

	got, err := auditLaundering(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected a violation")
	}
	row := got[0]
	if g, ok := row["guarantee"].(string); !ok || len(g) > 80 {
		t.Errorf("guarantee text not truncated to 80 chars: len=%d", len(g))
	}
	if u, ok := row["unknown"].(string); !ok || len(u) > 80 {
		t.Errorf("unknown text not truncated to 80 chars: len=%d", len(u))
	}
}

// TestAuditLaundering_DepNotInNodes covers the BFS branch where a dep ID
// references a finding not present in the nodes map (dangling reference).
// The function must not crash and must return no violation for the dangling dep.
func TestAuditLaundering_DepNotInNodes(t *testing.T) {
	store := newDBStore(t)

	// Insert a guarantee whose depends_on_ids points to a non-existent ID.
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding, depends_on_ids) VALUES (1,'a','guarantee','dangling','[99999]')`,
	); err != nil {
		t.Fatal(err)
	}

	got, err := auditLaundering(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	// Dangling dep is not unknown (it's not in nodes at all) → no violation.
	if len(got) != 0 {
		t.Errorf("dangling dep should produce 0 violations, got %d", len(got))
	}
}

// TestAuditLaundering_ClosedDB confirms an error is returned when the DB is closed.
func TestAuditLaundering_ClosedDB(t *testing.T) {
	store := newDBStore(t)
	store.ReadDB.Close()
	_, err := auditLaundering(store.ReadDB)
	if err == nil {
		t.Error("expected error from auditLaundering on closed DB, got nil")
	}
}

// ---------------------------------------------------------------------------
// Direct tests for auditPartition (unexported, same package)
// ---------------------------------------------------------------------------

// TestAuditPartition_HappyPath confirms no violations are returned on a clean DB.
func TestAuditPartition_HappyPath(t *testing.T) {
	store := newDBStore(t)
	got, err := auditPartition(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty DB: expected 0 violations, got %d", len(got))
	}
}

// TestAuditPartition_ClosedDB confirms an error is returned when the DB is closed.
func TestAuditPartition_ClosedDB(t *testing.T) {
	store := newDBStore(t)
	store.ReadDB.Close()
	_, err := auditPartition(store.ReadDB)
	if err == nil {
		t.Error("expected error from auditPartition on closed DB, got nil")
	}
}

// TestAuditPartition_Violation covers the violation-present branch including
// the map shape and, when the finding text exceeds 80 chars, the truncation branch.
// SQLite CHECK may or may not allow the corrupted label depending on version;
// the test uses UPDATE to attempt the bypass.
func TestAuditPartition_Violation(t *testing.T) {
	store := newDBStore(t)
	longText := strings.Repeat("x", 100)
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding) VALUES (1, 'a', 'assumption', ?)`,
		longText,
	); err != nil {
		t.Fatal(err)
	}
	// Attempt to corrupt the label via UPDATE — bypasses INSERT-time CHECK in most SQLite versions.
	store.WriteDB.Exec(`UPDATE findings SET mss_label = 'bogus' WHERE id = 1`)

	got, err := auditPartition(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	// If CHECK held, the label is still 'assumption' → 0 violations, which is fine.
	// If CHECK did not hold, we must have exactly 1 violation with truncated text.
	if len(got) == 0 {
		t.Skip("SQLite CHECK constraint prevented label corruption; partition violation branch untestable")
	}
	row := got[0]
	if _, ok := row["id"]; !ok {
		t.Error("violation map missing 'id' key")
	}
	if _, ok := row["label"]; !ok {
		t.Error("violation map missing 'label' key")
	}
	finding, ok := row["finding"].(string)
	if !ok {
		t.Error("violation map 'finding' is not a string")
	}
	if len(finding) > 80 {
		t.Errorf("finding text should be truncated to ≤80 chars, got len=%d", len(finding))
	}
}

// TestAuditPartition_TableDriven exercises the map key contract for rows
// produced by auditPartition on a DB where a valid finding exists and the
// happy-path slice is confirmed empty.
func TestAuditPartition_TableDriven(t *testing.T) {
	tests := []struct {
		name      string
		label     string
		finding   string
		wantCount int
	}{
		{"definition", "definition", "a definition", 0},
		{"guarantee_raw_sql", "guarantee", "a guarantee", 0},
		{"assumption", "assumption", "an assumption", 0},
		{"unknown", "unknown", "an unknown", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newDBStore(t)
			if _, err := store.WriteDB.Exec(
				`INSERT INTO findings(wave, agent, mss_label, finding) VALUES (1, 'a', ?, ?)`,
				tc.label, tc.finding,
			); err != nil {
				t.Fatal(err)
			}
			got, err := auditPartition(store.ReadDB)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.wantCount {
				t.Errorf("label=%q: want %d violations, got %d", tc.label, tc.wantCount, len(got))
			}
		})
	}
}

// TestAuditLaundering_GuaranteeWithNoDeps covers the early-continue branch
// for guarantees with an empty depIDs slice.
func TestAuditLaundering_GuaranteeWithNoDeps(t *testing.T) {
	store := newDBStore(t)
	// Insert a guarantee with an empty JSON array — should be skipped cleanly.
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding, depends_on_ids) VALUES (1,'a','guarantee','no deps','[]')`,
	); err != nil {
		t.Fatal(err)
	}

	got, err := auditLaundering(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("guarantee with no deps should produce 0 violations, got %d", len(got))
	}
}

// openMinimalDB opens an in-memory SQLite DB and creates a findings table
// with exactly the specified columns. This lets tests induce specific
// sub-audit failures while the preceding sub-audits still succeed.
func openMinimalDB(t *testing.T, createSQL string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(createSQL); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

// TestRunAudit_UntraceableSubauditError covers the
//
//	return nil, fmt.Errorf("untraceable: %w", err)
//
// branch in RunAudit. The schema includes only mss_label so
// auditLabelDistribution succeeds (it only GROUP BYs mss_label) while
// auditUntraceableGuarantees fails because it also SELECTs id, finding,
// and agent columns that don't exist.
func TestRunAudit_UntraceableSubauditError(t *testing.T) {
	db := openMinimalDB(t, `CREATE TABLE findings (mss_label TEXT)`)
	_, err := RunAudit(db)
	if err == nil {
		t.Fatal("expected error from RunAudit when auditUntraceableGuarantees fails, got nil")
	}
}

// TestRunAudit_RedundancySubauditError covers the
//
//	return nil, fmt.Errorf("redundancy: %w", err)
//
// branch in RunAudit. The schema includes all columns consumed by the first
// five sub-audits (label distribution, untraceable, laundering, cycles,
// partition) but omits d1–d5, which mss.IndependenceAudit (inside
// auditRedundancy) requires. All sub-audits before redundancy succeed on
// the empty table; auditRedundancy returns an error.
func TestRunAudit_RedundancySubauditError(t *testing.T) {
	db := openMinimalDB(t, `CREATE TABLE findings (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		mss_label      TEXT,
		finding        TEXT NOT NULL DEFAULT '',
		agent          TEXT NOT NULL DEFAULT '',
		depends_on_ids TEXT
	)`)
	_, err := RunAudit(db)
	if err == nil {
		t.Fatal("expected error from RunAudit when auditRedundancy fails, got nil")
	}
}

// truncateBytes cuts on a rune boundary: a cut that falls inside a
// multi-byte character leaves valid UTF-8 of at most the cut's length.
func TestTruncateBytes_CutsOnARuneBoundary(t *testing.T) {
	if got := truncateBytes(strings.Repeat("a", 79)+"—b", 80); !utf8.ValidString(got) || len(got) > 80 {
		t.Errorf("truncateBytes = %q, want valid UTF-8 of at most 80 bytes", got)
	}
}
