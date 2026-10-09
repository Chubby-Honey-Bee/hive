package design

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fixturesRoot = "../../fixtures/design"

// gradeTree copies t's tree to a temp dir, overlays the given solution
// directory (none when empty), drops in the hidden tests of hiddenOf, and
// grades. It is the shipped tasks' oracle: the same path the harness takes.
func gradeTree(t *testing.T, task Task, solution string, hiddenOf Task) Grade {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tree")
	if err := CopyTree(task, dir); err != nil {
		t.Fatal(err)
	}
	if solution != "" {
		if err := Overlay(solution, dir); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	g, err := RunHiddenTests(ctx, dir, hiddenOf.Hidden())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func treeHash(t *testing.T, dir string) string {
	t.Helper()
	files, err := TreeFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%s\n%s\n", f, b)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// The shipped tasks: twelve of them, at least four twin pairs; every hidden
// suite passes on its reference and fails on the unmodified tree; twins
// share a byte-identical tree and each twin's reference fails the other's
// hidden tests. All recomputed from the fixtures.
func TestShippedTasks(t *testing.T) {
	tasks, err := LoadTasks(fixturesRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 12 {
		t.Fatalf("%d tasks, want 12", len(tasks))
	}
	byName := map[string]Task{}
	pairs := 0
	for _, task := range tasks {
		byName[task.Name] = task
		if task.Twin != "" && task.Name < task.Twin {
			pairs++
		}
	}
	if pairs < 4 {
		t.Errorf("%d twin pairs, want at least 4", pairs)
	}
	for _, task := range tasks {
		task := task
		t.Run(task.Name, func(t *testing.T) {
			t.Parallel()
			ref := gradeTree(t, task, task.Reference(), task)
			if !ref.Built || ref.Passed != ref.Total || ref.Total == 0 {
				t.Errorf("reference passes %d of %d (built %v); failed %v\n%s", ref.Passed, ref.Total, ref.Built, ref.Failed, tail(ref.Output))
			}
			stub := gradeTree(t, task, "", task)
			if stub.Passed >= stub.Total {
				t.Errorf("the unmodified tree passes %d of %d: the hidden suite does not discriminate", stub.Passed, stub.Total)
			}
			if task.Twin == "" {
				return
			}
			other := byName[task.Twin]
			if treeHash(t, task.Tree()) != treeHash(t, other.Tree()) {
				t.Errorf("twins %s and %s do not share a byte-identical tree", task.Name, other.Name)
			}
			cross := gradeTree(t, task, task.Reference(), other)
			if cross.Built && cross.Passed == cross.Total {
				t.Errorf("%s's reference passes every hidden test of its twin %s: the statements do not require different designs", task.Name, other.Name)
			}
		})
	}
}

func tail(s string) string {
	if len(s) > 1500 {
		return "…" + s[len(s)-1500:]
	}
	return s
}

func writeTask(t *testing.T, root, name, twin, testName string, parts ...string) {
	t.Helper()
	dir := filepath.Join(root, name)
	for _, sub := range []string{"tree", "hidden", "reference"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"task.yaml":        "family: f\n",
		"statement.md":     "Do the thing.",
		"note.md":          "It decides X.",
		"tree/go.mod":      "module example.com/x\n\ngo 1.22\n",
		"tree/x.go":        "package x\n",
		"hidden/x_test.go": "package x\n\nimport \"testing\"\n\nfunc " + testName + "(t *testing.T) {}\n",
		"reference/x.go":   "package x\n\nfunc X() {}\n",
	}
	if twin != "" {
		files["task.yaml"] += "twin: " + twin + "\n"
	}
	for i := 0; i+1 < len(parts); i += 2 {
		if parts[i+1] == "" {
			delete(files, parts[i])
			continue
		}
		files[parts[i]] = parts[i+1]
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadTasks_RefusesABrokenFixture(t *testing.T) {
	cases := []struct {
		name  string
		setup func(root string)
		want  string
	}{
		{"a twin that does not name back", func(root string) {
			writeTask(t, root, "a", "b", "TestHiddenA")
			writeTask(t, root, "b", "", "TestHiddenB")
		}, "does not name it back"},
		{"a hidden test without the prefix", func(root string) {
			writeTask(t, root, "a", "", "TestPlain")
		}, "does not begin with TestHidden"},
		{"a missing note", func(root string) {
			writeTask(t, root, "a", "", "TestHiddenA", "note.md", "")
		}, "note.md"},
		{"a tree without go.mod", func(root string) {
			writeTask(t, root, "a", "", "TestHiddenA", "tree/go.mod", "")
		}, "no go.mod"},
		{"a reference that changes nothing", func(root string) {
			writeTask(t, root, "a", "", "TestHiddenA", "reference/x.go", "package x\n")
		}, "changes no file"},
		{"a name no task has", func(root string) {
			writeTask(t, root, "a", "", "TestHiddenA")
		}, `no task "zzz"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			c.setup(root)
			var names []string
			if strings.HasPrefix(c.want, "no task") {
				names = []string{"zzz"}
			}
			_, err := LoadTasks(root, names)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("LoadTasks error %v; want one naming %q", err, c.want)
			}
		})
	}
}

func TestLoadTasks_FiltersByNameInOrder(t *testing.T) {
	root := t.TempDir()
	writeTask(t, root, "b", "", "TestHiddenB")
	writeTask(t, root, "a", "", "TestHiddenA")
	writeTask(t, root, "c", "", "TestHiddenC")
	all, err := LoadTasks(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(all); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("order %v, want a,b,c", got)
	}
	some, err := LoadTasks(root, []string{"c", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(some); strings.Join(got, ",") != "a,c" {
		t.Errorf("filtered %v, want a,c", got)
	}
}

func names(ts []Task) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

// The pack holds the statement and every tree file; the copied tree holds
// the tree alone: no hidden test, no note, no reference.
func TestPackAndCopyTree_ShowTheTreeAlone(t *testing.T) {
	root := t.TempDir()
	writeTask(t, root, "a", "", "TestHiddenA", "tree/sub/y.go", "package sub\n\nfunc Y() {}\n")
	tasks, err := LoadTasks(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	task := tasks[0]
	pack, err := Pack(task)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Do the thing.", "### go.mod", "### x.go", "### sub/y.go", "func Y() {}"} {
		if !strings.Contains(pack, want) {
			t.Errorf("pack lacks %q:\n%s", want, pack)
		}
	}
	for _, leak := range []string{"It decides X.", "TestHiddenA", "reference"} {
		if strings.Contains(pack, leak) {
			t.Errorf("pack leaks %q", leak)
		}
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyTree(task, dst); err != nil {
		t.Fatal(err)
	}
	files, err := TreeFiles(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(files, ","); got != "go.mod,sub/y.go,x.go" {
		t.Errorf("copied files %s, want go.mod,sub/y.go,x.go", got)
	}
}
