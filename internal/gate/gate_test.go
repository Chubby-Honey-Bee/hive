package gate

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func newGateStore(t *testing.T) *db.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "gate.db")
	store, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRunGatePipeline_NoAgentsNoFindings_Errors(t *testing.T) {
	store := newGateStore(t)
	result, err := RunGatePipeline(store, 1, nil, false, false, false)
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Errorf("expected error for empty wave, got %+v", result)
	}
}

func TestRunGatePipeline_RunningAgentsBlock(t *testing.T) {
	store := newGateStore(t)

	if _, err := store.WriteDB.Exec(
		"INSERT INTO agent_runs(wave, agent_name, agent_type, status) VALUES (1, 'a', 'researcher', 'running')",
	); err != nil {
		t.Fatal(err)
	}

	result, err := RunGatePipeline(store, 1, nil, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "still running") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'still running' error, got %+v", result.Errors)
	}
}

func TestRunGatePipeline_HappyPath_NoErrors(t *testing.T) {
	store := newGateStore(t)

	// Add a completed agent
	if _, err := store.WriteDB.Exec(
		"INSERT INTO agent_runs(wave, agent_name, agent_type, status) VALUES (1, 'a', 'researcher', 'completed')",
	); err != nil {
		t.Fatal(err)
	}
	// Add a finding
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "x",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := RunGatePipeline(store, 1, nil, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %+v", result.Errors)
	}
}

func TestRunGatePipeline_AutoResolveNumeric(t *testing.T) {
	store := newGateStore(t)

	a, _ := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "A"})
	b, _ := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "b", MSSLabel: "definition", Finding: "B"})
	if _, err := store.WriteDB.Exec(
		"INSERT INTO conflicts(wave, finding_a_id, finding_b_id, description) VALUES (1, ?, ?, '[numeric] divergence at $50 vs $200')",
		a, b,
	); err != nil {
		t.Fatal(err)
	}

	result, err := RunGatePipeline(store, 1, nil, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	// After auto-resolve, there should be no unresolved warning.
	for _, w := range result.Warnings {
		if strings.Contains(w, "unresolved conflicts") {
			t.Errorf("auto-resolve did not clear conflict, got warning %q", w)
		}
	}
}

func TestPipeRecordEvaluation_WithEvalJSON_Complete(t *testing.T) {
	store := newGateStore(t)
	evalJSON := map[string]any{
		"coverage": 4, "depth": 4, "sources": 4,
		"actionability": 4, "mss_integrity": 3, "verdict": "COMPLETE",
	}
	sr := pipeRecordEvaluation(store.ReadDB, store.WriteDB, 1, evalJSON)
	if len(sr.errors) != 0 {
		t.Errorf("expected no errors for COMPLETE verdict, got %v", sr.errors)
	}
}

func TestPipeRecordEvaluation_WithEvalJSON_NeedsMoreWork(t *testing.T) {
	store := newGateStore(t)
	evalJSON := map[string]any{
		"coverage": 2, "depth": 2, "sources": 2,
		"actionability": 2, "mss_integrity": 2, "verdict": "NEEDS_MORE_WORK",
	}
	sr := pipeRecordEvaluation(store.ReadDB, store.WriteDB, 1, evalJSON)
	if len(sr.errors) == 0 {
		t.Errorf("expected error for NEEDS_MORE_WORK verdict")
	}
	found := false
	for _, e := range sr.errors {
		if strings.Contains(e, "NEEDS_MORE_WORK") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected NEEDS_MORE_WORK in errors, got %v", sr.errors)
	}
}

func TestPipeRecordEvaluation_NilJSON_NoEvaluation_Warns(t *testing.T) {
	store := newGateStore(t)
	sr := pipeRecordEvaluation(store.ReadDB, store.WriteDB, 99, nil)
	if len(sr.warnings) == 0 {
		t.Errorf("expected warning when no evaluation recorded, got %+v", sr)
	}
	found := false
	for _, w := range sr.warnings {
		if strings.Contains(w, "No evaluation recorded") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'No evaluation recorded' warning, got %v", sr.warnings)
	}
}

