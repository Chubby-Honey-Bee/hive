package workflow

import (
	"strings"
	"testing"
)

// A value's own placeholders stay as written, whatever order the state's
// keys come in: the fill reads the template once. Go randomises map order,
// so the fill runs many times.
func TestResolveTemplate_OnePass(t *testing.T) {
	state := map[string]any{"a": "{b} and {c}", "b": "B", "c": "{a}", "d": "D"}
	cases := map[string]string{
		"{a}|{b}|{c}":   "{b} and {c}|B|{a}",
		"{{b}}":         "{B}",
		"{x {d}":        "{x D",
		`{"k":{"b":1}}`: `{"k":{"b":1}}`,
		"{b":            "{b",
		"}{d}{":         "}D{",
	}
	for tmpl, want := range cases {
		for range 50 {
			if got := ResolveTemplate(tmpl, state); got != want {
				t.Fatalf("ResolveTemplate(%q) = %q, want %q", tmpl, got, want)
			}
		}
	}
}

// The placeholders a fill leaves are the template's alone, at their offsets
// in its result, and FillLeft fills those and no brace a value brought in.
// With no list it reads every {…} token.
func TestFillLeft_OnlyTheTemplatesPlaceholders(t *testing.T) {
	state := map[string]any{"pack": "see {comb.x} and {item}"}
	out, left := fillPlaceholders("{comb.x}|{pack}|{item}", stateLookup(state))
	if want := "{comb.x}|see {comb.x} and {item}|{item}"; out != want {
		t.Fatalf("fill = %q, want %q", out, want)
	}
	for _, p := range left {
		if out[p.Offset:p.Offset+len(p.Token)] != p.Token {
			t.Errorf("placeholder %q at %d does not sit there in %q", p.Token, p.Offset, out)
		}
	}
	upper := func(key string) (string, bool) { return strings.ToUpper(key), true }
	got, still := FillLeft(out, left, upper)
	if want := "COMB.X|see {comb.x} and {item}|ITEM"; got != want || len(still) != 0 {
		t.Errorf("FillLeft = %q with %d left, want %q with none", got, len(still), want)
	}
	onlyItem := func(key string) (string, bool) { return "I", key == "item" }
	got, still = FillLeft(out, left, onlyItem)
	if want := "{comb.x}|see {comb.x} and {item}|I"; got != want || len(still) != 1 || got[still[0].Offset:still[0].Offset+len(still[0].Token)] != "{comb.x}" {
		t.Errorf("FillLeft = %q with %+v left, want %q with {comb.x} at 0", got, still, want)
	}
	if got, _ := FillLeft(out, nil, onlyItem); got != "{comb.x}|see {comb.x} and I|I" {
		t.Errorf("FillLeft with no list = %q, want every {item} filled", got)
	}
}

// contextSwarm is a forager that reads the question and a context pack and a
// Queen that reads the run tokens.
const contextSwarm = `name: context
inputs: [question, context]
nodes:
  forager-a: {type: agent, prompt: "Q: {question}\nCTX: {context}"}
  queen:
    type: agent
    join: settled
    prompt: "CTX: {context}\nTALLY: {tally}\nLEDGER: {swarm.ledger}"
edges:
  - {from: forager-a, to: queen}
`

// A context pack that names a state key or a run token reaches the prompt
// as written: the engine fills the template, not the text a value brings in.
// Each init gets the same prompt, so --deterministic holds.
func TestResolveNodePrompt_ContextPackStaysAsWritten(t *testing.T) {
	pack := "Queen reads {tally} and {swarm.ledger}; a lens returns {verdict.forager:a}; the scope reads {question}."
	path := writeWorkflow(t, contextSwarm)
	for range 12 {
		store := newTestStore(t)
		repo := store.Workflows()
		runID, err := InitWorkflow(repo, path, map[string]any{"question": "q", "context": pack})
		if err != nil {
			t.Fatal(err)
		}
		next, err := GetNextNodes(repo, runID)
		if err != nil {
			t.Fatal(err)
		}
		if len(next) != 1 || next[0].Node != "forager-a" {
			t.Fatalf("next = %+v, want forager-a", next)
		}
		if got, want := next[0].ResolvedPrompt, "Q: q\nCTX: "+pack; got != want {
			t.Fatalf("forager prompt = %q, want %q", got, want)
		}
		if err := CompleteNode(repo, runID, "forager-a", map[string]any{"verdict": "support"}); err != nil {
			t.Fatal(err)
		}
		next, err = GetNextNodes(repo, runID)
		if err != nil {
			t.Fatal(err)
		}
		if len(next) != 1 || next[0].Node != "queen" {
			t.Fatalf("next = %+v, want queen", next)
		}
		p := next[0].ResolvedPrompt
		if !strings.HasPrefix(p, "CTX: "+pack+"\nTALLY: ") {
			t.Fatalf("Queen prompt = %q, want the pack as written before the tally", p)
		}
		if strings.Contains(p, "{tally}\nLEDGER") || strings.Contains(p, "LEDGER: {swarm.ledger}") {
			t.Errorf("Queen prompt %q left her own run tokens unfilled", p)
		}
	}
}
