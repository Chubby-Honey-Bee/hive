package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"github.com/spf13/cobra"
)

// foragerReasoningUsage and synthReasoningUsage document the reasoning
// flags on generate and ask.
const (
	foragerReasoningUsage = "reasoning level for each forager and follow-up lens: none, low, medium or high, sent as reasoning_effort on the OpenAI-compatible backend (default: none sent, so the server's default applies; a Qwen model on Ollama then thinks)"
	synthReasoningUsage   = "reasoning level for Queen, scope and the evaluator: none, low, medium or high (default: none sent)"
)

// lensToolsUsage documents --lens-tools on generate and ask.
const lensToolsUsage = "tools each forager and follow-up lens may use: none (no tools sent), read (read_file, glob, grep — for a question that needs the repository) or all (every tool). Scope, the evaluator and Queen get none"

// personaProfileUsage and personaSectionsUsage document --persona-profile
// and --persona-sections on generate and ask.
const (
	personaProfileUsage  = "how much of each persona the prompts carry: full (every section; Queen and the evaluator read every full verdict) or lean (sections 1,2,4,5,7; Queen and the evaluator read the swarm ledger), for a model with a small context window"
	personaSectionsUsage = "the persona body sections every forager and Queen render, such as 1,2,3,4,5,7, in place of the profile's (for ablation)"
)

// personaSectionsFlag reads --persona-sections: nil when it is unset, so the
// profile chooses.
func personaSectionsFlag(v string) ([]int, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	secs, err := foragers.ParsePersonaSections(v)
	if err != nil {
		return nil, fmt.Errorf("--persona-sections: %w", err)
	}
	return secs, nil
}

// warnTo writes each generation warning to w on its own line.
func warnTo(w io.Writer) func(string) {
	return func(msg string) { fmt.Fprintln(w, "warning: "+msg) }
}

// readContextFile is --context-file's text, after --context's when both are
// given. The file must be UTF-8 text.
func readContextFile(path, context string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--context-file: %w", err)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("--context-file %s: not UTF-8 text", path)
	}
	if context == "" {
		return string(data), nil
	}
	return context + "\n\n" + string(data), nil
}

