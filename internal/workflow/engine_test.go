package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := db.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

const simpleWorkflowYAML = `
name: test-simple
description: A simple linear workflow
version: 1
inputs:
  - topic

nodes:
  research:
    type: agent
    agent: researcher
    model: sonnet
    prompt: "Research {topic}"
    outputs: [findings]

  analyze:
    type: agent
    agent: analyst
    model: sonnet
    prompt: "Analyze findings: {findings}"
    outputs: [analysis]

edges:
  - from: research
    to: analyze
`

const parallelWorkflowYAML = `
name: test-parallel
description: Workflow with parallel start nodes
version: 1
inputs:
  - question

nodes:
  optimist:
    type: agent
    agent: analyst
    model: sonnet
    prompt: "Optimistic view of {question}"
    outputs: [opt_view]

  skeptic:
    type: agent
    agent: analyst
    model: sonnet
    prompt: "Skeptical view of {question}"
    outputs: [skep_view]

  synthesize:
    type: agent
    agent: analyst
    model: opus
    prompt: "Synthesize: {opt_view} vs {skep_view}"
    outputs: [synthesis]

edges:
  - from: optimist
    to: synthesize
  - from: skeptic
    to: synthesize
`

const decisionWorkflowYAML = `
name: test-decision
description: Workflow with conditional branching
version: 1
inputs:
  - question

nodes:
  plan:
    type: agent
    agent: researcher
    model: sonnet
    prompt: "Plan research on {question}"

  research:
    type: agent
    agent: researcher
    model: sonnet
    prompt: "Research {question}"
    outputs: [score]

  check:
    type: decision
    condition: "score >= 80"
    true_edge: finalize
    false_edge: research

  finalize:
    type: agent
    agent: analyst
    model: opus
    prompt: "Finalize with score {score}"
    outputs: [report]

edges:
  - from: plan
    to: research
  - from: research
    to: check
  - from: check
    to: finalize
    condition: "score >= 80"
  - from: check
    to: research
    condition: "score < 80"
`

const humanReviewWorkflowYAML = `
name: test-human-review
description: Workflow with human review checkpoint
version: 1
inputs:
  - topic

nodes:
  research:
    type: agent
    agent: researcher
    model: sonnet
    prompt: "Research {topic}"
    outputs: [findings]

  review:
    type: human_review
    prompt: "Review research on {topic}. Findings: {findings}"

  synthesize:
    type: agent
    agent: analyst
    model: opus
    prompt: "Synthesize {findings}"
    outputs: [synthesis]

edges:
  - from: research
    to: review
  - from: review
    to: synthesize
`

const gateWorkflowYAML = `
name: test-gate
description: Workflow with gate enforcement
version: 1
inputs:
  - topic

nodes:
  research:
    type: agent
    agent: researcher
    model: sonnet
    prompt: "Research {topic}"
    outputs: [findings]

  gate:
    type: gate
    wave_ref: wave
    auto_resolve_numeric: true
    skip_source_check: true

  synthesize:
    type: agent
    agent: analyst
    model: opus
    prompt: "Synthesize {findings}"
    outputs: [synthesis]

edges:
  - from: research
    to: gate
  - from: gate
    to: synthesize
`

// ───── SAFE EVAL ─────

func TestSafeEvalSimpleComparison(t *testing.T) {
	tests := []struct {
		expr   string
		state  map[string]any
		expect bool
	}{
		{"score >= 80", map[string]any{"score": 90}, true},
		{"score >= 80", map[string]any{"score": 70}, false},
	}
	for _, tc := range tests {
		result, err := SafeEval(tc.expr, tc.state)
		if err != nil {
			t.Fatalf("SafeEval(%q): %v", tc.expr, err)
		}
		if evalToBool(result) != tc.expect {
			t.Fatalf("SafeEval(%q) = %v, expected %v", tc.expr, result, tc.expect)
		}
	}
}

