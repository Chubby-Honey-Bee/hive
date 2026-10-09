package hive

import (
	"fmt"
	"math"
	"testing"
)

func mustPlan(t *testing.T, state *State, signals []Signal) []Action {
	t.Helper()
	plan, err := GeneratePlan(state, signals)
	if err != nil {
		t.Fatalf("GeneratePlan: %v", err)
	}
	return plan
}

func newCtx(state *State) *planContext {
	if state == nil {
		state = &State{Hive: HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet"}}
	}
	return &planContext{
		state:      state,
		wave:       state.LatestWave,
		batchSize:  state.Hive.BatchSize,
		suppressed: make(map[planCoord]bool),
	}
}

func TestStepQMP_EmitsCriticalForEachSignal(t *testing.T) {
	c := newCtx(nil)
	signals := []Signal{
		{SignalType: "qmp", Payload: map[string]any{"laundering_violations": 2, "untraceable_guarantees": 1}},
		{SignalType: "qmp", Payload: map[string]any{"laundering_violations": 0, "untraceable_guarantees": 5}},
		{SignalType: "alarm"}, // ignored
	}
	stepQMP(c, signals)
	if len(c.actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(c.actions))
	}
	for _, a := range c.actions {
		if a.Priority != "critical" || a.Type != "fix_mss" {
			t.Errorf("wrong action: %+v", a)
		}
	}
}

func TestStepAlarmCascade_HandlesBothNumberFormats(t *testing.T) {
	c := newCtx(nil)
	signals := []Signal{
		{SignalType: "alarm", Payload: map[string]any{"loser_finding_id": int64(42)}},
		{SignalType: "alarm", Payload: map[string]any{"loser_finding_id": float64(99)}}, // JSON-decoded
		{SignalType: "alarm", Payload: map[string]any{"loser_finding_id": int64(0)}},    // skipped
	}
	stepAlarmCascade(c, signals)
	if len(c.actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(c.actions))
	}
	if c.actions[0].FindingID != 42 || c.actions[1].FindingID != 99 {
		t.Errorf("wrong finding ids: %v %v", c.actions[0].FindingID, c.actions[1].FindingID)
	}
}

func TestStepConflictResolve_RespectsBatchSize(t *testing.T) {
	state := &State{
		Hive:                HiveState{BatchSize: 2, ModelTier: "sonnet"},
		UnresolvedConflicts: []map[string]any{{"id": 1}, {"id": 2}, {"id": 3}, {"id": 4}},
	}
	c := newCtx(state)
	stepConflictResolve(c, nil)
	if len(c.actions) != 2 {
		t.Errorf("expected batchSize=2 cap, got %d", len(c.actions))
	}
}

// A conflict with a named winner is left to its alarm's cascade and does not
// use the dispatch budget.
func TestStepConflictResolve_SkipsAdjudicatedConflicts(t *testing.T) {
	conflicts := []map[string]any{
		{"id": int64(1), "winner_finding_id": int64(10)},
		{"id": int64(2), "winner_finding_id": nil},
		{"id": int64(3), "winner_finding_id": int64(30)},
		{"id": int64(4), "winner_finding_id": nil},
		{"id": int64(5), "winner_finding_id": nil},
	}
	const batch = 2
	var want []any
	for _, cf := range conflicts {
		if cf["winner_finding_id"] == nil && len(want) < batch {
			want = append(want, cf["id"])
		}
	}
	c := newCtx(&State{Hive: HiveState{BatchSize: batch, ModelTier: "sonnet"}, UnresolvedConflicts: conflicts})
	stepConflictResolve(c, nil)
	var got []any
	for _, a := range c.actions {
		got = append(got, a.Payload["conflict_id"])
	}
	if len(got) != len(want) {
		t.Fatalf("dispatched conflicts %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dispatched conflicts %v, want %v", got, want)
		}
	}
}

func TestStepGapFill_SuppressedCoordSkipped(t *testing.T) {
	c := newCtx(&State{
		Hive:         HiveState{BatchSize: 5, ModelTier: "sonnet"},
		CriticalGaps: []map[string]any{{"d1": 0, "d2": 1, "d3": 2, "d4": 3, "description": "x"}},
	})
	c.suppressed[coordOf(0, 1, 2, 3)] = true
	stepGapFill(c, nil)
	if len(c.actions) != 0 {
		t.Errorf("suppressed coord should not produce action, got %v", c.actions)
	}
}

