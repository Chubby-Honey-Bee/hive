package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/design"
)

// One run of a design task on one arm, stage by stage: the pack, the
// design (on the designer arm the swarm's research, then the plan call),
// the executor, and the hidden tests.

// designTaskRun is one run of a task on an arm, in its own directory, and
// the row it fills.
type designTaskRun struct {
	h       *agentHarness
	task    design.Task
	arm     string
	dir     string
	preset  string
	minutes int
	row     *design.Row
}

// designFailure is how a task run that failed at a stage ended, and why.
type designFailure struct {
	end designEnd
	msg string
}

// designStageFailed is a failure at stage, which left st behind.
func designStageFailed(stage string, st stageRun, msg string) *designFailure {
	return &designFailure{end: designEnd{stage: stage, run: st.run, failed: st.failed}, msg: msg}
}

// designSetupFailed is a failure while setting a stage up: the stage counts
// as having reached a model, so it is data, not an outage.
func designSetupFailed(stage, msg string) *designFailure {
	return designStageFailed(stage, stageRun{run: true}, msg)
}

// fail records why the run failed and its wall time, and says how it
// ended.
func (t *designTaskRun) fail(f *designFailure) designEnd {
	t.row.Error = strings.TrimSpace(t.row.Error + " " + f.msg)
	t.row.WallSeconds = t.row.DesignSeconds + t.row.ExecSeconds
	return f.end
}

// run takes the task through its stages: the pack, the design, the
// executor and the hidden tests. It returns how the first stage that
// failed did.
func (t *designTaskRun) run(ctx context.Context) *designFailure {
	pack, f := t.packStage()
	if f != nil {
		return f
	}
	doc, f := t.designStage(ctx, pack)
	if f != nil {
		return f
	}
	est, execErr, f := t.executeStage(ctx, doc)
	if f != nil {
		return f
	}
	return t.gradeStage(ctx, est, execErr)
}

// designPack is what both arms read: the pack's text and path, and the
// files the reference changes.
type designPack struct {
	text, path string
	refs       []string
}

// packStage writes the pack, counts the hidden tests into the row, and
// lists the files the reference changes: plan completeness's denominator,
// from the fixture alone.
func (t *designTaskRun) packStage() (designPack, *designFailure) {
	text, path, err := t.writePack()
	if err != nil {
		return designPack{}, designSetupFailed("pack", err.Error())
	}
	refs, err := t.readFixture()
	if err != nil {
		return designPack{}, designSetupFailed("pack", err.Error())
	}
	return designPack{text: text, path: path, refs: refs}, nil
}

// writePack writes the task's pack into the run's directory.
func (t *designTaskRun) writePack() (text, path string, err error) {
	text, err = design.Pack(t.task)
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(t.dir, "pack.md")
	return text, path, os.WriteFile(path, []byte(text), 0o644)
}

// readFixture counts the hidden tests into the row, and lists the files
// the reference changes.
func (t *designTaskRun) readFixture() ([]string, error) {
	var err error
	if t.row.TestsTotal, err = design.CountHiddenTests(t.task.Hidden()); err != nil {
		return nil, err
	}
	return design.ReferenceChanges(t.task)
}

// designStage writes the design, timed as DesignSeconds: on the designer
// arm the swarm's research first, then the plan call.
func (t *designTaskRun) designStage(ctx context.Context, pack designPack) (design.Document, *designFailure) {
	start := time.Now()
	research, f := t.swarmStage(ctx, pack.path, start)
	if f != nil {
		return design.Document{}, f
	}
	return t.planStage(ctx, pack, research, start)
}

// swarmStage runs the designer arm's swarm over the pack and returns
// Queen's research; the control arm has none.
func (t *designTaskRun) swarmStage(ctx context.Context, packPath string, start time.Time) (string, *designFailure) {
	if t.arm != design.ArmDesigner {
		return "", nil
	}
	sw, f := t.prepareSwarm()
	if f != nil {
		return "", f
	}
	research, f := t.askSwarm(ctx, sw, packPath)
	if f != nil {
		t.row.DesignSeconds = time.Since(start).Seconds()
	}
	return research, f
}

// designSwarmTree is the designer swarm's working tree, its manifest from
// before the run, and the environment the run gets.
type designSwarmTree struct {
	tree     string
	manifest map[string]string
	merr     error
	env      []string
}

// prepareSwarm copies the task's tree and the personas into the run's
// directory for the swarm.
func (t *designTaskRun) prepareSwarm() (designSwarmTree, *designFailure) {
	tree := filepath.Join(t.dir, "tree")
	if err := design.CopyTree(t.task, tree); err != nil {
		return designSwarmTree{}, designSetupFailed("swarm", err.Error())
	}
	manifest, merr := treeManifest(tree)
	foragersDir := filepath.Join(t.dir, "foragers")
	if err := os.CopyFS(foragersDir, t.h.Foragers().FS); err != nil {
		return designSwarmTree{}, designSetupFailed("swarm", "copy foragers: "+err.Error())
	}
	env := append(t.h.caseEnv(filepath.Join(t.dir, "swarm.db")), "HIVE_FORAGERS_DIR="+foragersDir)
	return designSwarmTree{tree: tree, manifest: manifest, merr: merr, env: env}, nil
}

