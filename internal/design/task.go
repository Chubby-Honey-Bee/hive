// Package design is the executability benchmark (docs/specs/bench-design.md):
// tasks a designer researches and a plain executor carries out, graded by
// hidden tests, and the decision rule over the rows.
package design

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// HiddenPrefix is the name every hidden test function begins with, so a
// test the executor writes cannot collide with one.
const HiddenPrefix = "TestHidden"

// The arms of a task run.
const (
	ArmDesigner = "designer"
	ArmControl  = "control"
)

// Task is one fixture under fixtures/design/<name>/.
type Task struct {
	Name   string
	Family string `yaml:"family"`
	Twin   string `yaml:"twin"`
	// Dir is the task's directory; Tree, Hidden and Reference are under it.
	Dir       string
	Statement string
	Note      string
}

// Tree is the task's repository, which the designer reads and the
// executor edits.
func (t Task) Tree() string { return filepath.Join(t.Dir, "tree") }

// Hidden is the task's hidden tests, mirroring Tree's layout.
func (t Task) Hidden() string { return filepath.Join(t.Dir, "hidden") }

// Reference is one correct solution's files, mirroring Tree's layout.
func (t Task) Reference() string { return filepath.Join(t.Dir, "reference") }

// LoadTasks reads every task under root in name order. It refuses a task
// missing one of its parts, a twin that does not name it back, and a hidden
// test whose name lacks HiddenPrefix. With names non-empty only those tasks
// are returned, and a name no task has is an error.
func LoadTasks(root string, names []string) ([]Task, error) {
	all, err := loadTaskDirs(root)
	if err != nil {
		return nil, err
	}
	want := nameSet(names)
	if err := requireTasks(all, want, root); err != nil {
		return nil, err
	}
	if err := checkTwins(all); err != nil {
		return nil, err
	}
	return selectTasks(all, want), nil
}

// nameSet is the names as a set.
func nameSet(names []string) map[string]bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return set
}

// loadTaskDirs loads every task directory under root, by name.
func loadTaskDirs(root string) (map[string]Task, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	all := map[string]Task{}
	for _, e := range entries {
		if err := addTaskDir(all, root, e); err != nil {
			return nil, err
		}
	}
	return all, nil
}

// addTaskDir loads the task in entry e of root into all; a file is not a
// task and is skipped.
func addTaskDir(all map[string]Task, root string, e fs.DirEntry) error {
	if !e.IsDir() {
		return nil
	}
	t, err := LoadTask(filepath.Join(root, e.Name()))
	if err != nil {
		return err
	}
	all[t.Name] = t
	return nil
}

// requireTasks refuses a wanted name that no task under root has.
func requireTasks(all map[string]Task, want map[string]bool, root string) error {
	for n := range want {
		if _, ok := all[n]; !ok {
			return fmt.Errorf("no task %q under %s", n, root)
		}
	}
	return nil
}

// checkTwins refuses a task whose twin does not name it back.
func checkTwins(all map[string]Task) error {
	for _, t := range all {
		if !namedBack(all, t) {
			return fmt.Errorf("task %s names twin %s, which does not name it back", t.Name, t.Twin)
		}
	}
	return nil
}

// namedBack: t has no twin, or its twin is a task that names t back.
func namedBack(all map[string]Task, t Task) bool {
	if t.Twin == "" {
		return true
	}
	other, ok := all[t.Twin]
	return ok && other.Twin == t.Name
}

// selectTasks is the wanted tasks, or every task when none is wanted, in
// name order.
func selectTasks(all map[string]Task, want map[string]bool) []Task {
	var out []Task
	for _, t := range all {
		if len(want) == 0 || want[t.Name] {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LoadTask reads one task directory.
func LoadTask(dir string) (Task, error) {
	t := Task{Name: filepath.Base(dir), Dir: dir}
	for _, step := range []func(t *Task, dir string) error{readTaskYAML, readTaskTexts, checkTaskDirs, checkHiddenTests, checkReference} {
		if err := step(&t, dir); err != nil {
			return t, err
		}
	}
	return t, nil
}

// readTaskYAML reads task.yaml into t, which must name a family.
func readTaskYAML(t *Task, dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "task.yaml"))
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, t); err != nil {
		return fmt.Errorf("%s/task.yaml: %w", t.Name, err)
	}
	if t.Family == "" {
		return fmt.Errorf("%s/task.yaml: no family", t.Name)
	}
	return nil
}

