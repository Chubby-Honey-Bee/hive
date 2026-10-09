package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// executeAgentNode handles agent and parallel_fan nodes: resolves Comb
// tokens, runs the LLM backend, evaluates accept: predicates, and
// persists rationale + metrics. Returns true if OnlyWave is reached.
func (rc *runtimeContext) executeAgentNode(node workflow.DispatchNode) (stop bool) {
	node = rc.resolveNodeTokens(node)

	// Dreamer-archetype dispatch: skip the LLM backend entirely and
	// route through internal/dreamer.Run. The runner records the
	// LoopResult JSON as the node's rationale, so the rationale reads
	// "the dreamer touched N findings, emitted M signals."
	if node.Archetype == foragers.ArchetypeDreamer {
		return rc.executeDreamerNode(node)
	}
	if rc.cfg.DryRun {
		return rc.dryRunAgentNode(node)
	}
	// A fan whose fan_source holds no items has nothing to dispatch. A fan
	// whose fan_source no node produced still runs once, below.
	if node.FanEmpty {
		return rc.completeEmptyFan(node)
	}
	return rc.dispatchAgentNode(node)
}

// resolveNodeTokens substitutes the tokens the node's prompt holds: Comb
// ({comb.<vantage>}, regions and forager:<name> keys), CDE and calibration
// tokens. Substitution is resilient — failures silently collapse to empty
// rather than blocking dispatch.
func (rc *runtimeContext) resolveNodeTokens(node workflow.DispatchNode) workflow.DispatchNode {
	node.ResolvedPrompt, node.Placeholders = resolveCombTokens(node, rc.store)
	node.ResolvedPrompt, node.Placeholders = resolveCDETokens(node.ResolvedPrompt, node.Placeholders, rc.store)
	node.ResolvedPrompt, node.Placeholders = resolveCalibrationTokens(node.ResolvedPrompt, node.Placeholders, rc.store, rc.parsedDefn)
	return node
}

// dryRunAgentNode completes an agent node in a dry run with synthetic
// outputs (completeDryRun), so the engine can still walk the rest of the
// graph (decisions etc).
func (rc *runtimeContext) dryRunAgentNode(node workflow.DispatchNode) (stop bool) {
	rc.logf("[dry-run] would dispatch %s — prompt (%d chars)", node.Node, len(node.ResolvedPrompt))
	rc.completeDryRun(node, `{"dry_run":true}`)
	return false
}

// dispatchAgentNode sends an agent node's call, charges it, and settles the
// node on its reply: a call that failed fails the node (or releases it, on
// a cancelled run); a reply is checked, repaired when the node has an
// on_reject: block, and completes the node once accepted.
func (rc *runtimeContext) dispatchAgentNode(node workflow.DispatchNode) (stop bool) {
	system := rc.agentSystemPrompt(node)
	registry := rc.nodeRegistry(node)

	// Resolve everything that depends on the node + run config: backend
	// (per-node provider override), concrete model (tier-resolved + pinned),
	// per-turn output cap, and sampling fields.
	p := rc.resolveDispatchParams(node)

	nodeCtx, cancel := context.WithTimeout(rc.ctx, agentNodeTimeout(node))
	defer cancel()

	runResult, runErr := rc.runAgentCall(nodeCtx, node, p, rc.agentRequest(node, p, system, registry))
	rc.recordInvocations(node, runResult)
	rc.chargeAgentCall(node, p, runResult)
	if runErr != nil {
		rc.failAgentNode(node, runErr)
		return false
	}
	return rc.acceptAgentReply(nodeCtx, node, p, runResult)
}

