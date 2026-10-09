package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
)

// newProofCmd implements `chb proof`, which runs the
// proof workflow.
//
// Runs the smallest possible autonomous workflow that exercises every
// mechanism of the autonomous runner:
//   - provider routing
//   - accept: predicates with autonomous repair
//   - decision-branch persistence to workflow_decisions
//   - per-node rationale + tokens + cost in workflow_node_states
//   - preflight gate refusing to launch if env is unhealthy
//
// Reuses the reviewPipeline shell helpers — same single-binary
// re-entry pattern (no shell dependency, no jq, no sqlite3 binary).
func newProofCmd() *cobra.Command {
	var (
		workspaceDir string
		workflow     string
		provider     string
		maxIter      int
		maxCostUSD   float64
		profile      string
	)
	cmd := &cobra.Command{
		Use:   "proof",
		Short: "Smallest end-to-end run of the unattended runner",
		Long: `Runs the proof workflow against an isolated
workspace DB, then
inspects the post-run state:

  - decisions persisted in workflow_decisions
  - per-node rationale / tokens / cost populated
  - cost meter total

Authenticate exactly one provider before running:
  ANTHROPIC_API_KEY / GEMINI_API_KEY / OPENAI_API_KEY  (SDK)
  claude / gemini  (CLI on PATH)
or run it on this machine under a routing profile (--profile local-fast,
local-small or local-8gb), on the server at HIVE_LOCAL_BASE_URL.

Exit code: 0 iff the workflow reached a terminal accept-passing state
AND at least one decision was persisted AND at least one node was
enriched with rationale/tokens/cost.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := useProfile(profile); err != nil {
				return err
			}
			pipeline := &proofPipeline{
				workspaceDir: workspaceDir,
				workflow:     workflow,
				provider:     provider,
				maxIter:      maxIter,
				maxCostUSD:   maxCostUSD,
				stdout:       cmd.OutOrStdout(),
				stderr:       cmd.ErrOrStderr(),
				stdin:        os.Stdin,
			}
			return pipeline.run(context.Background())
		},
	}
	cmd.Flags().StringVar(&workspaceDir, "workspace", "workspace/proof",
		"workspace directory")
	cmd.Flags().StringVar(&workflow, "workflow", "workflows/proof.yaml",
		"proof workflow YAML")
	cmd.Flags().StringVar(&provider, "provider", "",
		"LLM provider override (empty = auto-detect)")
	cmd.Flags().IntVar(&maxIter, "max-iterations", 25, "agent-run iteration cap")
	cmd.Flags().Float64Var(&maxCostUSD, "max-cost-usd", 1.0, "cost cap")
	cmd.Flags().StringVar(&profile, "profile", "", profileUsage)
	return cmd
}

// proofPipeline orchestrates the proof stages.
type proofPipeline struct {
	workspaceDir string
	workflow     string
	provider     string
	maxIter      int
	maxCostUSD   float64
	stdout       io.Writer
	stderr       io.Writer
	stdin        *os.File
}

// run executes the four stages: workspace prep, preflight, agent-run,
// post-run inspection. Returns non-nil error iff any stage fails or
// the post-run verification finds missing provenance.
func (p *proofPipeline) run(ctx context.Context) error {
	rp := &reviewPipeline{stdout: p.stdout, stderr: p.stderr, stdin: p.stdin}
	dbPath := filepath.Join(p.workspaceDir, "hive.db")
	resultPath := filepath.Join(p.workspaceDir, "result.txt")
	if err := p.freshWorkspace(rp, dbPath, resultPath); err != nil {
		return err
	}
	if err := p.preflight(rp); err != nil {
		return err
	}
	runErr := p.agentRun(rp, resultPath)

	// 4. Post-run inspection, read through the Go DB layer rather than a
	// sqlite3 binary.
	if err := p.inspect(resultPath, dbPath); err != nil {
		return err
	}
	return proofOutcome(runErr, resultPath)
}

// freshWorkspace is stage 1: it creates the workspace, removes the previous
// run's database, WAL files and result, and initialises a new database.
func (p *proofPipeline) freshWorkspace(rp *reviewPipeline, dbPath, resultPath string) error {
	if err := os.MkdirAll(p.workspaceDir, 0o755); err != nil {
		return err
	}
	_ = os.Remove(dbPath)
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	_ = os.Remove(resultPath)
	os.Setenv("HIVE_DB_PATH", dbPath)
	if err := rp.runChb([]string{"db-init"}); err != nil {
		return fmt.Errorf("db-init: %w", err)
	}
	return nil
}

// preflight is stage 2, the preflight gate.
func (p *proofPipeline) preflight(rp *reviewPipeline) error {
	fmt.Fprintln(rp.stderr, "── PREFLIGHT ──")
	preflight := append([]string{"preflight", p.workflow}, p.providerArgs()...)
	if err := rp.runChb(preflight); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	return nil
}

// agentRun is stage 3: it runs the workflow under agent-run and returns
// the run's error.
func (p *proofPipeline) agentRun(rp *reviewPipeline, resultPath string) error {
	fmt.Fprintln(rp.stderr, "")
	fmt.Fprintln(rp.stderr, "── LAUNCHING agent-run ──")
	runArgs := []string{
		"agent-run", p.workflow,
		"--inputs", fmt.Sprintf(`{"target_file":"internal/cli/validate.go","result_path":%q}`, resultPath),
		"--max-iterations", strconv.Itoa(p.maxIter),
		"--max-cost-usd", fmt.Sprintf("%g", p.maxCostUSD),
		"--branch", "",
	}
	return rp.runChb(append(runArgs, p.providerArgs()...))
}

// providerArgs pass the proof's provider override to a stage.
func (p *proofPipeline) providerArgs() []string {
	if p.provider == "" {
		return nil
	}
	return []string{"--provider", p.provider}
}

// proofOutcome fails a proof whose agent-run failed or wrote no artifact.
func proofOutcome(runErr error, resultPath string) error {
	if runErr != nil {
		return fmt.Errorf("agent-run: %w", runErr)
	}
	if _, err := os.Stat(resultPath); err != nil {
		return fmt.Errorf("no artifact at %s", resultPath)
	}
	return nil
}

// inspect reads the post-run state through Go's database/sql so the
// proof command has zero external CLI dependencies.
func (p *proofPipeline) inspect(resultPath, dbPath string) error {
	fmt.Fprintln(p.stderr, "")
	fmt.Fprintln(p.stderr, "── ARTIFACT ──")
	if data, err := os.ReadFile(resultPath); err == nil {
		fmt.Fprintln(p.stdout, string(data))
	} else {
		fmt.Fprintln(p.stderr, "  (no artifact written)")
	}

	// Defer DB queries to the proof_inspect.go helper for cleanliness.
	return runProofInspection(p.stdout, p.stderr, dbPath)
}