// askSwarm runs the swarm with the tree as its working directory, records
// whether it changed the tree and its nodes, and returns Queen's research
// from its artifact.
func (t *designTaskRun) askSwarm(ctx context.Context, sw designSwarmTree, packPath string) (string, *designFailure) {
	out, runErr := t.h.exec(ctx, t.dir, "ask", sw.env, sw.tree, t.h.Self, t.h.designAskArgs(t.dir, packPath, t.preset)...)
	t.noteTreeChange(sw)
	st := readStage(filepath.Join(t.dir, "swarm.db"))
	t.addNodes("swarm/", st)
	if runErr != nil {
		return "", designStageFailed("swarm", st, tail(out))
	}
	a, err := loadArtifact(filepath.Join(t.dir, "artifact.json"))
	if err != nil {
		return "", designStageFailed("swarm", st, "artifact: "+err.Error())
	}
	return researchText(a.Synthesis), nil
}

// noteTreeChange records whether the swarm left its tree unchanged, and
// what changed.
func (t *designTaskRun) noteTreeChange(sw designSwarmTree) {
	var changes string
	t.row.TreeOK, changes = treeCheck(sw.tree, sw.manifest, sw.merr)
	if !t.row.TreeOK {
		t.row.Error = strings.TrimSpace(t.row.Error + " tree changed: " + changes)
	}
}

// addNodes adds a stage's nodes to the row, named under prefix, and their
// tokens to its totals.
func (t *designTaskRun) addNodes(prefix string, st stageRun) {
	for _, n := range st.nodes {
		n.Node = prefix + n.Node
		t.row.Nodes = append(t.row.Nodes, n)
		t.row.TokensIn += n.TokensIn
		t.row.TokensOut += n.TokensOut
	}
}

// planStage runs the plan call — the same prompt, model, schema and repair
// on both arms; the designer's carries the research — and reads its
// document.
func (t *designTaskRun) planStage(ctx context.Context, pack designPack, research string, start time.Time) (design.Document, *designFailure) {
	wf, planDir, f := t.writePlanWorkflow()
	if f != nil {
		return design.Document{}, f
	}
	st, out, runErr := t.callPlan(ctx, wf, planDir, pack.text, research)
	t.row.DesignSeconds = time.Since(start).Seconds()
	if runErr != nil {
		return design.Document{}, designStageFailed("plan", st, tail(out))
	}
	return t.readPlan(st, pack.refs)
}

// writePlanWorkflow makes the plan call's directory and writes its
// workflow.
func (t *designTaskRun) writePlanWorkflow() (wf, planDir string, f *designFailure) {
	planDir = filepath.Join(t.dir, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		return "", "", designSetupFailed("plan", err.Error())
	}
	wf = filepath.Join(t.dir, "plan.yaml")
	if err := os.WriteFile(wf, []byte(planWorkflow(t.h.soloModel(), t.h.LensReasoning, t.arm == design.ArmDesigner)), 0o644); err != nil {
		return "", "", designSetupFailed("plan", err.Error())
	}
	return wf, planDir, nil
}

// callPlan runs the plan call over the pack and the research, records its
// nodes, and returns what its database says, its output and its error.
func (t *designTaskRun) callPlan(ctx context.Context, wf, planDir, pack, research string) (stageRun, string, error) {
	planDB := filepath.Join(t.dir, "plan.db")
	inputs, _ := json.Marshal(map[string]string{"pack": pack, "research": research})
	out, runErr := t.h.exec(ctx, t.dir, "plan", t.h.caseEnv(planDB), planDir, t.h.Self, t.h.agentRunArgs(planDB, wf, planDir, inputs, 1)...)
	st := readStage(planDB)
	t.addNodes("", st)
	return st, out, runErr
}

// readPlan reads the plan call's document into the row: the claims'
// labels, plan completeness against refs, and the steps; the deviation
// rate waits for the executor's report.
func (t *designTaskRun) readPlan(st stageRun, refs []string) (design.Document, *designFailure) {
	doc, err := design.ParseDocument(st.outputs["plan"])
	if err != nil {
		return doc, designStageFailed("plan", st, "the plan call's outputs: "+err.Error())
	}
	t.row.Designed = true
	if js, err := json.MarshalIndent(doc, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(t.dir, "design.json"), js, 0o644)
	}
	t.row.Labels = design.CheckLabels(doc.Claims, filepath.Join(t.dir, "audit.db"))
	t.row.Completeness = design.PlanCompleteness(doc.Plan, refs)
	t.row.Steps = len(doc.Plan)
	return doc, nil
}

// execTree is where the executor works: a fresh copy of the task's tree.
func (t *designTaskRun) execTree() string { return filepath.Join(t.dir, "exec") }

