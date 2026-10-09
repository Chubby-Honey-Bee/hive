package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A placeholder no state value fills fails the node before anything runs:
// the program never sees `{finding_id}` as an argument.
func TestCommandNode_MissingPlaceholderFailsWithoutRunning(t *testing.T) {
	r := runCommandWorkflow(t, `
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: [chb, args, "{finding_id}"]
    outputs_from: stdout_json
    outputs: [args]
edges: []
`, nil, false)
	if r.err == nil {
		t.Fatal("Run succeeded with an unfilled placeholder")
	}
	row := r.node(t, "relay")
	if row.status != "failed" || !strings.Contains(row.errText, "{finding_id}") {
		t.Fatalf("node = %+v, want failed naming {finding_id}", row)
	}
	if invs := r.invocations(t, "relay"); len(invs) != 0 {
		t.Fatalf("the command ran: %+v", invs)
	}
}

// ok_exit names the exit codes that complete the node, as `chb guard` exits
// 1 on a closed gate after printing its verdict. Any other code still fails.
func TestCommandNode_OkExit(t *testing.T) {
	const printed = `{"opened": false}`
	cases := []struct {
		name   string
		code   int
		status string
	}{
		{"listed code completes", 1, "completed"},
		{"unlisted code fails", 2, "failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runCommandWorkflow(t, `
name: cmd
version: 1
nodes:
  gate:
    type: command
    argv: [chb, stdoutexit, "`+strconv.Itoa(c.code)+`", '`+printed+`']
    outputs_from: stdout_json
    outputs: [opened]
    ok_exit: [0, 1]
edges: []
`, nil, false)
			row := r.node(t, "gate")
			if row.status != c.status {
				t.Fatalf("status = %q (%s), want %q", row.status, row.errText, c.status)
			}
			invs := r.invocations(t, "gate")
			if len(invs) != 1 || invs[0].exitCode != c.code || invs[0].isError != (c.status == "failed") {
				t.Fatalf("invocations = %+v, want exit %d with is_error %v", invs, c.code, c.status == "failed")
			}
			if c.status == "completed" {
				if got, ok := r.state(t)["opened"]; !ok || got != false {
					t.Fatalf("opened = %v, want the printed false in state", got)
				}
			}
		})
	}
}

// `go test` reports its failures on stdout. A failure with nothing on stderr
// quotes the tail of stdout, so the node's error says what failed.
func TestCommandNode_FailureQuotesStdoutWhenStderrIsEmpty(t *testing.T) {
	const out = "--- FAIL: TestSomething"
	r := runCommandWorkflow(t, `
name: cmd
version: 1
nodes:
  gate:
    type: command
    argv: [chb, stdoutexit, "1", "`+out+`"]
edges: []
`, nil, false)
	row := r.node(t, "gate")
	if row.status != "failed" || !strings.Contains(row.errText, out) {
		t.Fatalf("node = %+v, want failed quoting %q", row, out)
	}
}

// Config.ChbPath names the program `chb` runs, as chb-mcp sets it: its own
// binary is not chb.
func TestCommandNode_ChbPathNamesTheProgram(t *testing.T) {
	t.Setenv(commandHelperEnv, "1")
	dir := t.TempDir()
	link := filepath.Join(dir, "chb-elsewhere")
	if err := os.Symlink(selfExe(t), link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(`
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: [chb, print, "{}"]
edges: []
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newTempStore(t)
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML: wf, ProjectDir: dir, DBPath: store.Path, ChbPath: link,
		MaxIterations: 5, Backend: &stubBackend{}, Log: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	invs := commandRun{store: store, runID: res.RunID}.invocations(t, "relay")
	if len(invs) != 1 || invs[0].argv[0] != link || invs[0].exitCode != 0 {
		t.Fatalf("invocations = %+v, want one run of %s", invs, link)
	}
}

// A workflow of command nodes calls no model, so it runs with no backend and
// no API key.
func TestRun_CommandOnlyWorkflowNeedsNoBackend(t *testing.T) {
	t.Setenv(commandHelperEnv, "1")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(`
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: [chb, print, "{}"]
edges: []
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newTempStore(t)
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML: wf, ProjectDir: dir, DBPath: store.Path, Provider: "openai",
		MaxIterations: 5, Log: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if row := (commandRun{store: store, runID: res.RunID}).node(t, "relay"); row.status != "completed" {
		t.Fatalf("status = %q, want completed", row.status)
	}
}
