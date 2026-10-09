package foragers

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	hive "github.com/Chubby-Honey-Bee/hive"
)

func writeForager(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte("---\nname: "+name+"\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// With the variable unset and no directory holding a forager, the tree is
// the copy the binary carries, and it loads the foragers that copy holds.
func TestResolve_CarriedCopyWhenNoDirHoldsAForager(t *testing.T) {
	t.Setenv("HIVE_FORAGERS_DIR", "")
	readmeOnly := t.TempDir()
	if err := os.WriteFile(filepath.Join(readmeOnly, "README.md"), []byte("# not a forager\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "absent")

	src := Resolve(missing, readmeOnly)
	if !src.Shipped() || src.Dir != "" {
		t.Fatalf("Resolve(%s, %s) = %+v, want the carried copy", missing, readmeOnly, src)
	}
	got, err := src.Load()
	if err != nil {
		t.Fatal(err)
	}
	want, err := LoadFS(hive.Foragers)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 || !reflect.DeepEqual(names(got), names(want)) {
		t.Errorf("loaded %v, want the carried foragers %v", names(got), names(want))
	}
}

// The first directory holding a forager file is the tree; one holding only
// a README is passed over.
func TestResolve_FirstDirHoldingAForagerWins(t *testing.T) {
	t.Setenv("HIVE_FORAGERS_DIR", "")
	readmeOnly, alphaDir, betaDir := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(readmeOnly, "README.md"), []byte("# not a forager\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeForager(t, alphaDir, "alpha")
	writeForager(t, betaDir, "beta")

	src := Resolve(readmeOnly, alphaDir, betaDir)
	if src.Dir != alphaDir {
		t.Fatalf("Resolve picked %q, want %q", src.Dir, alphaDir)
	}
	got, err := src.Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha"}; !reflect.DeepEqual(names(got), want) {
		t.Errorf("loaded %v, want %v", names(got), want)
	}
}

// HIVE_FORAGERS_DIR is the tree whatever it holds: it wins over a
// directory holding a forager, and an empty one yields no foragers rather
// than the carried copy.
func TestResolve_EnvWinsWhateverItHolds(t *testing.T) {
	alphaDir, gammaDir := t.TempDir(), t.TempDir()
	writeForager(t, alphaDir, "alpha")
	writeForager(t, gammaDir, "gamma")

	t.Setenv("HIVE_FORAGERS_DIR", gammaDir)
	src := Resolve(alphaDir)
	if src.Dir != gammaDir {
		t.Fatalf("Resolve picked %q, want the variable's %q", src.Dir, gammaDir)
	}
	got, err := src.Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gamma"}; !reflect.DeepEqual(names(got), want) {
		t.Errorf("loaded %v, want %v", names(got), want)
	}

	empty := t.TempDir()
	t.Setenv("HIVE_FORAGERS_DIR", empty)
	src = Resolve(alphaDir)
	if src.Dir != empty || src.Shipped() {
		t.Fatalf("Resolve picked %+v, want the variable's empty %q", src, empty)
	}
	got, err = src.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("an empty HIVE_FORAGERS_DIR loaded %v, want none", names(got))
	}
}