// newSwarmAskCmd is the day-to-day entry point: build the workflow
// and dispatch it. Defaults: the balanced nine take part (--foragers
// selects others; a dreamer runs its ripening pass only when the chosen
// set includes one), and dispatch happens immediately in-process — fully
// automated. Use --human to insert a checkpoint after Queen.
func newSwarmAskCmd() *cobra.Command {
	var o askOptions
	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Ask the forager swarm a question (the balanced nine by default)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSwarmAsk(cmd, &o, strings.Join(args, " "))
		},
	}
	// `balanced` is the empirically-grounded default (template-v1, 9
	// foragers: minimal-7 + Optimist + Historian): the persona
	// self-evaluation (docs/specs/swarm.md § The empirical record)
	// measured this roster as more decisive than minimal-7 alone.
	// `minimal` (7) is the smallest axis-complete deliberation set.
	// `default` selects foragers with default:true frontmatter.
	// `all` is every deliberation-eligible forager.
	cmd.Flags().StringSliceVar(&o.foragerList, "foragers", []string{"balanced"}, "forager list ('balanced' = 9-forager empirical default, 'minimal' = 7 axis-owners, 'default' = default:true foragers, 'all' = every deliberation-eligible forager, or comma-separated names)")
	cmd.Flags().StringVar(&o.model, "model", "", "pin a literal model for each forager; a comma-separated list gives the lenses, in name order, the models in turn, and the follow-up lens the first (default: empty → tier system; --budget-mode dials it)")
	cmd.Flags().StringVar(&o.synthModel, "synthesizer-model", "", "pin a literal model for Queen (default: empty → tier system)")
	cmd.Flags().StringVar(&o.foragerTier, "forager-tier", "", "tier role for each forager when --model is unset (default: synthesist)")
	cmd.Flags().StringVar(&o.synthTier, "synthesizer-tier", "", "tier role for Queen when --synthesizer-model is unset (default: planner)")
	cmd.Flags().StringVar(&o.budgetMode, "budget-mode", "", "cost dial: premium|standard|cheap|free (default: HIVE_BUDGET_MODE, else standard)")
	cmd.Flags().StringVar(&o.contextText, "context", "", "additional context to pass to every forager")
	cmd.Flags().StringVar(&o.outPath, "out", "", "where to write the generated workflow YAML (default: tmpdir)")
	cmd.Flags().BoolVar(&o.noDispatch, "no-dispatch", false, "print the agent-run command instead of running automatically")
	cmd.Flags().BoolVar(&o.humanGate, "human", false, "pause after Queen for your approval; answer with chb workflow resume <run_id> approve|reject|redirect")
	cmd.Flags().BoolVar(&o.noEval, "no-eval", false, "disable the coverage pass (default: on — the swarm scores its own coverage and fans any gaps to fresh lenses before Queen synthesizes)")
	cmd.Flags().BoolVar(&o.scope, "scope", false, "add a pre-dispatch scoping pass that sharpens the question into one decidable sentence before the lenses fan out")
	cmd.Flags().BoolVar(&o.deterministic, "deterministic", false, "Set temperature=0. Reduces sampling variance; does not guarantee identical outputs across providers.")
	cmd.Flags().Int64Var(&o.seedVal, "seed", 0, "Seed for reproducibility (implies --deterministic). OpenAI honors this; Gemini honors it on most model variants; Anthropic Messages API does not expose seed (best-effort via temp=0 only).")
	cmd.Flags().Float64Var(&o.temperature, "temperature", 0, "sampling temperature (0-2; the Anthropic API takes 0-1) passed to agent-run. Unset sends none, and the server chooses: Ollama's OpenAI endpoint then uses 1.0, not the model's own value. Not with --deterministic or --seed")
	cmd.Flags().Float64Var(&o.topP, "top-p", 0, "nucleus-sampling bound (above 0, at most 1) passed to agent-run. Unset sends none, and the server chooses: Ollama's OpenAI endpoint then uses 1.0, not the model's own value. Not with --deterministic or --seed")
	cmd.Flags().StringVar(&o.artifactPath, "artifact", "", "write the run's canonical artifact JSON, with its SHA-256, to this path after the run (chb verify-artifact re-checks it). Two runs give byte-identical artifacts only on a provider that honours --seed; otherwise the hash detects drift.")
	cmd.Flags().StringVar(&o.lensTools, "lens-tools", "none", lensToolsUsage)
	cmd.Flags().IntVar(&o.followups, "followups", foragers.DefaultFollowups, "how many of the evaluator's gaps get a fresh follow-up lens; the rest reach Queen by name")
	cmd.Flags().StringVar(&o.persona, "persona-profile", foragers.ProfileFull, personaProfileUsage)
	cmd.Flags().StringVar(&o.sections, "persona-sections", "", personaSectionsUsage)
	cmd.Flags().StringVar(&o.contextFile, "context-file", "", "read the context every forager gets from this UTF-8 text file, after any --context text: a context pack, so the lenses reason over facts gathered for them rather than reading the repository")
	cmd.Flags().StringVar(&o.foragerReasoning, "forager-reasoning", "", foragerReasoningUsage)
	cmd.Flags().StringVar(&o.synthReasoning, "synthesizer-reasoning", "", synthReasoningUsage)
	cmd.Flags().Int64Var(&o.maxOutTokens, "max-output-tokens", 0, "per-turn output cap for every backend.Run call this run. Overrides HIVE_MAX_OUTPUT_TOKENS env var. 0 = use the resolved model's max_output_tokens from default-models.yaml (with 8192 fallback). Has no effect on CLI backends (claude/gemini CLIs have no max-tokens flag).")
	cmd.Flags().StringVar(&o.profileFlag, "profile", "", profileUsage+"; not with the model, tier or reasoning flags, which the profile sets; --persona-profile is chosen apart from it")
	cmd.Flags().BoolVar(&o.directVoice, "direct-voice", defaultDirectVoice, directVoiceUsage)
	cmd.Flags().BoolVar(&o.contextSplit, "context-split", defaultContextSplit, contextSplitUsage)
	cmd.Flags().BoolVar(&o.jsonOut, "json", false, "print one JSON object and nothing else on stdout: run_id, question, verdict, recommendation, the Queen's report, calibration (as the artifact records it) and run (agent-run's trailer); not with --no-dispatch")
	return cmd
}

