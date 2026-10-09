package hive

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// A project with no hive is told the command that makes one, from every
// verb that needs it, in a message that ends at that command. The missing
// row is the error's cause, which errors.Is still finds, not part of what it
// says.
func TestNotInitialized_NamesTheInitCommand(t *testing.T) {
	s := newTestStore(t)
	_, scanErr := ScanState(s, "demo")
	_, recordErr := RecordScan(s, Scan{Project: "demo"})
	_, completeErr := CompleteIteration(s, Completion{Project: "demo"})
	const want = `hive not initialized for project "demo" (run: chb hive init --project demo)`
	for name, err := range map[string]error{"ScanState": scanErr, "RecordScan": recordErr, "CompleteIteration": completeErr} {
		if err == nil || err.Error() != want || !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("%s: %v; want exactly %q, with sql.ErrNoRows its cause", name, err, want)
		}
	}
}

// A read of the hive's state that fails for another reason says so, and
// does not call the hive uninitialised.
func TestNotInitialized_OnlyWhenThereIsNoRow(t *testing.T) {
	s := newTestStore(t)
	s.Close()
	_, err := ScanState(s, "demo")
	if err == nil || strings.Contains(err.Error(), "not initialized") || errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err = %v; want the read's own error, not the not-initialized one", err)
	}
}
