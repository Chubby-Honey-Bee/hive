package cli

import (
	"context"
	"database/sql"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// chb validate starts workflow runs and an agent run in its workspace, and
// every run it starts ends, completed or failed, so `chb workflow runs`
// lists none of them as live.
func TestValidate_LeavesNoRunRunning(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Setenv("HIVE_DB_PATH", "")
	for _, dir := range []string{"workflows", "foragers", "agents"} {
		if err := os.Symlink(filepath.Join(repoRoot, dir), dir); err != nil {
			t.Fatal(err)
		}
	}
	// Built apart from this test binary, as TestSelfValidationGatesItsOwnVerdict
	// does, so a -race run does not slow the harness's runs of chb.
	chb := filepath.Join(t.TempDir(), "chb")
	build := exec.Command("go", "build", "-o", chb, "./cmd/chb")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/chb: %v\n%s", err, out)
	}
	t.Setenv("ANTHROPIC_API_KEY", os.Getenv("ANTHROPIC_API_KEY"))
	harness := &validateRunner{workspaceDir: filepath.Join(t.TempDir(), "validate"), chbBin: chb, stdout: io.Discard, stderr: io.Discard}
	_ = harness.run(context.Background())
	t.Logf("chb validate: %d passed, %d failed %v", harness.pass, harness.fail, harness.failures)

	conn, err := sql.Open("sqlite", "file:"+harness.dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rows, err := conn.Query(`SELECT 'workflow', workflow_name, id, status FROM workflow_runs
		UNION ALL SELECT 'agent', agent_name, id, status FROM agent_runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	agentRuns := 0
	for rows.Next() {
		var kind, name, status string
		var id int64
		if err := rows.Scan(&kind, &name, &id, &status); err != nil {
			t.Fatal(err)
		}
		if kind == "workflow" {
			seen[name] = true
		} else {
			agentRuns++
		}
		if status != "completed" && status != "failed" {
			t.Errorf("%s run %d (%s) is %q after chb validate; want it ended, completed or failed", kind, id, name, status)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"margin-check", "v25"} {
		if !seen[name] {
			t.Errorf("chb validate started no %s run; the check has nothing to judge (failures: %v)", name, harness.failures)
		}
	}
	if agentRuns == 0 {
		t.Errorf("chb validate started no agent run; the check has nothing to judge (failures: %v)", harness.failures)
	}
}