// askOptions holds chb ask's flag values.
type askOptions struct {
	foragerList      []string
	model            string
	synthModel       string
	foragerTier      string
	synthTier        string
	budgetMode       string
	contextText      string
	outPath          string
	noDispatch       bool
	humanGate        bool
	noEval           bool
	scope            bool
	deterministic    bool
	seedVal          int64
	temperature      float64
	topP             float64
	artifactPath     string
	maxOutTokens     int64
	lensTools        string
	followups        int
	profileFlag      string
	persona          string
	sections         string
	contextFile      string
	directVoice      bool
	contextSplit     bool
	jsonOut          bool
	foragerReasoning string
	synthReasoning   string
}

// askPlan is what ask's flags resolve to before the workflow is built.
type askPlan struct {
	temp, topP  *float64
	profileName string
	sections    []int
}

// runSwarmAsk checks ask's flags, writes the swarm's workflow and dispatches
// it, or under --no-dispatch prints the commands that would.
func runSwarmAsk(cmd *cobra.Command, o *askOptions, question string) error {
	plan, err := o.resolve(cmd)
	if err != nil {
		return err
	}
	swarm, err := o.writeWorkflow(cmd, plan)
	if err != nil {
		return err
	}
	out := cmd.ErrOrStderr()
	announceAskSwarm(out, swarm, o.outPath)
	runArgs := o.agentRunArgs(cmd, question, swarm, plan)
	if o.noDispatch {
		o.printManualCommands(cmd, out, runArgs, plan.profileArgs())
		return nil
	}
	return dispatchAsk(cmd, out, runArgs, question, o.jsonOut)
}

// resolve checks ask's flags, in the order their errors are reported, and
// appends --context-file's text to the context.
func (o *askOptions) resolve(cmd *cobra.Command) (askPlan, error) {
	var p askPlan
	var err error
	if p.temp, p.topP, err = o.checkRunFlags(cmd); err != nil {
		return p, err
	}
	if p.profileName, err = o.checkSwarmFlags(cmd); err != nil {
		return p, err
	}
	if p.sections, err = personaSectionsFlag(o.sections); err != nil {
		return p, err
	}
	return p, o.loadContextFile()
}

// checkRunFlags checks the flags that shape the agent-run dispatch: --json
// against --no-dispatch, --budget-mode and the sampling flags, whose set
// values it returns.
func (o *askOptions) checkRunFlags(cmd *cobra.Command) (*float64, *float64, error) {
	if o.jsonOut && o.noDispatch {
		return nil, nil, fmt.Errorf("--json needs a run to report; it cannot be combined with --no-dispatch")
	}
	if _, err := runner.ResolveBudgetMode(o.budgetMode); err != nil {
		return nil, nil, err
	}
	return samplingFlags(cmd, o.deterministic, o.temperature, o.topP)
}

// checkSwarmFlags checks --followups, resolves the routing profile and
// refuses the flags the profile replaces.
func (o *askOptions) checkSwarmFlags(cmd *cobra.Command) (string, error) {
	if o.followups < 1 {
		return "", fmt.Errorf("--followups %d: want at least 1 (--no-eval skips the coverage pass)", o.followups)
	}
	profileName, err := useProfile(o.profileFlag)
	if err != nil {
		return "", err
	}
	return profileName, askProfileConflicts(cmd, profileName)
}

// loadContextFile appends --context-file's text, when given, to the context.
func (o *askOptions) loadContextFile() error {
	if o.contextFile == "" {
		return nil
	}
	var err error
	o.contextText, err = readContextFile(o.contextFile, o.contextText)
	return err
}

// writeWorkflow generates the swarm's workflow and writes it to --out, a
// file in the temp directory when --out is unset, returning the swarm.
func (o *askOptions) writeWorkflow(cmd *cobra.Command, plan askPlan) ([]foragers.Forager, error) {
	swarm, synth, err := selectSwarmForagers(o.foragerList)
	if err != nil {
		return nil, err
	}
	yamlText, err := foragers.GenerateWorkflow(swarm, o.workflowOptions(cmd, synth, plan.sections))
	if err != nil {
		return nil, err
	}
	if o.outPath == "" {
		o.outPath = filepath.Join(os.TempDir(),
			fmt.Sprintf("swarm-%d.yaml", os.Getpid()),
		)
	}
	return swarm, writeGeneratedSwarmYAML(o.outPath, yamlText)
}