// agentSystemPrompt is the node's persona (loadAgentPersona), empty when it
// cannot be read. A missing persona file is not an error inside
// loadAgentPersona — it returns empty — so a node naming an agent that does
// not exist runs with no system prompt at all; that is logged, since
// whatever the node produces is not the persona's work.
func (rc *runtimeContext) agentSystemPrompt(node workflow.DispatchNode) string {
	system, err := loadAgentPersona(rc.cfg.ProjectDir, rc.cfg.AgentsDir, node.Agent)
	if err != nil {
		rc.logf("agent persona load: %v — proceeding with empty system", err)
		system = ""
	}
	if node.Agent != "" && system == "" {
		rc.logf("agent persona %s/%s.md not found — node %s runs with an EMPTY system prompt",
			cmp.Or(rc.cfg.AgentsDir, defaultAgentsDir), node.Agent, node.Node)
	}
	return system
}

// agentNodeTimeout is a node's timeout budget. Single-call agents finish
// well inside 30m. parallel_fan nodes process their items in batches of the
// fan concurrency, so wall-clock scales with item count — give them a
// generous per-batch budget so the whole wave doesn't die when the slowest
// item runs long. A node's `ttl:` replaces the per-call TTL, and the budget
// is at least that long, so a call its TTL allows is never cut by the
// budget.
func agentNodeTimeout(node workflow.DispatchNode) time.Duration {
	nodeTimeout := max(30*time.Minute, node.TTL)
	if len(node.FanItems) == 0 {
		return nodeTimeout
	}
	conc := fanConcurrency()
	batches := (len(node.FanItems) + conc - 1) / conc
	// 15 minutes per child (or the node's TTL), accumulated across
	// sequential batches, floored at 30m so single-batch fans still
	// get the standard budget.
	return max(nodeTimeout, time.Duration(batches)*fanItemTTL(node))
}

// agentRequest is the node's call: its prompt with the persona and the
// node's tools, the resolved model and sampling, and the context window
// the call is held to.
func (rc *runtimeContext) agentRequest(node workflow.DispatchNode, p dispatchParams, system string, registry *ToolRegistry) RunRequest {
	req := RunRequest{
		System:       system,
		Prompt:       node.ResolvedPrompt,
		Model:        p.model,
		TTL:          callTTL(node),
		Registry:     registry,
		Temperature:  p.temp,
		TopP:         p.topP,
		Seed:         p.seed,
		MaxTokens:    p.maxOut,
		Reasoning:    node.Reasoning,
		Logf:         rc.logf,
		OutputSchema: node.OutputSchema,
		SchemaName:   node.Node,
	}
	req.ContextWindow, req.ContextWindowSource = rc.contextWindowFor(p.kind, p.model)
	return req
}

// runAgentCall sends the node's call. A true parallel_fan dispatches one
// backend.Run per item, concurrently, then merges them into a single
// RunResult (runFanOut), so the rest of the workflow engine sees one
// node + one combined output.
func (rc *runtimeContext) runAgentCall(ctx context.Context, node workflow.DispatchNode, p dispatchParams, req RunRequest) (*RunResult, error) {
	if len(node.FanItems) > 0 {
		return rc.runFanOut(ctx, node, p.backend, req)
	}
	runResult, runErr := p.backend.Run(ctx, req)
	rc.noteFinalize("node "+node.Node, runResult)
	rc.noteContext("node "+node.Node, runResult)
	return runResult, runErr
}

// chargeAgentCall charges every call that returned a result to the node
// and the run, on every path: a failed, rejected or repaired node spent it
// too, and --max-cost-usd has to see it. The cost meter is passed the
// *resolved* model — node.Model may be empty when the YAML used `tier:`
// instead.
func (rc *runtimeContext) chargeAgentCall(node workflow.DispatchNode, p dispatchParams, runResult *RunResult) {
	if runResult == nil {
		return
	}
	rc.recordSpend(node, cmp.Or(p.model, node.Model), p.provider, p.baseURL, runResult)
}

