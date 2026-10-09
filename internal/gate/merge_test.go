package gate

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// newTestStore creates an initialised in-memory-style SQLite store for tests.
func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Init(); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return store
}

func TestDetectNumericConflicts_NoFindings(t *testing.T) {
	store := newTestStore(t)
	if got, err := detectNumericConflicts(store, nil); err != nil || got != 0 {
		t.Errorf("nil slice: expected 0, got %d", got)
	}
	if got, err := detectNumericConflicts(store, []findingForMerge{}); err != nil || got != 0 {
		t.Errorf("empty slice: expected 0, got %d", got)
	}
}

func TestDetectNumericConflicts_SingleFinding(t *testing.T) {
	store := newTestStore(t)
	findings := []findingForMerge{{id: 1, wave: 1, finding: "cost is $100"}}
	if got, err := detectNumericConflicts(store, findings); err != nil || got != 0 {
		t.Errorf("single finding: expected 0 (no pair), got %d", got)
	}
}

func TestDetectNumericConflicts_NoNumbers(t *testing.T) {
	store := newTestStore(t)
	findings := []findingForMerge{
		{id: 1, wave: 1, finding: "product is blue"},
		{id: 2, wave: 1, finding: "product is red"},
	}
	if got, err := detectNumericConflicts(store, findings); err != nil || got != 0 {
		t.Errorf("no numbers: expected 0, got %d", got)
	}
}

func TestDetectNumericConflicts_Table(t *testing.T) {
	for _, tc := range []struct {
		name      string
		findingA  string
		findingB  string
		wantCount int
	}{
		{
			name:      "divergent dollars (>20%) inserts conflict",
			findingA:  "manufacturing cost is $50 per unit",
			findingB:  "manufacturing cost is $200 per unit",
			wantCount: 1,
		},
		{
			name:      "close dollars (≤20%) no conflict",
			findingA:  "price is $100",
			findingB:  "price is $110",
			wantCount: 0,
		},
		{
			name:      "divergent percents (>20%) inserts conflict",
			findingA:  "margin is 10%",
			findingB:  "margin is 50%",
			wantCount: 1,
		},
		{
			name:      "mismatched types (dollar vs percent) no conflict",
			findingA:  "cost is $80",
			findingB:  "rate is 80%",
			wantCount: 0,
		},
		{
			name:      "zero value in min skips comparison",
			findingA:  "count is $0",
			findingB:  "count is $100",
			wantCount: 0,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			// Insert real finding rows so the FK in conflicts is satisfied.
			idA, err := store.Findings().AddFinding(&db.Finding{
				Wave: 1, Agent: "a",
				MSSLabel: "definition",
				Finding:  tc.findingA,
			})
			if err != nil {
				t.Fatalf("AddFinding A: %v", err)
			}
			idB, err := store.Findings().AddFinding(&db.Finding{
				Wave: 1, Agent: "b",
				MSSLabel: "definition",
				Finding:  tc.findingB,
			})
			if err != nil {
				t.Fatalf("AddFinding B: %v", err)
			}
			findings := []findingForMerge{
				{id: idA, wave: 1, finding: tc.findingA},
				{id: idB, wave: 1, finding: tc.findingB},
			}
			got, err := detectNumericConflicts(store, findings)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.wantCount {
				t.Errorf("%s: got %d conflicts, want %d", tc.name, got, tc.wantCount)
			}
		})
	}
}

func TestExtractNumbers_Dollar(t *testing.T) {
	for _, tc := range []struct {
		text string
		want float64
	}{
		{"costs $89", 89},
		{"costs $1,250.50", 1250.50},
		{"$0.99 each", 0.99},
		{"no money here", 0},
	} {
		got := extractNumbers(tc.text)
		if tc.want == 0 {
			if len(got) != 0 {
				t.Errorf("text=%q: expected no nums, got %v", tc.text, got)
			}
			continue
		}
		if len(got) != 1 || got[0].typ != "dollar" || math.Abs(got[0].val-tc.want) > 0.001 {
			t.Errorf("text=%q: got %v, want $%g", tc.text, got, tc.want)
		}
	}
}

