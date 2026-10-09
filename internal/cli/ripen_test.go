package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Each ripen gets its own tick. The label had one-second resolution and Begin
// returns an existing (kind, label) row, so back-to-back ripens in the same
// second shared the first one's tick, already closed.
func TestRipen_EachInvocationOpensItsOwnTick(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "ripen.db"))
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

	const runs = 3
	for i := 0; i < runs; i++ {
		cmd := newRipenCmd()
		cmd.SetArgs([]string{"--passes", "hypothesize"})
		cmd.SetOut(&bytes.Buffer{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("ripen %d: %v", i+1, err)
		}
	}
	ticks, err := s.TimeWheel().Recent(db.TickRipen, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ticks) != runs {
		t.Fatalf("%d ripen ticks after %d ripens, want %d", len(ticks), runs, runs)
	}
	for _, tk := range ticks {
		if !tk.EndedAt.Valid {
			t.Errorf("tick %d (%s) was left open", tk.ID, tk.Label)
		}
	}
}

// --max-passes 0 is refused: the loop replaces a zero MaxPasses with its
// default, so it would run all five passes.
func TestRipen_ZeroMaxPassesIsRefused(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "ripen.db"))
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

	cmd := newRipenCmd()
	cmd.SetArgs([]string{"--max-passes", "0"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--max-passes") {
		t.Fatalf("err = %v, want a --max-passes refusal", err)
	}
	var passes int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM ripen_log`).Scan(&passes); err != nil {
		t.Fatal(err)
	}
	if passes != 0 {
		t.Fatalf("%d passes ran, want none", passes)
	}
}
