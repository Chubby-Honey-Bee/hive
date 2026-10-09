package gate

import (
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// A count that fails fails the step: read as zero with agent_runs gone,
// "none running" and "every agent completed" would both hold, and the agent
// check would pass a wave that has a finding.
func TestPipeCheckAgents_FailedCountFailsTheStep(t *testing.T) {
	store := newGateStore(t)
	if _, err := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteDB.Exec(`DROP TABLE agent_runs`); err != nil {
		t.Fatal(err)
	}

	sr := pipeCheckAgents(store.ReadDB, 1)
	if sr.passed {
		t.Error("the agent check passed on a count that failed")
	}
	if len(sr.errors) != 1 || !strings.Contains(sr.errors[0], "no such table: agent_runs") {
		t.Errorf("errors = %q, want the failed count's error", sr.errors)
	}

	result, err := RunGatePipeline(store, 1, nil, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Opened {
		t.Errorf("the gate opened on a failed count: %+v", result)
	}
}
