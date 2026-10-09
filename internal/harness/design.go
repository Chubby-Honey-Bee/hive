package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/bench"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/design"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

// The design case (docs/specs/bench-design.md): each task runs through
// the designer arm (chb ask over the task, then the plan call with the
// swarm's research) and the control arm (the plan call alone), a plain
// executor carries each plan out in a fresh copy of the tree without the
// statement, the research or the hidden tests, and the hidden tests grade
// what it built. One row per task and arm goes to results.jsonl, with the
// bench case's outage honesty: a run that reached no model, or after which
// the endpoint fails the preflight, stops the case, and the rows are then
// marked as not a measurement.

const (
	defaultTasksRoot       = "fixtures/design"
	defaultExecutorMinutes = 10
	hiddenTestMinutes      = 3
)

// designReport is what a design case measured.
type designReport struct {
	Tasks    int               `json:"tasks"`
	Arms     []design.ArmMeans `json:"arms"`
	Decision design.Decision   `json:"decision"`
	Rows     []design.Row      `json:"-"`
	Results  string            `json:"results"`
}

// runDesignCase runs every task on every arm until an outage stops the
// case, writes one row per task and arm to <ws>/results.jsonl, and returns
// the case's report; nil when it could not run or stopped.
func (h *agentHarness) runDesignCase(ctx context.Context, c harnessCase, ws string, check, warn func(string, bool, string)) *designReport {
	tasks, arms, ok := planDesignCase(c, check)
	if !ok {
		return nil
	}
	d := &designCase{h: h, ws: ws, tasks: tasks, arms: arms, preset: c.preset(), minutes: c.executorMinutes()}
	if !d.prepare(ctx, check) {
		return nil
	}
	resultsPath := d.run(ctx, check, warn)
	if !h.finishSweep(&d.sweep, check) {
		return nil
	}
	return &designReport{Tasks: len(tasks), Arms: design.Summarize(d.rows), Decision: design.Decide(d.rows, design.DefaultAlpha), Rows: d.rows, Results: resultsPath}
}

// planDesignCase checks that go is on PATH, for the hidden tests, loads the
// case's tasks and checks its arms; false when any of it fails.
func planDesignCase(c harnessCase, check func(string, bool, string)) ([]design.Task, []string, bool) {
	if _, err := exec.LookPath("go"); err != nil {
		check("go is on PATH (the hidden tests run with go test)", false, err.Error())
		return nil, nil, false
	}
	tasks, err := design.LoadTasks(c.tasksRoot(), c.TaskNames)
	check("the tasks load", err == nil, designLoadDetail(tasks, err))
	if err != nil {
		return nil, nil, false
	}
	arms := c.designArms()
	if a, unknown := harnessUnknownArm(arms, design.ArmDesigner, design.ArmControl); unknown {
		check("arms are designer or control", false, "unknown arm "+a)
		return nil, nil, false
	}
	return tasks, arms, true
}

// tasksRoot is the design case's tasks directory, fixtures/design by
// default.
func (c harnessCase) tasksRoot() string {
	if c.Tasks == "" {
		return defaultTasksRoot
	}
	return c.Tasks
}

// designArms is the design case's arms, designer and control by default.
func (c harnessCase) designArms() []string {
	if len(c.Arms) == 0 {
		return []string{design.ArmDesigner, design.ArmControl}
	}
	return c.Arms
}

// executorMinutes is how long the design case's executor may run.
func (c harnessCase) executorMinutes() int {
	if c.ExecutorMinutes <= 0 {
		return defaultExecutorMinutes
	}
	return c.ExecutorMinutes
}

// designCase is one design case's run: its tasks and arms, the preset, the
// executor's minutes, and the rows and sweep of its runs.
type designCase struct {
	h       *agentHarness
	ws      string
	tasks   []design.Task
	arms    []string
	preset  string
	minutes int
	rows    []design.Row
	sweep   harnessSweep
}

