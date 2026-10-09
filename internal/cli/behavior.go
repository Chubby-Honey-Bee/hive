package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/spf13/cobra"
)

// newGenBehaviorCmd implements `chb gen-behavior`: it captures the current
// binary's input → canonicalised-output behaviour into a JSONL fixture
// file, suitable for byte-equal regression replay.
//
// Pure Go; no python3, no jq, no sqlite3 binary dependency. Reads
// the workspace DB through database/sql; canonicalises text with
// stdlib regexp.
func newGenBehaviorCmd() *cobra.Command {
	var (
		out          string
		workspaceDir string
	)
	cmd := &cobra.Command{
		Use:   "gen-behavior",
		Short: "Capture the CLI's input → canonical-output behaviour into JSONL fixtures",
		Long: `Generates fixtures/cli-behavior.jsonl by running a fixed sequence
of subcommands against a fresh workspace DB and recording each one's
exit code + canonicalised stdout + DB invariants.

The fixtures are byte-equal regression contracts. Use 'chb
replay-behavior' to verify that a binary produces the same outputs.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workspaceDir == "" {
				workspaceDir = "workspace/behavior-spec"
			}
			b := &behaviorGenerator{
				out:          out,
				workspaceDir: workspaceDir,
				stdout:       cmd.OutOrStdout(),
				stderr:       cmd.ErrOrStderr(),
			}
			return b.run(context.Background())
		},
	}
	cmd.Flags().StringVar(&out, "out", "fixtures/cli-behavior.jsonl",
		"output path for the JSONL fixtures")
	cmd.Flags().StringVar(&workspaceDir, "workspace", "",
		"workspace directory (default: workspace/behavior-spec)")
	return cmd
}

// newReplayBehaviorCmd implements `chb replay-behavior`: it replays the
// recorded fixtures against a binary and reports any drift.
func newReplayBehaviorCmd() *cobra.Command {
	var (
		fixtures  string
		bin       string
		only      string
		verbose   bool
		keepGoing bool
	)
	cmd := &cobra.Command{
		Use:   "replay-behavior",
		Short: "Replay fixtures produced by gen-behavior against any binary",
		Long: `Replays each fixture in fixtures/cli-behavior.jsonl against the
target binary (default: the running chb), comparing
exit code + canonicalised stdout + DB invariants. Reports
passed/failed counts at the end.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if bin == "" {
				exe, err := os.Executable()
				if err != nil {
					return fmt.Errorf("resolve self executable: %w", err)
				}
				bin = exe
			}
			r := &behaviorReplayer{
				fixturesPath: fixtures,
				bin:          bin,
				only:         only,
				verbose:      verbose,
				keepGoing:    keepGoing,
				stdout:       cmd.OutOrStdout(),
				stderr:       cmd.ErrOrStderr(),
			}
			return r.run(context.Background())
		},
	}
	cmd.Flags().StringVar(&fixtures, "fixtures", "fixtures/cli-behavior.jsonl",
		"path to fixtures JSONL")
	cmd.Flags().StringVar(&bin, "bin", "",
		"binary under test (default: this binary)")
	cmd.Flags().StringVar(&only, "only", "",
		"only run fixtures whose name contains this substring")
	cmd.Flags().BoolVar(&verbose, "verbose", false,
		"print every passing fixture + diff on failure")
	cmd.Flags().BoolVar(&keepGoing, "keep-going", false,
		"don't stop on first failure")
	return cmd
}

// behaviorFixture is where gen-behavior writes the fixtures and
// replay-behavior reads them, in a HIVE source checkout.
const behaviorFixture = "fixtures/cli-behavior.jsonl"

// hiveModule is the module path a HIVE source checkout's go.mod declares.
const hiveModule = "github.com/Chubby-Honey-Bee/hive"

