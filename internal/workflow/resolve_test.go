package workflow

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hive "github.com/Chubby-Honey-Bee/hive"
)

// In a directory with no workflows/, workflows/hive.yaml resolves to the
// carried copy, byte for byte, in a file cleanup removes.
func TestResolveFile_CarriedCopyWhenNotOnDisk(t *testing.T) {
	t.Chdir(t.TempDir())
	path, shipped, cleanup, err := ResolveFile(filepath.Join("workflows", "hive.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !shipped || filepath.Base(path) != "hive.yaml" {
		t.Fatalf("ResolveFile = %q shipped=%v, want the carried hive.yaml", path, shipped)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fs.ReadFile(hive.Workflows, "hive.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("the file differs from the carried hive.yaml")
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s: %v", path, err)
	}
}

// Every carried workflow resolves by its plain name and as workflows/<name>.
func TestResolveFile_EveryCarriedWorkflowResolvesByName(t *testing.T) {
	t.Chdir(t.TempDir())
	entries, err := fs.ReadDir(hive.Workflows, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no carried workflows")
	}
	for _, e := range entries {
		want, err := fs.ReadFile(hive.Workflows, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, arg := range []string{e.Name(), filepath.Join("workflows", e.Name())} {
			path, shipped, cleanup, err := ResolveFile(arg)
			if err != nil {
				t.Errorf("ResolveFile(%s): %v", arg, err)
				continue
			}
			got, _ := os.ReadFile(path)
			cleanup()
			if !shipped || string(got) != string(want) {
				t.Errorf("ResolveFile(%s) = %q shipped=%v, want the carried copy", arg, path, shipped)
			}
		}
	}
}

// workflows/ under the working directory overrides the carried copy, by the
// path and by the plain name.
func TestResolveFile_DiskOverridesTheCarriedCopy(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("workflows", 0o755); err != nil {
		t.Fatal(err)
	}
	onDisk := filepath.Join("workflows", "hive.yaml")
	if err := os.WriteFile(onDisk, []byte("name: mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{onDisk, "hive.yaml"} {
		path, shipped, cleanup, err := ResolveFile(arg)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if shipped || path != onDisk {
			t.Errorf("ResolveFile(%s) = %q shipped=%v, want %q from disk", arg, path, shipped, onDisk)
		}
	}
}

// A path that exists is taken as given.
func TestResolveFile_PathAsGivenWins(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("mine.yaml", []byte("name: mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, shipped, _, err := ResolveFile("mine.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if shipped || path != "mine.yaml" {
		t.Errorf("ResolveFile(mine.yaml) = %q shipped=%v", path, shipped)
	}
}

// A name no copy holds, and a path into another directory, are not found;
// the error names the argument.
func TestResolveFile_NotFound(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, arg := range []string{"nope.yaml", filepath.Join("elsewhere", "hive.yaml"), filepath.Join("..", "hive.yaml")} {
		_, _, _, err := ResolveFile(arg)
		if err == nil || !strings.Contains(err.Error(), arg) {
			t.Errorf("ResolveFile(%s): err = %v, want an error naming it", arg, err)
		}
	}
}