// prepare writes the workflows the runs will dispatch, asks the endpoint
// whether it serves every model they send, and clears the workspace;
// false when any of it fails.
func (d *designCase) prepare(ctx context.Context, check func(string, bool, string)) bool {
	cfg, defns, err := d.h.designWorkflows(ctx, d.tasks[0], d.arms, d.preset)
	check("the workflows the runs dispatch are written (chb ask --no-dispatch, the plan call, the executor, --profile)", err == nil, errString(err))
	if err != nil {
		return false
	}
	d.sweep = newHarnessSweep(len(d.tasks)*len(d.arms), cfg, defns, "no result")
	return d.h.preflightEndpoint(ctx, cfg, defns, check) && resetHarnessWorkspace(d.ws, check)
}

// run runs every task on every arm until an outage stops the case, writes
// the rows and returns their path, comparing the checkout's tracked files
// before and after.
func (d *designCase) run(ctx context.Context, check, warn func(string, bool, string)) string {
	before, beforeErr := dirtyTrackedFiles()
	d.runAll(ctx)
	path := d.record(check)
	warnCheckoutChanged(before, beforeErr, warn)
	return path
}

// runAll runs the tasks and arms in order until every run is done or one
// stops the case.
func (d *designCase) runAll(ctx context.Context) {
	for _, task := range d.tasks {
		for _, arm := range d.arms {
			if !d.runOne(ctx, task, arm) {
				return
			}
		}
	}
}

// runOne runs the task once on arm, logs it, and counts it; false when it
// stopped the case. As the bench: a stage that reached no model, or an
// endpoint that now fails the preflight it passed before the first run, is
// an outage. Anything else that did not complete is data.
func (d *designCase) runOne(ctx context.Context, task design.Task, arm string) bool {
	row, end := d.h.runDesignTask(ctx, task, arm, filepath.Join(d.ws, task.Name, arm), d.preset, d.minutes)
	d.rows = append(d.rows, row)
	d.sweep.count(row.Completed, row.TreeOK, task.Name+"/"+arm)
	d.h.logf("  %s %-14s %-8s tests %d/%d  plan names %d/%d  steps %d dev %d add %d  audit %v  %.0fs", designMark(row), task.Name, arm,
		row.TestsPassed, row.TestsTotal, row.RefNamed, row.RefFiles, row.Steps, row.Deviations, row.Additions, row.AuditPass, row.WallSeconds)
	if row.Completed {
		return true
	}
	return !d.sweep.stopsAt(ctx, end.run, end.failed, row.Error, fmt.Sprintf("%s %s, %s stage", task.Name, arm, end.stage))
}

// record checks that the case did not stop, writes results.jsonl — marked
// as not a measurement, which chb design report refuses, when it stopped
// or no run completed — and checks every designer run's tree. It returns
// the results' path.
func (d *designCase) record(check func(string, bool, string)) string {
	path := filepath.Join(d.ws, "results.jsonl")
	notMeasured := d.sweep.checkMeasured(check)
	err := writeHarnessResults(path, designRowsWriter(d.rows, notMeasured))
	checkHarnessResults(check, err, notMeasured, "chb design report")
	check("every designer run left its tree unchanged", len(d.sweep.changed) == 0, strings.Join(d.sweep.changed, ", "))
	return path
}

// designRowsWriter writes the rows, under a not_measured line when
// notMeasured says why they are not a measurement.
func designRowsWriter(rows []design.Row, notMeasured string) func(io.Writer) error {
	if notMeasured != "" {
		return func(w io.Writer) error { return design.WriteNotMeasured(w, notMeasured, rows) }
	}
	return func(w io.Writer) error { return design.WriteRows(w, rows) }
}

// designLoadDetail says how many tasks and twin pairs loaded, or why none
// did.
func designLoadDetail(tasks []design.Task, err error) string {
	if err != nil {
		return err.Error()
	}
	twins := 0
	for _, t := range tasks {
		if t.Twin != "" {
			twins++
		}
	}
	return fmt.Sprintf("%d tasks, %d twin pairs", len(tasks), twins/2)
}

