package runner

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

type nodeRow struct {
	tokensIn, tokensOut, cost int64
	provider, rationale       string
}

func readNodeRow(t *testing.T, store *db.Store, runID int64, node string) nodeRow {
	t.Helper()
	var r nodeRow
	err := store.WriteDB.QueryRow(
		`SELECT COALESCE(tokens_in,0), COALESCE(tokens_out,0), COALESCE(cost_usd_x10000,0),
		        COALESCE(provider,''), COALESCE(rationale,'')
		 FROM workflow_node_states WHERE run_id=? AND node_name=?`, runID, node,
	).Scan(&r.tokensIn, &r.tokensOut, &r.cost, &r.provider, &r.rationale)
	if err != nil {
		t.Fatalf("read node row %s: %v", node, err)
	}
	return r
}

func usageResult(text string, in, out int64, err error) func(RunRequest) (*RunResult, error) {
	return func(RunRequest) (*RunResult, error) {
		return &RunResult{FinalText: text, Turns: 1, InputTokens: in, OutputTokens: out}, err
	}
}

// A node that fails accept: with no repair spent its dispatch all the same:
// the row and the run's cost total must carry it, or --max-cost-usd cannot
// see it.
func TestExecuteAgentNode_RejectedNodeSpendIsRecorded(t *testing.T) {
	const nodeName, model = "w1-rej", "claude-sonnet-4-6"
	const in, out = int64(1_000_000), int64(10)
	yamlStr := fmt.Sprintf(`
name: rejected-spend
nodes:
  %s:
    type: agent
    model: %s
    prompt: do something
    outputs: [ok]
    accept:
      - "outputs.ok == true"
`, nodeName, model)
	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		usageResult(`{"ok": false}`, in, out, nil),
	}}
	cfg := Config{Provider: "anthropic"}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)

	rc.executeAgentNode(workflow.DispatchNode{Node: nodeName, Type: "agent", Model: model})

	wantCost := ComputeCostUSDx10000(model, in, out)
	if wantCost == 0 {
		t.Fatalf("precondition: %s has no price, so the test cannot tell cost from none", model)
	}
	got := readNodeRow(t, store, runID, nodeName)
	if got.tokensIn != in || got.tokensOut != out || got.cost != wantCost {
		t.Errorf("row tokens=%d/%d cost=%d; want %d/%d cost=%d", got.tokensIn, got.tokensOut, got.cost, in, out, wantCost)
	}
	if want := providerLabel(rc.cfg); got.provider != want {
		t.Errorf("row provider=%q; want %q", got.provider, want)
	}
	if rc.res.CostUSDx10000 != wantCost {
		t.Errorf("run cost=%d; want %d", rc.res.CostUSDx10000, wantCost)
	}
}

// A backend that reports usage together with an error (a CLI hitting its
// turn cap) still spent it; the failed node's row and the run carry it.
func TestExecuteAgentNode_FailedNodeSpendIsRecorded(t *testing.T) {
	const nodeName, model = "w1-fail", "claude-sonnet-4-6"
	const in, out = int64(40_000), int64(2_000)
	yamlStr := fmt.Sprintf(`
name: failed-spend
nodes:
  %s:
    type: agent
    model: %s
    prompt: do something
`, nodeName, model)
	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		usageResult("", in, out, fmt.Errorf("max turns reached")),
	}}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, Config{Provider: "anthropic"})

	rc.executeAgentNode(workflow.DispatchNode{Node: nodeName, Type: "agent", Model: model})

	wantCost := ComputeCostUSDx10000(model, in, out)
	got := readNodeRow(t, store, runID, nodeName)
	if got.tokensIn != in || got.tokensOut != out || got.cost != wantCost {
		t.Errorf("row tokens=%d/%d cost=%d; want %d/%d cost=%d", got.tokensIn, got.tokensOut, got.cost, in, out, wantCost)
	}
	if rc.res.CostUSDx10000 != wantCost {
		t.Errorf("run cost=%d; want %d", rc.res.CostUSDx10000, wantCost)
	}
}

