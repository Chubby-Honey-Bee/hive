// Package runner drives a workflow run end-to-end: it pulls dispatchable nodes
// from the workflow engine, sends each node's prompt with the tool registry to
// its model through an LLM backend, submits outputs back to the workflow, and
// auto-commits any file edits the agent makes. This closes the "fully
// unattended" gap.
package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	hive "github.com/Chubby-Honey-Bee/hive"
	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// parseResonates extracts the machine-readable `resonates:` pairs a
// chb workflow emits — a top-level list of two-element [a, b] lists.
// Returns nil for any workflow without the key (e.g. a research workflow).
// The engine reads the same pairs for the Queen's {nabla.fired}.
func parseResonates(defn map[string]any) [][2]string {
	return workflow.ResonatesPairs(defn)
}

// startQuorumSensor constructs and starts the ∇ quorum sensor for a
// swarm run when the workflow declares resonates pairs. No-op (and no
// goroutine, nil channel) for workflows without them. The sensor subscribes
// to the global Comb bus; cancelling ctx stops its goroutine, and the
// returned channel closes once it has drained. A resumed run's sensor starts
// from the verdicts and fired bonds the run already has.
func startQuorumSensor(ctx context.Context, store *db.Store, runID int64, defn map[string]any, resumed bool, logf func(string, ...any)) <-chan struct{} {
	pairs := parseResonates(defn)
	if len(pairs) == 0 {
		return nil
	}
	s := newRunQuorumSensor(store, runID, pairs)
	if resumed {
		resumeQuorumSensor(s, logf)
	}
	done := s.Start(ctx)
	if logf != nil {
		logf("∇ quorum sensor watching %d resonates pair(s)", len(pairs))
	}
	return done
}

// newRunQuorumSensor is a quorum sensor on the global Comb bus for the run,
// watching each resonates pair.
func newRunQuorumSensor(store *db.Store, runID int64, pairs [][2]string) *comb.QuorumSensor {
	s := comb.NewQuorumSensor(store, comb.Default)
	s.SetRunID(runID)
	for _, p := range pairs {
		s.Register(p[0], p[1])
	}
	return s
}

// resumeQuorumSensor starts a resumed run's sensor from the verdicts and
// fired bonds the run already has. A failure is logged: the verdicts before
// the resume then go uncounted.
func resumeQuorumSensor(s *comb.QuorumSensor, logf func(string, ...any)) {
	if err := s.Resume(); err != nil && logf != nil {
		logf("∇ quorum sensor: %v (verdicts before the resume are not counted)", err)
	}
}

// startSensor is startQuorumSensor, indirected so a test can stand in a
// sensor whose drain outlasts the rest of Run.
var startSensor = startQuorumSensor

