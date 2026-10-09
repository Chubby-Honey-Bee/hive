package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/schema"
	"gopkg.in/yaml.v3"
)

// schemaYAML is a one-agent workflow whose node declares outputs and an
// output_schema listing its properties in a deliberately unsorted order.
const schemaYAML = `name: t
nodes:
  lens:
    type: agent
    prompt: go
    outputs: [verdict, key_points]
    accept:
      - "outputs.verdict != 'oppose'"
    output_schema:
      type: object
      required: [key_points, verdict]
      properties:
        key_points: {type: array, items: {type: string}, maxItems: 2}
        evidence: {type: array, items: {type: string}}
        verdict: {enum: [support, oppose, abstain]}
  fan:
    type: parallel_fan
    prompt_template: "{item}"
    outputs: [finding]
    output_schema:
      type: object
      required: [finding]
      properties:
        finding: {type: string}
edges:
  - {from: lens, to: fan}
`

func writeWF(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "wf.yaml")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// schemaPropertyOrder reads the property names of node's output_schema from
// the YAML text itself, in the order written.
func schemaPropertyOrder(t *testing.T, text, node string) []string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	props := schema.MapValue(schema.MapValue(schema.MapValue(schema.MapValue(doc.Content[0], "nodes"), node), "output_schema"), "properties")
	var out []string
	for i := 0; i+1 < len(props.Content); i += 2 {
		out = append(out, props.Content[i].Value)
	}
	return out
}

// Each malformed schema is refused by Validate, InitWorkflow and the
// dispatch parse, naming the node; the well-formed workflow passes all three.
func TestOutputSchema_RefusedAtLoad(t *testing.T) {
	bad := map[string]string{
		"unsupported keyword": strings.Replace(schemaYAML, "maxItems: 2", "pattern: x", 1),
		"not an object":       strings.Replace(schemaYAML, "      type: object\n      required: [key_points, verdict]", "      type: array", 1),
		"output not declared": strings.Replace(schemaYAML, "outputs: [verdict, key_points]", "outputs: [verdict, key_points, rationale]", 1),
		"on a decision node":  strings.Replace(schemaYAML, "  fan:\n    type: parallel_fan", "  fan:\n    type: decision\n    condition: \"x == 1\"\n    true_edge: lens\n    false_edge: lens", 1),
	}
	for name, text := range bad {
		t.Run(name, func(t *testing.T) {
			if text == schemaYAML {
				t.Fatal("the replacement did not apply")
			}
			path := writeWF(t, text)
			errs, _, err := Validate(path)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, "output_schema") }) {
				t.Errorf("Validate errors %v name no output_schema problem", errs)
			}
			store := newWFStore(t)
			if _, err := InitWorkflow(store.Workflows(), path, nil); err == nil || !strings.Contains(err.Error(), "output_schema") {
				t.Errorf("InitWorkflow err = %v, want an output_schema refusal", err)
			}
			var n int
			_ = store.ReadDB.QueryRow(`SELECT COUNT(*) FROM workflow_runs`).Scan(&n)
			if n != 0 {
				t.Errorf("%d runs created, want 0", n)
			}
		})
	}
	errs, _, err := Validate(writeWF(t, schemaYAML))
	if err != nil || slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, "output_schema") }) {
		t.Errorf("a well-formed schema: errs %v, err %v", errs, err)
	}
}

// The dispatch node carries the schema with its properties in the YAML's
// order, which a map would have sorted.
func TestOutputSchema_DispatchKeepsOrder(t *testing.T) {
	store := newWFStore(t)
	runID, err := InitWorkflow(store.Workflows(), writeWF(t, schemaYAML), nil)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := GetNextNodes(store.Workflows(), runID)
	if err != nil || len(nodes) != 1 || nodes[0].Node != "lens" {
		t.Fatalf("GetNextNodes = %+v, %v", nodes, err)
	}
	s := nodes[0].OutputSchema
	if s == nil {
		t.Fatal("no schema on the dispatch node")
	}
	b, _ := json.Marshal(s)
	dec := json.NewDecoder(bytes.NewReader(b))
	var raw struct {
		Properties json.RawMessage `json:"properties"`
	}
	if err := dec.Decode(&raw); err != nil {
		t.Fatal(err)
	}
	var got []string
	pd := json.NewDecoder(bytes.NewReader(raw.Properties))
	_, _ = pd.Token()
	for pd.More() {
		k, _ := pd.Token()
		got = append(got, k.(string))
		var skip json.RawMessage
		_ = pd.Decode(&skip)
	}
	want := schemaPropertyOrder(t, schemaYAML, "lens")
	sorted := slices.Sorted(slices.Values(want))
	if slices.Equal(want, sorted) {
		t.Fatal("the fixture's properties are already sorted, so order is not tested")
	}
	if !slices.Equal(got, want) {
		t.Errorf("serialized order %v, want the YAML's %v", got, want)
	}
}