// designMark is a run's sign: ✗ when it did not complete, ✓ when every
// hidden test passed, else ~.
func designMark(r design.Row) string {
	switch {
	case !r.Completed:
		return "✗"
	case r.TestsPassed == r.TestsTotal:
		return "✓"
	}
	return "~"
}

// designEnd is how a task run ended: the stage that failed (swarm, plan,
// execute or grade; "" when none), whether that stage left a run row, and
// the error of its first failed or rejected node.
type designEnd struct {
	stage  string
	run    bool
	failed string
}

// runDesignTask runs one task on one arm in a fresh temporary directory and
// moves it to dest afterwards. The designer arm runs the swarm over the pack
// with the tree as its working directory, then the plan call with the
// research; the control arm runs the plan call alone. The executor then
// carries the plan out in a second fresh copy of the tree, and the hidden
// tests grade a copy of that.
func (h *agentHarness) runDesignTask(ctx context.Context, task design.Task, arm, dest, preset string, minutes int) (row design.Row, end designEnd) {
	row = h.newDesignRow(task, arm)
	end.run = true
	dir, err := os.MkdirTemp("", "chb-design-")
	if err != nil {
		row.Error = err.Error()
		return row, end
	}
	defer func() {
		if err := moveDir(dir, dest); err != nil {
			row.Error = strings.TrimSpace(row.Error + " move the run into the workspace: " + err.Error())
		}
	}()
	t := designTaskRun{h: h, task: task, arm: arm, dir: dir, preset: preset, minutes: minutes, row: &row}
	if f := t.run(ctx); f != nil {
		end = t.fail(f)
		return row, end
	}
	row.Completed = true
	return row, end
}

// newDesignRow is the row of one run of task on arm, with the
// configuration and the models it runs on: under a profile, its routes.
func (h *agentHarness) newDesignRow(task design.Task, arm string) design.Row {
	row := design.Row{Config: h.configName(), Provider: h.Provider, QueenModel: h.QueenModel, Task: task.Name, Family: task.Family, Twin: task.Twin, Arm: arm, TreeOK: true}
	row.LensModel = strings.Join(h.lensModels, ",")
	if arm == design.ArmControl {
		row.LensModel = h.soloModel()
	}
	if h.Profile != "" {
		row.LensModel, row.QueenModel = h.profileLens.Model, h.profileQueen.Model
	}
	return row
}

// designAskArgs are the chb ask arguments of the designer arm's swarm: the
// design question over the pack as --context-file, the lenses reading the
// tree with --lens-tools read unless the profile's lens route sets tools.
func (h *agentHarness) designAskArgs(dir, packPath, preset string) []string {
	return append([]string{"--db", filepath.Join(dir, "swarm.db"), "ask", designQuestion, "--context-file", packPath, "--foragers", preset,
		"--budget-mode", h.BudgetMode, "--artifact", filepath.Join(dir, "artifact.json"),
		"--out", filepath.Join(dir, "swarm.yaml")}, h.swarmRunArgs(false, h.designLensTools(), h.lensModels, 1)...)
}

// designLensTools is read, so the designer's lenses read the tree, unless
// the profile's lens route sets the tools.
func (h *agentHarness) designLensTools() string {
	if h.Profile == "" || h.profileLens.Tools == nil {
		return "read"
	}
	return ""
}

// designQuestion is what the swarm is asked over each task's pack.
const designQuestion = "The context holds a coding task and the repository it applies to. What design should an executor implement for it, and what must a correct design decide? The executor will see neither the task nor this discussion, only a design document and a plan written from them, so name the public names and signatures the task fixes, the behaviours and edge cases a correct solution must cover, and the choices that decide between a passing and a failing solution."

// researchText is the swarm's research for the plan call: Queen's report,
// verdict and recommendation from the artifact.
func researchText(synthesis map[string]any) string {
	report, _ := synthesis["report"].(string)
	verdict, _ := synthesis["verdict"].(string)
	rec, _ := synthesis["recommendation"].(string)
	var parts []string
	if strings.TrimSpace(report) != "" {
		parts = append(parts, strings.TrimSpace(report))
	}
	if verdict != "" || rec != "" {
		parts = append(parts, strings.TrimSpace(fmt.Sprintf("Verdict: %s. Recommendation: %s", verdict, rec)))
	}
	return strings.Join(parts, "\n\n")
}

