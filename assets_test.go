package hive

import (
	"io/fs"
	"os"
	"sort"
	"testing"
)

// The carried trees are the repo folders, file for file: a file added to,
// removed from or changed in a folder reaches the binary, and nothing else
// does. The expected set is the folder on disk, read here.
func TestCarriedTreesMatchTheRepoFolders(t *testing.T) {
	for dir, carried := range map[string]fs.FS{"foragers": Foragers, "agents": Agents, "workflows": Workflows} {
		want := treeFiles(t, os.DirFS(dir))
		if len(want) == 0 {
			t.Fatalf("%s/: no files on disk", dir)
		}
		got := treeFiles(t, carried)
		for _, name := range sortedKeys(want) {
			switch body, ok := got[name]; {
			case !ok:
				t.Errorf("%s/%s is on disk and not in the binary", dir, name)
			case body != want[name]:
				t.Errorf("%s/%s differs between disk and the binary", dir, name)
			}
		}
		for _, name := range sortedKeys(got) {
			if _, ok := want[name]; !ok {
				t.Errorf("%s/%s is in the binary and not on disk", dir, name)
			}
		}
	}
}

func treeFiles(t *testing.T, fsys fs.FS) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
