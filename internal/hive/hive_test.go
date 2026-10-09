package hive

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := db.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func initProject(t *testing.T, s *db.Store, project string) {
	t.Helper()
	s.WriteDB.Exec("INSERT INTO hive_state (project) VALUES (?)", project)
}

func addFinding(t *testing.T, s *db.Store, label, text string, d1 int) int64 {
	t.Helper()
	res, err := s.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding) VALUES (1,'test',?,0,0,0,?,?)`,
		d1, label, text,
	)
	if err != nil {
		t.Fatalf("addFinding: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// ───── SCAN STATE ─────

func TestScanEmptyProject(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	state, err := ScanState(s, "test-project")
	if err != nil {
		t.Fatalf("ScanState: %v", err)
	}
	if state.TotalFindings != 0 {
		t.Fatalf("expected 0 findings, got %d", state.TotalFindings)
	}
	if state.LatestWave != 0 {
		t.Fatalf("expected wave 0, got %d", state.LatestWave)
	}
	if state.MSSIntegrity != "PASS" {
		t.Fatalf("expected PASS, got %s", state.MSSIntegrity)
	}
	if state.LabelSkew != 0.0 {
		t.Fatalf("expected 0 skew, got %f", state.LabelSkew)
	}
	if state.ConflictRate != 0.0 {
		t.Fatalf("expected 0 conflict rate, got %f", state.ConflictRate)
	}
}

func TestScanWithFindings(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	addFinding(t, s, "definition", "Def A", 0)
	addFinding(t, s, "assumption", "Assumption B", 0)
	addFinding(t, s, "assumption", "Assumption C", 0)

	state, _ := ScanState(s, "test-project")
	if state.TotalFindings != 3 {
		t.Fatalf("expected 3 findings, got %d", state.TotalFindings)
	}
	if state.Labels["definition"] != 1 {
		t.Fatalf("expected 1 definition, got %d", state.Labels["definition"])
	}
	if state.Labels["assumption"] != 2 {
		t.Fatalf("expected 2 assumptions, got %d", state.Labels["assumption"])
	}
	if state.LatestWave != 1 {
		t.Fatalf("expected wave 1, got %d", state.LatestWave)
	}
	expectedSkew := 2.0 / 3.0
	if state.LabelSkew < expectedSkew-0.01 || state.LabelSkew > expectedSkew+0.01 {
		t.Fatalf("expected skew ~%.2f, got %.2f", expectedSkew, state.LabelSkew)
	}
}

func TestScanDetectsLaundering(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	aID := addFinding(t, s, "unknown", "Unknown base", 0)
	// Bypass MSS checks to create a laundering violation
	depsJSON, _ := json.Marshal([]int64{aID})
	s.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding, depends_on_ids)
		 VALUES (1,'test',0,0,0,0,'guarantee','Laundered',?)`,
		string(depsJSON),
	)

	state, _ := ScanState(s, "test-project")
	if state.MSSIntegrity != "FAIL" {
		t.Fatalf("expected FAIL, got %s", state.MSSIntegrity)
	}
	if state.LaunderingViolations != 1 {
		t.Fatalf("expected 1 laundering violation, got %d", state.LaunderingViolations)
	}
}

