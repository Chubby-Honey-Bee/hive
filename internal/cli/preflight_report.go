package cli

import (
	"fmt"
	"sort"
	"strings"
)

type checkLevel int

const (
	checkPass checkLevel = iota
	checkFail
	checkWarn
)

// symbol is the mark a check of this level is printed with.
func (l checkLevel) symbol() string {
	switch l {
	case checkPass:
		return "✓"
	case checkFail:
		return "✗"
	case checkWarn:
		return "⚠"
	}
	return ""
}

type checkResult struct {
	level checkLevel
	name  string
	msg   string
}

type preflightReport struct {
	results []checkResult
}

func newPreflightReport() *preflightReport { return &preflightReport{} }

func (r *preflightReport) pass(name, msg string) {
	r.results = append(r.results, checkResult{checkPass, name, msg})
}
func (r *preflightReport) fail(name, msg string) {
	r.results = append(r.results, checkResult{checkFail, name, msg})
}
func (r *preflightReport) warn(name, msg string) {
	r.results = append(r.results, checkResult{checkWarn, name, msg})
}

func (r *preflightReport) failures() int {
	n := 0
	for _, c := range r.results {
		if c.level == checkFail {
			n++
		}
	}
	return n
}
func (r *preflightReport) hasFailures() bool { return r.failures() > 0 }

// print writes one line per check, with its mark, and then the verdict.
func (r *preflightReport) print(w interface{ Write([]byte) (int, error) }) {
	for _, c := range r.results {
		fmt.Fprintf(w, "  %s %s — %s\n", c.level.symbol(), c.name, c.msg)
	}
	fmt.Fprintln(w, "")
	fails := r.failures()
	if fails == 0 {
		fmt.Fprintln(w, "preflight: PASS")
		return
	}
	fmt.Fprintf(w, "preflight: FAIL (%d check%s failed)\n", fails, plural(fails))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// passListing adds one ✓ line under name that lists lines, sorted, when
// there are any.
func (r *preflightReport) passListing(name string, lines []string) {
	if len(lines) == 0 {
		return
	}
	sort.Strings(lines)
	r.pass(name, strings.Join(lines, "; "))
}
