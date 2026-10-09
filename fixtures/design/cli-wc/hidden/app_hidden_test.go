package main

import (
	"bytes"
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

func TestHiddenStdinAllCounts(t *testing.T) {
	code, out, errOut := runTool(nil, "a b\nc\n")
	if code != 0 || out != "2 3 6\n" || errOut != "" {
		t.Errorf("got code %d out %q err %q; want 0, %q, empty", code, out, errOut, "2 3 6\n")
	}
}

func TestHiddenFlagsSelectAndOrder(t *testing.T) {
	for _, args := range [][]string{{"-w", "-l"}, {"-l", "-w"}} {
		code, out, _ := runTool(args, "a b\nc\n")
		if code != 0 || out != "2 3\n" {
			t.Errorf("%v: code %d out %q; want 0, %q", args, code, out, "2 3\n")
		}
	}
	if _, out, _ := runTool([]string{"-c"}, "hello"); out != "5\n" {
		t.Errorf("-c: out %q, want 5", out)
	}
	if _, out, _ := runTool([]string{"-l"}, "no newline"); out != "0\n" {
		t.Errorf("-l without a newline: out %q, want 0", out)
	}
}

func TestHiddenFilesAndTotal(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.txt", "one two\n")
	b := writeFile(t, dir, "b.txt", "three\nfour five six\n")
	code, out, errOut := runTool([]string{a, b}, "")
	want := "1 2 8 " + a + "\n2 4 20 " + b + "\n3 6 28 total\n"
	if code != 0 || out != want || errOut != "" {
		t.Errorf("got code %d\n%q\nwant\n%q\nstderr %q", code, out, want, errOut)
	}
	code, out, _ = runTool([]string{"-l", a}, "")
	if code != 0 || out != "1 "+a+"\n" {
		t.Errorf("one file: code %d out %q; want no total line", code, out)
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
	if out != "1 1 2 "+a+"\n1 1 2 total\n" {
		t.Errorf("stdout %q; want the other file counted and a total", out)
	}
}

func TestHiddenUnknownFlagIsUsage(t *testing.T) {
	code, out, errOut := runTool([]string{"-x"}, "a\n")
	if code != 2 || out != "" || !strings.Contains(strings.ToLower(errOut), "usage") {
		t.Errorf("got code %d out %q err %q; want 2, no output, usage on stderr", code, out, errOut)
	}
}

func TestHiddenEmptyInput(t *testing.T) {
	code, out, _ := runTool(nil, "")
	if code != 0 || out != "0 0 0\n" {
		t.Errorf("got code %d out %q; want 0, %q", code, out, "0 0 0\n")
	}
}
