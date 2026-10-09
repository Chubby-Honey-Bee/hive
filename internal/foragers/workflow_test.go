package foragers

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGenerateWorkflow_EmptySwarmErrors(t *testing.T) {
	_, err := GenerateWorkflow(nil, WorkflowOptions{})
	if err == nil {
		t.Fatalf("expected error for empty swarm")
	}
}

func TestGenerateWorkflow_ProducesValidYAML(t *testing.T) {
	swarm := []Forager{
		{Name: "optimist", Title: "The Optimist", Description: "Sees upside.", Body: "You are The Optimist.\n"},
		{Name: "skeptic", Title: "The Skeptic", Description: "Sees risk.", Body: "You are The Skeptic.\n"},
	}
	yamlText, err := GenerateWorkflow(swarm, WorkflowOptions{
		Name: "test-swarm", Model: "sonnet", SynthesizerModel: "opus",
	})
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(yamlText), &doc); err != nil {
		t.Fatalf("unmarshal generated YAML: %v\nYAML:\n%s", err, yamlText)
	}
	if doc["name"] != "test-swarm" {
		t.Errorf("name: want test-swarm, got %v", doc["name"])
	}
	nodes, ok := doc["nodes"].(map[string]any)
	if !ok {
		t.Fatalf("nodes not a map: %T", doc["nodes"])
	}
	if _, ok := nodes["forager-optimist"]; !ok {
		t.Errorf("missing forager-optimist node")
	}
	if _, ok := nodes["forager-skeptic"]; !ok {
		t.Errorf("missing forager-skeptic node")
	}
	if _, ok := nodes["queen"]; !ok {
		t.Errorf("missing queen (synthesizer) node")
	}
}

func TestGenerateWorkflow_RejectsBondCycle(t *testing.T) {
	swarm := []Forager{
		{Name: "a", Title: "A", Description: "x", Body: "y",
			Bonds: []Bond{{To: "b", Kind: BondCites, Weight: 1}}},
		{Name: "b", Title: "B", Description: "x", Body: "y",
			Bonds: []Bond{{To: "a", Kind: BondCites, Weight: 1}}},
	}
	_, err := GenerateWorkflow(swarm, WorkflowOptions{})
	if err == nil {
		t.Fatalf("expected cycle to be rejected")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should name cycle: %v", err)
	}
}

func TestGenerateWorkflow_RejectsBondToMissingForager(t *testing.T) {
	swarm := []Forager{
		{Name: "a", Title: "A", Description: "x", Body: "y",
			Bonds: []Bond{{To: "ghost", Kind: BondCites, Weight: 1}}},
	}
	_, err := GenerateWorkflow(swarm, WorkflowOptions{})
	if err == nil {
		t.Fatalf("expected missing-forager reference to be rejected")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the missing forager: %v", err)
	}
}

func TestGenerateWorkflow_BondsRenderAsEdges(t *testing.T) {
	swarm := []Forager{
		{Name: "optimist", Title: "The Optimist", Description: "x", Body: "y"},
		{Name: "contrarian", Title: "The Contrarian", Description: "x", Body: "y",
			Bonds: []Bond{{To: "optimist", Kind: BondContradicts, Weight: 1}}},
	}
	yamlText, err := GenerateWorkflow(swarm, WorkflowOptions{})
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	if !strings.Contains(yamlText, "from: forager-optimist, to: forager-contrarian") {
		t.Errorf("expected upstream→downstream edge, got:\n%s", yamlText)
	}
	if !strings.Contains(yamlText, "construct the strongest") {
		t.Errorf("contradicts bond should wrap downstream prompt with inversion preamble, got:\n%s", yamlText)
	}
	if !strings.Contains(yamlText, "{comb.forager:optimist}") {
		t.Errorf("downstream prompt should substitute upstream Comb vantage, got:\n%s", yamlText)
	}
}

