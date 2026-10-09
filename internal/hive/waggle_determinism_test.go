package hive

import (
	"reflect"
	"testing"
)

// Waggle-dance targets were picked by ranging over a Go map, whose iteration
// order is randomised — so the same database produced different dispatch
// coordinates run to run, and a plan nobody can reproduce is a plan nobody can
// review.
func TestEvalWaggleDance_IsDeterministic(t *testing.T) {
	store := newTestStore(t)
	initProject(t, store, "p")
	// Several findings at the same d1 with high convergence, and several
	// unresolved gaps at distinct coordinates under it — so the pairing has
	// more than one legal answer and a random one is visible.
	for i := 0; i < 4; i++ {
		id := addFinding(t, store, "definition", "converged", 1)
		if _, err := store.WriteDB.Exec(
			`UPDATE findings SET convergence_level='high' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		if _, err := store.WriteDB.Exec(
			`INSERT INTO gaps (wave, agent, description, priority, d1, d2, d3, d4)
			 VALUES (1,'test',?,'important',1,?,?,?)`,
			"gap", i, i*2, i*3,
		); err != nil {
			t.Fatal(err)
		}
	}

	var first []Signal
	for i := 0; i < 12; i++ {
		state, err := ScanState(store, "p")
		if err != nil {
			t.Fatal(err)
		}
		got := evalWaggleDance(store, state)
		if i == 0 {
			first = got
			continue
		}
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("run %d produced different waggle targets:\nfirst: %+v\nnow:   %+v", i, first, got)
		}
	}
}
