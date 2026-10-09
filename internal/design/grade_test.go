package design

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func events(t *testing.T, evs ...[2]string) []byte {
	t.Helper()
	var b strings.Builder
	for _, ev := range evs {
		raw, _ := json.Marshal(map[string]string{"Action": ev[0], "Test": ev[1], "Package": "p"})
		b.Write(raw)
		b.WriteByte('\n')
	}
	b.WriteString(`{"Action":"pass","Package":"p"}` + "\n")
	return []byte(b.String())
}

// Passed counts the named top-level tests whose action was pass: a failed
// test, a skipped one, one missing from the events, and a passing subtest
// of a failing parent do not count; a package with no test event at all
// did not build.
func TestReadTestEvents(t *testing.T) {
	names := []string{"TestHiddenA", "TestHiddenB", "TestHiddenC", "TestHiddenD"}
	out := events(t,
		[2]string{"run", "TestHiddenA"}, [2]string{"pass", "TestHiddenA"},
		[2]string{"run", "TestHiddenB"}, [2]string{"pass", "TestHiddenB/sub"}, [2]string{"fail", "TestHiddenB"},
		[2]string{"run", "TestHiddenC"}, [2]string{"skip", "TestHiddenC"},
		[2]string{"pass", "TestNotHidden"},
	)
	passed, failed, saw := ReadTestEvents(out, names)
	if passed != 1 || !saw || strings.Join(failed, ",") != "TestHiddenB,TestHiddenC,TestHiddenD" {
		t.Errorf("passed %d saw %v failed %v; want 1, true, B,C,D", passed, saw, failed)
	}
	passed, failed, saw = ReadTestEvents([]byte("# p\n./x.go:3:1: syntax error\nFAIL p [build failed]\n"), names)
	if passed != 0 || saw || len(failed) != 4 {
		t.Errorf("build failure: passed %d saw %v failed %v; want 0, false, all four", passed, saw, failed)
	}
}

func TestHiddenTestNames_ParsesTopLevelTests(t *testing.T) {
	dir := t.TempDir()
	src := `package x

import "testing"

func TestHiddenOne(t *testing.T) {}

func helper(t *testing.T) {}

func TestHiddenTwo(t *testing.T) { t.Run("sub", func(t *testing.T) {}) }

func (s *suite) TestHiddenMethod(t *testing.T) {}

func TestHiddenBad(a, b int) {}

type suite struct{}
`
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notatest.go"), []byte("package x\n\nfunc TestHiddenIgnored(t int) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HiddenTestNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "TestHiddenOne,TestHiddenTwo" {
		t.Errorf("names %v, want TestHiddenOne,TestHiddenTwo", got)
	}
}

// A tree that does not build scores 0 of the hidden suite's total, not 0
// of 0; one that builds and passes scores the total.
func TestRunHiddenTests_BuildFailureIsZeroOfTotal(t *testing.T) {
	hidden := t.TempDir()
	testSrc := "package x\n\nimport \"testing\"\n\nfunc TestHiddenA(t *testing.T) { if X() != 1 { t.Fatal() } }\n\nfunc TestHiddenB(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(hidden, "x_test.go"), []byte(testSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, src string
		passed    int
		built     bool
	}{
		{"broken", "package x\n\nfunc X() int { return \n", 0, false},
		{"wrong", "package x\n\nfunc X() int { return 2 }\n", 1, true},
		{"right", "package x\n\nfunc X() int { return 1 }\n", 2, true},
	} {
		tree := filepath.Join(t.TempDir(), c.name)
		if err := os.MkdirAll(tree, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"go.mod": "module example.com/x\n\ngo 1.22\n", "x.go": c.src} {
			if err := os.WriteFile(filepath.Join(tree, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		g, err := RunHiddenTests(ctx, tree, hidden)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if g.Total != 2 || g.Passed != c.passed || g.Built != c.built {
			t.Errorf("%s: %d of %d, built %v; want %d of 2, built %v\n%s", c.name, g.Passed, g.Total, g.Built, c.passed, c.built, g.Output)
		}
	}
}
