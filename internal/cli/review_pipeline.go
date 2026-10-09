package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// reviewPipeline is the native (no-shell-script) orchestration of the
// six-stage self-review flow. Each stage maps to an existing cobra
// command (preflight / agent-run / extract-findings / render-review)
// or to a Go-side helper. Stages run sequentially and stop on the
// first error.
type reviewPipeline struct {
	target       string
	workflow     string
	workspaceDir string
	provider     string
	profile      string // the routing profile preflight and agent-run apply
	humanGate    bool
	noRegression bool
	stdout       io.Writer
	stderr       io.Writer
	stdin        *os.File
}

// run executes the pipeline. Returns nil only if every stage
// succeeded (modulo --no-regression / --human aborts which propagate
// as their own errors).
func (p *reviewPipeline) run() error {
	reviewPath, err := p.produceReview()
	if err != nil {
		return err
	}
	fmt.Fprintf(p.stderr, "\n── REVIEW.md rendered → %s ──\n", reviewPath)
	if err := p.reviewGate(); err != nil {
		return err
	}
	if err := p.regressionGate(); err != nil {
		return err
	}
	fmt.Fprintln(p.stderr, "\n════════════════════════════════════════════════════════════")
	fmt.Fprintln(p.stderr, "  REVIEW COMPLETE")
	fmt.Fprintf(p.stderr, "  Report:  %s\n", reviewPath)
	fmt.Fprintln(p.stderr, "════════════════════════════════════════════════════════════")
	return nil
}

// produceReview runs the stages that end in REVIEW.md — workspace,
// preflight, agent-run, findings extraction and rendering — and returns the
// report's path.
func (p *reviewPipeline) produceReview() (string, error) {
	if err := p.prepareWorkspace(); err != nil {
		return "", fmt.Errorf("prepare workspace: %w", err)
	}
	if err := p.preflight(); err != nil {
		return "", fmt.Errorf("preflight: %w", err)
	}
	runID, err := p.dispatchAgentRun()
	if err != nil {
		return "", fmt.Errorf("agent-run: %w", err)
	}
	return p.reportRun(runID)
}

// reportRun extracts the run's findings and renders them as REVIEW.md,
// whose path it returns.
func (p *reviewPipeline) reportRun(runID int64) (string, error) {
	findingsPath, err := p.extractFindings(runID)
	if err != nil {
		return "", fmt.Errorf("extract-findings: %w", err)
	}
	reviewPath, err := p.renderReview(findingsPath)
	if err != nil {
		return "", fmt.Errorf("render-review: %w", err)
	}
	return reviewPath, nil
}

// reviewGate pauses for a human once REVIEW.md is rendered, when --human
// asks for it.
func (p *reviewPipeline) reviewGate() error {
	if !p.humanGate {
		return nil
	}
	return pauseForHuman(p.stderr, p.stdin,
		"REVIEW.md rendered. Inspect the report, then press Enter to continue to the regression gate, or 'n' to abort.")
}

// regressionGate runs the regression stage unless --no-regression skips it.
func (p *reviewPipeline) regressionGate() error {
	if p.noRegression {
		return nil
	}
	if err := p.regression(); err != nil {
		return fmt.Errorf("regression: %w", err)
	}
	return nil
}

// prepareWorkspace ensures the workspace dir exists.
func (p *reviewPipeline) prepareWorkspace() error {
	fmt.Fprintf(p.stderr, "── 1/6 PREPARING WORKSPACE ──\n")
	return os.MkdirAll(p.workspaceDir, 0o755)
}

// preflight re-enters the cobra root with `preflight <workflow>`.
// Re-entering avoids duplicating preflight's logic and inherits any
// future preflight extensions automatically.
func (p *reviewPipeline) preflight() error {
	fmt.Fprintf(p.stderr, "\n── 2/6 PREFLIGHT ──\n")
	args := append([]string{"preflight", p.workflow}, p.profileArgs()...)
	return p.runChb(args)
}

