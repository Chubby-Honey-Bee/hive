package foragers

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/schema"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"gopkg.in/yaml.v3"
)

// frontmatterOf reads a persona file's frontmatter as a YAML node, apart
// from the loader, so the tests below grade the loader against the file.
func frontmatterOf(t *testing.T, path string) *yaml.Node {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(raw), "---\n", 3)
	if len(parts) < 3 {
		t.Fatalf("%s: no frontmatter", path)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(parts[1]), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

func keysOf(m *yaml.Node) []string {
	var out []string
	for i := 0; m != nil && i+1 < len(m.Content); i += 2 {
		out = append(out, m.Content[i].Value)
	}
	return out
}

func propertyNames(s *schema.Schema) []string {
	var out []string
	for _, p := range s.Properties {
		out = append(out, p.Name)
	}
	return out
}

// Every shipped persona that declares output_schema loads it in file order,
// and each length_caps max_items becomes that property's maxItems.
func TestLoad_ParsesOutputContract(t *testing.T) {
	all, err := Load("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	withSchema := 0
	for _, w := range all {
		fm := frontmatterOf(t, filepath.Join("..", "..", "foragers", w.File))
		raw := schema.MapValue(fm, "output_schema")
		if raw == nil {
			if w.OutputSchema != nil {
				t.Errorf("%s: a schema from a file that declares none", w.Name)
			}
			continue
		}
		withSchema++
		if w.ContractErr != nil || w.OutputSchema == nil {
			t.Errorf("%s: schema %v, error %v", w.Name, w.OutputSchema, w.ContractErr)
			continue
		}
		if got, want := propertyNames(w.OutputSchema), keysOf(schema.MapValue(raw, "properties")); !slices.Equal(got, want) {
			t.Errorf("%s: properties %v, want the file's order %v", w.Name, got, want)
		}
		caps := schema.MapValue(fm, "length_caps")
		for _, field := range keysOf(caps) {
			var want int
			mi := schema.MapValue(schema.MapValue(caps, field), "max_items")
			prop := w.OutputSchema.Property(field)
			if mi == nil {
				if prop.MaxItems != nil {
					t.Errorf("%s.%s: maxItems %d with no max_items cap", w.Name, field, *prop.MaxItems)
				}
				continue
			}
			if err := mi.Decode(&want); err != nil {
				t.Fatal(err)
			}
			if prop.MaxItems == nil || *prop.MaxItems != want {
				t.Errorf("%s.%s: maxItems %v, want max_items %d", w.Name, field, prop.MaxItems, want)
			}
		}
	}
	if withSchema == 0 {
		t.Fatal("no shipped persona declares output_schema, so nothing was checked")
	}
}

// A persona whose contract does not parse still loads, so it is not an
// unknown forager, and generation refuses it by name.
func TestLoad_BadContractRefusesGeneration(t *testing.T) {
	dir := t.TempDir()
	good := "---\nname: good\noutput_schema:\n  type: object\n  properties:\n    verdict: {enum: [support, oppose]}\n---\nbody\n"
	bad := "---\nname: bad\noutput_schema:\n  type: object\n  properties:\n    verdict: {enum: [support, oppose]}\nlength_caps:\n  key_points: {max_items: 3}\n---\nbody\n"
	for name, text := range map[string]string{"good.md": good, "bad.md": bad} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	all, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, ok := ByName(all, "bad")
	if !ok || b.ContractErr == nil || !strings.Contains(b.ContractErr.Error(), "length_caps.key_points") {
		t.Fatalf("bad: loaded=%v err=%v, want it loaded with an error naming length_caps.key_points", ok, b.ContractErr)
	}
	_, err = GenerateWorkflow(all, WorkflowOptions{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), `forager "bad"`) {
		t.Errorf("GenerateWorkflow err = %v, want a refusal naming bad", err)
	}
	g, _ := ByName(all, "good")
	if _, err := GenerateWorkflow([]Forager{g}, WorkflowOptions{Name: "x"}); err != nil {
		t.Errorf("the good persona alone: %v", err)
	}
}

// The generated swarm puts each persona's schema on its node, verdict and
// recommendation last and the rest in the persona's order, with one repair;
// Queen's is hers, in the same order. The workflow passes the engine's own
// schema check, so every declared output is a property.
func TestGenerateWorkflow_SendsPersonaSchemas(t *testing.T) {
	all, err := Load("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	swarm, err := Filter(all, []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	queen, _ := ByName(all, "queen")
	text, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "s", Evaluate: true, Scope: true, Synthesizer: queen})
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := workflow.OutputSchemas(text)
	if err != nil {
		t.Fatalf("the generated workflow's schemas: %v", err)
	}
	defn, _ := workflow.LoadYAMLString(text)
	wantOrder := func(w Forager) []string {
		props := keysOf(schema.MapValue(schema.MapValue(frontmatterOf(t, filepath.Join("..", "..", "foragers", w.File)), "output_schema"), "properties"))
		var out []string
		for _, p := range props {
			if !slices.Contains(decisionFields, p) {
				out = append(out, p)
			}
		}
		for _, d := range decisionFields {
			if slices.Contains(props, d) {
				out = append(out, d)
			}
		}
		return out
	}
	lenses, _ := SplitByArchetype(swarm)
	check := func(node string, w Forager) {
		s := schemas[node]
		// Every forager and Queen gets one repair, schema or not.
		if repair := workflow.NodeOnRejectBlock(defn, node); repair == nil || repair["max_repair_iterations"] != 1 {
			t.Errorf("%s: on_reject %v, want one attempt", node, repair)
		}
		if w.OutputSchema == nil {
			if s != nil {
				t.Errorf("%s: schema %v, for a persona that declares no schema", node, s)
			}
			return
		}
		if s == nil {
			t.Errorf("%s: no output_schema", node)
			return
		}
		if got, want := propertyNames(s), wantOrder(w); !slices.Equal(got, want) {
			t.Errorf("%s: properties %v, want %v", node, got, want)
		}
	}
	for _, w := range lenses {
		check("forager-"+w.Name, w)
	}
	check("queen", queen)
	for _, node := range []string{"scope", "swarm-evaluate", "swarm-followup"} {
		if schemas[node] == nil {
			t.Errorf("%s: no output_schema", node)
		}
	}
	path := filepath.Join(t.TempDir(), "swarm.yaml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, _, err := workflow.Validate(path)
	if err != nil || len(errs) > 0 {
		t.Errorf("Validate: %v %v", errs, err)
	}
}

// Without a Queen persona the fallback prompt asks for the same object, and
// its node carries the schema that says so, report first.
func TestGenerateWorkflow_FallbackQueenSchema(t *testing.T) {
	text, err := GenerateWorkflow([]Forager{{Name: "optimist", Description: "x", Body: "y"}}, WorkflowOptions{Name: "s"})
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := workflow.OutputSchemas(text)
	if err != nil {
		t.Fatal(err)
	}
	s := schemas["queen"]
	if s == nil {
		t.Fatal("the fallback queen has no schema")
	}
	names := propertyNames(s)
	if names[0] != "report" || !slices.Equal(names[len(names)-len(decisionFields):], decisionFields) {
		t.Errorf("fallback queen properties %v, want report first and %v last", names, decisionFields)
	}
	defn, _ := workflow.LoadYAMLString(text)
	prompt, _ := defn["nodes"].(map[string]any)["queen"].(map[string]any)["prompt"].(string)
	for _, p := range names {
		if !strings.Contains(prompt, `"`+p+`"`) {
			t.Errorf("the fallback prompt never names %q, which its schema requires the reply to hold", p)
		}
	}
}

// grammarOrder is the order llama.cpp's json-schema-to-grammar writes an
// object's properties in: the required ones in declared order, then the
// optional ones in declared order (_build_object_rule).
func grammarOrder(s *schema.Schema) []string {
	var required, optional []string
	for _, p := range s.Properties {
		if slices.Contains(s.Required, p.Name) {
			required = append(required, p.Name)
		} else {
			optional = append(optional, p.Name)
		}
	}
	return append(required, optional...)
}

// Under a grammar, Queen writes her whole object in its declared order,
// report first and the decision last: every property is required, hers and
// the fallback prompt's. A lens writes every required reason before its
// decision; its optional fields come after it.
func TestGeneratedSchemasDecodeReasonsFirst(t *testing.T) {
	all, err := Load("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	swarm, err := Filter(all, []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	queen, _ := ByName(all, "queen")
	for _, synth := range []Forager{queen, {}} {
		text, err := GenerateWorkflow(swarm, WorkflowOptions{Name: "s", Synthesizer: synth})
		if err != nil {
			t.Fatal(err)
		}
		schemas, err := workflow.OutputSchemas(text)
		if err != nil {
			t.Fatal(err)
		}
		q := schemas["queen"]
		if got, want := grammarOrder(q), propertyNames(q); !slices.Equal(got, want) || want[0] != "report" || !slices.Equal(want[len(want)-len(decisionFields):], decisionFields) {
			t.Errorf("queen (persona %q): a grammar writes %v; want her declared %v, report first and %v last", synth.Name, got, want, decisionFields)
		}
		for node, s := range schemas {
			if !strings.HasPrefix(node, "forager-") {
				continue
			}
			order := grammarOrder(s)
			first := slices.Index(order, decisionFields[0])
			for _, r := range s.Required {
				if !slices.Contains(decisionFields, r) && slices.Index(order, r) > first {
					t.Errorf("%s: a grammar writes required %q after %q: %v", node, r, decisionFields[0], order)
				}
			}
		}
	}
}
