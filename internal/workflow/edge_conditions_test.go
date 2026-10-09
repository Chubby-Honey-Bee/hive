package workflow

import "testing"

// A plain edge's `condition:` is evaluated: a node is ready only when some
// incoming edge fires, and is skipped when every one is false.
func TestFindReadyNodes_EdgeConditionsGate(t *testing.T) {
	defn := map[string]any{"nodes": map[string]any{
		"src":  map[string]any{"type": "agent"},
		"hot":  map[string]any{"type": "agent"},
		"cold": map[string]any{"type": "agent"},
		"any":  map[string]any{"type": "agent"},
	}}
	incoming := map[string]map[string]bool{
		"hot": {"src": true}, "cold": {"src": true}, "any": {"src": true},
	}
	outgoing := map[string][]Edge{"src": {
		{Target: "hot", Condition: "score >= 50"},
		{Target: "cold", Condition: "score < 50"},
		{Target: "any"}, // unconditioned: always fires
	}}
	nodeStates := func() map[string]map[string]any {
		return map[string]map[string]any{
			"src": {"status": "completed"}, "hot": {"status": "pending"},
			"cold": {"status": "pending"}, "any": {"status": "pending"},
		}
	}
	has := func(list []string, want string) bool {
		for _, s := range list {
			if s == want {
				return true
			}
		}
		return false
	}

	ready, blocked := findReadyNodes(defn, map[string]any{"score": int64(80)}, nodeStates(), incoming, outgoing)
	if !has(ready, "hot") || !has(ready, "any") {
		t.Errorf("score=80: ready = %v, want hot and any", ready)
	}
	if has(ready, "cold") || !has(blocked, "cold") {
		t.Errorf("score=80: cold should be blocked, ready=%v blocked=%v", ready, blocked)
	}

	ready, blocked = findReadyNodes(defn, map[string]any{"score": int64(10)}, nodeStates(), incoming, outgoing)
	if !has(ready, "cold") || !has(ready, "any") {
		t.Errorf("score=10: ready = %v, want cold and any", ready)
	}
	if has(ready, "hot") || !has(blocked, "hot") {
		t.Errorf("score=10: hot should be blocked, ready=%v blocked=%v", ready, blocked)
	}
}

// A decision node already chose its branch and skipped the other, so its
// implicit edges must not be re-evaluated here — that would double-count and
// could block the branch the decision deliberately took.
func TestFindReadyNodes_DecisionEdgesAreNotReEvaluated(t *testing.T) {
	defn := map[string]any{"nodes": map[string]any{
		"d":   map[string]any{"type": "decision", "condition": "n > 5", "true_edge": "yes"},
		"yes": map[string]any{"type": "agent"},
	}}
	incoming := map[string]map[string]bool{"yes": {"d": true}}
	outgoing := map[string][]Edge{"d": {{Target: "yes", Condition: "n > 5"}}}
	// State lacks `n` here (a later node overwrote it); the decision
	// already fired, so the branch must still be dispatchable.
	ready, blocked := findReadyNodes(defn, map[string]any{},
		map[string]map[string]any{"d": {"status": "completed"}, "yes": {"status": "pending"}},
		incoming, outgoing)
	if len(ready) != 1 || ready[0] != "yes" {
		t.Fatalf("ready = %v, blocked = %v; want the taken branch dispatchable", ready, blocked)
	}
}

// An unevaluable condition must not strand the run: the edge fires and the
// reason is logged.
func TestFindReadyNodes_UnevaluableConditionFailsOpen(t *testing.T) {
	defn := map[string]any{"nodes": map[string]any{
		"src": map[string]any{"type": "agent"}, "dst": map[string]any{"type": "agent"},
	}}
	ready, blocked := findReadyNodes(defn, map[string]any{},
		map[string]map[string]any{"src": {"status": "completed"}, "dst": {"status": "pending"}},
		map[string]map[string]bool{"dst": {"src": true}},
		map[string][]Edge{"src": {{Target: "dst", Condition: "missing_var >= 1"}}})
	if len(ready) != 1 || len(blocked) != 0 {
		t.Fatalf("ready = %v, blocked = %v; want the edge to fire open", ready, blocked)
	}
}

// `a` is blocked on the first readiness pass and skipped. Only after that
// skip is every edge into `X` known to be false, so the next readiness pass
// skips `X` too, rather than leave it pending with nothing running and fail
// a run in which nothing failed.
func TestGetNextNodes_SkipsANodeBlockedOnlyAfterTheCascade(t *testing.T) {
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, `
name: second-block
version: 1
nodes:
  root: {type: agent, prompt: r, outputs: [x]}
  a: {type: agent, prompt: a}
  X: {type: agent, prompt: X}
edges:
  - {from: root, to: a, condition: "x == 1"}
  - {from: a, to: X, condition: "x == 1"}
  - {from: root, to: X, condition: "x == 2"}
`, nil)
	expectNext(t, repo, runID, "root")
	complete(t, repo, runID, "root", map[string]any{"x": 0})
	expectNext(t, repo, runID)

	if s := runStatus(t, repo, runID); s != "completed" {
		t.Fatalf("run status = %q, want completed", s)
	}
	for _, node := range []string{"a", "X"} {
		if got := nodeStatus(t, repo, runID, node); got != "skipped" {
			t.Errorf("%s = %q, want skipped", node, got)
		}
	}
}