// workflowOptions are the generator options ask's flags set.
func (o *askOptions) workflowOptions(cmd *cobra.Command, synth foragers.Forager, secs []int) foragers.WorkflowOptions {
	return foragers.WorkflowOptions{
		Model:            o.model,
		ForagerTier:      o.foragerTier,
		SynthesizerModel: o.synthModel,
		SynthesizerTier:  o.synthTier,
		HumanGate:        o.humanGate,
		Evaluate:         !o.noEval,
		Scope:            o.scope,
		Synthesizer:      synth,
		LensTools:        o.lensTools,
		Followups:        o.followups,
		PersonaProfile:   o.persona,
		PersonaSections:  secs,
		Warn:             warnTo(cmd.ErrOrStderr()),
		DirectVoice:      o.directVoice,
		ContextSplit:     o.contextSplit,

		ForagerReasoning:     o.foragerReasoning,
		SynthesizerReasoning: o.synthReasoning,
	}
}

// selectSwarmForagers loads the forager tree and returns the swarm the list
// selects and Queen, its synthesizer.
func selectSwarmForagers(list []string) ([]foragers.Forager, foragers.Forager, error) {
	all, err := loadForagers()
	if err != nil {
		return nil, foragers.Forager{}, err
	}
	swarm, err := foragers.Filter(all, list)
	if err != nil {
		return nil, foragers.Forager{}, err
	}
	synth, _ := foragers.ByName(all, "queen")
	return swarm, synth, nil
}

// writeGeneratedSwarmYAML writes the workflow YAML to path, creating its
// directory.
func writeGeneratedSwarmYAML(path, yamlText string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(yamlText), 0o644)
}

// announceAskSwarm lists the swarm ask assembled and where its workflow is.
func announceAskSwarm(out io.Writer, swarm []foragers.Forager, outPath string) {
	fmt.Fprintf(out, "Swarm assembled: %d foragers → %s\n", len(swarm), outPath)
	for _, w := range swarm {
		marker := "★"
		if w.IsDreamer() {
			marker = "🜔"
		}
		fmt.Fprintf(out, "  %s %s — %s\n", marker, w.Name, firstLineDesc(w.Description))
	}
	fmt.Fprintln(out, "")
}

// agentRunArgs is the agent-run argument list. One argument list serves both
// paths, so the command --no-dispatch prints is the one ask would dispatch.
func (o *askOptions) agentRunArgs(cmd *cobra.Command, question string, swarm []foragers.Forager, plan askPlan) []string {
	// --seed implies --deterministic
	seedSet := cmd.Flags().Changed("seed")
	if seedSet {
		o.deterministic = true
	}

	// Resolve --max-output-tokens precedence: CLI flag > env > 0
	// (== "use per-model default at dispatch"). The dispatcher
	// honours `runner.Config.MaxOutputTokens > 0` as the override
	// and otherwise reads models.MaxOutputFor(resolvedModel).
	maxOut := maxOutputTokensFrom(o.maxOutTokens)

	runArgs := []string{
		"agent-run", o.outPath,
		"--inputs", o.workflowInputsJSON(question, swarm),
		"--branch", "",
	}
	runArgs = append(runArgs, o.samplingArgs(seedSet, plan)...)
	return append(runArgs, o.outputArgs(maxOut, plan.profileArgs())...)
}

// workflowInputsJSON is the workflow inputs ask passes: the question, the
// context and, under --context-split, each lens's part of the context.
func (o *askOptions) workflowInputsJSON(question string, swarm []foragers.Forager) string {
	inputs := map[string]any{
		"question": question,
		"context":  o.contextText,
	}
	if o.contextSplit {
		for key, part := range foragers.ContextParts(swarm, o.contextText) {
			inputs[key] = part
		}
	}
	inputsJSON, _ := json.Marshal(inputs)
	return string(inputsJSON)
}