func TestExtractNumbers_Percent(t *testing.T) {
	got := extractNumbers("47% margin and 12.5% return")
	if len(got) != 2 {
		t.Fatalf("expected 2 percents, got %v", got)
	}
	if got[0].typ != "percent" || got[0].val != 47 {
		t.Errorf("first: %v", got[0])
	}
	if got[1].val != 12.5 {
		t.Errorf("second: %v", got[1])
	}
}

func TestJaccardSimilarity_Identical(t *testing.T) {
	s := "the quick brown fox"
	if got := jaccardSimilarity(s, s); got != 1.0 {
		t.Errorf("identical strings should be 1.0, got %v", got)
	}
}

func TestJaccardSimilarity_Disjoint(t *testing.T) {
	if got := jaccardSimilarity("cat dog", "table chair"); got != 0 {
		t.Errorf("disjoint should be 0, got %v", got)
	}
}

func TestJaccardSimilarity_Empty(t *testing.T) {
	if got := jaccardSimilarity("", "anything"); got != 0 {
		t.Errorf("empty input should be 0, got %v", got)
	}
}

func TestJaccardSimilarity_PartialOverlap(t *testing.T) {
	got := jaccardSimilarity("the quick brown fox", "the slow brown dog")
	// {the, quick, brown, fox} ∩ {the, slow, brown, dog} = {the, brown} = 2
	// union = 6 → 2/6 ≈ 0.333
	if math.Abs(got-2.0/6) > 0.01 {
		t.Errorf("expected ~0.333, got %v", got)
	}
}

func TestToWordSet_StripsEmpty(t *testing.T) {
	got := toWordSet("hello   world  ")
	if len(got) != 2 || !got["hello"] || !got["world"] {
		t.Errorf("expected {hello,world}, got %v", got)
	}
}

func TestSourceDiversity_JSONArray(t *testing.T) {
	urls := `["https://nih.gov/p1","https://cdc.gov/p2","https://nih.gov/p3"]`
	findings := []findingForMerge{
		{sourceURLs: &urls},
	}
	if got := sourceDiversity(findings); got != 2 {
		t.Errorf("expected 2 unique domains, got %d", got)
	}
}

func TestSourceDiversity_RegexFallback(t *testing.T) {
	raw := "see https://example.com/a and https://other.org/b"
	findings := []findingForMerge{
		{sourceURLs: &raw},
	}
	if got := sourceDiversity(findings); got != 2 {
		t.Errorf("expected 2 domains (regex fallback), got %d", got)
	}
}

func TestSourceDiversity_NilSource(t *testing.T) {
	findings := []findingForMerge{{}, {}}
	if got := sourceDiversity(findings); got != 0 {
		t.Errorf("expected 0 for nil sources, got %d", got)
	}
}

