package gate

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func TestExtractNumbersWithContext_Multiplier(t *testing.T) {
	got := extractNumbersWithContext("revenue $5 million in 2025")
	if len(got) != 1 {
		t.Fatalf("expected 1 number, got %d", len(got))
	}
	if got[0].typ != "dollar" || got[0].val != 5_000_000 {
		t.Errorf("expected $5M = 5000000, got %v", got[0])
	}
}

func TestExtractNumbersWithContext_PlainDollar(t *testing.T) {
	got := extractNumbersWithContext("price tag $89.99 retail")
	if len(got) == 0 || got[0].val != 89.99 {
		t.Errorf("expected $89.99, got %v", got)
	}
}

func TestExtractNumbersWithContext_Percent(t *testing.T) {
	got := extractNumbersWithContext("47% margin")
	if len(got) != 1 || got[0].typ != "percent" || got[0].val != 47 {
		t.Errorf("expected 47 percent, got %v", got)
	}
}

func TestContentWords_FiltersStopwordsAndShortWords(t *testing.T) {
	got := contentWords("the quick brown fox jumps for the lazy dog with not has")
	// Stopwords from the package list: the, for, with, not, has → filtered.
	for _, w := range []string{"the", "for", "with", "not", "has"} {
		if got[w] {
			t.Errorf("%q should be filtered as stopword", w)
		}
	}
	// Words ≤3 chars also filtered: "fox", "dog".
	if got["fox"] || got["dog"] {
		t.Errorf("short words (≤3 chars) should be filtered, got %v", got)
	}
	// Content words (>3 chars, not stopwords) retained.
	if !got["quick"] || !got["brown"] || !got["jumps"] || !got["lazy"] {
		t.Errorf("expected content words, got %v", got)
	}
}

func TestContentWords_StripsPunctuation(t *testing.T) {
	got := contentWords("hello, world! testing... punctuation.")
	if !got["hello"] || !got["world"] || !got["testing"] || !got["punctuation"] {
		t.Errorf("punctuation not stripped: %v", got)
	}
}

func TestContextsMatch_Strong(t *testing.T) {
	a := "manufacturing cost per unit including packaging"
	b := "production cost per item including packaging materials"
	if !contextsMatch(a, b) {
		t.Errorf("strong overlap should match: %q vs %q", a, b)
	}
}

func TestContextsMatch_Weak(t *testing.T) {
	a := "shipping cost varies"
	b := "marketing budget allocated"
	if contextsMatch(a, b) {
		t.Errorf("disjoint contexts should not match")
	}
}

func TestContextsMatch_EmptyInputs(t *testing.T) {
	if contextsMatch("", "anything") {
		t.Errorf("empty input should not match")
	}
}

func TestNegationConflict_OneNegated(t *testing.T) {
	a := findingRow{finding: "the system supports plugins"}
	b := findingRow{finding: "the system does not support plugins"}
	conflicts := negationConflict(a, b)
	if len(conflicts) == 0 {
		t.Errorf("expected conflict — one asserts, other negates same subject")
	}
}

func TestNegationConflict_BothNegated_NoConflict(t *testing.T) {
	a := findingRow{finding: "system does not support plugins"}
	b := findingRow{finding: "system cannot run plugins"}
	if got := negationConflict(a, b); len(got) > 0 {
		t.Errorf("both negated → no conflict, got %v", got)
	}
}

func TestNegationConflict_DisjointSubjects(t *testing.T) {
	a := findingRow{finding: "the carbon emissions are stable"}
	b := findingRow{finding: "the marketing budget is not approved"}
	if got := negationConflict(a, b); len(got) > 0 {
		t.Errorf("disjoint subjects → no conflict, got %v", got)
	}
}

func TestNegationConflict_EmptyContentWords(t *testing.T) {
	// One side negates, but content words are empty after stopword filtering
	// (text is all short/stop words) → no conflict reported.
	a := findingRow{finding: "not"}      // only stopword-ish / short → wordsA empty
	b := findingRow{finding: "supports"} // wordsB non-empty but wordsA is empty → guard returns nil
	if got := negationConflict(a, b); len(got) > 0 {
		t.Errorf("empty content words should yield no conflict, got %v", got)
	}
}

func TestNegationConflict_ANegates_BAsserts(t *testing.T) {
	// a is the negator (aNeg=true), b is the positive asserter (bNeg=false).
	// Exercises the `negatorID = a.id` branch (opposite swap from OneNegated test).
	a := findingRow{id: 10, finding: "the system cannot support plugins extensions features"}
	b := findingRow{id: 20, finding: "the system fully supports plugins extensions features"}
	conflicts := negationConflict(a, b)
	if len(conflicts) == 0 {
		t.Errorf("expected conflict — a negates while b asserts same subject")
	}
	// negatorID should be a.id (10), asserterID should be b.id (20)
	if !strings.Contains(conflicts[0], "10") || !strings.Contains(conflicts[0], "20") {
		t.Errorf("conflict message should reference finding IDs 10 and 20, got: %s", conflicts[0])
	}
}