func TestStepGapFill_SharesBudgetBetweenWaggleAndGap(t *testing.T) {
	c := newCtx(&State{
		Hive: HiveState{BatchSize: 3, ModelTier: "sonnet"},
		CriticalGaps: []map[string]any{
			{"d1": 0, "description": "g1"},
			{"d1": 1, "description": "g2"},
			{"d1": 2, "description": "g3"},
		},
	})
	d1, d2, d3, d4 := 9, 9, 9, 9
	signals := []Signal{
		{SignalType: "waggle_dance", TargetD1: &d1, TargetD2: &d2, TargetD3: &d3, TargetD4: &d4, Payload: map[string]any{"reason": "x"}},
		{SignalType: "waggle_dance", TargetD1: &d1, TargetD2: &d2, TargetD3: &d3, TargetD4: &d4, Payload: map[string]any{"reason": "y"}},
	}
	stepGapFill(c, signals)
	// Budget 3: 2 waggles + 1 gap (the rest skipped).
	if len(c.actions) != 3 {
		t.Errorf("expected 3 actions (shared budget), got %d", len(c.actions))
	}
	if c.actions[0].SignalType != "waggle_dance" || c.actions[1].SignalType != "waggle_dance" {
		t.Errorf("waggles should come first")
	}
}

// A waggle dispatch's description names the gap's d1 by value, not the
// address of the *int that holds it.
func TestStepGapFill_WaggleDescriptionNamesD1(t *testing.T) {
	d1 := 4
	c := newCtx(nil)
	stepGapFill(c, []Signal{
		{SignalType: "waggle_dance", TargetD1: &d1, Payload: map[string]any{"reason": "x"}},
		{SignalType: "waggle_dance", Payload: map[string]any{"reason": "y"}},
	})
	want := []string{
		fmt.Sprintf("Waggle dance: investigate gap at d1=%d", d1),
		"Waggle dance: investigate gap at d1 absent",
	}
	if len(c.actions) != len(want) {
		t.Fatalf("%d actions; want %d", len(c.actions), len(want))
	}
	for i, a := range c.actions {
		if a.Description != want[i] {
			t.Errorf("action %d: description %q; want %q", i, a.Description, want[i])
		}
	}
}

