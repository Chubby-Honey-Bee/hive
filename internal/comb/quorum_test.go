package comb

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func quorumStore(t *testing.T) *db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quorum.db")
	store, err := db.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestQuorumSensor_FiresNablaOnConvergence(t *testing.T) {
	store := quorumStore(t)
	bus := NewEventBus()
	q := NewQuorumSensor(store, bus)
	q.Register("optimist", "pragmatist")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	q.Start(ctx)

	// Both foragers write congruent verdicts → ∇ fires.
	bus.Publish(ctx, Event{
		Kind:        EventVantageWritten,
		VantageKey:  "forager:optimist",
		VantageKind: string(db.VantageForager),
		Payload:     map[string]any{"forager": "optimist", "verdict": "support"},
	})
	bus.Publish(ctx, Event{
		Kind:        EventVantageWritten,
		VantageKind: string(db.VantageForager),
		VantageKey:  "forager:pragmatist",
		Payload:     map[string]any{"forager": "pragmatist", "verdict": "support"},
	})

	// Allow the sensor goroutine to process both events + emit.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var n int
		_ = store.ReadConn().QueryRow(
			`SELECT COUNT(*) FROM signals WHERE signal_type='nabla'`,
		).Scan(&n)
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected nabla signal to be emitted after convergence")
}

func TestQuorumSensor_DivergenceDoesNotFire(t *testing.T) {
	store := quorumStore(t)
	bus := NewEventBus()
	q := NewQuorumSensor(store, bus)
	q.Register("optimist", "skeptic")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	q.Start(ctx)

	bus.Publish(ctx, Event{
		Kind:        EventVantageWritten,
		VantageKind: string(db.VantageForager),
		Payload:     map[string]any{"forager": "optimist", "verdict": "support"},
	})
	bus.Publish(ctx, Event{
		Kind:        EventVantageWritten,
		VantageKind: string(db.VantageForager),
		Payload:     map[string]any{"forager": "skeptic", "verdict": "oppose"},
	})

	time.Sleep(200 * time.Millisecond)
	var n int
	_ = store.ReadConn().QueryRow(
		`SELECT COUNT(*) FROM signals WHERE signal_type='nabla'`,
	).Scan(&n)
	if n != 0 {
		t.Fatalf("∇ should not fire on divergence; got %d signals", n)
	}
}

func TestQuorumSensor_SetRunID(t *testing.T) {
	tests := []struct {
		name string
		id   int64
	}{
		{"positive", 42},
		{"zero_reset", 0},
		{"large", 9_999_999},
		{"negative", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := quorumStore(t)
			bus := NewEventBus()
			q := NewQuorumSensor(store, bus)
			q.SetRunID(tc.id)
			q.mu.Lock()
			got := q.runID
			q.mu.Unlock()
			if got != tc.id {
				t.Errorf("SetRunID(%d): runID = %d, want %d", tc.id, got, tc.id)
			}
		})
	}
}

// TestQuorumSensor_SetRunID_PropagatesToBond verifies that the run ID
// set via SetRunID reaches the forager_bonds row written when a ∇ fires.
// forager_bonds.run_id is a FK to workflow_runs(id), so we create a real
// workflow run first to satisfy the constraint.
func TestQuorumSensor_SetRunID_PropagatesToBond(t *testing.T) {
	store := quorumStore(t)

	// Create a workflow_runs row so the FK constraint is satisfied.
	runID, err := store.Workflows().CreateWorkflowRun("test-quorum", 1, "{}", "{}", nil)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}

	bus := NewEventBus()
	q := NewQuorumSensor(store, bus)
	q.Register("optimist", "pragmatist")
	q.SetRunID(runID)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	q.Start(ctx)

	bus.Publish(ctx, Event{
		Kind:        EventVantageWritten,
		VantageKey:  "forager:optimist",
		VantageKind: string(db.VantageForager),
		Payload:     map[string]any{"forager": "optimist", "verdict": "support"},
	})
	bus.Publish(ctx, Event{
		Kind:        EventVantageWritten,
		VantageKey:  "forager:pragmatist",
		VantageKind: string(db.VantageForager),
		Payload:     map[string]any{"forager": "pragmatist", "verdict": "support"},
	})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		bonds, err := store.ForagerBonds().ListByRun(runID)
		if err != nil {
			t.Fatalf("ListByRun: %v", err)
		}
		if len(bonds) > 0 {
			if !bonds[0].Fired {
				t.Fatalf("∇ bond row written with fired=0; the sensor only writes a row when the bond has fired")
			}
			return // bond written with the expected run_id and marked fired
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("forager_bonds row with run_id=%d was never written", runID)
}

func TestQuorumSensor_DedupesRepeatedConvergence(t *testing.T) {
	store := quorumStore(t)
	bus := NewEventBus()
	q := NewQuorumSensor(store, bus)
	q.Register("optimist", "pragmatist")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	q.Start(ctx)

	for i := 0; i < 4; i++ {
		bus.Publish(ctx, Event{
			Kind:        EventVantageWritten,
			VantageKind: string(db.VantageForager),
			Payload:     map[string]any{"forager": "optimist", "verdict": "support"},
		})
		bus.Publish(ctx, Event{
			Kind:        EventVantageWritten,
			VantageKind: string(db.VantageForager),
			Payload:     map[string]any{"forager": "pragmatist", "verdict": "support"},
		})
	}

	time.Sleep(300 * time.Millisecond)
	var n int
	_ = store.ReadConn().QueryRow(
		`SELECT COUNT(*) FROM signals WHERE signal_type='nabla'`,
	).Scan(&n)
	if n != 1 {
		t.Fatalf("expected exactly 1 nabla (deduped), got %d", n)
	}
}