func TestNegationConflict_ManySharedTerms(t *testing.T) {
	// More than 5 overlapping content words → exercises the `len(shared) >= 5` break.
	a := findingRow{finding: "the platform supports authentication authorization caching logging monitoring telemetry"}
	b := findingRow{finding: "the platform does not support authentication authorization caching logging monitoring telemetry"}
	conflicts := negationConflict(a, b)
	if len(conflicts) == 0 {
		t.Errorf("expected conflict with many shared terms")
	}
	// Result must be exactly one conflict string regardless of shared-term count.
	if len(conflicts) != 1 {
		t.Errorf("expected exactly 1 conflict entry, got %d: %v", len(conflicts), conflicts)
	}
}

func TestDetectConflicts_EmptyDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}

	report, err := DetectConflicts(store, nil, true)
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if report.Total != 0 {
		t.Errorf("expected 0 conflicts on empty DB, got %d", report.Total)
	}
}

func TestDetectConflicts_NumericDivergence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}

	d := func(v int) *int { return &v }
	// Two findings at the same coordinate with divergent dollar values.
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "definition",
		Finding:  "manufacturing cost is $50 per unit",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "b", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "definition",
		Finding:  "manufacturing cost is $200 per unit",
	}); err != nil {
		t.Fatal(err)
	}

	report, err := DetectConflicts(store, nil, true)
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if report.Total == 0 {
		t.Errorf("expected divergence conflict on $50 vs $200 same coord, got %+v", report)
	}
}

func TestDetectConflicts_WaveFilter(t *testing.T) {
	store := newTestStore(t)
	d := func(v int) *int { return &v }

	// wave=1: two findings with numeric divergence at same coord.
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "definition", Finding: "manufacturing cost is $50 per unit",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "b", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "definition", Finding: "manufacturing cost is $200 per unit",
	}); err != nil {
		t.Fatal(err)
	}
	// wave=2: a single finding — no pair, no conflict.
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 2, Agent: "c", D1: d(1), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "assumption", Finding: "revenue is $500 per unit",
	}); err != nil {
		t.Fatal(err)
	}

	wave1 := 1
	report, err := DetectConflicts(store, &wave1, true)
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if report.Total == 0 {
		t.Errorf("expected conflicts in wave 1, got none")
	}

	wave2 := 2
	report2, err := DetectConflicts(store, &wave2, true)
	if err != nil {
		t.Fatalf("DetectConflicts wave2: %v", err)
	}
	if report2.Total != 0 {
		t.Errorf("expected no conflicts in wave 2 (single finding), got %d", report2.Total)
	}
}

func TestDetectConflicts_MSSLabelDisagreement(t *testing.T) {
	store := newTestStore(t)
	d := func(v int) *int { return &v }

	// Add the assumption first to get a valid ID to depend on.
	assumptionID, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "b", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "assumption", Finding: "margin is approximately 47%",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Guarantee at the same coordinate — depends on the assumption above.
	depJSON := fmt.Sprintf("[%d]", assumptionID)
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "guarantee", Finding: "margin is 47%",
		DependsOnIDs: &depJSON,
	}); err != nil {
		t.Fatal(err)
	}

	report, err := DetectConflicts(store, nil, true)
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if report.MSSLabel == 0 {
		t.Errorf("expected MSS label conflict, got none: %+v", report)
	}
}

func TestDetectConflicts_NegationConflict(t *testing.T) {
	store := newTestStore(t)
	d := func(v int) *int { return &v }

	// Same coordinate: one asserts, one negates the same subject.
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "assumption",
		Finding:  "the platform supports plugins extensions features integrations",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "b", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "assumption",
		Finding:  "the platform does not support plugins extensions features integrations",
	}); err != nil {
		t.Fatal(err)
	}

	report, err := DetectConflicts(store, nil, true)
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if report.Negation == 0 {
		t.Errorf("expected negation conflict, got none: %+v", report)
	}
}

func TestDetectConflicts_DryRunFalse_WritesToDB(t *testing.T) {
	store := newTestStore(t)
	d := func(v int) *int { return &v }

	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "definition", Finding: "manufacturing cost is $50 per unit",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "b", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
		MSSLabel: "definition", Finding: "manufacturing cost is $200 per unit",
	}); err != nil {
		t.Fatal(err)
	}

	report, err := DetectConflicts(store, nil, false) // dryRun=false → writes conflicts
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if report.Total == 0 {
		t.Errorf("expected conflicts to be returned, got none")
	}

	// Verify at least one row landed in the conflicts table.
	var count int
	if err := store.ReadDB.QueryRow("SELECT COUNT(*) FROM conflicts").Scan(&count); err != nil {
		t.Fatalf("query conflicts: %v", err)
	}
	if count == 0 {
		t.Errorf("dryRun=false should write conflicts to DB, found 0 rows")
	}
}

