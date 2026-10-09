package hive

import (
	"strings"
	"testing"
)

// A seeded critical gap is planned for, filled and resolved, and then
// neither planned for again nor blocking termination.
func TestGapLoop_ResolvedGapLeavesThePlanAndTheTerminationReasons(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	d1 := 0
	if err := s.Gaps().AddGap(1, "seed", "What is the default page size?", "critical", &d1, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	state, err := ScanState(s, "p")
	if err != nil {
		t.Fatal(err)
	}
	var gapID any
	for _, a := range mustPlan(t, state, EvaluateSignals(s, state)) {
		if a.SignalType == "gap_fill" {
			gapID = a.Payload["gap_id"]
		}
	}
	if gapID == nil {
		t.Fatal("the gap-fill action carries no gap_id, so its answer cannot close it")
	}
	if done, reason := CheckTermination(state); done || !strings.Contains(reason, "Critical gaps") {
		t.Fatalf("CheckTermination = %v %q, want blocked on the critical gap", done, reason)
	}

	finding := addFinding(t, s, "definition", "4096 bytes since SQLite 3.12.0", 0)
	id, _ := gapID.(int64)
	if err := s.Gaps().ResolveGap(id, 1, "hive-scout", finding); err != nil {
		t.Fatalf("ResolveGap: %v", err)
	}

	state, err = ScanState(s, "p")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range mustPlan(t, state, EvaluateSignals(s, state)) {
		if a.SignalType == "gap_fill" {
			t.Fatalf("the resolved gap is still planned for: %+v", a)
		}
	}
	if _, reason := CheckTermination(state); strings.Contains(reason, "Critical gaps") {
		t.Fatalf("termination still blocked on a resolved gap: %q", reason)
	}
}

// An important gap blocks termination as a critical one does, so once no
// critical gap remains it is dispatched with its id too, and the hive can
// terminate once it is filled.
func TestGapLoop_ImportantGapIsFilledOnceNoCriticalGapRemains(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	d1, d2 := 1, 2
	if err := s.Gaps().AddGap(1, "seed", "Which journal mode is the default?", "important", &d1, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Gaps().AddGap(1, "seed", "What is the default page size?", "critical", &d2, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	gapID := func(priority string) int64 {
		var id int64
		if err := s.ReadDB.QueryRow("SELECT id FROM gaps WHERE priority = ?", priority).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	important, critical := gapID("important"), gapID("critical")
	finding := addFinding(t, s, "definition", "4096 bytes since SQLite 3.12.0", 0)

	scan := func() ([]int64, string) {
		t.Helper()
		state, err := ScanState(s, "p")
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, a := range mustPlan(t, state, EvaluateSignals(s, state)) {
			if a.SignalType == "gap_fill" {
				id, _ := a.Payload["gap_id"].(int64)
				ids = append(ids, id)
			}
		}
		_, reason := CheckTermination(state)
		return ids, reason
	}

	if ids, _ := scan(); len(ids) != 1 || ids[0] != critical {
		t.Fatalf("with a critical gap open, gap fills %v, want only the critical gap %d", ids, critical)
	}
	if err := s.Gaps().ResolveGap(critical, 1, "hive-scout", finding); err != nil {
		t.Fatal(err)
	}

	ids, reason := scan()
	if len(ids) != 1 || ids[0] != important {
		t.Fatalf("with no critical gap open, gap fills %v, want the important gap %d", ids, important)
	}
	if !strings.Contains(reason, "important") {
		t.Fatalf("CheckTermination reason %q, want it blocked on the important gap", reason)
	}
	if err := s.Gaps().ResolveGap(important, 1, "hive-scout", finding); err != nil {
		t.Fatal(err)
	}

	ids, reason = scan()
	if len(ids) != 0 {
		t.Fatalf("resolved gaps still planned for: %v", ids)
	}
	if strings.Contains(reason, "important") || strings.Contains(reason, "Critical gaps") {
		t.Fatalf("termination still blocked on a resolved gap: %q", reason)
	}
}
