package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func nextNames(t *testing.T, repo *db.WorkflowsRepo, runID int64) []string {
	t.Helper()
	nodes, err := GetNextNodes(repo, runID)
	if err != nil {
		t.Fatalf("GetNextNodes: %v", err)
	}
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Node)
	}
	sort.Strings(names)
	return names
}

func expectNext(t *testing.T, repo *db.WorkflowsRepo, runID int64, want ...string) {
	t.Helper()
	if got := nextNames(t, repo, runID); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("next = %v, want %v", got, want)
	}
}

func complete(t *testing.T, repo *db.WorkflowsRepo, runID int64, node string, outputs map[string]any) {
	t.Helper()
	if err := CompleteNode(repo, runID, node, outputs); err != nil {
		t.Fatalf("complete %s: %v", node, err)
	}
}

func runStatus(t *testing.T, repo *db.WorkflowsRepo, runID int64) string {
	t.Helper()
	run, err := repo.GetWorkflowRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	return run.Status
}

func initYAML(t *testing.T, repo *db.WorkflowsRepo, yaml string, inputs map[string]any) int64 {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wf.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	runID, err := InitWorkflow(repo, path, inputs)
	if err != nil {
		t.Fatal(err)
	}
	return runID
}

// A validated retry loop runs its body again: the head does not count the
// decision closing the loop as a predecessor, since that decision cannot
// finish before the head runs.
func TestDecisionLoop_RunsTheBodyAgain(t *testing.T) {
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, decisionWorkflowYAML, map[string]any{"question": "q"})

	expectNext(t, repo, runID, "plan")
	complete(t, repo, runID, "plan", map[string]any{})
	expectNext(t, repo, runID, "research")
	complete(t, repo, runID, "research", map[string]any{"score": 50})
	expectNext(t, repo, runID, "research")
	complete(t, repo, runID, "research", map[string]any{"score": 90})
	expectNext(t, repo, runID, "finalize")
	complete(t, repo, runID, "finalize", map[string]any{"report": "done"})
	expectNext(t, repo, runID)
	if s := runStatus(t, repo, runID); s != "completed" {
		t.Fatalf("run status = %q, want completed", s)
	}
}