func TestSafeEvalEquality(t *testing.T) {
	result, _ := SafeEval(`verdict == "COMPLETE"`, map[string]any{"verdict": "COMPLETE"})
	if evalToBool(result) != true {
		t.Fatal("expected true")
	}
	result, _ = SafeEval(`verdict == "COMPLETE"`, map[string]any{"verdict": "NEEDS_MORE_WORK"})
	if evalToBool(result) != false {
		t.Fatal("expected false")
	}
}

func TestSafeEvalNotEqual(t *testing.T) {
	result, _ := SafeEval(`verdict != "COMPLETE"`, map[string]any{"verdict": "NEEDS_MORE_WORK"})
	if evalToBool(result) != true {
		t.Fatal("expected true")
	}
}

func TestSafeEvalBooleanAnd(t *testing.T) {
	result, _ := SafeEval("a > 0 && b > 0", map[string]any{"a": 1, "b": 2})
	if evalToBool(result) != true {
		t.Fatal("expected true")
	}
	result, _ = SafeEval("a > 0 && b > 0", map[string]any{"a": 1, "b": -1})
	if evalToBool(result) != false {
		t.Fatal("expected false")
	}
}

func TestSafeEvalBooleanOr(t *testing.T) {
	result, _ := SafeEval(`verdict == "COMPLETE" || verdict == "NEEDS_MINOR_FOLLOWUP"`,
		map[string]any{"verdict": "NEEDS_MINOR_FOLLOWUP"})
	if evalToBool(result) != true {
		t.Fatal("expected true")
	}
}

func TestSafeEvalNot(t *testing.T) {
	result, _ := SafeEval("!done", map[string]any{"done": false})
	if evalToBool(result) != true {
		t.Fatal("expected true for !false")
	}
	result, _ = SafeEval("!done", map[string]any{"done": true})
	if evalToBool(result) != false {
		t.Fatal("expected false for !true")
	}
}

func TestSafeEvalUnknownVariableErrors(t *testing.T) {
	_, err := SafeEval("nonexistent > 5", map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown variable")
	}
}

// Boolean operators short-circuit: the right operand is not required when
// the left already determines the result, so a `false && x` / `true || x`
// predicate does not error on an unbound x.
func TestSafeEvalShortCircuits(t *testing.T) {
	cases := []struct {
		expr   string
		expect bool
	}{
		{"a > 0 && unbound > 0", false}, // left false → right (unbound) never evaluated
		{"a > 0 || unbound > 0", true},  // left true → right (unbound) never evaluated
	}
	for _, tc := range cases {
		result, err := SafeEval(tc.expr, map[string]any{"a": -1})
		if tc.expect {
			result, err = SafeEval(tc.expr, map[string]any{"a": 1})
		}
		if err != nil {
			t.Fatalf("SafeEval(%q) should short-circuit, got error: %v", tc.expr, err)
		}
		if evalToBool(result) != tc.expect {
			t.Fatalf("SafeEval(%q) = %v, expected %v", tc.expr, result, tc.expect)
		}
	}
	// Sanity: when the right operand IS needed, an unbound var still errors.
	if _, err := SafeEval("a > 0 && unbound > 0", map[string]any{"a": 1}); err == nil {
		t.Fatal("expected error: left true forces evaluation of unbound right operand")
	}
}

func TestSafeEvalNumericComparisons(t *testing.T) {
	tests := []struct {
		expr   string
		state  map[string]any
		expect bool
	}{
		{"x < 10", map[string]any{"x": 5}, true},
		{"x <= 10", map[string]any{"x": 10}, true},
		{"x > 10", map[string]any{"x": 15}, true},
		{"x >= 10", map[string]any{"x": 10}, true},
	}
	for _, tc := range tests {
		result, err := SafeEval(tc.expr, tc.state)
		if err != nil {
			t.Fatalf("SafeEval(%q): %v", tc.expr, err)
		}
		if evalToBool(result) != tc.expect {
			t.Fatalf("SafeEval(%q) = %v, expected %v", tc.expr, result, tc.expect)
		}
	}
}

