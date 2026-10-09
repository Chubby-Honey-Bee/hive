package runner

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

// The outcomes of a constraint probe.
const (
	// ProbeEnforced: the reply was the one object the schema allows, in
	// either key order, which a prose prompt yields only under constrained
	// decoding.
	ProbeEnforced = "enforced"
	// ProbeNotEnforced: the reply was text the schema does not allow.
	ProbeNotEnforced = "not enforced"
	// ProbeNoAnswer: the call failed, or the reply was empty or cut off
	// before it showed either way.
	ProbeNoAnswer = "no answer"
)

// The node records, in workflow_node_states.schema_enforcement.
const (
	SchemaEnforcedAtDecode = "enforced at decode"
	SchemaPostHocOnly      = "post-hoc only"
)

// The key order an enforced probe reply came back in.
const (
	// OrderDeclared: the keys came in the order the schema declares them,
	// so the server built its grammar from the schema as sent.
	OrderDeclared = "declared order"
	// OrderSorted: the keys came sorted, so the server re-encoded the schema
	// through a map first (Ollama's native-chat path does), and a node's
	// properties reach decoding in alphabetical order.
	OrderSorted = "keys sorted"
)

// probeConstant and orderConstant are the only values the probe's schema
// allows.
const (
	probeConstant = "hive-constraint-ok"
	orderConstant = "second"
)

// probePrompt asks for prose, so a reply holding probeConstant came from the
// schema, not the prompt.
const probePrompt = "Describe today's weather in two sentences of plain prose."

// probeSchemaJSON allows exactly {"probe":"hive-constraint-ok","order":
// "second"}. Both keys are required, so a grammar fixes their order, and
// they are declared out of alphabetical order, so the reply shows whether
// the server kept the declared order or sorted it. It is written out as
// text: a Go map would sort it.
const probeSchemaJSON = `{"type":"object","properties":{"probe":{"type":"string","const":"` + probeConstant +
	`"},"order":{"type":"string","const":"` + orderConstant + `"}},"required":["probe","order"],"additionalProperties":false}`

// ConstraintProbe is what one probe found: whether an endpoint constrained a
// model's decoding, at a reasoning level, to a schema sent as response_format,
// and in which key order.
type ConstraintProbe struct {
	// Provider is the provider kind whose endpoint, BaseURL, was asked.
	Provider  BackendKind
	BaseURL   string
	Model     string
	Reasoning string // "" when the nodes set none
	Outcome   string // ProbeEnforced, ProbeNotEnforced or ProbeNoAnswer
	Order     string // OrderDeclared or OrderSorted when enforced, else ""
	Detail    string // what the reply held, or why there was none
	// Nodes send this combination a schema: "<node>", or "<node> on_reject"
	// for its repair. Sorted.
	Nodes []string
	// The probe call's usage, and whether the server answered it at all.
	InputTokens, OutputTokens int64
	Answered                  bool
}

// Enforced reports whether the probe showed constrained decoding.
func (p ConstraintProbe) Enforced() bool { return p.Outcome == ProbeEnforced }

// Summary is one line for the run log and chb preflight.
func (p ConstraintProbe) Summary() string {
	r := p.Reasoning
	if r == "" {
		r = "unset"
	}
	s := fmt.Sprintf("%s (reasoning %s): %s", p.Model, r, p.Outcome)
	if p.Order != "" {
		s += ", " + p.Order
	}
	if p.Detail != "" {
		s += " — " + p.Detail
	}
	return s + " [" + strings.Join(p.Nodes, ", ") + "]"
}

type probeKey struct{ base, model, reasoning string }

func (p ConstraintProbe) key() probeKey { return probeKey{p.BaseURL, p.Model, p.Reasoning} }