func TestToInt64_Conversions(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want int64
	}{
		{int64(42), 42},
		{float64(3.7), 3},
		{int(5), 5},
		{"not numeric", 0},
		{nil, 0},
	} {
		if got := toInt64(tc.in); got != tc.want {
			t.Errorf("toInt64(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDeref_NilSentinel(t *testing.T) {
	if got := deref(nil); got != -1 {
		t.Errorf("expected sentinel -1, got %d", got)
	}
	v := int64(7)
	if got := deref(&v); got != 7 {
		t.Errorf("expected 7, got %d", got)
	}
}

func TestInt64PtrFromAny(t *testing.T) {
	got := int64PtrFromAny(int64(99))
	if got == nil || *got != 99 {
		t.Errorf("got %v", got)
	}
	if int64PtrFromAny(nil) != nil {
		t.Errorf("nil input should return nil pointer")
	}
}

// ── clusterFindings ───────────────────────────────────────────────────────────

func TestClusterFindings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		group     []findingForMerge
		threshold float64
		wantLen   int
		wantDupes int
	}{
		{
			name:      "nil group returns empty",
			group:     nil,
			threshold: 0.8,
			wantLen:   0,
			wantDupes: 0,
		},
		{
			name:      "empty group returns empty",
			group:     []findingForMerge{},
			threshold: 0.8,
			wantLen:   0,
			wantDupes: 0,
		},
		{
			name:      "single finding no dupe",
			group:     []findingForMerge{{id: 1, finding: "the sky is blue"}},
			threshold: 0.8,
			wantLen:   1,
			wantDupes: 0,
		},
		{
			name: "identical findings above threshold are deduped",
			group: []findingForMerge{
				{id: 1, finding: "manufacturing cost is fifty dollars per unit"},
				{id: 2, finding: "manufacturing cost is fifty dollars per unit"},
			},
			threshold: 0.8,
			wantLen:   1,
			wantDupes: 1,
		},
		{
			name: "distinct findings below threshold both kept",
			group: []findingForMerge{
				{id: 1, finding: "the product ships from China"},
				{id: 2, finding: "margin is forty seven percent"},
			},
			threshold: 0.5,
			wantLen:   2,
			wantDupes: 0,
		},
		{
			name: "threshold of 1.0 never deduplicates (requires strictly greater than 1.0)",
			group: []findingForMerge{
				{id: 1, finding: "the quick brown fox"},
				{id: 2, finding: "the quick brown fox"},
			},
			// jaccardSimilarity == 1.0 is NOT > 1.0, so nothing is deduped
			threshold: 1.0,
			wantLen:   2,
			wantDupes: 0,
		},
		{
			name: "threshold of 0.0 deduplicates any pair sharing at least one word",
			group: []findingForMerge{
				{id: 1, finding: "cost per unit"},
				{id: 2, finding: "cost breakdown"},
				{id: 3, finding: "delivery schedule"},
			},
			// "cost per unit" kept; "cost breakdown" shares "cost" (Jaccard > 0) → dupe; "delivery schedule" → kept
			threshold: 0.0,
			wantLen:   2,
			wantDupes: 1,
		},
		{
			name: "multiple dupes only first occurrence survives",
			group: []findingForMerge{
				{id: 1, finding: "unit price is eighty nine dollars"},
				{id: 2, finding: "unit price is eighty nine dollars"},
				{id: 3, finding: "unit price is eighty nine dollars"},
			},
			threshold: 0.8,
			wantLen:   1,
			wantDupes: 2,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clusters, dupes := clusterFindings(tc.group, tc.threshold)
			if len(clusters) != tc.wantLen {
				t.Errorf("clusters: want %d, got %d", tc.wantLen, len(clusters))
			}
			if dupes != tc.wantDupes {
				t.Errorf("dupes: want %d, got %d", tc.wantDupes, dupes)
			}
		})
	}
}

// ── detectConflicts ──────────────────────────────────────────────────────────

func TestDetectConflicts_ZeroOrOne(t *testing.T) {
	store := newTestStore(t)

	// nil slice
	if got, err := detectConflicts(store, nil); err != nil || got != 0 {
		t.Errorf("nil: expected 0, got %d", got)
	}
	// empty slice
	if got, err := detectConflicts(store, []findingForMerge{}); err != nil || got != 0 {
		t.Errorf("empty: expected 0, got %d", got)
	}
	// single element — short-circuit, never reaches detectNumericConflicts
	if got, err := detectConflicts(store, []findingForMerge{{id: 1, wave: 1, finding: "price $100"}}); err != nil || got != 0 {
		t.Errorf("single: expected 0, got %d", got)
	}
}