// inHiveCheckout reports whether the working directory is a HIVE source
// checkout: its go.mod declares hiveModule. The behavior fixture is checked
// only there.
func inHiveCheckout() bool {
	data, err := os.ReadFile("go.mod")
	return err == nil && goModModule(data) == hiveModule
}

// goModModule is the module path go.mod data declares, "" when it declares
// none.
func goModModule(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`)
		}
	}
	return ""
}

// ── Shared canonicalisation ──────────────────────────────────────────

// canonicalisers is the ordered list of regex substitutions applied to
// stdout before fixture comparison, when a fixture is written and when it is
// replayed.
var canonicalisers = []struct {
	re   *regexp.Regexp
	repl string
}{
	{
		re:   regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z?`),
		repl: "<ts>",
	},
	{
		re:   regexp.MustCompile(`(id|ID)=[0-9]+`),
		repl: "${1}=<int>",
	},
	{
		re:   regexp.MustCompile(`"id":\s*[0-9]+`),
		repl: `"id":<int>`,
	},
	{
		re:   regexp.MustCompile(`"created_at":\s*"[^"]*"`),
		repl: `"created_at":"<ts>"`,
	},
	// Preflight reports which optional binaries are on PATH; that's
	// inherently environment-dependent (a dev machine has git + jq;
	// the node:22-alpine container has git but not jq or sqlite3).
	// Collapse the entire "PATH binary" run-state line to one
	// canonical form so the behavior fixture replays identically
	// across environments.
	{
		re:   regexp.MustCompile(`(?m)^( {2})(✓|⚠|✗) PATH binary( \(soft\))? — .*$`),
		repl: "$1<path-binary-check>",
	},
	// Pricing freshness is wall-clock dependent in two ways: the day count,
	// and the status marker itself, which flips ✓ → ⚠ once the table passes
	// 60 days. Canonicalising only the count would leave the fixture to rot
	// on a timer, red 60 days after it was recorded with no commit
	// responsible, so the whole line collapses, as with the PATH binary rule
	// above.
	{
		re:   regexp.MustCompile(`(?m)^( {2})(✓|⚠|✗) pricing freshness — .*$`),
		repl: "$1<pricing-freshness-check>",
	},
	// Provider auth reports whichever key or CLI the recording machine
	// happens to have (a dev box has `claude` on PATH; CI has a key; the
	// container may have neither), and per-node lines follow the same
	// shape. Collapse the line, same precedent as the two rules above.
	{
		re:   regexp.MustCompile(`(?m)^( {2})(✓|⚠|✗) (per-node )?provider auth — .*$`),
		repl: "$1<provider-auth-check>",
	},
	// Routing (routing profiles) reports where each role runs and whether any
	// leaves the machine. That follows the recording machine's provider (a
	// dev box resolves to the claude CLI; CI to a key or nothing), and the
	// number of lines varies with it: the off-machine warning appears only
	// when a role runs remotely. Collapse the whole run of routing lines to
	// one placeholder, same precedent as provider auth.
	{
		re:   regexp.MustCompile(`(?m)(^ {2}(✓|⚠|✗) routing — .*\n)+`),
		repl: "  <routing-check>\n",
	},
	// The version is whatever the build injected: "dev" locally, the tag in
	// a release. Pinned verbatim, a release binary would fail a fixture
	// recorded by a dev build.
	{
		re:   regexp.MustCompile(`(?m)^chb version \S+$`),
		repl: "chb version <version>",
	},
	// Running-binary path varies between a dev build (e.g. `/tmp/chb`)
	// and the container (`/usr/local/bin/chb`); strip the path so the
	// "running binary at <path>" warning is environment-invariant.
	{
		re:   regexp.MustCompile(`running binary at \S+`),
		repl: "running binary at <path>",
	},
}

// pathTailNormalizer rewrites any "\" that immediately follows "<path>"
// (one or more times, with intermediate path segments) into "/" so that
// fixtures captured on Linux replay identically on Windows. Without it,
// stdout like "<path>\hive.db" drifts vs the fixture's
// "<path>/hive.db".
var pathTailNormalizer = regexp.MustCompile(`<path>([^\s"']*)`)