// ProbeConstraints sends one small call per (endpoint, model, reasoning) that
// serves a node with an output_schema on the OpenAI-compatible backend, or
// that node's on_reject repair, and reports whether each constrained its
// decoding to the schema, and in which key order. It asks OPENAI_BASE_URL
// only when that is set, as the model preflight does, and the local
// endpoint (HIVE_LOCAL_BASE_URL) whenever a node runs on the local
// provider; never a provider the allowlist refuses. Models resolve as
// PreflightEndpointModels resolves them. The call asks for prose and sends
// probeSchemaJSON, with strict false, the weakest form a node's schema is
// sent in, and a cap of 64 output tokens at reasoning none, else 1024, room
// for a thinking model to finish thinking. A failed probe is an outcome, not
// an error: it never stops a run. logf, when set, is told as each probe is
// sent, since on a local endpoint a cold model load makes one take a while,
// and when a probe does not send its reasoning level. It returns nil when
// there is nothing to probe.
func ProbeConstraints(ctx context.Context, cfg Config, defn map[string]any, logf func(string, ...any)) []ConstraintProbe {
	var out []ConstraintProbe
	for _, kind := range []BackendKind{BackendOpenAI, BackendLocal} {
		out = append(out, probeEndpoint(ctx, cfg, defn, kind, logf)...)
	}
	return out
}

// probeEndpoint is ProbeConstraints for the nodes one OpenAI-compatible
// provider kind serves, at its endpoint.
func probeEndpoint(ctx context.Context, cfg Config, defn map[string]any, target BackendKind, logf func(string, ...any)) []ConstraintProbe {
	if !endpointProbed(target) {
		return nil
	}
	targets := &probeTargets{base: endpointFor(target), target: target, mode: cfg.BudgetMode, nodes: map[probeKey][]string{}}
	forEachAgentNode(defn, runDefaultKind(cfg), targets.add)
	if len(targets.nodes) == 0 {
		return nil
	}
	b, err := newOpenAICompatible(target, cfg)
	if err != nil {
		return nil
	}
	return sendConstraintProbes(ctx, b, target, targets.nodes, logf)
}

// endpointProbed reports whether the target kind's endpoint is asked:
// OPENAI_BASE_URL only when that is set, the local endpoint always.
func endpointProbed(target BackendKind) bool {
	return target != BackendOpenAI || strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")) != ""
}

// probeTargets collects the nodes one OpenAI-compatible provider kind
// (target) serves at its endpoint (base) that send a schema, and their
// repairs, by the (endpoint, model, reasoning) each call sends.
type probeTargets struct {
	base   string
	target BackendKind
	mode   BudgetMode
	nodes  map[probeKey][]string
}

// add files a node that sends a schema, served by kind, under the model its
// call sends, and its on_reject repair under the model the block names.
func (t *probeTargets) add(name string, node map[string]any, kind BackendKind) {
	if _, has := node["output_schema"]; !has || kind != t.target {
		return
	}
	reasoning, _ := node["reasoning"].(string)
	t.file(probeKey{t.base, probeNodeModel(node, t.mode, t.target), reasoning}, name)
	if rm := probeRepairModel(node, t.mode, t.target); rm != "" {
		t.file(probeKey{t.base, nodeModel(rm, "", t.mode, t.target), reasoning}, name+" on_reject")
	}
}

// file adds name to the nodes behind k.
func (t *probeTargets) file(k probeKey, name string) {
	t.nodes[k] = append(t.nodes[k], name)
}

// probeNodeModel is the model a node's call sends on the target kind, as
// PreflightEndpointModels resolves it: its model, else its tier, else
// sonnet.
func probeNodeModel(node map[string]any, mode BudgetMode, target BackendKind) string {
	model, _ := node["model"].(string)
	tier, _ := node["tier"].(string)
	if model == "" && tier == "" {
		model = "sonnet"
	}
	return nodeModel(model, tier, mode, target)
}

// probeRepairModel is the model a node's on_reject block names
// (repairModelFor), "" when the node has no block that allows an attempt,
// or one that names none and so sends the dispatch's model.
func probeRepairModel(node map[string]any, mode BudgetMode, target BackendKind) string {
	block, _ := node["on_reject"].(map[string]any)
	if block == nil || intFromMap(block, "max_repair_iterations", 3) <= 0 {
		return ""
	}
	return repairModelFor(block, mode, target)
}

