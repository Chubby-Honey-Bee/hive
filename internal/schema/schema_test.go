package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// jsonKeyOrder returns the keys of the JSON object at path (a list of keys
// from the root), in the order the bytes list them.
func jsonKeyOrder(t *testing.T, b []byte, path ...string) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	var walk func(depth int) []string
	walk = func(depth int) []string {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if tok != json.Delim('{') {
			t.Fatalf("want an object at %v, got %v", path[:depth], tok)
		}
		var keys []string
		var found []string
		for dec.More() {
			kt, _ := dec.Token()
			k := kt.(string)
			keys = append(keys, k)
			if depth < len(path) && k == path[depth] {
				found = walk(depth + 1)
				continue
			}
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				t.Fatalf("decode value: %v", err)
			}
		}
		_, _ = dec.Token()
		if depth == len(path) {
			return keys
		}
		return found
	}
	return walk(0)
}

// yamlKeyOrder returns the keys of the mapping at path in a YAML document.
func yamlKeyOrder(t *testing.T, text string, path ...string) []string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	n := doc.Content[0]
	for _, p := range path {
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == p {
				next = n.Content[i+1]
			}
		}
		if next == nil {
			t.Fatalf("no %q in %v", p, path)
		}
		n = next
	}
	var keys []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		keys = append(keys, n.Content[i].Value)
	}
	return keys
}

func mustParse(t *testing.T, text string) *Schema {
	t.Helper()
	s, err := ParseYAML([]byte(text))
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	return s
}

// A Go map would sort the properties; the schema keeps the YAML's order, at
// every level, through a JSON round trip too.
func TestMarshalKeepsDeclaredOrder(t *testing.T) {
	text := `
type: object
properties:
  zeta: {type: string}
  alpha:
    type: object
    properties:
      yak: {type: integer}
      bee: {type: string}
  mid: {type: array, items: {type: string}}
required: [zeta]
`
	s := mustParse(t, text)
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range [][]string{{"properties"}, {"properties", "alpha", "properties"}} {
		want := yamlKeyOrder(t, text, path...)
		if got := jsonKeyOrder(t, b, path...); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%v order = %v, want %v (bytes %s)", path, got, want, b)
		}
	}
	var again Schema
	if err := json.Unmarshal(b, &again); err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(&again)
	if !bytes.Equal(b, b2) {
		t.Errorf("round trip changed the bytes:\n%s\n%s", b, b2)
	}
}

// Every keyword outside the subset is refused, naming its path; every keyword
// inside it parses.
func TestUnsupportedKeywordsAreRefused(t *testing.T) {
	for _, kw := range []string{"pattern", "format", "anyOf", "oneOf", "$ref", "uniqueItems", "maxitems", "patternProperties"} {
		text := fmt.Sprintf("type: object\nproperties:\n  x:\n    type: string\n    %s: 1\n", kw)
		_, err := ParseYAML([]byte(text))
		if err == nil || !strings.Contains(err.Error(), "properties.x."+kw) || !strings.Contains(err.Error(), "unsupported keyword") {
			t.Errorf("%s: err = %v, want unsupported keyword at properties.x.%s", kw, err, kw)
		}
	}
	all := `
type: object
title: t
description: d
properties:
  s: {type: string, minLength: 1, maxLength: 9, enum: [a, b]}
  n: {type: integer, minimum: 1, maximum: 5}
  c: {const: x}
  a: {type: array, items: {type: string}, minItems: 0, maxItems: 3}
required: [s]
additionalProperties: false
`
	if _, err := ParseYAML([]byte(all)); err != nil {
		t.Errorf("a schema using every supported keyword: %v", err)
	}
}