// Repairs that run out: the row carries the dispatch and every attempt,
// labelled with the provider (not the model), and every request carries
// the run's temperature and seed.
func TestExecuteAgentNode_ExhaustedRepairSpendProviderAndDeterminism(t *testing.T) {
	const nodeName, model = "w1-exhaust", "claude-sonnet-4-6"
	calls := []struct{ in, out int64 }{{100, 10}, {200, 20}, {400, 40}}
	yamlStr := fmt.Sprintf(`
name: exhausted-repair
nodes:
  %s:
    type: agent
    model: %s
    prompt: do something
    outputs: [ok]
    accept:
      - "outputs.ok == true"
    on_reject:
      max_repair_iterations: %d
`, nodeName, model, len(calls)-1)
	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	var mu sync.Mutex
	var reqs []RunRequest
	var fns []func(RunRequest) (*RunResult, error)
	for _, c := range calls {
		c := c
		fns = append(fns, func(req RunRequest) (*RunResult, error) {
			mu.Lock()
			reqs = append(reqs, req)
			mu.Unlock()
			return &RunResult{FinalText: `{"ok": false}`, Turns: 1, InputTokens: c.in, OutputTokens: c.out}, nil
		})
	}
	seed := int64(7)
	cfg := Config{Provider: "anthropic", Deterministic: true, Seed: &seed}
	rc := buildAgentNodeRC(t, store, runID, defn, &callableBackend{fns: fns}, cfg)

	rc.executeAgentNode(workflow.DispatchNode{Node: nodeName, Type: "agent", Model: model})

	var wantIn, wantOut, wantCost int64
	for _, c := range calls {
		wantIn += c.in
		wantOut += c.out
		// The repair block names no model, so repairs run on the node's own.
		wantCost += ComputeCostUSDx10000(model, c.in, c.out)
	}
	got := readNodeRow(t, store, runID, nodeName)
	if got.tokensIn != wantIn || got.tokensOut != wantOut || got.cost != wantCost {
		t.Errorf("row tokens=%d/%d cost=%d; want %d/%d cost=%d", got.tokensIn, got.tokensOut, got.cost, wantIn, wantOut, wantCost)
	}
	if want := providerLabel(rc.cfg); got.provider != want {
		t.Errorf("row provider=%q; want %q", got.provider, want)
	}
	if rc.res.CostUSDx10000 != wantCost {
		t.Errorf("run cost=%d; want %d", rc.res.CostUSDx10000, wantCost)
	}
	if len(reqs) != len(calls) {
		t.Fatalf("backend calls=%d; want %d", len(reqs), len(calls))
	}
	for i, r := range reqs {
		if r.Temperature == nil || *r.Temperature != 0 || r.Seed == nil || *r.Seed != seed {
			t.Errorf("request %d: temperature=%v seed=%v; want 0 and %d", i, r.Temperature, r.Seed, seed)
		}
	}
}

// A repair attempt whose backend returns usage with an error spent it: the
// node row and the run get it, as the workflow_repairs row already did.
func TestExecuteAgentNode_RepairBackendErrorWithUsageIsCharged(t *testing.T) {
	const nodeName, model = "w1-err-usage", "claude-sonnet-4-6"
	dispatch := struct{ in, out int64 }{100, 10}
	repair := struct{ in, out int64 }{500, 50}
	yamlStr := fmt.Sprintf(`
name: repair-err-usage
nodes:
  %s:
    type: agent
    model: %s
    prompt: fix
    outputs: [ok]
    accept:
      - "outputs.ok == true"
    on_reject:
      max_repair_iterations: 1
`, nodeName, model)
	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		usageResult(`{"ok": false}`, dispatch.in, dispatch.out, nil),
		usageResult("", repair.in, repair.out, fmt.Errorf("max turns reached")),
	}}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, Config{Provider: "anthropic"})

	rc.executeAgentNode(workflow.DispatchNode{Node: nodeName, Type: "agent", Model: model})

	wantIn, wantOut := dispatch.in+repair.in, dispatch.out+repair.out
	wantCost := ComputeCostUSDx10000(model, dispatch.in, dispatch.out) + ComputeCostUSDx10000(model, repair.in, repair.out)
	got := readNodeRow(t, store, runID, nodeName)
	if got.tokensIn != wantIn || got.tokensOut != wantOut || got.cost != wantCost {
		t.Errorf("row tokens=%d/%d cost=%d; want %d/%d cost=%d", got.tokensIn, got.tokensOut, got.cost, wantIn, wantOut, wantCost)
	}
	if rc.res.InputTokens != wantIn || rc.res.OutputTokens != wantOut || rc.res.CostUSDx10000 != wantCost {
		t.Errorf("run tokens=%d/%d cost=%d; want %d/%d cost=%d",
			rc.res.InputTokens, rc.res.OutputTokens, rc.res.CostUSDx10000, wantIn, wantOut, wantCost)
	}
}