// executeStage has the executor carry the plan out, bounded in wall time,
// and records the files it changed against the plan and its report. It
// returns what the executor's database says, and why it did not finish,
// "" when it did; that waits until the hidden tests have graded what it
// left.
func (t *designTaskRun) executeStage(ctx context.Context, doc design.Document) (stageRun, string, *designFailure) {
	ewf, f := t.prepareExecutor()
	if f != nil {
		return stageRun{}, "", f
	}
	before, berr := treeManifest(t.execTree())
	est, execErr := t.runExecutor(ctx, ewf, doc)
	t.noteCoverage(before, berr, doc.Plan)
	if execErr == "" {
		execErr = t.readExecutorReport(est, doc.Plan)
	}
	return est, execErr, nil
}

// prepareExecutor copies the task's tree for the executor and writes the
// executor's workflow.
func (t *designTaskRun) prepareExecutor() (string, *designFailure) {
	if err := design.CopyTree(t.task, t.execTree()); err != nil {
		return "", designSetupFailed("execute", err.Error())
	}
	ewf := filepath.Join(t.dir, "execute.yaml")
	if err := os.WriteFile(ewf, []byte(executorWorkflow(t.h.soloModel(), t.h.LensReasoning)), 0o644); err != nil {
		return "", designSetupFailed("execute", err.Error())
	}
	return ewf, nil
}

// runExecutor runs the executor on the design and the plan, stopped after
// the case's minutes, and records its time and nodes. It returns what its
// database says, and why it did not finish, "" when it did.
func (t *designTaskRun) runExecutor(ctx context.Context, ewf string, doc design.Document) (stageRun, string) {
	execDB := filepath.Join(t.dir, "exec.db")
	planJSON, _ := json.Marshal(doc.Plan)
	inputs, _ := json.Marshal(map[string]string{"design": doc.Design, "plan": string(planJSON)})
	args := t.h.agentRunArgs(execDB, ewf, t.execTree(), inputs, 1)
	execStart := time.Now()
	ectx, cancel := context.WithTimeout(ctx, time.Duration(t.minutes)*time.Minute)
	out, runErr := t.h.exec(ectx, t.dir, "execute", t.h.caseEnv(execDB), t.execTree(), t.h.Self, args...)
	timedOut := errors.Is(ectx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	t.row.ExecSeconds = time.Since(execStart).Seconds()
	est := readStage(execDB)
	t.addNodes("", est)
	return est, t.executorError(timedOut, runErr, out)
}

// executorError says why the executor did not finish: it ran out of time,
// or did not exit 0; "" when it did.
func (t *designTaskRun) executorError(timedOut bool, runErr error, out string) string {
	switch {
	case timedOut:
		return fmt.Sprintf("the executor ran past %d minutes and was stopped", t.minutes)
	case runErr != nil:
		return tail(out)
	}
	return ""
}

// noteCoverage records the files the executor changed against those the
// plan names, when the tree could be read before and after.
func (t *designTaskRun) noteCoverage(before map[string]string, berr error, plan []design.Step) {
	after, aerr := treeManifest(t.execTree())
	if berr != nil || aerr != nil {
		return
	}
	var paths []string
	for _, ch := range treeChanges(before, after) {
		paths = append(paths, ch[1:])
	}
	t.row.Coverage = design.FileCoverage(plan, paths)
}

// readExecutorReport reads the executor's report into the row's fidelity,
// or says that its outputs hold none.
func (t *designTaskRun) readExecutorReport(est stageRun, plan []design.Step) string {
	outputs := est.outputs["execute"]
	report, err := design.ParseReport(outputs)
	if err != nil || outputs == nil {
		return "the executor's outputs hold no report"
	}
	t.row.Executed = true
	t.row.Fidelity = design.PlanFidelity(plan, report)
	return ""
}

// gradeStage runs the hidden tests in a copy of what the executor left,
// and then reports the executor's own failure, execErr, if it had one.
func (t *designTaskRun) gradeStage(ctx context.Context, est stageRun, execErr string) *designFailure {
	graded := filepath.Join(t.dir, "graded")
	if err := os.CopyFS(graded, os.DirFS(t.execTree())); err != nil {
		return designStageFailed("grade", est, err.Error())
	}
	if err := t.runHiddenTests(ctx, graded); err != nil {
		return designStageFailed("grade", est, err.Error())
	}
	if execErr != "" {
		return designStageFailed("execute", est, execErr)
	}
	return nil
}

// runHiddenTests grades the copy, bounded in wall time, and records the
// result, its log and the run's wall time.
func (t *designTaskRun) runHiddenTests(ctx context.Context, graded string) error {
	gctx, gcancel := context.WithTimeout(ctx, hiddenTestMinutes*time.Minute)
	grade, gerr := design.RunHiddenTests(gctx, graded, t.task.Hidden())
	gcancel()
	_ = os.WriteFile(filepath.Join(t.dir, "go-test.log"), []byte(grade.Output), 0o644)
	t.row.TestsPassed, t.row.TestsTotal, t.row.Built, t.row.Failed, t.row.PassRate = grade.Passed, grade.Total, grade.Built, grade.Failed, grade.Rate()
	t.row.WallSeconds = t.row.DesignSeconds + t.row.ExecSeconds
	return gerr
}