func TestScanDetectsUntraceable(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	s.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding)
		 VALUES (1,'test',0,0,0,0,'guarantee','Untraceable guarantee')`,
	)
	state, _ := ScanState(s, "test-project")
	if state.MSSIntegrity != "FAIL" {
		t.Fatalf("expected FAIL, got %s", state.MSSIntegrity)
	}
	if state.UntraceableGuarantees != 1 {
		t.Fatalf("expected 1 untraceable, got %d", state.UntraceableGuarantees)
	}
}

func TestScanUninitializedProjectFails(t *testing.T) {
	s := newTestStore(t)
	_, err := ScanState(s, "nonexistent")
	if err == nil {
		t.Fatal("expected error for uninitialized project")
	}
}

func TestScanIncludesGaps(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	s.WriteDB.Exec("INSERT INTO gaps (wave, agent, description, priority, d1, d2, d3, d4) VALUES (1,'test','Critical gap','critical',0,0,0,0)")
	s.WriteDB.Exec("INSERT INTO gaps (wave, agent, description, priority, d1, d2, d3, d4) VALUES (1,'test','Minor gap','minor',0,1,0,0)")
	state, _ := ScanState(s, "test-project")
	if len(state.UnresolvedGaps) != 2 {
		t.Fatalf("expected 2 gaps, got %d", len(state.UnresolvedGaps))
	}
	if len(state.CriticalGaps) != 1 {
		t.Fatalf("expected 1 critical gap, got %d", len(state.CriticalGaps))
	}
}

func TestScanIncludesConflicts(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	aID := addFinding(t, s, "assumption", "Fact A", 0)
	bID := addFinding(t, s, "assumption", "Fact B", 0)
	s.WriteDB.Exec("INSERT INTO conflicts (wave, finding_a_id, finding_b_id, description) VALUES (1,?,?,'They disagree')", aID, bID)
	state, _ := ScanState(s, "test-project")
	if len(state.UnresolvedConflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(state.UnresolvedConflicts))
	}
	expectedRate := 0.5
	if state.ConflictRate < expectedRate-0.01 || state.ConflictRate > expectedRate+0.01 {
		t.Fatalf("expected conflict rate ~%.2f, got %.2f", expectedRate, state.ConflictRate)
	}
}

func TestScanIncludesPendingSignals(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	s.WriteDB.Exec("INSERT INTO signals (signal_type, payload_json) VALUES ('waggle_dance', '{}')")
	s.WriteDB.Exec("INSERT INTO signals (signal_type, payload_json, acted_on) VALUES ('alarm', '{}', 1)")
	state, _ := ScanState(s, "test-project")
	if len(state.PendingSignals) != 1 {
		t.Fatalf("expected 1 pending signal, got %d", len(state.PendingSignals))
	}
}

// ───── intPtrFromAny ─────

func TestIntPtrFromAny(t *testing.T) {
	intPtr := func(i int) *int { return &i }
	tests := []struct {
		name string
		in   any
		want *int
	}{
		{"nil input", nil, nil},
		{"int64 positive", int64(42), intPtr(42)},
		{"int64 zero", int64(0), intPtr(0)},
		{"int64 negative", int64(-7), intPtr(-7)},
		{"float64 positive", float64(3.9), intPtr(3)},
		{"float64 negative", float64(-2.1), intPtr(-2)},
		{"int positive", int(99), intPtr(99)},
		{"int zero", int(0), intPtr(0)},
		{"unknown type string", "hello", nil},
		{"unknown type bool", true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := intPtrFromAny(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %v", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %v, got nil", *tc.want)
			}
			if *got != *tc.want {
				t.Fatalf("expected %v, got %v", *tc.want, *got)
			}
		})
	}
}

// ───── EVALUATE SIGNALS ─────

func TestQMPFiresOnMSSFail(t *testing.T) {
	s := newTestStore(t)
	state := &State{
		Hive:         HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet", ConflictRateThreshold: 0.15},
		MSSIntegrity: "FAIL", LaunderingViolations: 2, UntraceableGuarantees: 1,
		Labels: map[string]int{}, Convergence: map[string]int{},
	}
	signals := EvaluateSignals(s, state)
	var qmpSignals []Signal
	for _, sig := range signals {
		if sig.SignalType == "qmp" {
			qmpSignals = append(qmpSignals, sig)
		}
	}
	if len(qmpSignals) != 1 {
		t.Fatalf("expected 1 qmp, got %d", len(qmpSignals))
	}
	if v, ok := qmpSignals[0].Payload["laundering_violations"].(int); !ok || v != 2 {
		t.Fatalf("expected laundering_violations=2, got %v", qmpSignals[0].Payload["laundering_violations"])
	}
}

func TestNoQMPWhenMSSPasses(t *testing.T) {
	s := newTestStore(t)
	state := &State{
		Hive:         HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet", ConflictRateThreshold: 0.15},
		MSSIntegrity: "PASS",
		Labels:       map[string]int{}, Convergence: map[string]int{},
	}
	signals := EvaluateSignals(s, state)
	for _, sig := range signals {
		if sig.SignalType == "qmp" {
			t.Fatal("expected no qmp signal")
		}
	}
}

// The tier upgrade answers a latest wave that unknowns dominate, and no other
// skew: a wave of assumptions is honest research, whatever its label skew.
func TestShakingSignalFiresOnAnUnknownDominatedWave(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range []struct {
		labels map[string]int
		skew   float64
	}{
		{map[string]int{"unknown": 5}, 1.0},
		{map[string]int{"assumption": 5}, 1.0},
	} {
		state := &State{
			Hive:             HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet", ConflictRateThreshold: 0.15},
			MSSIntegrity:     "PASS",
			LabelSkew:        tc.skew,
			LatestWaveLabels: tc.labels,
			Labels:           tc.labels, Convergence: map[string]int{},
		}
		found := false
		for _, sig := range EvaluateSignals(s, state) {
			if sig.SignalType == "shaking_signal" && sig.Payload["adjust"] == "upgrade_model_tier" {
				found = true
			}
		}
		n := 0
		for _, c := range tc.labels {
			n += c
		}
		if want := n >= 5 && tc.labels["unknown"]*2 > n; found != want {
			t.Errorf("latest wave %v: upgrade_model_tier shaking = %v, want %v", tc.labels, found, want)
		}
	}
}

func TestQuorumDetection(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	s.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding,
		 convergence_count, convergence_level)
		 VALUES (1,'test',0,0,0,0,'assumption','Converged finding',5,'high')`,
	)
	state, _ := ScanState(s, "test-project")
	signals := EvaluateSignals(s, state)
	var quorum []Signal
	for _, sig := range signals {
		if sig.SignalType == "quorum" {
			quorum = append(quorum, sig)
		}
	}
	if len(quorum) != 1 {
		t.Fatalf("expected 1 quorum, got %d", len(quorum))
	}
}

