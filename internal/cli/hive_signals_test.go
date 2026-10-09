package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// `hive next` records what its plan read, and `chb hive complete` consumes
// that far: a signal written while the iteration's action runs stays pending
// for the next plan.
func TestHiveComplete_ConsumesOnlyWhatNextRead(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "hive.db"))
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

	run := func(args ...string) {
		t.Helper()
		root := newHiveCmd()
		root.SetArgs(args)
		root.SetOut(io.Discard)
		stdout := os.Stdout
		os.Stdout, _ = os.Open(os.DevNull)
		defer func() { os.Stdout = stdout }()
		if err := root.Execute(); err != nil {
			t.Fatalf("hive %v: %v", args, err)
		}
	}
	signal := func() int64 {
		t.Helper()
		res, err := s.WriteDB.Exec(`INSERT INTO signals (signal_type, source_type, payload_json, wave) VALUES ('stop_signal','system','{}',1)`)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	acted := func(id int64) int {
		t.Helper()
		var v int
		if err := s.ReadDB.QueryRow(`SELECT acted_on FROM signals WHERE id=?`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	run("init", "--project", "p")
	seen := signal()
	run("next", "--project", "p")
	late := signal()
	run("complete", "--project", "p", "--action", "all")

	if acted(seen) != 1 {
		t.Error("the signal next read was not consumed")
	}
	if acted(late) != 0 {
		t.Error("a signal written after next was consumed")
	}
}

// Two signals written in one second tie on created_at, and `chb hive
// signals` breaks the tie by id: the newer, the higher id, comes first, with
// or without a filter.
func TestHiveSignals_SameSecondNewestIDFirst(t *testing.T) {
	s := useTestStore(t)
	insert := func() int64 {
		t.Helper()
		res, err := s.WriteDB.Exec(`INSERT INTO signals (signal_type, source_type, payload_json, wave, created_at)
			VALUES ('stop_signal', 'system', '{}', 1, '2026-10-07 12:00:00')`)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	older := insert()
	newer := insert()
	for _, c := range []struct {
		sigType string
		pending bool
	}{{"", false}, {"stop_signal", false}, {"", true}} {
		query, params := hiveSignalsQuery(c.sigType, c.pending)
		rows, err := db.QueryToMaps(s.ReadDB, query, params...)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, r := range rows {
			ids = append(ids, r["id"].(int64))
		}
		if len(ids) != 2 || ids[0] != newer || ids[1] != older {
			t.Errorf("type %q, pending %v: ids %v, want [%d %d]", c.sigType, c.pending, ids, newer, older)
		}
	}
}
