package foragers

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// directSwarm is the swarm the direct-voice tests generate from: two lenses
// bonded resonates, and a dreamer.
func directSwarm() []Forager {
	return []Forager{
		{Name: "optimist", Title: "The Optimist", Description: "x", Body: "y", Bonds: []Bond{{To: "skeptic", Kind: BondResonates}}},
		{Name: "skeptic", Title: "The Skeptic", Description: "x", Body: "y"},
		{Name: "dreamer", Title: "The Dreamer", Description: "x", Body: "y", Archetype: ArchetypeDreamer},
	}
}

// renderSwarm renders a swarm and loads it as the engine does.
func renderSwarm(t *testing.T, swarm []Forager, opts WorkflowOptions) (defn map[string]any, nodes map[string]map[string]any) {
	t.Helper()
	text, err := GenerateWorkflow(swarm, opts)
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	defn, err = workflow.LoadYAMLString(text)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.CheckNodeFields(defn); err != nil {
		t.Fatalf("generated workflow: %v", err)
	}
	nodes = map[string]map[string]any{}
	for name, raw := range defn["nodes"].(map[string]any) {
		nodes[name] = raw.(map[string]any)
	}
	return defn, nodes
}

// edgesOf lists a generated workflow's edges as from→to.
func edgesOf(defn map[string]any) []string {
	var out []string
	for _, e := range defn["edges"].([]any) {
		m := e.(map[string]any)
		out = append(out, m["from"].(string)+"→"+m["to"].(string))
	}
	return out
}

// With --direct-voice the swarm gains one node, forager-direct, whose
// prompt and schema are the solo control's, held to a lens's terms: no
// tools, one repair, the verdict enum. It runs with no system prompt, on
// the first lens model, and feeds the Queen, who reads it as the direct
// answer; no edge enters it and no resonates pair names it. Without the
// flag nothing of it is written.
func TestGenerateWorkflow_DirectVoiceIsTheSoloCallInsideTheSwarm(t *testing.T) {
	for _, eval := range []bool{false, true} {
		for _, on := range []bool{false, true} {
			defn, nodes := renderSwarm(t, directSwarm(), WorkflowOptions{Name: "n", DirectVoice: on, Evaluate: eval, Model: "m1,m2"})
			node, ok := nodes["forager-"+DirectVoiceName]
			if ok != on {
				t.Fatalf("eval %v, direct voice %v: node present %v", eval, on, ok)
			}
			queen, _ := nodes["queen"]["prompt"].(string)
			token := "{verdict.forager:" + DirectVoiceName + "}"
			if strings.Contains(queen, token) != on {
				t.Errorf("eval %v, direct voice %v: queen reads %s = %v", eval, on, token, !on)
			}
			for _, e := range edgesOf(defn) {
				if strings.Contains(e, DirectVoiceName) && !on {
					t.Errorf("edge %s with the direct voice off", e)
				}
			}
			if !on {
				continue
			}
			if got := strings.TrimSpace(node["prompt"].(string)); got != strings.TrimSpace(DirectPrompt) {
				t.Errorf("direct prompt:\n%s\nwant the solo control's:\n%s", got, DirectPrompt)
			}
			if _, has := node["agent"]; has {
				t.Errorf("direct node names an agent persona %v; the solo control has none", node["agent"])
			}
			if node["role"] != "lens" || node["model"] != "m1" {
				t.Errorf("direct node role %v model %v, want lens on the first lens model m1", node["role"], node["model"])
			}
			if tools, declared := toolsOf(node); !declared || len(tools) != 0 {
				t.Errorf("direct node tools %v (declared %v), want []", tools, declared)
			}
			if !reflect.DeepEqual(node["on_reject"], map[string]any{"max_repair_iterations": 1}) {
				t.Errorf("direct node on_reject %v, want one repair", node["on_reject"])
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(DirectSchemaJSON), &want); err != nil {
				t.Fatal(err)
			}
			schema, _ := node["output_schema"].(map[string]any)
			if !reflect.DeepEqual(normalizeJSON(t, schema), normalizeJSON(t, want)) {
				t.Errorf("direct node schema %v, want %s", schema, DirectSchemaJSON)
			}
			head := "queen"
			if eval {
				head = "swarm-evaluate"
			}
			var in, out []string
			for _, e := range edgesOf(defn) {
				if strings.HasPrefix(e, "forager-"+DirectVoiceName+"→") {
					out = append(out, e)
				} else if strings.HasSuffix(e, "→forager-"+DirectVoiceName) {
					in = append(in, e)
				}
			}
			if len(in) != 0 || !reflect.DeepEqual(out, []string{"forager-" + DirectVoiceName + "→" + head}) {
				t.Errorf("direct voice edges in %v out %v, want none in and one out to %s", in, out, head)
			}
			for _, p := range workflow.ResonatesPairs(defn) {
				if p[0] == DirectVoiceName || p[1] == DirectVoiceName {
					t.Errorf("resonates pair %v names the direct voice, which fires no ∇", p)
				}
			}
			if !strings.Contains(queen, "the model with no persona") {
				t.Errorf("queen's prompt does not say what the direct answer is:\n%s", queen)
			}
		}
	}
}

