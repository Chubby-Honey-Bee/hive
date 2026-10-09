package schema

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Validate checks v, a value as encoding/json decodes it (map[string]any,
// []any, string, float64, bool, nil; int and json.Number are also taken), and
// returns one message per violation, each naming its path from $. None means
// v is valid.
func (s *Schema) Validate(v any) []string {
	var out []string
	s.validate(v, "$", &out)
	return out
}

func (s *Schema) validate(v any, path string, out *[]string) {
	if s == nil {
		return
	}
	v = normalize(v)
	if !s.allowsType(v) {
		*out = append(*out, fmt.Sprintf("%s: %s is %s, want %s", path, show(v), typeOf(v), strings.Join(s.Types, " or ")))
		return
	}
	s.validateConst(v, path, out)
	s.validateEnum(v, path, out)
	s.validateKind(v, path, out)
}

// allowsType reports whether v has one of the schema's types, or the schema
// names none.
func (s *Schema) allowsType(v any) bool {
	return len(s.Types) == 0 || slices.ContainsFunc(s.Types, func(t string) bool { return hasType(v, t) })
}

func (s *Schema) validateConst(v any, path string, out *[]string) {
	if s.HasConst && !equal(v, s.Const) {
		*out = append(*out, fmt.Sprintf("%s: %s is not %s", path, show(v), show(s.Const)))
	}
}

func (s *Schema) validateEnum(v any, path string, out *[]string) {
	if len(s.Enum) > 0 && !slices.ContainsFunc(s.Enum, func(e any) bool { return equal(v, e) }) {
		*out = append(*out, fmt.Sprintf("%s: %s is not one of %s", path, show(v), s.enumText()))
	}
}

func (s *Schema) enumText() string {
	opts := make([]string, len(s.Enum))
	for i, e := range s.Enum {
		opts[i] = show(e)
	}
	return strings.Join(opts, ", ")
}

// validateKind applies the keywords of v's kind: a flat dispatch, each kind
// checked by one call.
func (s *Schema) validateKind(v any, path string, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		s.validateObject(x, path, out)
	case []any:
		s.validateArray(x, path, out)
	case string:
		s.validateString(x, path, out)
	case float64:
		s.validateNumber(x, path, out)
	}
}

func (s *Schema) validateObject(x map[string]any, path string, out *[]string) {
	s.validateRequired(x, path, out)
	s.validateProperties(x, path, out)
	s.validateClosed(x, path, out)
}

func (s *Schema) validateRequired(x map[string]any, path string, out *[]string) {
	for _, r := range s.Required {
		if _, ok := x[r]; !ok {
			*out = append(*out, fmt.Sprintf("%s: required property %q is missing", path, r))
		}
	}
}

func (s *Schema) validateProperties(x map[string]any, path string, out *[]string) {
	for _, p := range s.Properties {
		if val, ok := x[p.Name]; ok {
			p.Schema.validate(val, path+"."+p.Name, out)
		}
	}
}

// validateClosed refuses, in an object closed to its declared properties,
// every other property, in name order.
func (s *Schema) validateClosed(x map[string]any, path string, out *[]string) {
	if !s.closed() {
		return
	}
	for _, k := range s.extraProperties(x) {
		*out = append(*out, fmt.Sprintf("%s: property %q is not allowed", path, k))
	}
}

// extraProperties is the keys of x the schema declares no property for,
// sorted.
func (s *Schema) extraProperties(x map[string]any) []string {
	var extra []string
	for k := range x {
		if s.Property(k) == nil {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return extra
}

func (s *Schema) validateArray(x []any, path string, out *[]string) {
	if below(len(x), s.MinItems) {
		*out = append(*out, fmt.Sprintf("%s: %d items, want at least %d", path, len(x), *s.MinItems))
	}
	if above(len(x), s.MaxItems) {
		*out = append(*out, fmt.Sprintf("%s: %d items, want at most %d", path, len(x), *s.MaxItems))
	}
	for i, item := range x {
		s.Items.validate(item, fmt.Sprintf("%s[%d]", path, i), out)
	}
}

func (s *Schema) validateString(x string, path string, out *[]string) {
	n := utf8.RuneCountInString(x)
	if below(n, s.MinLength) {
		*out = append(*out, fmt.Sprintf("%s: %d characters, want at least %d", path, n, *s.MinLength))
	}
	if above(n, s.MaxLength) {
		*out = append(*out, fmt.Sprintf("%s: %d characters, want at most %d", path, n, *s.MaxLength))
	}
}

func (s *Schema) validateNumber(x float64, path string, out *[]string) {
	if below(x, s.Minimum) {
		*out = append(*out, fmt.Sprintf("%s: %s is below the minimum %s", path, show(x), show(*s.Minimum)))
	}
	if above(x, s.Maximum) {
		*out = append(*out, fmt.Sprintf("%s: %s is above the maximum %s", path, show(x), show(*s.Maximum)))
	}
}

// below reports whether n is under a lower bound that is set.
func below[T cmp.Ordered](n T, least *T) bool {
	return least != nil && n < *least
}

// above reports whether n is over an upper bound that is set.
func above[T cmp.Ordered](n T, most *T) bool {
	return most != nil && n > *most
}

// normalize turns the numeric types a caller may hand in into float64, as
// encoding/json decodes a number.
func normalize(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		return numberValue(x)
	}
	return v
}

// numberValue is a json.Number as a float64, or the number as it is when it
// does not parse.
func numberValue(n json.Number) any {
	if f, err := n.Float64(); err == nil {
		return f
	}
	return n
}

// typeTests report whether a decoded value has each type of the subset.
var typeTests = map[string]func(v any) bool{
	"object":  func(v any) bool { _, ok := v.(map[string]any); return ok },
	"array":   func(v any) bool { _, ok := v.([]any); return ok },
	"string":  func(v any) bool { _, ok := v.(string); return ok },
	"boolean": func(v any) bool { _, ok := v.(bool); return ok },
	"null":    func(v any) bool { return v == nil },
	"number":  func(v any) bool { _, ok := v.(float64); return ok },
	"integer": isInteger,
}

func hasType(v any, t string) bool {
	test, ok := typeTests[t]
	return ok && test(v)
}

func isInteger(v any) bool {
	f, ok := v.(float64)
	return ok && f == math.Trunc(f) && !math.IsInf(f, 0)
}

// typeDescriptions name, for a message, each type but a number's, in the
// order typeOf tries them.
var typeDescriptions = []struct{ schemaType, text string }{
	{"null", "null"},
	{"object", "an object"},
	{"array", "an array"},
	{"string", "a string"},
	{"boolean", "a boolean"},
}

// typeOf names v's type for a message.
func typeOf(v any) string {
	if f, ok := v.(float64); ok {
		return numberType(f)
	}
	for _, d := range typeDescriptions {
		if hasType(v, d.schemaType) {
			return d.text
		}
	}
	return fmt.Sprintf("a %T", v)
}

// numberType names a number's type: an integer when it has no fraction.
func numberType(f float64) string {
	if f == math.Trunc(f) {
		return "an integer"
	}
	return "a number"
}

// show renders a value for a message: JSON, cut to 60 bytes.
func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return clip(string(b))
}

// clip cuts s to 60 bytes or fewer at a rune boundary, ending a cut one in
// "...".
func clip(s string) string {
	if len(s) <= 60 {
		return s
	}
	cut := 57
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// equal compares two decoded JSON values as JSON does.
func equal(a, b any) bool {
	a, b = normalize(a), normalize(b)
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}
