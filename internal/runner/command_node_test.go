package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// commandRun is one run of a workflow through Run with the command helper
// standing in for every program a command node starts.
type commandRun struct {
	store *db.Store
	runID int64
	dir   string
	err   error
}

func runCommandWorkflow(t *testing.T, yaml string, inputs map[string]any, dryRun bool) commandRun {
	t.Helper()
	t.Setenv(commandHelperEnv, "1")
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newTempStore(t)
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML:  wf,
		ProjectDir:    dir,
		DBPath:        store.Path,
		Inputs:        inputs,
		MaxIterations: 20,
		Backend:       &stubBackend{},
		Log:           io.Discard,
		DryRun:        dryRun,
	})
	if res == nil {
		t.Fatalf("Run returned no result: %v", err)
	}
	return commandRun{store: store, runID: res.RunID, dir: dir, err: err}
}

type commandNodeRow struct {
	status, rationale, errText, outputs string
	tokensIn, tokensOut, cost           int64
	provider                            sql.NullString
}

func (r commandRun) node(t *testing.T, name string) commandNodeRow {
	t.Helper()
	var row commandNodeRow
	var rationale, errText, outputs sql.NullString
	if err := r.store.ReadDB.QueryRow(
		`SELECT status, rationale, error, outputs_json, tokens_in, tokens_out, cost_usd_x10000, provider
		 FROM workflow_node_states WHERE run_id=? AND node_name=?`, r.runID, name,
	).Scan(&row.status, &rationale, &errText, &outputs, &row.tokensIn, &row.tokensOut, &row.cost, &row.provider); err != nil {
		t.Fatalf("node %s: %v", name, err)
	}
	row.rationale, row.errText, row.outputs = rationale.String, errText.String, outputs.String
	return row
}

type commandInvocation struct {
	argv     []string
	exitCode int
	stdout   string
	stderr   string
	isError  bool
}

func (r commandRun) invocations(t *testing.T, name string) []commandInvocation {
	t.Helper()
	rows, err := r.store.ReadDB.Query(
		`SELECT tool, input, output, is_error FROM tool_invocations WHERE run_id=? AND node_name=? ORDER BY id`,
		r.runID, name)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []commandInvocation
	for rows.Next() {
		var tool, input, output string
		var isErr int
		if err := rows.Scan(&tool, &input, &output, &isErr); err != nil {
			t.Fatal(err)
		}
		if tool != "command" {
			t.Fatalf("tool = %q, want command", tool)
		}
		inv := commandInvocation{isError: isErr == 1}
		if err := json.Unmarshal([]byte(input), &inv.argv); err != nil {
			t.Fatalf("input %q is not a JSON argv: %v", input, err)
		}
		var o struct {
			ExitCode int    `json:"exit_code"`
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
		}
		if err := json.Unmarshal([]byte(output), &o); err != nil {
			t.Fatalf("output %q is not JSON: %v", output, err)
		}
		inv.exitCode, inv.stdout, inv.stderr = o.ExitCode, o.Stdout, o.Stderr
		out = append(out, inv)
	}
	return out
}

func (r commandRun) state(t *testing.T) map[string]any {
	t.Helper()
	run, err := r.store.Workflows().GetWorkflowRun(r.runID)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(run.StateJSON), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func selfExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// A command node runs its argv with no model: it spends no tokens, records
// its argv and exit code, and only its declared outputs reach state.
func TestCommandNode_RunsWithoutAModel(t *testing.T) {
	printed := `{"a": 7, "b": "not declared"}`
	r := runCommandWorkflow(t, `
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: [chb, print, '`+printed+`']
    outputs_from: stdout_json
    outputs: [a]
edges: []
`, nil, false)
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(printed), &want); err != nil {
		t.Fatal(err)
	}

	row := r.node(t, "relay")
	if row.status != "completed" || row.tokensIn != 0 || row.tokensOut != 0 || row.cost != 0 || row.provider.Valid {
		t.Fatalf("node = %+v, want completed with 0 tokens, 0 cost and no provider", row)
	}
	if row.rationale != printed {
		t.Fatalf("rationale = %q, want the command's stdout %q", row.rationale, printed)
	}
	st := r.state(t)
	if st["a"] != want["a"] {
		t.Fatalf("state a = %v, want %v", st["a"], want["a"])
	}
	if _, leaked := st["b"]; leaked {
		t.Fatalf("undeclared key b reached state: %v", st)
	}

	invs := r.invocations(t, "relay")
	wantArgv := []string{selfExe(t), "print", printed}
	if len(invs) != 1 || !reflect.DeepEqual(invs[0].argv, wantArgv) || invs[0].exitCode != 0 || invs[0].isError || invs[0].stdout != printed {
		t.Fatalf("invocations = %+v, want one row with argv %v, exit 0, stdout %q", invs, wantArgv, printed)
	}
}