// A decision whose predecessor is skipped only by the cascade is resolved in
// the same call, since decisions are resolved again after the skips, so it
// does not stay pending with nothing running and fail a run in which nothing
// failed.
func TestGetNextNodes_ResolvesADecisionAfterTheCascade(t *testing.T) {
	// x = 0 makes root → a false, so a is skipped and the skip reaches d.
	x := 0

	t.Run("an unconditioned edge still fires", func(t *testing.T) {
		repo := newWFStore(t).Workflows()
		runID := initYAML(t, repo, `
name: decision-after-cascade
version: 1
nodes:
  root: {type: agent, prompt: r, outputs: [x]}
  a: {type: agent, prompt: a}
  d: {type: decision, condition: "x == 0", true_edge: yes_branch, false_edge: no_branch}
  yes_branch: {type: agent, prompt: y}
  no_branch: {type: agent, prompt: n}
edges:
  - {from: root, to: a, condition: "x == 1"}
  - {from: a, to: d}
  - {from: root, to: d}
`, nil)
		// root → d has no condition, so d evaluates x == 0.
		branch := "no_branch"
		if x == 0 {
			branch = "yes_branch"
		}
		expectNext(t, repo, runID, "root")
		complete(t, repo, runID, "root", map[string]any{"x": x})
		expectNext(t, repo, runID, branch)
		if s := runStatus(t, repo, runID); s != "running" {
			t.Fatalf("run status = %q, want running", s)
		}
		for node, want := range map[string]string{"a": "skipped", "d": "completed"} {
			if got := nodeStatus(t, repo, runID, node); got != want {
				t.Errorf("%s = %q, want %q", node, got, want)
			}
		}
	})

	t.Run("every edge is false", func(t *testing.T) {
		store := newWFStore(t)
		repo := store.Workflows()
		runID := initYAML(t, repo, `
name: decision-after-cascade
version: 1
nodes:
  root: {type: agent, prompt: r, outputs: [x]}
  a: {type: agent, prompt: a}
  d: {type: decision, condition: "x == 0", true_edge: yes_branch, false_edge: no_branch}
  yes_branch: {type: agent, prompt: y}
  no_branch: {type: agent, prompt: n}
edges:
  - {from: root, to: a, condition: "x == 1"}
  - {from: a, to: d, condition: "x == 1"}
  - {from: root, to: d, condition: "x == 2"}
`, nil)
		// With x = 0, a → d and root → d are both false, so d is skipped.
		expectNext(t, repo, runID, "root")
		complete(t, repo, runID, "root", map[string]any{"x": x})
		expectNext(t, repo, runID)
		if s := runStatus(t, repo, runID); s != "completed" {
			t.Fatalf("run status = %q, want completed", s)
		}
		for _, node := range []string{"a", "d", "yes_branch", "no_branch"} {
			if got := nodeStatus(t, repo, runID, node); got != "skipped" {
				t.Errorf("%s = %q, want skipped", node, got)
			}
		}
		var rows int
		if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM workflow_decisions WHERE run_id=?`, runID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Errorf("a skipped decision recorded %d evaluations, want 0", rows)
		}
	})
}

// A decision goes through edgesFire too: one whose only incoming edge is
// false is skipped and dispatches no branch.
func TestDecision_SkippedWhenNoIncomingEdgeFires(t *testing.T) {
	store := newWFStore(t)
	repo := store.Workflows()
	const defn = `
name: cond-into-decision
version: 1
nodes:
  root: {type: agent, prompt: r, outputs: [x]}
  d: {type: decision, condition: "x == 0", true_edge: yes_branch, false_edge: no_branch}
  yes_branch: {type: agent, prompt: y}
  no_branch: {type: agent, prompt: n}
edges:
  - {from: root, to: d, condition: "x == 1"}
`
	runID := initYAML(t, repo, defn, nil)
	expectNext(t, repo, runID, "root")
	complete(t, repo, runID, "root", map[string]any{"x": 0})
	expectNext(t, repo, runID)

	if s := runStatus(t, repo, runID); s != "completed" {
		t.Fatalf("run status = %q, want completed", s)
	}
	for _, node := range []string{"d", "yes_branch", "no_branch"} {
		if got := nodeStatus(t, repo, runID, node); got != "skipped" {
			t.Errorf("%s = %q, want skipped", node, got)
		}
	}
	var rows int
	if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM workflow_decisions WHERE run_id=?`, runID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("a skipped decision recorded %d evaluations, want 0", rows)
	}

	// With the edge true, the decision evaluates x == 0 as false.
	runID = initYAML(t, repo, defn, nil)
	expectNext(t, repo, runID, "root")
	complete(t, repo, runID, "root", map[string]any{"x": 1})
	expectNext(t, repo, runID, "no_branch")
}
