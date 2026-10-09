package hive

import "testing"

// The hive's mss_integrity is the gate's transitive audit, not a one-hop
// query: a guarantee resting on a guarantee resting on an unknown — the
// exact chain the transitive scan exists for — fails both.
func TestScanState_MSSIntegrityMatchesTheAudit(t *testing.T) {
	store := newTestStore(t)
	initProject(t, store, "p")

	unknownID := addFinding(t, store, "unknown", "an open question", 1)
	midID := addFinding(t, store, "guarantee", "rests on the unknown", 1)
	topID := addFinding(t, store, "guarantee", "rests on the middle", 1)
	for id, dep := range map[int64]int64{midID: unknownID, topID: midID} {
		if _, err := store.WriteDB.Exec(
			`UPDATE findings SET depends_on_ids=? WHERE id=?`,
			"["+itoa(dep)+"]", id,
		); err != nil {
			t.Fatal(err)
		}
	}

	audit, err := store.MSSAudit()
	if err != nil {
		t.Fatal(err)
	}
	if audit.Integrity != "FAIL" {
		t.Fatalf("the audit did not call this chain a failure; the fixture is wrong (%+v)", audit.Integrity)
	}

	state, err := ScanState(store, "p")
	if err != nil {
		t.Fatal(err)
	}
	if state.MSSIntegrity != audit.Integrity {
		t.Errorf("hive says %s, the audit says %s — one database, two verdicts",
			state.MSSIntegrity, audit.Integrity)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
