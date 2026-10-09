package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runTool(args []string, stdin string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = run(args, strings.NewReader(stdin), &o, &e)
	return code, o.String(), e.String()
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestHiddenDefaultTenLines(t *testing.T) {
	code, out, errOut := runTool(nil, lines(12))
	if code != 0 || out != lines(10) || errOut != "" {
		t.Errorf("got code %d out %q err %q; want the first 10 lines", code, out, errOut)
	}
}

func TestHiddenLineAndByteCounts(t *testing.T) {
	if _, out, _ := runTool([]string{"-n", "2"}, lines(5)); out != lines(2) {
		t.Errorf("-n 2: %q", out)
	}
	if _, out, _ := runTool([]string{"-c", "3"}, "abcdef"); out != "abc" {
		t.Errorf("-c 3: %q", out)
	}
	if _, out, _ := runTool([]string{"-n", "0"}, lines(3)); out != "" {
		t.Errorf("-n 0: %q, want nothing", out)
	}
	if _, out, _ := runTool([]string{"-n", "5"}, "a\nb"); out != "a\nb" {
		t.Errorf("unterminated last line: %q, want it printed as is", out)
	}
}

func TestHiddenHeadersBetweenFiles(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.txt", "a1\na2\n")
	b := writeFile(t, dir, "b.txt", "b1\n")
	code, out, _ := runTool([]string{"-n", "1", a, b}, "")
	want := "==> " + a + " <==\na1\n\n==> " + b + " <==\nb1\n"
	if code != 0 || out != want {
		t.Errorf("got code %d\n%q\nwant\n%q", code, out, want)
	}
	code, out, _ = runTool([]string{a}, "")
	if code != 0 || out != "a1\na2\n" {
		t.Errorf("one file: code %d out %q; want no header", code, out)
	}
}

func TestHiddenMissingFileContinues(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.txt", "x\n")
	missing := filepath.Join(dir, "missing.txt")
	code, out, errOut := runTool([]string{missing, a}, "")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut, "tool: "+missing+": ") {
		t.Errorf("stderr %q does not name the missing file as tool: NAME: ERROR", errOut)
	}
	if !strings.Contains(out, "x\n") || !strings.Contains(out, "==> "+a+" <==") {
		t.Errorf("stdout %q; want the other file printed under its header", out)
	}
}

func TestHiddenUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"-n", "2", "-c", "3"}, {"-n", "x"}, {"-n", "-1"}, {"-q"}, {"-n"}} {
		code, out, errOut := runTool(args, "a\n")
		if code != 2 || out != "" || !strings.Contains(strings.ToLower(errOut), "usage") {
			t.Errorf("%v: code %d out %q err %q; want 2, no output, usage on stderr", args, code, out, errOut)
		}
	}
}