// normalizeJSON round-trips a value through JSON so numbers compare alike.
func normalizeJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Under the lean profile the Queen reads the ledger, which lists the direct
// voice by name, so her prompt says what that name is.
func TestGenerateWorkflow_DirectVoiceNamedInTheLedger(t *testing.T) {
	_, nodes := renderSwarm(t, directSwarm(), WorkflowOptions{Name: "n", DirectVoice: true, PersonaProfile: ProfileLean})
	queen := nodes["queen"]["prompt"].(string)
	if !strings.Contains(queen, "{swarm.ledger}") || !strings.Contains(queen, "The lens named "+DirectVoiceName+" in the ledger is the direct answer") {
		t.Errorf("lean queen prompt lacks the ledger or the direct voice's line:\n%s", queen)
	}
}

// A forager named like the direct voice would share its node name, so the
// flag refuses it; without the flag the forager is an ordinary lens.
func TestGenerateWorkflow_DirectVoiceRefusesANameCollision(t *testing.T) {
	swarm := append(directSwarm(), Forager{Name: DirectVoiceName, Description: "x", Body: "y"})
	if _, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "n", DirectVoice: true}); err == nil || !strings.Contains(err.Error(), DirectVoiceName) {
		t.Errorf("a forager named %s under --direct-voice: %v, want a refusal naming it", DirectVoiceName, err)
	}
	if _, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "n"}); err != nil {
		t.Errorf("the same swarm without the flag: %v", err)
	}
}

// The context is dealt by paragraph, round-robin, in order: part i holds
// paragraphs i, i+n, …, joined by a blank line.
func TestSplitContext_ParagraphsRoundRobin(t *testing.T) {
	ctx := "p1\n\np2 line a\np2 line b\n \n\np3\n\n\np4\n\np5\n"
	want := []string{"p1\n\np4", "p2 line a\np2 line b\n\np5", "p3"}
	if got := SplitContext(ctx, 3); !reflect.DeepEqual(got, want) {
		t.Errorf("SplitContext(3) = %q, want %q", got, want)
	}
	if got := SplitContext(ctx, 1); got[0] != "p1\n\np2 line a\np2 line b\n\np3\n\np4\n\np5" {
		t.Errorf("SplitContext(1) = %q, want the whole context", got)
	}
	if got := SplitContext(ctx, 7); !reflect.DeepEqual(got, []string{"p1", "p2 line a\np2 line b", "p3", "p4", "p5", "", ""}) {
		t.Errorf("SplitContext(7) = %q, want one paragraph each and two empty parts", got)
	}
	if got := SplitContext("", 2); !reflect.DeepEqual(got, []string{"", ""}) {
		t.Errorf("an empty context splits to %q, want two empty parts", got)
	}
	if got := SplitContext(ctx, 0); got != nil {
		t.Errorf("SplitContext(0) = %q, want nil", got)
	}
}

// A one-paragraph context is dealt by the items of its shallowest list,
// the roster entry boundary the bench's pack uses: a nested list does not
// split, and the text before the first item rides with it.
func TestSplitContext_RosterEntries(t *testing.T) {
	ctx := "roster:\n  - name: a\n    bonds:\n      - to: b\n        kind: resonates\n  - name: b\n    archetype: lens\n  - name: c\n  - name: d\n"
	want := []string{
		"roster:\n  - name: a\n    bonds:\n      - to: b\n        kind: resonates\n  - name: d",
		"  - name: b\n    archetype: lens",
		"  - name: c",
	}
	if got := SplitContext(ctx, 3); !reflect.DeepEqual(got, want) {
		t.Errorf("SplitContext(3) = %q, want %q", got, want)
	}
	if got := SplitContext("one line, no list", 2); !reflect.DeepEqual(got, []string{"one line, no list", ""}) {
		t.Errorf("a context with no block boundary splits to %q, want it whole in the first part", got)
	}
}

