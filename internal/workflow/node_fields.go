package workflow

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// ToolNames are the tools a node's `tools:` may name: the runner's tool
// registry, in the order it registers them. A runner test holds the two
// lists equal.
var ToolNames = []string{"read_file", "write_file", "edit_file", "glob", "grep", "shell", "web_fetch", "chb_db_write"}

// ToolAliases are the other names a registry tool answers to: `bash` is
// `shell`.
var ToolAliases = map[string]string{"bash": "shell"}

// CanonicalTool is the registry's name for a name a node's `tools:` or a
// model used: the alias's tool, or the name as given.
func CanonicalTool(name string) string {
	if canonical, ok := ToolAliases[name]; ok {
		return canonical
	}
	return name
}

// CheckNodeFields checks the node fields the engine and the runner act on
// beyond the graph: the fields fieldRules lists sit on the node types they
// apply to and hold what their checks accept (`tools:` a list of ToolNames,
// `min_tool_calls:` a positive integer on a node that has a tool, `role:`
// a role, `ttl:` a positive duration), an `on_reject:` block's `role:` is a
// repair role (checkRepairRole), `join:` is `settled` when set, `accept:` is
// as checkAccept reads it, and `fan_limit:` is a positive integer on a
// parallel_fan that also names `fan_overflow:`, the state key the items past
// the limit are written to (checkFanLimit). A command node holds what
// validateCommandNode checks, and a calibrate node what
// validateCalibrateNode checks. Validate reports these problems, and
// InitWorkflow refuses to start a run on one.
func CheckNodeFields(defn map[string]any) error {
	nodes, _ := defn["nodes"].(map[string]any)
	var problems []string
	for name, raw := range nodes {
		if node, ok := raw.(map[string]any); ok {
			problems = append(problems, nodeFieldProblems(name, node)...)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return errors.New(strings.Join(problems, "; "))
}

// nodeFieldChecks are the checks CheckNodeFields makes of every node, each
// given the node's name, its type (nodeType) and the node.
var nodeFieldChecks = []func(name, ntype string, node map[string]any) []string{
	checkTypeFields,
	checkFieldRules,
	checkRepairRole,
	checkJoin,
	func(name, _ string, node map[string]any) []string { return checkAccept(name, node) },
	checkFanLimit,
}

// nodeFieldProblems is every problem nodeFieldChecks find in one node.
func nodeFieldProblems(name string, node map[string]any) []string {
	ntype := nodeType(node)
	var problems []string
	for _, check := range nodeFieldChecks {
		problems = append(problems, check(name, ntype, node)...)
	}
	return problems
}

// typeChecks are the checks of the node types that carry fields of their
// own.
var typeChecks = map[string]func(name string, node map[string]any) []string{
	"command":   validateCommandNode,
	"calibrate": validateCalibrateNode,
}

func checkTypeFields(name, ntype string, node map[string]any) []string {
	if check, ok := typeChecks[ntype]; ok {
		return check(name, node)
	}
	return nil
}

// modelNodeTypes are the nodes a routing profile routes, which call a model
// through a backend: tools:, role: and ttl: apply to them only.
var modelNodeTypes = []string{"agent", "parallel_fan"}

// fieldRule is the check of one node field: a node that sets it must be one
// of types, and value checks what it is set to.
type fieldRule struct {
	field string
	types []string
	value func(name string, v any, node map[string]any) []string
}

// fieldRules are the node fields that apply to some node types only.
var fieldRules = []fieldRule{
	{"tools", modelNodeTypes, checkToolsValue},
	{"min_tool_calls", []string{"agent"}, checkMinToolCallsValue},
	{"role", modelNodeTypes, checkRoleValue},
	{"ttl", modelNodeTypes, checkTTLValue},
}

func checkFieldRules(name, ntype string, node map[string]any) []string {
	var problems []string
	for _, r := range fieldRules {
		problems = append(problems, r.check(name, ntype, node)...)
	}
	return problems
}

func (r fieldRule) check(name, ntype string, node map[string]any) []string {
	v, has := node[r.field]
	switch {
	case !has:
		return nil
	case !slices.Contains(r.types, ntype):
		return problemf("node %q: %s: applies to %s nodes only, not %s", name, r.field, strings.Join(r.types, " and "), ntype)
	}
	return r.value(name, v, node)
}

// checkToolsValue checks a `tools:`: a list naming tools of ToolNames.
func checkToolsValue(name string, v any, _ map[string]any) []string {
	list, ok := v.([]any)
	if !ok {
		return problemf("node %q: tools: must be a list of tool names, got %v", name, v)
	}
	var problems []string
	for _, t := range list {
		if s, _ := t.(string); !slices.Contains(ToolNames, CanonicalTool(s)) {
			problems = append(problems, fmt.Sprintf("node %q: tools: names unknown tool %v (known: %s)", name, t, strings.Join(ToolNames, ", ")))
		}
	}
	return problems
}

// checkMinToolCallsValue checks a `min_tool_calls:`: a positive integer, on
// a node whose tools: offers a tool to call.
func checkMinToolCallsValue(name string, v any, node map[string]any) []string {
	if !isPositiveInt(v) {
		return problemf("node %q: min_tool_calls: must be a positive integer, got %v", name, v)
	}
	if tools := nodeTools(node); tools != nil && len(*tools) == 0 {
		return problemf("node %q: min_tool_calls: needs a tool, and tools: [] offers none", name)
	}
	return nil
}

// checkRoleValue checks a node's `role:`: one of models.Roles, and not a
// repair role, which goes on the node's on_reject: block.
func checkRoleValue(name string, v any, _ map[string]any) []string {
	s, _ := v.(string)
	switch {
	case slices.Contains(models.RepairRoles, s):
		return problemf("node %q: role: %s names a repair; it goes on the node's on_reject: block", name, s)
	case !slices.Contains(models.Roles, s):
		return problemf("node %q: role: %v is not a role (known: %s)", name, v, strings.Join(models.Roles, ", "))
	}
	return nil
}

// checkTTLValue checks a `ttl:`: a positive Go duration (NodeTTL).
func checkTTLValue(name string, _ any, node map[string]any) []string {
	if _, err := NodeTTL(node); err != nil {
		return problemf("node %q: %v", name, err)
	}
	return nil
}

// checkRepairRole checks the `role:` of a node's `on_reject:` block: one of
// models.RepairRoles, on a node a routing profile routes.
func checkRepairRole(name, ntype string, node map[string]any) []string {
	block, _ := node["on_reject"].(map[string]any)
	v, has := block["role"]
	if s, _ := v.(string); has && (!slices.Contains(modelNodeTypes, ntype) || !slices.Contains(models.RepairRoles, s)) {
		return problemf("node %q: on_reject: role: %v is not a repair role (known: %s)", name, v, strings.Join(models.RepairRoles, ", "))
	}
	return nil
}

// checkJoin checks a `join:`: JoinSettled, the one value.
func checkJoin(name, _ string, node map[string]any) []string {
	if j, has := node["join"]; has && j != JoinSettled {
		return problemf("node %q: join: %v is not a join; the one value is %s", name, j, JoinSettled)
	}
	return nil
}

// checkAccept checks a node's `accept:`: a list whose items are each a
// predicate string, or a map holding a non-empty `predicate` string and
// optionally a `reason` string, and no other key. AcceptItems skips any
// other item, so it would gate nothing.
func checkAccept(name string, node map[string]any) []string {
	raw := node["accept"]
	if raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return problemf("node %q: accept: must be a list of predicates, got %v", name, raw)
	}
	var problems []string
	for i, x := range list {
		problems = append(problems, acceptItemProblems(name, i, x)...)
	}
	return problems
}

// acceptItemProblems checks one accept: item: a predicate string, or a map
// acceptMapProblems accepts.
func acceptItemProblems(name string, i int, x any) []string {
	if isString(x) {
		return nil
	}
	m, ok := x.(map[string]any)
	if !ok {
		return problemf("node %q: accept: item %d must be a predicate string or a {predicate, reason} map, got %v", name, i, x)
	}
	return acceptMapProblems(name, i, m)
}

// acceptMapProblems checks an accept: item written as a map: a non-empty
// predicate string, a reason that is a string when it has one, and no other
// key.
func acceptMapProblems(name string, i int, m map[string]any) []string {
	var problems []string
	if p, _ := m["predicate"].(string); p == "" {
		problems = append(problems, fmt.Sprintf("node %q: accept: item %d has no predicate string", name, i))
	}
	if r, has := m["reason"]; has && !isString(r) {
		problems = append(problems, fmt.Sprintf("node %q: accept: item %d: reason must be a string, got %v", name, i, r))
	}
	return append(problems, unknownAcceptKeys(name, i, m)...)
}

// acceptKeys are the keys an accept: item written as a map may hold.
var acceptKeys = map[string]bool{"predicate": true, "reason": true}

func unknownAcceptKeys(name string, i int, m map[string]any) []string {
	var problems []string
	for k := range m {
		if !acceptKeys[k] {
			problems = append(problems, fmt.Sprintf("node %q: accept: item %d: unknown key %q (known: predicate, reason)", name, i, k))
		}
	}
	return problems
}

// checkFanLimit checks `fan_limit:` and `fan_overflow:`, which go together
// on a parallel_fan.
func checkFanLimit(name, ntype string, node map[string]any) []string {
	_, hasLimit := node["fan_limit"]
	_, hasOverflow := node["fan_overflow"]
	if !hasLimit && !hasOverflow {
		return nil
	}
	if ntype != "parallel_fan" {
		return problemf("node %q: fan_limit: and fan_overflow: apply to parallel_fan nodes only", name)
	}
	return fanLimitProblems(name, node)
}

// fanLimitProblems checks a parallel_fan's `fan_limit:`: present, a positive
// integer, with the fan_overflow: it needs.
func fanLimitProblems(name string, node map[string]any) []string {
	limit, has := node["fan_limit"]
	switch {
	case !has:
		return problemf("node %q: fan_overflow: without fan_limit:", name)
	case !isPositiveInt(limit):
		return problemf("node %q: fan_limit: must be a positive integer, got %v", name, limit)
	}
	return overflowKeyProblems(name, node)
}

// overflowKeyProblems checks the `fan_overflow:` a fan_limit: needs: the
// state key that lists the items past the limit.
func overflowKeyProblems(name string, node map[string]any) []string {
	overflow, has := node["fan_overflow"]
	if !has {
		return problemf("node %q: fan_limit: needs fan_overflow:, the state key that lists the items past the limit", name)
	}
	if s, _ := overflow.(string); s == "" {
		return problemf("node %q: fan_overflow: must name a state key, got %v", name, overflow)
	}
	return nil
}

// problemf is one problem, formatted.
func problemf(format string, args ...any) []string {
	return []string{fmt.Sprintf(format, args...)}
}

func isPositiveInt(v any) bool {
	n, ok := v.(int)
	return ok && n > 0
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

// NodeTTL reads a node's `ttl:`, the bound on each of its calls, as a Go
// duration such as 5m. Absent is 0, the runner's default; anything but a
// positive duration is an error.
func NodeTTL(node map[string]any) (time.Duration, error) {
	v, has := node["ttl"]
	if !has {
		return 0, nil
	}
	s, _ := v.(string)
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("ttl: %v is not a positive duration such as 5m or 90s", v)
	}
	return d, nil
}

// nodeTools reads a node's `tools:` allowlist. Absent is nil, so the node
// keeps every tool; a list, empty included, is the allowlist.
func nodeTools(node map[string]any) *[]string {
	raw, ok := node["tools"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if s, ok := t.(string); ok {
			out = append(out, s)
		}
	}
	return &out
}

// joinsSettled reports whether a node declares `join: settled`: it is ready
// once every predecessor has finished, completed, skipped, failed or
// rejected, and at least one completed.
func joinsSettled(node map[string]any) bool {
	j, _ := node["join"].(string)
	return j == JoinSettled
}

// JoinSettled is the `join:` value that lets a node run past a predecessor
// that failed or was rejected.
const JoinSettled = "settled"

// nodeType is a node's type, agent when it names none.
func nodeType(node map[string]any) string {
	if t, _ := node["type"].(string); t != "" {
		return t
	}
	return "agent"
}

// isDecisionNode reports whether a node of the definition, as it is
// written, is a decision.
func isDecisionNode(raw any) bool {
	node, _ := raw.(map[string]any)
	return stringField(node, "type") == "decision"
}

// nodePrompt is the prompt a node dispatches: its `prompt:`, else its
// `prompt_template:`. A parallel_fan's template is its per-item prompt:
// when fan_source resolves items, the runner dispatches one backend.Run per
// item.
func nodePrompt(node map[string]any) string {
	if p, _ := node["prompt"].(string); p != "" {
		return p
	}
	t, _ := node["prompt_template"].(string)
	return t
}

// foragerName is the forager a node produces the verdict of: its
// forager_name (the swarm generator sets it), else its name after a
// forager- prefix, else "".
func foragerName(nodeName string, node map[string]any) string {
	if name, _ := node["forager_name"].(string); name != "" {
		return name
	}
	if name, ok := strings.CutPrefix(nodeName, "forager-"); ok {
		return name
	}
	return ""
}

// stringField is a node field's string value, "" when it is not a string.
func stringField(node map[string]any, key string) string {
	s, _ := node[key].(string)
	return s
}

// stringsIn is the string elements of list, in order; nil when it holds
// none.
func stringsIn(list []any) []string {
	var out []string
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
