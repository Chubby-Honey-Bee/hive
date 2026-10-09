package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The preflight help names the binaries checkPathBinaries looks up. Put
// exactly the binaries the help lists on PATH: every lookup the check makes
// must then pass, and the binaries it passes must be the ones the help
// lists.
func TestPreflightHelp_NamesTheBinariesItChecks(t *testing.T) {
	const marker = "Required PATH binaries:"
	var listed []string
	for _, line := range strings.Split(newPreflightCmd().Long, "\n") {
		if _, rest, ok := strings.Cut(line, marker); ok {
			for _, name := range strings.Split(rest, ",") {
				listed = append(listed, strings.TrimSpace(name))
			}
		}
	}
	if len(listed) == 0 {
		t.Fatalf("preflight help has no %q line", marker)
	}

	dir := t.TempDir()
	for _, name := range listed {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	r := newPreflightReport()
	r.checkPathBinaries()
	var passed []string
	for _, c := range r.results {
		if c.level != checkPass {
			t.Errorf("with every listed binary on PATH, a lookup still missed: %s — %s", c.name, c.msg)
			continue
		}
		passed = append(passed, c.msg)
	}
	sort.Strings(listed)
	sort.Strings(passed)
	if strings.Join(passed, ",") != strings.Join(listed, ",") {
		t.Errorf("help lists %v; the check passes %v", listed, passed)
	}
}
