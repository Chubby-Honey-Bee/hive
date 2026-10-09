package schema

import (
	"bytes"
	"encoding/json"
)

// UnmarshalJSON parses a schema from JSON, keeping property order. JSON is
// YAML, so the YAML parser keeps the order a map would lose.
func (s *Schema) UnmarshalJSON(b []byte) error {
	parsed, err := ParseYAML(b)
	if err != nil {
		return err
	}
	*s = *parsed
	return nil
}

// MarshalJSON writes the schema with its properties in declared order.
func (s *Schema) MarshalJSON() ([]byte, error) {
	return marshalObject(s.jsonMembers())
}

// jsonMember is one member of a JSON object.
type jsonMember struct {
	key   string
	value any
}

// jsonKeywords are the keywords MarshalJSON writes, in its order, each with
// the value it writes and whether the schema sets it.
var jsonKeywords = []struct {
	key   string
	value func(s *Schema) (any, bool)
}{
	{"type", (*Schema).typeValue},
	{"title", func(s *Schema) (any, bool) { return s.Title, s.Title != "" }},
	{"description", func(s *Schema) (any, bool) { return s.Description, s.Description != "" }},
	{"enum", func(s *Schema) (any, bool) { return s.Enum, len(s.Enum) > 0 }},
	{"const", func(s *Schema) (any, bool) { return s.Const, s.HasConst }},
	{"properties", func(s *Schema) (any, bool) { return orderedProperties(s.Properties), len(s.Properties) > 0 }},
	{"required", func(s *Schema) (any, bool) { return s.Required, len(s.Required) > 0 }},
	{"additionalProperties", func(s *Schema) (any, bool) { return pointee(s.AdditionalProperties) }},
	{"items", func(s *Schema) (any, bool) { return s.Items, s.Items != nil }},
	{"minItems", func(s *Schema) (any, bool) { return pointee(s.MinItems) }},
	{"maxItems", func(s *Schema) (any, bool) { return pointee(s.MaxItems) }},
	{"minLength", func(s *Schema) (any, bool) { return pointee(s.MinLength) }},
	{"maxLength", func(s *Schema) (any, bool) { return pointee(s.MaxLength) }},
	{"minimum", func(s *Schema) (any, bool) { return pointee(s.Minimum) }},
	{"maximum", func(s *Schema) (any, bool) { return pointee(s.Maximum) }},
}

// jsonMembers is the keywords the schema sets, in jsonKeywords' order.
func (s *Schema) jsonMembers() []jsonMember {
	var members []jsonMember
	for _, k := range jsonKeywords {
		if v, set := k.value(s); set {
			members = append(members, jsonMember{k.key, v})
		}
	}
	return members
}

// typeValue is the type keyword's value: one type as a string, several as a
// list.
func (s *Schema) typeValue() (any, bool) {
	switch len(s.Types) {
	case 0:
		return nil, false
	case 1:
		return s.Types[0], true
	}
	return s.Types, true
}

// pointee is the value p points to, and whether p is set.
func pointee[T any](p *T) (any, bool) {
	if p == nil {
		return nil, false
	}
	return *p, true
}

// orderedProperties marshals as a JSON object in slice order.
type orderedProperties []Property

func (o orderedProperties) MarshalJSON() ([]byte, error) {
	members := make([]jsonMember, len(o))
	for i, p := range o {
		members[i] = jsonMember{p.Name, p.Schema}
	}
	return marshalObject(members)
}

// marshalObject writes members as a JSON object, in order.
func marshalObject(members []jsonMember) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := writeMember(&b, m); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func writeMember(b *bytes.Buffer, m jsonMember) error {
	k, _ := json.Marshal(m.key)
	v, err := json.Marshal(m.value)
	if err != nil {
		return err
	}
	b.Write(k)
	b.WriteByte(':')
	b.Write(v)
	return nil
}
