package workflow

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
)

// Validate validates a workflow YAML file. It returns the problems found,
// and no warnings.
func Validate(path string) ([]string, []string, error) {
	defn, err := LoadYAML(path)
	if err != nil {
		return nil, nil, err
	}
	return definitionProblems(path, defn), nil, nil
}

// definitionProblems is every problem Validate reports, in its order: the
// name and the nodes, each node's type and required fields, the reasoning
// levels, the output schemas, the node fields, then the graph.
func definitionProblems(path string, defn map[string]any) []string {
	nodes, _ := defn["nodes"].(map[string]any)
	problems := headerProblems(defn, nodes)
	problems = append(problems, nodeTypeProblems(nodes)...)
	problems = append(problems, errorText(CheckReasoning(defn))...)
	problems = append(problems, outputSchemaProblems(path)...)
	problems = append(problems, errorText(CheckNodeFields(defn))...)
	return append(problems, graphProblems(defn, nodes)...)
}

func headerProblems(defn, nodes map[string]any) []string {
	var problems []string
	if defn["name"] == nil {
		problems = append(problems, "Missing 'name' field")
	}
	if len(nodes) == 0 {
		problems = append(problems, "No nodes defined")
	}
	return problems
}

// errorText is err's message as a problem, none when err is nil.
func errorText(err error) []string {
	if err == nil {
		return nil
	}
	return []string{err.Error()}
}

// outputSchemaProblems reads the file again for OutputSchemas, which keeps
// each schema's property order; a file it cannot read gives none.
func outputSchemaProblems(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	_, err = OutputSchemas(string(raw))
	return errorText(err)
}

// validNodeTypes are the node types a workflow may use. A wave gate runs as
// a command node (`chb guard`), so there is no `gate` type.
var validNodeTypes = map[string]bool{"agent": true, "decision": true, "parallel_fan": true, "human_review": true, "command": true, "calibrate": true}

// requiredFields are the checks of the fields some node types require.
var requiredFields = map[string]func(name string, node, nodes map[string]any) []string{
	"decision":     decisionProblems,
	"agent":        agentProblems,
	"parallel_fan": fanPromptProblems,
}

// nodeTypeProblems checks each node's type and the fields its type requires,
// in name order. A node must name its type.
func nodeTypeProblems(nodes map[string]any) []string {
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(nodes)) {
		if node, ok := nodes[name].(map[string]any); ok {
			problems = append(problems, nodeProblems(name, node, nodes)...)
		}
	}
	return problems
}

func nodeProblems(name string, node, nodes map[string]any) []string {
	ntype, _ := node["type"].(string)
	var problems []string
	if !validNodeTypes[ntype] {
		problems = append(problems, fmt.Sprintf("Node %q: invalid type %q", name, ntype))
	}
	if check, ok := requiredFields[ntype]; ok {
		problems = append(problems, check(name, node, nodes)...)
	}
	return problems
}

// decisionProblems checks a decision's condition and its two branches.
// BuildGraph drops a branch whose target is not a node, and the intended
// target then runs as a start node.
func decisionProblems(name string, node, nodes map[string]any) []string {
	var problems []string
	if node["condition"] == nil {
		problems = append(problems, fmt.Sprintf("Decision node %q: missing 'condition'", name))
	}
	for _, key := range []string{"true_edge", "false_edge"} {
		problems = append(problems, branchProblems(name, key, node[key], nodes)...)
	}
	return problems
}

func branchProblems(name, key string, branch any, nodes map[string]any) []string {
	if branch == nil {
		return problemf("Decision node %q: missing '%s'", name, key)
	}
	target, _ := branch.(string)
	if _, ok := nodes[target]; !ok {
		return problemf("Decision node %q: %s %v names no node", name, key, branch)
	}
	return nil
}

func agentProblems(name string, node, _ map[string]any) []string {
	if node["prompt"] == nil {
		return problemf("Agent node %q: missing 'prompt'", name)
	}
	return nil
}

func fanPromptProblems(name string, node, _ map[string]any) []string {
	if node["prompt"] == nil && node["prompt_template"] == nil {
		return problemf("parallel_fan node %q: missing both 'prompt' and 'prompt_template' (one is required)", name)
	}
	return nil
}