// Contradictions and typos are refused at load.
func TestContradictionsAreRefused(t *testing.T) {
	cases := map[string]string{
		"required names no property":       "type: object\nproperties: {a: {type: string}}\nrequired: [b]",
		"required with no properties":      "type: object\nrequired: [zzz]",
		"nested required, no properties":   "type: object\nproperties:\n  f: {type: object, required: [q]}",
		"minItems above maxItems":          "type: array\nminItems: 3\nmaxItems: 2",
		"maxItems on a string":             "type: string\nmaxItems: 2",
		"unknown type":                     "type: dict",
		"negative count":                   "type: array\nmaxItems: -1",
		"fractional count":                 "type: array\nmaxItems: 1.5",
		"empty enum":                       "enum: []",
		"items as a list":                  "type: array\nitems: [{type: string}]",
		"additionalProperties as a schema": "type: object\nadditionalProperties: {type: string}",
		"const outside its enum":           "enum: [a]\nconst: b",
		"property declared twice":          "type: object\nproperties:\n  a: {type: string}\n  a: {type: string}",
		"a list, not a mapping":            "[a, b]",
	}
	for name, text := range cases {
		if _, err := ParseYAML([]byte(text)); err == nil {
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}

// MapEntries reads a mapping as yaml.v3 decodes it into a map: merge keys
// resolved, a key the mapping sets itself winning over a merged one, and the
// first of several merged mappings winning. Graded against the decoder.
func TestMapEntriesMatchesDecode(t *testing.T) {
	text := `
base: &base {a: base-a, b: base-b, c: base-c}
other: &other {b: other-b, d: other-d}
deep: &deep {<<: *base, e: deep-e}
cases:
  own-wins: {<<: *base, b: own-b}
  list-first-wins: {<<: [*other, *base], z: own-z}
  nested: {<<: *deep, a: own-a}
  inline: {<<: {x: inline-x}, y: own-y}
  plain: {p: 1, q: 2}
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]map[string]any
	if err := yaml.Unmarshal([]byte(text), &struct {
		Cases *map[string]map[string]any `yaml:"cases"`
	}{&decoded}); err != nil {
		t.Fatal(err)
	}
	for _, c := range MapEntries(MapValue(doc.Content[0], "cases")) {
		want := decoded[c.Key.Value]
		got := map[string]any{}
		for _, e := range MapEntries(c.Value) {
			if _, dup := got[e.Key.Value]; dup {
				t.Errorf("%s: key %q listed twice", c.Key.Value, e.Key.Value)
			}
			var v any
			if err := e.Value.Decode(&v); err != nil {
				t.Fatal(err)
			}
			got[e.Key.Value] = v
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: entries %v, the decoder reads %v", c.Key.Value, got, want)
		}
	}
	// A merged mapping's entries take the merge key's place.
	var keys []string
	for _, e := range MapEntries(MapValue(MapValue(doc.Content[0], "cases"), "inline")) {
		keys = append(keys, e.Key.Value)
	}
	if strings.Join(keys, ",") != "x,y" {
		t.Errorf("inline merge order %v, want the merged x where the merge key is, then y", keys)
	}
}

// A schema built with merge keys parses as the same schema written inline,
// and a keyword outside the subset reached through a merge is still refused.
func TestMergeKeysInASchema(t *testing.T) {
	merged := `
defs: &common
  verdict: {enum: [support, oppose]}
schema:
  type: object
  required: [key_points, verdict]
  properties:
    key_points: {type: array, items: {type: string}}
    <<: *common
`
	inline := `
type: object
required: [key_points, verdict]
properties:
  key_points: {type: array, items: {type: string}}
  verdict: {enum: [support, oppose]}
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(merged), &doc); err != nil {
		t.Fatal(err)
	}
	s, err := FromYAML(MapValue(doc.Content[0], "schema"))
	if err != nil {
		t.Fatalf("a schema with a merge key: %v", err)
	}
	got, _ := json.Marshal(s)
	want, _ := json.Marshal(mustParse(t, inline))
	if !bytes.Equal(got, want) {
		t.Errorf("merged schema %s, inline %s", got, want)
	}
	bad := strings.Replace(merged, "verdict: {enum: [support, oppose]}", "verdict: {type: string, pattern: x}", 1)
	if err := yaml.Unmarshal([]byte(bad), &doc); err != nil {
		t.Fatal(err)
	}
	if _, err := FromYAML(MapValue(doc.Content[0], "schema")); err == nil || !strings.Contains(err.Error(), "properties.verdict.pattern") {
		t.Errorf("pattern through a merge key: err = %v, want it refused at properties.verdict.pattern", err)
	}
}

// maxItems n accepts exactly the arrays of n items or fewer.
func TestValidateMaxItems(t *testing.T) {
	for n := 0; n <= 3; n++ {
		s := mustParse(t, fmt.Sprintf("type: array\nitems: {type: string}\nmaxItems: %d", n))
		for size := 0; size <= n+2; size++ {
			arr := make([]any, size)
			for i := range arr {
				arr[i] = "x"
			}
			got := s.Validate(arr)
			if want := size <= n; (len(got) == 0) != want {
				t.Errorf("maxItems %d, %d items: violations %v, want valid=%v", n, size, got, want)
			}
		}
	}
}

// The forager contract: each violation names its path, and a valid verdict
// has none.
func TestValidateForagerContract(t *testing.T) {
	s := mustParse(t, `
type: object
required: [forager, verdict, key_points, recommendation]
properties:
  forager: {const: "skeptic"}
  verdict: {enum: [support, oppose, conditional, abstain]}
  key_points: {type: array, items: {type: string}, maxItems: 2}
  coverage: {type: integer, minimum: 1, maximum: 5}
  recommendation: {type: string}
`)
	valid := map[string]any{"forager": "skeptic", "verdict": "oppose", "key_points": []any{"a"}, "recommendation": "r", "coverage": 3.0}
	if v := s.Validate(valid); len(v) != 0 {
		t.Fatalf("valid verdict: %v", v)
	}
	mutate := func(k string, val any) map[string]any {
		m := map[string]any{}
		for kk, vv := range valid {
			m[kk] = vv
		}
		if val == nil {
			delete(m, k)
		} else {
			m[k] = val
		}
		return m
	}
	cases := []struct {
		name   string
		in     any
		path   string
		detail string
	}{
		{"verdict outside the enum", mutate("verdict", "maybe"), "$.verdict", `"maybe"`},
		{"wrong forager", mutate("forager", "optimist"), "$.forager", `"optimist"`},
		{"missing recommendation", mutate("recommendation", nil), "$", `"recommendation"`},
		{"too many key points", mutate("key_points", []any{"a", "b", "c"}), "$.key_points", "3 items"},
		{"a key point that is a number", mutate("key_points", []any{1.0}), "$.key_points[0]", "want string"},
		{"coverage not an integer", mutate("coverage", 2.5), "$.coverage", "want integer"},
		{"coverage above the maximum", mutate("coverage", 6.0), "$.coverage", "maximum"},
		{"not an object", []any{}, "$", "want object"},
	}
	for _, c := range cases {
		got := s.Validate(c.in)
		joined := strings.Join(got, "; ")
		if len(got) == 0 || !strings.Contains(joined, c.path+":") || !strings.Contains(joined, c.detail) {
			t.Errorf("%s: violations %q, want one at %s naming %s", c.name, joined, c.path, c.detail)
		}
	}
}

// additionalProperties: false names each extra key.
func TestValidateClosedObject(t *testing.T) {
	s := mustParse(t, "type: object\nproperties: {a: {type: string}}\nadditionalProperties: false")
	extra := []string{"b", "c"}
	in := map[string]any{"a": "x"}
	for _, k := range extra {
		in[k] = 1.0
	}
	got := strings.Join(s.Validate(in), "; ")
	for _, k := range extra {
		if !strings.Contains(got, fmt.Sprintf("property %q is not allowed", k)) {
			t.Errorf("violations %q do not name %q", got, k)
		}
	}
	if v := s.Validate(map[string]any{"a": "x"}); len(v) != 0 {
		t.Errorf("no extra keys: %v", v)
	}
}

// maxLength counts characters, not bytes.
func TestValidateMaxLengthCountsRunes(t *testing.T) {
	word := "∇∇∇"
	n := len([]rune(word))
	for _, max := range []int{n - 1, n} {
		s := mustParse(t, fmt.Sprintf("type: string\nmaxLength: %d", max))
		if got := s.Validate(word); (len(got) == 0) != (n <= max) {
			t.Errorf("maxLength %d on %d runes (%d bytes): %v", max, n, len(word), got)
		}
	}
}

// Strict holds only when every object closes additionalProperties and
// requires every property.
func TestStrict(t *testing.T) {
	closed := "type: object\nproperties:\n  a: {type: string}\n  b: {type: array, items: {type: object, properties: {c: {type: string}}, required: [c], additionalProperties: false}}\nrequired: [a, b]\nadditionalProperties: false"
	if !mustParse(t, closed).Strict() {
		t.Error("a closed schema requiring every property is not strict")
	}
	for name, text := range map[string]string{
		"open root":         strings.Replace(closed, "\nadditionalProperties: false", "", 1),
		"optional property": strings.Replace(closed, "required: [a, b]", "required: [a]", 1),
		"open nested":       strings.Replace(closed, "required: [c], additionalProperties: false", "required: [c]", 1),
	} {
		if mustParse(t, text).Strict() {
			t.Errorf("%s: strict, want not", name)
		}
	}
}

// MoveLast moves the named properties after the rest, keeping the others'
// order, and leaves the original alone.
func TestMoveLast(t *testing.T) {
	text := "type: object\nproperties:\n  forager: {const: x}\n  verdict: {type: string}\n  key_points: {type: array}\n  recommendation: {type: string}\n  evidence: {type: array}"
	s := mustParse(t, text)
	moved := s.MoveLast("verdict", "recommendation")
	var want []string
	for _, k := range yamlKeyOrder(t, text, "properties") {
		if k != "verdict" && k != "recommendation" {
			want = append(want, k)
		}
	}
	want = append(want, "verdict", "recommendation")
	var got []string
	for _, p := range moved.Properties {
		got = append(got, p.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order %v, want %v", got, want)
	}
	if s.Properties[1].Name != yamlKeyOrder(t, text, "properties")[1] {
		t.Error("MoveLast changed the original")
	}
}

// Every keyword the subset documents is parsed, and nothing else is: the
// keyword table and the keyword list the refusal message prints are one set.
func TestKeywordParsersMatchTheKeywordList(t *testing.T) {
	if len(keywordParsers) != len(keywords) {
		t.Errorf("%d keyword parsers for %d listed keywords", len(keywordParsers), len(keywords))
	}
	for _, k := range keywords {
		if keywordParsers[k] == nil {
			t.Errorf("listed keyword %q has no parser", k)
		}
	}
}
