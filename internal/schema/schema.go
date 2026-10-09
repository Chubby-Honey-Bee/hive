// Package schema is the JSON Schema subset a workflow node's output_schema
// may use. It parses a schema from YAML, checks a node's outputs against it,
// and serializes it with its properties in the order they were declared,
// which a Go map would sort. llama.cpp's json-schema-to-grammar writes an
// object's required properties first, in that order, then its optional ones.
// A server that re-encodes the schema through a map before building the
// grammar (Ollama's native-chat path) sorts them instead.
//
// Every keyword the parser accepts is either checked by Validate or is an
// annotation (title, description). Any other keyword is refused, so nothing
// an author writes goes silently unchecked.
package schema

import "slices"

// types are the JSON Schema type names the subset accepts.
var types = []string{"object", "array", "string", "integer", "number", "boolean", "null"}

// keywords are the keywords the subset accepts.
var keywords = []string{
	"type", "enum", "const", "required", "properties", "additionalProperties",
	"items", "minItems", "maxItems", "minLength", "maxLength", "minimum", "maximum",
	"title", "description",
}

// Schema is one schema of the subset. The zero value accepts anything.
type Schema struct {
	Types      []string
	Enum       []any
	Const      any
	HasConst   bool
	Required   []string
	Properties []Property
	// AdditionalProperties is nil when the schema does not say; false closes
	// the object to its declared properties.
	AdditionalProperties *bool
	Items                *Schema
	MinItems, MaxItems   *int
	MinLength, MaxLength *int
	Minimum, Maximum     *float64
	Title, Description   string
}

// Property is one named property, in declared order.
type Property struct {
	Name   string
	Schema *Schema
}

// Property returns the schema of the property named name, or nil.
func (s *Schema) Property(name string) *Schema {
	for _, p := range s.Properties {
		if p.Name == name {
			return p.Schema
		}
	}
	return nil
}

// IsObject reports whether the schema's type is exactly object.
func (s *Schema) IsObject() bool {
	return len(s.Types) == 1 && s.Types[0] == "object"
}

// Clone returns a deep copy, so a caller can reorder or tighten it without
// touching the original.
func (s *Schema) Clone() *Schema {
	if s == nil {
		return nil
	}
	c := *s
	c.Types = slices.Clone(s.Types)
	c.Enum = slices.Clone(s.Enum)
	c.Required = slices.Clone(s.Required)
	c.Properties = make([]Property, len(s.Properties))
	for i, p := range s.Properties {
		c.Properties[i] = Property{Name: p.Name, Schema: p.Schema.Clone()}
	}
	if len(s.Properties) == 0 {
		c.Properties = nil
	}
	c.Items = s.Items.Clone()
	c.AdditionalProperties = clonePtr(s.AdditionalProperties)
	c.MinItems, c.MaxItems = clonePtr(s.MinItems), clonePtr(s.MaxItems)
	c.MinLength, c.MaxLength = clonePtr(s.MinLength), clonePtr(s.MaxLength)
	c.Minimum, c.Maximum = clonePtr(s.Minimum), clonePtr(s.Maximum)
	return &c
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// MoveLast returns a copy whose properties named in names come after every
// other property, in the order names lists them. The others keep their order.
func (s *Schema) MoveLast(names ...string) *Schema {
	c := s.Clone()
	if moved := append(c.propertiesExcept(names), c.propertiesNamed(names)...); len(moved) > 0 {
		c.Properties = moved
	}
	return c
}

// propertiesExcept is the properties not named in names, in order.
func (s *Schema) propertiesExcept(names []string) []Property {
	var out []Property
	for _, p := range s.Properties {
		if !slices.Contains(names, p.Name) {
			out = append(out, p)
		}
	}
	return out
}

// propertiesNamed is the properties named in names, in names' order.
func (s *Schema) propertiesNamed(names []string) []Property {
	var out []Property
	for _, n := range names {
		for _, p := range s.Properties {
			if p.Name == n {
				out = append(out, p)
			}
		}
	}
	return out
}

// Strict reports whether the schema qualifies for OpenAI's strict structured
// outputs: every object schema in it closes additionalProperties and requires
// every property it declares. A server that is sent strict: true for a schema
// that does not qualify refuses the request.
func (s *Schema) Strict() bool {
	if s == nil {
		return true
	}
	return s.strictObject() && s.strictChildren()
}

// strictObject reports whether the schema, when it is an object schema,
// closes additionalProperties and requires every property it declares.
func (s *Schema) strictObject() bool {
	if !slices.Contains(s.Types, "object") && len(s.Properties) == 0 {
		return true
	}
	return s.closed() && s.requiresAll()
}

// closed reports whether additionalProperties is false.
func (s *Schema) closed() bool {
	return s.AdditionalProperties != nil && !*s.AdditionalProperties
}

func (s *Schema) requiresAll() bool {
	for _, p := range s.Properties {
		if !slices.Contains(s.Required, p.Name) {
			return false
		}
	}
	return true
}

// strictChildren reports whether every property's schema and the items
// schema are strict.
func (s *Schema) strictChildren() bool {
	for _, p := range s.Properties {
		if !p.Schema.Strict() {
			return false
		}
	}
	return s.Items.Strict()
}
