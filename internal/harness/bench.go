package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
	"github.com/Chubby-Honey-Bee/hive/internal/bench"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// benchReport is what a bench case measured, per arm and per node.
type benchReport struct {
	Items    int                                 `json:"items"`
	Pairs    int                                 `json:"pairs"`
	Arms     map[string]bench.Summary            `json:"arms"`
	Families map[string]map[string]bench.Summary `json:"families"` // family → arm → summary
	// Lift is swarm minus solo class-balanced accuracy: HIVE's gain over one
	// call of the same model on these items.
	Lift    *float64    `json:"lift,omitempty"`
	Nodes   []benchNode `json:"nodes"`
	Results string      `json:"results"`
}

// benchNode is one node's mean cost over the runs of one arm.
type benchNode struct {
	Arm         string  `json:"arm"`
	Node        string  `json:"node"`
	Runs        int     `json:"runs"`
	TokensIn    float64 `json:"mean_tokens_in"`
	TokensOut   float64 `json:"mean_tokens_out"`
	WallSeconds float64 `json:"mean_wall_seconds"`
}

// runBenchCase generates the case's twin items from the live roster, after
// those of its exclude_seeds, and runs every item on every arm, --reps
// times. The swarm arm is `chb ask` with the roster pack as --context; the
// solo arm is one call of the lens model with no persona over the same
// question and pack. Each run is graded and appended to <ws>/results.jsonl.
// Wrong answers and runs that did not complete are data, not failures —
// chb bench decide's guards judge them. The case fails when items cannot be
// generated, its workflows cannot be written, the endpoint fails the model
// preflight before the first run, a run reaches no model or does not
// complete and the endpoint then fails the preflight (the case stops
// there), no run completed, a run changed its tree, or the results cannot
// be written — and, when it sets require_pairs, when any twin was answered
// wrong. A case that stopped, or in which no run completed, writes its rows
// marked as not a measurement (bench.WriteNotMeasured).
func (h *agentHarness) runBenchCase(ctx context.Context, c harnessCase, ws string, check, warn func(string, bool, string)) *benchReport {
	items, arms, ok := h.planBenchCase(c, check)
	if !ok {
		return nil
	}
	b := &benchCase{h: h, c: c, ws: ws, items: items, arms: arms, preset: c.preset()}
	if !b.prepare(ctx, check) {
		return nil
	}
	resultsPath := b.run(ctx, check, warn)
	if !h.finishSweep(&b.sweep, check) {
		return nil
	}
	report := benchSummary(b.rows, items, arms, resultsPath)
	checkBenchPairs(c, b.rows, report, arms, check)
	return report
}

// planBenchCase generates the case's twin items from the live roster and
// checks its arms; false when either fails.
func (h *agentHarness) planBenchCase(c harnessCase, check func(string, bool, string)) ([]bench.Item, []string, bool) {
	all, err := h.LoadForagers()
	var items []bench.Item
	if err == nil {
		items, err = benchItems(all, c)
	}
	check("twin items generate from the live roster", err == nil, benchGenDetail(items, err))
	if err != nil {
		return nil, nil, false
	}
	arms := c.benchArms()
	if a, unknown := harnessUnknownArm(arms, bench.ArmSwarm, bench.ArmSolo); unknown {
		check("arms are swarm or solo", false, "unknown arm "+a)
		return nil, nil, false
	}
	return items, arms, true
}

// benchCase is one bench case's run: its items and arms, the preset, and
// the rows and sweep of its runs.
type benchCase struct {
	h      *agentHarness
	c      harnessCase
	ws     string
	items  []bench.Item
	arms   []string
	preset string
	rows   []bench.Row
	sweep  harnessSweep
}

// prepare writes the workflows the runs will dispatch, asks the endpoint
// whether it serves every model they send, as each run's agent-run will,
// and clears the workspace; false when any of it fails.
func (b *benchCase) prepare(ctx context.Context, check func(string, bool, string)) bool {
	cfg, defns, err := b.h.benchWorkflows(ctx, b.items, b.arms, b.preset, b.c.Eval, b.c.LensTools)
	check("the workflows the runs dispatch are written (chb ask --no-dispatch, the solo control, --profile)", err == nil, errString(err))
	if err != nil {
		return false
	}
	b.sweep = newHarnessSweep(len(b.items)*b.h.Reps*len(b.arms), cfg, defns, "no verdict")
	return b.h.preflightEndpoint(ctx, cfg, defns, check) && resetHarnessWorkspace(b.ws, check)
}

// run runs every item on every arm, --reps times, until an outage stops
// the case, writes the rows, and returns their path. A bash call or the
// claude CLI's own tools can write outside a run's private tree, where
// treeCheck does not look, so it compares the checkout's tracked files
// before and after.
func (b *benchCase) run(ctx context.Context, check, warn func(string, bool, string)) string {
	before, beforeErr := dirtyTrackedFiles()
	b.runAll(ctx)
	path := b.record(check)
	warnCheckoutChanged(before, beforeErr, warn)
	return path
}

// runAll runs the items, repetitions and arms in order until every run is
// done or one stops the case.
func (b *benchCase) runAll(ctx context.Context) {
	for _, it := range b.items {
		for rep := 1; rep <= b.h.Reps; rep++ {
			if !b.runArms(ctx, it, rep) {
				return
			}
		}
	}
}

// runArms runs one repetition of the item on each arm; false when a run
// stopped the case.
func (b *benchCase) runArms(ctx context.Context, it bench.Item, rep int) bool {
	for _, a := range b.arms {
		if !b.runOne(ctx, it, a, rep) {
			return false
		}
	}
	return true
}

