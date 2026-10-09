package hive

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// The hive loop, offline: scan, plan, apply (as `chb hive next --apply`
// does), and carry out every action the way the `hive` workflow's dispatch
// node is told to, with a fake scout, verifier and gate in place of agents.
// The seeded workspace raises every evaluated signal but qmp. The loop must
// still reach terminal, and each plan must follow the gain-control rules.
func TestColonyLoop_SeededWorkspaceReachesTerminal(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")

	// A rich patch: a converged assumption with two sibling definitions, and a
	// minor gap beside it for the waggle dance.
	patch := coord{1, 0, 0, 0}
	candidate := addFindingAt(t, s, "assumption", "the patch is rich", patch)
	addFindingAt(t, s, "definition", "measured once", patch)
	addFindingAt(t, s, "definition", "measured twice", patch)
	if _, err := s.WriteDB.Exec(`UPDATE findings SET convergence_level='high', convergence_count=3 WHERE id=?`, candidate); err != nil {
		t.Fatal(err)
	}
	addGapAt(t, s, "minor", "beside the patch", coord{1, 5, 0, 0})

	// Six conflicts, one more than the default batch: a tremble dance, and a
	// conflict rate high enough for stop signals at their coordinates.
	for i := 0; i < 6; i++ {
		a := addFindingAt(t, s, "assumption", fmt.Sprintf("claim %d", i), coord{2, i, 0, 0})
		b := addFindingAt(t, s, "assumption", fmt.Sprintf("counter-claim %d", i), coord{2, i, 1, 0})
		addConflictBetween(t, s, a, b)
	}
	// A critical gap on contested ground, and more important gaps than a
	// damped batch dispatches.
	contested := coord{2, 0, 0, 0}
	addGapAt(t, s, "critical", "on contested ground", contested)
	for i := 0; i < 7; i++ {
		addGapAt(t, s, "important", fmt.Sprintf("question %d", i), coord{3, i, 0, 0})
	}

	seen := map[string]bool{}
	const maxIterations = 15
	terminal := false
	for iteration := 1; iteration <= maxIterations; iteration++ {
		state, err := ScanState(s, "p")
		if err != nil {
			t.Fatal(err)
		}
		if done, _ := CheckTermination(state); done {
			terminal = true
			break
		}
		signals := EvaluateSignals(s, state)
		plan := mustPlan(t, state, signals)
		fired := map[string]int{}
		for _, sig := range signals {
			seen[sig.SignalType] = true
			fired[sig.SignalType]++
		}
		planned := map[string]int{}
		for _, a := range plan {
			planned[a.SignalType]++
		}
		t.Logf("iteration %d: batch_size %d, signals %v, actions %v", iteration, state.Hive.BatchSize, fired, planned)

		// Gain control: one batch_size change at most, lowered when a tremble
		// dance fires, and nothing ever touches convergence_threshold.
		trembling := len(signalsOf(signals, "tremble_dance")) > 0
		adjustments := batchAdjustments(plan)
		if len(adjustments) > 1 {
			t.Fatalf("iteration %d: batch_size adjusted %d times: %v", iteration, len(adjustments), adjustments)
		}
		for _, v := range adjustments {
			if n, _ := toInt(v); trembling && n >= state.Hive.BatchSize {
				t.Fatalf("iteration %d: batch_size %d → %d under a tremble dance", iteration, state.Hive.BatchSize, n)
			}
		}
		for _, a := range plan {
			if _, ok := a.Params["convergence_threshold"]; ok {
				t.Fatalf("iteration %d: convergence_threshold adjusted: %+v", iteration, a)
			}
		}
		// A stop signal at the contested gap's coordinate keeps scouts away.
		stopped := false
		for _, sig := range signalsOf(signals, "stop_signal") {
			if coordOf(sig.TargetD1, sig.TargetD2, sig.TargetD3, sig.TargetD4) == coordOf(contested[0], contested[1], contested[2], contested[3]) {
				stopped = true
			}
		}
		for _, a := range actionsOf(plan, "gap_fill") {
			if stopped && targetKey(a.TargetCoords) == contested.key() {
				t.Fatalf("iteration %d: a scout was sent to the stopped coordinate %s", iteration, contested.key())
			}
		}

		if _, err := ApplyCaps(s, plan); err != nil {
			t.Fatal(err)
		}
		if _, err := applyParamAdjustments(s.WriteDB, "p", plan); err != nil {
			t.Fatal(err)
		}
		for _, a := range plan {
			carryOut(t, s, a)
		}
	}
	if !terminal {
		state, _ := ScanState(s, "p")
		_, reason := CheckTermination(state)
		t.Fatalf("the hive did not reach terminal in %d iterations: %s", maxIterations, reason)
	}
	for _, kind := range []string{"alarm", "stop_signal", "waggle_dance", "tremble_dance", "shaking_signal", "quorum"} {
		if !seen[kind] {
			t.Errorf("the seeded run never raised %s", kind)
		}
	}
	batch, _, _ := storedParams(t, s)
	if batch < BatchSizeMin || batch > BatchSizeMax {
		t.Errorf("batch_size %d left the %d–%d band", batch, BatchSizeMin, BatchSizeMax)
	}
	var label string
	var capped int
	if err := s.ReadDB.QueryRow(
		`SELECT mss_label, (SELECT COUNT(*) FROM capped_findings WHERE finding_id = f.id) FROM findings f WHERE id=?`, candidate,
	).Scan(&label, &capped); err != nil || label != "assumption" || capped != 1 {
		t.Errorf("the converged candidate is a capped(%d) %q (%v), want a capped assumption", capped, label, err)
	}
}

