package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"github.com/spf13/cobra"
)

// agentRunOptions holds chb agent-run's flag values.
type agentRunOptions struct {
	projectName   string
	projectDir    string
	inputsJSON    string
	maxIterations int
	agentsDir     string
	branch        string
	resumeRunID   int64
	allowDirty    bool
	autoPR        bool
	prTitle       string
	prBody        string
	dryRun        bool
	onlyWave      int
	// provider, cost, sampling and artifact options
	provider      string
	maxCostUSD    float64
	budgetMode    string
	deterministic bool
	seedVal       int64
	temperature   float64
	topP          float64
	artifactPath  string
	maxOutTokens  int64
	profile       string
}

// newAgentRunCmd wires `chb agent-run <workflow.yaml>` — the fully
// unattended agentic runner that drives a workflow to terminal state via the
// Anthropic API.
func newAgentRunCmd() *cobra.Command {
	cmd, _ := newAgentRun()
	return cmd
}

// newAgentRun is chb agent-run's command and the options its flags set. chb
// ask parses the run it dispatches with them.
func newAgentRun() (*cobra.Command, *agentRunOptions) {
	o := &agentRunOptions{}
	cmd := &cobra.Command{
		Use:   "agent-run <workflow.yaml>",
		Short: "Drive a workflow to completion unattended (closes the fully-automated gap)",
		Long: `agent-run loads a workflow definition, then loops:
  1. Pulls the next dispatchable node(s) from the workflow engine.
  2. Loads the node's agent persona from <project>/agents/<agent>.md.
  3. Calls the resolved provider (--provider, else HIVE_PROVIDER, else the
     first SDK key or CLI found) with a tool registry
     (read/write/edit/glob/grep/shell/web_fetch/db-write).
  4. Resolves every tool_use in-process until the model emits its final text.
  5. Submits outputs back to the workflow engine (workflow complete).
  6. Auto-commits any file edits to the configured branch.
  7. Repeats until the workflow is terminal.
  8. Optionally opens a rollup PR.

Needs one resolvable provider: an SDK key or the claude / gemini CLI.
Optional: GH_TOKEN for --auto-pr.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := o.runWorkflow(cmd, args[0], os.Stdout)
			return err
		},
	}

	cmd.Flags().StringVar(&o.projectName, "project", "", "project name for logs (default: basename of --dir)")
	cmd.Flags().StringVar(&o.projectDir, "dir", "", "project working dir (default: cwd)")
	cmd.Flags().StringVar(&o.inputsJSON, "inputs", "", "JSON object of workflow inputs (default: {\"project\": <project>})")
	cmd.Flags().IntVar(&o.maxIterations, "max-iterations", 500, "safety cap on total workflow iterations")
	cmd.Flags().StringVar(&o.agentsDir, "agents-dir", "", "directory inside --dir containing <agent>.md personas (default: agents, and a persona missing there is read from the copy the binary carries)")
	cmd.Flags().Int64Var(&o.resumeRunID, "resume", 0, "continue the existing run `run_id` (after chb workflow resume answered its human_review checkpoint) instead of starting a new one")
	cmd.Flags().StringVar(&o.branch, "branch", "validate/auto-corrections", "branch for auto-committed edits (empty = no commits)")
	cmd.Flags().BoolVar(&o.allowDirty, "allow-dirty", false, "proceed with auto-commit even if the working tree has uncommitted changes (they will be swept into the branch)")
	cmd.Flags().BoolVar(&o.autoPR, "auto-pr", false, "open a rollup PR at the end of the run (requires gh CLI + GH_TOKEN)")
	cmd.Flags().StringVar(&o.prTitle, "pr-title", "", "override PR title")
	cmd.Flags().StringVar(&o.prBody, "pr-body", "", "override PR body")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "log dispatches but do not call Claude or mutate files")
	cmd.Flags().IntVar(&o.onlyWave, "only-wave", 0, "stop after the first node whose name starts with 'wN-' matches (smoke run)")
	// provider, cost, sampling and artifact flags
	cmd.Flags().StringVar(&o.provider, "provider", "", "LLM provider override (anthropic|gemini|openai|claude-cli|gemini-cli|local); falls through to the --profile's provider, then HIVE_PROVIDER env, then API-key auto-detect")
	cmd.Flags().Float64Var(&o.maxCostUSD, "max-cost-usd", 0, "if >0, terminate the run cleanly when cumulative LLM spend reaches this many USD")
	cmd.Flags().StringVar(&o.budgetMode, "budget-mode", "", "progressive cost-tier mode: premium|standard|cheap|free (default: HIVE_BUDGET_MODE, else standard). Controls which slot of each tier is used for nodes that set a tier: key in their YAML.")
	cmd.Flags().BoolVar(&o.deterministic, "deterministic", false, "Set temperature=0. Reduces sampling variance; does not guarantee identical outputs across providers.")
	cmd.Flags().Int64Var(&o.seedVal, "seed", 0, "Seed for reproducibility (implies --deterministic). OpenAI honors this; Gemini honors it on most model variants; Anthropic Messages API does not expose seed (best-effort via temp=0 only).")
	cmd.Flags().Float64Var(&o.temperature, "temperature", 0, "sampling temperature (0-2; the Anthropic API takes 0-1) sent on every call. Unset sends none, and the server chooses: Ollama's OpenAI endpoint then uses 1.0, not the model's own value. Not with --deterministic or --seed")
	cmd.Flags().Float64Var(&o.topP, "top-p", 0, "nucleus-sampling bound (above 0, at most 1) sent on every call. Unset sends none, and the server chooses: Ollama's OpenAI endpoint then uses 1.0, not the model's own value. Not with --deterministic or --seed")
	cmd.Flags().StringVar(&o.artifactPath, "artifact", "", "write the run's canonical artifact JSON, with its SHA-256, to this path after the run completes (chb verify-artifact re-checks it)")
	cmd.Flags().Int64Var(&o.maxOutTokens, "max-output-tokens", 0, "per-turn output cap (tokens) for every backend.Run call. Overrides HIVE_MAX_OUTPUT_TOKENS env var. 0 = use the resolved model's max_output_tokens from default-models.yaml (with 8192 fallback).")
	cmd.Flags().StringVar(&o.profile, "profile", "", profileUsage)
	return cmd, o
}

// runWorkflow resolves the workflow file, turns the flags cmd parsed into
// the run's configuration and drives the run, printing its JSON trailer to
// trailer.
func (o *agentRunOptions) runWorkflow(cmd *cobra.Command, workflowArg string, trailer io.Writer) (*runner.Result, error) {
	wfPath, shipped, cleanup, err := workflow.ResolveFile(workflowArg)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if shipped {
		fmt.Fprintf(cmd.ErrOrStderr(), "workflow %s: not on disk; running the copy the binary carries\n", workflowArg)
	}
	cfg, err := o.runConfig(cmd, wfPath)
	if err != nil {
		return nil, err
	}
	return driveAgentRun(cmd.Context(), cfg, trailer)
}

// runConfig resolves the flags into the runner's configuration, in the order
// their errors are reported, and refuses a run that needs an Anthropic key
// it does not have.
func (o *agentRunOptions) runConfig(cmd *cobra.Command, wfPath string) (runner.Config, error) {
	inputs, err := o.workflowInputs(wfPath)
	if err != nil {
		return runner.Config{}, err
	}
	s, err := o.resolveSampling(cmd)
	if err != nil {
		return runner.Config{}, err
	}
	profileName, err := o.resolveProfile()
	if err != nil {
		return runner.Config{}, err
	}
	cfg := o.config(wfPath, inputs, s, profileName)
	return cfg, o.requireKey(cfg)
}

// workflowInputs defaults --dir and --project, parses --inputs and, when the
// workflow drives the hive, points the run at the hive's database.
func (o *agentRunOptions) workflowInputs(wfPath string) (map[string]any, error) {
	o.defaultProject()
	inputs, err := o.parseInputs()
	if err != nil {
		return nil, err
	}
	// A run of a workflow that drives the hive uses the database the
	// hive rule chooses for its project, for every node and agent.
	return inputs, announceAgentRunHive(wfPath, inputs)
}

// defaultProject sets an unset --dir to the working directory and an unset
// --project to the directory's base name.
func (o *agentRunOptions) defaultProject() {
	if o.projectDir == "" {
		o.projectDir, _ = os.Getwd()
	}
	if o.projectName == "" {
		o.projectName = filepath.Base(o.projectDir)
	}
}

// parseInputs is --inputs, or {"project": <project>} when it is unset.
func (o *agentRunOptions) parseInputs() (map[string]any, error) {
	if o.inputsJSON == "" {
		return map[string]any{"project": o.projectName}, nil
	}
	var inputs map[string]any
	if err := json.Unmarshal([]byte(o.inputsJSON), &inputs); err != nil {
		return nil, fmt.Errorf("parse --inputs: %w", err)
	}
	return inputs, nil
}

// announceAgentRunHive points the run at the database the hive rule chooses
// when the workflow drives the hive, and names it on stderr.
func announceAgentRunHive(wfPath string, inputs map[string]any) error {
	r, err := resolveHiveRun(wfPath, inputs)
	if err != nil {
		return err
	}
	if r != nil {
		fmt.Fprintf(os.Stderr, "[agent-run] hive project %q: database %s\n", inputs["project"], r.Path)
	}
	return nil
}

// agentRunSampling is what agent-run's budget, sampling, output-cap and seed
// flags resolve to.
type agentRunSampling struct {
	budget      runner.BudgetMode
	temperature *float64
	topP        *float64
	maxOut      int64
	seed        *int64
}

// resolveSampling resolves --budget-mode, --temperature and --top-p,
// --max-output-tokens and --seed, in that order: the sampling check sees
// --deterministic as given, before --seed implies it.
func (o *agentRunOptions) resolveSampling(cmd *cobra.Command) (agentRunSampling, error) {
	var s agentRunSampling
	var err error
	if s.budget, err = o.exportBudgetMode(); err != nil {
		return s, err
	}
	if s.temperature, s.topP, err = samplingFlags(cmd, o.deterministic, o.temperature, o.topP); err != nil {
		return s, err
	}
	// Per-turn output cap: --max-output-tokens > HIVE_MAX_OUTPUT_TOKENS > 0
	// (0 means "per-model default at dispatch via models.MaxOutputFor").
	s.maxOut = maxOutputTokensFrom(o.maxOutTokens)
	s.seed = o.applySeed(cmd)
	return s, nil
}

// exportBudgetMode resolves the budget mode. A mode --budget-mode names is
// exported, as --profile is, so the commands the run starts see the mode:
// the hive's `chb hive next` bounds the model tier by it.
func (o *agentRunOptions) exportBudgetMode() (runner.BudgetMode, error) {
	bm, err := runner.ResolveBudgetMode(o.budgetMode)
	if err != nil || o.budgetMode == "" {
		return bm, err
	}
	return bm, os.Setenv("HIVE_BUDGET_MODE", string(bm))
}

// applySeed applies --seed, which implies --deterministic, and returns the
// seed when --seed was given.
func (o *agentRunOptions) applySeed(cmd *cobra.Command) *int64 {
	if !cmd.Flags().Changed("seed") {
		return nil
	}
	o.deterministic = true
	return &o.seedVal
}

// resolveProfile resolves the routing profile and, when --provider is unset,
// takes the profile's provider.
func (o *agentRunOptions) resolveProfile() (string, error) {
	profileName, err := useProfile(o.profile)
	if err != nil {
		return "", err
	}
	return profileName, o.takeProfileProvider(profileName)
}

// takeProfileProvider sets an unset --provider to the provider of the
// profile in force, if any.
func (o *agentRunOptions) takeProfileProvider(profileName string) error {
	if profileName == "" || o.provider != "" {
		return nil
	}
	p, err := runner.ResolveProfile(profileName)
	if err != nil {
		return err
	}
	o.provider = string(p.Provider)
	return nil
}

// config is the runner configuration the resolved flags describe.
func (o *agentRunOptions) config(wfPath string, inputs map[string]any, s agentRunSampling, profileName string) runner.Config {
	return runner.Config{
		WorkflowYAML:     wfPath,
		ProjectName:      o.projectName,
		ProjectDir:       o.projectDir,
		DBPath:           dbPath,
		Inputs:           inputs,
		MaxIterations:    o.maxIterations,
		AgentsDir:        o.agentsDir,
		APIKey:           os.Getenv("ANTHROPIC_API_KEY"),
		Branch:           o.branch,
		AllowDirty:       o.allowDirty,
		AutoPR:           o.autoPR,
		PRTitle:          o.prTitle,
		PRBody:           o.prBody,
		DryRun:           o.dryRun,
		OnlyWave:         o.onlyWave,
		ResumeRunID:      o.resumeRunID,
		Provider:         o.provider,
		BudgetMode:       s.budget,
		MaxCostUSDx10000: int64(o.maxCostUSD * 10000),
		Log:              os.Stderr,
		Deterministic:    o.deterministic,
		Seed:             s.seed,
		Temperature:      s.temperature,
		TopP:             s.topP,
		ArtifactPath:     o.artifactPath,
		MaxOutputTokens:  s.maxOut,
		Profile:          profileName,
	}
}

// requireKey refuses, unless --dry-run, a run whose backend needs an
// Anthropic key that is not set.
func (o *agentRunOptions) requireKey(cfg runner.Config) error {
	if o.dryRun {
		return nil
	}
	return requireAnthropicKey(cfg)
}

// driveAgentRun drives the run on the command's store, prints its JSON
// trailer to trailer and returns the run's result.
func driveAgentRun(ctx context.Context, cfg runner.Config, trailer io.Writer) (*runner.Result, error) {
	// Graceful Ctrl-C — cancel in-flight Claude calls. The capture
	// ends when the run returns, so a later Ctrl-C in a process that
	// goes on, as chb ask does, has its default effect.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defer context.AfterFunc(ctx, func() {
		fmt.Fprintln(os.Stderr, "[agent-run] signal received — cancelling")
	})()

	res, err := runner.Run(ctx, store, cfg)
	if res != nil {
		runner.PrintRunSummary(trailer, res)
	}
	return res, err
}

// profileUsage documents --profile on every command that takes it.
const profileUsage = "routing profile from the models config (local-fast, local-small, local-8gb, or your own): routes every node that names a role: to the profile's model, provider, reasoning, tools and ttl, and every other node that names no model or provider to its default route, and refuses a run that sends data off this machine when the profile routes no role off it (default: HIVE_PROFILE)"

// useProfile is the routing profile a command runs under: flag, else
// HIVE_PROFILE. A flag is exported as HIVE_PROFILE, so what the
// command runs, the hive's `chb hive next` among it, runs under it too. An
// unknown or malformed profile is refused here, before anything runs.
func useProfile(flag string) (string, error) {
	name := profileFlagOrEnv(flag)
	if name == "" {
		return "", nil
	}
	if _, err := runner.ResolveProfile(name); err != nil {
		return "", err
	}
	if err := exportProfileFlag(flag, name); err != nil {
		return "", err
	}
	return name, nil
}

// profileFlagOrEnv is the trimmed flag, else the trimmed HIVE_PROFILE.
func profileFlagOrEnv(flag string) string {
	if name := strings.TrimSpace(flag); name != "" {
		return name
	}
	return strings.TrimSpace(os.Getenv("HIVE_PROFILE"))
}

// exportProfileFlag exports the profile as HIVE_PROFILE when the flag
// was given.
func exportProfileFlag(flag, name string) error {
	if flag == "" {
		return nil
	}
	return os.Setenv("HIVE_PROFILE", name)
}

// requireAnthropicKey refuses a run whose resolved backend is the Anthropic
// SDK when neither ANTHROPIC_API_KEY nor ANTHROPIC_AUTH_TOKEN (which the SDK
// also reads) is set. It keys on the resolved kind, so --provider outranks
// HIVE_PROVIDER, and a provider's name matches in any case.
func requireAnthropicKey(cfg runner.Config) error {
	if runner.ResolveBackendKind(cfg) == runner.BackendAnthropic && cfg.APIKey == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" {
		return fmt.Errorf("the anthropic backend needs ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, and neither is set")
	}
	return nil
}

// samplingFlags validates --temperature and --top-p and returns the values
// that were set, nil for one that was not. Either is refused together with
// --deterministic or --seed, which fix temperature 0.
func samplingFlags(cmd *cobra.Command, deterministic bool, temperature, topP float64) (*float64, *float64, error) {
	setT, setP := cmd.Flags().Changed("temperature"), cmd.Flags().Changed("top-p")
	if samplingFlagsConflict(cmd, deterministic, setT, setP) {
		return nil, nil, fmt.Errorf("--temperature and --top-p cannot be combined with --deterministic or --seed, which fix temperature 0")
	}
	t, err := samplingTemperature(setT, temperature)
	if err != nil {
		return nil, nil, err
	}
	p, err := samplingTopP(setP, topP)
	if err != nil {
		return nil, nil, err
	}
	return t, p, nil
}

// samplingFlagsConflict reports whether --temperature or --top-p was given
// together with --deterministic or --seed.
func samplingFlagsConflict(cmd *cobra.Command, deterministic, setT, setP bool) bool {
	return (setT || setP) && (deterministic || cmd.Flags().Changed("seed"))
}

// samplingTemperature is --temperature when it was set, refusing a value
// outside 0 to 2.
func samplingTemperature(set bool, temperature float64) (*float64, error) {
	if !set {
		return nil, nil
	}
	if temperature < 0 || temperature > 2 {
		return nil, fmt.Errorf("--temperature %v: want a value from 0 to 2", temperature)
	}
	return &temperature, nil
}

// samplingTopP is --top-p when it was set, refusing a value that is not
// above 0 and at most 1.
func samplingTopP(set bool, topP float64) (*float64, error) {
	if !set {
		return nil, nil
	}
	if topP <= 0 || topP > 1 {
		return nil, fmt.Errorf("--top-p %v: want a value above 0 and at most 1", topP)
	}
	return &topP, nil
}

// maxOutputTokensFrom resolves --max-output-tokens: the flag when it is above
// 0, else HIVE_MAX_OUTPUT_TOKENS when that is a count above 0, else the
// flag (0 means the resolved model's default at dispatch).
func maxOutputTokensFrom(flag int64) int64 {
	if flag > 0 {
		return flag
	}
	n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("HIVE_MAX_OUTPUT_TOKENS")), 10, 64)
	if err != nil || n <= 0 {
		return flag
	}
	return n
}