// Config controls a single `chb agent-run` invocation.
type Config struct {
	WorkflowYAML string // path to workflow definition
	ProjectName  string // identifier for logs / hive_state
	ProjectDir   string // working dir (sandbox root)
	// DBPath is the database this run writes to, forwarded to every agent
	// subprocess as HIVE_DB_PATH, so an agent's `chb db-write` writes
	// the database that holds the run's workflow rows, not the default path
	// under its own working directory. Empty falls back to the environment.
	DBPath        string
	Inputs        map[string]any // workflow inputs
	MaxIterations int            // cap on total node dispatches (safety)
	AgentsDir     string         // dir containing <agent>.md personas (relative to ProjectDir); "" means agents/, then the copy the binary carries
	APIKey        string         // empty → env lookup
	Branch        string         // auto-commit branch ("" disables commits)
	AllowDirty    bool           // let auto-commit proceed on a dirty working tree (default: refuse)
	AutoPR        bool           // open PR when workflow completes
	PRTitle       string
	PRBody        string
	DryRun        bool  // if true, log dispatches but don't call Claude
	OnlyWave      int   // if >0, exit after first node whose wave matches (smoke run)
	ResumeRunID   int64 // if >0, continue this existing run instead of starting a new one
	// MaxCostUSDx10000, if >0, terminates the dispatch loop cleanly when
	// cumulative LLM spend (tracked per-call via the pricing tables in
	// pricing.go) reaches the cap. Stored in 1/10000 USD so the same
	// integer math used for per-call cost applies. Set via the
	// --max-cost-usd flag; the CLI converts dollars→1/10000 USD.
	MaxCostUSDx10000 int64
	// Deterministic, if true, sets Temperature=0 on every outgoing
	// request. Reduces sampling variance; does not guarantee identical
	// outputs across providers or Anthropic hardware batching.
	Deterministic bool
	// Seed, if non-nil, pins the random seed for reproducibility. Only
	// set when the user explicitly passes --seed <N>; never set silently.
	// Setting Seed implies Deterministic (the CLI flag wiring enforces
	// this). OpenAI and Gemini honor this; Anthropic Messages API does
	// not expose a seed parameter (best-effort via temperature=0 only).
	Seed *int64
	// Temperature and TopP, when non-nil, are sent on every dispatch and
	// repair (--temperature, --top-p). The CLI refuses either together with
	// Deterministic or Seed.
	Temperature *float64
	TopP        *float64
	// ArtifactPath, if non-empty, writes a canonical deterministic artifact
	// to the specified path after the run completes successfully. The
	// artifact bundles the question, determinism settings, per-forager
	// models/providers/verdicts, and the final comb_state snapshot. Its
	// SHA256 is embedded in the JSON.
	ArtifactPath string
	// Question is the swarm question surfaced in the artifact's `question`
	// field. When empty, BuildArtifact reads from workflow_runs.inputs_json
	// (the chb ask command stores it there). Set explicitly when the
	// caller knows the question at dispatch time.
	Question string
	// Provider, if non-empty, pins the LLM provider for this run.
	// Honored by ResolveBackendKind ahead of env vars. Workflow nodes
	// may override this on a per-node basis via the YAML `provider:` field.
	Provider string
	// BudgetMode picks the slot a `tier:` node, or an on_reject block's
	// `tier:`, is sent (tierModel): the tier's slot of the mode's name, an
	// empty slot degrading — cheap to standard, then premium; the others to
	// premium — and an empty mode reading as standard
	// (models.Config.ResolveTier). On a backend that cannot serve Claude, a
	// slot naming a Claude model is sent as its alias. A `model:` wins over a
	// `tier:`.
	BudgetMode BudgetMode
	// MaxOutputTokens, if >0, overrides the per-model max_output_tokens
	// lookup for every backend.Run call in this run. Wired from the
	// `--max-output-tokens` CLI flag and `HIVE_MAX_OUTPUT_TOKENS`
	// env var. Zero means "use the resolved model's per-model default
	// (or 8192 if unset) — see models.MaxOutputFor".
	MaxOutputTokens int64
	Log             io.Writer
	// Backend, if non-nil, bypasses backend resolution entirely. Intended
	// for tests that need to inject a stub; production callers leave this
	// nil and let ResolveBackendKind pick between SDK and CLI.
	Backend LLMBackend
	// thinkingCache is the run's thinking-capability cache, which Run makes
	// and every OpenAI-compatible backend built from the run's Config shares.
	thinkingCache *thinkingCache
	// calibration is the run's context-window calibration, which Run makes
	// and every OpenAI-compatible backend built from the run's Config
	// shares, so a count carries across nodes and repairs that each build
	// their own backend.
	calibration *calibration

	// ChbPath is the program a command node's `chb` runs. Empty, it is the
	// running binary, which is chb under `chb agent-run`. chb-mcp runs
	// workflows in-process and sets it, since its own binary is not chb.
	ChbPath string

	// Profile names the routing profile the run applies to its workflow
	// (ResolveProfile, ApplyProfile); empty applies HIVE_PROFILE's, and
	// none when that is unset too. Its provider is the run default unless
	// Provider is set.
	Profile string

	// RunStarted, when set, is called with the run's id once its
	// workflow_runs row exists, before any node runs. chb-mcp's research
	// goroutine keeps it, so a run that panics can still be marked failed.
	RunStarted func(runID int64)
}

// Result summarizes a completed run (success or error).
type Result struct {
	RunID         int64
	Iterations    int
	NodesRun      int
	Commits       []string
	PRURL         string
	InputTokens   int64
	OutputTokens  int64
	CostUSDx10000 int64 // cumulative cost in 1/10000 USD (10000 = $1.00). 0 on CLI backends.
	// MeteredCalls and UnmeteredCalls count the calls whose cost is and is
	// not known. A call's cost is unknown when its model has no models-config
	// price or its backend reports no token counts (a gemini CLI without
	// --output-format json). An
	// unmetered call adds nothing to CostUSDx10000 because its cost is
	// unknown, not because it was free.
	MeteredCalls   int
	UnmeteredCalls int
	// CopilotCreditsX1000 is the cumulative Copilot premium-request cost
	// in 1/1000 credits (so 0.33× × 1 call = 330). Independent of the
	// dollar cost above; reported in 1/1000 to keep integer math while
	// preserving the 0.33×/0.05×/etc. fractions in CopilotMultipliers.
	CopilotCreditsX1000 int64
	Err                 error
}