func TestGenerateWorkflow_EmbedsLensAndPersona(t *testing.T) {
	swarm := []Forager{
		{Name: "empiricist", Title: "The Empiricist",
			Description: "Demands data.", Body: "RUNS THE NUMBERS\n"},
	}
	yamlText, err := GenerateWorkflow(swarm, WorkflowOptions{})
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	if !strings.Contains(yamlText, "Demands data.") {
		t.Errorf("lens (description) should appear in prompt, got:\n%s", yamlText)
	}
	if !strings.Contains(yamlText, "RUNS THE NUMBERS") {
		t.Errorf("persona body should appear in prompt, got:\n%s", yamlText)
	}
}

func TestWithDefaults_FillsEmptyFields(t *testing.T) {
	out := WorkflowOptions{}.WithDefaults()
	// As of 2026-05-06 the defaults emit tier: roles instead of
	// model: literals so chb ask respects --budget-mode. Empty
	// Model/SynthesizerModel remain empty; the corresponding tier
	// field gets populated.
	if out.Model != "" {
		t.Errorf("default model: want empty (tier system kicks in), got %q", out.Model)
	}
	if out.SynthesizerModel != "" {
		t.Errorf("default synthesizer-model: want empty (tier system kicks in), got %q", out.SynthesizerModel)
	}
	if out.ForagerTier != "synthesist" {
		t.Errorf("default ForagerTier: want synthesist, got %q", out.ForagerTier)
	}
	if out.SynthesizerTier != "planner" {
		t.Errorf("default SynthesizerTier: want planner, got %q", out.SynthesizerTier)
	}
	if !strings.HasPrefix(out.Name, "swarm-of-foragers-") {
		t.Errorf("default name should be dated, got %q", out.Name)
	}
}

// TestWithDefaults_ExplicitModelSkipsTier: an explicit Model (a caller
// pinning sonnet, say) suppresses tier emission, so WithDefaults leaves
// ForagerTier empty.
func TestWithDefaults_ExplicitModelSkipsTier(t *testing.T) {
	out := WorkflowOptions{Model: "sonnet"}.WithDefaults()
	if out.Model != "sonnet" {
		t.Errorf("explicit Model should be preserved; got %q", out.Model)
	}
	if out.ForagerTier != "" {
		t.Errorf("ForagerTier should stay empty when Model is set; got %q", out.ForagerTier)
	}
}

func TestWithDefaults_PreservesExplicitFields(t *testing.T) {
	in := WorkflowOptions{Name: "custom", Model: "haiku", SynthesizerModel: "opus"}
	out := in.WithDefaults()
	if out.Name != "custom" || out.Model != "haiku" || out.SynthesizerModel != "opus" {
		t.Errorf("explicit fields should not be overwritten: %+v", out)
	}
}

func TestGenerateWorkflow_AcceptPredicatesPresent(t *testing.T) {
	swarm := []Forager{{Name: "skeptic", Title: "The Skeptic", Description: "x", Body: "y"}}
	yamlText, err := GenerateWorkflow(swarm, WorkflowOptions{})
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	if strings.Count(yamlText, "outputs.verdict ==") < 2 {
		t.Errorf("expected verdict accept predicate on both foragers and synthesizer, got:\n%s", yamlText)
	}
}

func TestGenerateWorkflow_EmptySwarm(t *testing.T) {
	_, err := GenerateWorkflow(nil, WorkflowOptions{})
	if err == nil {
		t.Fatal("expected error for empty swarm")
	}
}

func TestGenerateWorkflow_OnlyDreamers(t *testing.T) {
	// At least one lens forager is required.
	swarm := []Forager{
		{Name: "x", Archetype: "dreamer", Body: "body"},
	}
	_, err := GenerateWorkflow(swarm, WorkflowOptions{})
	if err == nil {
		t.Fatal("expected error for swarm with no lens foragers")
	}
	if !strings.Contains(err.Error(), "lens forager") {
		t.Errorf("error = %v; expected mention of lens forager", err)
	}
}