// graphProblems checks a workflow with nodes for a graph that builds, a
// start node, and loops something can exit.
func graphProblems(defn, nodes map[string]any) []string {
	if len(nodes) == 0 {
		return nil
	}
	incoming, outgoing, err := BuildGraph(defn)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	if len(findStartNodes(incoming)) == 0 {
		problems = append(problems, "No start nodes found")
	}
	return append(problems, loopProblems(incoming, outgoing, decisionSet(nodes))...)
}

// decisionSet is the set of decision nodes, by their type in any case and
// spacing.
func decisionSet(nodes map[string]any) map[string]bool {
	isDecision := make(map[string]bool, len(nodes))
	for name, raw := range nodes {
		node, _ := raw.(map[string]any)
		if t, _ := node["type"].(string); strings.ToLower(strings.TrimSpace(t)) == "decision" {
			isDecision[name] = true
		}
	}
	return isDecision
}

// loopProblems checks the workflow's cycles. A cycle with a decision on it
// is the engine's retry loop and is fine. One without has no exit: it
// validated clean, initialised, and then ran to the iteration cap — a silent
// false success at exactly the point that exists to catch it. A cycle made
// only of decisions has no node to run (decisionCycleProblems). The engine
// re-runs a loop when the decision closing it takes the branch back to its
// head. A loop closed by any other edge has nothing to re-run it, and its
// head waits on its own tail.
func loopProblems(incoming map[string]map[string]bool, outgoing map[string][]Edge, isDecision map[string]bool) []string {
	if cyc := findCycle(outgoing, isDecision); len(cyc) > 0 {
		return []string{"Cycle with no decision node on it, so nothing can exit it: " + strings.Join(cyc, " → ")}
	}
	problems := decisionCycleProblems(outgoing, isDecision)
	for e := range backEdges(incoming, outgoing) {
		if !isDecision[e[0]] {
			problems = append(problems, fmt.Sprintf(
				"Loop %s → %s is closed by an edge from %q, which is not a decision; a loop must return through a decision's branch",
				e[0], e[1], e[0]))
		}
	}
	sort.Strings(problems)
	return problems
}

// decisionCycleProblems names a cycle made only of decision nodes, a
// decision whose branch is itself among them. Such a cycle runs no node, so
// no output can change the branches it takes: the engine would take them
// again within one call, and refuses the third time it comes round
// (resolveDecisionNodes).
func decisionCycleProblems(outgoing map[string][]Edge, isDecision map[string]bool) []string {
	if cyc := findCycle(outgoing, nodesOutside(outgoing, isDecision)); len(cyc) > 0 {
		return []string{"Cycle made only of decision nodes, so no node runs on it: " + strings.Join(cyc, " → ")}
	}
	return nil
}

// nodesOutside is the graph's nodes that set does not hold.
func nodesOutside(outgoing map[string][]Edge, set map[string]bool) map[string]bool {
	out := make(map[string]bool, len(outgoing))
	for n := range outgoing {
		if !set[n] {
			out[n] = true
		}
	}
	return out
}

// commandNodeKeys are the keys a command node may carry. Anything else —
// a prompt, a model, an on_reject block — would be read by nothing, and a
// node whose configuration is silently ignored is how the removed `gate`
// type came to check nothing.
var commandNodeKeys = map[string]bool{
	"type": true, "argv": true, "stdin": true, "outputs_from": true,
	"outputs": true, "accept": true, "state_updates": true, "max_retries": true,
	"ok_exit": true,
}

// CalibrateOutputs are the two outputs a calibrate node writes, in order:
// the drift reports counted, and the calibrated lens with the smallest
// weight ("" when none).
var CalibrateOutputs = []string{"calibration_drift_count", "lowest_calibrated_lens"}

// calibrateNodeKeys are the keys a calibrate node may carry. It has no
// prompt, model or argv; an unknown key would be read by nothing.
var calibrateNodeKeys = map[string]bool{
	"type": true, "rebuild": true, "scope": true, "outputs": true, "accept": true, "state_updates": true,
}