func TestQuorumBlockedByUnresolvedConflict(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	aID := addFinding(t, s, "assumption", "Converged", 0)
	s.WriteDB.Exec("UPDATE findings SET convergence_count=5, convergence_level='high' WHERE id=?", aID)
	bID := addFinding(t, s, "assumption", "Conflicting", 0)
	s.WriteDB.Exec("INSERT INTO conflicts (wave, finding_a_id, finding_b_id, description) VALUES (1,?,?,'They disagree')", aID, bID)

	state, _ := ScanState(s, "test-project")
	signals := EvaluateSignals(s, state)
	for _, sig := range signals {
		if sig.SignalType == "quorum" {
			t.Fatal("expected no quorum when conflict exists")
		}
	}
}

// ───── GENERATE PLAN ─────

func TestQMPActionsAreCritical(t *testing.T) {
	state := baseState()
	signals := []Signal{{SignalType: "qmp", Payload: map[string]any{
		"laundering_violations": 2, "untraceable_guarantees": 1,
	}}}
	plan := mustPlan(t, state, signals)
	if len(plan) == 0 {
		t.Fatal("expected at least 1 action")
	}
	if plan[0].Type != "fix_mss" || plan[0].Priority != "critical" {
		t.Fatalf("expected fix_mss/critical, got %s/%s", plan[0].Type, plan[0].Priority)
	}
}

func TestAlarmGeneratesCascadeAction(t *testing.T) {
	state := baseState()
	signals := []Signal{{SignalType: "alarm", Payload: map[string]any{
		"loser_finding_id": int64(42), "conflict_id": int64(1),
	}}}
	plan := mustPlan(t, state, signals)
	var cascade []Action
	for _, a := range plan {
		if a.Type == "cascade_revert" {
			cascade = append(cascade, a)
		}
	}
	if len(cascade) != 1 {
		t.Fatalf("expected 1 cascade, got %d", len(cascade))
	}
	if cascade[0].FindingID != 42 {
		t.Fatalf("expected finding_id=42, got %d", cascade[0].FindingID)
	}
}

func TestConflictDispatchesVerifier(t *testing.T) {
	state := baseState()
	state.UnresolvedConflicts = []map[string]any{{
		"id": int64(1), "finding_a_id": int64(10), "finding_b_id": int64(11),
		"description": "Numbers disagree",
	}}
	plan := mustPlan(t, state, nil)
	var verifiers []Action
	for _, a := range plan {
		if a.Type == "dispatch_agent" && a.Agent == "verifier" {
			verifiers = append(verifiers, a)
		}
	}
	if len(verifiers) != 1 {
		t.Fatalf("expected 1 verifier dispatch, got %d", len(verifiers))
	}
}