// sendConstraintProbes sends one probe per key of nodes, by model then
// reasoning, on b, the backend of provider kind, and names the nodes behind
// each, sorted.
func sendConstraintProbes(ctx context.Context, b *OpenAIBackend, kind BackendKind, nodes map[probeKey][]string, logf func(string, ...any)) []ConstraintProbe {
	keys := slices.SortedFunc(maps.Keys(nodes), func(x, y probeKey) int {
		return cmp.Or(cmp.Compare(x.model, y.model), cmp.Compare(x.reasoning, y.reasoning))
	})
	out := make([]ConstraintProbe, 0, len(keys))
	for _, k := range keys {
		names := nodes[k]
		sort.Strings(names)
		logProbeAsk(logf, k, names)
		p := b.probeConstraint(ctx, k.model, k.reasoning, logf)
		p.Provider, p.BaseURL, p.Nodes = kind, k.base, names
		out = append(out, p)
	}
	return out
}

// logProbeAsk tells logf, when set, that a probe is sent, for the nodes
// behind it: on a local endpoint a cold model load makes one take a while.
func logProbeAsk(logf func(string, ...any), k probeKey, names []string) {
	if logf == nil {
		return
	}
	logf("constraint probe: asking %s (reasoning %s) for %s", k.model, cmp.Or(k.reasoning, "unset"), strings.Join(names, ", "))
}

// probeConstraint sends the probe and classifies the reply. It sends the
// reasoning level as a node's call would (reasoningToSend), so a call to a
// model the endpoint reports cannot think carries no reasoning_effort. The
// cap is 64 tokens when no thinking comes first, that is when none is sent
// or the model cannot think, else 1024, room to finish thinking.
func (b *OpenAIBackend) probeConstraint(ctx context.Context, model, reasoning string, logf func(string, ...any)) ConstraintProbe {
	p := ConstraintProbe{Model: model, Reasoning: reasoning}
	req := constraintProbeRequest(ctx, b, model, reasoning, logf)
	var usage RunResult
	choice, _, err := b.chatTurn(ctx, req, constraintProbeBody(model, req), &usage)
	p.InputTokens, p.OutputTokens, p.Answered = usage.InputTokens, usage.OutputTokens, usage.Turns > 0
	if err != nil {
		p.Outcome, p.Detail = ProbeNoAnswer, err.Error()
		return p
	}
	p.Outcome, p.Order, p.Detail = classifyProbe(choice, req.MaxTokens)
	return p
}

// constraintProbeRequest is the probe's request on b: the reasoning level
// a node's call would send, the note on a level it does not send told to
// logf when set, and the output cap.
func constraintProbeRequest(ctx context.Context, b *OpenAIBackend, model, reasoning string, logf func(string, ...any)) RunRequest {
	req := RunRequest{Model: model, MaxTokens: 1024, TTL: 5 * time.Minute}
	var note string
	req.Reasoning, note = b.reasoningToSend(ctx, model, reasoning)
	if note != "" && logf != nil {
		logf("%s", note)
	}
	if constraintProbeUnthinking(ctx, b, req.Reasoning, reasoning, model) {
		req.MaxTokens = 64
	}
	return req
}

// constraintProbeUnthinking reports whether no thinking comes before the
// probe's answer: the level sent is none, or a level asked is not sent to
// a model the endpoint reports cannot think.
func constraintProbeUnthinking(ctx context.Context, b *OpenAIBackend, sent, asked, model string) bool {
	return sent == "none" || asked != "" && b.thinking(ctx, model) == thinkingNo
}

// constraintProbeBody is the probe's chat body: the prose prompt, with
// probeSchemaJSON as its response_format.
func constraintProbeBody(model string, req RunRequest) map[string]any {
	body := chatBody(model, []map[string]any{{"role": "user", "content": probePrompt}}, req)
	body["response_format"] = map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "hive_constraint_probe",
			"schema": json.RawMessage(probeSchemaJSON),
			"strict": false,
		},
	}
	return body
}

// classifyProbe reads a probe's reply. Constrained decoding writes the one
// allowed object, and its first character is `{`: a reply starting with
// anything else was not constrained, even one cut off. A cut-off reply that
// starts with `{` cannot tell. For the allowed object, order is the order its
// keys came in.
func classifyProbe(choice openaiChoice, maxTokens int64) (outcome, order, detail string) {
	content := strings.TrimSpace(choice.Message.Content)
	switch {
	case content == "":
		return classifyEmptyProbeReply(choice)
	case !strings.HasPrefix(content, "{"):
		return ProbeNotEnforced, "", "the reply was " + quoteHead(content)
	}
	return classifyProbeObject(content, choice.FinishReason, maxTokens)
}