// readTaskTexts reads the statement and the note, neither of which may be
// empty.
func readTaskTexts(t *Task, dir string) error {
	for _, f := range []struct {
		name string
		dst  *string
	}{{"statement.md", &t.Statement}, {"note.md", &t.Note}} {
		b, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			return err
		}
		if *f.dst = strings.TrimSpace(string(b)); *f.dst == "" {
			return fmt.Errorf("%s/%s is empty", t.Name, f.name)
		}
	}
	return nil
}

// checkTaskDirs requires the tree, hidden and reference directories, and a
// go.mod in the tree.
func checkTaskDirs(t *Task, _ string) error {
	for _, sub := range []string{t.Tree(), t.Hidden(), t.Reference()} {
		if !isDir(sub) {
			return fmt.Errorf("%s: %s is not a directory", t.Name, sub)
		}
	}
	if _, err := os.Stat(filepath.Join(t.Tree(), "go.mod")); err != nil {
		return fmt.Errorf("%s: tree/ has no go.mod", t.Name)
	}
	return nil
}

// isDir: path is a directory.
func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// checkHiddenTests requires at least one hidden test, every one named with
// HiddenPrefix.
func checkHiddenTests(t *Task, _ string) error {
	names, err := HiddenTestNames(t.Hidden())
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("%s: hidden/ holds no test", t.Name)
	}
	if n, ok := firstUnprefixed(names, HiddenPrefix); ok {
		return fmt.Errorf("%s: hidden test %s does not begin with %s", t.Name, n, HiddenPrefix)
	}
	return nil
}

// firstUnprefixed is the first of names that does not begin with prefix.
func firstUnprefixed(names []string, prefix string) (string, bool) {
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) {
			return n, true
		}
	}
	return "", false
}

// checkReference requires the reference to change at least one file of
// the tree.
func checkReference(t *Task, _ string) error {
	changes, err := ReferenceChanges(*t)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return fmt.Errorf("%s: reference/ changes no file of tree/", t.Name)
	}
	return nil
}

// ReferenceChanges lists the files the reference solution adds or changes:
// every file under reference/ that the tree lacks or holds with other
// bytes, relative to the tree and slash-separated, sorted. It is the
// denominator of plan completeness, computed from the fixture alone.
func ReferenceChanges(t Task) ([]string, error) {
	files, err := TreeFiles(t.Reference())
	if err != nil {
		return nil, err
	}
	return changedByReference(t, files)
}

// changedByReference is the files, of those under reference/, that the
// reference adds or changes.
func changedByReference(t Task, files []string) ([]string, error) {
	var out []string
	for _, f := range files {
		changed, err := referenceChanges(t, f)
		if err != nil {
			return nil, err
		}
		if changed {
			out = append(out, f)
		}
	}
	return out, nil
}

// referenceChanges: the tree lacks the reference's file f, or holds it
// with other bytes.
func referenceChanges(t Task, f string) (bool, error) {
	ref, err := os.ReadFile(filepath.Join(t.Reference(), filepath.FromSlash(f)))
	if err != nil {
		return false, err
	}
	cur, err := os.ReadFile(filepath.Join(t.Tree(), filepath.FromSlash(f)))
	switch {
	case err == nil:
		return string(cur) != string(ref), nil
	case os.IsNotExist(err):
		return true, nil
	}
	return false, err
}

// CopyTree copies the task's tree, and nothing else of the task, to dst.
func CopyTree(t Task, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.CopyFS(dst, os.DirFS(t.Tree()))
}

// Overlay copies every file under src onto dst at its relative path.
func Overlay(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		return copyFile(path, filepath.Join(dst, rel))
	})
}

// copyFile writes the bytes of the file at from to the path to, making its
// directory.
func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o644)
}

// TreeFiles lists the regular files under dir, relative, sorted.
func TreeFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Pack is what both arms read: the statement, then every file of the tree
// under its path in a fenced block.
func Pack(t Task) (string, error) {
	files, err := TreeFiles(t.Tree())
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(t.Statement)
	b.WriteString("\n\n## Repository\n")
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(t.Tree(), filepath.FromSlash(f)))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n### %s\n\n````\n%s\n````\n", f, strings.TrimRight(string(raw), "\n"))
	}
	return b.String(), nil
}
