package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"github.com/spf13/cobra"
)

// The self-validation workflow gates its own verdict in the database `chb
// validate` writes, and final-synthesis reads the same database. Its relays
// are command nodes: exercise-cli runs chb validate and reads its counts as
// JSON, record-verdict writes them as a finding, and open-gate runs chb
// guard on that wave. This test walks the shipped workflow through the
// engine, runs the harness exercise-cli runs, then runs each command node's
// argv against the database it wrote, and requires the gate to open on the
// rows they wrote. The gate's integrity audit reads the whole database, so
// a harness fixture that fails it would block the real run; running the
// harness catches that. A gate that stays shut rejects open-gate.
func TestSelfValidationGatesItsOwnVerdict(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(repoRoot, "workflows", "self-validation.yaml")
	t.Chdir(t.TempDir())
	t.Setenv("HIVE_DB_PATH", "")
	// chb validate's checks read workflows/ and foragers/ from the working
	// directory.
	for _, dir := range []string{"workflows", "foragers"} {
		if err := os.Symlink(filepath.Join(repoRoot, dir), dir); err != nil {
			t.Fatal(err)
		}
	}

	wf, err := db.NewStore("run.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := wf.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wf.Close() })
	repo := wf.Workflows()
	runID, err := workflow.InitWorkflow(repo, yamlPath, map[string]any{"project": "p"})
	if err != nil {
		t.Fatal(err)
	}
	step := func(want string) workflow.DispatchNode {
		t.Helper()
		next, err := workflow.GetNextNodes(repo, runID)
		if err != nil {
			t.Fatal(err)
		}
		if len(next) != 1 || next[0].Node != want {
			t.Fatalf("next = %+v, want only %s", next, want)
		}
		if next[0].CommandError != "" {
			t.Fatalf("%s cannot run: %s", want, next[0].CommandError)
		}
		return next[0]
	}
	complete := func(node string, outputs map[string]any) error {
		t.Helper()
		return workflow.CompleteNode(repo, runID, node, outputs)
	}

	if n := step("init-db"); n.Type != "command" || strings.Join(n.Argv, " ") != "chb db-init" {
		t.Fatalf("init-db = %+v, want the command chb db-init", n)
	}
	if err := complete("init-db", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	exercise := step("exercise-cli")
	ws := ""
	for i, a := range exercise.Argv {
		if a == "--workspace" && i+1 < len(exercise.Argv) {
			ws = exercise.Argv[i+1]
		}
	}
	if exercise.Type != "command" || exercise.Argv[1] != "validate" || ws == "" || !slices.Contains(exercise.Argv, "--json") {
		t.Fatalf("exercise-cli = %q, want the command chb validate --workspace <dir> --json", exercise.Argv)
	}
	if !slices.Contains(exercise.OkExit, 1) {
		t.Fatalf("exercise-cli ok_exit = %v, want 1 listed: a failed check routes to triage", exercise.OkExit)
	}
	// Run the harness the node runs, against a chb built from this tree. It
	// is built apart from this test binary so a -race run does not put the
	// race detector under the harness's hundred runs of chb. Checks that
	// need what a temp directory lacks, such as chb-mcp, fail; the database
	// the rest write is what the gate judges.
	chb := filepath.Join(t.TempDir(), "chb")
	build := exec.Command("go", "build", "-o", chb, "./cmd/chb")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/chb: %v\n%s", err, out)
	}
	// The harness may export a placeholder key; t.Setenv restores the real one.
	t.Setenv("ANTHROPIC_API_KEY", os.Getenv("ANTHROPIC_API_KEY"))
	var printed strings.Builder
	harness := &validateRunner{workspaceDir: ws, chbBin: chb, asJSON: true, stdout: &printed, stderr: io.Discard}
	_ = harness.run(context.Background())
	t.Logf("chb validate: %d passed, %d failed %v", harness.pass, harness.fail, harness.failures)
	var counts map[string]any
	if err := json.Unmarshal([]byte(printed.String()), &counts); err != nil {
		t.Fatalf("chb validate --json printed %q: %v", printed.String(), err)
	}
	for _, k := range exercise.Outputs {
		if _, ok := counts[k]; !ok {
			t.Fatalf("chb validate --json has no %q, which exercise-cli declares: %v", k, counts)
		}
	}

	// Take the verdict as though every check passed, which is the branch
	// that reaches the gate.
	verdict := map[string]any{"passed": counts["passed"], "failed": float64(0), "failures": []any{}, "log": counts["log"]}
	if err := complete("exercise-cli", verdict); err != nil {
		t.Fatal(err)
	}

	hs, err := db.NewStore(harness.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := hs.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hs.Close() })
	prev := store
	store = hs
	t.Cleanup(func() { store = prev })
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := hs.ReadDB.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// run runs a command node's chb argv in-process against the harness's
	// database, which the argv must name with --db.
	run := func(n workflow.DispatchNode) string {
		t.Helper()
		if n.Type != "command" || n.Argv[0] != "chb" {
			t.Fatalf("%s = %+v, want a chb command node", n.Node, n)
		}
		root := &cobra.Command{Use: "chb"}
		root.AddCommand(newDBWriteCmd(), newGuardCmd())
		out, err := execute(t, root, withoutDB(t, n.Argv[1:], harness.dbPath)...)
		if err != nil && !slices.Contains(n.OkExit, 1) {
			t.Fatalf("chb %s: %v\n%s", strings.Join(n.Argv[1:], " "), err, out)
		}
		return out
	}

	record := step("record-verdict")
	gateWave := -1
	// The harness filled the database, and left the verdict's wave empty.
	if count(`SELECT COUNT(*) FROM findings`) == 0 {
		t.Fatalf("chb validate wrote no findings (failures: %v)", harness.failures)
	}
	run(record)
	if err := complete("record-verdict", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	gate := step("open-gate")
	for i, a := range gate.Argv {
		if a == "--wave" && i+1 < len(gate.Argv) {
			if gateWave, err = strconv.Atoi(gate.Argv[i+1]); err != nil {
				t.Fatal(err)
			}
		}
		if a == "--force" {
			t.Errorf("open-gate forces the gate: %q", gate.Argv)
		}
	}
	if gateWave < 0 || gate.Argv[3] != "guard" {
		t.Fatalf("open-gate runs no chb guard --wave N: %q", gate.Argv)
	}
	// The one finding record-verdict wrote is the verdict's wave, and it
	// states the counts chb validate printed.
	if n := count(`SELECT COUNT(*) FROM findings WHERE wave = ?`, gateWave); n != 1 {
		t.Fatalf("wave %d has %d findings, want the verdict alone", gateWave, n)
	}
	var agent, text string
	if err := hs.ReadDB.QueryRow(`SELECT agent, finding FROM findings WHERE wave = ?`, gateWave).Scan(&agent, &text); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("chb validate passed %v checks and failed %v", verdict["passed"], verdict["failed"]); agent != "self-validation" || text != want {
		t.Errorf("verdict finding = %q by %q, want %q by self-validation", text, agent, want)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(run(gate)), &got); err != nil {
		t.Fatal(err)
	}
	if got["opened"] != true {
		t.Fatalf("the gate stayed shut on the verdict's wave: %v", got)
	}
	outputs := map[string]any{}
	for _, k := range gate.Outputs {
		outputs[k] = got[k]
	}
	var rej *workflow.AcceptRejection
	if err := complete("open-gate", map[string]any{"opened": false, "errors": []any{"gate BLOCKED"}}); !errors.As(err, &rej) {
		t.Errorf("a blocked gate is accepted: err = %v", err)
	}
	if err := complete("open-gate", outputs); err != nil {
		t.Fatalf("an opened gate is refused: %v", err)
	}
	synth := step("final-synthesis")
	cmds := chbCommands(synth.ResolvedPrompt)
	if len(cmds) == 0 {
		t.Fatalf("final-synthesis lists no chb command:\n%s", synth.ResolvedPrompt)
	}
	for _, args := range cmds {
		if storeFreePath(args) {
			continue
		}
		withoutDB(t, args, harness.dbPath)
	}
}

// chbCommands returns the argument lists of the prompt's lines that run chb,
// split as a shell splits them, with single quotes grouping.
func chbCommands(prompt string) [][]string {
	var cmds [][]string
	for _, line := range strings.Split(prompt, "\n") {
		words := shellWords(strings.TrimSpace(line))
		if len(words) > 1 && words[0] == "chb" {
			cmds = append(cmds, words[1:])
		}
	}
	return cmds
}

func shellWords(line string) []string {
	var words []string
	var cur strings.Builder
	inWord, quoted := false, false
	for _, r := range line {
		switch {
		case r == '\'':
			quoted, inWord = !quoted, true
		case !quoted && (r == ' ' || r == '\t'):
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

// withoutDB requires args to start with --db want and returns the rest.
func withoutDB(t *testing.T, args []string, want string) []string {
	t.Helper()
	if len(args) < 3 || args[0] != "--db" || args[1] != want {
		t.Fatalf("chb %s does not name --db %s, the database chb validate writes", strings.Join(args, " "), want)
	}
	return args[2:]
}