// TestSafeEvalSingleQuotedStrings: workflows write conditions like
// `eval_verdict == 'COMPLETE'`, with Python/SQL-style single quotes. Go's
// parser rejects single-quoted multi-char tokens as malformed rune literals,
// so SafeEval normalizes them to double quotes before parsing; otherwise
// every decision node using a string comparison would fail to evaluate, stay
// pending and end skipped, with no error surfaced.
func TestSafeEvalSingleQuotedStrings(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		state  map[string]any
		expect bool
	}{
		{
			name:   "single-quoted equality (match)",
			expr:   "verdict == 'COMPLETE'",
			state:  map[string]any{"verdict": "COMPLETE"},
			expect: true,
		},
		{
			name:   "single-quoted equality (miss)",
			expr:   "verdict == 'COMPLETE'",
			state:  map[string]any{"verdict": "NEEDS_MORE_WORK"},
			expect: false,
		},
		{
			name:   "single-quoted inequality — the consciousness-textbook loop case",
			expr:   "eval_verdict != 'COMPLETE'",
			state:  map[string]any{"eval_verdict": "NEEDS_MORE_WORK"},
			expect: true,
		},
		{
			name:   "single-quoted disjunction",
			expr:   "verdict == 'COMPLETE' or verdict == 'NEEDS_MINOR_FOLLOWUP'",
			state:  map[string]any{"verdict": "NEEDS_MINOR_FOLLOWUP"},
			expect: true,
		},
		{
			name:   "single-quoted mixed with numeric — decompose.yaml pattern",
			expr:   "eval_verdict == 'NEEDS_MORE_WORK' and wave_count > 2",
			state:  map[string]any{"eval_verdict": "NEEDS_MORE_WORK", "wave_count": 3},
			expect: true,
		},
		{
			name:   "double-quoted string literal",
			expr:   `verdict == "COMPLETE"`,
			state:  map[string]any{"verdict": "COMPLETE"},
			expect: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := SafeEval(tc.expr, tc.state)
			if err != nil {
				t.Fatalf("SafeEval(%q) errored: %v", tc.expr, err)
			}
			if evalToBool(result) != tc.expect {
				t.Fatalf("SafeEval(%q) = %v, expected %v", tc.expr, result, tc.expect)
			}
		})
	}
}