func TestDetectConflicts_Table(t *testing.T) {
	for _, tc := range []struct {
		name      string
		findingA  string
		findingB  string
		wantCount int
	}{
		{
			name:      "divergent dollars inserts conflict",
			findingA:  "unit cost is $40",
			findingB:  "unit cost is $200",
			wantCount: 1,
		},
		{
			name:      "close dollars no conflict",
			findingA:  "price is $100",
			findingB:  "price is $110",
			wantCount: 0,
		},
		{
			name:      "no numbers no conflict",
			findingA:  "product is blue",
			findingB:  "product is red",
			wantCount: 0,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			idA, err := store.Findings().AddFinding(&db.Finding{
				Wave: 1, Agent: "a", MSSLabel: "definition", Finding: tc.findingA,
			})
			if err != nil {
				t.Fatalf("AddFinding A: %v", err)
			}
			idB, err := store.Findings().AddFinding(&db.Finding{
				Wave: 1, Agent: "b", MSSLabel: "definition", Finding: tc.findingB,
			})
			if err != nil {
				t.Fatalf("AddFinding B: %v", err)
			}
			findings := []findingForMerge{
				{id: idA, wave: 1, finding: tc.findingA},
				{id: idB, wave: 1, finding: tc.findingB},
			}
			got, err := detectConflicts(store, findings)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.wantCount {
				t.Errorf("%s: got %d, want %d", tc.name, got, tc.wantCount)
			}
		})
	}
}

// ── MergeFindings ────────────────────────────────────────────────────────────

func TestMergeFindings_EmptyDB(t *testing.T) {
	store := newTestStore(t)
	stats, err := MergeFindings(store, nil, 3, 2, 0.8)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.TotalFindings != 0 {
		t.Errorf("TotalFindings: want 0, got %d", stats.TotalFindings)
	}
	if stats.CoordinateGroups != 0 {
		t.Errorf("CoordinateGroups: want 0, got %d", stats.CoordinateGroups)
	}
	if stats.ConvergenceUpdates == nil {
		t.Error("ConvergenceUpdates should not be nil on empty result")
	}
}

func TestMergeFindings_WaveFilter(t *testing.T) {
	store := newTestStore(t)

	// Wave 1 finding
	_, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "wave-one finding",
	})
	if err != nil {
		t.Fatalf("AddFinding wave1: %v", err)
	}
	// Wave 2 finding
	_, err = store.Findings().AddFinding(&db.Finding{
		Wave: 2, Agent: "b", MSSLabel: "definition", Finding: "wave-two finding",
	})
	if err != nil {
		t.Fatalf("AddFinding wave2: %v", err)
	}

	wave1 := 1
	stats, err := MergeFindings(store, &wave1, 3, 2, 0.8)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.TotalFindings != 1 {
		t.Errorf("TotalFindings with wave filter: want 1, got %d", stats.TotalFindings)
	}
}

func TestMergeFindings_DedupCounting(t *testing.T) {
	store := newTestStore(t)

	// Two near-identical findings from different agents — threshold 0.5 should flag them as dupes.
	for _, agent := range []string{"a", "b"} {
		_, err := store.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: agent, MSSLabel: "definition",
			Finding: "the cost per unit is approximately fifty dollars",
		})
		if err != nil {
			t.Fatalf("AddFinding: %v", err)
		}
	}

	stats, err := MergeFindings(store, nil, 3, 2, 0.5)
	if err != nil {
		t.Fatalf("MergeFindings: %v", err)
	}
	if stats.NearDuplicates < 1 {
		t.Errorf("expected at least 1 duplicate, got %d", stats.NearDuplicates)
	}
}

func TestMergeFindings_ConflictDetection(t *testing.T) {
	store := newTestStore(t)

	// Two findings with divergent dollar amounts (>20%) at the same coordinate.
	for _, tc := range []struct {
		agent, finding string
	}{
		{"a", "manufacturing cost is $50 per unit"},
		{"b", "manufacturing cost is $200 per unit"},
	} {
		_, err := store.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: tc.agent, MSSLabel: "definition", Finding: tc.finding,
		})
		if err != nil {
			t.Fatalf("AddFinding: %v", err)
		}
	}

	stats, err := MergeFindings(store, nil, 3, 2, 0.99)
	if err != nil {
		t.Fatalf("MergeFindings: %v", err)
	}
	if stats.ConflictsDetected < 1 {
		t.Errorf("expected at least 1 conflict, got %d", stats.ConflictsDetected)
	}
}

