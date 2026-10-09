package hive

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// One test per signal: seed the state that raises it, scan, plan, and check
// the plan's response and any gain change against values computed from the
// seed.

type coord [4]int

func (c coord) key() string { return fmt.Sprintf("%d,%d,%d,%d", c[0], c[1], c[2], c[3]) }

func addFindingAt(t *testing.T, s *db.Store, label, text string, c coord) int64 {
	t.Helper()
	res, err := s.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding) VALUES (1,'test',?,?,?,?,?,?)`,
		c[0], c[1], c[2], c[3], label, text,
	)
	if err != nil {
		t.Fatalf("add finding: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func addConflictBetween(t *testing.T, s *db.Store, a, b int64) int64 {
	t.Helper()
	if err := s.Conflicts().AddConflict(1, a, b, "[negation] test"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.ReadDB.QueryRow(`SELECT id FROM conflicts WHERE finding_a_id=? AND finding_b_id=?`, a, b).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func addGapAt(t *testing.T, s *db.Store, priority, text string, c coord) int64 {
	t.Helper()
	d1, d2, d3, d4 := c[0], c[1], c[2], c[3]
	if err := s.Gaps().AddGap(1, "seed", text, priority, &d1, &d2, &d3, &d4); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.ReadDB.QueryRow(`SELECT MAX(id) FROM gaps`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func scanAndPlan(t *testing.T, s *db.Store) (*State, []Signal, []Action) {
	t.Helper()
	state, err := ScanState(s, "p")
	if err != nil {
		t.Fatal(err)
	}
	signals := EvaluateSignals(s, state)
	return state, signals, mustPlan(t, state, signals)
}

func signalsOf(signals []Signal, kind string) []Signal { return filterSignals(signals, kind) }

func actionsOf(plan []Action, signalType string) []Action {
	var out []Action
	for _, a := range plan {
		if a.SignalType == signalType {
			out = append(out, a)
		}
	}
	return out
}

// batchAdjustments returns the batch_size values the plan's adjust_params
// actions carry, in order.
func batchAdjustments(plan []Action) []any {
	var out []any
	for _, a := range plan {
		if v, ok := a.Params["batch_size"]; ok && a.Type == "adjust_params" {
			out = append(out, v)
		}
	}
	return out
}

func storedParams(t *testing.T, s *db.Store) (batch, conv int, tier string) {
	t.Helper()
	if err := s.ReadDB.QueryRow(`SELECT batch_size, convergence_threshold, model_tier FROM hive_state WHERE project='p'`).Scan(&batch, &conv, &tier); err != nil {
		t.Fatal(err)
	}
	return
}

func clampBatch(n int) int { return clampInt(n, BatchSizeMin, BatchSizeMax) }

func targetKey(tc map[string]any) string { return coordString(tc) }

// qmp: a laundered guarantee fails the audit. The plan fixes MSS first,
// withholds the gate, and moves no parameter.
func TestSignal_QMP_HaltsTheGate(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	unknown := addFindingAt(t, s, "unknown", "not established", coord{0, 0, 0, 0})
	deps, _ := json.Marshal([]int64{unknown})
	if _, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding, depends_on_ids)
		VALUES (1,'test',0,0,0,1,'guarantee','rests on the unknown',?)`, string(deps)); err != nil {
		t.Fatal(err)
	}
	const laundered = 1

	_, signals, plan := scanAndPlan(t, s)
	qmp := signalsOf(signals, "qmp")
	if len(qmp) != 1 || qmp[0].Payload["laundering_violations"] != laundered {
		t.Fatalf("qmp signals %+v, want one naming %d laundering violation", qmp, laundered)
	}
	if len(plan) == 0 || plan[0].Type != "fix_mss" || plan[0].Priority != "critical" {
		t.Fatalf("plan %+v, want fix_mss first and critical", plan)
	}
	for _, a := range plan {
		if a.Type == "run_gate" || a.Type == "adjust_params" {
			t.Errorf("plan holds %s under a qmp halt: %+v", a.Type, a)
		}
	}
}

