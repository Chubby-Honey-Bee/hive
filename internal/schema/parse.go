package schema

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// FromYAML parses a schema from a YAML node. Errors name the keyword path.
func FromYAML(n *yaml.Node) (*Schema, error) {
	return parse(n, "")
}

// ParseYAML parses a schema from YAML (or JSON) text.
func ParseYAML(text []byte) (*Schema, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, errors.New("empty schema")
	}
	return FromYAML(doc.Content[0])
}

func at(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func parse(n *yaml.Node, path string) (*Schema, error) {
	n = deref(n)
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: want a mapping, got %s", schemaPath(path), kindName(n))
	}
	s := &Schema{}
	seen := map[string]bool{}
	for _, e := range MapEntries(n) {
		if err := s.parseEntry(e, path, seen); err != nil {
			return nil, err
		}
	}
	return s, s.check(path)
}

// schemaPath is path for a message, or "schema" at the root.
func schemaPath(path string) string {
	if path == "" {
		return "schema"
	}
	return path
}

// parseEntry parses one keyword of a schema mapping; a keyword declared
// twice is refused.
func (s *Schema) parseEntry(e MapEntry, path string, seen map[string]bool) error {
	key := e.Key.Value
	p := at(path, key)
	if seen[key] {
		return fmt.Errorf("%s: declared twice", p)
	}
	seen[key] = true
	return s.parseKeyword(key, e.Value, p)
}

// keywordParser parses one keyword's value into a schema, p being its path.
type keywordParser func(s *Schema, val *yaml.Node, p string) error

// keywordParsers holds the parser of each keyword in keywords. It is filled
// in init rather than by a package-level literal: items and properties parse
// sub-schemas through parseKeyword, so a literal would be an initialisation
// cycle.
var keywordParsers map[string]keywordParser

func init() {
	keywordParsers = map[string]keywordParser{
		"type":       (*Schema).parseType,
		"enum":       (*Schema).parseEnum,
		"const":      (*Schema).parseConst,
		"properties": (*Schema).parseProperties,
		"required": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.Required, err = stringList(v, p)
			return err
		},
		"additionalProperties": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.AdditionalProperties, err = boolFlag(v, p)
			return err
		},
		"items": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.Items, err = itemsSchema(v, p)
			return err
		},
		"minItems": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.MinItems, err = count(v, p)
			return err
		},
		"maxItems": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.MaxItems, err = count(v, p)
			return err
		},
		"minLength": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.MinLength, err = count(v, p)
			return err
		},
		"maxLength": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.MaxLength, err = count(v, p)
			return err
		},
		"minimum": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.Minimum, err = number(v, p)
			return err
		},
		"maximum": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.Maximum, err = number(v, p)
			return err
		},
		"title": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.Title, err = str(v, p)
			return err
		},
		"description": func(s *Schema, v *yaml.Node, p string) (err error) {
			s.Description, err = str(v, p)
			return err
		},
	}
}

// parseKeyword parses one keyword's value into s, p being its path. Any
// keyword outside the subset is refused, naming the ones it accepts.
func (s *Schema) parseKeyword(key string, val *yaml.Node, p string) error {
	parse, ok := keywordParsers[key]
	if !ok {
		return fmt.Errorf("%s: unsupported keyword (supported: %s)", p, strings.Join(keywords, ", "))
	}
	return parse(s, val, p)
}

func (s *Schema) parseType(val *yaml.Node, p string) error {
	names, err := typeValueNames(val, p)
	if err != nil {
		return err
	}
	for _, t := range names {
		if err := s.addType(t, p); err != nil {
			return err
		}
	}
	return nil
}

// typeValueNames reads a type keyword's value: a type name, or a non-empty
// list of them.
func typeValueNames(val *yaml.Node, p string) ([]string, error) {
	switch val.Kind {
	case yaml.ScalarNode:
		return []string{val.Value}, nil
	case yaml.SequenceNode:
		return typeList(val, p)
	}
	return nil, fmt.Errorf("%s: want a type name or a list of them", p)
}

func typeList(val *yaml.Node, p string) ([]string, error) {
	names, err := stringList(val, p)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s: want at least one type", p)
	}
	return names, nil
}

// addType adds a type name, which must be one of the subset's types and
// not listed already.
func (s *Schema) addType(t, p string) error {
	if !slices.Contains(types, t) {
		return fmt.Errorf("%s: unknown type %q (want one of %s)", p, t, strings.Join(types, ", "))
	}
	if slices.Contains(s.Types, t) {
		return fmt.Errorf("%s: type %q listed twice", p, t)
	}
	s.Types = append(s.Types, t)
	return nil
}

