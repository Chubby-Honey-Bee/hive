package workflow

import (
	"fmt"
	"regexp"
	"strings"
)

// commandPlaceholder is a `{name}` in a command node's argv or stdin.
var commandPlaceholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_.-]*)\}`)

// resolveCommandTemplate fills each `{name}` in s from state, in one pass,
// rendering values as ResolveTemplate does. A value's own braces are never
// read as placeholders, as they could be in a second pass. It returns the
// names no state value fills; their placeholders stay as written.
func resolveCommandTemplate(s string, state map[string]any) (string, []string) {
	var missing []string
	out := commandPlaceholder.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		v, ok := state[name]
		if !ok {
			missing = append(missing, m)
			return m
		}
		return templateText(v)
	})
	return out, missing
}

// commandInvocation templates a command node's argv and stdin from state.
// Substitution never splits an element: a value with spaces stays one
// argument, since no shell reads the list. It also returns why the node
// cannot run as given, or "": an argv element that is not a string (a YAML
// number or bool, which Validate refuses), or a placeholder no state value
// fills. Run with either, the program would get an empty or a literal
// `{name}` argument instead of failing.
func commandInvocation(node map[string]any, state map[string]any) (argv []string, stdin, problem string) {
	argv, problems, missing := templateArgv(node, state)
	if tmpl, _ := node["stdin"].(string); tmpl != "" {
		var miss []string
		stdin, miss = resolveCommandTemplate(tmpl, state)
		missing = append(missing, miss...)
	}
	if len(missing) > 0 {
		problems = append(problems, "no state value for "+strings.Join(dedupe(missing), ", "))
	}
	return argv, stdin, strings.Join(problems, "; ")
}

// templateArgv templates each argv element from state, an element that is
// not a string as fmt prints it. It returns the elements, the problems with
// them, and the placeholders no state value fills.
func templateArgv(node, state map[string]any) (argv, problems, missing []string) {
	raw, _ := node["argv"].([]any)
	for i, a := range raw {
		s, ok := a.(string)
		if !ok {
			problems = append(problems, fmt.Sprintf("argv[%d] is %v, not a string", i, a))
			s = fmt.Sprint(a)
		}
		out, miss := resolveCommandTemplate(s, state)
		argv = append(argv, out)
		missing = append(missing, miss...)
	}
	return argv, problems, missing
}

// okExitCodes reads a command node's ok_exit: a non-empty list of exit codes
// from 0 to 255. Absent, it is nil and true: 0 alone completes the node.
func okExitCodes(raw any) ([]int, bool) {
	if raw == nil {
		return nil, true
	}
	list, _ := raw.([]any)
	if len(list) == 0 {
		return nil, false
	}
	return exitCodes(list)
}

// exitCodes reads every entry of a non-empty ok_exit list, or reports that
// one is not an exit code.
func exitCodes(list []any) ([]int, bool) {
	codes := make([]int, 0, len(list))
	for _, v := range list {
		n, ok := exitCode(v)
		if !ok {
			return nil, false
		}
		codes = append(codes, n)
	}
	return codes, true
}

// exitCode reads one ok_exit entry: a whole number from 0 to 255.
func exitCode(v any) (int, bool) {
	n, ok := wholeNumber(v)
	return n, ok && n >= 0 && n <= 255
}

// wholeNumber reads a YAML integer, or a float64 with no fraction, as an
// int.
func wholeNumber(v any) (int, bool) {
	switch c := v.(type) {
	case int:
		return c, true
	case float64:
		return int(c), c == float64(int(c))
	}
	return 0, false
}

// dedupe returns s without repeats, in first-seen order.
func dedupe(s []string) []string {
	seen := make(map[string]bool, len(s))
	out := s[:0:0]
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
