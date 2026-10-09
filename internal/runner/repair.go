package runner

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// defaultRepairPromptTemplate is the repair prompt of an on_reject: block
// that names none. The failure comes again at the end, after the original
// prompt and the failed outputs, so the last thing the model reads is what
// to fix.
const defaultRepairPromptTemplate = "The previous attempt failed acceptance criteria:\n{accept_failure}\n\nOriginal prompt:\n{original_prompt}\n\nOutputs that failed:\n{outputs}\n\nWhat failed: {accept_failure}\nFix only what failed. Keep the same output schema."

// tryRepair runs the on_reject: bounded repair loop for a node whose
// CompleteNode returned an *AcceptRejection. Returns the repaired
// outputs, the accepted attempt's text, the attempts' spend and ok=true on
// success; returns nil, "", the spend and false on exhaustion.
//
// Each iteration:
//  1. Records a workflow_repairs row (start).
//  2. Builds a repair prompt by interpolating {accept_failure},
//     {original_prompt}, {outputs} into the YAML's prompt_template.
//  3. Calls backend.Run with the repair prompt, with the node's tools.
//  4. On a node with min_tool_calls, counts the attempt's tool calls with
//     the dispatch's and the earlier attempts'; while they fall short, the
//     attempt is rejected with the count and the next one is asked to do
//     the work.
//  5. Re-attempts CompleteNode with the new outputs — if it succeeds
//     (no AcceptRejection), the parent node is now "completed" and we
//     return the repaired outputs.
//  6. Marks the repair row completed (accept_passed=1 on success).
//
// Tokens + cost from each repair attempt that returned a result, even
// with a backend error, accumulate on the parent node row and the run
// totals (so `chb run-totals` and the cost cap see all spent budget), and are
// also recorded per-attempt on the workflow_repairs row. They are also
// summed into the returned repairSpend, with the passing attempt's model,
// for the node's completion events. Each attempt's tool invocations join
// the node's tool_invocations audit trail.
func (rc *runtimeContext) tryRepair(node workflow.DispatchNode, original *RunResult, rej *workflow.AcceptRejection) (map[string]any, string, repairSpend, bool) {
	loop := rc.newRepairLoop(node, original, rej)
	if loop == nil {
		return nil, "", repairSpend{}, false
	}
	return loop.run()
}

// newRepairLoop is the repair loop of the node's on_reject: block, nil when
// the node has none or its block allows no attempt.
func (rc *runtimeContext) newRepairLoop(node workflow.DispatchNode, original *RunResult, rej *workflow.AcceptRejection) *repairLoop {
	block := workflow.NodeOnRejectBlock(rc.parsedDefn, node.Node)
	if block == nil {
		return nil
	}
	maxAttempts := intFromMap(block, "max_repair_iterations", 3)
	if maxAttempts <= 0 {
		return nil
	}
	return &repairLoop{
		rc:          rc,
		node:        node,
		block:       block,
		tmpl:        repairPromptTemplate(block),
		maxAttempts: maxAttempts,
		made:        runInvocationCount(original),
		rej:         rej,
		trigger:     "accept",
	}
}

// repairPromptTemplate is the block's prompt_template, else the default.
func repairPromptTemplate(block map[string]any) string {
	if tmpl, _ := block["prompt_template"].(string); tmpl != "" {
		return tmpl
	}
	return defaultRepairPromptTemplate
}

// runInvocationCount is the tool calls r's run made, none without a result.
func runInvocationCount(r *RunResult) int {
	if r == nil {
		return 0
	}
	return len(r.Invocations)
}

// repairLoop is one node's on_reject: repair: its block's settings, and
// what its attempts have done so far.
type repairLoop struct {
	rc          *runtimeContext
	node        workflow.DispatchNode
	block       map[string]any
	tmpl        string
	maxAttempts int

	// made is the tool calls the node's calls have made so far: the
	// dispatch's, then each attempt's as it answers.
	made int
	// rej is the rejection the next attempt repairs, and trigger what
	// started it: the first repairs an accept: rejection.
	rej     *workflow.AcceptRejection
	trigger string
	// spend is the attempts' spend, with the passing attempt's model.
	spend repairSpend
	// passed is whether an attempt passed, and outputs and text are its
	// answer.
	passed  bool
	outputs map[string]any
	text    string
}