// Quorum caps a converged finding once, over many iterations of the offline
// hive loop. The finding stays an assumption, no later plan names it again
// although its convergence never changes, and the hive still reaches
// terminal through the gap loop and the gate. The capped cell is sealed from
// recruitment: once it is capped, no waggle dance sends a scout to its
// coordinate. An important gap there is still dispatched by gap fill, once,
// and closed, because termination waits for it.
func TestColonyLoop_QuorumCapsOnceAndTheHiveReachesTerminal(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	_, threshold, _ := storedParams(t, s)

	site := coord{4, 0, 0, 0}
	candidate := addFindingAt(t, s, "assumption", "the site is good", site)
	addFindingAt(t, s, "assumption", "the site is good, again", site)
	if _, err := s.WriteDB.Exec(`UPDATE findings SET convergence_level='high', convergence_count=? WHERE id=?`, threshold, candidate); err != nil {
		t.Fatal(err)
	}
	// A critical gap elsewhere, so the loop runs several iterations, and an
	// important gap at the capped cell behind it.
	addGapAt(t, s, "critical", "first question", coord{8, 0, 0, 0})
	siteGap := addGapAt(t, s, "important", "a question at the site", site)

	const maxIterations = 10
	var namedIn []int // the iterations whose plan names the candidate
	quorumFor := 0
	capped := false       // whether an earlier iteration capped the candidate
	var siteFills []int64 // gap ids of the gap fills sent to the site after the cap
	terminal := false
	iterations := 0
	for iteration := 1; iteration <= maxIterations; iteration++ {
		state, err := ScanState(s, "p")
		if err != nil {
			t.Fatal(err)
		}
		if done, _ := CheckTermination(state); done {
			terminal = true
			break
		}
		iterations = iteration
		signals := EvaluateSignals(s, state)
		for _, sig := range signalsOf(signals, "quorum") {
			if sig.SourceID != nil && *sig.SourceID == candidate {
				quorumFor++
			}
		}
		plan := mustPlan(t, state, signals)
		for _, a := range plan {
			if a.FindingID == candidate {
				namedIn = append(namedIn, iteration)
				if a.Type != "cap_finding" {
					t.Fatalf("iteration %d: the plan acts on the candidate with %s, want only a cap", iteration, a.Type)
				}
			}
			if !capped || a.Type != "dispatch_agent" || targetKey(a.TargetCoords) != site.key() {
				continue
			}
			if a.SignalType != "gap_fill" {
				t.Fatalf("iteration %d: a %s dispatch went to the capped cell %s", iteration, a.SignalType, site.key())
			}
			gapID, _ := a.Payload["gap_id"].(int64)
			siteFills = append(siteFills, gapID)
		}
		results, err := ApplyCaps(s, plan)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range results {
			if r.FindingID == candidate && r.Capped {
				capped = true
			}
		}
		if _, err := applyParamAdjustments(s.WriteDB, "p", plan); err != nil {
			t.Fatal(err)
		}
		for _, a := range plan {
			carryOut(t, s, a)
		}
	}
	if !terminal {
		state, _ := ScanState(s, "p")
		_, reason := CheckTermination(state)
		t.Fatalf("the hive did not reach terminal in %d iterations: %s", maxIterations, reason)
	}
	if iterations < 2 {
		t.Fatalf("the hive ran %d iteration(s); the test needs the candidate to survive scans after its cap", iterations)
	}
	if quorumFor != 1 || len(namedIn) != 1 || namedIn[0] != 1 {
		t.Fatalf("quorum fired %d time(s) for the candidate, and plans named it in iterations %v; want once, in iteration 1", quorumFor, namedIn)
	}
	var label string
	var deps sql.NullString
	var caps, guarantees int
	if err := s.ReadDB.QueryRow(`SELECT mss_label, depends_on_ids FROM findings WHERE id=?`, candidate).Scan(&label, &deps); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM capped_findings WHERE finding_id=?`, candidate).Scan(&caps); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM findings WHERE mss_label='guarantee'`).Scan(&guarantees); err != nil {
		t.Fatal(err)
	}
	if label != "assumption" || deps.Valid || caps != 1 || guarantees != 0 {
		t.Fatalf("candidate: label %s, depends_on_ids %v, cap rows %d; guarantees in the store %d — want an assumption with no dependencies, one cap row, no guarantee",
			label, deps, caps, guarantees)
	}
	if len(siteFills) != 1 || siteFills[0] != siteGap {
		t.Fatalf("after the cap, gap fills sent to the capped cell named gaps %v; want one, naming gap %d", siteFills, siteGap)
	}
	var resolved sql.NullInt64
	if err := s.ReadDB.QueryRow(`SELECT resolved_by_wave FROM gaps WHERE id=?`, siteGap).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if !resolved.Valid {
		t.Fatalf("the gap at the capped cell is still open")
	}
}

