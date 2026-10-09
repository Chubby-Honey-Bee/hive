package harness

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A graded hive case asks something this repository can answer about
// itself, and the harness recomputes that answer on every run instead of
// pinning a number. A swarm's verdict is graded by a bench case's twin
// pairs instead (bench.go): one verdict is passed by a model
// that always gives it.
func gradeAnswer(name string) (verdict, detail string, err error) {
	switch name {
	case "sqlite-default-page-size":
		n, err := sqliteDefaultPageSize()
		if err != nil {
			return "", "", err
		}
		return fmt.Sprint(n), fmt.Sprintf("a new SQLite database here has %d-byte pages", n), nil
	}
	return "", "", fmt.Errorf("unknown grade %q", name)
}

// dirtyTrackedFiles returns git's porcelain status, empty when the tree is
// clean. A bench case compares it before and after, since a bash call can
// write outside a run's private tree.
func dirtyTrackedFiles() (string, error) {
	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// warnCheckoutChanged compares the checkout's tracked files with before,
// read when the case began: a bash call or the claude CLI's own tools can
// write outside a run's private tree, where treeCheck does not look.
func warnCheckoutChanged(before string, beforeErr error, warn func(string, bool, string)) {
	if beforeErr != nil {
		warn("checkout state readable", false, beforeErr.Error())
		return
	}
	after, err := dirtyTrackedFiles()
	if err != nil {
		warn("checkout state readable", false, err.Error())
		return
	}
	warn("no tracked file changed during the case (a concurrent edit counts too)", after == before, treeDelta(before, after))
}

// treeDelta names the porcelain lines present after a run and not before it.
func treeDelta(before, after string) string {
	was := harnessLineSet(strings.Split(before, "\n"))
	var added []string
	for _, line := range strings.Split(after, "\n") {
		if line != "" && !was[line] {
			added = append(added, line)
		}
	}
	return strings.Join(added, "; ")
}

// harnessLineSet is the lines as a set.
func harnessLineSet(lines []string) map[string]bool {
	set := map[string]bool{}
	for _, line := range lines {
		set[line] = true
	}
	return set
}

// sqliteDefaultPageSize creates an empty database with the SQLite build chb
// links and reads its page size, so the expected answer comes from SQLite
// itself rather than from a number written into the suite.
func sqliteDefaultPageSize() (int, error) {
	dir, err := os.MkdirTemp("", "chb-pagesize-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	return sqlitePageSizeOf(filepath.Join(dir, "probe.db"))
}

// sqlitePageSizeOf creates a table in a new database at path and reads
// the database's page size.
func sqlitePageSizeOf(path string) (int, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.Exec(`CREATE TABLE t (x)`); err != nil {
		return 0, err
	}
	var n int
	if err := conn.QueryRow(`PRAGMA page_size`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
