package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// staleByDefinition is comb.md § Staleness for one row, from its definition:
// a region is stale when a refresh would write it a different narrative,
// confidence, contested flag, dominant label, evidence count or
// open-question count; any other vantage is never stale.
func staleByDefinition(t *testing.T, s *db.Store, key string) bool {
	t.Helper()
	row, err := s.Comb().Get(key)
	if err != nil || row == nil {
		t.Fatalf("%q: row %v, %v", key, row, err)
	}
	region, err := comb.ParseRegionKey(key)
	if err != nil {
		return false
	}
	d, err := comb.BuildDigest(s, region)
	if err != nil {
		t.Fatal(err)
	}
	dom := sql.NullString{String: d.DominantLabel, Valid: d.DominantLabel != ""}
	return row.Narrative != d.Narrative || row.Confidence != d.Confidence || row.Contested != d.Contested ||
		row.DominantLabel != dom || row.EvidenceCount != d.EvidenceCount || row.OpenQuestionsCount != d.OpenQuestionsCount
}

// seedStalenessClauses refreshes the Comb, then applies one change per
// clause of comb.md § Staleness, each under its own d1. It returns the region
// keys each clause must make stale, and those that must stay fresh.
func seedStalenessClauses(t *testing.T, s *db.Store) (stale map[string][]string, fresh map[string]string) {
	t.Helper()
	add := func(label string, d1, d2 int) int64 {
		t.Helper()
		res, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, mss_label, finding) VALUES (1, 'test', ?, ?, ?, 'x')`, d1, d2, label)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	last := func(table string) int64 {
		t.Helper()
		var id int64
		if err := s.ReadDB.QueryRow(`SELECT MAX(id) FROM ` + table).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	p := func(v int) *int { return &v }

	add("assumption", 0, 0)
	c1a, c1b := add("assumption", 1, 0), add("assumption", 1, 1)
	g2 := add("assumption", 2, 0)
	if err := s.Gaps().AddGap(1, "test", "gap", "critical", p(2), p(0), nil, nil); err != nil {
		t.Fatal(err)
	}
	gap2 := last("gaps")
	d3 := add("assumption", 3, 0)
	g3 := add("assumption", 3, 1)
	if _, err := s.WriteDB.Exec(`UPDATE findings SET mss_label = 'guarantee', depends_on_ids = ? WHERE id = ?`, fmt.Sprintf("[%d]", d3), g3); err != nil {
		t.Fatal(err)
	}
	l4 := add("assumption", 4, 0)
	add("assumption", 4, 1)
	add("assumption", 5, 0)
	capped := add("assumption", 6, 0)
	r7a, r7b := add("assumption", 7, 0), add("assumption", 7, 1)
	if err := s.Conflicts().AddConflict(1, r7a, r7b, "disagree"); err != nil {
		t.Fatal(err)
	}
	conflict7 := last("conflicts")
	if _, err := comb.BuildAllRegions(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	if err := s.Conflicts().AddConflict(2, c1a, c1b, "disagree"); err != nil {
		t.Fatal(err)
	}
	if err := s.Gaps().ResolveGap(gap2, 2, "test", g2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CascadeRevert(d3); err != nil {
		t.Fatal(err)
	}
	if err := s.Findings().UpdateFinding(l4, map[string]any{"mss_label": "definition"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Followups().AddFollowup(2, "test", "q", "important", p(5), p(0), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDB.Exec(`INSERT INTO capped_findings (finding_id, capped_at) VALUES (?, datetime('now', '+1 hour'))`, capped); err != nil {
		t.Fatal(err)
	}
	if err := s.Conflicts().Resolve(conflict7, 2, "b is right"); err != nil {
		t.Fatal(err)
	}
	staleBy := map[string][]string{
		"a conflict opened after the digest":   {"d1=1", "d1=1;d2=0", "d1=1;d2=1"},
		"a gap resolved after it":              {"d1=2", "d1=2;d2=0"},
		"a label reverted by the cascade":      {"d1=3", "d1=3;d2=1"},
		"a label changed after it":             {"d1=4", "d1=4;d2=0"},
		"a followup opened after it":           {"d1=5", "d1=5;d2=0"},
		"a conflict resolved after the digest": {"d1=7", "d1=7;d2=0", "d1=7;d2=1"},
	}
	freshBy := map[string]string{
		"d1=0":      "untouched",
		"d1=3;d2=0": "a label reverted beside it",
		"d1=4;d2=1": "a label changed beside it",
		"d1=6":      "a cap",
		"d1=6;d2=0": "a cap",
	}
	return staleBy, freshBy
}

// comb query, comb status and its --json, and comb synthesize call a region
// stale when a conflict, a gap, a followup or a label changed after its
// refresh, and not when a finding in it was capped: each follows comb.md §
// Staleness, row by row.
func TestCombCLI_StalenessFollowsConflictsGapsAndLabels(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "stale.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	prev := store
	store = s
	t.Cleanup(func() { store = prev })

	staleKeys, freshKeys := seedStalenessClauses(t, s)
	for clause, keys := range staleKeys {
		for _, key := range keys {
			if !staleByDefinition(t, s, key) {
				t.Errorf("fixture: %s leaves %q fresh; want stale", clause, key)
			}
		}
	}
	for key, why := range freshKeys {
		if staleByDefinition(t, s, key) {
			t.Errorf("fixture: %q (%s) is stale; want fresh", key, why)
		}
	}

	rows, err := s.Comb().List()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	n := 0
	for _, r := range rows {
		want[r.VantageKey] = staleByDefinition(t, s, r.VantageKey)
		if want[r.VantageKey] {
			n++
		}
	}

	for _, r := range rows {
		region, err := comb.ParseRegionKey(r.VantageKey)
		if err != nil {
			t.Fatal(err)
		}
		var args []string
		for _, d := range []string{"d1", "d2"} {
			if v, ok := region[d]; ok {
				args = append(args, "--"+d, fmt.Sprint(v))
			}
		}
		cmd := newCombQueryCmd()
		cmd.SetArgs(append(args, "--json"))
		out, err := captureStdout(t, func() error { return cmd.ExecuteContext(context.Background()) })
		if err != nil {
			t.Fatalf("comb query %v: %v", args, err)
		}
		var got struct {
			Staleness comb.Staleness `json:"staleness"`
			CoveredBy string         `json:"covered_by"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("comb query %v: %v in %q", args, err, out)
		}
		wantS := comb.StaleFresh
		if want[r.VantageKey] {
			wantS = comb.StaleStale
		}
		if got.Staleness != wantS || got.CoveredBy != "" {
			t.Errorf("comb query %v: staleness %q covered by %q; want %q", args, got.Staleness, got.CoveredBy, wantS)
		}
	}

	status := func(args ...string) string {
		t.Helper()
		cmd := newCombStatusCmd()
		cmd.SetArgs(args)
		out, err := captureStdout(t, func() error { return cmd.ExecuteContext(context.Background()) })
		if err != nil {
			t.Fatalf("comb status %v: %v", args, err)
		}
		return out
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(status("--json")), &got); err != nil {
		t.Fatal(err)
	}
	if got["stale"] != float64(n) {
		t.Errorf("status --json: stale %v; want %d", got["stale"], n)
	}
	if text := status(); !strings.Contains(text, fmt.Sprintf(" stale=%d ", n)) {
		t.Errorf("status = %q; want stale=%d", text, n)
	}

	var out bytes.Buffer
	cmd := newCombSynthesizeCmd()
	cmd.SetArgs(nil)
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	checked := 0
	for _, line := range strings.Split(out.String(), "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "| "), " | ")
		key := strings.Trim(cells[0], "`")
		if got := cells[len(cells)-1]; got != fmt.Sprint(want[key]) {
			t.Errorf("synthesize %q: Stale column %s; want %v", key, got, want[key])
		}
		checked++
	}
	if checked != len(rows)-1 {
		t.Errorf("synthesis table has %d rows; want %d, every region but the global one", checked, len(rows)-1)
	}
}