// carryOut does what the hive workflow's dispatch node does for one action,
// with each agent's judgement replaced by a fixed answer.
func carryOut(t *testing.T, s *db.Store, a Action) {
	t.Helper()
	switch {
	case a.Type == "dispatch_agent" && (a.SignalType == "gap_fill" || a.SignalType == "waggle_dance"):
		tc := a.TargetCoords
		res, err := s.WriteDB.Exec(
			`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding) VALUES (?,'hive-scout',?,?,?,?,'definition','answered')`,
			a.Wave, coordArg(tc["d1"]), coordArg(tc["d2"]), coordArg(tc["d3"]), coordArg(tc["d4"]),
		)
		if err != nil {
			t.Fatal(err)
		}
		finding, _ := res.LastInsertId()
		if gapID, ok := a.Payload["gap_id"].(int64); ok {
			if err := s.Gaps().ResolveGap(gapID, a.Wave, "hive-scout", finding); err != nil {
				t.Fatal(err)
			}
		}
	case a.Type == "dispatch_agent" && a.SignalType == "conflict_resolution":
		id, _ := a.Payload["conflict_id"].(int64)
		var winner int64
		if err := s.ReadDB.QueryRow(`SELECT finding_a_id FROM conflicts WHERE id=?`, id).Scan(&winner); err != nil {
			t.Fatal(err)
		}
		if err := s.Conflicts().SetWinner(id, winner); err != nil {
			t.Fatal(err)
		}
	case a.Type == "cascade_revert":
		if _, err := s.CascadeRevert(a.FindingID); err != nil {
			t.Fatal(err)
		}
		id, _ := a.Payload["conflict_id"].(int64)
		if err := s.Conflicts().Resolve(id, a.Wave, fmt.Sprintf("loser %d reverted", a.FindingID)); err != nil {
			t.Fatal(err)
		}
	case a.Type == "run_gate":
		if _, err := s.WriteDB.Exec(`INSERT OR REPLACE INTO wave_gates (wave, mss_audit_passed, source_check_passed, conflict_check_passed, agents_completed) VALUES (?,1,1,1,1)`, a.Wave); err != nil {
			t.Fatal(err)
		}
		if err := s.Evaluations().AddEvaluation(a.Wave, 4, 4, 4, 4, 4, "COMPLETE", 0, 0, 0, nil); err != nil {
			t.Fatal(err)
		}
	case a.Type == "adjust_params" || a.Type == "cap_finding":
		// Applied by the scan, as `chb hive next --apply` does.
	default:
		t.Fatalf("the seeded run planned an action the fake dispatcher does not expect: %+v", a)
	}
}

// coordArg turns a target coordinate, *int from a signal or int64 from a gap
// row, into an SQL argument.
func coordArg(v any) any {
	if p, ok := v.(*int); ok {
		if p == nil {
			return nil
		}
		return *p
	}
	return v
}