// validateCalibrateNode checks one calibrate node: no key it would ignore,
// rebuild a boolean, scope a string, and outputs only the two it writes.
func validateCalibrateNode(name string, node map[string]any) []string {
	errs := unknownKeys("Calibrate", name, node, calibrateNodeKeys)
	if raw, bad := mistyped(node, "rebuild", isBool); bad {
		errs = append(errs, fmt.Sprintf("Calibrate node %q: 'rebuild' must be true or false, got %v", name, raw))
	}
	if raw, bad := mistyped(node, "scope", isString); bad {
		errs = append(errs, fmt.Sprintf("Calibrate node %q: 'scope' must be a string such as d1=2, got %v", name, raw))
	}
	return append(errs, calibrateOutputProblems(name, node)...)
}

// calibrateOutputProblems checks a calibrate node's outputs: a list naming
// only outputs it writes (CalibrateOutputs).
func calibrateOutputProblems(name string, node map[string]any) []string {
	outs, has := node["outputs"]
	list, isList := outs.([]any)
	switch {
	case !has:
		return nil
	case !isList:
		return problemf("Calibrate node %q: 'outputs' must be a list, got %v", name, outs)
	}
	return unwrittenOutputs(name, list)
}

// unwrittenOutputs names each output a calibrate node lists that is not one
// it writes.
func unwrittenOutputs(name string, list []any) []string {
	var errs []string
	for _, o := range list {
		if s, _ := o.(string); !slices.Contains(CalibrateOutputs, s) {
			errs = append(errs, fmt.Sprintf("Calibrate node %q: output %v is not one it writes (%s, %s)", name, o, CalibrateOutputs[0], CalibrateOutputs[1]))
		}
	}
	return errs
}

// validateCommandNode checks one command node: a non-empty argv of strings,
// no key it would ignore, outputs_from only as stdout_json, outputs only
// with outputs_from, and ok_exit as a non-empty list of exit codes.
func validateCommandNode(name string, node map[string]any) []string {
	errs := unknownKeys("Command", name, node, commandNodeKeys)
	errs = append(errs, argvProblems(name, node)...)
	if _, bad := mistyped(node, "stdin", isString); bad {
		errs = append(errs, fmt.Sprintf("Command node %q: 'stdin' must be a string", name))
	}
	errs = append(errs, commandOutputProblems(name, node)...)
	if raw, bad := mistyped(node, "ok_exit", isExitCodeList); bad {
		errs = append(errs, fmt.Sprintf("Command node %q: ok_exit must be a non-empty list of exit codes from 0 to 255, got %v", name, raw))
	}
	return errs
}

// argvProblems checks a command node's argv: a non-empty list of non-empty
// strings.
func argvProblems(name string, node map[string]any) []string {
	argv, _ := node["argv"].([]any)
	if len(argv) == 0 {
		return problemf("Command node %q: missing 'argv' (a non-empty list)", name)
	}
	var errs []string
	for i, a := range argv {
		if s, _ := a.(string); s == "" {
			errs = append(errs, fmt.Sprintf("Command node %q: argv[%d] is not a non-empty string", name, i))
		}
	}
	return errs
}

// commandOutputProblems checks how a command node's outputs are read:
// outputs_from only as stdout_json, and outputs only with outputs_from.
func commandOutputProblems(name string, node map[string]any) []string {
	switch from, hasFrom := node["outputs_from"]; {
	case !hasFrom:
		return outputsWithoutFrom(name, node)
	case from != "stdout_json":
		return problemf("Command node %q: outputs_from %v is not stdout_json", name, from)
	}
	return nil
}

func outputsWithoutFrom(name string, node map[string]any) []string {
	if outs, _ := node["outputs"].([]any); len(outs) > 0 {
		return problemf("Command node %q: 'outputs' needs outputs_from: stdout_json", name)
	}
	return nil
}

// unknownKeys names, in sorted order, each key of a node that allowed does
// not hold. kind is the node's type as the messages name it.
func unknownKeys(kind, name string, node map[string]any, allowed map[string]bool) []string {
	var errs []string
	for _, k := range slices.Sorted(maps.Keys(node)) {
		if !allowed[k] {
			errs = append(errs, fmt.Sprintf("%s node %q: unknown key %q", kind, name, k))
		}
	}
	return errs
}

