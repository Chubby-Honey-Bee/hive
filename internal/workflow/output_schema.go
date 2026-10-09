package workflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/schema"
	"gopkg.in/yaml.v3"
)

// OutputSchemaPredicate is the Predicate of an AcceptRejection for outputs
// that break the node's output_schema.
const OutputSchemaPredicate = "output_schema"

// maxViolationsShown bounds how many schema violations a rejection names.
const maxViolationsShown = 5

// OutputSchemas parses the output_schema of every node in a workflow's YAML
// text, keeping each schema's property order, which a map would sort. Merge
// keys resolve as they do in the decoded workflow, so a schema a node gets
// through `<<: *anchor` is parsed like one written inline. A node without one
// is absent from the map. A schema is refused when it is not an object
// schema, uses a keyword the validator does not check, sits on a node that is
// not an agent or parallel_fan, or leaves out a key the node declares in
// outputs:. Every problem is named, in node order.
func OutputSchemas(text string) (map[string]*schema.Schema, error) {
	nodes, err := nodesMapping(text)
	if err != nil {
		return nil, err
	}
	r := &schemaReader{schemas: map[string]*schema.Schema{}}
	for _, e := range schema.MapEntries(nodes) {
		r.read(e.Key.Value, e.Value)
	}
	return r.result()
}

// nodesMapping parses a workflow's YAML text and returns its nodes mapping,
// nil for an empty document.
func nodesMapping(text string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, nil
	}
	return schema.MapValue(doc.Content[0], "nodes"), nil
}

// schemaReader collects a workflow's output schemas and the problems with
// them.
type schemaReader struct {
	schemas  map[string]*schema.Schema
	problems []string
}

// read parses one node's output_schema, if it has one.
func (r *schemaReader) read(name string, node *yaml.Node) {
	raw := schema.MapValue(node, "output_schema")
	if raw == nil {
		return
	}
	s, err := nodeOutputSchema(node, raw)
	if err != nil {
		r.problems = append(r.problems, fmt.Sprintf("node %q: output_schema: %v", name, err))
		return
	}
	r.schemas[name] = s
}

// result is the schemas read, or every problem with them, in node order.
func (r *schemaReader) result() (map[string]*schema.Schema, error) {
	if len(r.problems) == 0 {
		return r.schemas, nil
	}
	sort.Strings(r.problems)
	return nil, errors.New(strings.Join(r.problems, "; "))
}

// outputNodeTypes are the node types whose outputs an output_schema can
// check; a node that names no type is an agent.
var outputNodeTypes = map[string]bool{"": true, "agent": true, "parallel_fan": true}

func nodeOutputSchema(node, raw *yaml.Node) (*schema.Schema, error) {
	if t := yamlNodeType(node); !outputNodeTypes[t] {
		return nil, fmt.Errorf("only agent and parallel_fan nodes produce outputs to check, not a %s node", t)
	}
	s, err := objectSchema(raw)
	if err != nil {
		return nil, err
	}
	if err := undeclaredOutputs(node, s); err != nil {
		return nil, err
	}
	return s, nil
}

// yamlNodeType is a YAML node's type: field, "" when it has none.
func yamlNodeType(node *yaml.Node) string {
	if t := schema.MapValue(node, "type"); t != nil {
		return t.Value
	}
	return ""
}

// objectSchema parses an output_schema, which must be an object schema since
// a node's outputs are an object.
func objectSchema(raw *yaml.Node) (*schema.Schema, error) {
	s, err := schema.FromYAML(raw)
	if err != nil {
		return nil, err
	}
	if !s.IsObject() {
		return nil, errors.New("want type: object, since a node's outputs are an object")
	}
	return s, nil
}

// undeclaredOutputs refuses a schema that declares no property for an
// output the node lists in outputs:.
func undeclaredOutputs(node *yaml.Node, s *schema.Schema) error {
	var missing []string
	for _, o := range sequenceItems(schema.MapValue(node, "outputs")) {
		if s.Property(o.Value) == nil {
			missing = append(missing, o.Value)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("declares no property for output %s", strings.Join(missing, ", "))
	}
	return nil
}

// sequenceItems is the items of a YAML sequence, none for any other node.
func sequenceItems(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

// CheckOutput checks one reply's outputs against a node's schema and returns
// the violations, none when they hold. Outputs holding only final_text, the
// runner's fallback when a reply holds no JSON object, are one violation
// unless the schema itself declares final_text: prose is never an answer to a
// schema.
func CheckOutput(s *schema.Schema, outputs map[string]any) []string {
	if _, ok := outputs["final_text"]; ok && len(outputs) == 1 && s.Property("final_text") == nil {
		return []string{"the reply holds no JSON object"}
	}
	return s.Validate(outputs)
}

// SummarizeViolations joins violations for a rationale or an error, naming at
// most maxViolationsShown and counting the rest.
func SummarizeViolations(violations []string) string {
	if len(violations) <= maxViolationsShown {
		return strings.Join(violations, "; ")
	}
	return fmt.Sprintf("%s; and %d more", strings.Join(violations[:maxViolationsShown], "; "), len(violations)-maxViolationsShown)
}

// schemaRejection is the AcceptRejection for a node's outputs that break its
// output_schema, or nil when they hold or the node has none. itemsChecked
// skips the check for a fan whose caller checked each item's reply; the
// joined outputs are not one reply.
func schemaRejection(definitionYAML string, nodeName string, outputs map[string]any, itemsChecked bool) (*AcceptRejection, error) {
	if itemsChecked {
		return nil, nil
	}
	schemas, err := OutputSchemas(definitionYAML)
	if err != nil {
		return nil, err
	}
	return outputRejection(schemas[nodeName], nodeName, outputs), nil
}

// outputRejection is the AcceptRejection for outputs that break s, or nil
// when they hold or there is no schema.
func outputRejection(s *schema.Schema, nodeName string, outputs map[string]any) *AcceptRejection {
	if s == nil {
		return nil
	}
	v := CheckOutput(s, outputs)
	if len(v) == 0 {
		return nil
	}
	return &AcceptRejection{
		NodeName:       nodeName,
		Predicate:      OutputSchemaPredicate,
		EvaluatedValue: SummarizeViolations(v),
		Outputs:        outputs,
	}
}
