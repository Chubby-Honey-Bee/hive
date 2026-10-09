package workflow

import (
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/schema"
)

// DispatchNode is a node ready for dispatch.
type DispatchNode struct {
	Node  string `json:"node"`
	Type  string `json:"type"`
	Agent string `json:"agent"`
	Model string `json:"model"`
	// Tier names a progressive cost-tier role
	// (planner|synthesist|worker|verifier). Resolved to a concrete
	// model id at dispatch by the runner, taking the active BudgetMode
	// into account. Ignored when Model is set explicitly.
	Tier string `json:"tier,omitempty"`
	// Provider, if non-empty, overrides the runtime provider for this
	// node only. Empty ⇒ inherit from the
	// agent-run's --provider flag, HIVE_PROVIDER env, or auto-detect.
	Provider string `json:"provider,omitempty"`
	// Reasoning is the node's `reasoning:` level (none|low|medium|high),
	// checked by CheckReasoning before a run starts. Empty sends none.
	Reasoning      string   `json:"reasoning,omitempty"`
	ResolvedPrompt string   `json:"resolved_prompt"`
	Outputs        []string `json:"outputs,omitempty"`
	// Placeholders are the tokens of the node's template the engine left
	// as written in ResolvedPrompt, where the runner fills {comb.…},
	// {cde.…} and a fan's item placeholder, and nowhere else (FillLeft).
	// Nil on a node the engine did not hand out.
	Placeholders []Placeholder `json:"-"`
	// CombVantage names the Comb region or forager vantage substituted for
	// `{comb.region}` / inline `{comb.<key>}` tokens during dispatch.
	CombVantage string `json:"comb_vantage,omitempty"`
	// FanItems, when non-empty, marks this node as a parallel_fan
	// expansion: the runner will dispatch one backend.Run call per
	// item, in parallel, substituting each item into ResolvedPrompt
	// at the FanItemPlaceholder ("{item}" by default). The returned
	// outputs are concatenated into the parent's single RunResult so
	// the rest of the workflow engine sees one node, one output —
	// the parallelism is internal to the dispatch step.
	FanItems []string `json:"fan_items,omitempty"`
	// FanItemPlaceholder is the token in ResolvedPrompt that gets
	// replaced per-item. Defaults to "{item}". Workflow authors can
	// override by setting `fan_placeholder:` in the YAML.
	FanItemPlaceholder string `json:"fan_item_placeholder,omitempty"`
	// FanEmpty marks a parallel_fan whose fan_source holds a list or a
	// string with no items. The runner completes it without a backend
	// call. When state holds no list or string under fan_source, it
	// stays false and the node runs as one ordinary call on its prompt.
	FanEmpty bool `json:"fan_empty,omitempty"`
	// Archetype, if non-empty, marks this node as a non-LLM dispatch.
	// "dreamer" routes to internal/dreamer.Run instead of an LLM
	// backend. Lens nodes (the default) leave this empty.
	Archetype string `json:"archetype,omitempty"`
	// ForagerName, if non-empty, names the forager whose verdict this
	// node will produce. Set automatically by the swarm generator
	// for `forager-<name>` nodes. The runner uses this to write the
	// forager's verdict to the Comb at vantage `forager:<name>` after
	// the node completes.
	ForagerName string `json:"forager_name,omitempty"`
	// VerifyTests, when true, instructs the runner to independently run `go
	// test` against any package the agent edited and override the agent's
	// reported `tests_pass` output based on the actual exit code, so an
	// agent cannot report `tests_pass=true` without the tests passing. Set
	// automatically by the implement loop's workflow generator on every
	// fix-N node.
	VerifyTests bool `json:"verify_tests,omitempty"`
	// OutputSchema is the node's output_schema, with its properties in the
	// order the YAML lists them; nil when it has none. The OpenAI-compatible
	// backend sends it as response_format, and CompleteNode checks a node's
	// outputs against it, a fan's as one reply. The runner checks each fan
	// item's reply and completes the fan with CompleteFanItems.
	OutputSchema *schema.Schema `json:"output_schema,omitempty"`
	// Tools is the node's `tools:` allowlist. nil means the node declares
	// none and keeps every tool; a list, empty included, is the only tools
	// the node's backend call may offer.
	Tools *[]string `json:"tools,omitempty"`
	// MinToolCalls is the node's min_tool_calls: a call that made fewer
	// tool calls fails the node. 0 means no minimum.
	MinToolCalls int `json:"min_tool_calls,omitempty"`
	// TTL is the node's `ttl:`, the bound on each of its calls (NodeTTL);
	// 0 leaves the runner's default. The runner's alone, so not handed out.
	TTL time.Duration `json:"-"`
	// Argv is a command node's program and arguments, templated from state.
	// The runner executes it with no shell and no model.
	Argv []string `json:"argv,omitempty"`
	// Stdin is a command node's templated standard input, if it has one.
	Stdin string `json:"stdin,omitempty"`
	// OutputsFrom is "stdout_json" when a command node reads its outputs
	// from stdout, and empty when it produces none.
	OutputsFrom string `json:"outputs_from,omitempty"`
	// OkExit is a command node's ok_exit: the exit codes that complete it.
	// nil means 0 alone.
	OkExit []int `json:"ok_exit,omitempty"`
	// CommandError, when set, is why a command node cannot run as given: a
	// placeholder no state value fills, or an argv element that is not a
	// string. The runner fails the node with it and runs nothing.
	CommandError string `json:"command_error,omitempty"`
	// Rebuild is a calibrate node's rebuild: recompute even when no outcome
	// was recorded since the last calibrate tick.
	Rebuild bool `json:"rebuild,omitempty"`
	// Scope is a calibrate node's scope, the one its outputs count, and
	// ScopeSet whether the node named one; "" set is the global scope, and
	// unset is every scope.
	Scope    string `json:"scope,omitempty"`
	ScopeSet bool   `json:"scope_set,omitempty"`
}

