package foragers

import (
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

func shippedRoster(t *testing.T) []Forager {
	t.Helper()
	all, err := Load(filepath.Join("..", "..", "foragers"))
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// bodySections splits a body at its "## § N" lines, independently of
// splitSections: section number → its text, marker line included.
func bodySections(body string) map[int]string {
	out := map[int]string{}
	cur := -1
	for _, line := range strings.SplitAfter(body, "\n") {
		if rest, ok := strings.CutPrefix(line, "## § "); ok {
			digits := rest[:len(rest)-len(strings.TrimLeft(rest, "0123456789"))]
			if n, err := strconv.Atoi(digits); err == nil {
				cur = n
			}
		}
		if cur >= 0 {
			out[cur] += line
		}
	}
	return out
}

// refPattern finds "§ N" and "§N" references, and a dotted "§2.10".
var refPattern = regexp.MustCompile(`§ ?(\d+)(\.\d+)?`)

// The lean profile keeps sections 1, 2, 4, 5 and 7 of every sectioned
// persona whole and leaves the rest out, and every section reference in
// what it renders names a section marker line the text holds.
func TestRenderPersona_LeanKeepsTheContract(t *testing.T) {
	n := 0
	for _, w := range shippedRoster(t) {
		secs := bodySections(w.Body)
		if len(secs) == 0 {
			continue
		}
		n++
		got, ok := renderPersona(w.Body, LeanSections)
		if !ok {
			t.Fatalf("%s has sections and did not render them", w.Name)
		}
		for num, text := range secs {
			kept := slices.Contains(LeanSections, num)
			marker, body, _ := strings.Cut(text, "\n")
			switch {
			case kept && !strings.Contains(got, text):
				t.Errorf("%s: lean lacks § %d whole", w.Name, num)
			case !kept && strings.TrimSpace(body) != "" && strings.Contains(got, strings.TrimSpace(body)):
				t.Errorf("%s: lean holds the text of § %d (%s)", w.Name, num, marker)
			}
		}
		for _, m := range refPattern.FindAllStringSubmatch(got, -1) {
			if m[2] != "" {
				continue
			}
			num, _ := strconv.Atoi(m[1])
			marker, _, _ := strings.Cut(secs[num], "\n")
			if marker == "" || !strings.Contains(got, marker) {
				t.Errorf("%s: lean refers to § %d and holds no marker line for it", w.Name, num)
			}
		}
		if len(got) >= len(w.Body) {
			t.Errorf("%s: lean is %d bytes, the body %d", w.Name, len(got), len(w.Body))
		}
	}
	if n == 0 {
		t.Fatal("no shipped persona has sections")
	}
}

// A section left out that a kept one refers to is written as its marker line
// and a note, so the reference lands; one nothing refers to is dropped.
func TestRenderPersona_LeftOutSectionReferredTo(t *testing.T) {
	body := "Intro.\n\n## § 1 — Contract\n\nShape per § 3 below; see RFC 793 §2.10.\n\n## § 2 — Rubric\n\nr\n\n## § 3 — Example\n\nexample text\n\n## § 4 — Lore\n\nlore text\n"
	got, ok := renderPersona(body, []int{1})
	if !ok {
		t.Fatal("not rendered")
	}
	if !strings.Contains(got, "## § 3 — Example"+leftOutNote) || strings.Contains(got, "example text") {
		t.Errorf("§ 3, referred to by § 1, is not its marker and note:\n%s", got)
	}
	if strings.Contains(got, "## § 2") || strings.Contains(got, "## § 4") {
		t.Errorf("a section nothing refers to is in the text:\n%s", got)
	}
	if !strings.HasPrefix(got, "Intro.") {
		t.Errorf("the text before § 1 is gone:\n%s", got)
	}
}

// A persona with no section markers renders whole under lean, and
// generation says so.
func TestGenerateWorkflow_UnsectionedPersonaWarns(t *testing.T) {
	var warned []string
	yamlText, err := GenerateWorkflow([]Forager{{Name: "plain", Description: "d", Body: "Plain persona body."}},
		WorkflowOptions{Name: "n", PersonaProfile: ProfileLean, Warn: func(s string) { warned = append(warned, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "plain") {
		t.Errorf("warnings %q, want one naming plain", warned)
	}
	if !strings.Contains(yamlText, "Plain persona body.") {
		t.Error("the unsectioned persona is not in its prompt")
	}
}

// generatedPrompts generates a swarm from the shipped roster and returns
// each node's prompt.
func generatedPrompts(t *testing.T, preset string, opts WorkflowOptions) (map[string]string, []Forager) {
	t.Helper()
	all := shippedRoster(t)
	swarm, err := Filter(all, []string{preset})
	if err != nil {
		t.Fatal(err)
	}
	opts.Synthesizer, _ = ByName(all, "queen")
	opts.Name = "n"
	yamlText, err := GenerateWorkflow(swarm, opts)
	if err != nil {
		t.Fatal(err)
	}
	defn, err := workflow.LoadYAMLString(yamlText)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.CheckNodeFields(defn); err != nil {
		t.Fatalf("generated workflow: %v", err)
	}
	out := map[string]string{}
	for name, raw := range defn["nodes"].(map[string]any) {
		node := raw.(map[string]any)
		p, _ := node["prompt"].(string)
		if p == "" {
			p, _ = node["prompt_template"].(string)
		}
		out[name] = p
	}
	return out, swarm
}

// Each persona's counter-bias clause is in its prompt under the heading its
// prose refers to as §behavioral_floor, in both profiles. Skeptic's, which
// its frontmatter conditions on Optimist's absence, is stated as applying
// when Optimist is not in the swarm and as not applying when it is.
func TestGenerateWorkflow_CounterBiasResolved(t *testing.T) {
	for _, profile := range []string{ProfileFull, ProfileLean} {
		for _, preset := range []string{"minimal", "balanced"} {
			prompts, swarm := generatedPrompts(t, preset, WorkflowOptions{PersonaProfile: profile, Evaluate: true})
			in := map[string]bool{}
			for _, w := range swarm {
				in[w.Name] = true
			}
			for _, w := range swarm {
				p := prompts["forager-"+w.Name]
				if strings.Contains(p, "§behavioral_floor") || strings.Contains(p, "behavioral_floor.") {
					if !strings.Contains(p, "## § behavioral_floor") {
						t.Errorf("%s/%s: %s refers to its behavioral floor, which its prompt lacks", profile, preset, w.Name)
					}
				}
				switch {
				case w.CounterBias == "":
				case w.CounterBiasWhenAbsent == "":
					if !strings.Contains(p, w.CounterBias) {
						t.Errorf("%s/%s: %s's prompt lacks its counter-bias clause", profile, preset, w.Name)
					}
				case in[w.CounterBiasWhenAbsent]:
					if strings.Contains(p, w.CounterBias) || !strings.Contains(p, "does not apply") {
						t.Errorf("%s/%s: %s's clause is conditioned on %s's absence, and %s is present", profile, preset, w.Name, w.CounterBiasWhenAbsent, w.CounterBiasWhenAbsent)
					}
				default:
					if !strings.Contains(p, "so this clause applies: "+w.CounterBias) {
						t.Errorf("%s/%s: %s's clause applies without %s, and its prompt does not say so", profile, preset, w.Name, w.CounterBiasWhenAbsent)
					}
				}
			}
		}
	}
	skeptic, ok := ByName(shippedRoster(t), "skeptic")
	if !ok || skeptic.CounterBiasWhenAbsent != "optimist" {
		t.Errorf("skeptic's counter_bias_when_absent = %q, want optimist", skeptic.CounterBiasWhenAbsent)
	}
}

// resonatesLine is the comma-separated list that follows lead in a prompt,
// up to its full stop.
func resonatesLine(prompt, lead string) []string {
	prompt = strings.Join(strings.Fields(prompt), " ")
	i := strings.Index(prompt, lead)
	if i < 0 {
		return nil
	}
	rest := prompt[i+len(lead):]
	rest = rest[:strings.Index(rest, ".")]
	var out []string
	for _, s := range strings.Split(rest, ",") {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

// A forager's prompt names its resonates partners in the swarm, with whom a
// ∇ can fire, apart from those outside it, with whom none can.
func TestGenerateWorkflow_ResonatesPartnersSplit(t *testing.T) {
	prompts, swarm := generatedPrompts(t, "minimal", WorkflowOptions{})
	in := map[string]bool{}
	for _, w := range swarm {
		in[w.Name] = true
	}
	for _, w := range swarm {
		var present, absent []string
		for _, b := range w.Bonds {
			if b.Kind != BondResonates {
				continue
			}
			if in[b.To] {
				present = append(present, b.To)
			} else {
				absent = append(absent, b.To)
			}
		}
		p := prompts["forager-"+w.Name]
		if got := resonatesLine(p, "bonded by `resonates` with:"); !reflect.DeepEqual(got, present) {
			t.Errorf("%s: partners in the swarm %v, want %v", w.Name, got, present)
		}
		if got := resonatesLine(p, "with whom no ∇ can fire:"); !reflect.DeepEqual(got, absent) {
			t.Errorf("%s: partners outside the swarm %v, want %v", w.Name, got, absent)
		}
	}
}

// The spec's axis vocabulary (swarm.md § Axis ownership), less CDE-encode.
var specAxes = map[string][]string{
	"wasp": {"k", "k_execution", "E", "I", "T", "F"},
	"cde":  {"detect", "decompose", "execute"},
	"mss":  {"def", "gua", "asm", "unk"},
}

// Under lean, Queen and the evaluator read the ledger, the tally and the ∇
// line in place of every full verdict, and are told the axes no lens owns;
// the foragers carry no Jungian or IFS line. Under full, Queen and the
// evaluator read each forager's verdict token and no ledger. Queen reads the
// diversity line under both, since the ledger names no model.
func TestGenerateWorkflow_ProfilesQueenInput(t *testing.T) {
	for _, profile := range []string{ProfileFull, ProfileLean} {
		prompts, swarm := generatedPrompts(t, "balanced", WorkflowOptions{PersonaProfile: profile, Evaluate: true})
		var uncovered []string
		for _, fam := range []string{"wasp", "cde", "mss"} {
			for _, v := range specAxes[fam] {
				owned := false
				for _, w := range swarm {
					c := map[string]string{"wasp": w.Coverage.Wasp, "cde": w.Coverage.Cde, "mss": w.Coverage.Mss}
					owned = owned || c[fam] == v
				}
				if !owned {
					uncovered = append(uncovered, strings.ToUpper(fam)+"-"+v)
				}
			}
		}
		lean := profile == ProfileLean
		for _, node := range []string{"queen", "swarm-evaluate"} {
			p := prompts[node]
			if strings.Contains(p, "{swarm.ledger}") != lean {
				t.Errorf("%s %s: ledger token present = %v", profile, node, !lean)
			}
			for _, w := range swarm {
				if strings.Contains(p, "{verdict.forager:"+w.Name+"}") == lean {
					t.Errorf("%s %s: verdict token for %s present = %v", profile, node, w.Name, lean)
				}
			}
			if lean && !strings.Contains(p, "Axes no lens in this swarm owns: "+strings.Join(uncovered, ", ")+".") {
				t.Errorf("%s %s: uncovered axes line, want %v:\n%s", profile, node, uncovered, p)
			}
		}
		for _, tok := range []string{"{tally}", "{nabla.fired}", "{diversity}"} {
			if !strings.Contains(prompts["queen"], tok) {
				t.Errorf("%s queen lacks %s", profile, tok)
			}
		}
		for _, w := range swarm {
			p := prompts["forager-"+w.Name]
			if w.Jungian != "" && strings.Contains(p, "Jungian archetype:") == lean {
				t.Errorf("%s %s: Jungian line present = %v", profile, w.Name, lean)
			}
			if w.IFSRole != "" && strings.Contains(p, "IFS role:") == lean {
				t.Errorf("%s %s: IFS line present = %v", profile, w.Name, lean)
			}
		}
	}
}

// --persona-sections replaces the profile's sections: lean with § 3 keeps
// each worked example whole.
func TestGenerateWorkflow_PersonaSectionsOverride(t *testing.T) {
	prompts, swarm := generatedPrompts(t, "minimal", WorkflowOptions{PersonaProfile: ProfileLean, PersonaSections: []int{1, 2, 3, 4, 5, 7}})
	for _, w := range swarm {
		s3 := bodySections(w.Body)[3]
		if s3 == "" {
			continue
		}
		if !strings.Contains(prompts["forager-"+w.Name], strings.TrimSpace(s3)) {
			t.Errorf("%s: § 3 is not in its prompt with sections 1,2,3,4,5,7", w.Name)
		}
	}
}

func TestGenerateWorkflow_UnknownProfile(t *testing.T) {
	_, err := GenerateWorkflow([]Forager{{Name: "a", Body: "b"}}, WorkflowOptions{PersonaProfile: "tiny"})
	if err == nil || !strings.Contains(err.Error(), "tiny") {
		t.Errorf("err = %v, want the unknown profile named", err)
	}
}

func TestParsePersonaSections(t *testing.T) {
	good := map[string][]int{"1,2,4,5,7": {1, 2, 4, 5, 7}, "3, 1,3": {1, 3}, "8": {8}}
	for in, want := range good {
		got, err := ParsePersonaSections(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParsePersonaSections(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "a", "1,,2", "-1"} {
		if _, err := ParsePersonaSections(in); err == nil {
			t.Errorf("ParsePersonaSections(%q) passed", in)
		}
	}
}