// TestMergeFindings_ConvergenceFloors pins the floor behaviour: a single agent
// can never earn convergence above "low", regardless of how many distinct source
// domains they cite. effective = min(numAgents, srcDiv) → numAgents=1 caps it.
func TestMergeFindings_ConvergenceFloors(t *testing.T) {
	store := newTestStore(t)
	// Single agent, three distinct domains.
	for i, dom := range []string{"a.com", "b.com", "c.com"} {
		src := fmt.Sprintf(`["https://%s/p"]`, dom)
		if _, err := store.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: "solo", MSSLabel: "definition",
			Finding:    fmt.Sprintf("solo-finding-%d", i),
			SourceURLs: &src,
		}); err != nil {
			t.Fatalf("AddFinding: %v", err)
		}
	}
	stats, err := MergeFindings(store, nil, 3, 2, 0.99)
	if err != nil {
		t.Fatalf("MergeFindings: %v", err)
	}
	// 3 findings in one coord group, all from one agent → effective=min(1,3)=1 → low.
	if stats.ConvergenceUpdates["low"] != 3 {
		t.Errorf("solo agent w/ 3 domains: low=%d (want 3); full=%v",
			stats.ConvergenceUpdates["low"], stats.ConvergenceUpdates)
	}
	if stats.ConvergenceUpdates["high"] != 0 || stats.ConvergenceUpdates["medium"] != 0 {
		t.Errorf("expected only low; got %v", stats.ConvergenceUpdates)
	}
}

// TestMergeFindings_ConvergenceNullSource pins the NULL-source-domain branch:
// findings without source_urls contribute 0 to srcDiv, so even with multiple
// agents, the floor-of-1 keeps the level at "low".
func TestMergeFindings_ConvergenceNullSource(t *testing.T) {
	store := newTestStore(t)
	for _, agent := range []string{"a", "b", "c"} {
		if _, err := store.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: agent, MSSLabel: "definition",
			Finding: fmt.Sprintf("finding-from-%s", agent),
			// SourceURLs intentionally nil
		}); err != nil {
			t.Fatalf("AddFinding(%s): %v", agent, err)
		}
	}
	stats, err := MergeFindings(store, nil, 3, 2, 0.99)
	if err != nil {
		t.Fatalf("MergeFindings: %v", err)
	}
	// 3 agents, srcDiv=0, effective=min(3,0)=0, floor → 1 → low.
	if stats.ConvergenceUpdates["low"] != 3 {
		t.Errorf("3 agents w/ no sources: low=%d (want 3); full=%v",
			stats.ConvergenceUpdates["low"], stats.ConvergenceUpdates)
	}
	if stats.ConvergenceUpdates["high"] != 0 {
		t.Errorf("expected no high; got %v", stats.ConvergenceUpdates)
	}
}

// TestMergeFindings_ConvergenceMalformedURL pins the empty-host fix in
// sourceDiversity: a malformed URL must NOT inflate srcDiv. With 2 agents
// where one cites only a malformed URL, srcDiv = 1 (only the valid one),
// effective = min(2, 1) = 1 → low (not medium).
func TestMergeFindings_ConvergenceMalformedURL(t *testing.T) {
	store := newTestStore(t)
	good := `["https://valid.com/page"]`
	bad := `["not-a-url-at-all"]`
	for i, src := range []string{good, bad} {
		if _, err := store.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: fmt.Sprintf("agent-%d", i), MSSLabel: "definition",
			Finding:    fmt.Sprintf("finding-%d", i),
			SourceURLs: &src,
		}); err != nil {
			t.Fatalf("AddFinding: %v", err)
		}
	}
	stats, err := MergeFindings(store, nil, 3, 2, 0.99)
	if err != nil {
		t.Fatalf("MergeFindings: %v", err)
	}
	// 2 agents but only 1 valid source domain → effective=1 → low.
	// Without the empty-host filter, srcDiv would be 2 and effective=2 → medium (wrong).
	if stats.ConvergenceUpdates["low"] != 2 {
		t.Errorf("2 agents, 1 valid + 1 malformed URL: low=%d (want 2); full=%v",
			stats.ConvergenceUpdates["low"], stats.ConvergenceUpdates)
	}
	if stats.ConvergenceUpdates["medium"] != 0 {
		t.Errorf("malformed URL should NOT inflate to medium; got %v", stats.ConvergenceUpdates)
	}
}