// Run is the one-shot entry point used by `chb agent-run <workflow.yaml>`.
//
// Phases, each delegated to a named helper:
//
//	applyConfigDefaults          → fills MaxIterations / ProjectDir / Log
//	applyRunProfile              → routes the workflow by cfg.Profile
//	runSetup.prepare             → the setup steps that can refuse (runner_setup.go):
//	  committerFor               → checks auto-commit can run (refuses a dirty tree)
//	  resolveLLMBackend          → SDK / CLI / injected
//	  PreflightWorkflowProviders → refuses a node provider that is unknown or the allowlist bars
//	  PreflightSampling          → refuses a temperature an Anthropic node cannot take
//	  Routing, PreflightLocality → logs the routing; a profile refuses a call off the machine it names no evidence for
//	  preflightProfileBackends   → under a profile, refuses a node provider that cannot serve it
//	  PreflightEndpointModels    → refuses a model an OpenAI-compatible endpoint does not serve
//	runSetup.execute             → the run itself:
//	  bootstrapWorkflow          → creates the workflow_runs row, returns runID
//	  enterBranch                → creates or checks out the auto-commit branch
//	  ProbeConstraints           → measures which models each OpenAI-compatible endpoint constrains to a schema
//	  dispatchLoop               → main per-iteration fan-out (runner_loop.go)
//	  finishRun                  → the artifact, the optional final PR (maybeOpenPR) and the totals (runner_finish.go)
//
// The setup steps that can refuse come before bootstrapWorkflow creates the
// run row, so a refusal leaves no running workflow_runs row and no
// checked-out branch.
func Run(ctx context.Context, store *db.Store, cfg Config) (*Result, error) {
	cfg = applyConfigDefaults(cfg)
	cfg.thinkingCache = &thinkingCache{}
	cfg.calibration = &calibration{}
	logf := makeLogf(cfg.Log)

	// First, so everything after it, the run row included, sees the
	// workflow as the profile routes it.
	profile, cfg, cleanup, err := applyRunProfile(cfg)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	setup := &runSetup{store: store, cfg: cfg, profile: profile, logf: logf}
	if err := setup.prepare(ctx); err != nil {
		return nil, err
	}
	return setup.execute(ctx)
}

// runSetup is what Run's phases share: the run's store, config, routing
// profile and logger, and what prepare finds before the run row exists:
// the auto-commit committer, the parsed workflow and the run default's
// backend.
type runSetup struct {
	store   *db.Store
	cfg     Config
	profile *Profile
	logf    func(string, ...any)

	gc         *GitCommitter
	parsedDefn map[string]any
	backend    LLMBackend
}

// execute starts the run and drives it to its end: it opens the run's Time
// Wheel tick, enters the auto-commit branch, sends the constraint probes,
// starts the quorum sensor, runs the dispatch loop and finishes the run. A
// run that cannot start returns its error with an empty Result, and a
// dispatch loop that fails returns its error with the Result so far. The
// sensor drains, and then the tick ends, before execute returns.
func (s *runSetup) execute(ctx context.Context) (*Result, error) {
	res := &Result{}
	if err := s.startRun(res); err != nil {
		return res, err
	}
	endTick := s.openTick(res.RunID)
	defer endTick()

	// After bootstrapWorkflow, so a run that cannot start (a missing
	// workflow, a run that is not resumable) leaves the checkout alone.
	enterBranch(s.gc, s.logf)
	rc := s.runtime(ctx, res)

	// After the model preflight, so every model probed is one the endpoint
	// serves, and after the run row and its log line, so a caller waiting
	// for the run ID (an MCP spawner waits seconds) gets it
	// before the probes: on a local endpoint a cold model load, then up to
	// 1024 tokens, can take minutes each. A probe measures; it never refuses
	// the run.
	if !s.cfg.DryRun {
		rc.probes = rc.runConstraintProbes()
	}

	stopSensor := s.startQuorum(ctx, res.RunID)
	defer stopSensor()

	if err := rc.dispatchLoop(); err != nil {
		return res, err
	}
	rc.finishRun()
	return res, nil
}

