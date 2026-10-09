package design

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// HiddenTestNames lists the top-level Test functions of the test files
// under hidden, in file then source order: those named Test… that take one
// parameter. The hidden suite's total is their count, read from the source,
// so a tree that does not build scores 0 of the total.
func HiddenTestNames(hidden string) ([]string, error) {
	var names []string
	err := filepath.WalkDir(hidden, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		found, err := fileTestNames(path)
		names = append(names, found...)
		return err
	})
	return names, err
}

// fileTestNames lists one test file's top-level Test functions, in source
// order.
func fileTestNames(path string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, decl := range f.Decls {
		if name, ok := testFuncName(decl); ok {
			names = append(names, name)
		}
	}
	return names, nil
}

// testFuncName is the name of a declaration that is a top-level test.
func testFuncName(decl ast.Decl) (string, bool) {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok {
		return "", false
	}
	return fn.Name.Name, isTestFunc(fn)
}

// isTestFunc: fn is a function, not a method, named Test… with one
// parameter.
func isTestFunc(fn *ast.FuncDecl) bool {
	return fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") && fn.Type.Params.NumFields() == 1
}

// CountHiddenTests is the hidden suite's total.
func CountHiddenTests(hidden string) (int, error) {
	names, err := HiddenTestNames(hidden)
	return len(names), err
}

// Grade is what the hidden tests said of an executed tree.
type Grade struct {
	Passed int      `json:"passed"`
	Total  int      `json:"total"`
	Failed []string `json:"failed,omitempty"`
	// Built is false when no package of the tree reported a test event:
	// the tree did not build, or go test did not run.
	Built  bool   `json:"built"`
	Output string `json:"-"`
}

// Rate is passed ÷ total, 0 when the suite is empty.
func (g Grade) Rate() float64 {
	if g.Total == 0 {
		return 0
	}
	return float64(g.Passed) / float64(g.Total)
}

// RunHiddenTests grades tree, a copy of an executed tree, in place: the
// hidden test files are dropped in at their mirrored paths and `go test
// -json -count=1 ./...` runs under GOTOOLCHAIN=local with ctx's deadline.
// Passed counts the hidden suite's top-level tests whose action was pass.
func RunHiddenTests(ctx context.Context, tree, hidden string) (Grade, error) {
	names, err := HiddenTestNames(hidden)
	if err != nil {
		return Grade{}, err
	}
	g := Grade{Total: len(names)}
	if err := Overlay(hidden, tree); err != nil {
		return g, err
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		return g, fmt.Errorf("go is not on PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, gobin, "test", "-json", "-count=1", "./...")
	cmd.Dir = tree
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=")
	out, _ := cmd.CombinedOutput()
	g.Output = string(out)
	passed, failed, saw := ReadTestEvents(out, names)
	g.Passed, g.Failed, g.Built = passed, failed, saw
	return g, nil
}

// ReadTestEvents reads `go test -json` output and reports how many of the
// named top-level tests passed, which did not, and whether any test event
// was seen at all. A test named in no event did not pass.
func ReadTestEvents(out []byte, names []string) (passed int, failed []string, saw bool) {
	status, saw := topLevelResults(out)
	for _, n := range names {
		if status[n] == "pass" {
			passed++
		} else {
			failed = append(failed, n)
		}
	}
	sort.Strings(failed)
	return passed, failed, saw
}

// testEvent is the part of a `go test -json` event that grading reads.
type testEvent struct {
	Action string `json:"action"`
	Test   string `json:"test"`
}

// topLevelResults reads `go test -json` output: the last result of each
// top-level test, and whether any test reported a result at all.
func topLevelResults(out []byte) (map[string]string, bool) {
	status := map[string]string{}
	saw := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		ev, ok := testResult(sc.Bytes())
		if !ok {
			continue
		}
		saw = true
		if !strings.Contains(ev.Test, "/") {
			status[ev.Test] = ev.Action
		}
	}
	return status, saw
}

// testResult decodes a line that reports a test's result: pass, fail or
// skip.
func testResult(line []byte) (testEvent, bool) {
	var ev testEvent
	if json.Unmarshal(line, &ev) != nil || ev.Test == "" {
		return ev, false
	}
	return ev, isResult(ev.Action)
}

// isResult: the action ends a test.
func isResult(action string) bool {
	return action == "pass" || action == "fail" || action == "skip"
}