func TestWaggleDanceDispatchesResearcher(t *testing.T) {
	state := baseState()
	d1 := 0
	signals := []Signal{{
		SignalType: "waggle_dance",
		TargetD1:   &d1, TargetD2: &d1, TargetD3: &d1, TargetD4: &d1,
		Payload: map[string]any{"reason": "High convergence with gap"},
	}}
	plan := mustPlan(t, state, signals)
	var researchers []Action
	for _, a := range plan {
		if a.Type == "dispatch_agent" && a.Agent == "hive-scout" && a.SignalType == "waggle_dance" {
			researchers = append(researchers, a)
		}
	}
	if len(researchers) != 1 {
		t.Fatalf("expected 1 researcher dispatch, got %d", len(researchers))
	}
}

func TestStopSignalSuppressesWaggle(t *testing.T) {
	state := baseState()
	d1, d2, d3, d4 := 0, 1, 0, 0
	signals := []Signal{
		{SignalType: "stop_signal", TargetD1: &d1, TargetD2: &d2, TargetD3: &d3, TargetD4: &d4},
		{SignalType: "waggle_dance", TargetD1: &d1, TargetD2: &d2, TargetD3: &d3, TargetD4: &d4,
			Payload: map[string]any{"reason": "Should be suppressed"}},
	}
	plan := mustPlan(t, state, signals)
	for _, a := range plan {
		if a.SignalType == "waggle_dance" {
			t.Fatal("waggle_dance should be suppressed by stop_signal")
		}
	}
}

func TestShakingUpgradesModelTier(t *testing.T) {
	state := baseState()
	state.Hive.ModelTier = "sonnet"
	signals := []Signal{{SignalType: "shaking_signal", Payload: map[string]any{
		"adjust": "upgrade_model_tier",
	}}}
	plan := mustPlan(t, state, signals)
	var adjust []Action
	for _, a := range plan {
		if a.Type == "adjust_params" && a.SignalType == "shaking_signal" {
			adjust = append(adjust, a)
		}
	}
	if len(adjust) != 1 {
		t.Fatalf("expected 1 adjust, got %d", len(adjust))
	}
	if adjust[0].Params["model_tier"] != "opus" {
		t.Fatalf("expected opus, got %v", adjust[0].Params["model_tier"])
	}
}

func TestQuorumGeneratesCapAction(t *testing.T) {
	state := baseState()
	signals := []Signal{{SignalType: "quorum", Payload: map[string]any{
		"finding_id": int64(99), "convergence_count": int64(5),
	}}}
	plan := mustPlan(t, state, signals)
	var caps []Action
	for _, a := range plan {
		if a.SignalType == "quorum" {
			caps = append(caps, a)
		}
	}
	if len(caps) != 1 || caps[0].Type != "cap_finding" {
		t.Fatalf("quorum actions %+v, want one cap_finding", caps)
	}
	if caps[0].FindingID != 99 {
		t.Fatalf("expected finding_id=99, got %d", caps[0].FindingID)
	}
}

func TestGateCheckWhenClean(t *testing.T) {
	state := baseState()
	plan := mustPlan(t, state, nil)
	var gates []Action
	for _, a := range plan {
		if a.Type == "run_gate" {
			gates = append(gates, a)
		}
	}
	if len(gates) != 1 {
		t.Fatalf("expected 1 gate, got %d", len(gates))
	}
}

func TestGateBlockedByQMP(t *testing.T) {
	state := baseState()
	signals := []Signal{{SignalType: "qmp", Payload: map[string]any{
		"laundering_violations": 1, "untraceable_guarantees": 0,
	}}}
	plan := mustPlan(t, state, signals)
	for _, a := range plan {
		if a.Type == "run_gate" {
			t.Fatal("gate should be blocked by QMP")
		}
	}
}

func TestGateBlockedByCriticalGaps(t *testing.T) {
	state := baseState()
	state.CriticalGaps = []map[string]any{{"d1": 0, "description": "Must fix"}}
	plan := mustPlan(t, state, nil)
	for _, a := range plan {
		if a.Type == "run_gate" {
			t.Fatal("gate should be blocked by critical gaps")
		}
	}
}