func TestNumericDivergence_HappyPath_Diverges(t *testing.T) {
	a := findingRow{finding: "manufacturing cost is $50 per unit"}
	b := findingRow{finding: "manufacturing cost is $200 per unit"}
	got := numericDivergence(a, b, 0.25)
	if len(got) == 0 {
		t.Errorf("expected divergence conflict for $50 vs $200, got none")
	}
	if !strings.Contains(got[0], "50.00") || !strings.Contains(got[0], "200.00") {
		t.Errorf("conflict message should contain both values, got: %s", got[0])
	}
}

func TestNumericDivergence_HappyPath_NoDivergence(t *testing.T) {
	a := findingRow{finding: "manufacturing cost is $100 per unit"}
	b := findingRow{finding: "manufacturing cost is $105 per unit"}
	got := numericDivergence(a, b, 0.25)
	if len(got) != 0 {
		t.Errorf("expected no conflict for $100 vs $105 at 25%% threshold, got %v", got)
	}
}

func TestNumericDivergence_ExactThreshold_NoDivergence(t *testing.T) {
	// div == threshold → not strictly greater → no conflict
	a := findingRow{finding: "manufacturing cost is $100 per unit"}
	b := findingRow{finding: "manufacturing cost is $75 per unit"}
	// |100-75|/100 = 0.25; threshold = 0.25 → div == threshold → no conflict
	got := numericDivergence(a, b, 0.25)
	if len(got) != 0 {
		t.Errorf("div == threshold should yield no conflict (not strictly >), got %v", got)
	}
}

func TestNumericDivergence_MismatchedTypes_NoConflict(t *testing.T) {
	// a has a dollar amount, b has a percent — different types → skipped
	a := findingRow{finding: "revenue is $50 per unit here context"}
	b := findingRow{finding: "margin is 50% here context words here"}
	got := numericDivergence(a, b, 0.1)
	if len(got) != 0 {
		t.Errorf("mismatched types should not conflict, got %v", got)
	}
}

func TestNumericDivergence_BothZero_NoConflict(t *testing.T) {
	// Both values are $0 — skipped by the (val==0 && val==0) guard
	a := findingRow{finding: "cost is $0 per unit context words here"}
	b := findingRow{finding: "cost is $0 per unit context words here"}
	got := numericDivergence(a, b, 0.1)
	if len(got) != 0 {
		t.Errorf("both-zero case should yield no conflict, got %v", got)
	}
}

func TestNumericDivergence_ContextMismatch_NoConflict(t *testing.T) {
	// Same numeric value type but unrelated contexts → contextsMatch returns false
	a := findingRow{finding: "shipping cost is $50 per unit"}
	b := findingRow{finding: "marketing budget is $200 allocated quarterly"}
	got := numericDivergence(a, b, 0.1)
	if len(got) != 0 {
		t.Errorf("context mismatch should yield no conflict, got %v", got)
	}
}

func TestNumericDivergence_NoNumbers_ReturnsNil(t *testing.T) {
	a := findingRow{finding: "the platform supports various integrations"}
	b := findingRow{finding: "the platform has many integration options"}
	got := numericDivergence(a, b, 0.25)
	if len(got) != 0 {
		t.Errorf("no numbers → no conflicts, got %v", got)
	}
}

func TestNumericDivergence_PercentConflict(t *testing.T) {
	a := findingRow{finding: "margin is 10% profit"}
	b := findingRow{finding: "margin is 90% profit"}
	got := numericDivergence(a, b, 0.25)
	if len(got) == 0 {
		t.Errorf("expected percent divergence conflict for 10%% vs 90%%, got none")
	}
	if !strings.Contains(got[0], "percent") {
		t.Errorf("conflict message should mention type 'percent', got: %s", got[0])
	}
}

func TestDetectConflicts_SingleFindingPerCoord_NoConflict(t *testing.T) {
	store := newTestStore(t)
	d := func(v int) *int { return &v }

	// Three findings each at a distinct coordinate — no pair shares a coord.
	for i, agent := range []string{"a", "b", "c"} {
		iv := i
		if _, err := store.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: agent, D1: d(iv), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
			MSSLabel: "assumption", Finding: "some finding about something useful here",
		}); err != nil {
			t.Fatal(err)
		}
	}

	report, err := DetectConflicts(store, nil, true)
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if report.Total != 0 {
		t.Errorf("distinct-coord findings should yield no conflicts, got %d", report.Total)
	}
}