// run makes the loop's attempts until one passes, a failure stops the
// loop, or the attempts run out, and returns the passing attempt's outputs
// and text, the attempts' spend, and whether one passed. A cancelled run
// stops the loop before its next attempt.
func (l *repairLoop) run() (map[string]any, string, repairSpend, bool) {
	for n := 1; n <= l.maxAttempts; n++ {
		if l.rc.ctx.Err() != nil {
			return nil, "", l.spend, false
		}
		if l.attempt(n) {
			return l.outputs, l.text, l.spend, l.passed
		}
	}
	l.rc.logf("repair %s exhausted (%d attempts)", l.node.Node, l.maxAttempts)
	return nil, "", l.spend, false
}

// repairAttempt is one attempt of a repair loop: its number and prompt,
// the node's dispatch parameters, the model it is sent and the one the
// ledger records, its workflow_repairs row, its call's tokens and cost,
// its outputs as JSON, and when it ended.
type repairAttempt struct {
	n             int
	prompt        string
	p             dispatchParams
	sentModel     string
	ledgerModel   string
	id            int64
	in, out, cost int64
	outputsJSON   string
	now           string
}

// attempt runs attempt n and reports whether the loop is done: the attempt
// passed, or a failure stops the loop.
func (l *repairLoop) attempt(n int) (done bool) {
	a := l.newAttempt(n)
	if !l.record(a) {
		return true
	}
	runResult, runErr := l.send(a)
	l.charge(a, runResult)
	a.now = time.Now().UTC().Format(time.RFC3339)
	if runErr != nil {
		l.backendFailed(a, runErr)
		return false
	}
	return l.judge(a, runResult)
}

// newAttempt prepares attempt n: its prompt and the model it is sent.
//
// The node's `provider:` override applies to its repair too, so a node
// pinned to one provider has its repair served, and its metrics labelled, by
// that provider. The sampling fields (--deterministic, --seed,
// --temperature, --top-p) and the node's reasoning apply as they do to the
// dispatch.
func (l *repairLoop) newAttempt(n int) *repairAttempt {
	a := &repairAttempt{n: n, prompt: l.prompt()}
	a.p = l.rc.resolveDispatchParams(l.node)
	a.sentModel = repairSentModel(l.block, l.rc.cfg.BudgetMode, a.p)
	a.ledgerModel = resolveAliasForPricing(a.sentModel)
	return a
}

// prompt is the next attempt's prompt: the template with the failure, the
// original prompt and the outputs that failed in place, in one pass, so a
// placeholder inside the original prompt, such as a context pack naming
// {outputs}, stays as written. An item's reason is written for the model,
// so the model reads it alone; the predicate stays in failure_reason for
// the operator.
func (l *repairLoop) prompt() string {
	outputsJSON, _ := json.Marshal(l.rej.Outputs)
	return workflow.ResolveTemplate(l.tmpl, map[string]any{
		"accept_failure":  cmp.Or(l.rej.Reason, l.rej.Error()),
		"original_prompt": l.node.ResolvedPrompt,
		"outputs":         string(outputsJSON),
	})
}

// repairSentModel is the model a repair attempt is sent: the block's
// (repairModelFor), resolved by the provider that serves it, as a
// dispatch's is, so the ledger, the cap and the cost name the model the
// provider was sent: `sonnet` on an openai node is gpt-5.1. A block naming
// none sends the model the dispatch was sent, not the canonical id
// recorded for pricing, which need not be a name the provider serves.
func repairSentModel(block map[string]any, mode BudgetMode, p dispatchParams) string {
	repairModel := repairModelFor(block, mode, p.kind)
	switch {
	case repairModel == "":
		return p.model
	case p.kind == "":
		return repairModel
	}
	return resolveModelForProvider(p.kind, repairModel)
}

// record records the attempt's workflow_repairs row (start), and reports
// whether it could: a loop whose attempts cannot be recorded stops.
func (l *repairLoop) record(a *repairAttempt) bool {
	id, err := l.rc.store.Workflows().RecordRepairAttempt(
		l.rc.runID, l.node.Node, a.n, l.rej.Error(), a.prompt, a.ledgerModel, a.p.provider, l.trigger,
	)
	if err != nil {
		l.rc.logf("record repair attempt (node %s, try %d): %v", l.node.Node, a.n, err)
		return false
	}
	a.id = id
	l.rc.logf("repair %s attempt %d/%d (model=%s)", l.node.Node, a.n, l.maxAttempts, a.sentModel)
	return true
}