// completeLens starts a run and completes the lens node with outputs.
func completeLens(t *testing.T, outputs map[string]any) (map[string]any, error) {
	t.Helper()
	store := newWFStore(t)
	runID, err := InitWorkflow(store.Workflows(), writeWF(t, schemaYAML), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GetNextNodes(store.Workflows(), runID); err != nil {
		t.Fatal(err)
	}
	cerr := CompleteNode(store.Workflows(), runID, "lens", outputs)
	run, _ := store.Workflows().GetWorkflowRun(runID)
	var state map[string]any
	_ = json.Unmarshal([]byte(run.StateJSON), &state)
	return state, cerr
}

// CompleteNode checks an agent node's outputs against its schema before its
// accept: predicates, and a violation is a rejection naming the path.
func TestOutputSchema_CompleteNodeChecksShape(t *testing.T) {
	cases := []struct {
		name    string
		outputs map[string]any
		// wantPath is the violation's path; "" means the outputs hold.
		wantPath string
	}{
		{"valid", map[string]any{"verdict": "support", "key_points": []any{"a"}}, ""},
		{"verdict outside the enum", map[string]any{"verdict": "maybe", "key_points": []any{}}, "$.verdict"},
		{"too many key points", map[string]any{"verdict": "support", "key_points": []any{"a", "b", "c"}}, "$.key_points"},
		{"missing key_points", map[string]any{"verdict": "support"}, "$"},
		{"prose with no JSON", map[string]any{"final_text": "bash: chb: command not found"}, "no JSON object"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, err := completeLens(t, c.outputs)
			var rej *AcceptRejection
			if c.wantPath == "" {
				if err != nil {
					t.Fatalf("CompleteNode: %v", err)
				}
				return
			}
			if !errors.As(err, &rej) || rej.Predicate != OutputSchemaPredicate || !strings.Contains(rej.EvaluatedValue, c.wantPath) {
				t.Fatalf("err = %v, want an output_schema rejection naming %s", err, c.wantPath)
			}
			// Nothing was merged: in particular, prose was not aliased onto
			// the first declared output.
			if _, ok := state["verdict"]; ok {
				t.Errorf("state holds verdict %v after a rejection", state["verdict"])
			}
		})
	}
}

// A schema violation is reported even when the accept: predicate would also
// fail; with the shape valid, the predicate still runs.
func TestOutputSchema_BeforeAccept(t *testing.T) {
	_, err := completeLens(t, map[string]any{"verdict": "oppose", "key_points": []any{1.0}})
	var rej *AcceptRejection
	if !errors.As(err, &rej) || rej.Predicate != OutputSchemaPredicate {
		t.Errorf("both broken: err = %v, want the schema's rejection first", err)
	}
	_, err = completeLens(t, map[string]any{"verdict": "oppose", "key_points": []any{"a"}})
	if !errors.As(err, &rej) || rej.Predicate == OutputSchemaPredicate || !strings.Contains(rej.Predicate, "verdict") {
		t.Errorf("shape valid, predicate false: err = %v, want the accept: rejection", err)
	}
}