// runOne runs the item once on arm a, logs it, and counts it; false when
// it stopped the case.
func (b *benchCase) runOne(ctx context.Context, it bench.Item, a string, rep int) bool {
	row, end := b.h.runBenchItem(ctx, it, a, rep, filepath.Join(b.ws, it.ID, fmt.Sprintf("%s-r%d", a, rep)), b.preset, b.c.Eval, b.c.LensTools)
	b.rows = append(b.rows, row)
	b.sweep.count(row.Completed, row.TreeOK, row.Item+"/"+a)
	b.h.logf("  %s %-10s %-5s r%d want %-7s got %s", harnessMark(row.Correct), it.ID, a, rep, row.Want, orDash(row.Got))
	if row.Completed {
		return true
	}
	return !b.sweep.stopsAt(ctx, end.run, end.failed, row.Error, fmt.Sprintf("%s %s r%d", row.Item, a, rep))
}

// record checks that the case did not stop, writes results.jsonl — marked
// as not a measurement, which chb bench decide refuses, when it stopped or
// no run completed — and checks every run's private tree. It returns the
// results' path.
func (b *benchCase) record(check func(string, bool, string)) string {
	path := filepath.Join(b.ws, "results.jsonl")
	notMeasured := b.sweep.checkMeasured(check)
	err := writeHarnessResults(path, benchRowsWriter(b.rows, notMeasured))
	checkHarnessResults(check, err, notMeasured, "chb bench decide")
	check("every run left its private tree unchanged", len(b.sweep.changed) == 0, strings.Join(b.sweep.changed, ", "))
	return path
}

// benchRowsWriter writes the rows, under a not_measured line when
// notMeasured says why they are not a measurement.
func benchRowsWriter(rows []bench.Row, notMeasured string) func(io.Writer) error {
	if notMeasured != "" {
		return func(w io.Writer) error { return bench.WriteNotMeasured(w, notMeasured, rows) }
	}
	return func(w io.Writer) error { return bench.WriteRows(w, rows) }
}

// checkBenchPairs is require_pairs: unless every run completed and both
// twins of every pair were answered right on every arm, the case fails. A
// run that did not complete is a failed setup — a refused request, a
// deadline, a failed Queen — not a wrong answer, so it is named with its
// error under its own check.
func checkBenchPairs(c harnessCase, rows []bench.Row, report *benchReport, arms []string, check func(string, bool, string)) {
	if !c.RequirePairs {
		return
	}
	incomplete, wrong := benchRunFaults(rows)
	check(fmt.Sprintf("every run completed (%d of %d)", len(rows)-len(incomplete), len(rows)), len(incomplete) == 0, strings.Join(incomplete, "; "))
	passed, ok := benchPairsPassed(report, arms)
	if len(incomplete) > 0 {
		wrong = append(wrong, fmt.Sprintf("%d run(s) did not complete, named above", len(incomplete)))
	}
	check("both twins of every pair answered right ("+strings.Join(passed, ", ")+" pairs)", ok, strings.Join(wrong, "; "))
}

// benchRunFaults names the runs that did not complete, with their error,
// and the runs answered wrong.
func benchRunFaults(rows []bench.Row) (incomplete, wrong []string) {
	for _, row := range rows {
		switch {
		case !row.Completed:
			incomplete = append(incomplete, fmt.Sprintf("%s %s r%d: %s", row.Item, row.Arm, row.Rep, orDash(lastLine(row.Error))))
		case !row.Correct:
			wrong = append(wrong, fmt.Sprintf("%s %s r%d want %s got %s", row.Item, row.Arm, row.Rep, row.Want, row.Got))
		}
	}
	return incomplete, wrong
}

// benchPairsPassed says each arm's pairs passed, and whether every arm
// passed every pair it has, having at least one.
func benchPairsPassed(report *benchReport, arms []string) ([]string, bool) {
	var passed []string
	ok := true
	for _, a := range arms {
		s := report.Arms[a]
		passed = append(passed, fmt.Sprintf("%s %d/%d", a, s.PairsPassed, s.Pairs))
		ok = ok && s.Pairs > 0 && s.PairsPassed == s.Pairs
	}
	return passed, ok
}

// lastLine is the last non-empty line of s, where a failed run's output
// says why it failed.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// firstLine is the first line of s, trimmed.
func firstLine(s string) string {
	return strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
}

// endpointCause is the model preflight's error, led, when nothing listens
// at the endpoint, by where and what to do.
func endpointCause(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Sprintf("nothing listens at %s (connection refused); is the model server running? For Ollama, open the app or run `ollama serve`. %v", ue.URL, err)
	}
	return err.Error()
}

// benchItems generates a bench case's items: its seeds, drawn after its
// exclude_seeds so that none repeats one of their items.
func benchItems(all []foragers.Forager, c harnessCase) ([]bench.Item, error) {
	return bench.GenerateAfter(all, c.Families, c.ExcludeSeeds, c.Seeds)
}

// benchGenDetail says how many items and pairs were generated, or why
// none were.
func benchGenDetail(items []bench.Item, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%d items in %d twin pairs", len(items), len(items)/2)
}

// orDash is s, or a dash when s is empty.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// fmtPtr writes v to two places, or a dash when there is none.
func fmtPtr(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *v)
}

// runBenchItem runs one item on one arm in its own directory, private tree
// and database, grades it, and moves the directory to dest. The run works
// in a fresh temporary directory rather than in dest: the workspace holds
// the item's ID in its path and earlier runs' results, which name the
// expected answers, and a lens looking around its working directory should
// find neither.
func (h *agentHarness) runBenchItem(ctx context.Context, it bench.Item, arm string, rep int, dest, preset string, eval bool, lensTools string) (row bench.Row, end benchEnd) {
	row = h.newBenchRow(it, arm, rep)
	dir, err := os.MkdirTemp("", "chb-bench-")
	if err != nil {
		row.Error = err.Error()
		return row, end
	}
	defer func() {
		if err := moveDir(dir, dest); err != nil {
			row.Error = strings.TrimSpace(row.Error + " move the run into the workspace: " + err.Error())
		}
	}()
	end = h.runBenchItemIn(ctx, dir, benchRunSpec{it: it, arm: arm, rep: rep, preset: preset, eval: eval, lensTools: lensTools}, &row)
	return row, end
}

