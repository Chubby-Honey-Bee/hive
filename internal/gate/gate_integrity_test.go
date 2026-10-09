package gate

import (
	"testing"
)

// The gate's inline pipeAuditMSS check is a single hop: it only sees a
// guarantee that depends directly on an unknown. docs/foundations.md names
// the transitive BFS as what catches `guarantee -> guarantee -> unknown`,
// and its § The gate says a cycle or partition violation fails the gate, so
// pipeAuditIntegrity runs the full audit: each of those blocks, and
// wave_gates records the audit's outcome.
func TestGateBlocksTransitiveLaundering(t *testing.T) {
	store := newGateStore(t)

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := store.WriteDB.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// u <- g1 <- g2. g2 launders u through g1, so the one-hop check misses it.
	exec(`INSERT INTO findings (id, wave, agent, mss_label, finding) VALUES (1,1,'a','unknown','shipping rates unknown')`)
	exec(`INSERT INTO findings (id, wave, agent, mss_label, finding, depends_on_ids) VALUES (2,1,'a','guarantee','rests on the unknown','[1]')`)
	exec(`INSERT INTO findings (id, wave, agent, mss_label, finding, depends_on_ids) VALUES (3,1,'a','guarantee','rests on g1','[2]')`)
	exec(`INSERT INTO agent_runs (wave, agent_name, status) VALUES (1,'a','completed')`)

	result, err := RunGatePipeline(store, 1, map[string]any{
		"coverage": 4, "depth": 4, "sources": 4, "actionability": 4,
		"mss_integrity": 4, "verdict": "COMPLETE",
	}, false, true, false)
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	if result.Opened {
		t.Error("gate opened on a database the MSS audit fails")
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected the integrity audit to raise a blocking error")
	}

	var opened int
	store.ReadDB.QueryRow("SELECT COUNT(*) FROM wave_gates WHERE wave=1").Scan(&opened)
	if opened != 0 {
		t.Errorf("no wave_gates row should be written for a failed gate, got %d", opened)
	}
}

// A clean database still opens, and records the audit outcome it actually got.
func TestGateOpensAndRecordsRealAuditOutcome(t *testing.T) {
	store := newGateStore(t)

	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings (id, wave, agent, mss_label, finding) VALUES (1,1,'a','definition','price is $89')`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.WriteDB.Exec(
		`INSERT INTO agent_runs (wave, agent_name, status) VALUES (1,'a','completed')`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	result, err := RunGatePipeline(store, 1, map[string]any{
		"coverage": 4, "depth": 4, "sources": 4, "actionability": 4,
		"mss_integrity": 4, "verdict": "COMPLETE",
	}, false, true, false)
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if !result.Opened {
		t.Fatalf("gate should open on a clean database, errors: %v warnings: %v", result.Errors, result.Warnings)
	}

	var passed int
	if err := store.ReadDB.QueryRow(
		"SELECT mss_audit_passed FROM wave_gates WHERE wave=1",
	).Scan(&passed); err != nil {
		t.Fatalf("read wave_gates: %v", err)
	}
	if passed != 1 {
		t.Errorf("mss_audit_passed = %d, want 1", passed)
	}
}
