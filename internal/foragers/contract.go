package foragers

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/schema"
	"gopkg.in/yaml.v3"
)

// lengthCapKeys are the keys a length_caps entry may hold. Only max_items
// reaches the schema; the rest are adherence checks in chb agent-harness,
// since a grammar holding a string to a length would clip it mid-sentence.
var lengthCapKeys = []string{"max_items", "max_chars", "max_chars_each", "must_name_one_of"}

// decisionFields are the properties a persona's schema is sent with last,
// in this order. A llama.cpp grammar writes required properties first, in
// declared order, then optional ones, so a model writes its required reasons
// before it commits; a persona's optional fields come after the decision.
var decisionFields = []string{"verdict", "recommendation"}

// parseContract reads a persona's output_schema from its frontmatter and
// compiles length_caps.<field>.max_items into that property's maxItems. It
// returns nil and no error when the frontmatter declares no output_schema.
func parseContract(frontmatter []byte) (*schema.Schema, error) {
	root, raw := contractNodes(frontmatter)
	if raw == nil {
		return nil, nil
	}
	s, err := objectSchema(raw)
	if err != nil {
		return nil, err
	}
	if err := applyLengthCaps(s, schema.MapValue(root, "length_caps")); err != nil {
		return nil, err
	}
	return s, nil
}

// contractNodes are the frontmatter's root mapping and its output_schema,
// both nil when the frontmatter does not parse as a YAML document.
func contractNodes(frontmatter []byte) (root, outputSchema *yaml.Node) {
	var doc yaml.Node
	if err := yaml.Unmarshal(frontmatter, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, nil
	}
	root = doc.Content[0]
	return root, schema.MapValue(root, "output_schema")
}

// objectSchema reads output_schema, which must be an object schema.
func objectSchema(raw *yaml.Node) (*schema.Schema, error) {
	s, err := schema.FromYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("output_schema: %w", err)
	}
	if !s.IsObject() {
		return nil, errors.New("output_schema: want type: object")
	}
	return s, nil
}

// applyLengthCaps checks length_caps, when the frontmatter has it, and
// compiles each property's max_items into the schema.
func applyLengthCaps(s *schema.Schema, caps *yaml.Node) error {
	if caps == nil {
		return nil
	}
	if caps.Kind != yaml.MappingNode {
		return errors.New("length_caps: want a mapping of output_schema properties to caps")
	}
	return applyEachFieldCaps(s, caps)
}

// applyEachFieldCaps applies each property's caps, in the order
// length_caps lists them.
func applyEachFieldCaps(s *schema.Schema, caps *yaml.Node) error {
	for _, c := range schema.MapEntries(caps) {
		if err := applyFieldCaps(s, c.Key.Value, c.Value); err != nil {
			return err
		}
	}
	return nil
}

// applyFieldCaps applies one property's caps.
func applyFieldCaps(s *schema.Schema, field string, entry *yaml.Node) error {
	prop, err := cappedProperty(s, field, entry)
	if err != nil {
		return err
	}
	for _, e := range schema.MapEntries(entry) {
		if err := applyCap(prop, field, e.Key.Value, e.Value); err != nil {
			return err
		}
	}
	return nil
}

// cappedProperty is the property a length_caps entry caps: one the schema
// declares, capped by a mapping.
func cappedProperty(s *schema.Schema, field string, entry *yaml.Node) (*schema.Schema, error) {
	prop := s.Property(field)
	if prop == nil {
		return nil, fmt.Errorf("length_caps.%s: output_schema declares no such property", field)
	}
	if entry.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("length_caps.%s: want a mapping (%s)", field, strings.Join(lengthCapKeys, ", "))
	}
	return prop, nil
}

// applyCap checks one cap of a property; only max_items reaches the schema.
func applyCap(prop *schema.Schema, field, key string, val *yaml.Node) error {
	if !slices.Contains(lengthCapKeys, key) {
		return fmt.Errorf("length_caps.%s.%s: unknown cap (want %s)", field, key, strings.Join(lengthCapKeys, ", "))
	}
	if key != "max_items" {
		return nil
	}
	return applyMaxItems(prop, field, val)
}

// applyMaxItems compiles a max_items cap into the array property's
// maxItems, keeping the tighter of the two.
func applyMaxItems(prop *schema.Schema, field string, val *yaml.Node) error {
	n, ok := wholeCount(val)
	if !ok {
		return fmt.Errorf("length_caps.%s.max_items: want a whole number, 0 or more", field)
	}
	if err := checkArrayProperty(prop, field); err != nil {
		return err
	}
	tightenMaxItems(prop, n)
	return checkMinItems(prop, field, n)
}

// wholeCount reads a YAML integer of 0 or more.
func wholeCount(val *yaml.Node) (int, bool) {
	var n int
	if !isIntScalar(val) || val.Decode(&n) != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// isIntScalar reports whether a node is a YAML integer scalar.
func isIntScalar(val *yaml.Node) bool {
	return val.Kind == yaml.ScalarNode && val.Tag == "!!int"
}

// checkArrayProperty refuses max_items on a property typed as anything but
// an array.
func checkArrayProperty(prop *schema.Schema, field string) error {
	if len(prop.Types) > 0 && !slices.Contains(prop.Types, "array") {
		return fmt.Errorf("length_caps.%s.max_items: output_schema's %s is not an array", field, field)
	}
	return nil
}

// tightenMaxItems sets maxItems to n unless the schema's is tighter.
func tightenMaxItems(prop *schema.Schema, n int) {
	if prop.MaxItems == nil || n < *prop.MaxItems {
		prop.MaxItems = &n
	}
}

// checkMinItems refuses a max_items below the schema's minItems.
func checkMinItems(prop *schema.Schema, field string, n int) error {
	if prop.MinItems != nil && *prop.MinItems > n {
		return fmt.Errorf("length_caps.%s.max_items %d is below output_schema's minItems %d", field, n, *prop.MinItems)
	}
	return nil
}

// dispatchSchema is the schema a persona's node is sent: its own, with the
// decision fields moved last. nil when it declares none.
func dispatchSchema(w Forager) *schema.Schema {
	if w.OutputSchema == nil {
		return nil
	}
	return w.OutputSchema.MoveLast(decisionFields...)
}
