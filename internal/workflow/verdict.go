package workflow

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// fieldLabel is a verdict field's key and the label it is written under.
type fieldLabel struct{ key, label string }

// fieldSet is the set of names and of the fields' keys.
func fieldSet(names []string, fields ...[]fieldLabel) map[string]bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	for _, f := range slices.Concat(fields...) {
		set[f.key] = true
	}
	return set
}

// otherKeys is the keys of outputs shown does not hold, in name order.
func otherKeys(outputs map[string]any, shown map[string]bool) []string {
	var keys []string
	for k := range outputs {
		if !shown[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// verdictFieldOrder is the order renderVerdict writes a verdict's fields
// in, with their labels. Fields it does not name follow in name order, and
// the recommendation comes last.
var verdictFieldOrder = []fieldLabel{
	{"verdict", "verdict"},
	{"key_points", "key points"},
	{"evidence", "evidence"},
	{"uncertainties", "uncertainties"},
	{"def_claims", "definitions (def_claims)"},
	{"gua_claims", "guarantees (gua_claims)"},
	{"asm_claims", "assumptions (asm_claims)"},
	{"unk_claims", "unknowns (unk_claims)"},
}

// verdictNamedKeys are the fields renderVerdict writes by name or leaves
// out: those of verdictFieldOrder, the recommendation, the forager's own
// name and a final_text fallback.
var verdictNamedKeys = fieldSet([]string{"forager", "final_text", "recommendation"}, verdictFieldOrder)

// renderVerdict writes every field a forager returned, uncut: the verdict,
// key points, evidence, uncertainties and typed claims, then any other
// field by name, then the recommendation. The forager's own name and a
// final_text fallback are left out.
func renderVerdict(outputs map[string]any) string {
	var b strings.Builder
	for _, f := range verdictFieldOrder {
		writeField(&b, "", f.label, outputs[f.key])
	}
	for _, k := range otherKeys(outputs, verdictNamedKeys) {
		writeField(&b, "", k, outputs[k])
	}
	writeField(&b, "", "recommendation", outputs["recommendation"])
	return strings.TrimSpace(b.String())
}

// writeField writes one field under indent: a list as bullets, anything
// else on its label's line. A missing field or an empty list writes nothing.
func writeField(b *strings.Builder, indent, label string, v any) {
	list, isList := v.([]any)
	switch {
	case isList:
		writeList(b, indent, label, list)
	case v != nil:
		fmt.Fprintf(b, "%s%s: %s\n", indent, label, verdictItemText(v))
	}
}

func writeList(b *strings.Builder, indent, label string, list []any) {
	if len(list) == 0 {
		return
	}
	fmt.Fprintf(b, "%s%s:\n", indent, label)
	for _, it := range list {
		fmt.Fprintf(b, "%s  - %s\n", indent, verdictItemText(it))
	}
}

// verdictItemText writes a string trimmed and anything else as JSON,
// without HTML escaping.
func verdictItemText(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	s, err := jsonText(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return s
}