// profileArgs pass the pipeline's routing profile to a stage.
func (p *reviewPipeline) profileArgs() []string {
	if p.profile == "" {
		return nil
	}
	return []string{"--profile", p.profile}
}

// dispatchAgentRun launches agent-run with the workflow.
// Sets HIVE_DB_PATH on the child process so all downstream
// subcommands (agent-run + extract-findings + render-review) operate
// on the same workspace-scoped DB rather than the cwd default.
// Returns the run id agent-run reports. The workspace DB is kept across
// reviews, so a second review into it is run 2, not 1.
func (p *reviewPipeline) dispatchAgentRun() (int64, error) {
	fmt.Fprintf(p.stderr, "\n── 3/6 LAUNCHING agent-run ──\n")
	fmt.Fprintf(p.stderr, "  log: %s/run.log\n", p.workspaceDir)

	// Pin the workspace DB for every child command in this pipeline.
	// runChb uses os.Environ(), so setting it here flows through.
	dbPath := filepath.Join(p.workspaceDir, "hive.db")
	os.Setenv("HIVE_DB_PATH", dbPath)

	args := []string{
		"agent-run", p.workflow,
		// --dir states the project root rather than inheriting it, and an
		// empty --branch stops a read-only audit from creating
		// validate/auto-corrections and committing into the tree it is
		// only supposed to be reading.
		"--dir", p.target,
		"--branch", "",
		"--inputs", fmt.Sprintf(`{"target_root":"%s"}`, p.target),
	}
	if p.provider != "" {
		args = append(args, "--provider", p.provider)
	}
	args = append(args, p.profileArgs()...)
	sniff := &runIDWriter{w: p.stderr}
	if err := p.runChbTo(sniff, args); err != nil {
		return 0, err
	}
	if sniff.id == 0 {
		return 0, fmt.Errorf("agent-run exited without reporting a workflow run ID")
	}
	return sniff.id, nil
}

// agentRunIDPrefix starts the line agent-run logs once it has created its run.
const agentRunIDPrefix = "[agent-run] workflow run ID: "

// runIDWriter passes agent-run's stderr through unchanged and keeps the run
// id from the first agentRunIDPrefix line it sees.
type runIDWriter struct {
	w    io.Writer
	line []byte
	id   int64
}

// Write passes b through to the wrapped writer, looking for the run id in
// it until one is found.
func (r *runIDWriter) Write(b []byte) (int, error) {
	if r.id == 0 {
		r.scan(b)
	}
	return r.w.Write(b)
}

// scan adds b to the partial line held over from the last write and reads
// the run id from each line b completes, stopping at the first.
func (r *runIDWriter) scan(b []byte) {
	r.line = append(r.line, b...)
	for r.id == 0 {
		i := bytes.IndexByte(r.line, '\n')
		if i < 0 {
			break
		}
		r.id = agentRunIDFromLine(r.line[:i])
		r.line = r.line[i+1:]
	}
	if r.id != 0 {
		r.line = nil
	}
}