func TestPipeRecordEvaluation_NilJSON_ExistingComplete_NoError(t *testing.T) {
	store := newGateStore(t)
	// Seed an evaluation with COMPLETE verdict
	if _, err := store.WriteDB.Exec(
		`INSERT INTO evaluations (wave, coverage_score, depth_score, source_score, actionability_score, mss_integrity_score, verdict)
		 VALUES (1, 4, 4, 4, 4, 3, 'COMPLETE')`,
	); err != nil {
		t.Fatal(err)
	}
	sr := pipeRecordEvaluation(store.ReadDB, store.WriteDB, 1, nil)
	if len(sr.errors) != 0 {
		t.Errorf("expected no errors for COMPLETE stored evaluation, got %v", sr.errors)
	}
	if len(sr.warnings) != 0 {
		t.Errorf("expected no warnings for COMPLETE stored evaluation, got %v", sr.warnings)
	}
}

func TestPipeRecordEvaluation_NilJSON_ExistingNeedsMoreWork_Errors(t *testing.T) {
	store := newGateStore(t)
	if _, err := store.WriteDB.Exec(
		`INSERT INTO evaluations (wave, coverage_score, depth_score, source_score, actionability_score, mss_integrity_score, verdict)
		 VALUES (1, 1, 1, 1, 1, 1, 'NEEDS_MORE_WORK')`,
	); err != nil {
		t.Fatal(err)
	}
	sr := pipeRecordEvaluation(store.ReadDB, store.WriteDB, 1, nil)
	if len(sr.errors) == 0 {
		t.Errorf("expected error for stored NEEDS_MORE_WORK verdict")
	}
	found := false
	for _, e := range sr.errors {
		if strings.Contains(e, "NEEDS_MORE_WORK") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected NEEDS_MORE_WORK in errors, got %v", sr.errors)
	}
}

// An evaluation the gate cannot record, a score outside 1–5 or a verdict the
// table's CHECK refuses, keeps the gate shut. A verdict is read
// case-insensitively, with spaces and hyphens as underscores.
func TestPipeRecordEvaluation_RecordsItOrBlocks(t *testing.T) {
	scores := func(v any, verdict any) map[string]any {
		return map[string]any{
			"coverage": v, "depth": 4.0, "sources": 4.0,
			"actionability": 4.0, "mss_integrity": 3.0, "verdict": verdict,
		}
	}
	for _, tc := range []struct {
		name        string
		eval        map[string]any
		wantVerdict string // "" means the evaluation must be refused
	}{
		{"canonical", scores(4.0, "COMPLETE"), "COMPLETE"},
		{"lower case", scores(4.0, "needs_more_work"), "NEEDS_MORE_WORK"},
		{"spaces", scores(4.0, "NEEDS MORE WORK"), "NEEDS_MORE_WORK"},
		{"minor follow-up", scores(4.0, "needs-minor-followup"), "NEEDS_MINOR_FOLLOWUP"},
		{"unknown verdict", scores(4.0, "MOSTLY DONE"), ""},
		{"no verdict", scores(4.0, nil), ""},
		{"score above 5", scores(9.0, "COMPLETE"), ""},
		{"score below 1", scores(0.0, "COMPLETE"), ""},
		{"fractional score", scores(3.5, "COMPLETE"), ""},
		{"score as text", scores("4", "COMPLETE"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newGateStore(t)
			sr := pipeRecordEvaluation(store.ReadDB, store.WriteDB, 1, tc.eval)
			var n int
			var stored string
			if err := store.ReadDB.QueryRow(`SELECT COUNT(*), COALESCE(MAX(verdict), '') FROM evaluations WHERE wave=1`).Scan(&n, &stored); err != nil {
				t.Fatal(err)
			}
			if tc.wantVerdict == "" {
				if n != 0 || len(sr.errors) == 0 {
					t.Errorf("%v: %d rows recorded, errors %v; want none recorded and an error", tc.eval, n, sr.errors)
				}
				return
			}
			if n != 1 || stored != tc.wantVerdict {
				t.Fatalf("%v: %d rows, verdict %q; want 1 row with %q", tc.eval, n, stored, tc.wantVerdict)
			}
			blocks := tc.wantVerdict == "NEEDS_MORE_WORK"
			if blocks != (len(sr.errors) > 0) {
				t.Errorf("%v: errors %v; want blocking=%v", tc.eval, sr.errors, blocks)
			}
		})
	}
}