// A non-zero exit fails the node with its stderr tail; nothing reads it as
// success.
func TestCommandNode_NonZeroExitFails(t *testing.T) {
	const code, stderr = 3, "boom on stderr"
	r := runCommandWorkflow(t, `
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: [chb, exit, "3", "`+stderr+`"]
edges: []
`, nil, false)
	if r.err == nil {
		t.Fatal("Run succeeded; a failed command must fail the run")
	}
	row := r.node(t, "relay")
	if row.status != "failed" || !strings.Contains(row.errText, "exited 3") || !strings.Contains(row.errText, stderr) {
		t.Fatalf("node = %+v, want failed naming exit %d and %q", row, code, stderr)
	}
	invs := r.invocations(t, "relay")
	if len(invs) != 1 || invs[0].exitCode != code || !invs[0].isError || invs[0].stderr != stderr {
		t.Fatalf("invocations = %+v, want exit %d recorded as an error", invs, code)
	}
}

// A program that is not there fails the node.
func TestCommandNode_MissingBinaryFails(t *testing.T) {
	const missing = "hive-test-no-such-binary"
	_, lookErr := exec.LookPath(missing)
	if lookErr == nil {
		t.Skipf("%s exists on PATH", missing)
	}
	r := runCommandWorkflow(t, `
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: [`+missing+`, hive, init]
    outputs_from: stdout_json
    outputs: [phase]
  after:
    type: command
    argv: [chb, print, "{}"]
edges:
  - {from: relay, to: after}
`, nil, false)
	if r.err == nil {
		t.Fatal("Run succeeded with a missing binary")
	}
	row := r.node(t, "relay")
	if row.status != "failed" || !strings.Contains(row.errText, lookErr.Error()) {
		t.Fatalf("node = %+v, want failed with %q", row, lookErr.Error())
	}
	if after := r.node(t, "after"); after.status != "pending" {
		t.Fatalf("successor status = %q, want pending: it must never run", after.status)
	}
	invs := r.invocations(t, "relay")
	if len(invs) != 1 || invs[0].exitCode != -1 || !invs[0].isError {
		t.Fatalf("invocations = %+v, want one error row with exit -1", invs)
	}
}

// With outputs_from: stdout_json, stdout that lacks a declared key, is not
// JSON, or fails accept: rejects the node.
func TestCommandNode_Rejections(t *testing.T) {
	cases := []struct {
		name, argv, extra, want string
	}{
		{"missing key", `[chb, print, '{"b": 1}']`, "", `stdout has no key "a"`},
		{"not json", `[chb, print, "a = 1"]`, "", "stdout is not one JSON object"},
		{"json array", `[chb, print, "[1, 2]"]`, "", "stdout is not one JSON object"},
		{"accept", `[chb, print, '{"a": 1}']`, "\n    accept: [\"outputs.a == 2\"]", "accept rejected: outputs.a == 2 = false"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runCommandWorkflow(t, `
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: `+c.argv+`
    outputs_from: stdout_json
    outputs: [a]`+c.extra+`
edges: []
`, nil, false)
			if r.err == nil {
				t.Fatal("Run succeeded; a rejected node must fail the run")
			}
			row := r.node(t, "relay")
			if row.status != "rejected" || !strings.Contains(row.rationale, c.want) {
				t.Fatalf("node = %+v, want rejected with %q", row, c.want)
			}
		})
	}
}