// startRun creates the run's workflow_runs row, or picks up the run
// --resume names (bootstrapWorkflow), records its id in res and tells
// cfg.RunStarted.
func (s *runSetup) startRun(res *Result) error {
	runID, err := bootstrapWorkflow(s.store, s.cfg, s.logf)
	if err != nil {
		return err
	}
	res.RunID = runID
	if s.cfg.RunStarted != nil {
		s.cfg.RunStarted(runID)
	}
	return nil
}

// openTick opens a Time Wheel tick for the run. Every Comb revision written
// while it is open is anchored to it, which is what makes `chb comb at
// --tick N` and `chb comb wheel` resolve. Best-effort: a wheel failure must
// never stop a run. It returns what ends the tick, which does nothing when
// none opened.
func (s *runSetup) openTick(runID int64) func() {
	tickID, err := s.store.TimeWheel().Begin(
		fmt.Sprintf("run-%d", runID), db.TickSwarm, runID, 0,
		filepath.Base(s.cfg.WorkflowYAML))
	// A resumed run gets its own tick back from Begin, closed by the first
	// segment. Reopen it, or every write after the resume anchors to no tick.
	if err == nil && s.cfg.ResumeRunID > 0 {
		err = s.store.TimeWheel().Reopen(tickID)
	}
	if err != nil {
		s.logf("time wheel: %v (revisions will anchor by timestamp only)", err)
		return func() {}
	}
	s.logf("time wheel: tick %d open (swarm)", tickID)
	return func() { _ = s.store.TimeWheel().End(tickID) }
}

// runtime is the state the run's dispatch loop and nodes share.
func (s *runSetup) runtime(ctx context.Context, res *Result) *runtimeContext {
	return &runtimeContext{
		ctx:        ctx,
		cfg:        s.cfg,
		store:      s.store,
		backend:    s.backend,
		gc:         s.gc,
		runID:      res.RunID,
		res:        res,
		logf:       s.logf,
		parsedDefn: s.parsedDefn,
	}
}

// startQuorum starts the swarm ∇ quorum sensor (startSensor). If the
// workflow declares `resonates:` pairs, it watches the Comb bus for
// bonded-verdict convergence and records each ∇ (forager_bonds row + nabla
// signal) as it fires. It starts before the dispatch loop, so it is
// subscribed before the first forager verdict is published.
//
// Its own context, cancelled when Run returns: the caller's ctx outlives the
// run whenever the caller runs more than one (`chb replicate`), and a sensor
// on it would stay subscribed to the shared bus and consume later runs'
// verdicts.
//
// It returns what stops the sensor and waits for it to drain, which Run
// calls before it returns, so a ∇ completed by the last buffered verdict
// is recorded by the time the run is done.
func (s *runSetup) startQuorum(ctx context.Context, runID int64) func() {
	sensorCtx, stopSensor := context.WithCancel(ctx)
	sensorDone := startSensor(sensorCtx, s.store, runID, s.parsedDefn, s.cfg.ResumeRunID > 0, s.logf)
	return func() {
		stopSensor()
		if sensorDone != nil {
			<-sensorDone
		}
	}
}

// bootstrapWorkflow inserts the workflow_runs row and returns its id, or
// picks up an existing run when --resume names one (the other half of a
// human_review pause: `workflow resume` answers the checkpoint, this carries
// the run on from where the dispatcher left it).
func bootstrapWorkflow(store *db.Store, cfg Config, logf func(string, ...any)) (int64, error) {
	if cfg.ResumeRunID > 0 {
		return resumeWorkflowRun(store, cfg.ResumeRunID, logf)
	}
	logf("initializing workflow: %s", cfg.WorkflowYAML)
	runID, err := workflow.InitWorkflow(store.Workflows(), cfg.WorkflowYAML, cfg.Inputs)
	if err != nil {
		return 0, fmt.Errorf("init workflow: %w", err)
	}
	logf("workflow run ID: %d", runID)
	return runID, nil
}