func TestBatchSizeLimitsDispatches(t *testing.T) {
	state := baseState()
	state.Hive.BatchSize = 2
	var signals []Signal
	for i := 0; i < 5; i++ {
		d := i
		signals = append(signals, Signal{
			SignalType: "waggle_dance",
			TargetD1:   &d, TargetD2: intPtrVal(0), TargetD3: intPtrVal(0), TargetD4: intPtrVal(0),
			Payload: map[string]any{"reason": "Gap"},
		})
	}
	plan := mustPlan(t, state, signals)
	var waggle int
	for _, a := range plan {
		if a.SignalType == "waggle_dance" {
			waggle++
		}
	}
	if waggle > 2 {
		t.Fatalf("expected <=2 waggle dispatches, got %d", waggle)
	}
}

func TestPriorityOrderingQMPBeforeWaggle(t *testing.T) {
	state := baseState()
	d := 0
	signals := []Signal{
		{SignalType: "waggle_dance", TargetD1: &d, TargetD2: &d, TargetD3: &d, TargetD4: &d,
			Payload: map[string]any{"reason": "gap"}},
		{SignalType: "qmp", Payload: map[string]any{
			"laundering_violations": 1, "untraceable_guarantees": 0}},
	}
	plan := mustPlan(t, state, signals)
	qmpIdx := -1
	waggleIdx := -1
	for i, a := range plan {
		if a.Type == "fix_mss" && qmpIdx < 0 {
			qmpIdx = i
		}
		if a.SignalType == "waggle_dance" {
			waggleIdx = i
		}
	}
	if qmpIdx >= 0 && waggleIdx >= 0 && qmpIdx >= waggleIdx {
		t.Fatalf("QMP (idx %d) should come before waggle (idx %d)", qmpIdx, waggleIdx)
	}
}

// ───── CHECK TERMINATION ─────

func TestTerminalWhenAllConditionsMet(t *testing.T) {
	state := terminalState()
	isTerminal, _ := CheckTermination(state)
	if !isTerminal {
		t.Fatal("expected terminal")
	}
}

