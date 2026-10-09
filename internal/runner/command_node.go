package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// commandNodeTimeout bounds one command node, as the per-node budget bounds
// an agent node.
const commandNodeTimeout = 30 * time.Minute

// commandTailBytes is how much of a command's stdout and stderr the audit
// row keeps, and how much stderr a failure message quotes.
const commandTailBytes = 4000

// executeCommandNode runs a command node: its argv, with no shell and no
// model. The runner, not the command, decides the outcome. A command that
// cannot start, or exits with a code outside its ok_exit (0 alone by
// default), fails the node with the tail of its stderr, or of its stdout
// when stderr is empty. So does a node whose argv cannot be run as given,
// before anything runs. With outputs_from: stdout_json, stdout must be one
// JSON object holding every declared output; anything else rejects the
// node. Only the declared keys reach state. The node spends no tokens; its
// argv, exit code and output tails are one tool_invocations row.
func (rc *runtimeContext) executeCommandNode(node workflow.DispatchNode) (stop bool) {
	if rc.cfg.DryRun {
		rc.dryRunCommandNode(node)
		return false
	}
	if node.CommandError != "" {
		rc.failNode(node, "FAILED", "command not run: "+node.CommandError)
		return false
	}
	res := rc.runCommandNode(node)
	if res.err != nil {
		rc.commandNodeFailed(node, res)
		return false
	}
	return rc.completeCommandNode(node, res)
}

// dryRunCommandNode logs what a command node would run, and why it cannot
// when its argv cannot be run as given, and completes it with synthetic
// outputs (completeDryRun).
func (rc *runtimeContext) dryRunCommandNode(node workflow.DispatchNode) {
	rc.logf("[dry-run] would run %s: %s", node.Node, strings.Join(node.Argv, " "))
	if node.CommandError != "" {
		rc.logf("[dry-run] %s: %s", node.Node, node.CommandError)
	}
	rc.completeDryRun(node, `{"dry_run":true}`)
}

// runCommandNode runs a command node's argv (runCommand) under
// commandNodeTimeout, and records it as one tool_invocations row.
func (rc *runtimeContext) runCommandNode(node workflow.DispatchNode) commandResult {
	ctx, cancel := context.WithTimeout(rc.ctx, commandNodeTimeout)
	defer cancel()
	res := runCommand(ctx, commandSpec{
		argv: node.Argv, stdin: node.Stdin, dir: rc.cfg.ProjectDir,
		env: agentEnv(rc.cfg.DBPath), chb: rc.cfg.ChbPath, okExit: node.OkExit,
	})
	rc.recordInvocations(node, &RunResult{Invocations: []ToolInvocation{res.invocation()}})
	return res
}

// commandNodeFailed settles a command node whose command failed. The run's
// own cancellation is not the node's failure: the node goes back to
// pending (releaseNode).
func (rc *runtimeContext) commandNodeFailed(node workflow.DispatchNode, res commandResult) {
	if rc.ctx.Err() != nil {
		rc.releaseNode(node)
		return
	}
	rc.failNode(node, "FAILED", res.err.Error())
}

// completeCommandNode completes a command node with the outputs its stdout
// holds (commandOutputs), and keeps its stdout as the node's rationale.
// Stdout that does not qualify, and a rejected accept: predicate, reject
// the node (rejectCommandNode). Returns true if OnlyWave is reached.
func (rc *runtimeContext) completeCommandNode(node workflow.DispatchNode, res commandResult) (stop bool) {
	outputs, problem := commandOutputs(node, res.stdout)
	if problem != "" {
		rc.rejectCommandNode(node, res, "command output rejected: "+problem)
		return false
	}
	if err := workflow.CompleteNode(rc.store.Workflows(), rc.runID, node.Node, outputs); err != nil {
		rc.refuseCompletion(node, err, func(rej *workflow.AcceptRejection) {
			rc.rejectCommandNode(node, res, acceptRejectionRationale(rej))
		})
		return false
	}
	rc.countNodeRun()
	rc.recordCompletion(node, res.stdout)
	return rc.onlyWaveReached(node)
}