// TestMergeFindings_ConvergenceLevels covers the "high" and "medium"
// convergence levels. effective = min(numAgents, srcDiv), so each finding
// must carry a source URL from a distinct domain to drive srcDiv up.
func TestMergeFindings_ConvergenceLevels(t *testing.T) {
	for _, tc := range []struct {
		name          string
		agents        []string // parallel slice: agent name → source domain
		domains       []string
		minHigh       int
		minMed        int
		wantLevelHigh bool
		wantLevelMed  bool
	}{
		{
			name:          "three agents with three domains → high convergence",
			agents:        []string{"a", "b", "c"},
			domains:       []string{"https://site-a.com/p", "https://site-b.com/p", "https://site-c.com/p"},
			minHigh:       3,
			minMed:        2,
			wantLevelHigh: true,
		},
		{
			name:         "two agents with two domains → medium convergence",
			agents:       []string{"x", "y"},
			domains:      []string{"https://domain-x.com/p", "https://domain-y.com/p"},
			minHigh:      3,
			minMed:       2,
			wantLevelMed: true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			for i, agent := range tc.agents {
				finding := fmt.Sprintf("unique finding from agent %s number %d", agent, i)
				src := fmt.Sprintf(`["%s"]`, tc.domains[i])
				_, err := store.Findings().AddFinding(&db.Finding{
					Wave: 1, Agent: agent, MSSLabel: "definition",
					Finding:    finding,
					SourceURLs: &src,
				})
				if err != nil {
					t.Fatalf("AddFinding(%s): %v", agent, err)
				}
			}

			stats, err := MergeFindings(store, nil, tc.minHigh, tc.minMed, 0.0)
			if err != nil {
				t.Fatalf("MergeFindings: %v", err)
			}
			// Each finding ends up in the same coord group (all default coords),
			// so all len(tc.agents) findings get the same level.
			wantLevel := "low"
			if tc.wantLevelHigh {
				wantLevel = "high"
			} else if tc.wantLevelMed {
				wantLevel = "medium"
			}
			if got := stats.ConvergenceUpdates[wantLevel]; got != len(tc.agents) {
				t.Errorf("expected %s=%d; got %v", wantLevel, len(tc.agents), stats.ConvergenceUpdates)
			}
			// And no findings should land in the other levels.
			for _, other := range []string{"high", "medium", "low"} {
				if other == wantLevel {
					continue
				}
				if stats.ConvergenceUpdates[other] != 0 {
					t.Errorf("unexpected %s findings in %s test: %v", other, wantLevel, stats.ConvergenceUpdates)
				}
			}
		})
	}
}

// TestMergeFindings_SourceURLsSetter: source_urls stored in the DB are read
// back into findingForMerge.sourceURLs.
func TestMergeFindings_SourceURLsSetter(t *testing.T) {
	store := newTestStore(t)
	urls := `["https://example.com/page"]`
	_, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition",
		Finding:    "cost is fifty dollars",
		SourceURLs: &urls,
	})
	if err != nil {
		t.Fatalf("AddFinding: %v", err)
	}

	stats, err := MergeFindings(store, nil, 3, 2, 0.8)
	if err != nil {
		t.Fatalf("MergeFindings: %v", err)
	}
	if stats.TotalFindings != 1 {
		t.Errorf("TotalFindings: want 1, got %d", stats.TotalFindings)
	}
}

