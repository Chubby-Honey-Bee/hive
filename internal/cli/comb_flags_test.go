package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// A --kind typo is refused, rather than filtering everything out into an
// empty result that reads exactly like an empty database.
func TestCombKindFlagsRefuseUnknownValues(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "kinds.db"))
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

	for name, args := range map[string][]string{
		"wheel": {"--kind", "swarms"},
		"embed": {"--kind", "regions", "--provider", "stub"},
	} {
		var cmd = newCombWheelCmd()
		if name == "embed" {
			cmd = newCombEmbedCmd()
		}
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "unknown --kind") {
			t.Errorf("comb %s %v: err = %v, want an unknown --kind refusal", name, args, err)
		}
	}
	if err := flagOneOf("--kind", "", "a"); err != nil {
		t.Errorf("an absent flag was refused: %v", err)
	}
}
