package dreamer

import (
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// The reprove pass flags a guarantee whose dependency has changed since it
// was proven: by a plain update_finding on the dependency, the ordinary way
// a claim changes, or by a later signal touching it, the dreamer's own
// 'audit' signals included.
func TestDepWasModifiedAfter(t *testing.T) {
	store := freshStore(t)

	depID, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", MSSLabel: "assumption", Finding: "a dependency",
		D1: intPtr(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")

	if modified, err := depWasModifiedAfter(store.ReadDB, []int64{depID}, past); err != nil || modified {
		t.Error("an untouched dependency reported as modified")
	}

	// A direct edit is the case the pass exists for.
	if err := store.Findings().UpdateFinding(depID, map[string]any{
		"finding": "the dependency, revised",
	}); err != nil {
		t.Fatal(err)
	}
	if modified, err := depWasModifiedAfter(store.ReadDB, []int64{depID}, past); err != nil || !modified {
		t.Error("a dependency edited by update_finding was not noticed")
	}
}

func TestDepWasModifiedAfter_SeesTheDreamersOwnSignals(t *testing.T) {
	store := freshStore(t)
	depID, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", MSSLabel: "assumption", Finding: "a dependency",
		D1: intPtr(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")

	audit := "audit"
	wave := 1
	if _, err := store.Signals().EmitSignal(
		"stop_signal", &audit, &depID, nil, nil, nil, nil, map[string]any{}, &wave,
	); err != nil {
		t.Fatal(err)
	}
	if modified, err := depWasModifiedAfter(store.ReadDB, []int64{depID}, past); err != nil || !modified {
		t.Error("a stop_signal the dreamer itself emitted did not count as a change")
	}
}

func intPtr(v int) *int { return &v }