// samplingArgs are the budget, determinism and sampling flags ask passes to
// agent-run, each only when set.
func (o *askOptions) samplingArgs(seedSet bool, plan askPlan) []string {
	var args []string
	if o.budgetMode != "" {
		args = append(args, "--budget-mode", o.budgetMode)
	}
	if o.deterministic {
		args = append(args, "--deterministic")
	}
	if seedSet {
		args = append(args, "--seed", strconv.FormatInt(o.seedVal, 10))
	}
	return append(args, plan.samplingArgs()...)
}

// samplingArgs are --temperature and --top-p, each only when set.
func (p askPlan) samplingArgs() []string {
	var args []string
	if p.temp != nil {
		args = append(args, "--temperature", strconv.FormatFloat(*p.temp, 'g', -1, 64))
	}
	if p.topP != nil {
		args = append(args, "--top-p", strconv.FormatFloat(*p.topP, 'g', -1, 64))
	}
	return args
}

// outputArgs are --artifact, --max-output-tokens and the profile flags ask
// passes to agent-run, each only when set.
func (o *askOptions) outputArgs(maxOut int64, profileArgs []string) []string {
	var args []string
	if o.artifactPath != "" {
		args = append(args, "--artifact", o.artifactPath)
	}
	if maxOut > 0 {
		args = append(args, "--max-output-tokens", strconv.FormatInt(maxOut, 10))
	}
	return append(args, profileArgs...)
}

// profileArgs is the --profile flag the profile in force passes on, nil
// when none is.
func (p askPlan) profileArgs() []string {
	if p.profileName == "" {
		return nil
	}
	return []string{"--profile", p.profileName}
}

// printManualCommands prints the preflight and agent-run commands
// --no-dispatch hands the caller.
func (o *askOptions) printManualCommands(cmd *cobra.Command, out io.Writer, runArgs, profileArgs []string) {
	// The printed commands run in a new process, which inherits nothing:
	// both name --db when the caller set it, the run keeps the --branch ""
	// that leaves agent-run's auto-commit branch off, and each argument is
	// shell-quoted so a pasted question is not expanded.
	var dbArgs []string
	if cmd.Flags().Changed("db") {
		dbArgs = []string{"--db", dbPath}
	}
	fmt.Fprintln(out, "To run it manually:")
	fmt.Fprintln(out, "  chb "+shellJoin(slices.Concat(dbArgs, []string{"preflight", o.outPath}, profileArgs)))
	fmt.Fprintln(out, "  chb "+shellJoin(slices.Concat(dbArgs, runArgs)))
}

// dispatchAsk runs the swarm and prints its verdict.
func dispatchAsk(cmd *cobra.Command, out io.Writer, runArgs []string, question string, jsonOut bool) error {
	fmt.Fprintln(out, "Dispatching the swarm automatically — pass --no-dispatch to opt out.")
	res, err := dispatchSwarm(cmd, runArgs, jsonOut)
	if err != nil {
		return err
	}
	if jsonOut {
		return printSwarmVerdictJSON(cmd.OutOrStdout(), question, res)
	}
	printSwarmVerdict(cmd.OutOrStdout(), out, res)
	return nil
}

// dispatchSwarm runs the swarm in this process, as chb agent-run runs
// runArgs: agent-run's own flags parse them into the run's configuration,
// and the run uses the store ask opened. Its JSON trailer goes to stderr, so
// ask's stdout holds the verdict alone, and under --json nowhere, since the
// object carries its fields.
func dispatchSwarm(cmd *cobra.Command, runArgs []string, jsonOut bool) (*runner.Result, error) {
	trailer := cmd.ErrOrStderr()
	if jsonOut {
		trailer = io.Discard
	}
	run, o := newAgentRun()
	run.SetContext(cmd.Context())
	if err := run.ParseFlags(runArgs[1:]); err != nil {
		return nil, err
	}
	return o.runWorkflow(run, run.Flags().Arg(0), trailer)
}

// The defaults of --direct-voice and --context-split, which the measurement
// in swarm.md § The direct voice and § The context split sets.
const (
	defaultDirectVoice  = false
	defaultContextSplit = false
)

// directVoiceUsage and contextSplitUsage document the two flags on ask.
const (
	directVoiceUsage  = "add the direct voice: the model's own answer to the question and the whole context, with no persona, as one more vote in the Queen's tally (node forager-direct; it declares no bond and fires no ∇)"
	contextSplitUsage = "give each lens its own part of the context in place of all of it: the paragraphs, or the items of the shallowest list, dealt round-robin in lens name order; the direct voice and scope keep the whole context"
)