func TestNotTerminalWithCriticalGaps(t *testing.T) {
	state := terminalState()
	state.CriticalGaps = []map[string]any{{"priority": "critical"}}
	isTerminal, reason := CheckTermination(state)
	if isTerminal {
		t.Fatal("expected not terminal")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}

func TestNotTerminalWithImportantGaps(t *testing.T) {
	state := terminalState()
	state.UnresolvedGaps = []map[string]any{{"priority": "important"}}
	isTerminal, _ := CheckTermination(state)
	if isTerminal {
		t.Fatal("expected not terminal")
	}
}

func TestNotTerminalWithConflicts(t *testing.T) {
	state := terminalState()
	state.UnresolvedConflicts = []map[string]any{{"id": 1}}
	isTerminal, _ := CheckTermination(state)
	if isTerminal {
		t.Fatal("expected not terminal")
	}
}

func TestNotTerminalWithMSSFail(t *testing.T) {
	state := terminalState()
	state.MSSIntegrity = "FAIL"
	isTerminal, reason := CheckTermination(state)
	if isTerminal {
		t.Fatal("expected not terminal")
	}
	if reason == "" {
		t.Fatal("expected reason containing MSS")
	}
}

func TestNotTerminalWithoutCompleteEval(t *testing.T) {
	state := terminalState()
	state.LatestEval = map[string]any{"verdict": "NEEDS_MORE_WORK"}
	isTerminal, _ := CheckTermination(state)
	if isTerminal {
		t.Fatal("expected not terminal")
	}
}

func TestNotTerminalWithoutAnyEval(t *testing.T) {
	state := terminalState()
	state.LatestEval = nil
	isTerminal, _ := CheckTermination(state)
	if isTerminal {
		t.Fatal("expected not terminal")
	}
}

func TestNotTerminalWithoutGates(t *testing.T) {
	state := terminalState()
	state.WaveGates = nil
	isTerminal, _ := CheckTermination(state)
	if isTerminal {
		t.Fatal("expected not terminal")
	}
}

func TestMinorGapsDontBlockTermination(t *testing.T) {
	state := terminalState()
	state.UnresolvedGaps = []map[string]any{{"priority": "minor"}}
	isTerminal, _ := CheckTermination(state)
	if !isTerminal {
		t.Fatal("expected terminal (minor gaps don't block)")
	}
}

// ───── GAIN CONTROL ─────

func TestApplyBatchSize(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	applyGainControl(s.WriteDB, "test-project", map[string]any{"batch_size": 12})
	var bs int
	s.ReadDB.QueryRow("SELECT batch_size FROM hive_state WHERE project='test-project'").Scan(&bs)
	if bs != 12 {
		t.Fatalf("expected 12, got %d", bs)
	}
}

func TestBatchSizeClampedToMax(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	applyGainControl(s.WriteDB, "test-project", map[string]any{"batch_size": 100})
	var bs int
	s.ReadDB.QueryRow("SELECT batch_size FROM hive_state WHERE project='test-project'").Scan(&bs)
	if bs != BatchSizeMax {
		t.Fatalf("expected %d, got %d", BatchSizeMax, bs)
	}
}

func TestBatchSizeClampedToMin(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	applyGainControl(s.WriteDB, "test-project", map[string]any{"batch_size": 0})
	var bs int
	s.ReadDB.QueryRow("SELECT batch_size FROM hive_state WHERE project='test-project'").Scan(&bs)
	if bs != BatchSizeMin {
		t.Fatalf("expected %d, got %d", BatchSizeMin, bs)
	}
}

func TestConvergenceThresholdBounded(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	applyGainControl(s.WriteDB, "test-project", map[string]any{"convergence_threshold": 50})
	var ct int
	s.ReadDB.QueryRow("SELECT convergence_threshold FROM hive_state WHERE project='test-project'").Scan(&ct)
	if ct != ConvergenceMax {
		t.Fatalf("expected %d, got %d", ConvergenceMax, ct)
	}
}

func TestModelTierValid(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	applyGainControl(s.WriteDB, "test-project", map[string]any{"model_tier": "opus"})
	var mt string
	s.ReadDB.QueryRow("SELECT model_tier FROM hive_state WHERE project='test-project'").Scan(&mt)
	if mt != "opus" {
		t.Fatalf("expected opus, got %s", mt)
	}
}

func TestModelTierInvalidDefaultsSonnet(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	applyGainControl(s.WriteDB, "test-project", map[string]any{"model_tier": "gpt-4"})
	var mt string
	s.ReadDB.QueryRow("SELECT model_tier FROM hive_state WHERE project='test-project'").Scan(&mt)
	if mt != "sonnet" {
		t.Fatalf("expected sonnet, got %s", mt)
	}
}

func TestMultipleParamsAtOnce(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	applyGainControl(s.WriteDB, "test-project", map[string]any{
		"batch_size": 8, "convergence_threshold": 5, "model_tier": "opus",
	})
	var bs, ct int
	var mt string
	s.ReadDB.QueryRow("SELECT batch_size, convergence_threshold, model_tier FROM hive_state WHERE project='test-project'").Scan(&bs, &ct, &mt)
	if bs != 8 || ct != 5 || mt != "opus" {
		t.Fatalf("expected 8/5/opus, got %d/%d/%s", bs, ct, mt)
	}
}

// ───── HELPERS ─────

func baseState() *State {
	return &State{
		Hive:         HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet", ConflictRateThreshold: 0.15},
		MSSIntegrity: "PASS",
		Labels:       map[string]int{},
		Convergence:  map[string]int{},
	}
}

func terminalState() *State {
	return &State{
		Hive:          HiveState{Iteration: 5},
		TotalFindings: 20,
		LatestWave:    3,
		Labels:        map[string]int{"definition": 5, "guarantee": 10, "assumption": 5},
		MSSIntegrity:  "PASS",
		WaveGates:     []map[string]any{{"wave": 3}},
		LatestEval:    map[string]any{"verdict": "COMPLETE"},
		Convergence:   map[string]int{},
	}
}

func intPtrVal(v int) *int { return &v }

func TestAlarmSignalFiresWhenConflictHasWinner(t *testing.T) {
	s := newTestStore(t)
	aID := int64(10)
	bID := int64(11)
	conflictID := int64(1)
	state := &State{
		Hive:         HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet", ConflictRateThreshold: 0.15},
		MSSIntegrity: "PASS",
		UnresolvedConflicts: []map[string]any{
			{
				"id":                conflictID,
				"finding_a_id":      aID,
				"finding_b_id":      bID,
				"winner_finding_id": aID, // winner is A, loser should be B
			},
		},
		Labels:      map[string]int{},
		Convergence: map[string]int{},
	}
	signals := EvaluateSignals(s, state)
	var alarms []Signal
	for _, sig := range signals {
		if sig.SignalType == "alarm" {
			alarms = append(alarms, sig)
		}
	}
	if len(alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(alarms))
	}
	if alarms[0].Payload["loser_finding_id"] != bID {
		t.Fatalf("expected loser_finding_id=%d, got %v", bID, alarms[0].Payload["loser_finding_id"])
	}
}