func (s *Schema) parseEnum(val *yaml.Node, p string) error {
	if !isNonEmptySequence(val) {
		return fmt.Errorf("%s: want a non-empty list", p)
	}
	for i, item := range val.Content {
		v, err := scalarValue(item, fmt.Sprintf("%s[%d]", p, i))
		if err != nil {
			return err
		}
		s.Enum = append(s.Enum, v)
	}
	return nil
}

func isNonEmptySequence(n *yaml.Node) bool {
	return n.Kind == yaml.SequenceNode && len(n.Content) > 0
}

func (s *Schema) parseConst(val *yaml.Node, p string) error {
	v, err := scalarValue(val, p)
	if err != nil {
		return err
	}
	s.Const, s.HasConst = v, true
	return nil
}

func (s *Schema) parseProperties(val *yaml.Node, p string) error {
	if val.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: want a mapping of property names to schemas", p)
	}
	for _, e := range MapEntries(val) {
		if err := s.addProperty(e, p); err != nil {
			return err
		}
	}
	return nil
}

// addProperty parses one property's schema; a name declared twice is
// refused.
func (s *Schema) addProperty(e MapEntry, p string) error {
	name := e.Key.Value
	if s.Property(name) != nil {
		return fmt.Errorf("%s: declared twice", at(p, name))
	}
	sub, err := parse(e.Value, at(p, name))
	if err != nil {
		return err
	}
	s.Properties = append(s.Properties, Property{Name: name, Schema: sub})
	return nil
}

// boolFlag reads additionalProperties: true or false; a schema there is not
// supported.
func boolFlag(val *yaml.Node, p string) (*bool, error) {
	var b bool
	if val.Kind != yaml.ScalarNode || val.Decode(&b) != nil || val.Tag != "!!bool" {
		return nil, fmt.Errorf("%s: want true or false (a schema here is not supported)", p)
	}
	return &b, nil
}

// itemsSchema reads items: one schema; a list of schemas is not supported.
func itemsSchema(val *yaml.Node, p string) (*Schema, error) {
	if val.Kind == yaml.SequenceNode {
		return nil, fmt.Errorf("%s: want one schema (a list of schemas is not supported)", p)
	}
	return parse(val, p)
}

// schemaChecks are the checks check makes, in the order it makes them.
var schemaChecks = []func(s *Schema, path string) error{
	(*Schema).checkRequired,
	(*Schema).checkTypes,
	(*Schema).checkBounds,
	(*Schema).checkConst,
}

// check refuses combinations the validator could never satisfy or that point
// at a typo.
func (s *Schema) check(path string) error {
	for _, c := range schemaChecks {
		if err := c(s, path); err != nil {
			return err
		}
	}
	return nil
}

// checkRequired refuses a required name that is not a declared property.
func (s *Schema) checkRequired(path string) error {
	for _, r := range s.Required {
		if s.Property(r) == nil {
			return fmt.Errorf("%s: %q is not a declared property", at(path, "required"), r)
		}
	}
	return nil
}

// typedKeyword is a group of keywords that apply to some types only.
type typedKeyword struct {
	uses    func(s *Schema) bool // the schema sets one of the keywords
	types   []string             // the types they apply to
	message string
}

// typedKeywords are the keyword groups checkTypes checks, in its order.
var typedKeywords = []typedKeyword{
	{(*Schema).setsObjectKeywords, []string{"object"}, "properties, required and additionalProperties need type object"},
	{(*Schema).setsArrayKeywords, []string{"array"}, "items, minItems and maxItems need type array"},
	{(*Schema).setsStringKeywords, []string{"string"}, "minLength and maxLength need type string"},
	{(*Schema).setsNumberKeywords, []string{"integer", "number"}, "minimum and maximum need type integer or number"},
}

// checkTypes refuses, in a schema that names its types, a keyword that
// applies to none of them.
func (s *Schema) checkTypes(path string) error {
	if len(s.Types) == 0 {
		return nil
	}
	for _, k := range typedKeywords {
		if k.refuses(s) {
			return fmt.Errorf("%s: %s", at(path, "type"), k.message)
		}
	}
	return nil
}

// refuses reports whether s sets one of the group's keywords and names none
// of the types they apply to.
func (k typedKeyword) refuses(s *Schema) bool {
	return k.uses(s) && !slices.ContainsFunc(k.types, func(t string) bool { return slices.Contains(s.Types, t) })
}

