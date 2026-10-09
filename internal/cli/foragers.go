package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
)

// Forager-swarm subcommands are wired in main.go directly to the root.
// The doc-comment block below documents the user-facing entry points
// for HIVE's forager swarm. The subcommands live at the root level
// (a parent `swarm` group would be a wasted dimension in the CLI's
// coordinate space):
//
//	chb ask "<question>" [--foragers default,steward]
//	chb list                  — show available foragers
//	chb generate "<q>" --out  — emit the workflow YAML, don't dispatch
//	chb palette --out         — export the sigil/accent palette
//	chb gaps                  — surface coverage gaps
//	chb recall "<q>"          — semantic prior-swarm retrieval
//	chb ripen                — Dreamer forager's five-pass loop

// foragerDirs are the directories the registry checks, in order, when
// HIVE_FORAGERS_DIR is unset: `foragers/` under the working directory
// (running from the repo root), then beside the binary (`<bindir>/foragers`,
// `<bindir>/../foragers`, `<bindir>/../share/chb/foragers`), so an installed
// binary finds personas dropped alongside it. None holding a forager file,
// foragers.Resolve reads the copy the binary carries.
func foragerDirs() []string {
	out := []string{"foragers"}
	if exe, err := os.Executable(); err == nil {
		binDir := filepath.Dir(exe)
		out = append(out,
			filepath.Join(binDir, "foragers"),
			filepath.Join(binDir, "..", "foragers"),
			filepath.Join(binDir, "..", "share", "chb", "foragers"),
		)
	}
	return out
}

// shippedForagersNote prints once per process that the carried copy is read.
var shippedForagersNote sync.Once

// foragersSource resolves the forager tree (foragers.Resolve over
// foragerDirs). The first time the copy the binary carries is taken, it
// says so on stderr, so a user who meant their own folder sees why it was
// not read.
func foragersSource() foragers.Source {
	src := foragers.Resolve(foragerDirs()...)
	if src.Shipped() {
		shippedForagersNote.Do(func() {
			fmt.Fprintln(os.Stderr, "foragers: no foragers/ on disk; reading the copy the binary carries (set HIVE_FORAGERS_DIR to use your own)")
		})
	}
	return src
}

// sourceLabel names a forager tree in a message: its directory, or the
// carried copy.
func sourceLabel(src foragers.Source) string {
	if src.Shipped() {
		return "the foragers the binary carries"
	}
	return src.Dir
}

// loadForagers reads the resolved tree. An empty tree is an error naming it
// (noForagersError).
func loadForagers() ([]foragers.Forager, error) {
	src := foragersSource()
	all, err := src.Load()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, noForagersError(src)
	}
	return all, nil
}

// noForagersError is the error for a tree with no forager in it: the
// directory HIVE_FORAGERS_DIR names or a directory on disk whose
// markdown files none parsed as a forager. The carried copy is never empty.
func noForagersError(src foragers.Source) error {
	return fmt.Errorf("no foragers found in %s: it holds no forager markdown file. Point HIVE_FORAGERS_DIR at a directory of forager files, or unset it: chb then reads foragers/ under the working directory or beside the binary, else the copy it carries",
		src.Dir)
}