// dispatchNode is the DispatchNode for a ready node: its fields as the
// definition gives them, its prompt resolved from state and the run tokens,
// and the fields its type carries (typeFields).
func (p *pass) dispatchNode(name string, node map[string]any) DispatchNode {
	ntype := nodeType(node)
	resolved, placeholders := resolveNodePrompt(nodePrompt(node), p.state, p.repo, p.runID, p.defn)
	outputs, _ := node["outputs"].([]any)
	d := DispatchNode{
		Node:           name,
		Type:           ntype,
		Agent:          stringField(node, "agent"),
		Model:          dispatchModel(ntype, node),
		Tier:           stringField(node, "tier"),
		Provider:       stringField(node, "provider"),
		Reasoning:      stringField(node, "reasoning"),
		ResolvedPrompt: resolved,
		Placeholders:   placeholders,
		CombVantage:    stringField(node, "comb_vantage"),
		// Case- and space-normalised, so `archetype: Dreamer` reaches the
		// dreamer branch, not an LLM.
		Archetype:    strings.ToLower(strings.TrimSpace(stringField(node, "archetype"))),
		ForagerName:  foragerName(name, node),
		OutputSchema: p.schemas[name],
		Tools:        nodeTools(node),
		Outputs:      stringsIn(outputs),
	}
	d.VerifyTests, _ = node["verify_tests"].(bool)
	d.MinToolCalls, _ = node["min_tool_calls"].(int)
	d.TTL, _ = NodeTTL(node)
	if fill, ok := typeFields[ntype]; ok {
		fill(&d, node, p.state)
	}
	return d
}

// typeFields fill the DispatchNode fields that one node type carries.
var typeFields = map[string]func(d *DispatchNode, node, state map[string]any){
	"parallel_fan": setFanFields,
	"command":      setCommandFields,
	"calibrate":    setCalibrateFields,
}

// setCommandFields fills a command node's argv and stdin, templated from
// state, and how its outputs and exit codes are read.
func setCommandFields(d *DispatchNode, node, state map[string]any) {
	d.Argv, d.Stdin, d.CommandError = commandInvocation(node, state)
	d.OutputsFrom, _ = node["outputs_from"].(string)
	d.OkExit, _ = okExitCodes(node["ok_exit"])
}

// setCalibrateFields fills a calibrate node's rebuild and scope.
func setCalibrateFields(d *DispatchNode, node, _ map[string]any) {
	d.Rebuild, _ = node["rebuild"].(bool)
	scope, has := node["scope"]
	d.Scope, _ = scope.(string)
	d.ScopeSet = has
}

// noDefaultModel are the node types that run no model, so get no default.
var noDefaultModel = map[string]bool{"command": true, "calibrate": true}

// dispatchModel is the node's model: as written, or sonnet for a node that
// names neither a model nor a tier. A `tier:` resolves to a model id at
// dispatch, taking the budget mode into account; an explicit `model:` always
// wins, so an author who pins a model still gets it.
func dispatchModel(ntype string, node map[string]any) string {
	model, _ := node["model"].(string)
	tier, _ := node["tier"].(string)
	if model == "" && tier == "" && !noDefaultModel[ntype] {
		return "sonnet"
	}
	return model
}