// rejectCommandNode marks a command node rejected, which is terminal: a
// command given the same state writes the same output, so there is nothing
// for a retry or a repair to change. The rationale ends with the exit code
// and the tail of stdout, so it shows what the program said, such as the
// errors of a gate that stayed closed.
func (rc *runtimeContext) rejectCommandNode(node workflow.DispatchNode, res commandResult, rationale string) {
	rationale += fmt.Sprintf("\nexit %d; stdout: %s", res.exitCode, tailOf(strings.TrimSpace(res.stdout), 1000))
	now := time.Now().UTC().Format(time.RFC3339)
	if err := rc.store.Workflows().MarkNodeRejected(rc.runID, node.Node, rationale, now); err != nil {
		rc.logf("mark rejected (run %d node %s): %v", rc.runID, node.Node, err)
	}
	rc.logf("node %s REJECTED: %s", node.Node, rationale)
}

// commandOutputs reads a command node's outputs from its stdout. Without
// outputs_from there are none. With stdout_json, stdout must be one JSON
// object, and the outputs are its declared keys, every one of which must be
// present. It returns the problem when stdout does not qualify.
func commandOutputs(node workflow.DispatchNode, stdout string) (map[string]any, string) {
	if node.OutputsFrom != "stdout_json" {
		return map[string]any{}, ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err != nil || parsed == nil {
		return nil, "stdout is not one JSON object"
	}
	return declaredCommandOutputs(parsed, node.Outputs)
}

// declaredCommandOutputs is the declared keys of a command's JSON stdout,
// or the first key it lacks as the problem.
func declaredCommandOutputs(parsed map[string]any, keys []string) (map[string]any, string) {
	outputs := map[string]any{}
	for _, k := range keys {
		v, ok := parsed[k]
		if !ok {
			return nil, fmt.Sprintf("stdout has no key %q", k)
		}
		outputs[k] = v
	}
	return outputs, ""
}

// commandResult is one execution of a command node's argv.
type commandResult struct {
	argv      []string // as executed: `chb` resolved to this binary
	exitCode  int      // -1 when the command never started or was killed
	stdout    string
	stderr    string
	durationS float64
	err       error // nil only for a command that ran and exited with an ok code
}

// commandSpec is what runCommand runs: argv with no shell, in dir, with env
// and stdin. chb, when set, is the program the name `chb` runs; empty, it is
// the running binary. okExit lists the exit codes that succeed; nil is 0
// alone.
type commandSpec struct {
	argv   []string
	stdin  string
	dir    string
	env    []string
	chb    string
	okExit []int
}

// runCommand executes a command spec with no shell. The name `chb` resolves
// to spec.chb, else to the running binary, so a workflow drives the same chb
// that runs it, whatever is on PATH. chb-mcp, whose own binary is not chb,
// names the chb beside it. Any other name is looked up on PATH. The program
// runs in a process group of its own, and a timeout or a cancelled run
// kills the whole group, so no program it started outlives the node.
func runCommand(ctx context.Context, spec commandSpec) commandResult {
	r := commandResult{argv: append([]string(nil), spec.argv...), exitCode: -1}
	if err := resolveCommandArgv(r.argv, spec.chb); err != nil {
		r.err = err
		return r
	}
	runErr := execCommandNode(ctx, &r, spec)
	r.err = commandExitErr(ctx, &r, runErr, spec.okExit)
	return r
}

// resolveCommandArgv resolves, in place, the program argv names: the name
// `chb` runs chb, else the running binary (commandChbProgram). An empty
// argv names none.
func resolveCommandArgv(argv []string, chb string) error {
	if len(argv) == 0 {
		return errors.New("command node has no argv")
	}
	if argv[0] != "chb" {
		return nil
	}
	exe, err := commandChbProgram(chb)
	if err != nil {
		return err
	}
	argv[0] = exe
	return nil
}

// commandChbProgram is the program the name `chb` runs: chb when set, else
// the running binary.
func commandChbProgram(chb string) (string, error) {
	if chb != "" {
		return chb, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve chb: %w", err)
	}
	return exe, nil
}

// execCommandNode runs r.argv with no shell, in spec.dir with spec.env and
// spec.stdin, in a process group of its own (killProcessGroupOnCancel), and
// keeps its output and duration in r. It returns cmd.Run's error.
func execCommandNode(ctx context.Context, r *commandResult, spec commandSpec) error {
	cmd := exec.CommandContext(ctx, r.argv[0], r.argv[1:]...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	killProcessGroupOnCancel(cmd)
	// A grandchild that keeps stdout open would otherwise hold Wait past a
	// timeout's kill.
	cmd.WaitDelay = 5 * time.Second
	if spec.stdin != "" {
		cmd.Stdin = strings.NewReader(spec.stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	runErr := cmd.Run()
	r.durationS = time.Since(start).Seconds()
	r.stdout, r.stderr = stdout.String(), stderr.String()
	return runErr
}

// commandExitErr is the error of a command that ran with runErr: nil when
// it exited with an ok code (okExit, 0 alone by default) before its
// context ended, else why it failed. It sets r's exit code when the
// command ran.
func commandExitErr(ctx context.Context, r *commandResult, runErr error, okExit []int) error {
	code, err := commandExitCode(runErr)
	if err != nil {
		return err
	}
	r.exitCode = code
	if ctx.Err() == nil && slices.Contains(okExitOrZero(okExit), code) {
		return nil
	}
	return r.failure(ctx)
}

// commandExitCode is the exit code cmd.Run's error reports, or why the
// command did not run.
func commandExitCode(runErr error) (int, error) {
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return 0, nil
	case errors.As(runErr, &exitErr):
		return exitErr.ExitCode(), nil
	}
	return -1, fmt.Errorf("command did not run: %w", runErr)
}

// failure is the error of a command that exited outside its ok codes, or
// past its context's end: its exit code, the context's error, and the tail
// of its stderr, or of its stdout when stderr is empty.
func (r commandResult) failure(ctx context.Context) error {
	msg := fmt.Sprintf("command exited %d", r.exitCode)
	if ctx.Err() != nil {
		msg += " (" + ctx.Err().Error() + ")"
	}
	if tail := r.failureTail(); tail != "" {
		msg += ": " + tail
	}
	return errors.New(msg)
}

// failureTail is the output a failure quotes: the tail of stderr, else of
// stdout, since go test reports its failures on stdout.
func (r commandResult) failureTail() string {
	if tail := tailOf(strings.TrimSpace(r.stderr), commandTailBytes); tail != "" {
		return tail
	}
	return tailOf(strings.TrimSpace(r.stdout), commandTailBytes)
}

// okExitOrZero is a node's ok_exit, or 0 alone when it names none.
func okExitOrZero(codes []int) []int {
	if len(codes) == 0 {
		return []int{0}
	}
	return codes
}

// invocation is the tool_invocations row for one command: the argv as
// executed, and the exit code with the tails of stdout and stderr, as JSON.
func (r commandResult) invocation() ToolInvocation {
	in, _ := json.Marshal(r.argv)
	out := r.outputJSON(commandTailBytes)
	// recordInvocations keeps 20,000 bytes of output. Escaping can grow a
	// tail several times over, so shrink the tails rather than let the
	// JSON be cut.
	if len(out) > 20_000 {
		out = r.outputJSON(1000)
	}
	return ToolInvocation{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Tool:      "command",
		Input:     string(in),
		Output:    out,
		IsError:   r.err != nil,
		DurationS: r.durationS,
	}
}

func (r commandResult) outputJSON(tail int) string {
	b, _ := json.Marshal(map[string]any{
		"exit_code": r.exitCode,
		"stdout":    tailOf(r.stdout, tail),
		"stderr":    tailOf(r.stderr, tail),
	})
	return string(b)
}

// tailOf returns the last n bytes of s, cut forward to a rune boundary.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}