// TestNormalizeExpression exercises the scanner that rewrites
// Python/SQL-style surface syntax (single-quoted strings, word-form
// boolean operators) into Go syntax. Must leave double-quoted literals
// untouched, and must respect identifier boundaries when rewriting
// word operators (so `nordic`, `land`, `not_done` stay intact).
func TestNormalizeExpression(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		// single-quote → double-quote
		{"verdict == 'COMPLETE'", `verdict == "COMPLETE"`},
		{`msg == "it's fine"`, `msg == "it's fine"`},         // apostrophe inside double-quoted
		{`x == "a'b" or y == 'z'`, `x == "a'b" || y == "z"`}, // also exercises `or`
		{"unterminated 'oops", "unterminated 'oops"},         // passthrough

		// word operators → Go operators
		{"a and b", "a && b"},
		{"a or b", "a || b"},
		{"not done", "! done"},
		{"not (a or b)", "! (a || b)"},

		// identifier boundaries — do NOT rewrite mid-word
		{"nordic_count > 0", "nordic_count > 0"},
		{"not_done == true", "not_done == true"},
		{"brand == 'x'", `brand == "x"`},
		{"xor_flag > 0", "xor_flag > 0"},

		// combined — the decompose.yaml pattern
		{
			"eval_verdict == 'NEEDS_MORE_WORK' and wave_count > 2",
			`eval_verdict == "NEEDS_MORE_WORK" && wave_count > 2`,
		},

		// word operators inside double-quoted strings must not be rewritten
		{`msg == "stop and think"`, `msg == "stop and think"`},

		// empty / no-op
		{"no quotes here", "no quotes here"},
		{"", ""},
	}
	for _, tc := range tests {
		got := normalizeExpression(tc.in)
		if got != tc.want {
			t.Errorf("normalizeExpression(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ───── TEMPLATE RESOLUTION ─────

func TestTemplateSimpleSubstitution(t *testing.T) {
	result := ResolveTemplate("Research {topic}", map[string]any{"topic": "bees"})
	if result != "Research bees" {
		t.Fatalf("expected 'Research bees', got %q", result)
	}
}

func TestTemplateMultipleSubstitutions(t *testing.T) {
	result := ResolveTemplate("{a} and {b}", map[string]any{"a": "hello", "b": "world"})
	if result != "hello and world" {
		t.Fatalf("expected 'hello and world', got %q", result)
	}
}

func TestTemplateMissingVariableUnchanged(t *testing.T) {
	result := ResolveTemplate("Research {missing}", map[string]any{"topic": "bees"})
	if result != "Research {missing}" {
		t.Fatalf("expected 'Research {missing}', got %q", result)
	}
}

// ───── GRAPH ANALYSIS ─────

func TestBuildGraphSimple(t *testing.T) {
	defn, err := LoadYAMLString(simpleWorkflowYAML)
	if err != nil {
		t.Fatalf("LoadYAMLString: %v", err)
	}
	incoming, _, err := BuildGraph(defn)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if _, ok := incoming["research"]; !ok {
		t.Fatal("expected research in incoming")
	}
	if _, ok := incoming["analyze"]; !ok {
		t.Fatal("expected analyze in incoming")
	}
	if len(incoming["research"]) != 0 {
		t.Fatal("research should have no predecessors")
	}
	if _, ok := incoming["analyze"]["research"]; !ok {
		t.Fatal("analyze should have research as predecessor")
	}
}

func TestFindStartNodes(t *testing.T) {
	defn, _ := LoadYAMLString(simpleWorkflowYAML)
	incoming, _, _ := BuildGraph(defn)
	start := findStartNodes(incoming)
	if len(start) != 1 || start[0] != "research" {
		t.Fatalf("expected [research], got %v", start)
	}
}

func TestParallelStartNodes(t *testing.T) {
	defn, _ := LoadYAMLString(parallelWorkflowYAML)
	incoming, _, _ := BuildGraph(defn)
	start := findStartNodes(incoming)
	startSet := map[string]bool{}
	for _, s := range start {
		startSet[s] = true
	}
	if !startSet["optimist"] || !startSet["skeptic"] {
		t.Fatalf("expected {optimist, skeptic}, got %v", start)
	}
}

// ───── WORKFLOW RUN (DB-BACKED) ─────

func TestInitAndNext(t *testing.T) {
	s := newTestStore(t)

	// Write YAML to temp file
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "test.yaml")
	os.WriteFile(wfFile, []byte(simpleWorkflowYAML), 0644)

	runID, err := InitWorkflow(s.Workflows(), wfFile, map[string]any{"topic": "bees"})
	if err != nil {
		t.Fatalf("InitWorkflow: %v", err)
	}

	dispatch, err := GetNextNodes(s.Workflows(), runID)
	if err != nil {
		t.Fatalf("GetNextNodes: %v", err)
	}
	if len(dispatch) == 0 {
		t.Fatal("expected at least 1 dispatchable node")
	}

	// Check research is dispatchable
	found := false
	for _, d := range dispatch {
		if d.Node == "research" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected research to be dispatchable")
	}
}

func TestCompleteUnlocksNext(t *testing.T) {
	s := newTestStore(t)
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "test.yaml")
	os.WriteFile(wfFile, []byte(simpleWorkflowYAML), 0644)

	runID, _ := InitWorkflow(s.Workflows(), wfFile, map[string]any{"topic": "bees"})

	// Complete research node
	err := CompleteNode(s.Workflows(), runID, "research", map[string]any{"findings": "lots of data"})
	if err != nil {
		t.Fatalf("CompleteNode: %v", err)
	}

	// Analyze should now be available
	dispatch, _ := GetNextNodes(s.Workflows(), runID)
	found := false
	for _, d := range dispatch {
		if d.Node == "analyze" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected analyze to be dispatchable after research completes")
	}
}

func TestParallelNodesBothReady(t *testing.T) {
	s := newTestStore(t)
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "test.yaml")
	os.WriteFile(wfFile, []byte(parallelWorkflowYAML), 0644)

	runID, _ := InitWorkflow(s.Workflows(), wfFile, map[string]any{"question": "x"})
	dispatch, _ := GetNextNodes(s.Workflows(), runID)

	nodeNames := map[string]bool{}
	for _, d := range dispatch {
		nodeNames[d.Node] = true
	}
	if !nodeNames["optimist"] || !nodeNames["skeptic"] {
		t.Fatalf("expected both optimist and skeptic, got %v", nodeNames)
	}
}