// send makes the attempt's call, with the node's tools under the node's
// budget, and logs its notes. The per-turn output cap resolves the same
// way a dispatch's does (dispatchMaxOutput), for the model the attempt is
// sent.
func (l *repairLoop) send(a *repairAttempt) (*RunResult, error) {
	rc, node := l.rc, l.node
	registry := rc.nodeRegistry(node)
	nodeCtx, cancel := context.WithTimeout(rc.ctx, max(30*time.Minute, node.TTL))
	maxOut := rc.dispatchMaxOutput(a.sentModel)
	window, windowSource := rc.contextWindowFor(a.p.kind, a.sentModel)
	runResult, runErr := a.p.backend.Run(nodeCtx, RunRequest{
		System:              "", // repair runs without an agent persona — keeps it cheap
		Prompt:              a.prompt,
		Model:               a.sentModel,
		TTL:                 callTTL(node),
		Registry:            registry,
		Temperature:         a.p.temp,
		TopP:                a.p.topP,
		Seed:                a.p.seed,
		MaxTokens:           maxOut,
		Reasoning:           node.Reasoning,
		Logf:                rc.logf,
		OutputSchema:        node.OutputSchema,
		SchemaName:          node.Node,
		ContextWindow:       window,
		ContextWindowSource: windowSource,
	})
	cancel()
	what := fmt.Sprintf("node %s repair attempt %d", node.Node, a.n)
	rc.noteFinalize(what, runResult)
	rc.noteContext(what, runResult)
	return runResult, runErr
}

// charge accounts for the attempt's call, on the parent node and the run
// totals, whether or not the backend also returned an error (a CLI hitting
// --max-turns reports usage and an error together): its tokens and cost
// (recordSpend), which the attempt's row records too, and its tool
// invocations, which join the node's audit trail and count toward its
// min_tool_calls.
func (l *repairLoop) charge(a *repairAttempt, r *RunResult) {
	if r == nil {
		return
	}
	rc := l.rc
	a.in, a.out = r.InputTokens, r.OutputTokens
	var credits int64
	a.cost, credits = rc.recordSpend(l.node, a.sentModel, a.p.provider, a.p.baseURL, r)
	rc.recordToolInvocations(l.node, r.Invocations)
	l.made += len(r.Invocations)
	rc.resMu.Lock()
	rc.res.InputTokens += a.in
	rc.res.OutputTokens += a.out
	rc.resMu.Unlock()
	l.spend.in += a.in
	l.spend.out += a.out
	l.spend.cost += a.cost
	l.spend.credits += credits
}

// backendFailed closes an attempt whose call returned an error, which
// becomes the rejection the next attempt repairs (trigger backend_error).
func (l *repairLoop) backendFailed(a *repairAttempt, runErr error) {
	l.rc.markRepair(a.id, "{}", false, a.in, a.out, a.cost, a.now)
	l.rc.logf("repair %s attempt %d backend error: %v", l.node.Node, a.n, runErr)
	l.rej = &workflow.AcceptRejection{
		NodeName:       l.node.Node,
		Predicate:      l.rej.Predicate,
		EvaluatedValue: l.rej.EvaluatedValue,
		EvalError:      runErr.Error(),
		Outputs:        l.rej.Outputs,
		Reason:         l.rej.Reason,
	}
	l.trigger = "backend_error"
}

// judge checks an answered attempt's reply and reports whether the loop is
// done. The attempt's outputs go on its row before the completion is
// re-tried, so even a successful retry leaves a row.
func (l *repairLoop) judge(a *repairAttempt, r *RunResult) (done bool) {
	outputs := workflow.ExtractJSONOutput(r.FinalText)
	outputsJSON, _ := json.Marshal(outputs)
	a.outputsJSON = string(outputsJSON)

	// A node with min_tool_calls completes only once the dispatch and
	// its attempts together made that many: an attempt that answers
	// without the work is sent back as the dispatch was, before its
	// answer is checked, and writes no verdict to the Comb.
	if l.shortOfToolCalls(r) {
		l.stillRejected(a, toolCallsShort(l.node, l.made, outputs))
		return false
	}

	// The dispatch path's Comb write fires on the first attempt, before
	// acceptance is known, so each repair attempt writes the vantage too: a
	// repaired node must not end `completed` while comb_state holds the
	// verdict that failed the accept gate, which a downstream `cites:`
	// forager reads.
	//
	// Before CompleteNode, not inside the success branch: writing after
	// would reopen the TOCTOU window dispatch.go closes deliberately.
	// Writing on every attempt that holds the node's output_schema means an
	// attempt accept: rejects overwrites too, but the last write preceding a
	// successful CompleteNode is the accepted one, and the extra rows are
	// honest comb_revisions history. An attempt that breaks the schema
	// writes nothing (writeForagerVantage).
	if l.node.ForagerName != "" {
		l.rc.writeForagerVantage(l.node, r.FinalText)
	}
	return l.complete(a, r, outputs)
}