// argv and stdin go through the prompts' resolver: a value with spaces stays
// one argument, a list renders as JSON, and the command runs in the project
// directory against the run's database.
func TestCommandNode_TemplatesArgvStdinAndEnvironment(t *testing.T) {
	inputs := map[string]any{"name": "two words", "items": []any{"x", 2.0}}
	r := runCommandWorkflow(t, `
name: cmd
version: 1
inputs: [name, items]
nodes:
  argv:
    type: command
    argv: [chb, args, "{name}", "{items}"]
    outputs_from: stdout_json
    outputs: [args]
  stdin:
    type: command
    argv: [chb, stdin]
    stdin: "{name}"
    outputs_from: stdout_json
    outputs: [stdin]
  env:
    type: command
    argv: [chb, env, HIVE_DB_PATH]
    outputs_from: stdout_json
    outputs: [value]
  pwd:
    type: command
    argv: [chb, pwd]
    outputs_from: stdout_json
    outputs: [dir]
edges:
  - {from: argv, to: stdin}
  - {from: stdin, to: env}
  - {from: env, to: pwd}
`, inputs, false)
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	st := r.state(t)
	items, _ := json.Marshal(inputs["items"])
	wantArgs := []any{inputs["name"], string(items)}
	if !reflect.DeepEqual(st["args"], wantArgs) {
		t.Fatalf("args = %#v, want %#v", st["args"], wantArgs)
	}
	if st["stdin"] != inputs["name"] {
		t.Fatalf("stdin = %v, want %v", st["stdin"], inputs["name"])
	}
	if st["value"] != r.store.Path {
		t.Fatalf("HIVE_DB_PATH = %v, want the run's database %s", st["value"], r.store.Path)
	}
	gotDir, _ := filepath.EvalSymlinks(st["dir"].(string))
	wantDir, _ := filepath.EvalSymlinks(r.dir)
	if gotDir != wantDir {
		t.Fatalf("working directory = %s, want the project directory %s", gotDir, wantDir)
	}
}

// Under --dry-run a command node is not executed.
func TestCommandNode_DryRunDoesNotExecute(t *testing.T) {
	r := runCommandWorkflow(t, `
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: [chb, exit, "1", "must not run"]
edges: []
`, nil, true)
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if row := r.node(t, "relay"); row.status != "completed" {
		t.Fatalf("status = %q, want completed", row.status)
	}
	if invs := r.invocations(t, "relay"); len(invs) != 0 {
		t.Fatalf("dry run executed the command: %+v", invs)
	}
}

// bashEnvBackend answers an agent node by printing one variable through the
// call's bash tool, so it reports what an agent's `chb` would see.
type bashEnvBackend struct {
	name string
	seen string
}

func (b *bashEnvBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	out, err := req.Registry.Handlers["shell"](ctx, map[string]any{"command": `printf '%s' "$` + b.name + `"`})
	if err != nil {
		return nil, err
	}
	b.seen = out
	return &RunResult{FinalText: "ok", Turns: 1, StopReason: "end_turn"}, nil
}

// A relative database path, given or inherited, names the run's database
// from the runner's working directory. Agents and command nodes run in the
// project directory, where the same relative path names another file, so
// each is handed the absolute path: an agent's `chb db-write` and a command
// node's `chb` both write the database the run opened.
func TestRun_RelativeDatabasePathIsMadeAbsolute(t *testing.T) {
	const rel = "run.db"
	for _, fromEnv := range []bool{false, true} {
		t.Run(map[bool]string{false: "given", true: "inherited"}[fromEnv], func(t *testing.T) {
			t.Setenv(commandHelperEnv, "1")
			cwd, project := t.TempDir(), t.TempDir()
			t.Chdir(cwd)
			cfgPath := rel
			if fromEnv {
				t.Setenv("HIVE_DB_PATH", rel)
				cfgPath = ""
			}
			store, err := db.NewStore(rel)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			if err := store.Init(); err != nil {
				t.Fatal(err)
			}
			wf := filepath.Join(project, "wf.yaml")
			if err := os.WriteFile(wf, []byte(`
name: cmd
version: 1
nodes:
  look:
    type: agent
    prompt: look
    outputs: [said]
  env:
    type: command
    argv: [chb, env, HIVE_DB_PATH]
    outputs_from: stdout_json
    outputs: [value]
edges:
  - {from: look, to: env}
`), 0o644); err != nil {
				t.Fatal(err)
			}
			backend := &bashEnvBackend{name: "HIVE_DB_PATH"}
			res, err := Run(context.Background(), store, Config{
				WorkflowYAML: wf, ProjectDir: project, DBPath: cfgPath,
				MaxIterations: 5, Backend: backend, Log: io.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			want, err := filepath.Abs(rel)
			if err != nil {
				t.Fatal(err)
			}
			r := commandRun{store: store, runID: res.RunID}
			if got, _ := r.state(t)["value"].(string); got != want {
				t.Errorf("command node saw HIVE_DB_PATH %q, want %q", got, want)
			}
			if backend.seen != want {
				t.Errorf("agent's bash saw HIVE_DB_PATH %q, want %q", backend.seen, want)
			}
		})
	}
}