// The shipped autonomous hive workflow runs past `init` into its passes.
func TestHiveWorkflow_Iterates(t *testing.T) {
	yaml, err := os.ReadFile(filepath.Join("..", "..", "workflows", "hive.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const iterations = 2
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, string(yaml), map[string]any{"project": "p", "max_iterations": iterations})

	expectNext(t, repo, runID, "init")
	complete(t, repo, runID, "init", map[string]any{"iteration": 0.0})
	for iteration := 1; iteration <= iterations; iteration++ {
		// The first pass's plan holds a research action and a gate, so it
		// runs dispatch and the gate's nodes; the second's holds neither and
		// goes straight to merge.
		research := []any{}
		gate := iteration == 1
		if iteration == 1 {
			research = append(research, map[string]any{"type": "dispatch_agent", "payload": map[string]any{"gap_id": 1.0}})
		}
		expectNext(t, repo, runID, "scan")
		complete(t, repo, runID, "scan", map[string]any{"phase": "dispatching", "research_actions": research,
			"gate_requested": gate, "gate_wave": 1.0, "iteration": float64(iteration)})
		if len(research) > 0 {
			expectNext(t, repo, runID, "dispatch")
			complete(t, repo, runID, "dispatch", map[string]any{"dispatch_results": "ok", "findings_written": 0.0, "gaps_resolved": 0.0})
		}
		if gate {
			for _, node := range []struct {
				name    string
				outputs map[string]any
			}{
				{"validate-sources", map[string]any{}},
				{"gate-state", map[string]any{"findings_total": 1.0, "findings": []any{}, "wave_sources": []any{}, "open_gaps": []any{}}},
				{"evaluate", map[string]any{"gaps": []any{}, "coverage": 4.0, "depth": 4.0, "sources": 4.0, "actionability": 4.0}},
				{"gate", map[string]any{"opened": false, "verdict": "COMPLETE"}},
			} {
				expectNext(t, repo, runID, node.name)
				complete(t, repo, runID, node.name, node.outputs)
			}
		}
		expectNext(t, repo, runID, "merge")
		complete(t, repo, runID, "merge", map[string]any{})
		expectNext(t, repo, runID, "detect-conflicts")
		complete(t, repo, runID, "detect-conflicts", map[string]any{})
		expectNext(t, repo, runID, "complete-iteration")
		complete(t, repo, runID, "complete-iteration", map[string]any{"should_continue": iteration < iterations, "stop_reason": ""})
	}
	// After the loop, each runs once, in a chain.
	for _, node := range []struct {
		name    string
		outputs map[string]any
	}{
		{"report", map[string]any{"phase": "scanning", "iteration": float64(iterations), "stop_reason": "cap", "passes_this_run": float64(iterations),
			"summary": map[string]any{}, "findings_total": 0.0, "findings": []any{}, "open_gaps": []any{}, "open_conflicts": []any{}}},
		{"final-synthesis", map[string]any{"report_markdown": "# done", "open_questions": []any{}}},
		{"write-synthesis", map[string]any{"path": "workspace/p/final-synthesis.md"}},
		{"export-graph", map[string]any{}},
	} {
		expectNext(t, repo, runID, node.name)
		complete(t, repo, runID, node.name, node.outputs)
	}
	expectNext(t, repo, runID)
	if s := runStatus(t, repo, runID); s != "completed" {
		t.Fatalf("run status = %q, want completed", s)
	}
}

// A scan that finds the hive terminal, or at its cap, goes straight to the
// report: no pass runs. check-gate follows both the skipped
// check-has-actions and the skipped dispatch, and merge both the skipped
// check-gate and the skipped gate. The skip cascade reaches them in map
// order, so the walk repeats until merge is skipped too, whichever it meets
// first.
func TestHiveWorkflow_CappedOrTerminalScanEndsTheLoop(t *testing.T) {
	yaml, err := os.ReadFile(filepath.Join("..", "..", "workflows", "hive.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		phase := []string{"capped", "terminal"}[i%2]
		repo := newWFStore(t).Workflows()
		runID := initYAML(t, repo, string(yaml), map[string]any{"project": "p", "max_iterations": 1})
		expectNext(t, repo, runID, "init")
		complete(t, repo, runID, "init", map[string]any{"iteration": 1.0})
		expectNext(t, repo, runID, "scan")
		complete(t, repo, runID, "scan", map[string]any{"phase": phase, "research_actions": []any{}, "gate_requested": false, "gate_wave": 0.0, "iteration": 1.0})
		expectNext(t, repo, runID, "report")
		for _, node := range []string{"dispatch", "check-gate", "gate", "merge", "complete-iteration"} {
			if s := nodeStatus(t, repo, runID, node); s != "skipped" {
				t.Fatalf("%s scan: %s is %s, want skipped", phase, node, s)
			}
		}
	}
}

// The dispatch node reads the plan as JSON through {hive_plan}, which the
// scan sets from its `research_actions` list, so the gap_id a dispatch needs
// to close its gap is readable; %v would render Go syntax.
func TestHiveWorkflow_DispatchReadsThePlanAsJSON(t *testing.T) {
	yaml, err := os.ReadFile(filepath.Join("..", "..", "workflows", "hive.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, string(yaml), map[string]any{"project": "p", "max_iterations": 1})
	expectNext(t, repo, runID, "init")
	complete(t, repo, runID, "init", map[string]any{"iteration": 0.0})
	expectNext(t, repo, runID, "scan")
	open := []any{map[string]any{"type": "dispatch_agent", "signal_type": "gap_fill", "payload": map[string]any{"gap_id": 42.0}}}
	complete(t, repo, runID, "scan", map[string]any{"phase": "dispatching", "research_actions": open, "gate_requested": false, "gate_wave": 0.0, "iteration": 1.0})

	nodes, err := GetNextNodes(repo, runID)
	if err != nil || len(nodes) != 1 || nodes[0].Node != "dispatch" {
		t.Fatalf("next = %+v (%v), want dispatch", nodes, err)
	}
	want, _ := json.Marshal(open)
	if !strings.Contains(nodes[0].ResolvedPrompt, string(want)) {
		t.Fatalf("dispatch prompt does not carry the plan as JSON %s:\n%s", want, nodes[0].ResolvedPrompt)
	}
}

// A failed node's successor can never run, so the run fails rather than stay
// `running`.
func TestStrandedRun_Fails(t *testing.T) {
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, `
name: chain
version: 1
nodes:
  a: {type: agent, prompt: "a"}
  b: {type: agent, prompt: "b"}
edges:
  - {from: a, to: b}
`, nil)
	expectNext(t, repo, runID, "a")
	if err := FailNode(repo, runID, "a", "boom"); err != nil {
		t.Fatal(err)
	}
	expectNext(t, repo, runID)
	if s := runStatus(t, repo, runID); s != "failed" {
		t.Fatalf("run status = %q, want failed", s)
	}
}

// A loop closed by an agent has nothing to re-run it.
func TestValidate_LoopMustCloseThroughADecision(t *testing.T) {
	f := filepath.Join(t.TempDir(), "wf.yaml")
	if err := os.WriteFile(f, []byte(`
name: agent-closed
version: 1
nodes:
  research: {type: agent, prompt: "r", outputs: [score]}
  check: {type: decision, condition: "score >= 80", true_edge: finalize, false_edge: retry}
  retry: {type: agent, prompt: "again"}
  finalize: {type: agent, prompt: "f"}
edges:
  - {from: research, to: check}
  - {from: retry, to: check}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, _, err := Validate(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], `closed by an edge from "retry"`) {
		t.Fatalf("errors = %v, want the loop closed by retry refused", errs)
	}
}

func nodeStatus(t *testing.T, repo *db.WorkflowsRepo, runID int64, node string) string {
	t.Helper()
	states, err := repo.GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.NodeName == node {
			return s.Status
		}
	}
	t.Fatalf("node %q not found", node)
	return ""
}

// An inner decision whose branch leaves the loop: pass 1 takes `work` and
// skips `bailout` and `cleanup`, and the loop's reset returns both to
// pending, so when pass 2 takes `bailout` it runs.
func TestDecisionLoop_InnerExitRunsOnALaterPass(t *testing.T) {
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, `
name: early-exit
version: 1
nodes:
  begin: {type: agent, prompt: b}
  head: {type: agent, prompt: h, outputs: [bail]}
  triage: {type: decision, condition: "bail == 'yes'", true_edge: bailout, false_edge: work}
  bailout: {type: agent, prompt: out}
  cleanup: {type: agent, prompt: c}
  work: {type: agent, prompt: w, outputs: [again]}
  loopcheck: {type: decision, condition: "again == 'yes'", true_edge: head, false_edge: done}
  done: {type: agent, prompt: d}
edges:
  - {from: begin, to: head}
  - {from: head, to: triage}
  - {from: bailout, to: cleanup}
  - {from: work, to: loopcheck}
`, nil)

	expectNext(t, repo, runID, "begin")
	complete(t, repo, runID, "begin", map[string]any{})
	expectNext(t, repo, runID, "head")
	complete(t, repo, runID, "head", map[string]any{"bail": "no"})
	expectNext(t, repo, runID, "work")
	complete(t, repo, runID, "work", map[string]any{"again": "yes"})

	// Pass 2 leaves the loop through the branch pass 1 skipped.
	expectNext(t, repo, runID, "head")
	complete(t, repo, runID, "head", map[string]any{"bail": "yes"})
	expectNext(t, repo, runID, "bailout")
	complete(t, repo, runID, "bailout", map[string]any{})
	expectNext(t, repo, runID, "cleanup")
	complete(t, repo, runID, "cleanup", map[string]any{})
	expectNext(t, repo, runID)

	if s := runStatus(t, repo, runID); s != "completed" {
		t.Fatalf("run status = %q, want completed", s)
	}
	for node, want := range map[string]string{"bailout": "completed", "cleanup": "completed", "work": "skipped", "loopcheck": "skipped", "done": "skipped"} {
		if got := nodeStatus(t, repo, runID, node); got != want {
			t.Errorf("%s = %q, want %q", node, got, want)
		}
	}
}

// A decision past the loop body chooses one branch on pass 1 and is not
// evaluated again, so its untaken branch stays skipped on pass 2: nothing
// chose it.
func TestDecisionLoop_SideDecisionsUntakenBranchStaysSkipped(t *testing.T) {
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, `
name: side-decision
version: 1
nodes:
  begin: {type: agent, prompt: b}
  head: {type: agent, prompt: h, outputs: [flag]}
  side: {type: decision, condition: "flag == 'left'", true_edge: left, false_edge: right}
  left: {type: agent, prompt: l}
  right: {type: agent, prompt: r}
  work: {type: agent, prompt: w, outputs: [again]}
  loopcheck: {type: decision, condition: "again == 'yes'", true_edge: head, false_edge: done}
  done: {type: agent, prompt: d}
edges:
  - {from: begin, to: head}
  - {from: head, to: side}
  - {from: head, to: work}
  - {from: work, to: loopcheck}
`, nil)

	// flag 'left' makes side's condition true, so it takes left and skips right.
	flag, taken, untaken := "left", "left", "right"

	expectNext(t, repo, runID, "begin")
	complete(t, repo, runID, "begin", map[string]any{})
	expectNext(t, repo, runID, "head")
	complete(t, repo, runID, "head", map[string]any{"flag": flag})
	expectNext(t, repo, runID, taken, "work")
	complete(t, repo, runID, taken, map[string]any{})
	complete(t, repo, runID, "work", map[string]any{"again": "yes"})

	// Pass 2 runs the body only. side is past it and keeps its choice.
	expectNext(t, repo, runID, "head")
	complete(t, repo, runID, "head", map[string]any{"flag": flag})
	expectNext(t, repo, runID, "work")
	complete(t, repo, runID, "work", map[string]any{"again": "no"})
	expectNext(t, repo, runID, "done")
	complete(t, repo, runID, "done", map[string]any{})
	expectNext(t, repo, runID)

	if s := runStatus(t, repo, runID); s != "completed" {
		t.Fatalf("run status = %q, want completed", s)
	}
	for node, want := range map[string]string{"side": "completed", taken: "completed", untaken: "skipped"} {
		if got := nodeStatus(t, repo, runID, node); got != want {
			t.Errorf("%s = %q, want %q", node, got, want)
		}
	}
}

// The loop's head is blocked on pass 2 (its only edge in is false) and
// skipped, but the closing decision has a completed predecessor outside the
// loop, so it evaluates and loops back again, which resets the head, which
// is blocked again. Resolving decisions after each round of skips would make
// that a cycle inside one GetNextNodes call, so the head is skipped at most
// once per call: it stays pending, nothing else can run, and the run fails.
func TestDecisionLoop_SkippedHeadLoopingBackEndsTheCall(t *testing.T) {
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, `
name: skipped-head
version: 1
nodes:
  begin: {type: agent, prompt: b, outputs: [c]}
  outside: {type: agent, prompt: o}
  head: {type: agent, prompt: h, outputs: [c, again]}
  loopcheck: {type: decision, condition: "again == 'yes'", true_edge: head, false_edge: done}
  done: {type: agent, prompt: d}
edges:
  - {from: begin, to: head, condition: "c == 'go'"}
  - {from: outside, to: loopcheck}
  - {from: head, to: loopcheck}
`, nil)
	expectNext(t, repo, runID, "begin", "outside")
	complete(t, repo, runID, "begin", map[string]any{"c": "go"})
	complete(t, repo, runID, "outside", map[string]any{})
	expectNext(t, repo, runID, "head")
	// c = stop makes begin → head false; again = yes makes loopcheck loop back.
	complete(t, repo, runID, "head", map[string]any{"c": "stop", "again": "yes"})

	type result struct {
		nodes []DispatchNode
		err   error
	}
	done := make(chan result, 1)
	go func() {
		nodes, err := GetNextNodes(repo, runID)
		done <- result{nodes, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || len(r.nodes) != 0 {
			t.Fatalf("GetNextNodes = %v, %v; want nothing ready", r.nodes, r.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("GetNextNodes did not return: the skip and the loop reset cycled")
	}
	if s := runStatus(t, repo, runID); s != "failed" {
		t.Fatalf("run status = %q, want failed", s)
	}
	for node, want := range map[string]string{"head": "pending", "loopcheck": "pending", "done": "pending"} {
		if got := nodeStatus(t, repo, runID, node); got != want {
			t.Errorf("%s = %q, want %q", node, got, want)
		}
	}
}

// An edge from a skipped predecessor never fires, whether that predecessor
// is a decision or not and whether the edge is conditioned or not. A node
// with a live predecessor still fires by the usual rule.
func TestEdgesFire_SkippedPredecessorsCarryNothing(t *testing.T) {
	nodes := map[string]any{
		"d": map[string]any{"type": "decision"},
		"a": map[string]any{"type": "agent"},
		"m": map[string]any{"type": "agent"},
	}
	outgoing := map[string][]Edge{
		"d": {{Target: "m", Condition: "x == 0"}},
		"a": {{Target: "m"}},
	}
	preds := map[string]bool{"d": true, "a": true}
	state := map[string]any{"x": 0.0}
	for _, c := range []struct {
		d, a string
		want bool
	}{
		{"skipped", "skipped", false},
		{"completed", "skipped", true},
		{"skipped", "completed", true},
	} {
		states := map[string]map[string]any{"d": {"status": c.d}, "a": {"status": c.a}}
		if got := edgesFire("m", preds, nodes, state, outgoing, states); got != c.want {
			t.Errorf("d %s, a %s: edgesFire = %v, want %v", c.d, c.a, got, c.want)
		}
	}
}

// A decision loop that runs no node — a decision whose branch is itself, or
// a cycle of decisions — reached by a run that never went through Validate,
// as InitWorkflow and `chb agent-run` do not, fails the GetNextNodes call,
// naming the decisions, in well under a second, rather than take its
// branches without end.
func TestDecisionLoop_OfDecisionsOnlyFailsTheCall(t *testing.T) {
	for _, c := range []struct {
		name, yaml string
		looping    []string
	}{
		{"a decision whose branch is itself", decisionSelfLoopYAML, []string{"d"}},
		{"two decisions in a loop", decisionCycleYAML, []string{"d1", "d2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := newWFStore(t).Workflows()
			runID := initYAML(t, repo, c.yaml, nil)
			expectNext(t, repo, runID, "start")
			complete(t, repo, runID, "start", map[string]any{})

			done := make(chan error, 1)
			go func() {
				_, err := GetNextNodes(repo, runID)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("GetNextNodes = nil error, want the decision loop refused")
				}
				for _, name := range c.looping {
					if !strings.Contains(err.Error(), name) {
						t.Errorf("error %q does not name decision %s", err, name)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("GetNextNodes was still resolving decisions after a second")
			}
		})
	}
}

// One pass of a loop may run no node, and the run goes on. Here check, the
// loop's closing decision, also follows a node outside the loop: triage
// skips work, check loops back once and turns its own flag off, triage
// skips work again, and check leaves. Each decision resolves twice in one
// call, in three scans, more than one scan per decision node.
func TestDecisionLoop_OnePassWithNoNodeRunGoesOn(t *testing.T) {
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, `
name: idle-pass
version: 1
nodes:
  begin: {type: agent, prompt: b, outputs: [proceed, again]}
  outside: {type: agent, prompt: o}
  triage: {type: decision, condition: "proceed == 'yes'", true_edge: work, false_edge: aside}
  work: {type: agent, prompt: w}
  aside: {type: agent, prompt: a}
  check: {type: decision, condition: "again == 'yes'", true_edge: triage, false_edge: done, state_updates: {again: "no"}}
  done: {type: agent, prompt: d}
edges:
  - {from: begin, to: triage}
  - {from: work, to: check}
  - {from: outside, to: check}
`, nil)
	expectNext(t, repo, runID, "begin", "outside")
	complete(t, repo, runID, "begin", map[string]any{"proceed": "no", "again": "yes"})
	complete(t, repo, runID, "outside", map[string]any{})
	expectNext(t, repo, runID, "aside", "done")
	complete(t, repo, runID, "aside", map[string]any{})
	complete(t, repo, runID, "done", map[string]any{})
	expectNext(t, repo, runID)
	if s := runStatus(t, repo, runID); s != "completed" {
		t.Fatalf("run status = %q, want completed", s)
	}
}
