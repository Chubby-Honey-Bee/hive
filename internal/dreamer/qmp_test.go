package dreamer

import (
	"fmt"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func TestEmitQMP_HappyPath(t *testing.T) {
	store := freshStore(t)

	err := EmitQMP(store, "laundering")
	if err != nil {
		t.Fatalf("EmitQMP happy path: %v", err)
	}

	n := countSignals(t, store, "qmp")
	if n != 1 {
		t.Fatalf("expected 1 qmp signal, got %d", n)
	}
	// A halt is recorded twice, as this signal and as a halted ripen_log
	// row. Both name the same source.
	if err := recordHaltedPass(store, "prune", "laundering"); err != nil {
		t.Fatal(err)
	}
	var signalSource, logSource string
	if err := store.ReadDB.QueryRow(`SELECT json_extract(payload_json, '$.source') FROM signals WHERE signal_type = 'qmp'`).Scan(&signalSource); err != nil {
		t.Fatal(err)
	}
	if err := store.ReadDB.QueryRow(`SELECT json_extract(notes_json, '$.source') FROM ripen_log WHERE status = 'halted'`).Scan(&logSource); err != nil {
		t.Fatal(err)
	}
	if signalSource != logSource {
		t.Fatalf("qmp signal source %q, halted ripen_log source %q: one halt, two names", signalSource, logSource)
	}
}

func TestEmitQMP_TableDriven(t *testing.T) {
	reasons := []string{"laundering", "partition", "cycle", ""}

	for _, reason := range reasons {
		t.Run("reason="+reason, func(t *testing.T) {
			store := freshStore(t)
			if err := EmitQMP(store, reason); err != nil {
				t.Fatalf("EmitQMP(%q): %v", reason, err)
			}
			n := countSignals(t, store, "qmp")
			if n != 1 {
				t.Fatalf("EmitQMP(%q): expected 1 qmp signal, got %d", reason, n)
			}
		})
	}
}

// TestLaunderingDetected covers all branches of LaunderingDetected.

func TestLaunderingDetected_NoViolations(t *testing.T) {
	store := freshStore(t)
	detected, reason, err := LaunderingDetected(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detected {
		t.Errorf("expected detected=false on empty store, got true (reason=%q)", reason)
	}
	if reason != "" {
		t.Errorf("expected empty reason, got %q", reason)
	}
}

func TestLaunderingDetected_LaunderingViolation(t *testing.T) {
	store := freshStore(t)
	// Insert an unknown finding, then inject a guarantee→unknown via raw SQL
	// (bypassing the write-time laundering check).
	unkID := mustAddFinding(t, store, "unknown", "gap we cannot resolve", nil)
	deps := fmt.Sprintf("[%d]", unkID)
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding, depends_on_ids) VALUES (1,'b','guarantee','launderer',?)`,
		deps,
	); err != nil {
		t.Fatalf("inject laundering violation: %v", err)
	}

	detected, reason, err := LaunderingDetected(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !detected {
		t.Error("expected detected=true for laundering violation")
	}
	if reason != "laundering" {
		t.Errorf("expected reason=%q, got %q", "laundering", reason)
	}
}

func TestLaunderingDetected_DependencyCycle(t *testing.T) {
	store := freshStore(t)
	// Create two definitions and inject a mutual dependency cycle via raw SQL.
	a := mustAddFinding(t, store, "definition", "fact A", nil)
	b := mustAddFinding(t, store, "definition", "fact B", nil)
	store.WriteDB.Exec("UPDATE findings SET depends_on_ids=? WHERE id=?", fmt.Sprintf("[%d]", b), a) //nolint:errcheck
	store.WriteDB.Exec("UPDATE findings SET depends_on_ids=? WHERE id=?", fmt.Sprintf("[%d]", a), b) //nolint:errcheck

	detected, reason, err := LaunderingDetected(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !detected {
		t.Error("expected detected=true for dependency cycle")
	}
	if reason != "cycle" {
		t.Errorf("expected reason=%q, got %q", "cycle", reason)
	}
}

func TestLaunderingDetected_PartitionViolation(t *testing.T) {
	store := freshStore(t)
	// Insert a valid row, then attempt to corrupt its label via UPDATE
	// (bypasses INSERT-time CHECK in most SQLite versions).
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding) VALUES (1,'a','assumption','seed')`,
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	store.WriteDB.Exec(`UPDATE findings SET mss_label = 'corrupted' WHERE finding = 'seed'`) //nolint:errcheck

	// If CHECK constraint held, the label is still 'assumption' → no violation;
	// skip rather than fail, consistent with the db package's own audit tests.
	var label string
	store.ReadDB.QueryRow(`SELECT mss_label FROM findings WHERE finding = 'seed'`).Scan(&label) //nolint:errcheck
	if label != "corrupted" {
		t.Skip("SQLite CHECK constraint prevented label corruption; partition branch untestable")
	}

	detected, reason, err := LaunderingDetected(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !detected {
		t.Error("expected detected=true for partition violation")
	}
	if reason != "partition" {
		t.Errorf("expected reason=%q, got %q", "partition", reason)
	}
}

func TestLaunderingDetected_Error(t *testing.T) {
	store := freshStore(t)
	// Close the store to force MSSAudit to return an error.
	store.Close()

	detected, reason, err := LaunderingDetected(store)
	if err == nil {
		t.Fatal("expected error from closed store, got nil")
	}
	if detected {
		t.Errorf("expected detected=false on error, got true (reason=%q)", reason)
	}
	if reason != "" {
		t.Errorf("expected empty reason on error, got %q", reason)
	}
}

func TestLaunderingDetected_CleanStore(t *testing.T) {
	store := freshStore(t)
	got, reason, err := LaunderingDetected(store)
	if err != nil {
		t.Fatalf("LaunderingDetected: %v", err)
	}
	if got {
		t.Errorf("clean store: laundering=true (reason=%s); want false", reason)
	}
}

func TestLaunderingDetected_AuditCalledWithSeededFindings(t *testing.T) {
	// Add a normal finding so the audit composer runs all passes against
	// real data — exercises the success path of LaunderingDetected.
	store := freshStore(t)
	d1 := 0
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "ok", D1: &d1,
	}); err != nil {
		t.Fatal(err)
	}
	got, reason, err := LaunderingDetected(store)
	if err != nil {
		t.Fatalf("LaunderingDetected: %v", err)
	}
	if got {
		t.Errorf("clean store with definition: laundering=true (reason=%s); want false", reason)
	}
}
