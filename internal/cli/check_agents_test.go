package cli

import (
	"strings"
	"testing"
)

// checkAgentsOutput runs check-agents for wave 1 and returns what it printed
// and its error.
func checkAgentsOutput(t *testing.T) (string, error) {
	t.Helper()
	cmd := newCheckAgentsCmd()
	cmd.SetArgs([]string{"--wave", "1"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	return captureStdout(t, cmd.Execute)
}

// A running agent that started more than ten minutes ago is reported stale,
// and one that started just now is not. started_at is a TIMESTAMP column,
// which the driver hands back as a time.Time.
func TestCheckAgents_ReportsAStaleRun(t *testing.T) {
	s := useTestStore(t)
	for _, q := range []string{
		`INSERT INTO agent_runs (wave, agent_name, agent_type, status, started_at) VALUES (1, 'slow-scout', 'researcher', 'running', datetime('now', '-1 hour'))`,
		`INSERT INTO agent_runs (wave, agent_name, agent_type, status) VALUES (1, 'fresh-scout', 'researcher', 'running')`,
	} {
		if _, err := s.WriteDB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	out, err := checkAgentsOutput(t)
	if err != nil {
		t.Fatalf("check-agents: %v\n%s", err, out)
	}
	_, stale, found := strings.Cut(out, "STALE AGENTS")
	if !found || !strings.Contains(stale, "slow-scout") || strings.Contains(stale, "fresh-scout") {
		t.Fatalf("want slow-scout, and only it, reported stale:\n%s", out)
	}
}

// check-agents exits non-zero when it cannot read the agent runs, rather
// than report the wave as having no runs.
func TestCheckAgents_FailsWhenTheRunsCannotBeRead(t *testing.T) {
	s := useTestStore(t)
	if _, err := s.WriteDB.Exec(`DROP TABLE agent_runs`); err != nil {
		t.Fatal(err)
	}
	out, err := checkAgentsOutput(t)
	if err == nil || !strings.Contains(err.Error(), "no such table: agent_runs") {
		t.Fatalf("check-agents with no agent_runs table: err = %v, printed %q; want the read's error", err, out)
	}
}