// convergence_count is the effective number convergence_level comes from:
// distinct agents, capped by the distinct source domains they cite, so three
// agents citing one source store 1 beside level low.
func TestMergeFindings_StoredCountIsTheEffectiveCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []string // one finding per entry, each from its own agent
	}{
		{"three agents, one source", []string{"https://same.example.com/report", "https://same.example.com/report", "https://same.example.com/report"}},
		{"three agents, two sources", []string{"https://a.example.com/r", "https://b.example.com/r", "https://a.example.com/q"}},
		{"two agents, two sources", []string{"https://a.example.com/r", "https://b.example.com/r"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			d := func(v int) *int { return &v }
			domains := map[string]bool{}
			for i, src := range tc.sources {
				u := fmt.Sprintf(`[%q]`, src)
				domains[strings.SplitN(strings.TrimPrefix(src, "https://"), "/", 2)[0]] = true
				if _, err := store.Findings().AddFinding(&db.Finding{
					Wave: 1, Agent: fmt.Sprintf("agent-%d", i), D1: d(0), D2: d(1),
					MSSLabel: "assumption", Finding: "the unit cost settles near fifty dollars", SourceURLs: &u,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := MergeFindings(store, nil, 3, 2, 0.7); err != nil {
				t.Fatal(err)
			}
			want := min(len(tc.sources), len(domains))
			wantLevel := "low"
			switch {
			case want >= 3:
				wantLevel = "high"
			case want == 2:
				wantLevel = "medium"
			}
			rows, err := store.ReadDB.Query(`SELECT id, convergence_count, convergence_level FROM findings`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var id, count int
				var level string
				if err := rows.Scan(&id, &count, &level); err != nil {
					t.Fatal(err)
				}
				if count != want || level != wantLevel {
					t.Errorf("finding %d: count=%d level=%s; want count=%d level=%s", id, count, level, want, wantLevel)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A merge across waves records a conflict under the later finding's wave,
// where that wave's gate counts it. It used the earlier finding's wave.
func TestMergeFindings_ConflictUnderTheLaterWave(t *testing.T) {
	store := newTestStore(t)
	d := func(v int) *int { return &v }
	for _, f := range []struct {
		wave int
		text string
	}{{1, "unit price is $100 at volume"}, {2, "unit price is $250 at volume"}} {
		if _, err := store.Findings().AddFinding(&db.Finding{
			Wave: f.wave, Agent: fmt.Sprintf("w%d", f.wave), D1: d(0), MSSLabel: "definition", Finding: f.text,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := MergeFindings(store, nil, 3, 2, 0.99); err != nil {
		t.Fatal(err)
	}
	var cw, wa, wb int
	if err := store.ReadDB.QueryRow(`
		SELECT c.wave, fa.wave, fb.wave FROM conflicts c
		JOIN findings fa ON fa.id = c.finding_a_id
		JOIN findings fb ON fb.id = c.finding_b_id`).Scan(&cw, &wa, &wb); err != nil {
		t.Fatal(err)
	}
	if cw != max(wa, wb) {
		t.Errorf("conflict between waves %d and %d recorded under wave %d, want %d", wa, wb, cw, max(wa, wb))
	}
}

// Re-running swarm-merge records each numeric conflict once.
func TestMergeFindings_RecordsConflictsOnce(t *testing.T) {
	store := newTestStore(t)
	d := func(v int) *int { return &v }
	for agent, text := range map[string]string{"a": "unit price is $100 at volume", "b": "unit price is $250 at volume"} {
		if _, err := store.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: agent, D1: d(0), MSSLabel: "definition", Finding: text,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for run, want := range []int{1, 0} {
		stats, err := MergeFindings(store, nil, 3, 2, 0.8)
		if err != nil {
			t.Fatalf("run %d: %v", run+1, err)
		}
		if stats.ConflictsDetected != want {
			t.Fatalf("run %d: %d conflicts recorded, want %d", run+1, stats.ConflictsDetected, want)
		}
	}
	var n int
	if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM conflicts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d conflict rows after two merges, want 1", n)
	}
}
