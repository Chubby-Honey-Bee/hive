package foragers

import (
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

func swarmNodes(t *testing.T, opts WorkflowOptions) map[string]map[string]any {
	t.Helper()
	swarm := []Forager{
		{Name: "optimist", Title: "The Optimist", Description: "x", Body: "y", Bonds: []Bond{{To: "skeptic", Kind: BondResonates}}},
		{Name: "skeptic", Title: "The Skeptic", Description: "x", Body: "y"},
		{Name: "dreamer", Title: "The Dreamer", Description: "x", Body: "y", Archetype: ArchetypeDreamer},
	}
	yamlText, err := GenerateWorkflow(swarm, opts)
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	defn, err := workflow.LoadYAMLString(yamlText)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.CheckNodeFields(defn); err != nil {
		t.Fatalf("generated workflow: %v", err)
	}
	out := map[string]map[string]any{}
	for name, raw := range defn["nodes"].(map[string]any) {
		out[name] = raw.(map[string]any)
	}
	return out
}

func toolsOf(node map[string]any) (list []string, declared bool) {
	raw, ok := node["tools"]
	if !ok {
		return nil, false
	}
	list = []string{}
	for _, t := range raw.([]any) {
		list = append(list, t.(string))
	}
	return list, true
}

// Every node that calls a model runs as the short swarm persona. Scope,
// the evaluator and Queen get no tools; the lenses — each forager and the
// follow-up fan — get what --lens-tools names, and with "all" no tools:
// key, so every tool.
func TestGenerateWorkflow_SwarmPersonaAndTools(t *testing.T) {
	lensTools := map[string][]string{"none": {}, "read": {"read_file", "glob", "grep"}, "all": nil}
	for lt, want := range lensTools {
		t.Run(lt, func(t *testing.T) {
			nodes := swarmNodes(t, WorkflowOptions{Name: "n", Evaluate: true, Scope: true, LensTools: lt})
			for name, node := range nodes {
				if strings.HasPrefix(name, "dreamer-") {
					if _, ok := node["tools"]; ok {
						t.Errorf("%s calls no model but declares tools", name)
					}
					continue
				}
				if node["agent"] != "swarm" {
					t.Errorf("%s runs as agent %v, want swarm", name, node["agent"])
				}
				got, declared := toolsOf(node)
				lens := strings.HasPrefix(name, "forager-") || name == "swarm-followup"
				switch {
				case !lens:
					if !declared || len(got) != 0 {
						t.Errorf("%s tools %v (declared %v), want []", name, got, declared)
					}
				case want == nil:
					if declared {
						t.Errorf("%s tools %v, want no tools: key", name, got)
					}
				case !reflect.DeepEqual(got, want):
					t.Errorf("%s tools %v, want %v", name, got, want)
				}
			}
		})
	}
	for _, bad := range []WorkflowOptions{{LensTools: "write"}, {Evaluate: true, Followups: -1}} {
		if _, err := GenerateWorkflow([]Forager{{Name: "a", Description: "x", Body: "y"}}, bad); err == nil {
			t.Errorf("GenerateWorkflow(%+v) passed, want it refused", bad)
		}
	}
}

// Foragers, the evaluator and Queen each get one repair. The follow-up fan
// is capped at Followups and lists the rest under gaps_unfollowed. The
// evaluator returns no verdict: the edge into the fan tests its gaps.
func TestGenerateWorkflow_RepairAndFollowupCap(t *testing.T) {
	for _, k := range []int{0, 1, 5} {
		nodes := swarmNodes(t, WorkflowOptions{Name: "n", Evaluate: true, Followups: k})
		want := k
		if want == 0 {
			want = 3
		}
		fan := nodes["swarm-followup"]
		if fan["fan_limit"] != want || fan["fan_overflow"] != "gaps_unfollowed" {
			t.Errorf("Followups %d: fan_limit %v, fan_overflow %v; want %d, gaps_unfollowed", k, fan["fan_limit"], fan["fan_overflow"], want)
		}
		for name, node := range nodes {
			if !strings.HasPrefix(name, "forager-") && name != "swarm-evaluate" && name != "queen" {
				continue
			}
			block, _ := node["on_reject"].(map[string]any)
			if block["max_repair_iterations"] != 1 {
				t.Errorf("%s on_reject %v, want one repair", name, node["on_reject"])
			}
		}
		eval := nodes["swarm-evaluate"]
		if p := eval["prompt"].(string); strings.Contains(p, "eval_verdict") || strings.Contains(p, "NEEDS_FOLLOWUP") {
			t.Errorf("swarm-evaluate still asks the model for a verdict:\n%s", p)
		}
		if !reflect.DeepEqual(eval["outputs"], []any{"coverage", "gaps"}) {
			t.Errorf("swarm-evaluate outputs %v, want [coverage gaps]", eval["outputs"])
		}
	}
}

// Queen reads each forager's verdict, the runner's tally and its ∇ line in
// both prompts — with the Queen persona and with the inlined fallback — and
// returns one JSON object whose verdict her accept: holds to the plurality
// unless she explains the departure.
func TestGenerateWorkflow_QueenReadsTallyAndNabla(t *testing.T) {
	persona := Forager{Name: "queen", Title: "Queen", Description: "x", Body: "QUEEN PERSONA", Archetype: ArchetypeSynthesizer}
	for _, synth := range []Forager{{}, persona} {
		queen := swarmNodes(t, WorkflowOptions{Name: "n", Evaluate: true, Synthesizer: synth})["queen"]
		p := queen["prompt"].(string)
		for _, tok := range []string{"{verdict.forager:optimist}", "{verdict.forager:skeptic}", "{tally}", "{nabla.fired}", "{diversity}",
			"{gaps}", "{followup_findings}", "{gaps_unfollowed}", "report", "dissent_from_plurality"} {
			if !strings.Contains(p, tok) {
				t.Errorf("queen prompt (persona %q) lacks %s", synth.Name, tok)
			}
		}
		if strings.Contains(p, "{verdict.forager:dreamer}") {
			t.Errorf("queen prompt reads the dreamer, which runs after her")
		}
		outs, _ := queen["outputs"].([]any)
		if len(outs) == 0 || outs[0] != "report" {
			t.Errorf("queen outputs %v, want report first", outs)
		}
		rule := fmt.Sprintf("%[1]s == '' or outputs.verdict == %[1]s or (outputs.verdict == 'abstain' and %[2]s >= %[3]s) or len(outputs.dissent_from_plurality) >= %[4]d",
			workflow.TallyPluralityKey, workflow.TallyAbstentionsKey, workflow.TallyVotesKey, MinDissentChars)
		var item *workflow.AcceptItem
		items := workflow.AcceptItems(queen)
		for i := range items {
			if items[i].Predicate == rule {
				item = &items[i]
			}
		}
		if item == nil {
			t.Fatalf("queen accept has no plurality coherence rule %q:\n%+v", rule, items)
		}
		for _, tok := range []string{"{" + workflow.TallyPluralityKey + "}", "{outputs.verdict}", "{" + workflow.TallyLineKey + "}", "dissent_from_plurality"} {
			if !strings.Contains(item.Reason, tok) {
				t.Errorf("the rule's reason lacks %s: %q", tok, item.Reason)
			}
		}
	}
}

// The evaluator and Queen join settled, so a lens that failed or was
// rejected does not stop synthesis: they read a line saying so. Nothing
// else does. The evaluator's state_updates leave a line saying no
// follow-up finished, which the fan overwrites when it completes.
func TestGenerateWorkflow_EvaluatorAndQueenJoinSettled(t *testing.T) {
	for _, eval := range []bool{false, true} {
		nodes := swarmNodes(t, WorkflowOptions{Name: "n", Evaluate: eval, Scope: true})
		for name, node := range nodes {
			want := name == "queen" || name == "swarm-evaluate"
			if got := node["join"] == workflow.JoinSettled; got != want {
				t.Errorf("eval %v: %s join %v, want settled = %v", eval, name, node["join"], want)
			}
		}
		if !eval {
			continue
		}
		updates, _ := nodes["swarm-evaluate"]["state_updates"].(map[string]any)
		if _, ok := updates["gaps_unfollowed"]; ok {
			t.Errorf("swarm-evaluate writes gaps_unfollowed, which the engine writes from its gaps")
		}
		if f, _ := updates["followup_findings"].(string); !strings.HasPrefix(f, "(none:") {
			t.Errorf("swarm-evaluate leaves followup_findings %q, want a line saying none finished", f)
		}
	}
}

// A comma-separated Model mixes models across the lenses: in name order,
// the lenses take the models in turn, the follow-up fan takes the first,
// and the synthesizer's nodes keep their own pin. One model pins every
// lens, and a list with an empty entry is refused.
func TestGenerateWorkflow_MixedLensModels(t *testing.T) {
	swarm := []Forager{
		{Name: "skeptic", Description: "x", Body: "y"},
		{Name: "architect", Description: "x", Body: "y"},
		{Name: "pragmatist", Description: "x", Body: "y"},
	}
	var names []string
	for _, f := range swarm {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	const synth = "qwen3.6:35b-a3b-q4_K_M"
	for _, models := range [][]string{{"qwen3.5:4b", "ministral-3:8b"}, {"ministral-3:8b"}} {
		yamlText, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "n", Model: strings.Join(models, ", "), SynthesizerModel: synth, Evaluate: true, Scope: true})
		if err != nil {
			t.Fatal(err)
		}
		defn, err := workflow.LoadYAMLString(yamlText)
		if err != nil {
			t.Fatal(err)
		}
		nodes := defn["nodes"].(map[string]any)
		model := func(n string) (any, any) {
			node := nodes[n].(map[string]any)
			return node["model"], node["tier"]
		}
		for i, n := range names {
			if m, tier := model("forager-" + n); m != models[i%len(models)] || tier != nil {
				t.Errorf("%v: forager-%s model %v tier %v, want %s", models, n, m, tier, models[i%len(models)])
			}
		}
		if m, _ := model("swarm-followup"); m != models[0] {
			t.Errorf("%v: swarm-followup model %v, want the first, %s", models, m, models[0])
		}
		for _, n := range []string{"queen", "scope", "swarm-evaluate"} {
			if m, _ := model(n); m != synth {
				t.Errorf("%v: %s model %v, want the synthesizer's %s", models, n, m, synth)
			}
		}
	}
	for _, bad := range []string{"qwen3.5:4b,,ministral-3:8b", "qwen3.5:4b,", " , "} {
		if _, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "n", Model: bad}); err == nil {
			t.Errorf("model list %q generated; want it refused for its empty entry", bad)
		}
	}
}