// resumeWorkflowRun picks up the run --resume names, which must be
// running: a paused run has its checkpoint answered first.
func resumeWorkflowRun(store *db.Store, runID int64, logf func(string, ...any)) (int64, error) {
	run, err := store.Workflows().GetWorkflowRun(runID)
	if err != nil {
		return 0, fmt.Errorf("resume run %d: %w", runID, err)
	}
	if run.Status == "paused" {
		return 0, fmt.Errorf("run %d is still paused — answer its checkpoint first: chb workflow resume %d approve", runID, runID)
	}
	if run.Status != "running" {
		return 0, fmt.Errorf("run %d is %s, not resumable", runID, run.Status)
	}
	logf("resuming workflow run %d (%s)", run.ID, run.Name)
	return run.ID, nil
}

// enterBranch creates or checks out the auto-commit branch. A failure
// disables auto-commit rather than stopping the run.
func enterBranch(gc *GitCommitter, logf func(string, ...any)) {
	if gc == nil || !gc.Enabled {
		return
	}
	if err := gc.EnsureBranch(); err != nil {
		logf("git: %v — continuing without auto-commit", err)
		gc.Enabled = false
		return
	}
	logf("auto-commit branch: %s", gc.Branch)
}

// defaultAgentsDir is the personas directory under the project when
// Config.AgentsDir is empty.
const defaultAgentsDir = "agents"

// loadAgentPersona reads <agentsDir>/<name>.md from the project. With
// agentsDir empty, the default, it reads agents/<name>.md there, else the
// shipped agents/<name>.md the binary carries. A persona missing everywhere
// is not an error — we just return empty.
func loadAgentPersona(projectDir, agentsDir, agentName string) (string, error) {
	if agentName == "" {
		return "", nil
	}
	b, err := os.ReadFile(filepath.Join(projectDir, cmp.Or(agentsDir, defaultAgentsDir), agentName+".md"))
	if os.IsNotExist(err) {
		return missingAgentPersona(agentsDir, agentName)
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// missingAgentPersona is the persona of an agent the project's personas
// directory does not hold: none when that directory was named (agentsDir),
// else the shipped agents/<name>.md the binary carries, and none when it
// carries no such persona either.
func missingAgentPersona(agentsDir, agentName string) (string, error) {
	if agentsDir != "" {
		return "", nil
	}
	b, err := fs.ReadFile(hive.Agents, agentName+".md")
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[:i]
	}
	return s
}

// truncate caps s at n bytes, ellipsis included, cutting on a rune boundary
// so no multi-byte character is split.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	cut := max(n-len(ellipsis), 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// parallelismFromEnv reads HIVE_MAX_PARALLEL_NODES and returns the bound
// for concurrent node dispatch. Invalid / unset values fall back to def.
// Values < 1 are clamped to 1; values > 64 are clamped to 64.
func parallelismFromEnv(def int) int {
	return boundFromEnv("HIVE_MAX_PARALLEL_NODES", def)
}

// fanConcurrency is how many items of one parallel_fan are in flight at
// once: HIVE_MAX_PARALLEL_FAN, else 4, read and clamped like
// HIVE_MAX_PARALLEL_NODES.
func fanConcurrency() int {
	return boundFromEnv("HIVE_MAX_PARALLEL_FAN", 4)
}

// boundFromEnv reads a concurrency bound from the environment variable name.
// Unset or unparseable values give def; values < 1 are clamped to 1 and
// values > 64 to 64.
func boundFromEnv(name string, def int) int {
	n, err := strconv.Atoi(os.Getenv(name))
	if err != nil {
		return def
	}
	return min(max(n, 1), 64)
}

// detectWave parses a leading "wN-" prefix out of a node name, for use in
// commit-message prefixes: a "w", in either case, then digits that some
// other character ends. Returns 0 if none found, so "wave-3" and a bare
// "w1" are wave 0.
func detectWave(nodeName string) int {
	rest, ok := strings.CutPrefix(strings.ToLower(nodeName), "w")
	if !ok {
		return 0
	}
	n, end := leadingWaveNumber(rest)
	if end == len(rest) {
		return 0
	}
	return n
}

// leadingWaveNumber is the decimal number s starts with, 0 when it starts
// with no digit, and the index of the first byte after its digits.
func leadingWaveNumber(s string) (n, end int) {
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		n = n*10 + int(s[end]-'0')
		end++
	}
	return n, end
}