// failAgentNode settles a node whose call returned an error. The run's own
// cancellation is not the node's failure: the node goes back to pending
// (releaseNode). Rate-limit, quota and overload errors are told apart from
// genuine logic failures in the run log. Both mark the node failed
// (RateLimitedBackend has exhausted its retries by the time the error
// arrives here), but the operator reading the log can tell "claude is
// throttled, retry the run later" from "this fix is broken; investigate the
// rationale".
func (rc *runtimeContext) failAgentNode(node workflow.DispatchNode, runErr error) {
	if rc.ctx.Err() != nil {
		rc.releaseNode(node)
		return
	}
	label := "FAILED"
	if isRateLimitError(runErr) {
		label = "RATE_LIMITED"
	}
	rc.failNode(node, label, runErr.Error())
}

// acceptAgentReply checks an agent node's reply and settles the node on
// it: completed (with its rationale, its schema enforcement and an
// auto-commit) once accepted, repaired or rejected otherwise. Returns true
// if OnlyWave is reached.
func (rc *runtimeContext) acceptAgentReply(ctx context.Context, node workflow.DispatchNode, p dispatchParams, runResult *RunResult) (stop bool) {
	short := rc.toolCallsShortfall(node, runResult)
	if short == nil {
		rc.checkAgentReply(ctx, node, runResult)
	}
	_, finalText, repair, accepted := rc.completeOrRepair(node, runResult, short)
	if !accepted {
		return false
	}
	rc.countNodeRun()
	rc.recordCompletion(node, finalText)
	rc.recordAcceptedSchema(node, p, runResult, repair)
	rc.maybeCommit(node, runResult.FinalText)
	return rc.onlyWaveReached(node)
}

// toolCallsShortfall is the rejection of a reply whose call made fewer
// tool calls than the node's min_tool_calls, nil for any other. Such a
// call did not do the work its prompt asks for, whatever its answer says:
// a small model can return a well-formed report of actions it never took.
// The shortfall is a rejection, so on_reject can send the call back with
// the count and what to do; a node with no repair block fails instead, so
// max_retries can run the prompt again. Either way the answer is checked
// and merged no further. A backend that runs its own tool loop and reports
// none of its calls cannot be checked, and says so.
func (rc *runtimeContext) toolCallsShortfall(node workflow.DispatchNode, runResult *RunResult) *workflow.AcceptRejection {
	if node.MinToolCalls <= 0 {
		return nil
	}
	if runResult.ToolCallsUnreported {
		rc.logf("node %s: min_tool_calls %d not checked: this backend does not report its tool calls", node.Node, node.MinToolCalls)
		return nil
	}
	if calls := len(runResult.Invocations); calls < node.MinToolCalls {
		return toolCallsShort(node, calls, nodeOutputs(node, runResult.FinalText))
	}
	return nil
}

// checkAgentReply runs the checks on a reply that made its tool calls,
// before the node completes.
//
// A positive tests_pass claim is verified independently, by shelling out to
// `go test` against the agent's edited packages; a false claim rewrites
// runResult.FinalText so the downstream accept: predicates evaluate against
// reality.
//
// Swarm: write the forager's verdict to the Comb BEFORE completing the node
// in the DB. This closes the TOCTOU race: were the dispatcher's outer loop
// to tick between CompleteNode and BuildForagerVantage, a downstream forager
// with a `cites` bond would read empty comb_state, and its
// {comb.forager:<name>} substitution would silently become "". With the Comb
// row written first, any forager that sees `completed` status also sees the
// corresponding verdict persisted. Best-effort: Comb write failures are
// logged but never fail the node.
func (rc *runtimeContext) checkAgentReply(ctx context.Context, node workflow.DispatchNode, runResult *RunResult) {
	rc.verifyTestsClaim(ctx, node, runResult)
	if node.ForagerName != "" {
		rc.writeForagerVantage(node, runResult.FinalText)
	}
}

// recordAcceptedSchema records how the node's schema held its accepted
// answer: the dispatch's, or a repair's when one passed.
func (rc *runtimeContext) recordAcceptedSchema(node workflow.DispatchNode, p dispatchParams, runResult *RunResult, repair repairSpend) {
	acceptedModel, atDecode := p.model, runResult.SchemaAtDecode
	if repair.model != "" {
		acceptedModel, atDecode = repair.model, repair.atDecode
	}
	rc.recordSchemaEnforcement(node, p, acceptedModel, atDecode)
}

