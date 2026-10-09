package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResolveTemplate replaces {var} placeholders in a string with state values,
// in one pass: a placeholder inside a value stays as written
// (fillPlaceholders). A list or a map is written as JSON, so its items keep
// their boundaries; any other value is written with %v.
func ResolveTemplate(template string, state map[string]any) string {
	out, _ := fillPlaceholders(template, stateLookup(state))
	return out
}

// stateLookup resolves a placeholder's key to its state value's text.
func stateLookup(state map[string]any) func(key string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := state[key]
		if !ok {
			return "", false
		}
		return templateText(v), true
	}
}

// resolveNodePrompt fills a node's prompt from state and the run tokens in
// one pass, a state key before a run token of the same name, so no text a
// value brings in, such as a context pack naming {tally}, is filled. It
// returns the placeholders it left for the runner (DispatchNode.Placeholders).
func resolveNodePrompt(prompt string, state map[string]any, repo Store, runID int64, defn map[string]any) (string, []Placeholder) {
	fromState, runToken := stateLookup(state), runTokenLookup(repo, runID, defn)
	return fillPlaceholders(prompt, func(key string) (string, bool) {
		if v, ok := fromState(key); ok {
			return v, true
		}
		return runToken(key)
	})
}

// templateText renders one state value for a prompt: []any and
// map[string]any as JSON without HTML escaping, a float64 as JSON writes it,
// anything else with %v. A JSON number reaches state as a float64, and %v
// wrote 1234567 as 1.234567e+06, a wrong argument for a command node.
func templateText(v any) string {
	switch v.(type) {
	case float64, []any, map[string]any:
		if s, err := jsonText(v); err == nil {
			return s
		}
	}
	return fmt.Sprintf("%v", v)
}

// jsonText is v as JSON without HTML escaping and without the trailing
// newline an encoder writes.
func jsonText(v any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}