// classifyEmptyProbeReply reads a probe reply that holds no answer: one
// that held reasoning alone, or nothing at all.
func classifyEmptyProbeReply(choice openaiChoice) (outcome, order, detail string) {
	if r := strings.TrimSpace(choice.Message.thinking()); r != "" {
		return ProbeNoAnswer, "", fmt.Sprintf("the reply held %d bytes of reasoning and no answer (finish_reason %q)", len(r), choice.FinishReason)
	}
	return ProbeNoAnswer, "", fmt.Sprintf("empty reply (finish_reason %q)", choice.FinishReason)
}

// classifyProbeObject reads a probe reply that starts with `{`: the
// allowed object, in the key order it came in, or text that is not it,
// which a reply cut off at maxTokens cannot tell.
func classifyProbeObject(content, finishReason string, maxTokens int64) (outcome, order, detail string) {
	if isProbeObject(content) {
		return ProbeEnforced, probeKeyOrder(content), ""
	}
	if finishReason == "length" {
		return ProbeNoAnswer, "", fmt.Sprintf("the reply was cut off at %d tokens: %s", maxTokens, quoteHead(content))
	}
	return ProbeNotEnforced, "", "the reply was " + quoteHead(content)
}

// isProbeObject reports whether content is the one object the probe's
// schema allows.
func isProbeObject(content string) bool {
	var obj map[string]any
	return json.Unmarshal([]byte(content), &obj) == nil && len(obj) == 2 && obj["probe"] == probeConstant && obj["order"] == orderConstant
}

// probeKeyOrder is the order the allowed object's keys came in.
func probeKeyOrder(content string) string {
	if firstKey(content) == "probe" {
		return OrderDeclared
	}
	return OrderSorted
}

// firstKey is the first key of the JSON object text holds, or "".
func firstKey(text string) string {
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return ""
	}
	k, _ := dec.Token()
	s, _ := k.(string)
	return s
}

// quoteHead is the start of s, quoted, for a one-line detail.
func quoteHead(s string) string {
	return fmt.Sprintf("%q", truncate(strings.Join(strings.Fields(s), " "), 80))
}

// schemaEnforcement is what a node records for the accepted answer to its
// schema, and why: enforced at decode only when the answer came from a call
// that sent the schema (atDecode) to a combination the probe showed enforced.
func schemaEnforcement(probes map[probeKey]ConstraintProbe, kind BackendKind, baseURL, model, reasoning string, atDecode bool) (value, why string) {
	switch {
	case !openAICompatible(kind):
		return SchemaPostHocOnly, schemaUnsentReason(kind)
	case !atDecode:
		return SchemaPostHocOnly, "the answer came from a call that offered tools, which carries no schema, and it held the schema"
	}
	return probedSchemaEnforcement(probes, probeKey{baseURL, model, reasoning})
}

// schemaUnsentReason says why a backend that is not OpenAI-compatible held
// no answer to a schema at decode: it sends none.
func schemaUnsentReason(kind BackendKind) string {
	if kind == "" {
		return "the backend sends no schema"
	}
	return fmt.Sprintf("the %s backend sends no schema", kind)
}

// probedSchemaEnforcement is what a node records for an answer from a call
// that sent its schema: enforced at decode only when the probe of its
// combination (k) showed it enforced.
func probedSchemaEnforcement(probes map[probeKey]ConstraintProbe, k probeKey) (value, why string) {
	p, ok := probes[k]
	switch {
	case !ok:
		return SchemaPostHocOnly, fmt.Sprintf("no constraint probe ran for %s", k.model)
	case p.Enforced():
		return SchemaEnforcedAtDecode, fmt.Sprintf("the constraint probe showed %s enforces it, %s", k.model, p.Order)
	}
	why = fmt.Sprintf("the constraint probe found %s %s", k.model, p.Outcome)
	if p.Detail != "" {
		why += ": " + p.Detail
	}
	return SchemaPostHocOnly, why
}