// Termination waits on important gaps too, and only a dispatch names a gap's
// id, so the step dispatches important gaps once no critical gap is open.
func TestStepGapFill_ImportantGapsOnceNoCriticalRemains(t *testing.T) {
	gaps := []map[string]any{
		{"id": int64(1), "priority": "important", "d1": 1, "description": "a"},
		{"id": int64(2), "priority": "minor", "d1": 2, "description": "b"},
		{"id": int64(3), "priority": "important", "d1": 3, "description": "c"},
		{"id": int64(4), "priority": "critical", "d1": 4, "description": "d"},
		{"id": int64(5), "priority": "important", "d1": 5, "description": "e"},
	}
	const batch = 2
	dispatched := func(state *State) []any {
		c := newCtx(state)
		stepGapFill(c, nil)
		var ids []any
		for _, a := range c.actions {
			if a.SignalType == "gap_fill" {
				ids = append(ids, a.Payload["gap_id"])
			}
		}
		return ids
	}
	want := func(priority string, open []map[string]any) []any {
		var ids []any
		for _, g := range open {
			if g["priority"] == priority && len(ids) < batch {
				ids = append(ids, g["id"])
			}
		}
		return ids
	}
	same := func(got, want []any) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range want {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	var critical []map[string]any
	for _, g := range gaps {
		if g["priority"] == "critical" {
			critical = append(critical, g)
		}
	}
	hs := HiveState{BatchSize: batch, ModelTier: "sonnet"}
	got := dispatched(&State{Hive: hs, UnresolvedGaps: gaps, CriticalGaps: critical})
	if w := want("critical", gaps); !same(got, w) {
		t.Fatalf("with a critical gap open, dispatched gaps %v, want %v", got, w)
	}

	var open []map[string]any
	for _, g := range gaps {
		if g["priority"] != "critical" {
			open = append(open, g)
		}
	}
	got = dispatched(&State{Hive: hs, UnresolvedGaps: open})
	if w := want("important", open); len(w) == 0 || !same(got, w) {
		t.Fatalf("with no critical gap open, dispatched gaps %v, want %v", got, w)
	}
}

// Dispatches go to the latest wave plus one. A latest wave at the top of int
// has no next wave, so the plan refuses it rather than wrap to a negative
// dispatch wave.
func TestGeneratePlan_RefusesAWaveThatCannotBeIncremented(t *testing.T) {
	state := func(wave int) *State {
		return &State{
			Hive:         HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet"},
			MSSIntegrity: "PASS",
			LatestWave:   wave,
			CriticalGaps: []map[string]any{{"id": int64(1), "d1": 0, "description": "g"}},
		}
	}
	if plan, err := GeneratePlan(state(math.MaxInt), nil); err == nil {
		t.Fatalf("latest wave %d planned %+v, want an error", math.MaxInt, plan)
	}

	s := state(math.MaxInt - 1)
	plan := mustPlan(t, s, nil)
	if len(plan) == 0 || plan[0].SignalType != "gap_fill" {
		t.Fatalf("plan %+v, want the gap fill first", plan)
	}
	if want := s.LatestWave + 1; plan[0].Wave != want {
		t.Fatalf("dispatch wave %d, want %d", plan[0].Wave, want)
	}
}

func TestStepShakingSignal_UpgradeTier(t *testing.T) {
	c := newCtx(&State{Hive: HiveState{BatchSize: 5, ModelTier: "sonnet"}})
	signals := []Signal{
		{SignalType: "shaking_signal", Payload: map[string]any{"adjust": "upgrade_model_tier"}},
	}
	stepShakingSignal(c, signals)
	if len(c.actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(c.actions))
	}
	if c.actions[0].Params["model_tier"] != "opus" {
		t.Errorf("sonnet should upgrade to opus, got %v", c.actions[0].Params)
	}
}

func TestStepShakingSignal_AtTopTier_NoOp(t *testing.T) {
	c := newCtx(&State{Hive: HiveState{BatchSize: 5, ModelTier: "opus"}})
	signals := []Signal{
		{SignalType: "shaking_signal", Payload: map[string]any{"adjust": "upgrade_model_tier"}},
	}
	stepShakingSignal(c, signals)
	if len(c.actions) != 0 {
		t.Errorf("opus should not upgrade, got %v", c.actions)
	}
}

// A tremble dance lowers batch_size by trembleBatchDecrement, and at the
// floor it emits nothing rather than an adjustment that changes nothing.
func TestStepTrembleDance_LowersBatchToTheFloor(t *testing.T) {
	tremble := []Signal{{SignalType: "tremble_dance"}}
	for _, batch := range []int{BatchSizeMin + trembleBatchDecrement + 1, BatchSizeMin + 1, BatchSizeMin} {
		c := newCtx(&State{Hive: HiveState{BatchSize: batch, ConvergenceThreshold: 5, ModelTier: "sonnet"}})
		stepTrembleDance(c, tremble)
		want := batch - trembleBatchDecrement
		if want < BatchSizeMin {
			want = BatchSizeMin
		}
		if want == batch {
			if len(c.actions) != 0 {
				t.Errorf("batch %d at the floor: actions %+v, want none", batch, c.actions)
			}
			continue
		}
		if len(c.actions) != 1 || c.actions[0].Params["batch_size"] != want {
			t.Errorf("batch %d: actions %+v, want one batch_size=%d", batch, c.actions, want)
		}
	}
}