func TestAlarmLoserSwappedWhenWinnerIsB(t *testing.T) {
	s := newTestStore(t)
	aID := int64(20)
	bID := int64(21)
	conflictID := int64(2)
	state := &State{
		Hive:         HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet", ConflictRateThreshold: 0.15},
		MSSIntegrity: "PASS",
		UnresolvedConflicts: []map[string]any{
			{
				"id":                conflictID,
				"finding_a_id":      aID,
				"finding_b_id":      bID,
				"winner_finding_id": bID, // winner is B → loser should be A
			},
		},
		Labels:      map[string]int{},
		Convergence: map[string]int{},
	}
	signals := EvaluateSignals(s, state)
	var alarms []Signal
	for _, sig := range signals {
		if sig.SignalType == "alarm" {
			alarms = append(alarms, sig)
		}
	}
	if len(alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(alarms))
	}
	if alarms[0].Payload["loser_finding_id"] != aID {
		t.Fatalf("expected loser_finding_id=%d (A), got %v", aID, alarms[0].Payload["loser_finding_id"])
	}
}

func TestWaggleDanceFiresOnHighConvergenceWithGapAtSameD1(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "test-project")
	// Insert a finding with convergence_level='high' at d1=3
	s.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding, convergence_level)
		 VALUES (1,'test',3,0,0,0,'assumption','Converged at d1=3','high')`,
	)

	// Build state with a gap at the same d1=3
	state := &State{
		Hive:         HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet", ConflictRateThreshold: 0.15},
		MSSIntegrity: "PASS",
		UnresolvedGaps: []map[string]any{
			{"d1": int64(3), "d2": int64(0), "d3": int64(0), "d4": int64(0), "description": "gap at d1=3", "priority": "important"},
		},
		Labels:      map[string]int{},
		Convergence: map[string]int{},
	}
	signals := EvaluateSignals(s, state)
	var waggle []Signal
	for _, sig := range signals {
		if sig.SignalType == "waggle_dance" {
			waggle = append(waggle, sig)
		}
	}
	if len(waggle) != 1 {
		t.Fatalf("expected 1 waggle_dance, got %d", len(waggle))
	}
	if waggle[0].TargetD1 == nil || *waggle[0].TargetD1 != 3 {
		t.Fatalf("expected TargetD1=3, got %v", waggle[0].TargetD1)
	}
}

func TestWaggleDanceNoFireWhenNoHighConvergence(t *testing.T) {
	s := newTestStore(t)
	// No findings with convergence_level='high' in DB
	state := &State{
		Hive:         HiveState{BatchSize: 5, ConvergenceThreshold: 3, ModelTier: "sonnet", ConflictRateThreshold: 0.15},
		MSSIntegrity: "PASS",
		UnresolvedGaps: []map[string]any{
			{"d1": int64(1), "d2": int64(0), "d3": int64(0), "d4": int64(0), "priority": "important"},
		},
		Labels:      map[string]int{},
		Convergence: map[string]int{},
	}
	signals := EvaluateSignals(s, state)
	for _, sig := range signals {
		if sig.SignalType == "waggle_dance" {
			t.Fatal("expected no waggle_dance when no high-convergence findings in DB")
		}
	}
}

// ───── toInt64 ─────

func TestToInt64(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int64
	}{
		{"int64", int64(42), 42},
		{"int64 zero", int64(0), 0},
		{"int64 negative", int64(-7), -7},
		{"float64", float64(3.9), 3},
		{"float64 negative", float64(-2.1), -2},
		{"int", int(99), 99},
		{"int negative", int(-1), -1},
		{"nil", nil, 0},
		{"string", "hello", 0},
		{"bool", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toInt64(tc.in)
			if got != tc.want {
				t.Errorf("toInt64(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