// A second detection records nothing new, and a conflict resolved in between
// stays resolved, not inserted again as a fresh unresolved row.
func TestDetectConflicts_RecordsEachConflictOnce(t *testing.T) {
	store := newTestStore(t)
	d := func(v int) *int { return &v }
	for _, text := range []string{
		"manufacturing cost is $50 per unit and the supplier does not ship to europe",
		"manufacturing cost is $200 per unit and the supplier ships to europe weekly",
	} {
		if _, err := store.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: "a", D1: d(0), D2: d(0), D3: d(0), D4: d(0), D5: d(0),
			MSSLabel: "definition", Finding: text,
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := DetectConflicts(store, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Numeric == 0 || first.Negation == 0 || first.New != first.Total {
		t.Fatalf("first run: %+v, want numeric and negation conflicts, all new", first)
	}
	if _, err := store.WriteDB.Exec(`UPDATE conflicts SET resolution = 'settled'`); err != nil {
		t.Fatal(err)
	}
	second, err := DetectConflicts(store, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.New != 0 || second.Total != first.Total {
		t.Fatalf("second run: new=%d total=%d, want 0 new of %d", second.New, second.Total, first.Total)
	}
	var rows, open int
	if err := store.ReadDB.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE resolution IS NULL) FROM conflicts`).Scan(&rows, &open); err != nil {
		t.Fatal(err)
	}
	if rows != first.Total || open != 0 {
		t.Fatalf("conflicts table: %d rows, %d unresolved; want %d rows, 0 unresolved", rows, open, first.Total)
	}
}

// Detection without a wave records each conflict under its findings' wave,
// so a conflict the dreamer recorded between two wave-1 findings counts at
// the wave 1 gate, which counts conflicts by wave, though record-once makes
// `detect-conflicts --wave 1` see it as done.
func TestDetectConflicts_NoWaveRecordsUnderTheFindingsWave(t *testing.T) {
	store := newTestStore(t)
	d := func(v int) *int { return &v }
	add := func(wave, cell int, text string) {
		t.Helper()
		if _, err := store.Findings().AddFinding(&db.Finding{
			Wave: wave, Agent: fmt.Sprintf("agent-w%d", wave), D1: d(cell),
			MSSLabel: "definition", Finding: text,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Cell 0 holds a wave-1 pair; cell 1 holds a wave-1 and a wave-3 finding.
	add(1, 0, "manufacturing cost is $50 per unit")
	add(1, 0, "manufacturing cost is $200 per unit")
	add(1, 1, "shipping cost is $10 per unit")
	add(3, 1, "shipping cost is $40 per unit")

	if _, err := DetectConflicts(store, nil, false); err != nil {
		t.Fatal(err)
	}
	// Each conflict's wave is the later of its two findings' waves.
	rows, err := store.ReadDB.Query(`
		SELECT c.wave, fa.wave, fb.wave FROM conflicts c
		JOIN findings fa ON fa.id = c.finding_a_id
		JOIN findings fb ON fb.id = c.finding_b_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var cw, wa, wb int
		if err := rows.Scan(&cw, &wa, &wb); err != nil {
			t.Fatal(err)
		}
		if want := max(wa, wb); cw != want {
			t.Errorf("conflict between waves %d and %d recorded under wave %d, want %d", wa, wb, cw, want)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("%d conflicts recorded, want 2 (one per cell)", n)
	}

	// The wave 1 gate counts the wave-1 conflict the unscoped pass recorded.
	sr := pipeResolveConflicts(store, 1, false)
	if sr.passed || len(sr.warnings) != 1 || !strings.Contains(sr.warnings[0], "1 unresolved conflicts") {
		t.Errorf("wave 1 conflict check: passed=%v warnings=%v; want 1 unresolved conflict", sr.passed, sr.warnings)
	}
}

// Detection records that two findings disagree, not which one is wrong: it
// writes a conflict and no outcome. Only an adjudication writes one.
func TestDetectConflicts_WritesNoOutcome(t *testing.T) {
	store := newGateStore(t)
	d1 := 1
	for _, text := range []string{"the plan costs $500 a month", "the plan costs $100 a month"} {
		if _, err := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: text, D1: &d1}); err != nil {
			t.Fatal(err)
		}
	}
	wave := 1
	report, err := DetectConflicts(store, &wave, false)
	if err != nil {
		t.Fatal(err)
	}
	var conflicts, outcomes int
	store.ReadDB.QueryRow(`SELECT COUNT(*) FROM conflicts`).Scan(&conflicts)
	store.ReadDB.QueryRow(`SELECT COUNT(*) FROM outcomes`).Scan(&outcomes)
	if conflicts == 0 {
		t.Fatalf("detection found nothing between two numeric claims: %+v", report)
	}
	if outcomes != 0 {
		t.Fatalf("detection wrote %d outcomes; adjudication is the one writer", outcomes)
	}
}