// The bus is shared by every run in the process, so a sensor belonging to
// run 1 ignores run 2's verdicts: a forager_bonds row stamped with run 1 for
// them would be a provenance record naming a run that did not produce the
// bond.
func TestQuorumSensor_IgnoresAnotherRunsVerdicts(t *testing.T) {
	store := quorumStore(t)
	bus := NewEventBus()
	q := NewQuorumSensor(store, bus)
	q.Register("optimist", "pragmatist")
	q.SetRunID(1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	q.Start(ctx)

	for _, f := range []string{"optimist", "pragmatist"} {
		bus.Publish(ctx, Event{
			Kind:        EventVantageWritten,
			VantageKey:  "forager:" + f,
			VantageKind: string(db.VantageForager),
			RunID:       2, // a different run
			Payload:     map[string]any{"forager": f, "verdict": "support"},
		})
	}
	time.Sleep(300 * time.Millisecond)

	var n int
	_ = store.ReadConn().QueryRow(`SELECT COUNT(*) FROM signals WHERE signal_type='nabla'`).Scan(&n)
	if n != 0 {
		t.Errorf("run 1's sensor fired %d ∇ on run 2's verdicts", n)
	}
}

func TestVerdictsConverge(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"support", "support", true},
		{"oppose", "oppose", true},
		{"conditional", "conditional", true},
		{"support", "oppose", false},
		{"support", "conditional", false},
		{"abstain", "abstain", false}, // abstain never converges
		{"support", "abstain", false},
		{"abstain", "support", false},
		{"", "support", false},
		{"support", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := verdictsConverge(tc.a, tc.b); got != tc.want {
			t.Errorf("verdictsConverge(%q,%q) = %v; want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestPairKey_OrderInsensitive(t *testing.T) {
	if pairKey("a", "b") != pairKey("b", "a") {
		t.Error("pairKey not order-insensitive")
	}
}

func TestPairKey_Format(t *testing.T) {
	got := pairKey("optimist", "skeptic")
	if got != "optimist↔skeptic" {
		t.Errorf("pairKey = %q; want optimist↔skeptic", got)
	}
}

// ConvergedPairs applies the sensor's rule to a pair list: a pair fires
// when both verdicts are present, equal and not abstain. Each fired pair
// appears once, written with the lesser name first, and the list is sorted
// whatever order the pairs came in.
func TestConvergedPairs(t *testing.T) {
	verdicts := map[string]string{
		"a": "support", "b": "support", "c": "abstain", "d": "abstain",
		"e": "conditional", "f": "conditional", "g": "oppose", "h": "",
	}
	pairs := [][2]string{
		{"b", "a"}, {"a", "b"}, // one pair, both orders
		{"c", "d"}, // abstain never converges
		{"f", "e"}, // conditional with conditional
		{"a", "g"}, // different verdicts
		{"h", "x"}, // an empty verdict and a missing one
		{"x", "y"}, // both missing
	}
	var want [][2]string
	for _, p := range pairs {
		a, b := min(p[0], p[1]), max(p[0], p[1])
		va, vb := verdicts[a], verdicts[b]
		if va != "" && va != "abstain" && va == vb && !slices.Contains(want, [2]string{a, b}) {
			want = append(want, [2]string{a, b})
		}
	}
	slices.SortFunc(want, func(x, y [2]string) int {
		if x[0] != y[0] {
			return strings.Compare(x[0], y[0])
		}
		return strings.Compare(x[1], y[1])
	})

	got := ConvergedPairs(pairs, verdicts)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConvergedPairs = %v, want %v", got, want)
	}
	reversed := slices.Clone(pairs)
	slices.Reverse(reversed)
	if again := ConvergedPairs(reversed, verdicts); !reflect.DeepEqual(again, got) {
		t.Errorf("reversed input gives %v, want %v", again, got)
	}
}

// A panic while the sensor handles a verdict stops the sensor, logged, and
// leaves the process running — the run, or chb-mcp with every request it
// serves. A store with no database behind it makes the ∇ write panic.
func TestQuorumSensor_APanicStopsTheSensorAlone(t *testing.T) {
	bus := NewEventBus()
	q := NewQuorumSensor(&db.Store{}, bus)
	q.Register("optimist", "pragmatist")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := q.Start(ctx)
	for _, name := range []string{"optimist", "pragmatist"} {
		bus.Publish(ctx, Event{
			Kind:        EventVantageWritten,
			VantageKind: string(db.VantageForager),
			VantageKey:  "forager:" + name,
			Payload:     map[string]any{"forager": name, "verdict": "support"},
		})
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the sensor is still running after the panic, or never handled the verdicts")
	}
}
