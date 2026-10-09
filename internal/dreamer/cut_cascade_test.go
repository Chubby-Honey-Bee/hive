package dreamer

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// A guarantee resting on another guarantee with a higher id — what
// promote_finding on an older assumption produces — is flagged in the same
// pass as the guarantee it rests on, so the cascade does not advance one
// level per ripen, and a second ripen over an unchanged comb emits no new
// alarm.
func TestReprove_CascadeReachesEveryDependentInOnePass(t *testing.T) {
	store := freshStore(t)
	a := mustAddFinding(t, store, "assumption", "A", nil)
	p := mustAddFinding(t, store, "assumption", "P", nil)
	g := mustAddFinding(t, store, "guarantee", "G follows from A", []int64{a})
	if _, err := store.WriteDB.Exec(
		`UPDATE findings SET mss_label = 'guarantee', depends_on_ids = ? WHERE id = ?`,
		fmt.Sprintf("[%d]", g), p,
	); err != nil {
		t.Fatalf("promote %d: %v", p, err)
	}
	// One-second timestamps: the edit must land after the guarantees.
	time.Sleep(1100 * time.Millisecond)
	if err := store.Findings().UpdateFinding(a, map[string]any{"finding": "A, edited"}); err != nil {
		t.Fatalf("edit %d: %v", a, err)
	}

	// Every guarantee resting on the edited finding, directly or through
	// another guarantee, is flagged once, and only once.
	resting := []int64{g, p}
	for run := 1; run <= 2; run++ {
		if _, err := passReprove(context.Background(), store, DefaultOptions()); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if got := countSignals(t, store, "alarm"); got != len(resting) {
			t.Fatalf("after run %d: %d alarm signals, want %d", run, got, len(resting))
		}
	}
}

// A dependency's alarm is written before its dependent's, so a second pass
// finds nothing new however the writes of the first fall across second
// boundaries. P rests on an edited X and on a higher-id guarantee G, so P is
// flagged on its own, after G.
func TestReprove_SecondPassAcrossSecondBoundariesIsANoOp(t *testing.T) {
	store := freshStore(t)
	x := mustAddFinding(t, store, "assumption", "X", nil)
	a := mustAddFinding(t, store, "assumption", "A", nil)
	p := mustAddFinding(t, store, "assumption", "P", nil)
	g := mustAddFinding(t, store, "guarantee", "G follows from A", []int64{a})
	if _, err := store.WriteDB.Exec(
		`UPDATE findings SET mss_label = 'guarantee', depends_on_ids = ? WHERE id = ?`,
		fmt.Sprintf("[%d,%d]", x, g), p,
	); err != nil {
		t.Fatalf("promote %d: %v", p, err)
	}
	time.Sleep(1100 * time.Millisecond)
	for _, id := range []int64{x, a} {
		if err := store.Findings().UpdateFinding(id, map[string]any{"finding": "edited"}); err != nil {
			t.Fatalf("edit %d: %v", id, err)
		}
	}

	resting := []int64{g, p}
	if _, err := passReprove(context.Background(), store, DefaultOptions()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if got := countSignals(t, store, "alarm"); got != len(resting) {
		t.Fatalf("after the first pass: %d alarm signals, want %d", got, len(resting))
	}
	// The worst timing: each write of the pass lands a second or more after
	// the one before it, in the order the pass wrote them.
	if _, err := store.WriteDB.Exec(
		`UPDATE signals SET created_at = datetime(created_at, '+' || id || ' seconds') WHERE signal_type = 'alarm'`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := passReprove(context.Background(), store, DefaultOptions()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if got := countSignals(t, store, "alarm"); got != len(resting) {
		t.Fatalf("after the second pass: %d alarm signals, want %d", got, len(resting))
	}
}

// cutAfter is a context whose Err reports a deadline once n checks have
// passed — a pass deadline expiring part-way through its rows.
type cutAfter struct {
	context.Context
	n int
}

func (c *cutAfter) Err() error {
	if c.n > 0 {
		c.n--
		return nil
	}
	return context.DeadlineExceeded
}

// A pass cut part-way still reports the rows it acted on as touched.
func TestCutPass_ReportsTheRowsItActedOn(t *testing.T) {
	type setup func(t *testing.T, store *db.Store)
	twoLaundered := func(t *testing.T, store *db.Store) {
		for i := 0; i < 2; i++ {
			dep := mustAddFinding(t, store, "definition", fmt.Sprintf("fact %d", i), nil)
			unk := mustAddFinding(t, store, "unknown", fmt.Sprintf("unknown %d", i), nil)
			gid := mustAddFinding(t, store, "guarantee", fmt.Sprintf("claim %d", i), []int64{dep})
			deps, _ := json.Marshal([]int64{dep, unk})
			if _, err := store.WriteDB.Exec(`UPDATE findings SET depends_on_ids = ? WHERE id = ?`, string(deps), gid); err != nil {
				t.Fatal(err)
			}
		}
	}
	twoEdited := func(t *testing.T, store *db.Store) {
		var deps []int64
		for i := 0; i < 2; i++ {
			dep := mustAddFinding(t, store, "definition", fmt.Sprintf("fact %d", i), nil)
			mustAddFinding(t, store, "guarantee", fmt.Sprintf("claim %d", i), []int64{dep})
			deps = append(deps, dep)
		}
		time.Sleep(1100 * time.Millisecond)
		for _, dep := range deps {
			if err := store.Findings().UpdateFinding(dep, map[string]any{"finding": "edited"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	twoDuplicatePairs := func(t *testing.T, store *db.Store) {
		for _, s := range []string{
			"the unit draws zero point three watts under normal load condition",
			"the unit draws zero point three watts under normal load conditions",
			"orders ship from the eastern warehouse every tuesday morning before noon",
			"orders ship from the eastern warehouse every tuesday morning before noon today",
		} {
			mustAddFinding(t, store, "assumption", s, nil)
		}
	}
	twoOldGaps := func(t *testing.T, store *db.Store) {
		mustAddGap(t, store, "first open question", "critical")
		mustAddGap(t, store, "second open question", "critical")
		if _, err := store.WriteDB.Exec(`UPDATE gaps SET created_at = datetime('now', '-30 days')`); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		pass  func(context.Context, *db.Store, Options) (Result, error)
		setup setup
	}{
		{"settle", passSettle, twoLaundered},
		{"reprove", passReprove, twoEdited},
		{"prune", passPrune, twoDuplicatePairs},
		{"hypothesize", passHypothesize, twoOldGaps},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := freshStore(t)
			tc.setup(t, store)
			const before = 1 // rows the pass may act on before the cut
			res, err := tc.pass(&cutAfter{Context: context.Background(), n: before}, store, DefaultOptions())
			if err == nil {
				t.Fatal("pass ran to completion; want the deadline error")
			}
			if res.Touched != before {
				t.Errorf("touched %d, want the %d row(s) acted on before the cut", res.Touched, before)
			}
		})
	}
}