// callTTL bounds one call of a node, its dispatch or a repair attempt: its
// `ttl:`, else 5 minutes. HIVE_HTTP_TIMEOUT still replaces it
// (callDeadline).
func callTTL(node workflow.DispatchNode) time.Duration {
	if node.TTL > 0 {
		return node.TTL
	}
	return 5 * time.Minute
}

// fanItemTTL bounds one parallel_fan item's call: the node's `ttl:`, else 15
// minutes.
func fanItemTTL(node workflow.DispatchNode) time.Duration {
	if node.TTL > 0 {
		return node.TTL
	}
	return 15 * time.Minute
}

// recordCompletion persists an accepted node's rationale (finalText: the
// accepted attempt's text, a repair's when one passed), whose spend
// recordSpend has already counted. Best-effort: a failed write is logged,
// never fatal.
func (rc *runtimeContext) recordCompletion(node workflow.DispatchNode, finalText string) {
	// Cap rationale at 64,000 bytes: large enough to hold a full opus
	// synthesis (~25 KB) and most fan-out joins intact, while still bounding
	// pathological output.
	//
	// state_json is not a fallback for the untruncated text. It holds each
	// node's *parsed outputs* merged into one flat namespace, so it
	// preserves the final text only when the agent emitted prose
	// (workflow.ExtractJSONOutput's final_text fallback) and no later node
	// reused the key — and parallel siblings sharing an output key keep only
	// the last one to commit.
	if err := rc.store.Workflows().UpdateNodeRationale(
		rc.runID, node.Node, truncate(finalText, 64000),
	); err != nil {
		rc.logf("update rationale (run %d node %s): %v", rc.runID, node.Node, err)
	}
}

// recordSchemaEnforcement records, for a node with an output_schema, how the
// schema held its accepted answer (schemaEnforcement), and logs why. model is
// the accepted attempt's, as sent; atDecode is whether its text came from a
// call that sent the schema. Best-effort: a write failure is logged.
func (rc *runtimeContext) recordSchemaEnforcement(node workflow.DispatchNode, p dispatchParams, model string, atDecode bool) {
	if node.OutputSchema == nil {
		return
	}
	value, why := schemaEnforcement(rc.probes, p.kind, p.baseURL, model, node.Reasoning, atDecode)
	rc.logf("node %s: output schema %s (%s)", node.Node, value, why)
	if err := rc.store.Workflows().UpdateNodeSchemaEnforcement(rc.runID, node.Node, value); err != nil {
		rc.logf("record schema_enforcement (run %d node %s): %v", rc.runID, node.Node, err)
	}
}

// completeOrRepair calls workflow.CompleteNode and handles an accept:
// rejection, which goes to the on_reject: repair. short, when set, is the dispatch's min_tool_calls
// shortfall: its answer is not completed; the shortfall goes to the node's
// on_reject: repair, or fails the node when it has none. Returns the final
// outputs map, the text of the accepted attempt (the repair's when one
// passed), the repair attempts' spend, and whether the node was accepted
// (true) or terminally rejected or failed (false).
func (rc *runtimeContext) completeOrRepair(node workflow.DispatchNode, runResult *RunResult, short *workflow.AcceptRejection) (outputs map[string]any, finalText string, repair repairSpend, accepted bool) {
	if short != nil {
		return rc.repairShortfall(node, runResult, short)
	}
	outputs = nodeOutputs(node, runResult.FinalText)
	if err := rc.completeAgentNode(node, outputs); err != nil {
		return rc.completionRefused(node, runResult, err)
	}
	return outputs, runResult.FinalText, repair, true
}

// repairShortfall hands a min_tool_calls shortfall to the node's on_reject:
// repair, or fails the node when it has none (failShort).
func (rc *runtimeContext) repairShortfall(node workflow.DispatchNode, runResult *RunResult, short *workflow.AcceptRejection) (map[string]any, string, repairSpend, bool) {
	if workflow.NodeOnRejectBlock(rc.parsedDefn, node.Node) == nil {
		rc.failShort(node, runResult, short)
		return nil, "", repairSpend{}, false
	}
	return rc.repairOrReject(node, runResult, short)
}