// alarm: a conflict with a named winner. The plan reverts what rests on the
// loser and does not send the conflict to a verifier again.
func TestSignal_Alarm_CascadesTheLoser(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	winner := addFindingAt(t, s, "definition", "4096 bytes", coord{0, 0, 0, 0})
	loser := addFindingAt(t, s, "definition", "1024 bytes", coord{0, 1, 0, 0})
	conflict := addConflictBetween(t, s, winner, loser)
	if err := s.Conflicts().SetWinner(conflict, winner); err != nil {
		t.Fatal(err)
	}

	_, signals, plan := scanAndPlan(t, s)
	alarms := signalsOf(signals, "alarm")
	if len(alarms) != 1 || alarms[0].Payload["loser_finding_id"] != loser {
		t.Fatalf("alarms %+v, want one naming loser %d", alarms, loser)
	}
	cascades := actionsOf(plan, "alarm")
	if len(cascades) != 1 || cascades[0].Type != "cascade_revert" || cascades[0].FindingID != loser ||
		cascades[0].Payload["conflict_id"] != conflict {
		t.Fatalf("alarm actions %+v, want one cascade_revert of %d carrying conflict %d", cascades, loser, conflict)
	}
	if v := actionsOf(plan, "conflict_resolution"); len(v) != 0 {
		t.Fatalf("an adjudicated conflict was sent to a verifier: %+v", v)
	}
}

// stop_signal: while the conflict rate is over its threshold, each contested
// coordinate gets one stop signal and no scout is dispatched there; the
// convergence threshold is left alone. Under the threshold nothing is
// stopped.
func TestSignal_StopSignal_SuppressesContestedCoordinates(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	pairs := [][2]coord{
		{{2, 0, 0, 0}, {2, 1, 0, 0}},
		{{2, 1, 0, 0}, {2, 2, 0, 0}}, // shares (2,1,0,0) with the first
	}
	oldest := map[string]int64{} // contested coordinate → lowest conflict id
	var conflicts int
	for i, p := range pairs {
		a := addFindingAt(t, s, "assumption", fmt.Sprintf("claim %da", i), p[0])
		b := addFindingAt(t, s, "assumption", fmt.Sprintf("claim %db", i), p[1])
		id := addConflictBetween(t, s, a, b)
		conflicts++
		for _, c := range p {
			if cur, ok := oldest[c.key()]; !ok || id < cur {
				oldest[c.key()] = id
			}
		}
	}
	for i := 0; i < 6; i++ {
		addFindingAt(t, s, "definition", fmt.Sprintf("filler %d", i), coord{5, i, 0, 0})
	}
	contestedGap, openGap := coord{2, 1, 0, 0}, coord{7, 0, 0, 0}
	addGapAt(t, s, "critical", "on contested ground", contestedGap)
	addGapAt(t, s, "critical", "on open ground", openGap)

	state, signals, plan := scanAndPlan(t, s)
	rate := float64(conflicts) / float64(state.TotalFindings)
	if rate <= state.Hive.ConflictRateThreshold {
		t.Fatalf("setup: conflict rate %.2f is not over the threshold %.2f", rate, state.Hive.ConflictRateThreshold)
	}
	stops := signalsOf(signals, "stop_signal")
	if len(stops) != len(oldest) {
		t.Fatalf("%d stop signals, want one per contested coordinate (%d): %+v", len(stops), len(oldest), stops)
	}
	for _, sig := range stops {
		k := coord{*sig.TargetD1, *sig.TargetD2, *sig.TargetD3, *sig.TargetD4}.key()
		want, ok := oldest[k]
		if !ok || sig.SourceType != "conflict" || sig.SourceID == nil || *sig.SourceID != want {
			t.Errorf("stop at %s sourced %s/%v, want conflict %d at a contested coordinate", k, sig.SourceType, sig.SourceID, want)
		}
	}
	fills := map[string]int{}
	for _, a := range actionsOf(plan, "gap_fill") {
		fills[targetKey(a.TargetCoords)]++
	}
	if fills[contestedGap.key()] != 0 || fills[openGap.key()] != 1 {
		t.Fatalf("gap fills %v, want the open gap and not the contested one", fills)
	}
	for _, a := range plan {
		if _, ok := a.Params["convergence_threshold"]; ok {
			t.Fatalf("a stop signal moved convergence_threshold: %+v", a)
		}
	}

	if _, err := s.WriteDB.Exec(`UPDATE hive_state SET conflict_rate_threshold = ? WHERE project='p'`, rate+0.1); err != nil {
		t.Fatal(err)
	}
	_, signals, plan = scanAndPlan(t, s)
	if stops := signalsOf(signals, "stop_signal"); len(stops) != 0 {
		t.Fatalf("under the threshold: stop signals %+v, want none", stops)
	}
	if n := len(actionsOf(plan, "gap_fill")); n != 2 {
		t.Fatalf("under the threshold: %d gap fills, want both gaps", n)
	}
}