// designWorkflows writes the workflows a design case's runs dispatch, as
// their agent-run routes them: the swarm's from chb ask --no-dispatch over
// the first task's pack, both plan workflows and the executor's.
func (h *agentHarness) designWorkflows(ctx context.Context, task design.Task, arms []string, preset string) (runner.Config, []map[string]any, error) {
	cfg, profile, err := h.harnessRouting()
	if err != nil {
		return cfg, nil, err
	}
	texts, err := h.designWorkflowTexts(ctx, task, arms, preset)
	if err != nil {
		return cfg, nil, err
	}
	defns, err := routeHarnessWorkflows(texts, profile)
	return cfg, defns, err
}

// designWorkflowTexts writes, in a scratch directory, the executor's
// workflow and each arm's.
func (h *agentHarness) designWorkflowTexts(ctx context.Context, task design.Task, arms []string, preset string) ([]string, error) {
	dir, err := os.MkdirTemp("", "chb-design-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	texts := []string{executorWorkflow(h.soloModel(), h.LensReasoning)}
	for _, a := range arms {
		armTexts, err := h.designArmWorkflows(ctx, dir, a, task, preset)
		if err != nil {
			return nil, err
		}
		texts = append(texts, armTexts...)
	}
	return texts, nil
}

// designArmWorkflows is one arm's workflows: its plan call, and on the
// designer arm the swarm's.
func (h *agentHarness) designArmWorkflows(ctx context.Context, dir, arm string, task design.Task, preset string) ([]string, error) {
	plan := planWorkflow(h.soloModel(), h.LensReasoning, arm == design.ArmDesigner)
	if arm != design.ArmDesigner {
		return []string{plan}, nil
	}
	swarm, err := h.designNoDispatch(ctx, dir, task, preset)
	if err != nil {
		return nil, err
	}
	return []string{plan, swarm}, nil
}

// designNoDispatch writes the designer's swarm workflow as chb ask
// --no-dispatch writes it over the task's pack, in dir, and returns it.
func (h *agentHarness) designNoDispatch(ctx context.Context, dir string, task design.Task, preset string) (string, error) {
	pack, err := design.Pack(task)
	if err != nil {
		return "", err
	}
	packPath := filepath.Join(dir, "pack.md")
	if err := os.WriteFile(packPath, []byte(pack), 0o644); err != nil {
		return "", err
	}
	args := append(h.designAskArgs(dir, packPath, preset), "--no-dispatch")
	if out, err := h.exec(ctx, dir, "ask", h.caseEnv(filepath.Join(dir, "swarm.db")), "", h.Self, args...); err != nil {
		return "", fmt.Errorf("chb ask --no-dispatch: %s", tail(out))
	}
	raw, err := os.ReadFile(filepath.Join(dir, "swarm.yaml"))
	return string(raw), err
}

// stageRun is what one stage's database says: whether a run row exists,
// the first failed or rejected node's error, every node's stats, and each
// node's decoded outputs.
type stageRun struct {
	run     bool
	failed  string
	nodes   []bench.NodeStat
	outputs map[string]map[string]any
}

// readStage reads a stage's database, best effort: what it holds is data
// about the stage, and a database that is missing or unreadable says the
// stage left no run.
func readStage(dbPath string) stageRun {
	st := stageRun{outputs: map[string]map[string]any{}}
	if _, err := os.Stat(dbPath); err != nil {
		return st
	}
	store, err := db.NewStore(dbPath)
	if err != nil {
		return st
	}
	defer store.Close()
	run, _ := harnessLastRun(store)
	if run == nil {
		return st
	}
	st.run = true
	st.failed = harnessFailedNodeError(store, run.ID)
	st.nodes, _ = nodeStats(store, run.ID)
	st.outputs, _ = stageNodeOutputs(store, run.ID)
	return st
}

// stageNodeOutputs decodes the outputs of each node of the run that
// completed with an object; with an error it returns those read before it.
func stageNodeOutputs(store *db.Store, runID int64) (map[string]map[string]any, error) {
	outputs := map[string]map[string]any{}
	rows, err := store.ReadDB.Query(`SELECT node_name, COALESCE(outputs_json, '') FROM workflow_node_states WHERE run_id = ? AND status = 'completed'`, runID)
	if err != nil {
		return outputs, err
	}
	defer rows.Close()
	for rows.Next() {
		addStageNodeOutputs(rows, outputs)
	}
	return outputs, rows.Err()
}

// addStageNodeOutputs decodes one node row's outputs into outputs, when the
// row scans and its outputs hold an object.
func addStageNodeOutputs(rows *sql.Rows, outputs map[string]map[string]any) {
	var name, outs string
	if rows.Scan(&name, &outs) != nil {
		return
	}
	var o map[string]any
	if json.Unmarshal([]byte(outs), &o) == nil && len(o) > 0 {
		outputs[name] = o
	}
}

// planPrompt is the plan call's prompt on both arms; the designer's adds
// researchBlock after it.
const planPrompt = `You are writing a design document and an execution plan for a coding task. A separate executor will carry your plan out on the repository below with a shell and file tools. The executor will see neither this task statement nor any research: only your design document and your plan. Hidden tests will then grade what it built. So the design must state every public name, signature and behaviour the executor needs, and every decision a correct solution rests on.

## Task and repository

{pack}
`

const researchBlock = `
## Research

A swarm of specialist lenses read this task and the repository. Their synthesis:

{research}
`

const planReturn = `
## What to return

One JSON object with three fields. "design": the design document in Markdown, with the decisions, the public API with exact signatures, the behaviours and the edge cases. "claims": every load-bearing claim of the design, each an object with "label" (one of definition, guarantee, assumption, unknown), "claim" (the text) and "rests_on" (the indexes, counted from 0, of the earlier claims it follows from; a guarantee rests on at least one definition or assumption and never on an unknown; a definition, assumption or unknown may rest on nothing). "plan": the steps in the order the executor should take them, each an object with "step" (its number from 1), "action" (what to do, specific enough to do without the task statement) and "files" (the paths it touches).`

// planWorkflow is the plan call: one node, role implement-plan, no tools,
// the document schema enforced, one repair. withResearch adds the research
// block to the prompt, and nothing else differs between the arms.
func planWorkflow(model, reasoning string, withResearch bool) string {
	prompt := planPrompt
	if withResearch {
		prompt += researchBlock
	}
	prompt += planReturn
	return `name: design-plan
description: The design bench's plan call — the design document, its labelled claims and the execution plan, from the task pack (and on the designer arm the swarm's research).
version: 1
inputs: [pack, research]
nodes:
  plan:
    type: agent
    role: implement-plan
    ` + pinModel(model, reasoning) + `
    prompt: |
` + indentBlock(prompt, "      ") + `
    tools: []
    output_schema: ` + design.DocumentSchemaJSON + `
    on_reject: {max_repair_iterations: 1}
    outputs: [design, claims, plan]
`
}

// executorPrompt is what the executor is given with the design and the plan.
const executorPrompt = `Carry out this plan on the repository in your working directory. You have a shell and file tools. Take the steps in order. When a step cannot be done as written, do what the design needs instead and record it as a deviation. Record anything you did that no step asked for as an addition. Run go build ./... and go vet ./... before you finish, and fix what they report.

## Design

{design}

## Plan

{plan}

End your answer with one JSON object: "steps_done" (the numbers of the steps you completed), "deviations" (one object per step you did differently or skipped, with "step" and "why"), "additions" (what you did that no step asked for) and "summary" (one paragraph).`

// executorWorkflow is the executor: one node under the plain coder persona,
// role implement-fix, with the shell and the file tools, the report schema
// enforced, one repair, and at least one tool call.
func executorWorkflow(model, reasoning string) string {
	return `name: design-execute
description: The design bench's executor — the plain coder persona carrying out a plan it did not write, in a fresh copy of the tree, with the shell and file tools.
version: 1
inputs: [design, plan]
nodes:
  execute:
    type: agent
    agent: coder
    role: implement-fix
    ` + pinModel(model, reasoning) + `
    prompt: |
` + indentBlock(executorPrompt, "      ") + `
    tools: [shell, read_file, write_file, edit_file, glob, grep]
    min_tool_calls: 1
    output_schema: ` + design.ReportSchemaJSON + `
    on_reject: {max_repair_iterations: 1}
    outputs: [steps_done, deviations, additions, summary]
`
}

// pinModel pins a node to model at reasoning, or to tier worker.
func pinModel(model, reasoning string) string {
	pin := "tier: worker"
	if model != "" {
		pin = "model: " + strconv.Quote(model)
	}
	if reasoning != "" {
		pin += "\n    reasoning: " + reasoning
	}
	return pin
}

// indentBlock indents every line of text, trailing newlines dropped.
func indentBlock(text, indent string) string {
	return indent + strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n"+indent)
}

// writeDesignReport writes a design case's section of the report.
func writeDesignReport(md *strings.Builder, r *designReport) {
	fmt.Fprintf(md, "%d tasks, one row per task and arm in `%s`, for `chb design report`.\n\n", r.Tasks, r.Results)
	WriteDesignTables(md, r.Rows, r.Arms, r.Decision)
}

// WriteDesignTables writes the per-task table, the per-arm means and the
// decision, for the harness report and chb design report alike.
func WriteDesignTables(md *strings.Builder, rows []design.Row, arms []design.ArmMeans, d design.Decision) {
	md.WriteString("| Task | Arm | Tests | Plan names ref. files | Steps | Deviations | Additions | File coverage | Claims | Guarantees w/ premises | Audit | Done | Wall s | Tokens in | Tokens out |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(md, "| %s | %s | %d/%d | %d/%d | %d | %d | %d | %s | %d | %d/%d | %s | %s | %.0f | %d | %d |\n",
			r.Task, r.Arm, r.TestsPassed, r.TestsTotal, r.RefNamed, r.RefFiles, r.Steps, r.Deviations, r.Additions, fmtPtr(r.Coverage.Rate),
			r.Claims, r.GuaranteesWithPremises, r.Guarantees, yesNo(r.AuditPass), designDone(r), r.WallSeconds, r.TokensIn, r.TokensOut)
	}
	md.WriteString("\n| Arm | Rows | Completed | Pass rate | Plan completeness | Deviation rate | File coverage | Audit passed | Guarantees w/ premises | Mean wall s | Tokens in | Tokens out |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, m := range arms {
		fmt.Fprintf(md, "| %s | %d | %d | %.2f | %s | %s | %s | %d | %.2f | %.0f | %d | %d |\n",
			m.Arm, m.Rows, m.Completed, m.PassRate, fmtPtr(m.PlanCompleteness), fmtPtr(m.DeviationRate), fmtPtr(m.FileCoverage), m.AuditPassed, m.GuaranteesHonest, m.WallSeconds, m.TokensIn, m.TokensOut)
	}
	fmt.Fprintf(md, "\n**Verdict: %s** (alpha %.2f; token cost designer ÷ control = %s, executors included)\n", d.Verdict, d.Alpha, fmtRatio(d.CostRatio))
	for _, reason := range d.Reasons {
		fmt.Fprintf(md, "- %s\n", reason)
	}
	md.WriteString("\n")
}

// fmtRatio writes a ratio, or a dash when there is none.
func fmtRatio(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f×", *v)
}

// yesNo writes b as yes or no.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// designDone says how far a run got.
func designDone(r design.Row) string {
	switch {
	case r.Completed:
		return "yes"
	case r.Executed:
		return "executed, not graded"
	case r.Designed:
		return "designed only"
	}
	return "no design"
}