// shortOfToolCalls reports whether a node with min_tool_calls is still
// short of them after this attempt: the dispatch and its attempts together
// made fewer. A backend that reports none of its calls cannot be checked.
func (l *repairLoop) shortOfToolCalls(r *RunResult) bool {
	return l.node.MinToolCalls > 0 && !r.ToolCallsUnreported && l.made < l.node.MinToolCalls
}

// complete re-tries the completion with the attempt's outputs, and reports
// whether the loop is done. If accept passes this time, CompleteNode marks
// the node completed for us and the attempt passed; a rejection asks for
// another attempt, and any other error stops the loop.
func (l *repairLoop) complete(a *repairAttempt, r *RunResult, outputs map[string]any) (done bool) {
	err := workflow.CompleteNode(l.rc.store.Workflows(), l.rc.runID, l.node.Node, outputs)
	if err == nil {
		l.pass(a, r, outputs)
		return true
	}
	var next *workflow.AcceptRejection
	if errors.As(err, &next) {
		l.stillRejected(a, next)
		return false
	}
	l.rc.markRepair(a.id, a.outputsJSON, false, a.in, a.out, a.cost, a.now)
	l.rc.logf("repair %s attempt %d non-accept error: %v", l.node.Node, a.n, err)
	return true
}

// pass closes the attempt that passed. The node's answer is this
// attempt's, so its model is too: the artifact and the audit trail read
// resolved_model.
func (l *repairLoop) pass(a *repairAttempt, r *RunResult, outputs map[string]any) {
	l.rc.markRepair(a.id, a.outputsJSON, true, a.in, a.out, a.cost, a.now)
	l.rc.pinDispatchModel(l.node, a.ledgerModel)
	l.rc.logf("repair %s attempt %d SUCCEEDED", l.node.Node, a.n)
	l.spend.model = a.sentModel
	l.spend.atDecode = r.SchemaAtDecode
	l.passed, l.outputs, l.text = true, outputs, r.FinalText
}

// stillRejected closes an attempt the node still rejects, and makes rej
// the rejection the next attempt repairs.
func (l *repairLoop) stillRejected(a *repairAttempt, rej *workflow.AcceptRejection) {
	l.rc.markRepair(a.id, a.outputsJSON, false, a.in, a.out, a.cost, a.now)
	l.rej = rej
	l.rc.logf("repair %s attempt %d still rejected: %v", l.node.Node, a.n, rej)
	l.trigger = "accept"
}

// repairSpend is the tokens, cost and Copilot credits of a node's repair
// attempts, and the model of the attempt that passed ("" when none did),
// with whether its text came from a call that sent the node's schema.
type repairSpend struct {
	model                  string
	atDecode               bool
	in, out, cost, credits int64
}

// repairModelFor is the model an on_reject block sends its attempts on the
// provider kind: its `model:`, else its `tier:` resolved under mode as a
// node's tier is (tierModel), else "", which means the model the node's
// dispatch was sent. The serving provider's alias table applies after this.
func repairModelFor(block map[string]any, mode BudgetMode, kind BackendKind) string {
	if m := stringFromMap(block, "model", ""); m != "" {
		return m
	}
	if tier := stringFromMap(block, "tier", ""); tier != "" {
		return tierModel(kind, tier, mode)
	}
	return ""
}

// markRepair records a single repair attempt's terminal state, logging
// (rather than silently dropping) a persistence failure so the audit row
// isn't lost — matching the runner's no-silent-provenance-loss discipline.
func (rc *runtimeContext) markRepair(repairID int64, outputsJSON string, passed bool, in, out, cost int64, now string) {
	if err := rc.store.Workflows().MarkRepairCompleted(repairID, outputsJSON, passed, in, out, cost, now); err != nil {
		rc.logf("mark repair completed (id %d): %v", repairID, err)
	}
}

// intFromMap reads an int field with a default. Tolerant of float64
// (which is what JSON-numbers and YAML-numbers come in as via go-yaml).
func intFromMap(m map[string]any, key string, def int) int {
	v := m[key]
	if s, ok := v.(string); ok {
		return intFromString(s, def)
	}
	if n, ok := intFromNumber(v); ok {
		return n
	}
	return def
}

// intFromNumber is v as an int when it is a number: an int, int64 or
// float64.
func intFromNumber(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}

// intFromString is the integer s starts with (Sscanf's %d), def when it
// holds none, or 0.
func intFromString(s string, def int) int {
	var n int
	_, _ = fmt.Sscanf(s, "%d", &n)
	if n != 0 {
		return n
	}
	return def
}

func stringFromMap(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}