// benchRunSpec is one run of a bench item: the item, its arm and
// repetition, and the case's preset, coverage pass and lens tools.
type benchRunSpec struct {
	it        bench.Item
	arm       string
	rep       int
	preset    string
	eval      bool
	lensTools string
}

// newBenchRow is the row of one run of it on arm, with the configuration
// and the models it runs on. Under a profile the lenses, the solo control
// (role lens) and Queen run on its routes, which HIVE_PROFILE carries
// to chb ask and agent-run.
func (h *agentHarness) newBenchRow(it bench.Item, arm string, rep int) bench.Row {
	row := bench.NewRow(it, arm, rep)
	row.Config, row.Provider, row.QueenModel = h.configName(), h.Provider, h.QueenModel
	row.LensModel = h.benchArmModel(it, arm, rep)
	if h.Profile != "" {
		row.LensModel, row.QueenModel = h.profileLens.Model, h.profileQueen.Model
	}
	return row
}

// benchArmModel is the lens model a run on arm records: the swarm's lens
// list, or the solo control's model.
func (h *agentHarness) benchArmModel(it bench.Item, arm string, rep int) string {
	switch arm {
	case bench.ArmSwarm:
		return strings.Join(h.benchLensModels(it, rep), ",")
	case bench.ArmSolo:
		return h.soloModel()
	}
	return ""
}

// runBenchItemIn runs the item in dir with its own private tree and
// database, and grades the run into row.
func (h *agentHarness) runBenchItemIn(ctx context.Context, dir string, s benchRunSpec, row *bench.Row) benchEnd {
	tree, err := h.prepareTree(dir)
	if err != nil {
		row.Error = err.Error()
		return benchEnd{}
	}
	manifest, merr := treeManifest(tree)
	dbPath := filepath.Join(dir, "hive.db")
	env := treeEnv(h.caseEnv(dbPath), tree)
	start := time.Now()
	step, args, err := h.benchArmCommand(dir, dbPath, s)
	if err != nil {
		row.Error = err.Error()
		return benchEnd{}
	}
	out, runErr := h.exec(ctx, dir, step, env, tree, h.Self, args...)
	row.WallSeconds = time.Since(start).Seconds()
	noteBenchRun(row, out, runErr, tree, manifest, merr)
	return gradeBenchRun(row, s.it, dbPath, filepath.Join(dir, "artifact.json"), runErr == nil)
}

// benchArmCommand is the run's chb step and arguments: chb ask for the
// swarm, or agent-run of the solo control's workflow, which it writes
// into dir.
func (h *agentHarness) benchArmCommand(dir, dbPath string, s benchRunSpec) (string, []string, error) {
	if s.arm == bench.ArmSwarm {
		return "ask", h.askArgs(s.it, s.rep, dir, s.preset, s.eval, s.lensTools, h.benchLensModels(s.it, s.rep)), nil
	}
	wf := filepath.Join(dir, "solo.yaml")
	if err := os.WriteFile(wf, []byte(soloWorkflow(h.soloModel(), h.LensReasoning)), 0o644); err != nil {
		return "", nil, err
	}
	inputs, _ := json.Marshal(map[string]string{"question": s.it.Question, "context": s.it.Context})
	return "agent-run", h.agentRunArgs(dbPath, wf, "", inputs, s.rep), nil
}

// agentRunArgs are the chb agent-run arguments of a workflow wf on dbPath
// with inputs, in dir when it is set, at repetition rep's seed.
func (h *agentHarness) agentRunArgs(dbPath, wf, dir string, inputs []byte, rep int) []string {
	args := []string{"--db", dbPath, "agent-run", wf}
	if dir != "" {
		args = append(args, "--dir", dir)
	}
	args = append(args, "--inputs", string(inputs), "--branch", "", "--budget-mode", h.BudgetMode)
	return append(args, h.seedArgs(rep)...)
}

// noteBenchRun records whether the run left its private tree unchanged,
// and its error: the tail of its output when it failed, then what in the
// tree changed.
func noteBenchRun(row *bench.Row, out string, runErr error, tree string, manifest map[string]string, merr error) {
	var changes string
	row.TreeOK, changes = treeCheck(tree, manifest, merr)
	if runErr != nil {
		row.Error = tail(out)
	}
	if !row.TreeOK {
		row.Error = strings.TrimSpace(row.Error + " tree changed: " + changes)
	}
}

// askArgs are the chb ask arguments of a swarm run of it that works in dir,
// on the lens list lensModels.
func (h *agentHarness) askArgs(it bench.Item, rep int, dir, preset string, eval bool, lensTools string, lensModels []string) []string {
	return append([]string{"--db", filepath.Join(dir, "hive.db"), "ask", it.Question, "--context", it.Context, "--foragers", preset,
		"--budget-mode", h.BudgetMode, "--artifact", filepath.Join(dir, "artifact.json"),
		"--out", filepath.Join(dir, "swarm.yaml")}, h.swarmRunArgs(eval, lensTools, lensModels, rep)...)
}

// soloModel is the solo control's model: the first of --lens-model, or ""
// for the lenses' tier.
func (h *agentHarness) soloModel() string {
	if len(h.lensModels) > 0 {
		return h.lensModels[0]
	}
	return ""
}