// A region `comb refresh --region` just wrote is fresh, by the rule and by
// its definition, whether it holds findings, a conflict and a gap, no
// finding, or pins an axis past d4. A full refresh leaves the global region
// fresh. The CLI writes through the refresh's own row converter, so a region
// with no findings, which no full refresh rebuilds, is not left stale.
func TestCombCLI_RegionRefreshIsFresh(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "refresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	prev := store
	store = s
	t.Cleanup(func() { store = prev })
	var ids []int64
	for _, q := range []string{
		`INSERT INTO findings (wave, agent, d1, mss_label, finding) VALUES (1, 't', 0, 'definition', 'x')`,
		`INSERT INTO findings (wave, agent, d1, d2, d5, mss_label, finding) VALUES (1, 't', 0, 1, 2, 'assumption', 'y')`,
	} {
		res, err := s.WriteDB.Exec(q)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	if err := s.Conflicts().AddConflict(1, ids[0], ids[1], "disagree"); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := s.Gaps().AddGap(1, "t", "gap", "critical", &zero, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"d1=0", "d1=9", "d1=0;d5=2", ""} {
		cmd := newCombRefreshCmd()
		cmd.SetArgs([]string{"--region", key})
		if key == "" {
			cmd.SetArgs(nil)
		}
		if _, err := captureStdout(t, func() error { return cmd.ExecuteContext(context.Background()) }); err != nil {
			t.Fatalf("comb refresh --region %q: %v", key, err)
		}
		if staleByDefinition(t, s, key) {
			t.Errorf("%q after comb refresh --region: stale by the definition; want fresh", key)
		}
		region, err := comb.ParseRegionKey(key)
		if err != nil {
			t.Fatal(err)
		}
		if c, err := comb.Classify(s, region); err != nil || c != comb.StaleFresh {
			t.Errorf("%q after comb refresh --region: Classify %q %v; want fresh", key, c, err)
		}
	}
}

// `comb query` on a region with no row of its own reports the covering
// region's digest and that region's staleness by the rule, not `missing`.
func TestCombCLI_QueryFallbackStaleness(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "fallback.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	prev := store
	store = s
	t.Cleanup(func() { store = prev })
	seedStalenessClauses(t, s)

	seen := map[bool]bool{}
	for _, c := range []struct {
		args     []string
		covering string
	}{
		{[]string{"--d1", "1", "--d2", "0", "--d3", "7"}, "d1=1;d2=0"},
		{[]string{"--d1", "0", "--d2", "0", "--d3", "7", "--d4", "2"}, "d1=0;d2=0"},
		{[]string{"--d1", "0", "--d2", "8"}, "d1=0"},
		{[]string{"--d1", "99"}, ""},
	} {
		want := staleByDefinition(t, s, c.covering)
		seen[want] = true
		cmd := newCombQueryCmd()
		cmd.SetArgs(append(c.args, "--json"))
		out, err := captureStdout(t, func() error { return cmd.ExecuteContext(context.Background()) })
		if err != nil {
			t.Fatalf("comb query %v: %v", c.args, err)
		}
		var got struct {
			Found     bool           `json:"found"`
			Staleness comb.Staleness `json:"staleness"`
			CoveredBy string         `json:"covered_by"`
			Narrative string         `json:"narrative"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("comb query %v: %v in %q", c.args, err, out)
		}
		row, err := s.Comb().Get(c.covering)
		if err != nil || row == nil {
			t.Fatalf("%q: %v %v", c.covering, row, err)
		}
		wantS := comb.StaleFresh
		if want {
			wantS = comb.StaleStale
		}
		if !got.Found || got.CoveredBy != c.covering || got.Narrative != row.Narrative || got.Staleness != wantS {
			t.Errorf("comb query %v: found %v, covered by %q, staleness %q; want covered by %q, %q",
				c.args, got.Found, got.CoveredBy, got.Staleness, c.covering, wantS)
		}
	}
	if !seen[true] || !seen[false] {
		t.Errorf("the covering regions are stale %v; the cases must hold a stale and a fresh one", seen)
	}
}