// agentRunIDFromLine is the positive run id an agentRunIDPrefix line
// reports, or 0 for any other line.
func agentRunIDFromLine(line []byte) int64 {
	v, ok := strings.CutPrefix(strings.TrimSpace(string(line)), agentRunIDPrefix)
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// extractFindings turns rationale columns → findings.json. The
// extract-findings subcommand reads from HIVE_DB_PATH (set by
// dispatchAgentRun above) and pulls every audit-* node by default.
func (p *reviewPipeline) extractFindings(runID int64) (string, error) {
	fmt.Fprintf(p.stderr, "\n── 4/6 EXTRACTING FINDINGS ──\n")
	out := filepath.Join(p.workspaceDir, "findings.json")
	args := []string{
		"extract-findings", "--run-id", strconv.FormatInt(runID, 10),
		"--out", out,
	}
	if err := p.runChb(args); err != nil {
		return "", err
	}
	return out, nil
}

// renderReview turns findings.json → REVIEW.md.
func (p *reviewPipeline) renderReview(findingsPath string) (string, error) {
	fmt.Fprintf(p.stderr, "\n── 5/6 RENDERING REVIEW.md ──\n")
	out := filepath.Join(p.workspaceDir, "REVIEW.md")
	args := []string{
		"render-review", findingsPath,
		"--out", out,
	}
	if err := p.runChb(args); err != nil {
		return "", err
	}
	return out, nil
}

// regression runs `go test ./...` + `chb validate` against the
// target repo. Surfaces failures without re-rendering REVIEW.md.
func (p *reviewPipeline) regression() error {
	fmt.Fprintf(p.stderr, "\n── 6/6 REGRESSION ──\n")
	fmt.Fprintln(p.stderr, "  go test ./...")
	if err := p.runShell("go", "test", "./..."); err != nil {
		return fmt.Errorf("go test: %w", err)
	}
	fmt.Fprintln(p.stderr, "  ✓ go test ./... clean")

	fmt.Fprintln(p.stderr, "  chb validate")
	if err := p.runChb([]string{"validate"}); err != nil {
		return fmt.Errorf("chb validate: %w", err)
	}
	fmt.Fprintln(p.stderr, "  ✓ chb validate clean")
	return nil
}

// runChb re-enters the cobra root with the supplied args. We
// fork the same binary (os.Args[0]) so the child inherits any
// build-time flags + uses identical code paths, instead of trying to
// recurse into the in-process root command (which would inherit the
// wrong flag state).
func (p *reviewPipeline) runChb(args []string) error {
	return p.runChbTo(p.stderr, args)
}

// runChbTo is runChb with the child's stderr sent to stderr.
func (p *reviewPipeline) runChbTo(stderr io.Writer, args []string) error {
	bin, err := os.Executable()
	if err != nil {
		bin = os.Args[0] // fallback for symlinked invocations
	}
	return p.runShellTo(stderr, bin, args...)
}

// stageStopGrace is how long a stage's child has to exit after SIGTERM
// before it is killed.
const stageStopGrace = 10 * time.Second

// errStageStopped marks a stage that a SIGINT or SIGTERM stopped.
var errStageStopped = errors.New("stopped by a signal")

// runShell runs one stage, a program other than chb, with its stderr sent
// to the pipeline's.
func (p *reviewPipeline) runShell(name string, args ...string) error {
	return p.runShellTo(p.stderr, name, args...)
}

// runShellTo runs one stage. A SIGINT or SIGTERM that arrives while the
// stage runs is passed on to its child as SIGTERM, and the stage then fails,
// so the pipeline stops: terminating `chb review` or `chb implement` (the
// pid chb_self_review and chb_self_implement return) ends its agent-run and
// the model call under it, not only the wrapper. The handler is held only
// while a child runs, so a signal between stages, at a --human gate say,
// still ends the process at once.
func (p *reviewPipeline) runShellTo(stderr io.Writer, name string, args ...string) error {
	stopped, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(stopped, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// SIGTERM, not CommandContext's SIGKILL: agent-run handles SIGTERM by
	// cancelling its run, and a killed agent-run leaves its model call running.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = stageStopGrace
	// Every stage runs in the target repo, so preflight, the agent-run
	// (whose ProjectDir defaults to the process cwd), the git branch it
	// creates and auto-commits to, and the `go test ./...` + `chb validate`
	// regression gate all act on --target, not on the reviewing checkout.
	cmd.Dir = p.target
	cmd.Stdout = p.stdout
	cmd.Stderr = stderr
	cmd.Stdin = p.stdin
	cmd.Env = os.Environ()
	err := cmd.Run()
	if stopped.Err() != nil {
		return fmt.Errorf("%s %w", name, errStageStopped)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("%s exited %d", name, exitErr.ExitCode())
		}
		return err
	}
	return nil
}