// benchWorkflows writes the workflows a bench case's runs will dispatch and
// routes them as their agent-run will: the swarm's, from chb ask
// --no-dispatch with each lens list the case's items and repetitions rotate
// to, and the solo control's. It returns them with the config their
// agent-run resolves: under a profile, the profile's provider.
func (h *agentHarness) benchWorkflows(ctx context.Context, items []bench.Item, arms []string, preset string, eval bool, lensTools string) (runner.Config, []map[string]any, error) {
	cfg, profile, err := h.harnessRouting()
	if err != nil {
		return cfg, nil, err
	}
	texts, err := h.benchWorkflowTexts(ctx, items, arms, preset, eval, lensTools)
	if err != nil {
		return cfg, nil, err
	}
	defns, err := routeHarnessWorkflows(texts, profile)
	return cfg, defns, err
}

// benchWorkflowTexts writes, in a scratch directory, the workflows each
// arm's runs dispatch.
func (h *agentHarness) benchWorkflowTexts(ctx context.Context, items []bench.Item, arms []string, preset string, eval bool, lensTools string) ([]string, error) {
	dir, err := os.MkdirTemp("", "chb-bench-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var texts []string
	for _, a := range arms {
		armTexts, err := h.benchArmWorkflows(ctx, dir, a, items, preset, eval, lensTools)
		if err != nil {
			return nil, err
		}
		texts = append(texts, armTexts...)
	}
	return texts, nil
}

// benchArmWorkflows is one arm's workflows: the solo control's, or the
// swarm's for each distinct lens list, each from chb ask --no-dispatch in
// its own numbered directory of dir.
func (h *agentHarness) benchArmWorkflows(ctx context.Context, dir, arm string, items []bench.Item, preset string, eval bool, lensTools string) ([]string, error) {
	if arm == bench.ArmSolo {
		return []string{soloWorkflow(h.soloModel(), h.LensReasoning)}, nil
	}
	var texts []string
	for i, lr := range h.benchLensRuns(items) {
		text, err := h.benchNoDispatch(ctx, filepath.Join(dir, strconv.Itoa(i+1)), lr, preset, eval, lensTools)
		if err != nil {
			return nil, err
		}
		texts = append(texts, text)
	}
	return texts, nil
}

// benchLensRun is the first item and repetition that runs on one lens list.
type benchLensRun struct {
	it   bench.Item
	rep  int
	lens []string
}

// benchLensRuns is, for each distinct lens list the items and repetitions
// rotate to, the first run on it.
func (h *agentHarness) benchLensRuns(items []bench.Item) []benchLensRun {
	var out []benchLensRun
	seen := map[string]bool{}
	for _, it := range items {
		for rep := 1; rep <= h.Reps; rep++ {
			lens := h.benchLensModels(it, rep)
			if key := strings.Join(lens, ","); !seen[key] {
				seen[key] = true
				out = append(out, benchLensRun{it: it, rep: rep, lens: lens})
			}
		}
	}
	return out
}

// benchNoDispatch writes the swarm workflow chb ask --no-dispatch writes
// for one lens run, in sub, and returns it.
func (h *agentHarness) benchNoDispatch(ctx context.Context, sub string, lr benchLensRun, preset string, eval bool, lensTools string) (string, error) {
	if err := os.Mkdir(sub, 0o755); err != nil {
		return "", err
	}
	args := append(h.askArgs(lr.it, lr.rep, sub, preset, eval, lensTools, lr.lens), "--no-dispatch")
	if out, err := h.exec(ctx, sub, "ask", h.caseEnv(filepath.Join(sub, "hive.db")), "", h.Self, args...); err != nil {
		return "", fmt.Errorf("chb ask --no-dispatch: %s", tail(out))
	}
	raw, err := os.ReadFile(filepath.Join(sub, "swarm.yaml"))
	return string(raw), err
}

// checkEndpoints runs the runner's model preflight over each workflow, as
// each run's agent-run does before its first call. It returns the
// preflight's summaries, or its first error.
func checkEndpoints(ctx context.Context, cfg runner.Config, defns []map[string]any) (string, error) {
	var lines []string
	for _, defn := range defns {
		checks, err := runner.PreflightEndpointModels(ctx, cfg, defn)
		if err != nil {
			return "", err
		}
		lines = appendEndpointSummaries(lines, checks)
	}
	return strings.Join(lines, "; "), nil
}

// appendEndpointSummaries appends each check's summary that lines does not
// hold yet.
func appendEndpointSummaries(lines []string, checks []*runner.ModelCheck) []string {
	for _, c := range checks {
		if s := c.Summary(); !slices.Contains(lines, s) {
			lines = append(lines, s)
		}
	}
	return lines
}

// benchLensModels is the lens model list for one run of an item: the
// --lens-model list rotated by the item's seed plus the repetition. chb
// ask gives the lenses, in name order, the list's models in turn, so
// without the rotation the same personas would always run on the same
// family. With it, each lens runs on each model across seeds and
// repetitions. Twins share a seed, so both run on one assignment.
func (h *agentHarness) benchLensModels(it bench.Item, rep int) []string {
	n := int64(len(h.lensModels))
	if n < 2 {
		return h.lensModels
	}
	k := ((it.Seed+int64(rep-1))%n + n) % n
	return append(slices.Clone(h.lensModels[k:]), h.lensModels[:k]...)
}

// moveDir moves src to dst, replacing dst, and copies it when a rename
// fails, as it does across devices.
func moveDir(src, dst string) error {
	if err := clearMoveDest(dst); err != nil {
		return err
	}
	if os.Rename(src, dst) == nil {
		return nil
	}
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// clearMoveDest makes dst's parent and removes dst, for a move to replace it.
func clearMoveDest(dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.RemoveAll(dst)
}

// benchEnd is what a bench run's database says about how it ended.
type benchEnd struct {
	// run: the run left a workflow_runs row. agent-run makes every setup
	// refusal, its model preflight among them, before it writes one, so a
	// run without one reached no model.
	run bool
	// failed is the error of the run's first failed or rejected node, ""
	// when none has one.
	failed string
}

// gradeBenchRun scores the run's answer. A run that left no answer is
// graded like any other: its verdict is empty and it named no tokens.
func gradeBenchRun(row *bench.Row, it bench.Item, dbPath, artifactPath string, exitedOK bool) benchEnd {
	verdict, text, end := readBenchAnswer(row, dbPath, artifactPath)
	row.Got = strings.ToLower(strings.TrimSpace(verdict))
	row.Completed = exitedOK && row.Got != ""
	row.Correct, row.GotTokens, row.TokenF1 = bench.Grade(it, row.Got, text)
	return end
}

// readBenchAnswer reads a run's answer from its database and artifact, and
// fills the row's per-node, schema and full-verdict counts on the way. The
// swarm's answer is Queen's: her verdict from the artifact, her report for
// tokens (her full text when her outputs hold no report). Its lens answers
// and diversity come from the node rows, so a run whose Queen failed, and
// which wrote no artifact, still has them. The solo arm's is its one
// node's. end says whether the run left a run row, and why it failed.
func readBenchAnswer(row *bench.Row, dbPath, artifactPath string) (verdict, text string, end benchEnd) {
	store, err := db.NewStore(dbPath)
	if err != nil {
		row.Error = strings.TrimSpace(row.Error + " open db: " + err.Error())
		return "", "", end
	}
	defer store.Close()
	run, _ := harnessLastRun(store)
	if run == nil {
		return "", "", end
	}
	end = benchEnd{run: true, failed: harnessFailedNodeError(store, run.ID)}
	readBenchRunStats(store, run.ID, row)
	verdict, text = benchRunReader{store: store, runID: run.ID}.answer(row, artifactPath)
	return verdict, text, end
}

// harnessFailedNodeError is the error of the run's first failed or rejected
// node that has one, "" when none has.
func harnessFailedNodeError(store *db.Store, runID int64) string {
	var failed string
	_ = store.ReadDB.QueryRow(`SELECT COALESCE(error, '') FROM workflow_node_states WHERE run_id = ? AND status IN ('failed', 'rejected') AND COALESCE(error, '') != '' ORDER BY id LIMIT 1`,
		runID).Scan(&failed)
	return failed
}

// readBenchRunStats fills the row's per-node stats, finish reasons and
// token totals from the run's node rows.
func readBenchRunStats(store *db.Store, runID int64, row *bench.Row) {
	var err error
	if row.Nodes, err = nodeStats(store, runID); err != nil {
		row.Error = strings.TrimSpace(row.Error + " node stats: " + err.Error())
	}
	if row.FinishLength, err = finishLength(store, runID); err != nil {
		row.Error = strings.TrimSpace(row.Error + " finish reasons: " + err.Error())
	}
	for _, n := range row.Nodes {
		row.TokensIn += n.TokensIn
		row.TokensOut += n.TokensOut
	}
}

// benchRunReader reads one bench run's node rows.
type benchRunReader struct {
	store *db.Store
	runID int64
}

// rationale is a node's text and its stored outputs.
func (r benchRunReader) rationale(node string) (text, outputs string) {
	_ = r.store.ReadDB.QueryRow(`SELECT COALESCE(rationale, ''), COALESCE(outputs_json, '') FROM workflow_node_states WHERE run_id = ? AND node_name = ?`,
		r.runID, node).Scan(&text, &outputs)
	return text, outputs
}

// answer reads the run's verdict and the text its tokens are graded on, by
// its arm.
func (r benchRunReader) answer(row *bench.Row, artifactPath string) (verdict, text string) {
	switch row.Arm {
	case bench.ArmSwarm:
		return r.swarmAnswer(row, artifactPath)
	case bench.ArmSolo:
		return r.soloAnswer(row)
	}
	return "", ""
}

// swarmAnswer reads a swarm run's answer: Queen's verdict from the
// artifact and her text for tokens, the lens answers and their diversity
// from the node rows, and each lens's schema and full-verdict counts.
func (r benchRunReader) swarmAnswer(row *bench.Row, artifactPath string) (verdict, text string) {
	text = queenAnswerText(r.rationale("queen"))
	r.readLensAnswers(row)
	a, err := loadArtifact(artifactPath)
	if err != nil {
		return "", text
	}
	verdict, _ = a.Synthesis["verdict"].(string)
	for _, name := range a.Foragers {
		r.countLens(row, name, a)
	}
	return verdict, text
}

// readLensAnswers fills the row's lens answers and their diversity from
// the run's node rows.
func (r benchRunReader) readLensAnswers(row *bench.Row) {
	lenses, err := workflow.RunLensAnswers(r.store.Workflows(), r.runID)
	if err != nil {
		row.Error = strings.TrimSpace(row.Error + " lens answers: " + err.Error())
		return
	}
	row.Diversity = workflow.DiversityOf(lenses).State()
	row.LensAnswers = map[string]bench.LensAnswer{}
	for name, l := range lenses {
		row.LensAnswers[name] = bench.LensAnswer{Verdict: strings.ToLower(strings.TrimSpace(l.Verdict)), Model: l.Model}
	}
}

// countLens counts one lens of the artifact: its output, whether it held
// its contract, and whether Queen could read its full verdict. Queen reads
// a lens's full verdict from its node's outputs in this run, and only a
// line saying why there is none when the node did not complete with a
// verdict.
func (r benchRunReader) countLens(row *bench.Row, name string, a *artifact.Artifact) {
	row.Lenses++
	row.Outputs++
	v, _ := a.Verdicts[name].(map[string]any)
	if lensContractHeld(name, v) {
		row.SchemaValid++
	}
	if _, outs := r.rationale("forager-" + name); benchOutputsHaveVerdict(outs) {
		row.FullVerdicts++
	}
}

// lensContractHeld: the lens's verdict held its contract. The direct voice
// has no persona file: its contract is the solo control's.
func lensContractHeld(name string, v map[string]any) bool {
	if name == foragers.DirectVoiceName {
		return soloContractHeld(v)
	}
	contract, err := loadPersonaContract(name)
	return err == nil && contractHeld(checkVerdictContract(name, v, contract))
}

// benchOutputsHaveVerdict: a node's stored outputs decode to an object with a
// verdict.
func benchOutputsHaveVerdict(outs string) bool {
	if outs == "" {
		return false
	}
	var o map[string]any
	if json.Unmarshal([]byte(outs), &o) != nil {
		return false
	}
	v, _ := o["verdict"].(string)
	return v != ""
}

// soloAnswer reads a solo run's answer from its one node: its text, and
// from its outputs its verdict and whether it held its contract.
func (r benchRunReader) soloAnswer(row *bench.Row) (verdict, text string) {
	text, outputs := r.rationale("solo")
	row.Outputs = 1
	var o map[string]any
	if json.Unmarshal([]byte(outputs), &o) == nil {
		verdict, _ = o["verdict"].(string)
		if soloContractHeld(o) {
			row.SchemaValid = 1
		}
	}
	return verdict, text
}

// soloContractHeld: the solo control's and the direct voice's contract held,
// its required keys present and its verdict in the enum.
func soloContractHeld(o map[string]any) bool {
	verdict, _ := o["verdict"].(string)
	_, hasKP := o["key_points"]
	_, hasRec := o["recommendation"]
	return hasKP && hasRec && slices.Contains([]string{"support", "oppose", "conditional", "abstain"}, verdict)
}

// finishLength is how many of a run's model calls the provider stopped at the
// output cap: the sum of its nodes' cutoff_calls, which count every dispatch,
// fan item, tool turn, finalize call and repair attempt. On the Claude CLI
// they count its final reply only, the one stop reason it reports. It is nil
// when a node ran on the Gemini CLI, which reports none.
func finishLength(store *db.Store, runID int64) (*int, error) {
	var cut, blind int
	if err := store.ReadDB.QueryRow(`SELECT COALESCE(SUM(cutoff_calls), 0),
		COALESCE(SUM(CASE WHEN provider = 'gemini-cli' THEN 1 ELSE 0 END), 0)
		FROM workflow_node_states WHERE run_id = ?`, runID).Scan(&cut, &blind); err != nil {
		return nil, err
	}
	if blind > 0 {
		return nil, nil
	}
	return &cut, nil
}

// queenAnswerText is the text a swarm run's tokens are graded on: Queen's
// own text, then her stored outputs decoded, her report first and then
// every other field by name. Tokens are a set, so a token counts whether it
// sits in prose around her object or inside it, where her JSON may have
// written ⇄ as a \u escape. A node without an outputs object, or with only
// the final_text fallback, is graded on its text alone.
func queenAnswerText(text, outputsJSON string) string {
	o, ok := queenOutputs(outputsJSON)
	if !ok {
		return text
	}
	parts := []string{text}
	if r, ok := o["report"].(string); ok {
		parts = append(parts, r)
	}
	for _, k := range queenFieldsBut(o, "report") {
		parts = append(parts, answerFieldText(o[k]))
	}
	return strings.Join(parts, "\n")
}

// queenOutputs decodes Queen's stored outputs; false when they hold no
// object, or only the final_text fallback.
func queenOutputs(outputsJSON string) (map[string]any, bool) {
	var o map[string]any
	if json.Unmarshal([]byte(outputsJSON), &o) != nil || len(o) == 0 {
		return nil, false
	}
	return o, !onlyFinalText(o)
}

// onlyFinalText: the outputs hold the final_text fallback and nothing else.
func onlyFinalText(o map[string]any) bool {
	_, fallback := o["final_text"]
	return fallback && len(o) == 1
}

// queenFieldsBut is the outputs' field names other than skip, sorted.
func queenFieldsBut(o map[string]any, skip string) []string {
	keys := make([]string, 0, len(o))
	for k := range o {
		if k != skip {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// answerFieldText writes a decoded field as text: a string as it is, a list
// one item per line, anything else as JSON, which leaves non-ASCII as is.
func answerFieldText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		lines := make([]string, 0, len(x))
		for _, it := range x {
			lines = append(lines, answerFieldText(it))
		}
		return strings.Join(lines, "\n")
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// contractHeld: every contract (non-adherence) check passed.
func contractHeld(checks []harnessCheck) bool {
	for _, c := range checks {
		if !c.OK && !c.Warn {
			return false
		}
	}
	return len(checks) > 0
}

// soloWorkflow is the solo control: one node, no persona, the lens
// contract's verdict, key points and recommendation over the same question
// and pack, the prompt and schema the swarm's direct voice shares
// (foragers.DirectPrompt). It runs on the lens model, or on the lenses'
// tier when none is pinned, at the lenses' reasoning level when one is set.
func soloWorkflow(model, reasoning string) string {
	pin := "tier: synthesist"
	if model != "" {
		pin = "model: " + strconv.Quote(model)
	}
	if reasoning != "" {
		pin += "\n    reasoning: " + reasoning
	}
	prompt := "      " + strings.ReplaceAll(foragers.DirectPrompt, "\n", "\n      ")
	return `name: bench-solo
description: The bench solo control — one call, no persona, the same question and roster pack as the swarm, held to the lens's terms — no tools, its output schema enforced, one repair.
version: 1
inputs: [question, context]
nodes:
  solo:
    type: agent
    role: lens
    ` + pin + `
    prompt: |
` + prompt + `
    tools: []
    output_schema: ` + foragers.DirectSchemaJSON + `
    on_reject: {max_repair_iterations: 1}
    outputs: [verdict, key_points, recommendation]
    accept:
      - "outputs.verdict == 'support' or outputs.verdict == 'oppose' or outputs.verdict == 'conditional' or outputs.verdict == 'abstain'"
`
}

// benchSummary is a bench case's report: each arm's and each family's
// scores, the lift, and each arm's nodes' mean cost.
func benchSummary(rows []bench.Row, items []bench.Item, arms []string, resultsPath string) *benchReport {
	r := &benchReport{Items: len(items), Pairs: len(items) / 2, Arms: map[string]bench.Summary{},
		Families: map[string]map[string]bench.Summary{}, Results: resultsPath}
	byArm, byFam := groupBenchRows(rows)
	for _, a := range arms {
		r.Arms[a] = bench.Summarize(byArm[a])
	}
	for fam, m := range byFam {
		r.Families[fam] = summarizeBenchArms(m)
	}
	r.Lift = benchLift(r.Arms)
	r.Nodes = benchNodeMeans(rows)
	return r
}

// groupBenchRows files the rows by arm, and by family then arm.
func groupBenchRows(rows []bench.Row) (map[string][]bench.Row, map[string]map[string][]bench.Row) {
	byArm := map[string][]bench.Row{}
	byFam := map[string]map[string][]bench.Row{}
	for _, row := range rows {
		byArm[row.Arm] = append(byArm[row.Arm], row)
		if byFam[row.Family] == nil {
			byFam[row.Family] = map[string][]bench.Row{}
		}
		byFam[row.Family][row.Arm] = append(byFam[row.Family][row.Arm], row)
	}
	return byArm, byFam
}

// summarizeBenchArms scores each arm's rows.
func summarizeBenchArms(byArm map[string][]bench.Row) map[string]bench.Summary {
	out := map[string]bench.Summary{}
	for a, rs := range byArm {
		out[a] = bench.Summarize(rs)
	}
	return out
}

// benchLift is the swarm's class-balanced accuracy minus the solo
// control's, nil unless both arms ran.
func benchLift(arms map[string]bench.Summary) *float64 {
	sw, okSw := benchArmRan(arms, bench.ArmSwarm)
	so, okSo := benchArmRan(arms, bench.ArmSolo)
	if !okSw || !okSo {
		return nil
	}
	v := sw.ClassBalanced - so.ClassBalanced
	return &v
}

// benchArmRan is the arm's summary, and whether it has runs.
func benchArmRan(arms map[string]bench.Summary, arm string) (bench.Summary, bool) {
	s, ok := arms[arm]
	return s, ok && s.Runs > 0
}

// benchNodeKey is one node of one arm.
type benchNodeKey struct{ arm, node string }

// benchNodeMeans is each arm's nodes' mean cost over their runs, arms in
// reverse order, then nodes by name.
func benchNodeMeans(rows []bench.Row) []benchNode {
	var out []benchNode
	for _, b := range benchNodeSums(rows) {
		out = append(out, b.mean())
	}
	sort.Slice(out, func(i, j int) bool { return benchNodeBefore(out[i], out[j]) })
	return out
}

// benchNodeSums sums each arm's nodes' runs, tokens and wall time.
func benchNodeSums(rows []bench.Row) map[benchNodeKey]*benchNode {
	nodes := map[benchNodeKey]*benchNode{}
	for _, row := range rows {
		for _, n := range row.Nodes {
			addBenchNode(nodes, row.Arm, n)
		}
	}
	return nodes
}

// addBenchNode adds one run of a node on arm to its sums.
func addBenchNode(nodes map[benchNodeKey]*benchNode, arm string, n bench.NodeStat) {
	k := benchNodeKey{arm, n.Node}
	if nodes[k] == nil {
		nodes[k] = &benchNode{Arm: arm, Node: n.Node}
	}
	b := nodes[k]
	b.Runs++
	b.TokensIn += float64(n.TokensIn)
	b.TokensOut += float64(n.TokensOut)
	b.WallSeconds += n.WallSeconds
}

// mean is the node's sums divided by its runs.
func (b benchNode) mean() benchNode {
	b.TokensIn /= float64(b.Runs)
	b.TokensOut /= float64(b.Runs)
	b.WallSeconds /= float64(b.Runs)
	return b
}

// benchNodeBefore orders nodes by arm, reversed, then by name.
func benchNodeBefore(a, b benchNode) bool {
	if a.Arm != b.Arm {
		return a.Arm > b.Arm
	}
	return a.Node < b.Node
}

// coErrorsText writes a correlated-error rate with its counts and the rate
// the same pairs would have if the lenses erred independently, or why
// there is none. scope narrows "pairs of lenses", as " on different
// models" does.
func coErrorsText(c bench.CoErrors, scope string) string {
	switch {
	case c.Compared == 0:
		return "none: no pair of lenses" + scope + " both returned a verdict"
	case c.Rate == nil:
		return fmt.Sprintf("none: no error in the %d pairs of lenses%s compared", c.Compared, scope)
	}
	return fmt.Sprintf("%d of %d pairs%s with an error erred together = %.2f (%s if each lens erred independently at its own rate), of %d compared",
		c.Together, c.Pairs, scope, *c.Rate, fmtPtr(c.Independent), c.Compared)
}

// diversityText counts swarm runs by lens diversity state.
func diversityText(counts map[string]int, runs int) string {
	var parts []string
	for _, s := range []string{workflow.DiversityLow, workflow.DiversityNotLow, workflow.DiversityUnknown, workflow.DiversityNotChecked, "not_recorded"} {
		if n := counts[s]; n > 0 || s != "not_recorded" {
			parts = append(parts, fmt.Sprintf("%s %d", s, n))
		}
	}
	return fmt.Sprintf("%s, of %d", strings.Join(parts, " · "), runs)
}

// writeBenchReport writes a bench case's section of the report: each arm's
// scores, its classes, the lift, the swarm's correlated lens errors, the
// pairs passed by family, and each node's mean cost.
func writeBenchReport(md *strings.Builder, r *benchReport) {
	fmt.Fprintf(md, "%d twin items in %d pairs; one row per run in `%s`, for `chb bench decide`.\n\n", r.Items, r.Pairs, r.Results)
	arms := slices.Sorted(maps.Keys(r.Arms))
	slices.Reverse(arms)
	writeBenchArmTable(md, r.Arms, arms)
	writeBenchClassLines(md, r, arms)
	writeBenchLensErrors(md, r.Arms)
	writeBenchFamilyTable(md, r.Families, arms)
	writeBenchNodeTable(md, r.Nodes)
}

// writeBenchArmTable writes one line of scores and cost per arm.
func writeBenchArmTable(md *strings.Builder, summaries map[string]bench.Summary, arms []string) {
	md.WriteString("| Arm | Runs | Completed | Accuracy | Class-balanced | Pairs passed | Token F1 | Abstain fabricated | Tokens in | Tokens out | Wall s |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, a := range arms {
		s := summaries[a]
		fmt.Fprintf(md, "| %s | %d | %d | %.2f | %.2f | %d/%d = %.2f | %.2f | %d/%d | %d | %d | %.0f |\n",
			a, s.Runs, s.Completed, s.Accuracy, s.ClassBalanced, s.PairsPassed, s.Pairs, s.PairScore, s.TokenF1,
			s.Fabricated, s.Classes[bench.Abstain].N, s.TokensIn, s.TokensOut, s.WallSeconds)
	}
	md.WriteString("\n")
}

// writeBenchClassLines writes each arm's accuracy by expected verdict, and
// the lift when there is one.
func writeBenchClassLines(md *strings.Builder, r *benchReport, arms []string) {
	for _, a := range arms {
		fmt.Fprintf(md, "- %s by expected verdict: %s\n", a, benchClassText(r.Arms[a]))
	}
	if r.Lift != nil {
		fmt.Fprintf(md, "- HIVE lift (swarm − solo, class-balanced): %+.2f\n", *r.Lift)
	}
}

// benchClassText writes the correct answers of each expected verdict
// present.
func benchClassText(s bench.Summary) string {
	var parts []string
	for _, c := range []string{bench.Support, bench.Oppose, bench.Abstain} {
		if cs, ok := s.Classes[c]; ok {
			parts = append(parts, fmt.Sprintf("%s %d/%d", c, cs.Correct, cs.N))
		}
	}
	return strings.Join(parts, " · ")
}

// writeBenchLensErrors writes the swarm arm's correlated lens errors, its
// lens verdicts by model and its runs by lens diversity, when it ran.
func writeBenchLensErrors(md *strings.Builder, summaries map[string]bench.Summary) {
	s, ok := summaries[bench.ArmSwarm]
	if !ok || s.Runs == 0 {
		return
	}
	e := s.LensErrors
	fmt.Fprintf(md, "- correlated lens errors, over the %d of %d swarm runs with two or more lens verdicts: %s\n", e.Runs, s.Runs, coErrorsText(e.All, ""))
	writeBenchModelLines(md, e)
	fmt.Fprintf(md, "- swarm runs by lens diversity (low: one model, one verdict): %s; %d of the low runs wrong\n", diversityText(s.Diversity, s.Runs), s.DiversityLowWrong)
}

// writeBenchModelLines writes the correlated lens errors across models,
// when the lenses ran on more than one, and each model's lens verdicts.
func writeBenchModelLines(md *strings.Builder, e bench.LensErrors) {
	if len(e.Models) > 1 {
		fmt.Fprintf(md, "- correlated lens errors across models: %s\n", coErrorsText(e.CrossModel, " on different models"))
	}
	if len(e.Models) > 0 {
		fmt.Fprintf(md, "- lens runs that returned a verdict, by model: %s\n", benchModelVerdictsText(e.Models))
	}
}

// benchModelVerdictsText writes each model's lens runs that returned a
// verdict.
func benchModelVerdictsText(models []bench.ModelVerdicts) string {
	var parts []string
	for _, m := range models {
		parts = append(parts, fmt.Sprintf("%s %d/%d", m.Model, m.Verdicts, m.Lenses))
	}
	return strings.Join(parts, " · ")
}

// writeBenchFamilyTable writes each family's pairs passed on each arm.
func writeBenchFamilyTable(md *strings.Builder, families map[string]map[string]bench.Summary, arms []string) {
	md.WriteString("\n| Family | " + strings.Join(arms, " pairs | ") + " pairs |\n|---|" + strings.Repeat("---|", len(arms)) + "\n")
	for _, f := range slices.Sorted(maps.Keys(families)) {
		md.WriteString("| " + f + " |")
		for _, a := range arms {
			s := families[f][a]
			fmt.Fprintf(md, " %d/%d |", s.PairsPassed, s.Pairs)
		}
		md.WriteString("\n")
	}
}

// writeBenchNodeTable writes each node's mean cost on each arm.
func writeBenchNodeTable(md *strings.Builder, nodes []benchNode) {
	md.WriteString("\n| Arm | Node | Runs | Mean tokens in | Mean tokens out | Mean wall s |\n|---|---|---|---|---|---|\n")
	for _, n := range nodes {
		fmt.Fprintf(md, "| %s | %s | %d | %.0f | %.0f | %.1f |\n", n.Arm, n.Node, n.Runs, n.TokensIn, n.TokensOut, n.WallSeconds)
	}
	md.WriteString("\n")
}
