package harness

import (
	"os"
	"path/filepath"
	"testing"
)

// The manifest names what was added, removed and changed, and nothing else.
func TestTreeChanges(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("foragers/a.md", "a")
	write("foragers/b.md", "b")
	write("agents/c.md", "c")
	before, err := treeManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok, changes := treeCheck(dir, before, nil); !ok {
		t.Fatalf("an untouched tree reported changes: %s", changes)
	}
	write("foragers/a.md", "edited")
	write("agents/new/d.md", "d")
	if err := os.Remove(filepath.Join(dir, "foragers", "b.md")); err != nil {
		t.Fatal(err)
	}
	ok, changes := treeCheck(dir, before, nil)
	if want := "+agents/new, +agents/new/d.md, -foragers/b.md, ~foragers/a.md"; ok || changes != want {
		t.Fatalf("changes %q, want %q", changes, want)
	}
}