// ForagerReasoning sets `reasoning:` on every lens node — the foragers and
// swarm-followup — and SynthesizerReasoning on scope, swarm-evaluate and
// Queen; an empty level writes no key. A level outside
// models.ReasoningLevels is refused.
func TestGenerateWorkflow_ReasoningLevels(t *testing.T) {
	swarm := []Forager{
		{Name: "skeptic", Description: "x", Body: "y"},
		{Name: "architect", Description: "x", Body: "y"},
	}
	lensNodes := []string{"forager-skeptic", "forager-architect", "swarm-followup"}
	synthNodes := []string{"scope", "swarm-evaluate", "queen"}
	for _, c := range []struct{ lens, synth string }{{"none", "none"}, {"low", ""}, {"", "high"}, {"", ""}} {
		yamlText, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "n", Evaluate: true, Scope: true, ForagerReasoning: c.lens, SynthesizerReasoning: c.synth})
		if err != nil {
			t.Fatal(err)
		}
		defn, err := workflow.LoadYAMLString(yamlText)
		if err != nil {
			t.Fatal(err)
		}
		if err := workflow.CheckReasoning(defn); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
		nodes := defn["nodes"].(map[string]any)
		for _, set := range []struct {
			names []string
			level string
		}{{lensNodes, c.lens}, {synthNodes, c.synth}} {
			for _, n := range set.names {
				got, ok := nodes[n].(map[string]any)["reasoning"]
				if (set.level == "") == ok || (ok && got != set.level) {
					t.Errorf("%+v: %s reasoning %v (set %v), want %q", c, n, got, ok, set.level)
				}
			}
		}
	}
	for _, bad := range []WorkflowOptions{{Name: "n", ForagerReasoning: "think"}, {Name: "n", SynthesizerReasoning: "max"}} {
		if _, err := GenerateWorkflow(swarm, bad); err == nil {
			t.Errorf("%+v generated; want the level refused", bad)
		}
	}
}

// A persona body is inlined into its node's prompt, where the engine and the
// runner fill every token they know. So no shipped persona holds one: a
// token in the prose, such as {verdict.forager:<name>}, would be filled with
// this run's data.
func TestPersonaBodiesHoldNoPromptTokens(t *testing.T) {
	all, err := Load(filepath.Join("..", "..", "foragers"))
	if err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`\{[a-z_][a-z0-9_]*(?:[.:][a-z0-9_<>-]+)*\}`)
	for _, f := range all {
		if m := token.FindAllString(f.Body, -1); len(m) > 0 {
			t.Errorf("%s's persona body holds %v, which a prompt would fill", f.Name, m)
		}
	}
}