// canonicalise applies every replacement in order, plus a per-line
// trailing-whitespace strip + workspace-path → "<path>" rewrite +
// path-separator normalization (Windows backslashes → forward slashes
// in the tail of every "<path>…" run) + CRLF → LF, so a Windows replay
// compares equal to a fixture recorded elsewhere.
func canonicalise(text, workspace string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for _, c := range canonicalisers {
		text = c.re.ReplaceAllString(text, c.repl)
	}
	if workspace != "" {
		text = strings.ReplaceAll(text, workspace, "<path>")
	}
	text = pathTailNormalizer.ReplaceAllStringFunc(text, func(m string) string {
		return strings.ReplaceAll(m, `\`, `/`)
	})
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// ── Generator ────────────────────────────────────────────────────────

type behaviorGenerator struct {
	out            string
	workspaceDir   string
	stdout, stderr io.Writer

	dbPath   string
	binPath  string
	fixtures []fixture

	// probeErr holds the first probe that could not be run at all — the
	// binary missing, the context cancelled. Such a probe yields exit=-1
	// and no output, and recording that as a fixture would pin a non-run
	// as the expected behaviour for every later replay.
	probeErr error
}

// behaviorAgent is the agent the fixture run records and its findings name.
const behaviorAgent = "behavior-spec"

// fixture is the on-disk record per probe.
type fixture struct {
	Name            string   `json:"name"`
	Cmd             []string `json:"cmd"`
	Exit            int      `json:"exit"`
	StdoutCanonical string   `json:"stdout_canonical"`
	DBInvariants    []string `json:"db_invariants"`
}

func (g *behaviorGenerator) run(ctx context.Context) error {
	ensureHarnessProviderEnv(g.stderr)
	if err := g.locate(); err != nil {
		return err
	}
	if err := g.freshDB(ctx); err != nil {
		return err
	}

	fmt.Fprintf(g.stderr, "── Capturing CLI behavior to %s ──\n", g.out)
	g.probeAll(ctx)
	if g.probeErr != nil {
		return fmt.Errorf("refusing to write fixtures from an incomplete capture: %w", g.probeErr)
	}
	g.appendFinalInvariants()
	return g.writeFixtures()
}

// locate resolves the running binary, which every probe runs, and the
// absolute workspace and database paths.
func (g *behaviorGenerator) locate() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve self: %w", err)
	}
	g.binPath = exe
	// canonicalise substitutes workspaceDir literally, and the CLI prints
	// absolute paths, so a relative --workspace is made absolute once, here:
	// the fixture carries no trace of the generating machine's home
	// directory, whichever form the caller passed.
	if abs, err := filepath.Abs(g.workspaceDir); err == nil {
		g.workspaceDir = abs
	}
	g.dbPath = filepath.Join(g.workspaceDir, "hive.db")
	return nil
}

// freshDB replaces the workspace's database with one the binary under test
// initialises, and points HIVE_DB_PATH at it.
func (g *behaviorGenerator) freshDB(ctx context.Context) error {
	if err := os.MkdirAll(g.workspaceDir, 0o755); err != nil {
		return err
	}
	_ = os.Remove(g.dbPath)
	os.Setenv("HIVE_DB_PATH", g.dbPath)

	// 1. db-init
	if _, _, _, err := g.exec(ctx, "db-init"); err != nil {
		return fmt.Errorf("db-init bootstrap: %w", err)
	}
	return nil
}

// probeAll runs the fixed probe sequence, recording a fixture per probe.
func (g *behaviorGenerator) probeAll(ctx context.Context) {
	g.probe(ctx, "version", nil, "--version")
	g.probe(ctx, "db-init-idempotent", nil, "db-init")
	g.probe(ctx, "dimension-add", nil, "db-write", "dimension",
		`{"name":"component","description":"x","values_json":"[\"a\",\"b\",\"c\",\"d\",\"e\",\"f\",\"g\",\"h\",\"i\",\"j\",\"k\",\"l\"]"}`)
	g.probe(ctx, "dimensions-list", nil, "db-read", "dimensions")
	// The run whose agent the findings below name.
	g.probe(ctx, "agent-run-create", nil, "db-write", "agent_run",
		fmt.Sprintf(`{"wave":1,"agent_name":%q,"agent_type":"verifier"}`, behaviorAgent))

	g.probe(ctx, "finding-definition", nil, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":1,"finding":"behavior spec test definition","mss_label":"definition"}`, behaviorAgent))
	g.probe(ctx, "finding-assumption", nil, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":2,"finding":"behavior spec test assumption","mss_label":"assumption","source_urls":"https://example.com/x"}`, behaviorAgent))
	g.probe(ctx, "finding-unknown", nil, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":3,"finding":"behavior spec test unknown","mss_label":"unknown"}`, behaviorAgent))

	defID := g.lookupFirstDefinitionID()
	g.probe(ctx, "finding-guarantee", nil, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":1,"finding":"behavior spec test guarantee","mss_label":"guarantee","depends_on_ids":[%d]}`, behaviorAgent, defID))

	g.probe(ctx, "definitions-read", nil, "db-read", "definitions")
	g.probe(ctx, "summary", nil, "db-read", "summary")
	g.probe(ctx, "mss-audit", nil, "db-read", "mss_audit")

	g.probe(ctx, "guarantee-without-deps-rejected", nil, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":4,"finding":"unsupported","mss_label":"guarantee"}`, behaviorAgent))
	g.probe(ctx, "bogus-mss-label-rejected", nil, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":4,"finding":"x","mss_label":"truthish"}`, behaviorAgent))

	g.probe(ctx, "workflow-list", nil, "workflow", "list")
	g.probe(ctx, "workflow-validate", nil, "workflow", "validate", "workflows/proof.yaml")
	g.probe(ctx, "preflight-proof", nil, "preflight", "workflows/proof.yaml")

	g.probe(ctx, "hive-init", nil, "hive", "init", "--project", "bspec")
	g.probe(ctx, "hive-status", nil, "hive", "status", "--project", "bspec")
	g.probe(ctx, "hive-next", nil, "hive", "next", "--project", "bspec")
}

// appendFinalInvariants records the final-invariants fixture: no command,
// just DB invariants.
func (g *behaviorGenerator) appendFinalInvariants() {
	totalFindings := g.scalarCount("SELECT COUNT(*) FROM findings")
	totalDimensions := g.scalarCount("SELECT COUNT(*) FROM dimensions")
	totalRuns := g.scalarCount("SELECT COUNT(*) FROM agent_runs")
	g.fixtures = append(g.fixtures, fixture{
		Name: "final-invariants",
		DBInvariants: []string{
			fmt.Sprintf("SELECT COUNT(*) FROM findings = %d", totalFindings),
			fmt.Sprintf("SELECT COUNT(*) FROM dimensions = %d", totalDimensions),
			fmt.Sprintf("SELECT COUNT(*) FROM agent_runs = %d", totalRuns),
		},
	})
	fmt.Fprintf(g.stderr, "  ✓ final-invariants (findings=%d dims=%d runs=%d)\n",
		totalFindings, totalDimensions, totalRuns)
}

// writeFixtures writes the fixtures as JSONL to the output path.
func (g *behaviorGenerator) writeFixtures() error {
	// Write in execution order — replay reuses the same DB across probes,
	// so dependency order (insert before read) must be preserved.
	if err := os.MkdirAll(filepath.Dir(g.out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(g.out)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := encodeBehaviorFixtures(f, g.fixtures); err != nil {
		return err
	}
	fmt.Fprintf(g.stderr, "\nWrote %d fixtures to %s\n", len(g.fixtures), g.out)
	return nil
}

// encodeBehaviorFixtures writes one JSON line per fixture, HTML characters
// unescaped.
func encodeBehaviorFixtures(w io.Writer, fixtures []fixture) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, fx := range fixtures {
		if err := enc.Encode(fx); err != nil {
			return err
		}
	}
	return nil
}

// exec runs the chb binary under test with `args` and captures
// stdout+stderr. Uses os.Executable() so we always test the running
// binary's behaviour, never an older one on PATH.
func (g *behaviorGenerator) exec(ctx context.Context, args ...string) (string, int, []string, error) {
	out, exit, err := runBehaviorBinary(ctx, append([]string{g.binPath}, args...))
	return out, exit, args, err
}

// runBehaviorBinary runs argv with the harness's child environment and
// returns its combined stdout and stderr and its exit code. The error is set
// only when the command could not run at all; the exit code is then -1.
func runBehaviorBinary(ctx context.Context, argv []string) (string, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = harnessChildEnv()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err == nil {
		return buf.String(), 0, nil
	}
	var ex *exec.ExitError
	if errors.As(err, &ex) {
		return buf.String(), ex.ExitCode(), nil
	}
	return buf.String(), -1, err
}

// probe runs one fixture probe and appends its result to g.fixtures.
// The leading token "chb" is a placeholder that the replayer
// substitutes with the binary under test (replay-behavior --bin),
// so the same fixture works against any binary that exposes the
// chb CLI surface.
func (g *behaviorGenerator) probe(ctx context.Context, name string, invariants []string, args ...string) {
	stdout, exit, _, err := g.exec(ctx, args...)
	if err != nil {
		if g.probeErr == nil {
			g.probeErr = fmt.Errorf("probe %q could not run: %w", name, err)
		}
		fmt.Fprintf(g.stderr, "  ✗ %s: %v\n", name, err)
		return
	}
	canon := canonicalise(stdout, g.workspaceDir)
	cmdJSON := append([]string{"chb"}, args...)
	g.fixtures = append(g.fixtures, fixture{
		Name:            name,
		Cmd:             cmdJSON,
		Exit:            exit,
		StdoutCanonical: canon,
		DBInvariants:    invariants,
	})
	fmt.Fprintf(g.stderr, "  ✓ %s (exit=%d, %d bytes)\n", name, exit, len(stdout))
}

// lookupFirstDefinitionID returns the smallest id of an MSS-definition
// finding. Used by the guarantee fixture so depends_on_ids is valid.
func (g *behaviorGenerator) lookupFirstDefinitionID() int64 {
	db, err := sql.Open("sqlite", "file:"+g.dbPath+"?mode=ro")
	if err != nil {
		return 1
	}
	defer db.Close()
	var id int64
	if err := db.QueryRow(
		`SELECT id FROM findings WHERE mss_label='definition' ORDER BY id LIMIT 1`,
	).Scan(&id); err != nil {
		return 1
	}
	return id
}

// scalarCount runs a one-row scalar query and returns its int value.
func (g *behaviorGenerator) scalarCount(query string) int64 {
	db, err := sql.Open("sqlite", "file:"+g.dbPath+"?mode=ro")
	if err != nil {
		return 0
	}
	defer db.Close()
	var n int64
	_ = db.QueryRow(query).Scan(&n)
	return n
}

// ── Replayer ─────────────────────────────────────────────────────────

type behaviorReplayer struct {
	fixturesPath   string
	bin            string
	only           string
	verbose        bool
	keepGoing      bool
	stdout, stderr io.Writer
}

// behaviorReplayRun is one replay: the temporary workspace and database it
// replays against, and its tally so far.
type behaviorReplayRun struct {
	workspace string
	dbPath    string
	pass      int
	failures  []string
}

func (r *behaviorReplayer) run(ctx context.Context) error {
	ensureHarnessProviderEnv(os.Stderr)
	if _, err := os.Stat(r.fixturesPath); err != nil {
		return fmt.Errorf("fixtures not found: %s", r.fixturesPath)
	}
	workspace, err := os.MkdirTemp("", "chb-replay-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	rr := r.freshReplayRun(ctx, workspace)
	if err := r.replayFixtures(ctx, rr); err != nil {
		return err
	}
	return r.printSummary(rr)
}

// freshReplayRun points HIVE_DB_PATH at a database in workspace and
// initialises it.
func (r *behaviorReplayer) freshReplayRun(ctx context.Context, workspace string) *behaviorReplayRun {
	rr := &behaviorReplayRun{workspace: workspace, dbPath: filepath.Join(workspace, "hive.db")}
	os.Setenv("HIVE_DB_PATH", rr.dbPath)

	// Fresh DB. Its result is ignored: db-init may report "already exists"
	// on rerun.
	_, _ = exec.CommandContext(ctx, r.bin, "db-init").CombinedOutput()
	return rr
}

// replayFixtures replays the fixtures file line by line. A line that does
// not parse ends the replay with an error, and so, without --keep-going,
// does the first failing fixture, without one.
func (r *behaviorReplayer) replayFixtures(ctx context.Context, rr *behaviorReplayRun) error {
	data, err := os.ReadFile(r.fixturesPath)
	if err != nil {
		return err
	}
	for lineNum, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if done, err := r.replayLine(ctx, rr, lineNum, line); done {
			return err
		}
	}
	return nil
}

// replayLine replays the fixture on one line of the file and reports
// whether the replay ends there.
func (r *behaviorReplayer) replayLine(ctx context.Context, rr *behaviorReplayRun, lineNum int, line string) (bool, error) {
	if line == "" {
		return false, nil
	}
	var fx fixture
	if err := json.Unmarshal([]byte(line), &fx); err != nil {
		return true, fmt.Errorf("parse fixture line %d: %w", lineNum+1, err)
	}
	if r.skips(fx.Name) {
		return false, nil
	}
	return r.record(rr, fx.Name, r.runOne(ctx, fx, rr.workspace, rr.dbPath)), nil
}

// skips reports whether --only leaves the named fixture out.
func (r *behaviorReplayer) skips(name string) bool {
	return r.only != "" && !strings.Contains(name, r.only)
}

// record counts one fixture's result and reports whether the replay stops:
// on a failure, unless --keep-going.
func (r *behaviorReplayer) record(rr *behaviorReplayRun, name string, ok bool) bool {
	if !ok {
		rr.failures = append(rr.failures, name)
		return !r.keepGoing
	}
	rr.pass++
	if r.verbose {
		fmt.Fprintf(r.stdout, "  ✓ %s\n", name)
	}
	return false
}

// printSummary prints the replay's tally and fails when any fixture did.
func (r *behaviorReplayer) printSummary(rr *behaviorReplayRun) error {
	fail := len(rr.failures)
	fmt.Fprintln(r.stdout, "")
	fmt.Fprintln(r.stdout, "── replay summary ──")
	fmt.Fprintf(r.stdout, "  binary:   %s\n", r.bin)
	fmt.Fprintf(r.stdout, "  fixtures: %s\n", r.fixturesPath)
	fmt.Fprintf(r.stdout, "  passed:   %d\n", rr.pass)
	fmt.Fprintf(r.stdout, "  failed:   %d\n", fail)
	if fail == 0 {
		return nil
	}
	fmt.Fprintln(r.stdout, "  failures:")
	for _, n := range rr.failures {
		fmt.Fprintf(r.stdout, "    - %s\n", n)
	}
	return fmt.Errorf("%d fixtures failed", fail)
}

// runOne executes one fixture and returns true on full pass.
func (r *behaviorReplayer) runOne(ctx context.Context, fx fixture, workspace, dbPath string) bool {
	// Invariant-only fixture.
	if len(fx.Cmd) == 0 {
		return r.checkInvariants(fx.DBInvariants, dbPath)
	}
	return r.replayCommand(ctx, fx, workspace) && r.checkInvariants(fx.DBInvariants, dbPath)
}

// replayCommand runs the fixture's command against the binary under test and
// reports whether its exit code and canonical stdout match the fixture's.
func (r *behaviorReplayer) replayCommand(ctx context.Context, fx fixture, workspace string) bool {
	out, exit, err := runBehaviorBinary(ctx, r.commandArgv(fx.Cmd))
	if err != nil {
		fmt.Fprintf(r.stderr, "  ✗ %s — run error: %v\n", fx.Name, err)
		return false
	}
	return r.exitMatches(fx, exit, out) && r.stdoutMatches(fx, out, workspace)
}

// commandArgv is a fixture's command with the "chb" placeholder replaced by
// the binary under test.
func (r *behaviorReplayer) commandArgv(cmd []string) []string {
	argv := append([]string{}, cmd...)
	if len(argv) > 0 && argv[0] == "chb" {
		argv[0] = r.bin
	}
	return argv
}

// exitMatches reports whether the command exited as the fixture recorded,
// and says how it differs when not, with the output's head under --verbose.
func (r *behaviorReplayer) exitMatches(fx fixture, exit int, out string) bool {
	if exit == fx.Exit {
		return true
	}
	fmt.Fprintf(r.stderr, "  ✗ %s — exit %d ≠ %d\n", fx.Name, exit, fx.Exit)
	if r.verbose {
		fmt.Fprintln(r.stderr, clip(out, 400))
	}
	return false
}

// stdoutMatches reports whether the command's canonical output is the
// fixture's, and shows both under --verbose when not.
func (r *behaviorReplayer) stdoutMatches(fx fixture, out, workspace string) bool {
	canon := canonicalise(out, workspace)
	if canon == fx.StdoutCanonical {
		return true
	}
	fmt.Fprintf(r.stderr, "  ✗ %s — stdout drift\n", fx.Name)
	if r.verbose {
		fmt.Fprintln(r.stderr, "expected:")
		fmt.Fprintln(r.stderr, fx.StdoutCanonical)
		fmt.Fprintln(r.stderr, "got:")
		fmt.Fprintln(r.stderr, canon)
	}
	return false
}

// checkInvariants evaluates each "SELECT … = N" string against the
// workspace DB. Returns true iff every invariant holds.
func (r *behaviorReplayer) checkInvariants(invariants []string, dbPath string) bool {
	if len(invariants) == 0 {
		return true
	}
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		fmt.Fprintf(r.stderr, "    invariant open db: %v\n", err)
		return false
	}
	defer conn.Close()
	return r.invariantsHold(conn, invariants)
}

// invariantsHold reports whether every invariant holds, stopping at the
// first that does not.
func (r *behaviorReplayer) invariantsHold(conn *sql.DB, invariants []string) bool {
	for _, inv := range invariants {
		if !r.invariantHolds(conn, inv) {
			return false
		}
	}
	return true
}

// invariantHolds evaluates one "SELECT … = N" invariant and says why on
// stderr when it does not hold.
func (r *behaviorReplayer) invariantHolds(conn *sql.DB, inv string) bool {
	// Format: "SELECT … = N" — split on the last " = ".
	i := strings.LastIndex(inv, " = ")
	if i < 0 {
		fmt.Fprintf(r.stderr, "    invariant malformed: %q\n", inv)
		return false
	}
	query := inv[:i]
	want := strings.TrimSpace(inv[i+3:])
	var got int64
	if err := conn.QueryRow(query).Scan(&got); err != nil {
		fmt.Fprintf(r.stderr, "    invariant query %q: %v\n", query, err)
		return false
	}
	if fmt.Sprintf("%d", got) != want {
		fmt.Fprintf(r.stderr, "    invariant FAIL: %s → got %d, want %s\n",
			query, got, want)
		return false
	}
	return true
}