// completeAgentNode completes an agent node with its outputs. A fan
// completes through CompleteFanItems: runFanOut checked each item's reply
// against the schema, and the joined text is not one reply.
func (rc *runtimeContext) completeAgentNode(node workflow.DispatchNode, outputs map[string]any) error {
	if len(node.FanItems) > 0 {
		return workflow.CompleteFanItems(rc.store.Workflows(), rc.runID, node.Node, outputs)
	}
	return workflow.CompleteNode(rc.store.Workflows(), rc.runID, node.Node, outputs)
}

// completionRefused handles a completion the engine refused. A failing
// accept: predicate surfaces as *workflow.AcceptRejection and goes to the
// repair loop; any other error is logged, and the node is not accepted.
func (rc *runtimeContext) completionRefused(node workflow.DispatchNode, runResult *RunResult, err error) (map[string]any, string, repairSpend, bool) {
	var rej *workflow.AcceptRejection
	if errors.As(err, &rej) {
		return rc.repairOrReject(node, runResult, rej)
	}
	rc.logf("complete node: %v", err)
	return nil, "", repairSpend{}, false
}

// repairOrReject hands a rejection to the node's on_reject: repair. When an
// attempt passes it returns the repaired outputs, that attempt's text, the
// attempts' spend and true; otherwise it marks the node rejected, naming the
// predicate, and returns the spend and false. A repair the run's
// cancellation cut short rejects nothing: the node goes back to pending.
func (rc *runtimeContext) repairOrReject(node workflow.DispatchNode, runResult *RunResult, rej *workflow.AcceptRejection) (map[string]any, string, repairSpend, bool) {
	repaired, repairedText, spend, ok := rc.tryRepair(node, runResult, rej)
	if ok {
		return repaired, repairedText, spend, true
	}
	if rc.ctx.Err() != nil {
		rc.releaseNode(node)
		return nil, "", spend, false
	}
	rc.markAcceptRejected(node, rej)
	return nil, "", spend, false
}

// failShort fails a node whose call fell short of min_tool_calls and that
// has no on_reject: block, through its max_retries, and keeps the call's
// reply as the node's rationale: no repair row will hold it, and an
// operator reading the failure needs what the model said.
func (rc *runtimeContext) failShort(node workflow.DispatchNode, runResult *RunResult, short *workflow.AcceptRejection) {
	msg := fmt.Sprintf("the call made %s tool call(s); min_tool_calls is %d, so the actions it reports were not taken", short.EvaluatedValue, node.MinToolCalls)
	if err := rc.store.Workflows().UpdateNodeRationale(rc.runID, node.Node, truncate(runResult.FinalText, 64000)); err != nil {
		rc.logf("update rationale (run %d node %s): %v", rc.runID, node.Node, err)
	}
	rc.failNode(node, "FAILED", msg)
}

// toolCallsShort is the rejection for a node with min_tool_calls whose
// calls so far made fewer: made counts the dispatch's and each repair
// attempt's tool calls together. Its reason tells the model the count and
// what to do, and a repair prompt's {accept_failure} is that reason alone;
// outputs is the answer the prompt shows as {outputs}.
func toolCallsShort(node workflow.DispatchNode, made int, outputs map[string]any) *workflow.AcceptRejection {
	calls := "tool calls"
	if made == 1 {
		calls = "tool call"
	}
	return &workflow.AcceptRejection{
		NodeName:       node.Node,
		Predicate:      workflow.MinToolCallsPredicate,
		EvaluatedValue: strconv.Itoa(made),
		Outputs:        outputs,
		Reason: fmt.Sprintf("You made %d %s, and this task needs at least %d: the actions you report were not taken. Do them now with your tools, then answer.",
			made, calls, node.MinToolCalls),
	}
}
