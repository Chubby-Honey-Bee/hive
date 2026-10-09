package cli

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// newReviewCmd implements `chb review`: it drives a five-lens audit
// workflow against the supplied target, extracts findings, renders
// REVIEW.md, and optionally pauses for human approval before further
// action.
//
//	chb review                                          # repo defaults
//	chb review --target /path/to/other/repo
//	chb review --workflow workflows/self-review.yaml
//	chb review --provider claude-cli
//	chb review --human                                  # pause after render
//	chb review --no-regression                          # skip post-run go test + self-validate
func newReviewCmd() *cobra.Command {
	var (
		flags   reviewPipeline
		profile string
	)
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run the five-lens review on the target repo",
		Long: `Drives the five-lens audit workflow (SOLID, WASP/CDE,
MSS, cyclomatic, Go-idiom) end-to-end. Each lens runs in parallel,
emits structured findings, then a synthesizer rolls them into one
REVIEW.md. Optionally pauses for human approval before regression
gates run.

The pipeline:
  1. chb preflight <workflow>
  2. chb agent-run <workflow> --inputs ...
  3. chb extract-findings (rationale → JSON)
  4. chb render-review (JSON → REVIEW.md)
  5. (optional) human gate — wait for confirmation
  6. go test ./... + chb validate (regression)

Pure Go. No shell scripts, no jq dependency, cross-platform.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReview(cmd, flags, profile)
		},
	}
	cmd.Flags().StringVar(&flags.target, "target", "",
		"target repo root (default: cwd)")
	cmd.Flags().StringVar(&flags.workflow, "workflow", "workflows/self-review.yaml",
		"audit workflow YAML")
	cmd.Flags().StringVar(&flags.workspaceDir, "workspace", "",
		"workspace directory for run artefacts (default: <target>/workspace/self-review)")
	cmd.Flags().StringVar(&flags.provider, "provider", "",
		"LLM provider (claude-cli|anthropic|openai|gemini); empty = auto-detect")
	cmd.Flags().BoolVar(&flags.humanGate, "human", false,
		"pause for human approval after REVIEW.md is rendered, before regression gate")
	cmd.Flags().BoolVar(&flags.noRegression, "no-regression", false,
		"skip the post-run go test + self-validate gate")
	cmd.Flags().StringVar(&profile, "profile", "", profileUsage)
	return cmd
}

// runReview runs the review pipeline p, as the flags set it, on absolute
// paths and under the routing profile --profile or HIVE_PROFILE names.
func runReview(cmd *cobra.Command, p reviewPipeline, profileFlag string) error {
	if err := p.resolvePaths(); err != nil {
		return err
	}
	profileName, err := useProfile(profileFlag)
	if err != nil {
		return err
	}
	p.profile = profileName
	p.stdout, p.stderr, p.stdin = cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Stdin
	return p.run()
}

// resolvePaths makes the target, the workflow and the workspace absolute.
// Absolute, because every stage runs with its working directory set to the
// target — and because the workflow path has to be resolved against the
// *current* directory before that happens, or `workflows/self-review.yaml`
// would be looked for inside the target, which generally does not have one.
func (p *reviewPipeline) resolvePaths() error {
	target, err := reviewTarget(p.target)
	if err != nil {
		return err
	}
	p.target = target
	p.workflow = reviewWorkflowPath(p.workflow)
	p.workspaceDir = reviewWorkspaceDir(p.workspaceDir, target)
	return nil
}

// reviewTarget is the absolute path of --target, the working directory when
// it is empty.
func reviewTarget(target string) (string, error) {
	if target == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		target = cwd
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve --target: %w", err)
	}
	return abs, nil
}

// reviewWorkflowPath is the absolute path of a relative --workflow that
// exists under the working directory, else --workflow as given.
func reviewWorkflowPath(workflow string) string {
	if filepath.IsAbs(workflow) {
		return workflow
	}
	abs, err := filepath.Abs(workflow)
	if err != nil {
		return workflow
	}
	if _, err := os.Stat(abs); err != nil {
		return workflow
	}
	return abs
}

// reviewWorkspaceDir is the absolute path of --workspace, by default
// workspace/self-review in the target. Absolute too: the stages run inside
// the target, so a relative --workspace would otherwise silently re-anchor
// there.
func reviewWorkspaceDir(workspaceDir, target string) string {
	if workspaceDir == "" {
		workspaceDir = filepath.Join(target, "workspace", "self-review")
	}
	if abs, err := filepath.Abs(workspaceDir); err == nil {
		workspaceDir = abs
	}
	return workspaceDir
}

// newImplementCmd implements `chb implement`: it reads a findings.json,
// generates a per-fix workflow, drives agent-run, optionally opens a
// PR, and optionally pauses for human approval at each load-bearing
// boundary.
//
//	chb implement                                       # critical+high, 5 fixes
//	chb implement --findings workspace/self-review/findings.json
//	chb implement --severity critical --max-fixes 2
//	chb implement --branch self-implement/mss-fix --no-pr
//	chb implement --human                               # pause before each phase
//	chb implement --dry-run                             # validate + preflight only
func newImplementCmd() *cobra.Command {
	var (
		flags   implementPipeline
		profile string
	)
	cmd := &cobra.Command{
		Use:   "implement",
		Short: "Apply review findings autonomously",
		Long: `Closes the self-review → fix loop. Reads findings.json,
generates one workflow node per finding (gated on compile_ok +
tests_pass), then drives agent-run with optional auto-PR.

The pipeline:
  1. chb db-init (fresh workspace DB)
  2. chb gen-implement-workflow (findings → workflow.yaml)
  3. chb workflow validate <workflow> + chb preflight
  4. (optional) human gate — confirm before LLM spend
  5. chb agent-run <workflow> --branch <branch> [--auto-pr]
  6. chb run-totals + per-node table report

Each fix lands as its own commit on the branch (when working tree is
clean). Per-fix accept-gate is compile_ok && tests_pass — failed
fixes get rejected and the agent retries with the repair model.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImplement(cmd, flags, profile)
		},
	}
	cmd.Flags().StringVar(&flags.findingsPath, "findings", "workspace/self-review/findings.json",
		"findings.json path")
	cmd.Flags().StringVar(&flags.severity, "severity", "critical,high",
		"comma-separated severities to fix")
	cmd.Flags().IntVar(&flags.maxFixes, "max-fixes", 5,
		"hard cap on number of fixes per run")
	cmd.Flags().Float64Var(&flags.maxCostUSD, "max-cost-usd", 10.0,
		"cumulative cost ceiling")
	cmd.Flags().IntVar(&flags.maxIter, "max-iterations", 60,
		"agent-run iteration cap")
	cmd.Flags().StringVar(&flags.model, "model", "",
		"model for the fix nodes (default: tier worker under the budget mode)")
	cmd.Flags().StringVar(&flags.repairModel, "repair-model", "",
		"escalation model on accept-gate rejection (default: tier synthesist under the budget mode)")
	cmd.Flags().StringVar(&flags.provider, "provider", "",
		"LLM provider; empty = auto-detect")
	cmd.Flags().StringVar(&flags.branch, "branch", "",
		"git branch (default: self-implement/<utc-timestamp>)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false,
		"validate + preflight only; no LLM calls")
	cmd.Flags().BoolVar(&flags.allowDirty, "allow-dirty", false,
		"proceed even if the working tree has uncommitted changes (auto-commit will sweep them into the branch)")
	cmd.Flags().BoolVar(&flags.noPR, "no-pr", false,
		"skip GitHub PR auto-open")
	cmd.Flags().BoolVar(&flags.humanGate, "human", false,
		"pause for human approval before each load-bearing phase")
	cmd.Flags().StringVar(&flags.workspaceDir, "workspace", "",
		"workspace directory (default: workspace/self-implement)")
	cmd.Flags().StringVar(&flags.prTitle, "pr-title", "",
		"PR title (default: Self-implement: <branch> — autonomous fixes)")
	cmd.Flags().StringVar(&profile, "profile", "", profileUsage+"; not with --model or --repair-model")
	return cmd
}

