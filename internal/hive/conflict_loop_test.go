package hive

import (
	"strings"
	"testing"
)

// A [negation] conflict, which the gate's numeric auto-resolve cannot close,
// is planned for, adjudicated, cascaded and closed, and once closed is
// neither planned for nor blocking termination.
func TestConflictLoop_ResolvedConflictLeavesThePlanAndTheTerminationReasons(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	winner := addFinding(t, s, "definition", "the default page size is 4096 bytes", 0)
	loser := addFinding(t, s, "definition", "the default page size is 1024 bytes", 0)
	if err := s.Conflicts().AddConflict(1, winner, loser, "[negation] page size"); err != nil {
		t.Fatal(err)
	}
	var conflictID int64
	if err := s.ReadDB.QueryRow("SELECT id FROM conflicts").Scan(&conflictID); err != nil {
		t.Fatal(err)
	}

	scan := func() ([]Action, string) {
		t.Helper()
		state, err := ScanState(s, "p")
		if err != nil {
			t.Fatal(err)
		}
		_, reason := CheckTermination(state)
		return mustPlan(t, state, EvaluateSignals(s, state)), reason
	}
	forConflict := func(plan []Action) []Action {
		var out []Action
		for _, a := range plan {
			if id, _ := a.Payload["conflict_id"].(int64); id == conflictID {
				out = append(out, a)
			}
		}
		return out
	}

	plan, reason := scan()
	if got := forConflict(plan); len(got) != 1 || got[0].SignalType != "conflict_resolution" || got[0].Agent != "verifier" {
		t.Fatalf("open conflict: actions %+v, want one verifier dispatch carrying payload.conflict_id", got)
	}
	if !strings.Contains(reason, "unresolved conflicts") {
		t.Fatalf("termination reason %q, want it blocked on the conflict", reason)
	}

	// Naming a winner arms the alarm; its cascade carries the conflict id so
	// the cascade can close it.
	if err := s.Conflicts().SetWinner(conflictID, winner); err != nil {
		t.Fatal(err)
	}
	plan, _ = scan()
	var cascade *Action
	for _, a := range forConflict(plan) {
		if a.Type == "cascade_revert" {
			a := a
			cascade = &a
		}
	}
	if cascade == nil || cascade.FindingID != loser {
		t.Fatalf("adjudicated conflict: no cascade_revert of finding %d carrying the conflict id: %+v", loser, plan)
	}
	// The alarm's cascade is the conflict's only action now: a verifier sent
	// to adjudicate it again could name the other winner and revert it.
	if got := forConflict(plan); len(got) != 1 {
		t.Fatalf("adjudicated conflict: actions %+v, want only the cascade_revert", got)
	}

	if _, err := s.CascadeRevert(cascade.FindingID); err != nil {
		t.Fatal(err)
	}
	if err := s.Conflicts().Resolve(conflictID, cascade.Wave, "finding survives; the loser's dependents were reverted"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	plan, reason = scan()
	if got := forConflict(plan); len(got) != 0 {
		t.Fatalf("the resolved conflict is still planned for: %+v", got)
	}
	if strings.Contains(reason, "unresolved conflicts") {
		t.Fatalf("termination still blocked on a resolved conflict: %q", reason)
	}
}
