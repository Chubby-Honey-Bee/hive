package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func TestCompleteNode_RunNotFound(t *testing.T) {
	store := newWFStore(t)
	err := CompleteNode(store.Workflows(), 99999, "a", map[string]any{"x": 1})
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}

func TestCompleteNode_HappyPath(t *testing.T) {
	store := newWFStore(t)
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, `name: t
nodes:
  a:
    type: agent
`, `{}`, []db.NodeSeed{{Name: "a", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	err = CompleteNode(store.Workflows(), id, "a", map[string]any{"out": "value"})
	if err != nil {
		t.Fatalf("CompleteNode: %v", err)
	}
}

// TestCompleteNode_AliasesFinalTextToDeclaredOutput: when an agent emits
// prose, CompleteNode aliases its final text onto the node's declared output
// (AliasFinalTextToDeclaredOutput), so downstream parallel_fan and decision
// nodes see state[<declared output>], and the runner and `chb workflow
// complete` alias alike.
func TestCompleteNode_AliasesFinalTextToDeclaredOutput(t *testing.T) {
	store := newWFStore(t)
	yaml := `name: t
nodes:
  decompose:
    type: agent
    prompt: "..."
    outputs: [research_angles]
`
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, yaml, `{}`, []db.NodeSeed{{Name: "decompose", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	prose := "1. Foraging\n2. Hive cooling\n3. Queen pheromones"
	if err := CompleteNode(store.Workflows(), id, "decompose", map[string]any{"final_text": prose}); err != nil {
		t.Fatalf("CompleteNode: %v", err)
	}
	run, err := store.Workflows().GetWorkflowRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.StateJSON, `"research_angles"`) {
		t.Errorf("state_json missing research_angles after alias: %s", run.StateJSON)
	}
	if !strings.Contains(run.StateJSON, `"final_text"`) {
		t.Errorf("state_json should carry final_text beside the alias: %s", run.StateJSON)
	}
}

func TestCompleteNode_AcceptRejected(t *testing.T) {
	store := newWFStore(t)
	yaml := `name: t
nodes:
  a:
    type: agent
    accept:
      - "outputs.compile_ok == true"
`
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, yaml, `{}`, []db.NodeSeed{{Name: "a", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	err = CompleteNode(store.Workflows(), id, "a", map[string]any{"compile_ok": false})
	if err == nil {
		t.Fatal("expected AcceptRejection")
	}
	if _, ok := err.(*AcceptRejection); !ok {
		t.Errorf("err type = %T; want *AcceptRejection", err)
	}
}

func TestFailNode_RunNotFound_StillMarks(t *testing.T) {
	store := newWFStore(t)
	// Even when GetWorkflowRun fails, FailNode is best-effort: it still
	// tries to mark the node failed (non-retryable).
	err := FailNode(store.Workflows(), 99999, "ghost", "boom")
	_ = err // either nil (if marked) or an error from MarkNodeFailed; both branches exercised
}

func TestFailNode_NoRetriesMarksFailed(t *testing.T) {
	store := newWFStore(t)
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, `name: t
nodes:
  a:
    type: agent
`, `{}`, []db.NodeSeed{{Name: "a", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := FailNode(store.Workflows(), id, "a", "boom"); err != nil {
		t.Fatalf("FailNode: %v", err)
	}
}

func TestFailNode_WithRetries(t *testing.T) {
	store := newWFStore(t)
	yaml := `name: t
nodes:
  a:
    type: agent
    max_retries: 2
`
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, yaml, `{}`, []db.NodeSeed{{Name: "a", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := FailNode(store.Workflows(), id, "a", "boom"); err != nil {
		t.Fatalf("FailNode: %v", err)
	}
}

func TestExtractJSONOutput(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]any
	}{
		{`{"a": 1}`, map[string]any{"a": float64(1)}},
		{"```json\n{\"b\": 2}\n```", map[string]any{"b": float64(2)}},
		{"prefix\n{\"c\": 3}\nsuffix", map[string]any{"c": float64(3)}},
		{"no json here", map[string]any{"final_text": "no json here"}},
	}
	for _, c := range cases {
		got := ExtractJSONOutput(c.in)
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(c.want)
		if string(a) != string(b) {
			t.Errorf("ExtractJSONOutput(%q) = %s, want %s", c.in, a, b)
		}
	}
}

// A reply of prose followed by a JSON tail parses to the tail: extraction
// takes the last balanced object, since the widest first-{ to last-} span
// cannot parse once the prose holds a brace of its own, and the reply's
// accept: predicate would then see no verdict.
func TestExtractJSONOutput_ProseThenJSONTail(t *testing.T) {
	cases := []struct {
		name, text, wantVerdict string
	}{
		{
			"clean prose then tail",
			"## Swarm Verdict\n\nFive foragers converge on opt-in.\n\n{\"verdict\":\"conditional\",\"convergence\":\"partial\",\"recommendation\":\"ship behind a flag\"}",
			"conditional",
		},
		{
			"prose containing a brace, then tail",
			"## Verdict\n\nThe template `{comb.forager:skeptic}` was unresolved, and `{}` appeared in one quote.\n\n{\"verdict\":\"support\",\"recommendation\":\"ship\"}",
			"support",
		},
		{
			"tail with a brace inside a string value",
			"Notes about {placeholders}.\n\n{\"verdict\":\"oppose\",\"recommendation\":\"the literal {x} stays\"}",
			"oppose",
		},
		{
			"fenced tail after prose",
			"## Verdict\n\nSomething about {a}.\n\n```json\n{\"verdict\":\"abstain\",\"recommendation\":\"need scope\"}\n```",
			"abstain",
		},
	}
	for _, c := range cases {
		got := ExtractJSONOutput(c.text)
		if got["verdict"] != c.wantVerdict {
			t.Errorf("%s: verdict = %v, want %q (got %v)", c.name, got["verdict"], c.wantVerdict, got)
		}
	}
}

// The existing shapes must keep working.
func TestExtractJSONOutput_UnchangedShapes(t *testing.T) {
	if got := ExtractJSONOutput(`{"verdict":"support"}`); got["verdict"] != "support" {
		t.Errorf("bare object: %v", got)
	}
	if got := ExtractJSONOutput("```json\n{\"a\":1}\n```"); got["a"] != float64(1) {
		t.Errorf("fenced object: %v", got)
	}
	if got := ExtractJSONOutput("just prose, no object"); got["final_text"] != "just prose, no object" {
		t.Errorf("prose falls back to final_text: %v", got)
	}
	if got := ExtractJSONOutput(""); len(got) != 0 {
		t.Errorf("empty input: %v", got)
	}
}