// printSwarmVerdict writes the verdict of the run ask just dispatched, led by
// its calibration (workflow.Calibration.Lines): how sure the swarm was, the
// ∇ pairs that fired, then the verdict, read from the run's rows in the store
// it ran on. It is presentation: a verdict it cannot read is said on errw,
// and the run's exit status stands.
func printSwarmVerdict(w, errw io.Writer, res *runner.Result) {
	cal, err := workflow.RunCalibration(store.Workflows(), res.RunID)
	if err != nil {
		fmt.Fprintf(errw, "verdict: unavailable (%v)\n", err)
		return
	}
	for _, line := range cal.Lines() {
		fmt.Fprintln(w, line)
	}
}

// askVerdictJSON is what chb ask --json prints: the run's verdict with its
// calibration and agent-run's trailer, one object, for a script to parse.
type askVerdictJSON struct {
	RunID          int64                `json:"run_id"`
	Question       string               `json:"question"`
	Verdict        string               `json:"verdict"`
	Recommendation string               `json:"recommendation"`
	Report         string               `json:"report"`
	Calibration    workflow.Calibration `json:"calibration"`
	Run            map[string]any       `json:"run"`
}

// printSwarmVerdictJSON writes the run ask just dispatched as one JSON
// object. Unlike the text print, a verdict it cannot read is an error: the
// object is what the caller asked for.
func printSwarmVerdictJSON(w io.Writer, question string, res *runner.Result) error {
	cal, err := workflow.RunCalibration(store.Workflows(), res.RunID)
	if err != nil {
		return fmt.Errorf("verdict: unavailable (%w)", err)
	}
	b, err := json.MarshalIndent(askVerdictJSON{
		RunID:          res.RunID,
		Question:       question,
		Verdict:        cal.Verdict(),
		Recommendation: cal.Recommendation(),
		Report:         cal.Report(),
		Calibration:    cal,
		Run:            runner.RunSummary(res),
	}, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// askProfileFlags are the chb ask flags a routing profile replaces: it sets
// every swarm role's model and reasoning, so each would do nothing.
var askProfileFlags = []string{"model", "synthesizer-model", "forager-tier", "synthesizer-tier", "forager-reasoning", "synthesizer-reasoning"}

// askProfileConflicts refuses, under a profile, a flag the profile replaces,
// and --lens-tools when the profile's lens or followup route sets the tools.
func askProfileConflicts(cmd *cobra.Command, profile string) error {
	if profile == "" {
		return nil
	}
	if f, ok := askChangedProfileFlag(cmd); ok {
		return fmt.Errorf("--%s cannot be combined with routing profile %s, which sets every swarm role's model and reasoning", f, profile)
	}
	if !cmd.Flags().Changed("lens-tools") {
		return nil
	}
	return askProfileToolsConflict(profile)
}

// askChangedProfileFlag is the first given flag of those a routing profile
// replaces.
func askChangedProfileFlag(cmd *cobra.Command) (string, bool) {
	for _, f := range askProfileFlags {
		if cmd.Flags().Changed(f) {
			return f, true
		}
	}
	return "", false
}

// askProfileToolsConflict refuses --lens-tools when the profile's lens or
// followup route sets the tools. A role the profile does not route has no
// tools of its own.
func askProfileToolsConflict(profile string) error {
	p, err := runner.ResolveProfile(profile)
	if err != nil {
		return err
	}
	for _, role := range []string{"lens", "followup"} {
		if p.Routes[role].Tools != nil {
			return fmt.Errorf("--lens-tools cannot be combined with routing profile %s, whose %s route sets the tools", profile, role)
		}
	}
	return nil
}

func firstLineDesc(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// shellQuote wraps one argument in single quotes, inside which a POSIX
// shell expands nothing. Every argument is quoted: a bare word is not safe
// across shells (zsh expands a leading '=' to a command path). An embedded
// single quote closes the quoting, is written backslash-escaped, and
// reopens it.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellJoin quotes each argument with shellQuote and joins them with spaces.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}
