package harness

import (
	"strconv"
	"strings"
	"testing"
)

// The hive's grade comes from SQLite itself: a page size is a power of two
// from 512 to 65536. Any other grade name is an error rather than a silent
// pass.
func TestGradeAnswer_KnowsOnlyTheHiveGrade(t *testing.T) {
	verdict, detail, err := gradeAnswer("sqlite-default-page-size")
	if err != nil {
		t.Fatalf("gradeAnswer: %v", err)
	}
	n, err := strconv.Atoi(verdict)
	if err != nil || n < 512 || n > 65536 || n&(n-1) != 0 {
		t.Fatalf("page size %q, want a power of two from 512 to 65536", verdict)
	}
	if !strings.Contains(detail, verdict) {
		t.Fatalf("detail %q does not name the page size; the harness prints it as the check's name", detail)
	}
	if _, _, err := gradeAnswer("no-such-grade"); err == nil {
		t.Error(`grade "no-such-grade" passed; an unknown grade must be an error`)
	}
}

// The tree check reports what the run changed, not what was already in flight.
func TestTreeDelta_IgnoresPreexistingEdits(t *testing.T) {
	before := " M CHANGELOG.md\n?? scratch.txt"
	if got := treeDelta(before, before); got != "" {
		t.Fatalf("delta = %q, want empty for an unchanged tree", got)
	}
	after := before + "\n M foragers/skeptic.md"
	if got := treeDelta(before, after); got != " M foragers/skeptic.md" {
		t.Fatalf("delta = %q, want only the file the run touched", got)
	}
}

// SQLite requires a page size that is a power of two from 512 to 65536; the
// grade reads it from the linked build instead of pinning one.
func TestSQLiteDefaultPageSize_IsAValidSQLitePageSize(t *testing.T) {
	n, err := sqliteDefaultPageSize()
	if err != nil {
		t.Fatal(err)
	}
	if n < 512 || n > 65536 || n&(n-1) != 0 {
		t.Fatalf("page size %d is not a power of two in [512, 65536]", n)
	}
}