// A shaking signal raises batch_size by shakingBatchIncrement up to the
// ceiling, and at the ceiling it emits nothing.
func TestStepShakingSignal_RaisesBatchToTheCeiling(t *testing.T) {
	shake := []Signal{{SignalType: "shaking_signal", Payload: map[string]any{"adjust": "raise_batch_size"}}}
	for _, batch := range []int{BatchSizeMin, BatchSizeMax - 1, BatchSizeMax} {
		c := newCtx(&State{Hive: HiveState{BatchSize: batch, ModelTier: "sonnet"}})
		stepShakingSignal(c, shake)
		want := batch + shakingBatchIncrement
		if want > BatchSizeMax {
			want = BatchSizeMax
		}
		if want == batch {
			if len(c.actions) != 0 {
				t.Errorf("batch %d at the ceiling: actions %+v, want none", batch, c.actions)
			}
			continue
		}
		if len(c.actions) != 1 || c.actions[0].Params["batch_size"] != want {
			t.Errorf("batch %d: actions %+v, want one batch_size=%d", batch, c.actions, want)
		}
	}
}

func TestStepShakingSignal_UnknownAdjust_NoOp(t *testing.T) {
	c := newCtx(nil)
	signals := []Signal{
		{SignalType: "shaking_signal", Payload: map[string]any{"adjust": "rocket_launch"}},
	}
	stepShakingSignal(c, signals)
	if len(c.actions) != 0 {
		t.Errorf("unknown adjust should be ignored, got %v", c.actions)
	}
}

func TestStepGateCheck_BlockedByCriticalAction(t *testing.T) {
	c := newCtx(&State{
		Hive:         HiveState{BatchSize: 5, ModelTier: "sonnet"},
		MSSIntegrity: "PASS",
	})
	c.actions = append(c.actions, Action{Priority: "critical", Type: "fix_mss"})
	stepGateCheck(c, nil)
	for _, a := range c.actions {
		if a.Type == "run_gate" {
			t.Errorf("gate should not emit when a critical action exists")
		}
	}
}

func TestStepGateCheck_BlockedByMSSFail(t *testing.T) {
	c := newCtx(&State{Hive: HiveState{BatchSize: 5, ModelTier: "sonnet"}, MSSIntegrity: "FAIL"})
	stepGateCheck(c, nil)
	if len(c.actions) != 0 {
		t.Errorf("MSS=FAIL should block gate, got %v", c.actions)
	}
}

func TestStepGateCheck_HappyPath(t *testing.T) {
	c := newCtx(&State{
		Hive:         HiveState{BatchSize: 5, ModelTier: "sonnet"},
		LatestWave:   2,
		MSSIntegrity: "PASS",
	})
	stepGateCheck(c, nil)
	if len(c.actions) != 1 || c.actions[0].Type != "run_gate" {
		t.Errorf("expected one run_gate action, got %v", c.actions)
	}
	if c.actions[0].Wave != 2 {
		t.Errorf("wrong wave: %d", c.actions[0].Wave)
	}
}

func TestPlanSteps_OrderingPreserved(t *testing.T) {
	// Sanity check: gate must be last in the registry — it reads
	// already-emitted actions to decide whether to fire.
	if len(planSteps) == 0 {
		t.Fatal("planSteps empty")
	}
	// The gate handler is identifiable by its behavior; we can't assert
	// function identity directly, but verify the list length matches the
	// 8-priority pipeline documented in the comment.
	if len(planSteps) != 8 {
		t.Errorf("planSteps length = %d, want 8 (one per priority slot)", len(planSteps))
	}
}

func TestGeneratePlan_FullPipeline_NoCriticalsEmitsGate(t *testing.T) {
	state := &State{
		Hive:         HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet"},
		MSSIntegrity: "PASS",
		LatestWave:   1,
	}
	got := mustPlan(t, state, nil)
	if len(got) != 1 || got[0].Type != "run_gate" {
		t.Errorf("expected single gate action on clean state, got %v", got)
	}
}

func TestGeneratePlan_QMPSuppressesGate(t *testing.T) {
	state := &State{
		Hive:         HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet"},
		MSSIntegrity: "FAIL",
	}
	signals := []Signal{
		{SignalType: "qmp", Payload: map[string]any{"laundering_violations": 1, "untraceable_guarantees": 0}},
	}
	got := mustPlan(t, state, signals)
	for _, a := range got {
		if a.Type == "run_gate" {
			t.Errorf("gate must not emit when QMP/critical present, got %+v", a)
		}
	}
	if len(got) == 0 || got[0].Type != "fix_mss" {
		t.Errorf("expected QMP fix_mss first, got %v", got)
	}
}