// waggle_dance: findings that converged at a d1 recruit a scout to an open
// gap sharing that d1, and to no gap elsewhere.
func TestSignal_WaggleDance_RecruitsBesideARichPatch(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	patch := coord{3, 0, 0, 0}
	id := addFindingAt(t, s, "assumption", "converged", patch)
	if _, err := s.WriteDB.Exec(`UPDATE findings SET convergence_level='high' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	beside, elsewhere := coord{patch[0], 4, 0, 0}, coord{6, 0, 0, 0}
	addGapAt(t, s, "minor", "beside the patch", beside)
	addGapAt(t, s, "minor", "elsewhere", elsewhere)

	_, signals, plan := scanAndPlan(t, s)
	dances := signalsOf(signals, "waggle_dance")
	if len(dances) != 1 {
		t.Fatalf("waggle dances %+v, want one", dances)
	}
	recruits := actionsOf(plan, "waggle_dance")
	if len(recruits) != 1 || recruits[0].Agent != "hive-scout" || targetKey(recruits[0].TargetCoords) != beside.key() {
		t.Fatalf("recruits %+v, want one hive-scout at %s", recruits, beside.key())
	}
}

// waggle_dance passes over a capped cell: the dance still recruits beside
// the patch, to the next open gap in coordinate order that shares its d1,
// but never to a gap at the capped finding's own coordinate.
func TestSignal_WaggleDance_PassesOverACappedCell(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	patch := coord{3, 0, 0, 0}
	id := addFindingAt(t, s, "assumption", "converged", patch)
	if _, err := s.WriteDB.Exec(`UPDATE findings SET convergence_level='high' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	// In coordinate order the gap at the patch comes first, then the one
	// beside it.
	beside := coord{patch[0], 4, 0, 0}
	addGapAt(t, s, "minor", "at the patch", patch)
	addGapAt(t, s, "minor", "beside the patch", beside)

	for _, want := range []coord{patch, beside} {
		_, _, plan := scanAndPlan(t, s)
		recruits := actionsOf(plan, "waggle_dance")
		if len(recruits) != 1 || targetKey(recruits[0].TargetCoords) != want.key() {
			t.Fatalf("recruits %+v, want one at %s", recruits, want.key())
		}
		if want == patch {
			if _, err := ApplyCaps(s, []Action{{Type: "cap_finding", FindingID: id}}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// tremble_dance: more conflicts wait for a verifier than the batch
// dispatches. The plan dispatches a verifier for each, up to BatchSizeMax,
// and lowers batch_size. A conflict whose winner is named is not waiting.
func TestSignal_TrembleDance_RecruitsVerifiersAndDampsForaging(t *testing.T) {
	for _, waiting := range []int{6, 7, BatchSizeMax + 3} {
		t.Run(fmt.Sprint(waiting), func(t *testing.T) {
			s := newTestStore(t)
			initProject(t, s, "p")
			batch, _, _ := storedParams(t, s)
			for i := 0; i < waiting; i++ {
				a := addFindingAt(t, s, "assumption", "a", coord{10 + i, 0, 0, 0})
				b := addFindingAt(t, s, "assumption", "b", coord{10 + i, 1, 0, 0})
				addConflictBetween(t, s, a, b)
			}
			// Adjudicated conflicts wait on their cascade, not a verifier.
			for i := 0; i < 3; i++ {
				a := addFindingAt(t, s, "assumption", "a", coord{90 + i, 0, 0, 0})
				b := addFindingAt(t, s, "assumption", "b", coord{90 + i, 1, 0, 0})
				if err := s.Conflicts().SetWinner(addConflictBetween(t, s, a, b), a); err != nil {
					t.Fatal(err)
				}
			}

			_, signals, plan := scanAndPlan(t, s)
			trembles := signalsOf(signals, "tremble_dance")
			if fires := waiting > batch; fires != (len(trembles) == 1) {
				t.Fatalf("%d waiting, batch %d: tremble signals %+v", waiting, batch, trembles)
			}
			if len(trembles) == 0 {
				return
			}
			if trembles[0].Payload["awaiting_verifier"] != waiting {
				t.Errorf("awaiting_verifier %v, want %d", trembles[0].Payload["awaiting_verifier"], waiting)
			}
			wantVerifiers := waiting
			if wantVerifiers > BatchSizeMax {
				wantVerifiers = BatchSizeMax
			}
			if n := len(actionsOf(plan, "conflict_resolution")); n != wantVerifiers {
				t.Errorf("%d verifier dispatches, want %d", n, wantVerifiers)
			}
			want := clampBatch(batch - trembleBatchDecrement)
			if got := batchAdjustments(plan); len(got) != 1 || got[0] != want {
				t.Fatalf("batch_size adjustments %v, want [%d]", got, want)
			}
			if _, err := applyParamAdjustments(s.WriteDB, "p", plan); err != nil {
				t.Fatal(err)
			}
			if got, _, _ := storedParams(t, s); got != want {
				t.Errorf("stored batch_size %d, want %d", got, want)
			}
		})
	}
}

// A backlog no larger than the batch is not a tremble: every waiting
// conflict is dispatched without one.
func TestSignal_TrembleDance_QuietWhenTheBatchKeepsUp(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	batch, _, _ := storedParams(t, s)
	for i := 0; i < batch; i++ {
		a := addFindingAt(t, s, "assumption", "a", coord{10 + i, 0, 0, 0})
		b := addFindingAt(t, s, "assumption", "b", coord{10 + i, 1, 0, 0})
		addConflictBetween(t, s, a, b)
	}
	_, signals, plan := scanAndPlan(t, s)
	if n := len(signalsOf(signals, "tremble_dance")); n != 0 {
		t.Fatalf("%d tremble signals with %d waiting and batch %d, want none", n, batch, batch)
	}
	if n := len(actionsOf(plan, "conflict_resolution")); n != batch {
		t.Fatalf("%d verifier dispatches, want %d", n, batch)
	}
}

// shaking_signal: open gaps outnumber the batch. Each scan raises batch_size
// until the batch covers the queue, and then the signal stops.
func TestSignal_Shaking_RaisesBatchUntilItCoversTheQueue(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	batch, _, _ := storedParams(t, s)
	critical := batch + 4
	for i := 0; i < critical; i++ {
		addGapAt(t, s, "critical", "open", coord{20 + i, 0, 0, 0})
	}
	// Important gaps wait behind the critical ones and do not count yet.
	for i := 0; i < 30; i++ {
		addGapAt(t, s, "important", "later", coord{60 + i, 0, 0, 0})
	}

	for scan := 1; ; scan++ {
		if scan > 10 {
			t.Fatal("batch_size never covered the queue")
		}
		_, signals, plan := scanAndPlan(t, s)
		shakes := signalsOf(signals, "shaking_signal")
		if critical <= batch {
			if len(shakes) != 0 || len(batchAdjustments(plan)) != 0 {
				t.Fatalf("scan %d: batch %d covers %d gaps, yet shaking %+v", scan, batch, critical, shakes)
			}
			break
		}
		if len(shakes) != 1 || shakes[0].Payload["adjust"] != "raise_batch_size" {
			t.Fatalf("scan %d: shaking signals %+v, want one raise", scan, shakes)
		}
		if n := len(actionsOf(plan, "gap_fill")); n != batch {
			t.Fatalf("scan %d: %d gap fills, want the current batch %d", scan, n, batch)
		}
		want := clampBatch(batch + shakingBatchIncrement)
		if got := batchAdjustments(plan); len(got) != 1 || got[0] != want {
			t.Fatalf("scan %d: batch_size adjustments %v, want [%d]", scan, got, want)
		}
		if _, err := applyParamAdjustments(s.WriteDB, "p", plan); err != nil {
			t.Fatal(err)
		}
		batch, _, _ = storedParams(t, s)
		if batch != want {
			t.Fatalf("scan %d: stored batch_size %d, want %d", scan, batch, want)
		}
	}
}

// shaking_signal counts only gaps the colony can take up. A gap at a
// coordinate a stop signal covers, raised by the scan or written by an
// agent, is not idle work: shaking for it would widen the batch while every
// scout it adds is held back.
func TestSignal_Shaking_IgnoresStoppedGaps(t *testing.T) {
	for _, open := range []int{0, 5, 6} {
		t.Run(fmt.Sprint(open), func(t *testing.T) {
			s := newTestStore(t)
			initProject(t, s, "p")
			batch, _, _ := storedParams(t, s)
			// Three conflicts touch six coordinates; each gets a critical gap.
			contested := map[string]bool{}
			var gaps []coord
			for i := 0; i < 3; i++ {
				a, b := coord{50 + i, 0, 0, 0}, coord{50 + i, 1, 0, 0}
				addConflictBetween(t, s,
					addFindingAt(t, s, "assumption", "a", a),
					addFindingAt(t, s, "assumption", "b", b))
				contested[a.key()], contested[b.key()] = true, true
				gaps = append(gaps, a, b)
			}
			// A pending stop signal written outside the scan covers one more.
			agentStop := coord{70, 0, 0, 0}
			src := "system"
			if _, err := s.Signals().EmitSignal("stop_signal", &src, nil,
				&agentStop[0], &agentStop[1], &agentStop[2], &agentStop[3], nil, nil); err != nil {
				t.Fatal(err)
			}
			gaps = append(gaps, agentStop)
			for i := 0; i < open; i++ {
				gaps = append(gaps, coord{80 + i, 0, 0, 0})
			}
			for _, g := range gaps {
				addGapAt(t, s, "critical", "gap", g)
			}

			state, signals, plan := scanAndPlan(t, s)
			if float64(3)/float64(state.TotalFindings) <= state.Hive.ConflictRateThreshold {
				t.Fatal("setup: the conflict rate is not over its threshold")
			}
			if n := len(signalsOf(signals, "tremble_dance")); n != 0 {
				t.Fatalf("setup: %d tremble dances, want none", n)
			}
			dispatchable := 0
			for _, g := range gaps {
				if !contested[g.key()] && g != agentStop {
					dispatchable++
				}
			}
			var shakes []Signal
			for _, sig := range signalsOf(signals, "shaking_signal") {
				if sig.Payload["adjust"] == "raise_batch_size" {
					shakes = append(shakes, sig)
				}
			}
			if wantShake := dispatchable > batch; wantShake != (len(shakes) == 1) {
				t.Fatalf("%d of %d gaps dispatchable, batch %d: shaking signals %+v", dispatchable, len(gaps), batch, shakes)
			}
			wantFills := min(dispatchable, batch)
			if n := len(actionsOf(plan, "gap_fill")); n != wantFills {
				t.Fatalf("%d gap fills, want %d", n, wantFills)
			}
			var wantAdjust []any
			if dispatchable > batch {
				wantAdjust = []any{clampBatch(batch + shakingBatchIncrement)}
			}
			if got := batchAdjustments(plan); fmt.Sprint(got) != fmt.Sprint(wantAdjust) {
				t.Fatalf("batch_size adjustments %v, want %v", got, wantAdjust)
			}
		})
	}
}

// addFindingsInWave writes n findings of one label into a wave, each at its
// own coordinate.
func addFindingsInWave(t *testing.T, s *db.Store, wave int, label string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := s.WriteDB.Exec(
			`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding) VALUES (?,'test',?,?,0,0,?,'f')`,
			wave, 30+wave, i, label,
		); err != nil {
			t.Fatal(err)
		}
	}
}

// shaking_signal for a tier upgrade fires when unknowns make up more than
// half of the latest wave, and that wave holds five findings or more. Any
// other skew is honest research and rouses nothing; neither does an earlier
// wave. The expected signal is computed from each seed's latest wave.
func TestSignal_Shaking_UnknownDominatedWaveUpgradesTheTier(t *testing.T) {
	for _, tc := range []struct {
		name   string
		waves  map[int]map[string]int
		latest int
	}{
		{"five unknowns", map[int]map[string]int{1: {"unknown": 5}}, 1},
		{"five assumptions", map[int]map[string]int{1: {"assumption": 5}}, 1},
		{"five definitions", map[int]map[string]int{1: {"definition": 5}}, 1},
		{"three unknowns of five", map[int]map[string]int{1: {"unknown": 3, "assumption": 2}}, 1},
		{"half unknown", map[int]map[string]int{1: {"unknown": 3, "assumption": 3}}, 1},
		{"four unknowns", map[int]map[string]int{1: {"unknown": 4}}, 1},
		{"an earlier wave of unknowns", map[int]map[string]int{1: {"unknown": 10}, 2: {"assumption": 5}}, 2},
		{"a latest wave of unknowns", map[int]map[string]int{1: {"assumption": 10}, 2: {"unknown": 5}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			initProject(t, s, "p")
			for wave, labels := range tc.waves {
				for label, n := range labels {
					addFindingsInWave(t, s, wave, label, n)
				}
			}
			inWave, unknowns := 0, tc.waves[tc.latest]["unknown"]
			for _, n := range tc.waves[tc.latest] {
				inWave += n
			}
			want := inWave >= 5 && float64(unknowns)/float64(inWave) > 0.5

			_, signals, _ := scanAndPlan(t, s)
			var upgrades []Signal
			for _, sig := range signalsOf(signals, "shaking_signal") {
				if sig.Payload["adjust"] == "upgrade_model_tier" {
					upgrades = append(upgrades, sig)
				}
			}
			if (len(upgrades) == 1) != want || len(upgrades) > 1 {
				t.Fatalf("waves %v: tier-upgrade signals %+v, want fired=%v", tc.waves, upgrades, want)
			}
		})
	}
}

// The tier upgrade rouses the next model tier, and at the top tier there is
// nothing to rouse. No scan is recorded here, so the tier rule's clock does
// not run (tier_test.go covers it).
func TestSignal_Shaking_UnknownWaveWalksTheTiers(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	addFindingsInWave(t, s, 1, "unknown", 5)
	_, _, tier := storedParams(t, s)
	next := ""
	tiers := ModelTiers()
	for i, name := range tiers {
		if name == tier && i+1 < len(tiers) {
			next = tiers[i+1]
		}
	}
	for _, want := range []string{next, ""} {
		_, signals, plan := scanAndPlan(t, s)
		if len(signalsOf(signals, "shaking_signal")) != 1 {
			t.Fatalf("tier %s: shaking signals %+v, want one for the unknown wave", tier, signals)
		}
		var got []any
		for _, a := range actionsOf(plan, "shaking_signal") {
			got = append(got, a.Params["model_tier"])
		}
		if want == "" {
			if len(got) != 0 {
				t.Fatalf("top tier %s: tier changes %v, want none", tier, got)
			}
			break
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("tier %s: tier changes %v, want [%s]", tier, got, want)
		}
		if _, err := applyParamAdjustments(s.WriteDB, "p", plan); err != nil {
			t.Fatal(err)
		}
		_, _, tier = storedParams(t, s)
	}
}

// A tremble dance outranks a shaking signal: with conflicts waiting and gaps
// piled up, the plan lowers batch_size once and never raises it.
func TestSignal_TrembleOutranksShaking(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	batch, _, _ := storedParams(t, s)
	for i := 0; i < batch+2; i++ {
		a := addFindingAt(t, s, "assumption", "a", coord{10 + i, 0, 0, 0})
		b := addFindingAt(t, s, "assumption", "b", coord{10 + i, 1, 0, 0})
		addConflictBetween(t, s, a, b)
	}
	for i := 0; i < batch+4; i++ {
		addGapAt(t, s, "critical", "open", coord{40 + i, 0, 0, 0})
	}

	_, signals, plan := scanAndPlan(t, s)
	if len(signalsOf(signals, "tremble_dance")) != 1 || len(signalsOf(signals, "shaking_signal")) == 0 {
		t.Fatalf("setup: signals %+v, want a tremble dance and a shaking signal", signals)
	}
	want := clampBatch(batch - trembleBatchDecrement)
	if got := batchAdjustments(plan); len(got) != 1 || got[0] != want {
		t.Fatalf("batch_size adjustments %v, want only the tremble's %d", got, want)
	}
	if _, err := applyParamAdjustments(s.WriteDB, "p", plan); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := storedParams(t, s); got != want {
		t.Fatalf("stored batch_size %d, want %d", got, want)
	}
}

// quorum: an assumption whose convergence count reached the threshold is
// capped. It stays an assumption with no dependencies, because agreement
// among agents is not a derivation, and the next scan raises no quorum for
// it. One short of the threshold raises nothing.
func TestSignal_Quorum_CapsAtTheThreshold(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	_, threshold, _ := storedParams(t, s)
	site := coord{4, 0, 0, 0}
	candidate := addFindingAt(t, s, "assumption", "the site is good", site)
	for i := 0; i < 2; i++ {
		addFindingAt(t, s, "assumption", "the site is good, again", site)
	}

	for _, count := range []int{threshold - 1, threshold} {
		if _, err := s.WriteDB.Exec(`UPDATE findings SET convergence_level='high', convergence_count=? WHERE id=?`, count, candidate); err != nil {
			t.Fatal(err)
		}
		_, signals, plan := scanAndPlan(t, s)
		quorum := signalsOf(signals, "quorum")
		caps := actionsOf(plan, "quorum")
		if count < threshold {
			if len(quorum) != 0 || len(caps) != 0 {
				t.Fatalf("count %d below threshold %d: quorum %+v, actions %+v", count, threshold, quorum, caps)
			}
			continue
		}
		if len(caps) != 1 || caps[0].Type != "cap_finding" || caps[0].FindingID != candidate {
			t.Fatalf("count %d: actions %+v, want a cap of %d", count, caps, candidate)
		}
		results, err := ApplyCaps(s, plan)
		if err != nil || len(results) != 1 || !results[0].Capped || results[0].FindingID != candidate {
			t.Fatalf("ApplyCaps = %+v, %v", results, err)
		}
		var label string
		var deps sql.NullString
		if err := s.ReadDB.QueryRow(`SELECT mss_label, depends_on_ids FROM findings WHERE id=?`, candidate).Scan(&label, &deps); err != nil {
			t.Fatal(err)
		}
		if label != "assumption" || deps.Valid {
			t.Fatalf("capped finding is a %s resting on %v, want an assumption resting on nothing", label, deps)
		}
		if _, signals, plan := scanAndPlan(t, s); len(signalsOf(signals, "quorum")) != 0 || len(actionsOf(plan, "quorum")) != 0 {
			t.Fatalf("after the cap: quorum %+v, actions %+v, want none", signalsOf(signals, "quorum"), actionsOf(plan, "quorum"))
		}
		results, err = ApplyCaps(s, caps)
		if err != nil || len(results) != 1 || results[0].Capped {
			t.Fatalf("capping it again = %+v, %v; want it reported and not capped", results, err)
		}
	}
}