func TestSynthesizeWaitsForBothParallel(t *testing.T) {
	s := newTestStore(t)
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "test.yaml")
	os.WriteFile(wfFile, []byte(parallelWorkflowYAML), 0644)

	runID, _ := InitWorkflow(s.Workflows(), wfFile, map[string]any{"question": "x"})

	// Complete optimist, and set skeptic to running (not yet completed)
	CompleteNode(s.Workflows(), runID, "optimist", map[string]any{"opt_view": "good"})
	s.WriteDB.Exec("UPDATE workflow_node_states SET status='running' WHERE run_id=? AND node_name='skeptic'", runID)

	dispatch, _ := GetNextNodes(s.Workflows(), runID)
	for _, d := range dispatch {
		if d.Node == "synthesize" {
			t.Fatal("synthesize should NOT be ready while skeptic is running")
		}
	}
}

// ───── HUMAN REVIEW ─────

func TestHumanReviewNodeReady(t *testing.T) {
	s := newTestStore(t)
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "test.yaml")
	os.WriteFile(wfFile, []byte(humanReviewWorkflowYAML), 0644)

	runID, _ := InitWorkflow(s.Workflows(), wfFile, map[string]any{"topic": "bees"})
	CompleteNode(s.Workflows(), runID, "research", map[string]any{"findings": "lots of bees"})

	dispatch, _ := GetNextNodes(s.Workflows(), runID)
	found := false
	for _, d := range dispatch {
		if d.Node == "review" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected review to be ready")
	}
}

// ───── GATE NODE ─────

// Validate refuses `gate`, which is no node type: a wave gate runs as a
// command node (`chb guard`).
func TestValidateRefusesGateType(t *testing.T) {
	wfFile := filepath.Join(t.TempDir(), "test.yaml")
	os.WriteFile(wfFile, []byte(gateWorkflowYAML), 0644)

	errs, _, err := Validate(wfFile)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !strings.Contains(strings.Join(errs, "\n"), `invalid type "gate"`) {
		t.Fatalf("a gate node validated; errors = %v", errs)
	}
}

// ───── VALIDATION ─────

func TestValidateSimpleWorkflow(t *testing.T) {
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "test.yaml")
	os.WriteFile(wfFile, []byte(simpleWorkflowYAML), 0644)

	errs, _, err := Validate(wfFile)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("expected valid, got errors: %v", errs)
	}
}

func TestValidateDecisionWorkflow(t *testing.T) {
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "test.yaml")
	os.WriteFile(wfFile, []byte(decisionWorkflowYAML), 0644)

	errs, _, err := Validate(wfFile)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("expected valid, got errors: %v", errs)
	}
}

func TestValidateRejectsMissingNodes(t *testing.T) {
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "test.yaml")
	os.WriteFile(wfFile, []byte(`
name: empty
description: empty
version: 1
nodes: {}
edges: []
`), 0644)

	errs, _, err := Validate(wfFile)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("expected validation errors for empty nodes")
	}
}

