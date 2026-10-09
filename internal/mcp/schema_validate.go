package mcp

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Tool arguments are checked against the inputSchema the server advertises:
// a schema is a contract, and a server that publishes one holds callers to
// it. Each handler reads the arguments it wants with a type assertion and
// treats anything else as absent, so unchecked, `{"limit": "5"}` would mean
// "no limit" and `{"wave": true}` "every wave", both answered with a normal
// result.
//
// The check covers what these schemas actually use: `required`, a `type` per
// property, and an array's `items` type. It is not a general JSON Schema
// implementation, and the 2025-11-25 revision's full 2020-12 requirement
// stays unclaimed.

// validateToolArgs checks arguments against a tool spec's inputSchema.
func validateToolArgs(spec map[string]any, args map[string]any) error {
	schema, ok := spec["inputSchema"].(map[string]any)
	if !ok {
		return nil
	}
	props, _ := schema["properties"].(map[string]any)
	problems := missingRequiredArgs(schema, args)
	for name, raw := range args {
		problems = append(problems, argProblems(props, name, raw)...)
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("arguments do not match this tool's inputSchema: %s",
		strings.Join(problems, "; "))
}

// missingRequiredArgs lists the schema's required arguments that are
// absent or null.
func missingRequiredArgs(schema, args map[string]any) []string {
	required, _ := schema["required"].([]string)
	var problems []string
	for _, name := range required {
		if v, present := args[name]; !present || v == nil {
			problems = append(problems, fmt.Sprintf("%s is required", name))
		}
	}
	return problems
}

// argProblems lists how one argument breaks the property it is declared
// as: its type, and then its enum and its elements' type. Unknown keys are
// tolerated: MCP clients add their own, and refusing them would break
// callers the schema never spoke to. A property with no type, and a null
// value, are not checked either.
func argProblems(props map[string]any, name string, raw any) []string {
	decl, want := declaredArgType(props, name)
	if want == "" || raw == nil {
		return nil
	}
	if problem := argTypeProblem(name, want, raw); problem != "" {
		return []string{problem}
	}
	return append(enumProblems(decl, name, raw), itemProblems(decl, name, raw)...)
}

// declaredArgType is the property an argument is declared as, and its
// type: "" when the argument is undeclared or its property names none.
func declaredArgType(props map[string]any, name string) (map[string]any, string) {
	decl, _ := props[name].(map[string]any)
	want, _ := decl["type"].(string)
	return decl, want
}

// argTypeProblem says how raw is not of the declared type want, "" when it
// is.
func argTypeProblem(name, want string, raw any) string {
	if !valueMatchesType(want, raw) {
		return fmt.Sprintf("%s must be %s, got %s", name, want, describe(raw))
	}
	if want == "integer" {
		return wholeNumberProblem(name, raw)
	}
	return ""
}

// wholeNumberProblem says how raw, declared an integer, is not a whole
// number, "" when it is one, so a fractional value is refused rather than
// truncated by the handler.
func wholeNumberProblem(name string, raw any) string {
	if f, ok := raw.(float64); ok && f != float64(int64(f)) {
		return fmt.Sprintf("%s must be a whole number, got %v", name, f)
	}
	return ""
}

// enumProblems says how a string argument falls outside its property's
// enum. An enum is a closed set; a value outside it is refused here rather
// than by whichever handler happened to check.
func enumProblems(decl map[string]any, name string, raw any) []string {
	enum, hasEnum := decl["enum"].([]string)
	v, isStr := raw.(string)
	if !hasEnum || !isStr || slices.Contains(enum, v) {
		return nil
	}
	return []string{fmt.Sprintf("%s must be one of %s, got %q", name, strings.Join(enum, "|"), v)}
}

// itemProblems holds each element of an array to the declared items type,
// since the handler drops elements of the wrong type: `severity: [1]` would
// become an empty filter and fall back to the default.
func itemProblems(decl map[string]any, name string, raw any) []string {
	elems, isArray := raw.([]any)
	items, _ := decl["items"].(map[string]any)
	itemType, _ := items["type"].(string)
	if !isArray || itemType == "" {
		return nil
	}
	return elementProblems(name, itemType, elems)
}

// elementProblems says how each element that is not of itemType breaks it.
func elementProblems(name, itemType string, elems []any) []string {
	var problems []string
	for i, e := range elems {
		if !valueMatchesType(itemType, e) {
			problems = append(problems, fmt.Sprintf("%s[%d] must be %s, got %s", name, i, itemType, describe(e)))
		}
	}
	return problems
}

// valueMatchesType reports whether v is a JSON value of the declared type
// want.
func valueMatchesType(want string, v any) bool {
	got, ok := jsonTypeOf(v)
	return ok && typeMatches(want, got)
}

func jsonTypeOf(v any) (string, bool) {
	switch v.(type) {
	case string:
		return "string", true
	case bool:
		return "boolean", true
	case float64, int, int64:
		return "number", true
	case []any:
		return "array", true
	case map[string]any:
		return "object", true
	}
	return "", false
}

func typeMatches(want, got string) bool {
	if want == got {
		return true
	}
	// JSON has one number type; "integer" is a number that happens to be whole.
	return want == "integer" && got == "number"
}

func describe(v any) string {
	if t, ok := jsonTypeOf(v); ok {
		if t == "string" {
			return fmt.Sprintf("the string %q", v)
		}
		return fmt.Sprintf("%s (%v)", t, v)
	}
	return fmt.Sprintf("%T", v)
}

// specByToolName indexes the advertised specs so a call can find its own.
func specByToolName(name string) map[string]any {
	for _, raw := range allToolSpecs() {
		spec, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := spec["name"].(string); n == name {
			return spec
		}
	}
	return nil
}
