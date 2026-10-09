package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// synthesize renders region digests only: a forager verdict appears neither
// under "Contested regions" nor in the per-region table, and the printed
// count leaves it out.
func TestCombSynthesize_RendersRegionsOnly(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "synth.db"))
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

	rows := []*db.CombRow{
		{VantageKey: "", VantageKind: db.VantageRegion, Narrative: "global voice", Confidence: 70},
		{VantageKey: "d1=0", VantageKind: db.VantageRegion, Narrative: "region zero", Confidence: 60, Contested: true},
		{VantageKey: "d1=1", VantageKind: db.VantageRegion, Narrative: "region one", Confidence: 90},
		{VantageKey: "forager:optimist", VantageKind: db.VantageForager, Narrative: "ship it", Confidence: 50, Contested: true},
	}
	regions := 0
	for _, r := range rows {
		if err := s.Comb().Upsert(r); err != nil {
			t.Fatal(err)
		}
		if r.VantageKind == db.VantageRegion {
			regions++
		}
	}

	path := filepath.Join(t.TempDir(), "synth.md")
	var out bytes.Buffer
	cmd := newCombSynthesizeCmd()
	cmd.SetArgs([]string{"--out", path})
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if want := fmt.Sprintf("(%d regions)", regions); !strings.Contains(out.String(), want) {
		t.Errorf("output %q; want it to report %s", out.String(), want)
	}
	md, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.VantageKind == db.VantageForager && strings.Contains(string(md), r.VantageKey) {
			t.Errorf("synthesis renders forager vantage %q as a region:\n%s", r.VantageKey, md)
		}
		if r.VantageKind == db.VantageRegion && r.VantageKey != "" && !strings.Contains(string(md), "`"+r.VantageKey+"`") {
			t.Errorf("synthesis is missing region %q:\n%s", r.VantageKey, md)
		}
	}
}

// synthesize's Stale column calls a region stale by comb.md § Staleness, as
// comb query does, so a region written to after its refresh reads stale in
// both.
func TestCombSynthesize_StaleByCombQuerysRule(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "synth.db"))
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

	add := func(d1, d2 int, createdAt string) {
		t.Helper()
		if _, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, mss_label, finding, created_at)
			VALUES (1, 'test', ?, ?, 'assumption', 'x', `+createdAt+`)`, d1, d2); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range [][2]int{{0, 1}, {1, 0}, {2, 0}, {3, 0}} {
		add(d[0], d[1], "CURRENT_TIMESTAMP")
	}
	if _, err := comb.BuildAllRegions(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	add(0, 1, "datetime('now', '+1 hour')")
	add(2, 5, "(SELECT MIN(last_revised_at) FROM comb_state)")

	rows, err := s.Comb().ListKind(db.VantageRegion)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	byRule, fresh := 0, 0
	for _, r := range rows {
		region, err := comb.ParseRegionKey(r.VantageKey)
		if err != nil {
			t.Fatal(err)
		}
		c, err := comb.Classify(s, region)
		if err != nil {
			t.Fatal(err)
		}
		want[r.VantageKey] = c == comb.StaleStale
		if want[r.VantageKey] {
			byRule++
		} else {
			fresh++
		}
	}
	if byRule == 0 || fresh == 0 {
		t.Fatalf("regions: %d stale by the rule, %d fresh; the fixture must hold both", byRule, fresh)
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
		stale, ok := want[key]
		if !ok {
			t.Errorf("synthesis renders %q, which is no region", key)
			continue
		}
		if got := cells[len(cells)-1]; got != fmt.Sprint(stale) {
			t.Errorf("%q: Stale column %s; comb query's rule says %v", key, got, stale)
		}
		checked++
	}
	if checked != len(rows)-1 {
		t.Errorf("synthesis table has %d rows; want %d, every region but the global one", checked, len(rows)-1)
	}
}