// A refused verdict is named in the error as the JSON value it was given,
// never as Go's %q of the raw value.
func TestPipeRecordEvaluation_RefusalNamesTheVerdict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict any
		omit    bool
		want    string
	}{
		{"omitted", nil, true, "verdict null "},
		{"null", nil, false, "verdict null "},
		{"unknown text", "MOSTLY DONE", false, `verdict "MOSTLY DONE" `},
		{"number", 3.0, false, "verdict 3 "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eval := map[string]any{
				"coverage": 4.0, "depth": 4.0, "sources": 4.0,
				"actionability": 4.0, "mss_integrity": 3.0, "verdict": tc.verdict,
			}
			if tc.omit {
				delete(eval, "verdict")
			}
			sr := pipeRecordEvaluation(nil, nil, 1, eval)
			if len(sr.errors) != 1 {
				t.Fatalf("errors %v; want one refusal", sr.errors)
			}
			msg := sr.errors[0]
			if strings.Contains(msg, "%!") || !strings.Contains(msg, tc.want) {
				t.Errorf("refusal %q; want it to contain %q and no formatting error", msg, tc.want)
			}
		})
	}
}

// The gate runs conflict detection over its wave itself, so a wave nobody
// ran `chb detect-conflicts` on does not open with a real conflict in it.
func TestRunGatePipeline_DetectsTheWavesConflicts(t *testing.T) {
	store := newGateStore(t)
	d := func(v int) *int { return &v }
	for _, text := range []string{"manufacturing cost is $50 per unit", "manufacturing cost is $200 per unit"} {
		if _, err := store.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: "a", D1: d(0), MSSLabel: "definition", Finding: text,
		}); err != nil {
			t.Fatal(err)
		}
	}
	eval := map[string]any{"coverage": 4.0, "depth": 4.0, "sources": 4.0, "actionability": 4.0, "mss_integrity": 4.0, "verdict": "COMPLETE"}
	res, err := RunGatePipeline(store, 1, eval, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Opened {
		t.Fatalf("the gate opened over an undetected conflict: %+v", res)
	}
	var n int
	if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM conflicts WHERE wave=1 AND resolution IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("the gate recorded no conflict for the wave")
	}
	want := fmt.Sprintf("%d unresolved conflicts", n)
	found := false
	for _, w := range res.Warnings {
		found = found || w == want
	}
	if !found {
		t.Errorf("warnings %v; want %q", res.Warnings, want)
	}
}

