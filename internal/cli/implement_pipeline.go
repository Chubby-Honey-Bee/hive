package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// implementPipeline is the native (no-shell-script) orchestration of
// the five-stage self-implement flow. Each stage maps to an existing
// cobra subcommand or to a Go-side helper.
type implementPipeline struct {
	findingsPath string
	severity     string
	maxFixes     int
	maxCostUSD   float64
	maxIter      int
	model        string
	repairModel  string
	provider     string
	profile      string // the routing profile preflight and agent-run apply
	branch       string
	dryRun       bool
	allowDirty   bool
	noPR         bool
	humanGate    bool
	workspaceDir string
	prTitle      string
	stdout       io.Writer
	stderr       io.Writer
	stdin        *os.File

	// reusable shell wrapper from review_pipeline.go.
	shell *reviewPipeline
}

// run executes the pipeline.
func (p *implementPipeline) run() error {
	if err := p.start(); err != nil {
		return err
	}
	dbPath := filepath.Join(p.workspaceDir, "hive.db")
	workflowPath := filepath.Join(p.workspaceDir, "workflow.yaml")
	if err := p.prepare(dbPath, workflowPath); err != nil {
		return err
	}
	if p.dryRun {
		p.printDryRun(workflowPath)
		return nil
	}
	return p.dispatch(dbPath, workflowPath)
}

// start checks the findings file is there, and sets up the stage runner and
// the branch the fixes land on.
func (p *implementPipeline) start() error {
	if _, err := os.Stat(p.findingsPath); err != nil {
		return fmt.Errorf(
			"findings file not found: %s\n"+
				"Run `chb review` first to produce one, "+
				"or pass --findings <path>", p.findingsPath)
	}
	p.shell = &reviewPipeline{
		stdout: p.stdout, stderr: p.stderr, stdin: p.stdin,
	}
	if p.branch == "" {
		p.branch = "self-implement/" + time.Now().UTC().Format("2006-01-02-1504")
	}
	return nil
}

// prepare runs every stage before agent-run: a fresh database, the fix
// workflow generated from the findings, and its validation and preflight.
func (p *implementPipeline) prepare(dbPath, workflowPath string) error {
	if err := p.freshDB(dbPath, workflowPath); err != nil {
		return err
	}
	if err := p.generateWorkflow(workflowPath); err != nil {
		return err
	}
	return p.checkWorkflow(workflowPath)
}

// freshDB creates the workspace, removes the previous run's database and
// workflow, and initialises a new database (stage 1).
func (p *implementPipeline) freshDB(dbPath, workflowPath string) error {
	if err := os.MkdirAll(p.workspaceDir, 0o755); err != nil {
		return err
	}
	_ = os.Remove(dbPath)
	_ = os.Remove(workflowPath)
	os.Setenv("HIVE_DB_PATH", dbPath)
	if err := p.shell.runChb([]string{"db-init"}); err != nil {
		return fmt.Errorf("db-init: %w", err)
	}
	return nil
}

// generateWorkflow writes the fix workflow for the findings (stage 2).
func (p *implementPipeline) generateWorkflow(workflowPath string) error {
	fmt.Fprintln(p.stderr, "── 1/5 GENERATING FIX WORKFLOW ──")
	args := []string{
		"gen-implement-workflow", p.findingsPath,
		"--severity", p.severity,
		"--max-fixes", strconv.Itoa(p.maxFixes),
		"--model", p.model,
		"--repair-model", p.repairModel,
		"--out", workflowPath,
	}
	if err := p.shell.runChb(args); err != nil {
		return fmt.Errorf("gen-implement-workflow: %w", err)
	}
	return nil
}

// checkWorkflow validates and preflights the generated workflow (stage 3).
func (p *implementPipeline) checkWorkflow(workflowPath string) error {
	fmt.Fprintln(p.stderr, "\n── 2/5 VALIDATE + PREFLIGHT ──")
	if err := p.shell.runChb([]string{"workflow", "validate", workflowPath}); err != nil {
		return fmt.Errorf("workflow validate: %w", err)
	}
	preflightArgs := append([]string{"preflight", workflowPath}, p.routeArgs()...)
	if err := p.shell.runChb(preflightArgs); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	return nil
}

// routeArgs pass the pipeline's provider and routing profile to a stage.
func (p *implementPipeline) routeArgs() []string {
	var args []string
	if p.provider != "" {
		args = append(args, "--provider", p.provider)
	}
	if p.profile != "" {
		args = append(args, "--profile", p.profile)
	}
	return args
}

// printDryRun reports what --dry-run generated in place of a run.
func (p *implementPipeline) printDryRun(workflowPath string) {
	fmt.Fprintln(p.stderr, "\n── DRY RUN ──")
	fmt.Fprintf(p.stderr, "  Generated: %s\n", workflowPath)
	fmt.Fprintf(p.stderr, "  Branch (would-be): %s\n", p.branch)
	fmt.Fprintln(p.stderr, "  Skipping agent-run (--dry-run)")
}