// mistyped reports whether node sets key to a value accept refuses, and the
// value.
func mistyped(node map[string]any, key string, accept func(any) bool) (any, bool) {
	raw, has := node[key]
	return raw, has && !accept(raw)
}

func isBool(v any) bool {
	_, ok := v.(bool)
	return ok
}

func isExitCodeList(v any) bool {
	_, ok := okExitCodes(v)
	return ok
}

// FanSourceProblems reports, sorted by node, each parallel_fan that would not
// fan out as written. Its fan_source must be a workflow input or a key that a
// node upstream of the fan declares in `outputs` or `state_updates`, else the
// fan runs once on its unsubstituted prompt. Upstream is over forward edges: a
// node reached only through a loop's back edge runs after the fan's first
// pass. The prompt the fan dispatches (`prompt`, else `prompt_template`) must
// hold its fan_placeholder, else every item gets the same prompt. A fan with
// no fan_source is not checked. When BuildGraph refuses the graph, nothing is
// checked and its error is returned.
func FanSourceProblems(defn map[string]any) ([]string, error) {
	fullIncoming, outgoing, err := BuildGraph(defn)
	if err != nil {
		return nil, err
	}
	nodes, _ := defn["nodes"].(map[string]any)
	inputs, _ := defn["inputs"].([]any)
	w := fanWiring{
		nodes:    nodes,
		incoming: withoutBackEdges(fullIncoming, backEdges(fullIncoming, outgoing)),
		inputs:   stringsIn(inputs),
	}
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(nodes)) {
		problems = append(problems, w.problems(name)...)
	}
	return problems, nil
}

// fanWiring is what FanSourceProblems reads of a workflow: its nodes, its
// forward edges and its inputs.
type fanWiring struct {
	nodes    map[string]any
	incoming map[string]map[string]bool
	inputs   []string
}

// problems checks one node when it is a parallel_fan with a fan_source.
func (w fanWiring) problems(name string) []string {
	node, _ := w.nodes[name].(map[string]any)
	src, _ := node["fan_source"].(string)
	if t, _ := node["type"].(string); t != "parallel_fan" || src == "" {
		return nil
	}
	return append(w.sourceProblems(name, src), placeholderProblems(name, node)...)
}

func (w fanWiring) sourceProblems(name, src string) []string {
	if slices.Contains(w.inputs, src) || w.declaredUpstream(name, src) {
		return nil
	}
	return problemf("parallel_fan %q: fan_source %q is not an input, and no node upstream of it declares it in outputs or state_updates", name, src)
}

// declaredUpstream reports whether a node upstream of name, over forward
// edges, declares key.
func (w fanWiring) declaredUpstream(name, key string) bool {
	seen := map[string]bool{}
	stack := []string{name}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		fresh, found := w.unseenPredecessors(n, key, seen)
		if found {
			return true
		}
		stack = append(stack, fresh...)
	}
	return false
}

// unseenPredecessors marks n's predecessors seen and returns those not seen
// before, or reports that one of them declares key.
func (w fanWiring) unseenPredecessors(n, key string, seen map[string]bool) ([]string, bool) {
	var fresh []string
	for p := range w.incoming[n] {
		if seen[p] {
			continue
		}
		seen[p] = true
		if w.declares(p, key) {
			return nil, true
		}
		fresh = append(fresh, p)
	}
	return fresh, false
}

// declares reports whether a node lists key in its outputs or its
// state_updates.
func (w fanWiring) declares(nodeName, key string) bool {
	node, _ := w.nodes[nodeName].(map[string]any)
	outs, _ := node["outputs"].([]any)
	updates, _ := node["state_updates"].(map[string]any)
	_, inUpdates := updates[key]
	return slices.Contains(outs, any(key)) || inUpdates
}

func placeholderProblems(name string, node map[string]any) []string {
	placeholder := fanPlaceholder(node)
	if !strings.Contains(nodePrompt(node), placeholder) {
		return problemf("parallel_fan %q: its prompt has no %s, so every item gets the same prompt", name, placeholder)
	}
	return nil
}