func TestGenerateWorkflow_HappyPath(t *testing.T) {
	swarm := []Forager{
		{Name: "optimist", Title: "Optimist", Archetype: "lens", Body: "be optimistic", Jungian: "magician"},
		{Name: "skeptic", Archetype: "lens", Body: "be skeptical"},
	}
	yaml, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "test", Model: "haiku"})
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	if !strings.Contains(yaml, "name: test") {
		t.Errorf("yaml missing name: test; got first 200 chars: %s", yaml[:min(200, len(yaml))])
	}
	if !strings.Contains(yaml, "forager-optimist") {
		t.Error("yaml missing forager-optimist node")
	}
	if !strings.Contains(yaml, "forager-skeptic") {
		t.Error("yaml missing forager-skeptic node")
	}
}

func TestGenerateWorkflow_WithBonds(t *testing.T) {
	swarm := []Forager{
		{Name: "optimist", Archetype: "lens", Body: "be optimistic"},
		{Name: "skeptic", Archetype: "lens", Body: "be skeptical",
			Bonds: []Bond{
				{To: "optimist", Kind: BondCites, Weight: 1.0},
			}},
	}
	yaml, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "t", Model: "haiku"})
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	if !strings.Contains(yaml, "forager-optimist") {
		t.Error("missing optimist node")
	}
}

func TestGenerateWorkflow_HumanGateEnabled(t *testing.T) {
	swarm := []Forager{
		{Name: "optimist", Archetype: "lens", Body: "go"},
	}
	yaml, err := GenerateWorkflow(swarm, WorkflowOptions{
		Name: "t", Model: "haiku", HumanGate: true,
	})
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	if !strings.Contains(yaml, "human") && !strings.Contains(yaml, "Human") {
		t.Errorf("expected human gate in yaml; got %s", yaml[:min(500, len(yaml))])
	}
}

func TestGenerateWorkflow_WithDreamers(t *testing.T) {
	swarm := []Forager{
		{Name: "optimist", Archetype: "lens", Body: "go"},
		{Name: "dreamer1", Archetype: "dreamer", Body: "ripen"},
	}
	yaml, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "t", Model: "haiku"})
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	if !strings.Contains(yaml, "dreamer1") {
		t.Errorf("dreamer node missing")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestFirstLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"single", "single"},
		{"first\nsecond", "first"},
		{"  spaced  \nrest", "spaced"},
		{"\nleading newline", ""},
		{"a\nb\nc", "a"},
	}
	for _, tc := range cases {
		if got := firstLine(tc.in); got != tc.want {
			t.Errorf("firstLine(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"x", "y", "x"},
		{"", "y", "y"},
		{"   ", "y", "y"},
		{"\t\n", "y", "y"},
		{"x", "", "x"},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := firstNonEmpty(tc.a, tc.b); got != tc.want {
			t.Errorf("firstNonEmpty(%q, %q) = %q; want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCapitalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"a", "A"},
		{"abc", "Abc"},
		{"ABC", "ABC"},
		{"x y z", "X y z"},
	}
	for _, tc := range cases {
		if got := capitalize(tc.in); got != tc.want {
			t.Errorf("capitalize(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestIndent(t *testing.T) {
	cases := []struct {
		s, prefix, want string
	}{
		{"", "  ", ""},
		{"line1", "  ", "  line1"},
		{"line1\nline2", "  ", "  line1\n  line2"},
		{"\n", "  ", "  "}, // TrimRight removes trailing \n → single empty line prefixed
		{"a\nb\nc", ">>", ">>a\n>>b\n>>c"},
	}
	for _, tc := range cases {
		if got := indent(tc.s, tc.prefix); got != tc.want {
			t.Errorf("indent(%q, %q) = %q; want %q", tc.s, tc.prefix, got, tc.want)
		}
	}
}

// capitalize upper-cases the first character, whatever its width, and keeps
// the rest whole.
func TestCapitalize_UpperCasesTheFirstCharacter(t *testing.T) {
	for in, want := range map[string]string{"queen": "Queen", "élan": "Élan", "": ""} {
		if got := capitalize(in); got != want {
			t.Errorf("capitalize(%q) = %q, want %q", in, got, want)
		}
	}
}