func (s *Schema) setsObjectKeywords() bool {
	return len(s.Properties) > 0 || len(s.Required) > 0 || s.AdditionalProperties != nil
}

func (s *Schema) setsArrayKeywords() bool {
	return s.Items != nil || s.MinItems != nil || s.MaxItems != nil
}

func (s *Schema) setsStringKeywords() bool {
	return s.MinLength != nil || s.MaxLength != nil
}

func (s *Schema) setsNumberKeywords() bool {
	return s.Minimum != nil || s.Maximum != nil
}

// checkBounds refuses a lower bound above its upper bound.
func (s *Schema) checkBounds(path string) error {
	if inverted(s.MinItems, s.MaxItems) {
		return fmt.Errorf("%s: minItems %d is above maxItems %d", path, *s.MinItems, *s.MaxItems)
	}
	if inverted(s.MinLength, s.MaxLength) {
		return fmt.Errorf("%s: minLength %d is above maxLength %d", path, *s.MinLength, *s.MaxLength)
	}
	if inverted(s.Minimum, s.Maximum) {
		return fmt.Errorf("%s: minimum %g is above maximum %g", path, *s.Minimum, *s.Maximum)
	}
	return nil
}

// inverted reports whether both bounds are set and the lower is above the
// upper.
func inverted[T cmp.Ordered](lo, hi *T) bool {
	return lo != nil && hi != nil && *lo > *hi
}

// checkConst refuses a const that is not one of the schema's enum.
func (s *Schema) checkConst(path string) error {
	if s.HasConst && len(s.Enum) > 0 && !slices.ContainsFunc(s.Enum, func(e any) bool { return equal(e, s.Const) }) {
		return fmt.Errorf("%s: const %v is not in enum", path, s.Const)
	}
	return nil
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return "a scalar"
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "a mapping"
	}
	return "nothing"
}

// scalarDecoders decode a scalar of each YAML tag to the value JSON would
// give it. Any other scalar is a string.
var scalarDecoders = map[string]func(n *yaml.Node, p string) (any, error){
	"!!null":  func(*yaml.Node, string) (any, error) { return nil, nil },
	"!!bool":  decodeScalar[bool],
	"!!int":   decodeScalar[float64],
	"!!float": decodeScalar[float64],
}

// scalarValue decodes a scalar to the value JSON would give it: a string,
// float64, bool or nil.
func scalarValue(n *yaml.Node, p string) (any, error) {
	if n.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("%s: want a string, number, boolean or null", p)
	}
	if decode, ok := scalarDecoders[n.Tag]; ok {
		return decode(n, p)
	}
	return n.Value, nil
}

func decodeScalar[T any](n *yaml.Node, p string) (any, error) {
	var v T
	if err := n.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return v, nil
}

func stringList(n *yaml.Node, p string) ([]string, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: want a list of strings", p)
	}
	out := make([]string, 0, len(n.Content))
	for i, item := range n.Content {
		if err := listItemProblem(item, out, p, i); err != nil {
			return nil, err
		}
		out = append(out, item.Value)
	}
	return out, nil
}

// listItemProblem refuses the i-th item of a list of strings when it is not
// a string, or repeats one before it.
func listItemProblem(item *yaml.Node, before []string, p string, i int) error {
	if item.Kind != yaml.ScalarNode || item.Tag == "!!null" {
		return fmt.Errorf("%s[%d]: want a string", p, i)
	}
	if slices.Contains(before, item.Value) {
		return fmt.Errorf("%s: %q listed twice", p, item.Value)
	}
	return nil
}

func count(n *yaml.Node, p string) (*int, error) {
	var v int
	if !isScalarTagged(n, "!!int") || n.Decode(&v) != nil || v < 0 {
		return nil, fmt.Errorf("%s: want a whole number, 0 or more", p)
	}
	return &v, nil
}

func number(n *yaml.Node, p string) (*float64, error) {
	var v float64
	if !isScalarTagged(n, "!!int", "!!float") || n.Decode(&v) != nil {
		return nil, fmt.Errorf("%s: want a number", p)
	}
	return &v, nil
}

// isScalarTagged reports whether n is a scalar with one of tags.
func isScalarTagged(n *yaml.Node, tags ...string) bool {
	return n.Kind == yaml.ScalarNode && slices.Contains(tags, n.Tag)
}

func str(n *yaml.Node, p string) (string, error) {
	if n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s: want a string", p)
	}
	return n.Value, nil
}