// Every part is a distinct piece: each non-blank line of the context lands
// in exactly one part, in its original order, and the parts together are
// the whole context.
func TestSplitContext_PartsAreDistinctAndTheirUnionIsTheWhole(t *testing.T) {
	contexts := []string{
		"p1\n\np2 line a\np2 line b\n\np3\n\np4\n\np5",
		"roster:\n  - name: a\n    bonds:\n      - to: b\n        kind: resonates\n  - name: b\n    archetype: lens\n  - name: c\n  - name: d\n  - name: e\n",
		"- x\n- y\n- z",
		"single paragraph with no list at all",
	}
	for _, ctx := range contexts {
		for n := 1; n <= 6; n++ {
			parts := SplitContext(ctx, n)
			if len(parts) != n {
				t.Fatalf("SplitContext(%q, %d) gave %d parts", ctx, n, len(parts))
			}
			var all []string
			for _, l := range strings.Split(ctx, "\n") {
				if strings.TrimSpace(l) != "" {
					all = append(all, l)
				}
			}
			used := make([]bool, len(all))
			for i, p := range parts {
				pos := 0
				for _, l := range strings.Split(p, "\n") {
					if strings.TrimSpace(l) == "" {
						continue
					}
					for pos < len(all) && (used[pos] || all[pos] != l) {
						pos++
					}
					if pos == len(all) {
						t.Fatalf("n=%d part %d line %q is not an unused line of the context after its predecessors:\n%q", n, i, l, ctx)
					}
					used[pos] = true
					pos++
				}
			}
			for i, u := range used {
				if !u {
					t.Errorf("n=%d: context line %q is in no part", n, all[i])
				}
			}
		}
	}
}

// ContextParts deals the context to the lenses in name order, under the
// input each lens's prompt reads under --context-split; the generated
// workflow declares those inputs, the direct voice and scope keep the
// whole context, and without the flag every lens reads {context}.
func TestGenerateWorkflow_ContextSplitGivesEachLensItsOwnInput(t *testing.T) {
	swarm := []Forager{
		{Name: "zeta", Description: "x", Body: "y"},
		{Name: "alpha", Description: "x", Body: "y"},
		{Name: "mid", Description: "x", Body: "y"},
		{Name: "dreamer", Description: "x", Body: "y", Archetype: ArchetypeDreamer},
	}
	ctx := "p1\n\np2\n\np3\n\np4"
	parts := ContextParts(swarm, ctx)
	wantParts := map[string]string{ContextKey("alpha"): "p1\n\np4", ContextKey("mid"): "p2", ContextKey("zeta"): "p3"}
	if !reflect.DeepEqual(parts, wantParts) {
		t.Errorf("ContextParts = %q, want %q", parts, wantParts)
	}
	for _, on := range []bool{false, true} {
		defn, nodes := renderSwarm(t, swarm, WorkflowOptions{Name: "n", ContextSplit: on, Scope: true, DirectVoice: true})
		var inputs []string
		for _, in := range defn["inputs"].([]any) {
			inputs = append(inputs, in.(string))
		}
		wantInputs := []string{"question", "context"}
		if on {
			wantInputs = append(wantInputs, ContextKey("alpha"), ContextKey("mid"), ContextKey("zeta"))
		}
		if !reflect.DeepEqual(inputs, wantInputs) {
			t.Errorf("split %v: inputs %v, want %v", on, inputs, wantInputs)
		}
		for name, node := range nodes {
			p, _ := node["prompt"].(string)
			switch {
			case name == "scope", name == "forager-"+DirectVoiceName:
				if !strings.Contains(p, "{context}") || strings.Contains(p, "{"+ContextKey("")) {
					t.Errorf("split %v: %s reads %q, want the whole {context}", on, name, p)
				}
			case strings.HasPrefix(name, "forager-"):
				own := "{" + ContextKey(strings.TrimPrefix(name, "forager-")) + "}"
				if on && (!strings.Contains(p, own) || strings.Contains(p, "{context}")) {
					t.Errorf("split on: %s reads %q, want %s and not {context}", name, p, own)
				}
				if !on && (strings.Contains(p, own) || !strings.Contains(p, "{context}")) {
					t.Errorf("split off: %s reads %q, want {context}", name, p)
				}
			}
		}
	}
}