// Opening the gate records the wave's Time Wheel tick, whichever command
// opened it.
func TestRunGatePipeline_OpeningRecordsTheWaveTick(t *testing.T) {
	store := newGateStore(t)
	insertFindingSQL(t, store, 2, "a", "definition", "x", "")
	eval := map[string]any{"coverage": 4.0, "depth": 4.0, "sources": 4.0, "actionability": 4.0, "mss_integrity": 4.0, "verdict": "COMPLETE"}
	res, err := RunGatePipeline(store, 2, eval, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Opened {
		t.Fatalf("the gate stayed shut on a clean wave: %+v", res)
	}
	ticks, err := store.TimeWheel().Recent(db.TickWave, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ticks) != 1 {
		t.Fatalf("%d wave ticks after the gate opened, want 1", len(ticks))
	}
	if tick := ticks[0]; tick.Label != "wave-2" || !tick.Wave.Valid || tick.Wave.Int64 != 2 || !tick.EndedAt.Valid {
		t.Errorf("tick = %+v; want a closed tick labelled wave-2 for wave 2", tick)
	}
}

// insertConflict seeds a conflict row for pipeResolveConflicts tests.
func insertConflict(t *testing.T, store *db.Store, wave int, desc string) {
	t.Helper()
	a, _ := store.Findings().AddFinding(&db.Finding{Wave: wave, Agent: "a", MSSLabel: "definition", Finding: "A"})
	b, _ := store.Findings().AddFinding(&db.Finding{Wave: wave, Agent: "b", MSSLabel: "definition", Finding: "B"})
	if _, err := store.WriteDB.Exec(
		"INSERT INTO conflicts(wave, finding_a_id, finding_b_id, description) VALUES (?, ?, ?, ?)",
		wave, a, b, desc,
	); err != nil {
		t.Fatalf("insertConflict: %v", err)
	}
}

func TestPipeResolveConflicts_NoConflicts_NoWarning(t *testing.T) {
	store := newGateStore(t)
	sr := pipeResolveConflicts(store, 1, false)
	if len(sr.warnings) != 0 {
		t.Errorf("expected no warnings with zero conflicts, got %v", sr.warnings)
	}
}

func TestPipeResolveConflicts_UnresolvedNoAutoResolve_Warning(t *testing.T) {
	store := newGateStore(t)
	insertConflict(t, store, 1, "plain conflict description")
	sr := pipeResolveConflicts(store, 1, false)
	if len(sr.warnings) == 0 {
		t.Errorf("expected unresolved-conflicts warning, got none")
	}
	found := false
	for _, w := range sr.warnings {
		if strings.Contains(w, "unresolved conflicts") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'unresolved conflicts' in warnings, got %v", sr.warnings)
	}
}

func TestPipeResolveConflicts_AutoResolve_NumericDivergenceSubstring(t *testing.T) {
	store := newGateStore(t)
	insertConflict(t, store, 1, "Numeric divergence at $50 vs $200")
	sr := pipeResolveConflicts(store, 1, true)
	for _, w := range sr.warnings {
		if strings.Contains(w, "unresolved conflicts") {
			t.Errorf("auto-resolve via 'Numeric divergence' should have cleared conflict, got warning: %v", sr.warnings)
		}
	}
}

func TestPipeResolveConflicts_AutoResolve_NonMatchingDesc_RemainsUnresolved(t *testing.T) {
	store := newGateStore(t)
	insertConflict(t, store, 1, "factual disagreement about category")
	sr := pipeResolveConflicts(store, 1, true)
	found := false
	for _, w := range sr.warnings {
		if strings.Contains(w, "unresolved conflicts") {
			found = true
		}
	}
	if !found {
		t.Errorf("non-matching conflict should remain unresolved, want warning, got %v", sr.warnings)
	}
}

// insertFindingSQL inserts a finding row directly via SQL, bypassing write-time
// MSS enforcement so we can seed laundering scenarios for audit tests.
func insertFindingSQL(t *testing.T, store *db.Store, wave int, agent, label, finding, dependsOnIDs string) int64 {
	t.Helper()
	var depsArg interface{}
	if dependsOnIDs != "" {
		depsArg = dependsOnIDs
	}
	res, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding, depends_on_ids) VALUES (?, ?, ?, ?, ?)`,
		wave, agent, label, finding, depsArg,
	)
	if err != nil {
		t.Fatalf("insertFindingSQL: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestPipeAuditMSS_HappyPath_NoErrorsOrWarnings(t *testing.T) {
	store := newGateStore(t)
	// Insert a small number of mixed-label findings (no laundering, no skew).
	insertFindingSQL(t, store, 1, "a", "definition", "def finding", "")
	insertFindingSQL(t, store, 1, "a", "assumption", "assumption finding", "")

	sr := pipeAuditMSS(store.ReadDB, 1)
	if len(sr.errors) != 0 {
		t.Errorf("expected no errors, got %v", sr.errors)
	}
	if len(sr.warnings) != 0 {
		t.Errorf("expected no warnings, got %v", sr.warnings)
	}
}

func TestPipeAuditMSS_LaunderingViolation_Errors(t *testing.T) {
	store := newGateStore(t)
	// Insert an unknown finding.
	unknownID := insertFindingSQL(t, store, 1, "a", "unknown", "gap", "")
	// Insert a guarantee that depends on the unknown — this is MSS laundering.
	// We use raw SQL to bypass write-time enforcement.
	depsJSON := "[" + itoa64(unknownID) + "]"
	insertFindingSQL(t, store, 1, "b", "guarantee", "launderer", depsJSON)

	sr := pipeAuditMSS(store.ReadDB, 1)
	if len(sr.errors) == 0 {
		t.Errorf("expected laundering error, got none")
	}
	found := false
	for _, e := range sr.errors {
		if strings.Contains(e, "laundering") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'laundering' in error messages, got %v", sr.errors)
	}
}

// The skew warning flags overclaiming only: a wave of five or more findings
// where definitions, or guarantees, pass 80%. A wave of mostly assumptions
// or unknowns is honest research and draws no warning. The expected warnings
// are computed from each mix by that rule.
func TestPipeAuditMSS_SkewWarnsOnlyOnOverclaiming(t *testing.T) {
	for _, mix := range []map[string]int{
		{"assumption": 5},
		{"unknown": 5},
		{"assumption": 9, "unknown": 1},
		{"definition": 5},
		{"guarantee": 5},
		{"definition": 4, "assumption": 1},
		{"definition": 9, "assumption": 1},
		{"guarantee": 9, "unknown": 1},
		{"definition": 3, "guarantee": 2},
		{"definition": 4},
	} {
		t.Run(fmt.Sprint(mix), func(t *testing.T) {
			store := newGateStore(t)
			total := 0
			for label, n := range mix {
				for i := 0; i < n; i++ {
					insertFindingSQL(t, store, 1, "a", label, "finding", "")
				}
				total += n
			}
			var want []string
			for _, label := range []string{"definition", "guarantee"} {
				if total >= 5 && float64(mix[label])/float64(total) > 0.8 {
					want = append(want, label)
				}
			}

			sr := pipeAuditMSS(store.ReadDB, 1)
			var got []string
			for _, w := range sr.warnings {
				if !strings.Contains(w, "MSS skew") {
					continue
				}
				named := "a label it does not name"
				for _, label := range []string{"definition", "guarantee", "assumption", "unknown"} {
					if strings.Contains(w, " are "+label) {
						named = label
					}
				}
				got = append(got, named)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("skew warnings for %v: %v (%q), want %v", mix, got, sr.warnings, want)
			}
		})
	}
}

func TestPipeAuditMSS_BalancedLabels_NoSkewWarning(t *testing.T) {
	store := newGateStore(t)
	// Insert 5 findings with mixed labels — no single label exceeds 80%.
	for _, label := range []string{"definition", "assumption", "assumption", "definition", "assumption"} {
		insertFindingSQL(t, store, 1, "a", label, "finding", "")
	}

	sr := pipeAuditMSS(store.ReadDB, 1)
	for _, w := range sr.warnings {
		if strings.Contains(w, "MSS skew") {
			t.Errorf("unexpected skew warning with balanced labels: %v", sr.warnings)
		}
	}
}

func TestPipeAuditMSS_FewFindings_SkewCheckSkipped(t *testing.T) {
	store := newGateStore(t)
	// Only 4 findings (below threshold of 5) — skew check is skipped even if all same label.
	for i := 0; i < 4; i++ {
		insertFindingSQL(t, store, 1, "a", "definition", "finding", "")
	}

	sr := pipeAuditMSS(store.ReadDB, 1)
	for _, w := range sr.warnings {
		if strings.Contains(w, "MSS skew") {
			t.Errorf("unexpected skew warning for fewer than 5 findings: %v", sr.warnings)
		}
	}
}

// TestPipeDecideGate covers all three explicit branches of pipeDecideGate:
//  1. Errors present → gate stays closed, no DB write.
//  2. Warnings present with force=false → gate stays closed, no DB write.
//  3. Happy path (no errors; either no warnings or force=true) → gate opens.
func TestPipeDecideGate_ErrorsBlock(t *testing.T) {
	store := newGateStore(t)
	result := &GateResult{Errors: []string{"something failed"}}
	got, err := pipeDecideGate(store.ReadDB, store.WriteDB, 1, result, false, gateFlags{mss: true, sources: true, conflicts: true, agents: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Opened {
		t.Errorf("gate should stay closed when errors are present")
	}
	if len(got.Errors) != 1 {
		t.Errorf("errors should be preserved, got %v", got.Errors)
	}
}

func TestPipeDecideGate_WarningsBlockWithoutForce(t *testing.T) {
	store := newGateStore(t)
	result := &GateResult{Warnings: []string{"some warning"}}
	got, err := pipeDecideGate(store.ReadDB, store.WriteDB, 1, result, false, gateFlags{mss: true, sources: true, conflicts: true, agents: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Opened {
		t.Errorf("gate should stay closed when warnings are present and force=false")
	}
	if len(got.Warnings) != 1 {
		t.Errorf("warnings should be preserved, got %v", got.Warnings)
	}
}

func TestPipeDecideGate_WarningsOpenWithForce(t *testing.T) {
	store := newGateStore(t)
	// Seed an evaluation so the SELECT in pipeDecideGate finds an evalID.
	if _, err := store.WriteDB.Exec(
		`INSERT INTO evaluations (wave, coverage_score, depth_score, source_score, actionability_score, mss_integrity_score, verdict)
		 VALUES (1, 4, 4, 4, 4, 3, 'COMPLETE')`,
	); err != nil {
		t.Fatal(err)
	}
	result := &GateResult{Warnings: []string{"some warning"}}
	got, err := pipeDecideGate(store.ReadDB, store.WriteDB, 1, result, true, gateFlags{mss: true, sources: true, conflicts: true, agents: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Opened {
		t.Errorf("gate should open when force=true even with warnings")
	}
}

func TestPipeDecideGate_HappyPath_NoErrorsNoWarnings(t *testing.T) {
	store := newGateStore(t)
	if _, err := store.WriteDB.Exec(
		`INSERT INTO evaluations (wave, coverage_score, depth_score, source_score, actionability_score, mss_integrity_score, verdict)
		 VALUES (1, 5, 5, 5, 5, 5, 'COMPLETE')`,
	); err != nil {
		t.Fatal(err)
	}
	result := &GateResult{}
	got, err := pipeDecideGate(store.ReadDB, store.WriteDB, 1, result, false, gateFlags{mss: true, sources: true, conflicts: true, agents: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Opened {
		t.Errorf("gate should open with no errors or warnings")
	}
}

// itoa64 converts an int64 to its decimal string representation
// without importing strconv in the test file.
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 20)
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// wave_gates is the audit trail for why a wave was allowed through, so each
// column records what its check found: a wave whose sources are dead and
// whose conflicts are unresolved is recorded as failing both, and the row is
// written when the wave has no evaluation, its evaluation_id NULL.
func TestGateRow_RecordsWhatWasActuallyChecked(t *testing.T) {
	store := newGateStore(t)

	// A wave with one dead source must not open, and must not be recorded
	// as having passed the source check.
	if _, err := store.WriteDB.Exec(
		`INSERT INTO sources (url, wave, validation_status) VALUES ('http://dead', 1, 'dead')`,
	); err != nil {
		t.Fatal(err)
	}
	insertFindingSQL(t, store, 1, "a", "definition", "x", "")

	res, err := RunGatePipeline(store, 1, map[string]any{"verdict": "COMPLETE"}, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Opened {
		t.Errorf("the gate opened on a wave with a dead source: %+v", res)
	}
	if len(res.Errors) == 0 {
		t.Errorf("no error reported for a dead source")
	}

	// Once the source is live the gate opens, and the row says so.
	if _, err := store.WriteDB.Exec(
		`UPDATE sources SET validation_status='live' WHERE url='http://dead'`); err != nil {
		t.Fatal(err)
	}
	res, err = RunGatePipeline(store, 1, map[string]any{"verdict": "COMPLETE"}, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Opened {
		t.Fatalf("the gate stayed shut on a clean wave: %+v", res)
	}
	var src, conf int
	if err := store.ReadDB.QueryRow(
		`SELECT source_check_passed, conflict_check_passed FROM wave_gates WHERE wave=1`,
	).Scan(&src, &conf); err != nil {
		t.Fatalf("the gate opened but recorded no row: %v", err)
	}
	if src != 1 || conf != 1 {
		t.Errorf("recorded source=%d conflict=%d; want 1 and 1 on a clean wave", src, conf)
	}
}