// CompleteNode checks a fan's outputs as one reply: a caller outside the
// runner (chb workflow complete) checked no item.
// CompleteFanItems, which the runner uses after checking each item's reply,
// does not check the joined outputs again. Graded against the fan's own
// schema.
func TestOutputSchema_FanCompletion(t *testing.T) {
	schemas, err := OutputSchemas(schemaYAML)
	if err != nil {
		t.Fatal(err)
	}
	fanSchema := schemas["fan"]
	for _, outputs := range []map[string]any{
		{"finding": "## Item 1\n\n{\"finding\":\"a\"}"},
		{"final_text": "bash: chb: command not found"},
		{"finding": []any{"a", "b"}},
	} {
		wantRejected := len(CheckOutput(fanSchema, outputs)) > 0
		for _, c := range []struct {
			name     string
			complete func(Store, int64, string, map[string]any) error
			checks   bool
		}{
			{"CompleteNode", CompleteNode, true},
			{"CompleteFanItems", CompleteFanItems, false},
		} {
			store := newWFStore(t)
			runID, err := InitWorkflow(store.Workflows(), writeWF(t, schemaYAML), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := GetNextNodes(store.Workflows(), runID); err != nil {
				t.Fatal(err)
			}
			if err := CompleteNode(store.Workflows(), runID, "lens", map[string]any{"verdict": "support", "key_points": []any{}}); err != nil {
				t.Fatal(err)
			}
			if _, err := GetNextNodes(store.Workflows(), runID); err != nil {
				t.Fatal(err)
			}
			err = c.complete(store.Workflows(), runID, "fan", outputs)
			var rej *AcceptRejection
			rejected := errors.As(err, &rej) && rej.Predicate == OutputSchemaPredicate
			if rejected != (c.checks && wantRejected) {
				t.Errorf("%s(%v): err %v; want an output_schema rejection = %v", c.name, outputs, err, c.checks && wantRejected)
			}
		}
	}
}

// A schema'd node completed from outside the runner is checked, but chb sent
// no call, so it records no schema_enforcement.
func TestOutputSchema_CompletedOutsideTheRunnerRecordsNoEnforcement(t *testing.T) {
	store := newWFStore(t)
	runID, err := InitWorkflow(store.Workflows(), writeWF(t, schemaYAML), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GetNextNodes(store.Workflows(), runID); err != nil {
		t.Fatal(err)
	}
	if err := CompleteNode(store.Workflows(), runID, "lens", map[string]any{"verdict": "support", "key_points": []any{}}); err != nil {
		t.Fatal(err)
	}
	var status string
	var recorded bool
	if err := store.ReadDB.QueryRow(`SELECT status, schema_enforcement IS NOT NULL FROM workflow_node_states WHERE run_id=? AND node_name='lens'`, runID).Scan(&status, &recorded); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || recorded {
		t.Errorf("lens %s, schema_enforcement recorded %v; want completed with none recorded", status, recorded)
	}
}

// A schema is checked whatever it declares: a required name, or a declared
// output, that no property covers is refused even when the schema declares
// no properties at all, at the root or nested.
func TestOutputSchema_RefusedWithoutProperties(t *testing.T) {
	cases := map[string]struct{ schema, outputs, names string }{
		"required, no properties":        {"{type: object, required: [zzz]}", "[]", "zzz"},
		"declared output, no properties": {"{type: object}", "[verdict]", "verdict"},
		"nested required":                {"{type: object, properties: {f: {type: object, required: [q]}}}", "[f]", "q"},
	}
	for name, c := range cases {
		text := fmt.Sprintf("name: t\nnodes:\n  lens:\n    type: agent\n    prompt: go\n    outputs: %s\n    output_schema: %s\n", c.outputs, c.schema)
		path := writeWF(t, text)
		errs, _, err := Validate(path)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(errs, func(e string) bool {
			return strings.Contains(e, "output_schema") && strings.Contains(e, c.names)
		}) {
			t.Errorf("%s: Validate errors %v, want one naming output_schema and %s", name, errs, c.names)
		}
		if _, err := InitWorkflow(newWFStore(t).Workflows(), path, nil); err == nil || !strings.Contains(err.Error(), c.names) {
			t.Errorf("%s: InitWorkflow err = %v, want a refusal naming %s", name, err, c.names)
		}
	}
}

// mergedYAML gives twin its whole definition, output_schema included,
// through a merge key, as the decoded workflow does.
const mergedYAML = `name: t
nodes:
  lens: &lens
    type: agent
    prompt: go
    outputs: [verdict]
    output_schema:
      type: object
      required: [verdict]
      properties:
        verdict: {enum: [support, oppose]}
  twin:
    <<: *lens
    prompt: again
edges:
  - {from: lens, to: twin}
`

// A schema a node gets through a merge key is parsed, dispatched and
// checked like one written inline; one that breaks the subset is refused.
func TestOutputSchema_MergeKeys(t *testing.T) {
	defn, err := LoadYAMLString(mergedYAML)
	if err != nil {
		t.Fatal(err)
	}
	twin, _ := defn["nodes"].(map[string]any)["twin"].(map[string]any)
	if _, ok := twin["output_schema"]; !ok {
		t.Fatal("the decoder did not merge output_schema into twin, so the fixture tests nothing")
	}
	schemas, err := OutputSchemas(mergedYAML)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(schemas["lens"])
	b, _ := json.Marshal(schemas["twin"])
	if schemas["twin"] == nil || !bytes.Equal(a, b) {
		t.Fatalf("twin's schema %s, want lens's %s", b, a)
	}

	store := newWFStore(t)
	runID, err := InitWorkflow(store.Workflows(), writeWF(t, mergedYAML), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GetNextNodes(store.Workflows(), runID); err != nil {
		t.Fatal(err)
	}
	if err := CompleteNode(store.Workflows(), runID, "lens", map[string]any{"verdict": "support"}); err != nil {
		t.Fatal(err)
	}
	nodes, err := GetNextNodes(store.Workflows(), runID)
	if err != nil || len(nodes) != 1 || nodes[0].Node != "twin" || nodes[0].OutputSchema == nil {
		t.Fatalf("GetNextNodes = %+v, %v; want twin with its schema", nodes, err)
	}
	var rej *AcceptRejection
	if err := CompleteNode(store.Workflows(), runID, "twin", map[string]any{"final_text": "bash: chb: command not found"}); !errors.As(err, &rej) || rej.Predicate != OutputSchemaPredicate {
		t.Errorf("prose on twin: err = %v, want an output_schema rejection", err)
	}

	bad := "name: t\nnodes:\n  twin:\n    <<: {type: agent, prompt: go, outputs: [verdict], output_schema: {type: object, properties: {verdict: {type: string, pattern: x}}}}\n"
	errs, _, err := Validate(writeWF(t, bad))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, "properties.verdict.pattern") }) {
		t.Errorf("pattern through a merge key: Validate errors %v, want it refused", errs)
	}
}
