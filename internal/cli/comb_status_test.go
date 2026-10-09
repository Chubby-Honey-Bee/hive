package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// comb status counts the vantages stale by comb query's rule (comb.md §
// Staleness): a region as comb.Classify calls it, and a forager never. A
// region written to after its refresh counts.
func TestCombStatus_CountsStaleByCombQuerysRule(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "status.db"))
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
	for _, d := range [][2]int{{0, 1}, {1, 0}, {2, 0}} {
		add(d[0], d[1], "CURRENT_TIMESTAMP")
	}
	if _, err := comb.BuildAllRegions(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"forager:optimist", "forager:skeptic"} {
		if err := s.Comb().Upsert(&db.CombRow{VantageKey: key, VantageKind: db.VantageForager, EvidenceCount: 2}); err != nil {
			t.Fatal(err)
		}
	}
	add(0, 1, "datetime('now', '+1 hour')")
	add(2, 5, "(SELECT MIN(last_revised_at) FROM comb_state)")

	rows, err := s.Comb().List()
	if err != nil {
		t.Fatal(err)
	}
	want, regions := 0, 0
	for _, r := range rows {
		if comb.IsForagerVantage(r.VantageKey) {
			continue
		}
		regions++
		region, err := comb.ParseRegionKey(r.VantageKey)
		if err != nil {
			t.Fatal(err)
		}
		c, err := comb.Classify(s, region)
		if err != nil {
			t.Fatal(err)
		}
		if c == comb.StaleStale {
			want++
		}
	}
	if want == 0 || want == regions {
		t.Fatalf("%d of %d regions stale by the rule; the fixture must hold stale and fresh regions", want, regions)
	}

	run := func(args ...string) string {
		cmd := newCombStatusCmd()
		cmd.SetArgs(args)
		out, err := captureStdout(t, func() error { return cmd.ExecuteContext(context.Background()) })
		if err != nil {
			t.Fatalf("comb status %v: %v", args, err)
		}
		return out
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(run("--json")), &got); err != nil {
		t.Fatal(err)
	}
	if got["stale"] != float64(want) {
		t.Errorf("status --json: stale %v; want %d", got["stale"], want)
	}
	if text := run(); !strings.Contains(text, fmt.Sprintf(" stale=%d ", want)) {
		t.Errorf("status = %q; want stale=%d", text, want)
	}
}