// dispatch runs the fixes: the human gate when --human asks for it, then
// agent-run and the run totals. A run stopped by a signal still gets its
// totals report, and then the command fails: an interrupted implement must
// not exit 0.
func (p *implementPipeline) dispatch(dbPath, workflowPath string) error {
	if err := p.gate(workflowPath); err != nil {
		return err
	}
	if err := p.runTotals(p.agentRun(workflowPath)); err != nil {
		return err
	}
	p.printComplete(dbPath, workflowPath)
	return nil
}

// gate is the human gate before LLM spend (the most load-bearing point),
// when --human asks for it.
func (p *implementPipeline) gate(workflowPath string) error {
	if !p.humanGate {
		return nil
	}
	model, repairModel := p.gateModels()
	fmt.Fprintf(p.stderr, "\n── HUMAN GATE ──\n  About to dispatch agent-run.\n  branch:    %s\n  workflow:  %s\n  severity:  %s, max-fixes %d\n  model:     %s (repair: %s)\n  cost cap:  $%.2f\n",
		p.branch, workflowPath, p.severity, p.maxFixes, model, repairModel, p.maxCostUSD)
	return pauseForHuman(p.stderr, p.stdin,
		"Review the workflow above, then press Enter to dispatch (LLM spend begins) or 'n' to abort.")
}

// gateModels names the fix and repair models the human gate shows.
func (p *implementPipeline) gateModels() (model, repairModel string) {
	if p.profile != "" {
		return "profile " + p.profile + " implement-fix", "implement-repair"
	}
	return cmp.Or(p.model, "tier worker"), cmp.Or(p.repairModel, "tier synthesist")
}

// agentRun launches agent-run on the workflow (stage 4). A failed run does
// not stop the pipeline — partial progress is preserved on the branch and
// the run totals are still reported — so it returns only a stop by a signal.
func (p *implementPipeline) agentRun(workflowPath string) error {
	fmt.Fprintln(p.stderr, "\n── 3/5 LAUNCHING agent-run ──")
	fmt.Fprintf(p.stderr, "  branch: %s\n", p.branch)
	err := p.shell.runChb(p.agentRunArgs(workflowPath))
	if err == nil {
		return nil
	}
	fmt.Fprintf(p.stderr, "agent-run exited non-zero: %v\n", err)
	if errors.Is(err, errStageStopped) {
		return fmt.Errorf("agent-run: %w", err)
	}
	return nil
}

// agentRunArgs are the arguments of the agent-run stage.
func (p *implementPipeline) agentRunArgs(workflowPath string) []string {
	args := []string{
		"agent-run", workflowPath,
		"--branch", p.branch,
		"--max-iterations", strconv.Itoa(p.maxIter),
		"--max-cost-usd", fmt.Sprintf("%g", p.maxCostUSD),
	}
	if p.allowDirty {
		args = append(args, "--allow-dirty")
	}
	if !p.noPR {
		title := cmp.Or(p.prTitle, "Self-implement: "+p.branch+" — autonomous fixes from review findings")
		args = append(args, "--auto-pr", "--pr-title", title)
	}
	return append(args, p.routeArgs()...)
}

// runTotals reports the run totals (stage 5). It returns stopped, agent-run's
// stop by a signal, else a stop of run-totals itself.
func (p *implementPipeline) runTotals(stopped error) error {
	fmt.Fprintln(p.stderr, "\n── 4/5 RUN TOTALS ──")
	err := p.shell.runChb([]string{"run-totals", "1"})
	if err == nil {
		return stopped
	}
	fmt.Fprintf(p.stderr, "run-totals: %v\n", err)
	if stopped == nil && errors.Is(err, errStageStopped) {
		return fmt.Errorf("run-totals: %w", err)
	}
	return stopped
}

// printComplete reports the finished run. The per-node counts come from the
// run-totals JSON (nodes_completed, nodes_failed, nodes_rejected), so no
// sqlite3 binary is needed.
func (p *implementPipeline) printComplete(dbPath, workflowPath string) {
	fmt.Fprintln(p.stderr, "\n════════════════════════════════════════════════════════════")
	fmt.Fprintln(p.stderr, "  IMPLEMENT COMPLETE")
	fmt.Fprintf(p.stderr, "  Branch:    %s\n", p.branch)
	fmt.Fprintf(p.stderr, "  Workflow:  %s\n", workflowPath)
	fmt.Fprintf(p.stderr, "  DB:        %s\n", dbPath)
	if !p.noPR {
		fmt.Fprintln(p.stderr, "  PR:        opened (--auto-pr) — see agent-run output for URL")
	}
	fmt.Fprintln(p.stderr, "════════════════════════════════════════════════════════════")
}