// ───── RETRY ─────

func TestRetryResetsNodeToPending(t *testing.T) {
	s := newTestStore(t)
	retryYAML := `
name: test-retry
version: 1
inputs: [topic]
nodes:
  research:
    type: agent
    agent: researcher
    model: sonnet
    prompt: "Research {topic}"
    outputs: [findings]
    max_retries: 2
edges: []
`
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "test.yaml")
	os.WriteFile(wfFile, []byte(retryYAML), 0644)

	runID, _ := InitWorkflow(s.Workflows(), wfFile, map[string]any{"topic": "bees"})

	// Mark as running with attempt 1
	s.WriteDB.Exec("UPDATE workflow_node_states SET status='running', attempt=1 WHERE run_id=? AND node_name='research'", runID)

	// Fail the node
	err := FailNode(s.Workflows(), runID, "research", "timeout")
	if err != nil {
		t.Fatalf("FailNode: %v", err)
	}

	// Check the node was reset to pending (retry)
	var status string
	var attempt int
	s.ReadDB.QueryRow("SELECT status, attempt FROM workflow_node_states WHERE run_id=? AND node_name='research'", runID).Scan(&status, &attempt)
	if status != "pending" {
		t.Fatalf("expected pending (retry), got %s", status)
	}
}

// ───── HELPERS ─────

func evalToBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case int64:
		return b != 0
	case float64:
		return b != 0
	}
	return false
}

// Ensure json import is used
var _ = json.Marshal

func TestToBool(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{nil, false},
		{true, true},
		{false, false},
		{int64(0), false},
		{int64(7), true},
		{float64(0), false},
		{float64(0.5), true},
		{"", false},
		{"false", false},
		{"False", false},
		{"true", true},
		{"yes", true},
		{[]int{1}, true}, // unknown type → true
	}
	for _, tc := range cases {
		if got := toBool(tc.in); got != tc.want {
			t.Errorf("toBool(%v) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestToFloat(t *testing.T) {
	cases := []struct {
		in   any
		want float64
	}{
		{int64(7), 7},
		{int(3), 3},
		{float64(2.5), 2.5},
		{nil, 0}, // unknown → 0
		{"foo", 0},
		{true, 0},
	}
	for _, tc := range cases {
		if got := toFloat(tc.in); got != tc.want {
			t.Errorf("toFloat(%v) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestListWorkflows_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := ListWorkflows(dir)
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

func TestListWorkflows_MissingDir(t *testing.T) {
	_, err := ListWorkflows("/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error for missing dir")
	}
}

func TestListWorkflows_PicksUpYAMLFiles(t *testing.T) {
	dir := t.TempDir()
	yaml := `name: test-wf
version: 1
inputs: []
nodes:
  a:
    type: agent
    agent: x
    model: haiku
    prompt: hi
    outputs: [ok]
    accept:
      - "outputs.ok == true"
`
	if err := os.WriteFile(filepath.Join(dir, "good.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("\tnot:valid:::"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ListWorkflows(dir)
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 yaml results, got %d", len(got))
	}
	// Find the good one and assert it has a name field.
	foundGood := false
	for _, item := range got {
		if item["file"] == "good.yaml" {
			foundGood = true
			if item["name"] != "test-wf" {
				t.Errorf("name field = %v; want test-wf", item["name"])
			}
		}
		if item["file"] == "broken.yaml" {
			if item["error"] == nil {
				t.Errorf("expected error field for broken.yaml")
			}
		}
	}
	if !foundGood {
		t.Error("did not find good.yaml entry")
	}
}

func TestListWorkflows_FallbackName(t *testing.T) {
	dir := t.TempDir()
	yaml := `version: 1
inputs: []
nodes:
  a:
    type: agent
`
	if err := os.WriteFile(filepath.Join(dir, "noname.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ListWorkflows(dir)
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0]["name"] != "noname" {
		t.Errorf("fallback name = %v; want %q", got[0]["name"], "noname")
	}
}
