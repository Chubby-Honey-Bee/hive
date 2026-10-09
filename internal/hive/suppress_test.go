package hive

import (
	"fmt"
	"testing"
)

// coordString renders an action's target coordinates, which reach it as *int
// (from a signal) or int64 (from a gap row), so the test can count dispatches
// per coordinate without the planner's own comparison.
func coordString(tc map[string]any) string {
	part := func(v any) string {
		switch n := v.(type) {
		case *int:
			if n == nil {
				return "nil"
			}
			return fmt.Sprint(*n)
		case nil:
			return "nil"
		default:
			return fmt.Sprint(n)
		}
	}
	return part(tc["d1"]) + "," + part(tc["d2"]) + "," + part(tc["d3"]) + "," + part(tc["d4"])
}

// A pending stop_signal reaches the plan as a database row (int64
// coordinates) and meets a gap row and a waggle signal whose *int
// coordinates were allocated elsewhere. Suppression compares the values, so
// it holds on both paths; a coordinate the signal does not target still
// dispatches.
func TestStopSignal_SuppressesByCoordinateValue(t *testing.T) {
	stopped := [4]int{0, 1, 0, 0}
	open := [4]int{0, 2, 0, 0}
	gapRow := func(id int64, c [4]int) map[string]any {
		return map[string]any{"id": id, "priority": "critical", "description": "gap",
			"d1": int64(c[0]), "d2": int64(c[1]), "d3": int64(c[2]), "d4": int64(c[3])}
	}
	waggle := func(c [4]int) Signal {
		d1, d2, d3, d4 := c[0], c[1], c[2], c[3]
		return Signal{SignalType: "waggle_dance", TargetD1: &d1, TargetD2: &d2, TargetD3: &d3, TargetD4: &d4,
			Payload: map[string]any{"reason": "x"}}
	}
	state := baseState()
	state.Hive.BatchSize = 10
	state.CriticalGaps = []map[string]any{gapRow(1, stopped), gapRow(2, open)}
	state.PendingSignals = []map[string]any{{
		"id": int64(9), "signal_type": "stop_signal",
		"target_d1": int64(stopped[0]), "target_d2": int64(stopped[1]),
		"target_d3": int64(stopped[2]), "target_d4": int64(stopped[3]),
	}}

	dispatched := map[string]int{}
	for _, a := range mustPlan(t, state, []Signal{waggle(stopped), waggle(open)}) {
		if a.Type == "dispatch_agent" {
			dispatched[coordString(a.TargetCoords)]++
		}
	}
	key := func(c [4]int) string { return fmt.Sprintf("%d,%d,%d,%d", c[0], c[1], c[2], c[3]) }
	if n := dispatched[key(stopped)]; n != 0 {
		t.Errorf("%d dispatches at the stopped coordinate %v, want 0", n, stopped)
	}
	// One waggle and one gap fill target the open coordinate.
	if n := dispatched[key(open)]; n != 2 {
		t.Errorf("%d dispatches at the open coordinate %v, want 2 (plan: %v)", n, open, dispatched)
	}
}

// The same case through the database: `chb hive next` scans a critical gap
// and a pending stop_signal at one coordinate, and a waggle dance recruited
// to it. Neither dispatches.
func TestStopSignal_SuppressesThroughScanState(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	zero, one, two := 0, 1, 2
	if err := s.Gaps().AddGap(1, "seed", "stopped", "critical", &zero, &one, &zero, &zero); err != nil {
		t.Fatal(err)
	}
	if err := s.Gaps().AddGap(1, "seed", "open", "critical", &zero, &two, &zero, &zero); err != nil {
		t.Fatal(err)
	}
	// A high-convergence finding at d1=0 recruits a waggle dance to the
	// lowest open gap coordinate at d1=0, which is the stopped one.
	if _, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding, convergence_level)
		VALUES (1,'t',0,0,0,0,'definition','seen twice','high')`); err != nil {
		t.Fatal(err)
	}
	src := "system"
	if _, err := s.Signals().EmitSignal("stop_signal", &src, nil, &zero, &one, &zero, &zero, nil, nil); err != nil {
		t.Fatal(err)
	}

	state, err := ScanState(s, "p")
	if err != nil {
		t.Fatal(err)
	}
	signals := EvaluateSignals(s, state)
	recruited := false
	for _, sig := range signals {
		if sig.SignalType == "waggle_dance" && sig.TargetD2 != nil && *sig.TargetD2 == one {
			recruited = true
		}
	}
	if !recruited {
		t.Fatalf("setup: no waggle dance recruited to the stopped coordinate: %+v", signals)
	}
	dispatched := map[string]int{}
	for _, a := range mustPlan(t, state, signals) {
		if a.Type == "dispatch_agent" {
			dispatched[coordString(a.TargetCoords)]++
		}
	}
	if n := dispatched["0,1,0,0"]; n != 0 {
		t.Errorf("%d dispatches at the stopped coordinate, want 0 (plan: %v)", n, dispatched)
	}
	if n := dispatched["0,2,0,0"]; n != 1 {
		t.Errorf("%d dispatches at the open gap, want 1 (plan: %v)", n, dispatched)
	}
}