// A repaired node's rationale is the text that passed accept:, not the
// rejected dispatch's.
func TestExecuteAgentNode_RepairedRationaleIsAcceptedText(t *testing.T) {
	const nodeName = "w1-repaired"
	const first, repaired = `FIRST ATTEMPT {"ok": false}`, `SECOND ATTEMPT {"ok": true}`
	yamlStr := fmt.Sprintf(`
name: repaired-rationale
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
    outputs: [ok]
    accept:
      - "outputs.ok == true"
    on_reject:
      max_repair_iterations: 1
`, nodeName)
	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(first),
		successResult(repaired),
	}}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, Config{})

	rc.executeAgentNode(workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "haiku"})

	if got := readNodeRow(t, store, runID, nodeName).rationale; got != repaired {
		t.Errorf("rationale=%q; want the accepted attempt's text %q", got, repaired)
	}
}

// fixedByPrompt answers each request by the first map key its prompt holds.
type fixedByPrompt struct {
	mu    sync.Mutex
	by    map[string]*RunResult
	calls map[string]int
}

func (b *fixedByPrompt) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, r := range b.by {
		if strings.Contains(req.Prompt, k) {
			b.calls[k]++
			cp := *r
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("no canned result for %q", req.Prompt)
}

// The cost cap counts a rejected node's spend: a $1 cap after a rejected
// node spent more than $1 dispatches nothing further.
func TestRun_CostCapSeesRejectedSpend(t *testing.T) {
	const model = "claude-sonnet-4-6"
	const rejIn = int64(1_000_000)
	wf := fmt.Sprintf(`
name: cap-sees-rejected
nodes:
  w1-a:
    type: agent
    model: %[1]s
    prompt: "NODE-A"
    outputs: [ok]
    accept:
      - "outputs.ok == true"
  w1-b:
    type: agent
    model: %[1]s
    prompt: "NODE-B"
  w2-c:
    type: agent
    model: %[1]s
    prompt: "NODE-C"
edges:
  - {from: w1-b, to: w2-c}
`, model)
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	mustWriteFile(t, path, wf)
	capX10000 := int64(10_000)
	if c := ComputeCostUSDx10000(model, rejIn, 1); c < capX10000 {
		t.Fatalf("precondition: rejected spend %d must reach the cap %d", c, capX10000)
	}
	backend := &fixedByPrompt{calls: map[string]int{}, by: map[string]*RunResult{
		"NODE-A": {FinalText: `{"ok": false}`, InputTokens: rejIn, OutputTokens: 1},
		"NODE-B": {FinalText: `{"b": 1}`, InputTokens: 10, OutputTokens: 1},
		"NODE-C": {FinalText: `{"c": 1}`, InputTokens: 10, OutputTokens: 1},
	}}
	store := newTempStore(t)
	_, err := Run(context.Background(), store, Config{
		WorkflowYAML: path, ProjectDir: dir, Backend: backend,
		MaxCostUSDx10000: capX10000, Log: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := backend.calls["NODE-C"]; n != 0 {
		t.Errorf("node C dispatched %d times after the cap was reached", n)
	}
}

func writeWorkflow(t *testing.T, body string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "wf.yaml")
	mustWriteFile(t, path, body)
	return dir, path
}

// A run whose last wave lands on the last iteration is complete, not cut
// short.
func TestRun_MaxIterationsOnSettledRunSucceeds(t *testing.T) {
	dir, path := writeWorkflow(t, `
name: one-node
nodes:
  w1-a:
    type: agent
    model: haiku
    prompt: go
`)
	store := newTempStore(t)
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML: path, ProjectDir: dir, Backend: &stubBackend{},
		MaxIterations: 1, Log: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	run, err := store.Workflows().GetWorkflowRun(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Errorf("run status=%q; want completed", run.Status)
	}
}

// With work left, the cap is still an error that names --resume.
func TestRun_MaxIterationsWithWorkLeftErrors(t *testing.T) {
	dir, path := writeWorkflow(t, `
name: two-nodes
nodes:
  w1-a:
    type: agent
    model: haiku
    prompt: go
  w2-b:
    type: agent
    model: haiku
    prompt: then
edges:
  - {from: w1-a, to: w2-b}
`)
	store := newTempStore(t)
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML: path, ProjectDir: dir, Backend: &stubBackend{},
		MaxIterations: 1, Log: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("--resume %d", res.RunID)) {
		t.Fatalf("err=%v; want the MaxIterations cap naming --resume %d", err, res.RunID)
	}
}

// A human_review pause succeeds and names both commands that continue the
// run; it is not an --only-wave stop.
func TestRun_HumanReviewPauseNamesResumeCommands(t *testing.T) {
	dir, path := writeWorkflow(t, `
name: pause
nodes:
  w1-a:
    type: agent
    model: haiku
    prompt: go
  w2-review:
    type: human_review
  w3-c:
    type: agent
    model: haiku
    prompt: after
edges:
  - {from: w1-a, to: w2-review}
  - {from: w2-review, to: w3-c}
`)
	store := newTempStore(t)
	var log bytes.Buffer
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML: path, ProjectDir: dir, Backend: &stubBackend{}, Log: &log,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := log.String()
	for _, want := range []string{
		fmt.Sprintf("chb workflow resume %d", res.RunID),
		fmt.Sprintf("--resume %d", res.RunID),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "OnlyWave") {
		t.Errorf("log reports an --only-wave stop that was never asked for:\n%s", out)
	}
}

// --dry-run --auto-pr walks the graph without the committer a dry run lacks,
// and does not panic.
func TestRun_DryRunAutoPRDoesNotPanic(t *testing.T) {
	dir, path := writeWorkflow(t, `
name: dry
nodes:
  w1-a:
    type: agent
    model: haiku
    prompt: go
`)
	store := newTempStore(t)
	if _, err := Run(context.Background(), store, Config{
		WorkflowYAML: path, ProjectDir: dir, Backend: &stubBackend{},
		DryRun: true, AutoPR: true, Branch: "x", Log: &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A dirty-tree refusal happens before the run exists, so nothing is left
// behind in status running.
func TestRun_DirtyTreeRefusalWritesNoRun(t *testing.T) {
	dir := initCleanRepo(t)
	mustWriteFile(t, filepath.Join(dir, "wf.yaml"), `
name: dirty
nodes:
  w1-a:
    type: agent
    model: haiku
    prompt: go
`)
	store := newTempStore(t)
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){successResult("ok")}}
	_, err := Run(context.Background(), store, Config{
		WorkflowYAML: filepath.Join(dir, "wf.yaml"), ProjectDir: dir, Backend: backend,
		Branch: "auto", Log: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "--allow-dirty") {
		t.Fatalf("err=%v; want the dirty-tree refusal", err)
	}
	var runs int
	if err := store.WriteDB.QueryRow(`SELECT COUNT(*) FROM workflow_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Errorf("workflow_runs=%d after the refusal; want 0", runs)
	}
	if backend.calls != 0 {
		t.Errorf("backend called %d times", backend.calls)
	}
}

// A run that cannot start fails before the auto-commit branch is created
// or checked out, so the checkout stays on the branch the user was on.
func TestRun_UnstartableRunLeavesCheckoutAlone(t *testing.T) {
	for _, tc := range []struct {
		name, wantErr string
		resume        int64
	}{
		{name: "missing workflow", wantErr: "init workflow"},
		{name: "run not resumable", wantErr: "resume run 99", resume: 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := initCleanRepo(t)
			repo := &GitCommitter{ProjectDir: dir}
			before, err := repo.run("rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				t.Fatalf("read HEAD: %v %s", err, before)
			}
			_, err = Run(context.Background(), newTempStore(t), Config{
				WorkflowYAML: filepath.Join(dir, "missing.yaml"), ResumeRunID: tc.resume,
				ProjectDir: dir, Branch: "x", Backend: &stubBackend{}, Log: &bytes.Buffer{},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v; want one naming %q", err, tc.wantErr)
			}
			if after, _ := repo.run("rev-parse", "--abbrev-ref", "HEAD"); after != before {
				t.Errorf("HEAD moved from %q to %q", strings.TrimSpace(before), strings.TrimSpace(after))
			}
			if out, _ := repo.run("branch", "--list", "x"); strings.TrimSpace(out) != "" {
				t.Errorf("branch x was created: %q", out)
			}
		})
	}
}

// A project in a subdirectory of a repository is inside a git working
// tree: auto-commit stays on and the branch is created.
func TestPrepareGitCommitter_SubdirOfRepo(t *testing.T) {
	root := initCleanRepo(t)
	sub := filepath.Join(root, "svc")
	mustWriteFile(t, filepath.Join(sub, "keep.txt"), "k\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "svc")

	var log bytes.Buffer
	gc, err := prepareGitCommitter(Config{ProjectDir: sub, Branch: "auto-sub"}, makeLogf(&log))
	if err != nil {
		t.Fatalf("prepareGitCommitter: %v", err)
	}
	enterBranch(gc, makeLogf(&log))
	if !gc.Enabled {
		t.Fatalf("auto-commit disabled for a subdirectory of a repo; log: %s", log.String())
	}
	out, err := gc.run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || strings.TrimSpace(out) != "auto-sub" {
		t.Errorf("HEAD=%q err=%v; want branch auto-sub", out, err)
	}
}

// maybeOpenPR is reached with no committer under --dry-run.
func TestMaybeOpenPR_NilCommitter(t *testing.T) {
	maybeOpenPR(Config{AutoPR: true}, nil, &Result{Commits: []string{"abc"}}, func(string, ...any) {})
}
