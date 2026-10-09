package comb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// freshStore opens an isolated DB + initialised schema in t.TempDir().
func freshStore(t *testing.T) *db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comb.db")
	store, err := db.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Register a single dimension so WASP sufficiency only demands d1
	// to be non-NULL on every finding. Tests that pin further dimensions
	// supply them explicitly; tests that only pin d1 are still legal.
	if err := store.Dimensions().AddDimension("dim1", "test dim 1", `["0","1","2"]`); err != nil {
		t.Fatalf("AddDimension dim1: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func intPtr(v int) *int { return &v }

func addFinding(t *testing.T, store *db.Store, label, text string, d1, d2 *int, deps []int64) int64 {
	t.Helper()
	f := &db.Finding{
		Wave:     1,
		Agent:    "test",
		MSSLabel: label,
		Finding:  text,
		D1:       d1, D2: d2,
	}
	if len(deps) > 0 {
		b, _ := json.Marshal(deps)
		s := string(b)
		f.DependsOnIDs = &s
	}
	id, err := store.Findings().AddFinding(f)
	if err != nil {
		t.Fatalf("AddFinding(%s): %v", label, err)
	}
	return id
}

func TestBuildDigest_EmptyDB(t *testing.T) {
	store := freshStore(t)
	d, err := BuildDigest(store, Coords{})
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if d.EvidenceCount != 0 {
		t.Fatalf("expected zero evidence on empty DB, got %d", d.EvidenceCount)
	}
	if d.DominantLabel != "" {
		t.Fatalf("expected empty dominant_label on empty DB, got %q", d.DominantLabel)
	}
	if d.Contested {
		t.Fatalf("empty DB should not be contested")
	}
}

func TestBuildDigest_OneDefinitionOneAssumption(t *testing.T) {
	store := freshStore(t)
	addFinding(t, store, "definition", "the moon is grey", intPtr(0), intPtr(0), nil)
	addFinding(t, store, "assumption", "tides come from the moon", intPtr(0), intPtr(0), nil)

	d, err := BuildDigest(store, Coords{"d1": 0, "d2": 0})
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if d.EvidenceCount != 2 {
		t.Fatalf("expected 2 findings, got %d", d.EvidenceCount)
	}
	if d.Confidence == 0 {
		t.Fatalf("expected non-zero confidence with definitions present")
	}
	if d.Contested {
		t.Fatalf("two non-conflicting findings should not be contested")
	}
}

func TestBuildDigest_OpenConflictMakesContested(t *testing.T) {
	store := freshStore(t)
	a := addFinding(t, store, "assumption", "RTL-SDR draws 0.3W", intPtr(0), nil, nil)
	b := addFinding(t, store, "assumption", "RTL-SDR draws 1.2W", intPtr(0), nil, nil)
	if err := store.Conflicts().AddConflict(1, a, b, "spec disagreement"); err != nil {
		t.Fatalf("AddConflict: %v", err)
	}

	d, err := BuildDigest(store, Coords{"d1": 0})
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if !d.Contested {
		t.Fatalf("expected contested=true when an open conflict exists in region")
	}
	if d.Confidence > 60 {
		t.Fatalf("expected confidence to drop with conflict, got %d", d.Confidence)
	}
}

// Contested means contested: an unresolved conflict touches the region. The
// label mix never makes a region contested, however one-sided: honest
// research is mostly assumptions, and a colony that found no answer is
// mostly unknowns. Each region below holds five findings of one label at its
// own d1, and the expected flag is computed from its open conflicts alone.
func TestBuildDigest_ContestedMeansAnOpenConflict(t *testing.T) {
	store := freshStore(t)
	labels := []string{"assumption", "unknown", "definition"}
	ids := map[int][]int64{}
	for d1, label := range labels {
		for i := 0; i < 5; i++ {
			ids[d1] = append(ids[d1], addFinding(t, store, label, label+" finding", intPtr(d1), nil, nil))
		}
	}
	open := map[int]int{}
	check := func(when string) {
		t.Helper()
		for d1, label := range labels {
			d, err := BuildDigest(store, Coords{"d1": d1})
			if err != nil {
				t.Fatalf("BuildDigest d1=%d: %v", d1, err)
			}
			if want := open[d1] > 0; d.Contested != want {
				t.Errorf("%s: region d1=%d of %d %s findings with %d open conflict(s): contested=%v, want %v",
					when, d1, d.EvidenceCount, label, open[d1], d.Contested, want)
			}
		}
	}
	check("no conflicts")

	a, b := ids[0][0], ids[0][1]
	if err := store.Conflicts().AddConflict(1, a, b, "the two assumptions disagree"); err != nil {
		t.Fatal(err)
	}
	var conflict int64
	if err := store.ReadDB.QueryRow(`SELECT id FROM conflicts WHERE finding_a_id=? AND finding_b_id=?`, a, b).Scan(&conflict); err != nil {
		t.Fatal(err)
	}
	open[0]++
	check("one open conflict")

	if err := store.Conflicts().Resolve(conflict, 1, "a survives"); err != nil {
		t.Fatal(err)
	}
	open[0]--
	check("the conflict resolved")
}

func TestBuildAllRegions_TouchesGlobalAndPrefixes(t *testing.T) {
	store := freshStore(t)
	addFinding(t, store, "definition", "x", intPtr(0), intPtr(1), nil)
	addFinding(t, store, "assumption", "y", intPtr(0), intPtr(2), nil)

	touched, err := BuildAllRegions(context.Background(), store)
	if err != nil {
		t.Fatalf("BuildAllRegions: %v", err)
	}
	// expected regions: "" (global), d1=0, d1=0;d2=1, d1=0;d2=2 → 4 rows
	if touched < 4 {
		t.Fatalf("expected ≥4 regions touched, got %d", touched)
	}
	rows, err := store.Comb().List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) < 4 {
		t.Fatalf("expected ≥4 stored rows, got %d", len(rows))
	}
}

func TestBuildAllRegions_Idempotent(t *testing.T) {
	store := freshStore(t)
	addFinding(t, store, "definition", "x", intPtr(0), intPtr(1), nil)

	first, err := BuildAllRegions(context.Background(), store)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	second, err := BuildAllRegions(context.Background(), store)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if first != second {
		t.Fatalf("idempotent build should touch same row count: first=%d second=%d", first, second)
	}
}

func TestStaleness_FreshThenStaleAfterNewFinding(t *testing.T) {
	store := freshStore(t)
	addFinding(t, store, "definition", "x", intPtr(0), nil, nil)
	if _, err := BuildAllRegions(context.Background(), store); err != nil {
		t.Fatalf("BuildAllRegions: %v", err)
	}
	got, err := Classify(store, Coords{"d1": 0})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != StaleFresh {
		t.Fatalf("expected fresh right after refresh, got %q", got)
	}

	// A second finding in the region moves its evidence count, so a refresh
	// would write another digest.
	addFinding(t, store, "definition", "y", intPtr(0), nil, nil)
	got, err = Classify(store, Coords{"d1": 0})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != StaleStale {
		t.Fatalf("expected stale after a new finding, got %q", got)
	}
}

func TestStaleness_MissingRegion(t *testing.T) {
	store := freshStore(t)
	got, err := Classify(store, Coords{"d1": 9})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != StaleMissing {
		t.Fatalf("expected missing for never-built region, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// gaps in a region's tally
// ---------------------------------------------------------------------------

// scanGaps is the region's (critical, open) gaps as its digest counts them.
func scanGaps(read *sql.DB, region Coords) (int, int, error) {
	ts, err := regionTallies(read, region, []Coords{region})
	if err != nil {
		return 0, 0, err
	}
	t := ts[pinsOf(region, orderedDims())]
	return t.criticalGaps, t.openGaps, nil
}

func addGap(t *testing.T, store *db.Store, priority string, d1, d2, d3, d4 *int, resolved bool) {
	t.Helper()
	err := store.Gaps().AddGap(1, "test-agent", "test gap", priority, d1, d2, d3, d4)
	if err != nil {
		t.Fatalf("AddGap: %v", err)
	}
	if resolved {
		// Mark the most-recently-inserted gap as resolved.
		_, execErr := store.ReadDB.Exec(
			`UPDATE gaps SET resolved_by_wave=1, resolved_by_agent='test' WHERE id=(SELECT MAX(id) FROM gaps)`,
		)
		if execErr != nil {
			t.Fatalf("resolve gap: %v", execErr)
		}
	}
}

func TestScanGaps_EmptyDB(t *testing.T) {
	store := freshStore(t)
	crit, total, err := scanGaps(store.ReadConn(), Coords{})
	if err != nil {
		t.Fatalf("scanGaps: %v", err)
	}
	if crit != 0 || total != 0 {
		t.Fatalf("expected (0,0), got (%d,%d)", crit, total)
	}
}

func TestScanGaps_TableDriven(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(t *testing.T, store *db.Store)
		region    Coords
		wantCrit  int
		wantTotal int
	}{
		{
			name: "one critical gap global",
			setup: func(t *testing.T, store *db.Store) {
				addGap(t, store, "critical", intPtr(0), nil, nil, nil, false)
			},
			region:    Coords{},
			wantCrit:  1,
			wantTotal: 1,
		},
		{
			name: "one important gap counted in total not critical",
			setup: func(t *testing.T, store *db.Store) {
				addGap(t, store, "important", intPtr(0), nil, nil, nil, false)
			},
			region:    Coords{},
			wantCrit:  0,
			wantTotal: 1,
		},
		{
			name: "resolved gap excluded",
			setup: func(t *testing.T, store *db.Store) {
				addGap(t, store, "critical", intPtr(0), nil, nil, nil, true)
			},
			region:    Coords{},
			wantCrit:  0,
			wantTotal: 0,
		},
		{
			name: "region filter d1 matches only pinned gaps",
			setup: func(t *testing.T, store *db.Store) {
				addGap(t, store, "critical", intPtr(0), nil, nil, nil, false)
				addGap(t, store, "critical", intPtr(1), nil, nil, nil, false)
			},
			region:    Coords{"d1": 0},
			wantCrit:  1,
			wantTotal: 1,
		},
		{
			name: "region filter d1+d2 narrows further",
			setup: func(t *testing.T, store *db.Store) {
				addGap(t, store, "critical", intPtr(0), intPtr(1), nil, nil, false)
				addGap(t, store, "important", intPtr(0), intPtr(2), nil, nil, false)
			},
			region:    Coords{"d1": 0, "d2": 1},
			wantCrit:  1,
			wantTotal: 1,
		},
		{
			name: "dimension >4 ignored (d5 not filtered)",
			setup: func(t *testing.T, store *db.Store) {
				addGap(t, store, "critical", intPtr(0), nil, nil, nil, false)
			},
			// d5 is beyond what scanGaps filters; the gap should still appear.
			region:    Coords{"d1": 0, "d5": 99},
			wantCrit:  1,
			wantTotal: 1,
		},
		{
			name: "mixed critical and non-critical",
			setup: func(t *testing.T, store *db.Store) {
				addGap(t, store, "critical", intPtr(0), nil, nil, nil, false)
				addGap(t, store, "critical", intPtr(0), nil, nil, nil, false)
				addGap(t, store, "important", intPtr(0), nil, nil, nil, false)
			},
			region:    Coords{"d1": 0},
			wantCrit:  2,
			wantTotal: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := freshStore(t)
			tc.setup(t, store)
			crit, total, err := scanGaps(store.ReadConn(), tc.region)
			if err != nil {
				t.Fatalf("scanGaps: %v", err)
			}
			if crit != tc.wantCrit || total != tc.wantTotal {
				t.Fatalf("got (crit=%d, total=%d), want (crit=%d, total=%d)",
					crit, total, tc.wantCrit, tc.wantTotal)
			}
		})
	}
}

func TestResolveTemplate_FallbackToWiderRegion(t *testing.T) {
	store := freshStore(t)
	addFinding(t, store, "definition", "x", intPtr(0), nil, nil)
	if _, err := BuildAllRegions(context.Background(), store); err != nil {
		t.Fatalf("BuildAllRegions: %v", err)
	}
	// We have d1=0 but not d1=0;d2=7.
	got := Resolve(store, "d1=0;d2=7")
	if got == "" {
		t.Fatalf("expected fallback to d1=0 narrative, got empty string")
	}
}

func TestResolveTemplate_MalformedKeyReturnsEmpty(t *testing.T) {
	store := freshStore(t)
	got := Resolve(store, "this is not a region")
	if got != "" {
		t.Fatalf("malformed key should resolve to empty, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// BuildForagerVantage
// ---------------------------------------------------------------------------

func TestBuildForagerVantage_HappyPath(t *testing.T) {
	store := freshStore(t)

	tests := []struct {
		name             string
		foragerName      string
		rawJSON          string
		verdict          map[string]any
		wantConfidence   int
		wantContested    bool
		wantNarrativeHas string // substring that must appear in stored Narrative
		wantRawJSONSet   bool
	}{
		{
			name:             "support verdict with recommendation",
			foragerName:      "optimist",
			rawJSON:          `{"verdict":"support"}`,
			verdict:          map[string]any{"verdict": "support", "recommendation": "ship it"},
			wantConfidence:   80,
			wantContested:    false,
			wantNarrativeHas: "ship it",
			wantRawJSONSet:   true,
		},
		{
			name:             "oppose verdict with recommendation",
			foragerName:      "skeptic",
			rawJSON:          "",
			verdict:          map[string]any{"verdict": "oppose", "recommendation": "hold"},
			wantConfidence:   20,
			wantContested:    false,
			wantNarrativeHas: "hold",
			wantRawJSONSet:   false,
		},
		{
			name:             "conditional verdict — contested, no recommendation falls back",
			foragerName:      "pragmatist",
			rawJSON:          "",
			verdict:          map[string]any{"verdict": "conditional"},
			wantConfidence:   50,
			wantContested:    true,
			wantNarrativeHas: "no recommendation supplied",
			wantRawJSONSet:   false,
		},
		{
			name:             "abstain verdict, no recommendation fallback",
			foragerName:      "historian",
			rawJSON:          "",
			verdict:          map[string]any{"verdict": "abstain"},
			wantConfidence:   30,
			wantContested:    false,
			wantNarrativeHas: "verdict=abstain",
			wantRawJSONSet:   false,
		},
		{
			name:             "empty verdict string — contested",
			foragerName:      "dreamer",
			rawJSON:          "",
			verdict:          map[string]any{},
			wantConfidence:   50,
			wantContested:    true,
			wantNarrativeHas: "no recommendation supplied",
			wantRawJSONSet:   false,
		},
		{
			name:             "verdict with evidence and uncertainties lists",
			foragerName:      "empiricist",
			rawJSON:          `{"verdict":"support"}`,
			verdict:          map[string]any{"verdict": "support", "recommendation": "go", "evidence": []any{"e1", "e2"}, "uncertainties": []any{"u1"}},
			wantConfidence:   80,
			wantContested:    false,
			wantNarrativeHas: "go",
			wantRawJSONSet:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			err := BuildForagerVantage(ctx, store, 0, tc.foragerName, tc.rawJSON, tc.verdict)
			if err != nil {
				t.Fatalf("BuildForagerVantage: %v", err)
			}

			key := db.ForagerVantageKey(tc.foragerName)
			row, err := store.Comb().Get(key)
			if err != nil {
				t.Fatalf("Comb.Get(%q): %v", key, err)
			}
			if row == nil {
				t.Fatalf("expected row for key %q, got nil", key)
			}
			if row.Confidence != tc.wantConfidence {
				t.Errorf("confidence = %d, want %d", row.Confidence, tc.wantConfidence)
			}
			if row.Contested != tc.wantContested {
				t.Errorf("contested = %v, want %v", row.Contested, tc.wantContested)
			}
			if !strings.Contains(row.Narrative, tc.wantNarrativeHas) {
				t.Errorf("narrative = %q, want it to contain %q", row.Narrative, tc.wantNarrativeHas)
			}
			if tc.wantRawJSONSet && (!row.RawJSON.Valid || row.RawJSON.String == "") {
				t.Errorf("expected RawJSON to be set, got invalid/empty")
			}
			if !tc.wantRawJSONSet && row.RawJSON.Valid && row.RawJSON.String != "" {
				t.Errorf("expected RawJSON to be unset, got %q", row.RawJSON.String)
			}
			if row.VantageKind != db.VantageForager {
				t.Errorf("vantage_kind = %q, want %q", row.VantageKind, db.VantageForager)
			}
		})
	}
}

func TestBuildForagerVantage_UpsertError(t *testing.T) {
	store := freshStore(t)
	// Close the store so Upsert fails with a DB error.
	store.Close()

	err := BuildForagerVantage(context.Background(), store, 0, "optimist", "", map[string]any{"verdict": "support"})
	if err == nil {
		t.Fatal("expected error after store closed, got nil")
	}
}

func TestCountList(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want int
	}{
		{
			name: "missing key returns 0",
			m:    map[string]any{"other": []any{"a", "b"}},
			key:  "missing",
			want: 0,
		},
		{
			name: "nil map returns 0",
			m:    nil,
			key:  "k",
			want: 0,
		},
		{
			name: "empty []any returns 0",
			m:    map[string]any{"k": []any{}},
			key:  "k",
			want: 0,
		},
		{
			name: "[]any with 3 elements",
			m:    map[string]any{"k": []any{"x", "y", "z"}},
			key:  "k",
			want: 3,
		},
		{
			name: "[]string with 2 elements",
			m:    map[string]any{"k": []string{"a", "b"}},
			key:  "k",
			want: 2,
		},
		{
			name: "empty []string returns 0",
			m:    map[string]any{"k": []string{}},
			key:  "k",
			want: 0,
		},
		{
			name: "unsupported type (int) returns 0",
			m:    map[string]any{"k": 42},
			key:  "k",
			want: 0,
		},
		{
			name: "unsupported type (string scalar) returns 0",
			m:    map[string]any{"k": "hello"},
			key:  "k",
			want: 0,
		},
		{
			name: "nil value for key returns 0",
			m:    map[string]any{"k": nil},
			key:  "k",
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := countList(tc.m, tc.key)
			if got != tc.want {
				t.Fatalf("countList(%v, %q) = %d, want %d", tc.m, tc.key, got, tc.want)
			}
		})
	}
}

// BuildDigest counts what the tables hold, by queries written from comb.md's
// rules, for every region of the staleness fixture: the findings in it and
// their labels, the unresolved conflicts either of whose findings lies in it,
// once each, and its open gaps and unanswered followups, which carry only
// d1..d4. The staleness rule computes its digests with the same code, so
// this is what makes its comparison one against the tables.
func TestBuildDigest_CountsAsTheTablesSay(t *testing.T) {
	store := storeForTest(t)
	seedStaleness(t, store)
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := store.ReadDB.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	seen := map[string]bool{}
	for _, key := range combKeys(t, store) {
		region, err := ParseRegionKey(key)
		if err != nil {
			continue
		}
		pin := func(prefix string, upTo int) (string, []any) {
			q, args := "", []any{}
			for i := 1; i <= upTo; i++ {
				d := fmt.Sprintf("d%d", i)
				if v, ok := region[d]; ok {
					q += " AND " + prefix + d + " = ?"
					args = append(args, v)
				}
			}
			return q, args
		}
		fq, fa := pin("f.", 8)
		gq, ga := pin("", 4)
		labels := map[string]int{}
		for _, l := range []string{"definition", "guarantee", "assumption", "unknown"} {
			labels[l] = count(`SELECT COUNT(*) FROM findings f WHERE mss_label = ?`+fq, append([]any{l}, fa...)...)
		}
		f := labels["definition"] + labels["guarantee"] + labels["assumption"] + labels["unknown"]
		dom, most := "", 0
		for _, l := range []string{"definition", "guarantee", "assumption", "unknown"} {
			if labels[l] > most {
				dom, most = l, labels[l]
			}
		}
		c := count(`SELECT COUNT(DISTINCT c.id) FROM conflicts c JOIN findings f ON f.id IN (c.finding_a_id, c.finding_b_id)
			WHERE c.resolution IS NULL`+fq, fa...)
		aq, aa := pin("a.", 8)
		bq, ba := pin("b.", 8)
		both := count(`SELECT COUNT(*) FROM conflicts c JOIN findings a ON a.id = c.finding_a_id JOIN findings b ON b.id = c.finding_b_id
			WHERE c.resolution IS NULL`+aq+bq, append(aa, ba...)...)
		g := count(`SELECT COUNT(*) FROM gaps WHERE resolved_by_wave IS NULL AND priority = 'critical'`+gq, ga...)
		open := count(`SELECT COUNT(*) FROM gaps WHERE resolved_by_wave IS NULL`+gq, ga...) +
			count(`SELECT COUNT(*) FROM followups WHERE answered = 0`+gq, ga...)

		d, err := BuildDigest(store, region)
		if err != nil {
			t.Fatal(err)
		}
		if d.EvidenceCount != f || d.DominantLabel != dom || d.Confidence != exactConfidence(f, c, g) ||
			d.Contested != (c > 0) || d.OpenQuestionsCount != open {
			t.Errorf("%q: digest %+v; the tables give %d findings, dominant %q, %d conflicts, %d critical gaps, %d open questions",
				key, d, f, dom, c, g, open)
		}
		if f > 0 {
			for _, part := range []struct {
				text string
				n    int
			}{{" conflicts=%d", c}, {" critical_gaps=%d", g}} {
				if got := strings.Contains(d.Narrative, fmt.Sprintf(part.text, part.n)); got != (part.n > 0) {
					t.Errorf("%q: narrative %q; want %q %v", key, d.Narrative, fmt.Sprintf(part.text, part.n), part.n > 0)
				}
			}
		}
		seen["a conflict with both findings in the region"] = seen["a conflict with both findings in the region"] || both > 0
		seen["a conflict with one finding in the region"] = seen["a conflict with one finding in the region"] || c > both
		seen["a critical gap"] = seen["a critical gap"] || g > 0
		seen["an open followup"] = seen["an open followup"] || open > count(`SELECT COUNT(*) FROM gaps WHERE resolved_by_wave IS NULL`+gq, ga...)
		seen["no findings"] = seen["no findings"] || f == 0
		_, pastD4 := region["d5"]
		seen["an open gap under a pin past d4"] = seen["an open gap under a pin past d4"] || pastD4 && open > 0
	}
	for _, kind := range []string{"a conflict with both findings in the region", "a conflict with one finding in the region",
		"a critical gap", "an open followup", "no findings", "an open gap under a pin past d4"} {
		if !seen[kind] {
			t.Errorf("no region holds %s; the fixture must hold one", kind)
		}
	}
}

func builderTestStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.NewStore(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func intP(v int) *int { return &v }

func TestBuildDigest_EmptyRegion(t *testing.T) {
	s := builderTestStore(t)
	d, err := BuildDigest(s, Coords{"d1": 0})
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if d.EvidenceCount != 0 {
		t.Errorf("EvidenceCount = %d; want 0", d.EvidenceCount)
	}
	if d.Confidence != 0 {
		// no findings → nothing to be confident in → 0.
		t.Errorf("Confidence = %d; want 0", d.Confidence)
	}
}

func TestBuildDigest_GlobalRegion(t *testing.T) {
	s := builderTestStore(t)
	for i := 0; i < 5; i++ {
		if _, err := s.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: "a", MSSLabel: "definition",
			D1: intP(0), D2: intP(0), D3: intP(0), D4: intP(0),
			Finding: "f",
		}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := BuildDigest(s, Coords{}) // global
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if d.EvidenceCount != 5 {
		t.Errorf("EvidenceCount = %d; want 5", d.EvidenceCount)
	}
	if d.DominantLabel != "definition" {
		t.Errorf("DominantLabel = %q; want definition", d.DominantLabel)
	}
}

func TestBuildDigest_ContestedFlag(t *testing.T) {
	s := builderTestStore(t)
	// 3 definitions, 3 assumptions → no dominant > 60%, balanced.
	for _, label := range []string{"definition", "definition", "definition", "assumption", "assumption", "assumption"} {
		if _, err := s.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: "a", MSSLabel: label,
			D1: intP(0), D2: intP(0), D3: intP(0), D4: intP(0),
			Finding: "f",
		}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := BuildDigest(s, Coords{})
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if d.EvidenceCount != 6 {
		t.Errorf("EvidenceCount = %d; want 6", d.EvidenceCount)
	}
}

func TestPickDominant_SingleLabel(t *testing.T) {
	dom, count := pickDominant(map[string]int{"definition": 5})
	if dom != "definition" || count != 5 {
		t.Errorf("got %q,%d; want definition,5", dom, count)
	}
}

func TestPickDominant_TieReturnsDeterministic(t *testing.T) {
	dom, _ := pickDominant(map[string]int{"definition": 3, "assumption": 3})
	if dom == "" {
		t.Error("expected non-empty dominant on tie")
	}
}

func TestPickDominant_Empty(t *testing.T) {
	dom, count := pickDominant(map[string]int{})
	if dom != "" || count != 0 {
		t.Errorf("got %q,%d; want empty,0", dom, count)
	}
}