// runImplement runs the implement pipeline p, as the flags set it, under the
// routing profile --profile or HIVE_PROFILE names. A profile sets the fix
// and repair models, so it refuses --model and --repair-model beside one.
func runImplement(cmd *cobra.Command, p implementPipeline, profileFlag string) error {
	p.workspaceDir = cmp.Or(p.workspaceDir, "workspace/self-implement")
	profileName, err := useProfile(profileFlag)
	if err != nil {
		return err
	}
	if profileName != "" && p.modelFlagsSet() {
		return fmt.Errorf("--model and --repair-model cannot be combined with routing profile %s, which sets the implement-fix and implement-repair models", profileName)
	}
	p.profile = profileName
	p.stdout, p.stderr, p.stdin = cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Stdin
	return p.run()
}

// modelFlagsSet reports whether --model or --repair-model names a model.
func (p *implementPipeline) modelFlagsSet() bool {
	return p.model != "" || p.repairModel != ""
}

// pauseForHuman writes a confirmation prompt to stderr and reads one line
// from stdin. Empty / "y" / "yes" → continue; anything else → abort. Used by
// --human gates in both pipelines, with the pipeline's own writer and
// reader, whatever their type.
func pauseForHuman(stderr io.Writer, stdin io.Reader, label string) error {
	fmt.Fprintf(stderr, "\n──── HUMAN GATE: %s ────\n", label)
	fmt.Fprintln(stderr, "Press [Enter] to continue, type 'n' to abort:")
	var line string
	// EOF or an empty line leaves line empty, and the gate continues (the
	// canonical "press enter" path), so the read's error is not consulted.
	_, _ = fmt.Fscanln(stdin, &line)
	if humanGateAborts(line) {
		return fmt.Errorf("aborted by human gate at %q", label)
	}
	return nil
}

// humanGateAborts reports whether a human gate's answer aborts the run.
func humanGateAborts(answer string) bool {
	switch answer {
	case "n", "N", "no", "No", "abort":
		return true
	}
	return false
}
