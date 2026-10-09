package runner

import (
	"cmp"
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// fanOutMinSuccessNumer/Denom set the minimum success ratio for a
// parallel_fan node to count as overall successful, rounded up. 1/2
// means: a wave of 12 items is OK with 6 successes, a wave of 8 with 4,
// a wave of 3 with 2. This trades
// strictness for resilience to transient per-child failures (CLI
// timeouts, rate limits). Single-item fans always require their one
// item to succeed (the >= 1 floor below).
const (
	fanOutMinSuccessNumer = 1
	fanOutMinSuccessDenom = 2
)

// runFanOut dispatches one backend.Run call per FanItem in parallel,
// substituting the item value into ResolvedPrompt at the configured
// FanItemPlaceholder, and merges the N RunResults into one composite
// result. Token + tool-invocation counts are summed across children so the
// workflow engine sees a single accurate aggregate, and its Turns is the
// calls that answered them (callsMade). Each child's result is kept
// (ItemResults), so its cost is priced on its own. A fan none of whose
// calls was answered returns no result, only its error.
//
// Failure semantics: the node fails only when fewer than half its items
// (and at least one) return text without an error — see the aggregation
// in mergeFanResults. The accept-gate / on_reject path then handles it
// like any failed dispatch.
//
// Concurrency: bounded by fanConcurrency (HIVE_MAX_PARALLEL_FAN), which
// also avoids creating N goroutines for huge fan-outs. Each child gets its
// own context derived from the parent nodeCtx (so a parent timeout
// cancels every in-flight child). Children share the registry — the
// underlying backends document their own thread-safety; the SDK
// backends are safe; the CLI backend forks subprocesses so it's
// trivially safe.
//
// base is the node's request; each child sends it with its item
// substituted into the prompt and a 15-minute TTL, or the node's `ttl:`.
func (rc *runtimeContext) runFanOut(
	ctx context.Context,
	node workflow.DispatchNode,
	backend LLMBackend,
	base RunRequest,
) (*RunResult, error) {
	placeholder := cmp.Or(node.FanItemPlaceholder, "{item}")
	conc := fanConcurrency()
	rc.logf("parallel_fan %s: dispatching %d items (placeholder=%s, max parallel=%d)",
		node.Node, len(node.FanItems), placeholder, conc)
	fan := &fanDispatch{rc: rc, node: node, backend: backend, base: base, placeholder: placeholder}
	return rc.mergeFanResults(node, fan.run(ctx, conc))
}

// fanItemResult is one fan item's call: the item's index, and the call's
// result and error.
type fanItemResult struct {
	index  int
	result *RunResult
	err    error
}

// fanDispatch is one parallel_fan node's item calls: each sends the node's
// request (base) with its item in place of the placeholder.
type fanDispatch struct {
	rc          *runtimeContext
	node        workflow.DispatchNode
	backend     LLMBackend
	base        RunRequest
	placeholder string
}

// run sends every item's call, at most conc at once, and returns their
// results in item order.
func (f *fanDispatch) run(ctx context.Context, conc int) []fanItemResult {
	results := make([]fanItemResult, len(f.node.FanItems))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, item := range f.node.FanItems {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = f.item(ctx, i, item)
		}()
	}
	wg.Wait()
	return results
}

// item sends item i's call. A panic fails its item alone, as a call that
// returned an error does; the fan's other items run on.
func (f *fanDispatch) item(ctx context.Context, i int, item string) (res fanItemResult) {
	defer func() {
		if rec := recover(); rec != nil {
			f.rc.logf("parallel_fan %s item %d: panic: %v\n%s", f.node.Node, i+1, rec, debug.Stack())
			res = fanItemResult{index: i, err: fmt.Errorf("panic: %v", rec)}
		}
	}()

	req := f.base
	req.Prompt = fanItemPrompt(f.node, f.placeholder, item)
	f.rc.logf("parallel_fan %s: dispatching item %d/%d", f.node.Node, i+1, len(f.node.FanItems))
	// TTL bounds each call: on the OpenAI-compatible and Gemini
	// backends it caps each HTTP call (HIVE_HTTP_TIMEOUT
	// replaces it); on the Anthropic SDK backend it is the idle
	// bound, the longest wait for a turn's next stream event. The
	// CLI backends ignore it. The node's timeout (ctx) caps the
	// whole fan. A node's `ttl:` replaces the 15 minutes.
	req.TTL = fanItemTTL(f.node)
	r, err := f.backend.Run(ctx, req)
	what := fmt.Sprintf("parallel_fan %s item %d", f.node.Node, i+1)
	f.rc.noteFinalize(what, r)
	f.rc.noteContext(what, r)
	return fanItemResult{index: i, result: r, err: err}
}

// mergeFanResults aggregates a fan's item results (fanResultMerge).
// Partial-success is honored: the node fails only when fewer than
// fanOutMinSuccessNumer/Denom of its items produced a result. This trades
// the original conservative "any failure invalidates the whole wave"
// semantic for resilience against transient per-child failures (CLI
// timeouts, rate limits, sporadic 5xx) — research waves of 8+ items
// routinely had one slow child kill an otherwise-complete wave under the
// strict policy.
func (rc *runtimeContext) mergeFanResults(node workflow.DispatchNode, results []fanItemResult) (*RunResult, error) {
	merge := &fanResultMerge{node: node, combined: &RunResult{SchemaAtDecode: true}}
	for _, cr := range results {
		merge.add(cr)
	}
	merge.finish()
	total := len(results)
	if need := fanMinSuccesses(total); merge.successes < need {
		return merge.failed(total, need)
	}
	if len(merge.failures) > 0 {
		// Soft-success path: at least half the items returned. Log the
		// failed ones so the operator can see what didn't make it.
		rc.logf("parallel_fan %s: partial success — %d/%d items (failed: %s)",
			node.Node, merge.successes, total, strings.Join(merge.failures, "; "))
	}
	return merge.combined, nil
}

// fanMinSuccesses is how many of a fan's total items must answer for the
// fan to succeed: fanOutMinSuccessNumer/Denom of them, rounded up, and at
// least one.
func fanMinSuccesses(total int) int {
	return max((total*fanOutMinSuccessNumer+fanOutMinSuccessDenom-1)/fanOutMinSuccessDenom, 1)
}

// fanResultMerge folds a fan's item results into one RunResult. Every item
// gets its `## Item N` section, in order: its text, or a line saying why
// it has none, so a reader of the joined text can tell an item that failed
// from one that was never sent. Each item's reply is checked against the
// node's schema, as the engine checks an agent node's: the joined text is
// not one reply.
type fanResultMerge struct {
	node      workflow.DispatchNode
	combined  *RunResult
	sections  []string
	failures  []string
	successes int
}

// add folds in one item's result: its usage, and its section, an answer or
// a failure.
func (m *fanResultMerge) add(cr fanItemResult) {
	if cr.result != nil {
		m.addUsage(cr.result)
	}
	text, failure := fanItemOutcome(m.node, cr)
	if failure != "" {
		m.failures = append(m.failures, failure)
	} else {
		m.successes++
		m.combined.SchemaAtDecode = m.combined.SchemaAtDecode && cr.result.SchemaAtDecode
	}
	m.sections = append(m.sections, fmt.Sprintf("## Item %d\n\n%s", cr.index+1, text))
}

// addUsage sums an item's usage into the fan's: its tokens, the calls that
// answered it (callsMade), its cut-off calls, tool uses and invocations.
// Each item is kept (ItemResults), to be priced as its own call
// (recordSpend).
func (m *fanResultMerge) addUsage(r *RunResult) {
	c := m.combined
	c.InputTokens += r.InputTokens
	c.OutputTokens += r.OutputTokens
	c.Turns += callsMade(r)
	c.CutOffCalls += r.CutOffCalls
	c.ItemResults = append(c.ItemResults, r)
	c.ToolUses += r.ToolUses
	c.Invocations = append(c.Invocations, r.Invocations...)
}

// finish joins the sections into the fan's text. The fan's text came from
// calls that sent the schema only when every answer did and there is one.
func (m *fanResultMerge) finish() {
	m.combined.SchemaAtDecode = m.combined.SchemaAtDecode && m.successes > 0
	m.combined.FinalText = strings.Join(m.sections, "\n\n---\n\n")
	m.combined.StopReason = "end_turn"
}

// failed is the error of a fan too few of whose total items answered, need
// being how many must, with the merged result, or with none when no item's
// call was answered: the fan made no call, and like a run whose one call
// failed, it has nothing to charge.
func (m *fanResultMerge) failed(total, need int) (*RunResult, error) {
	err := fmt.Errorf(
		"parallel_fan failed: only %d/%d items produced output (need %d); failures: %s",
		m.successes, total, need, strings.Join(m.failures, "; "))
	if m.combined.Turns == 0 {
		return nil, err
	}
	return m.combined, err
}

// fanItemOutcome is an item's section text, and why it failed, "" when its
// reply is an answer. A child that returned text with an error (a reply
// cut off at the output cap), or text that breaks the schema, failed; its
// text is not an answer, and its section says why it has none.
func fanItemOutcome(node workflow.DispatchNode, cr fanItemResult) (text, failure string) {
	if cr.err != nil {
		return fmt.Sprintf("(no finding: the call failed: %v)", cr.err), fmt.Sprintf("item %d: %v", cr.index+1, cr.err)
	}
	text = runResultText(cr.result)
	if text == "" {
		return "(no finding: the reply was empty)", fmt.Sprintf("item %d: no text", cr.index+1)
	}
	if v := fanItemViolations(node, text); len(v) > 0 {
		return "(no finding: the reply broke the node's output schema)", fmt.Sprintf("item %d: output_schema: %s", cr.index+1, workflow.SummarizeViolations(v))
	}
	return text, ""
}

// runResultText is r's final text, "" for no result.
func runResultText(r *RunResult) string {
	if r == nil {
		return ""
	}
	return r.FinalText
}

// fanItemViolations is how an item's reply breaks the node's output_schema;
// none for a node without one.
func fanItemViolations(node workflow.DispatchNode, text string) []string {
	if node.OutputSchema == nil {
		return nil
	}
	return schemaViolations(node.OutputSchema, text)
}

// fanItemPrompt is the prompt one fan item sends: node's prompt with item in
// place of the placeholder where the node's template holds it
// (node.Placeholders), so a placeholder inside a value, such as a context
// pack naming {gap}, stays as written. A placeholder that is not a {…}
// token is replaced wherever it appears.
func fanItemPrompt(node workflow.DispatchNode, placeholder, item string) string {
	key, ok := strings.CutPrefix(placeholder, "{")
	key, ok2 := strings.CutSuffix(key, "}")
	if !ok || !ok2 || strings.ContainsAny(key, "{}") {
		return strings.ReplaceAll(node.ResolvedPrompt, placeholder, item)
	}
	out, _ := workflow.FillLeft(node.ResolvedPrompt, node.Placeholders, func(k string) (string, bool) { return item, k == key })
	return out
}

// completeEmptyFan finishes a parallel_fan whose fan_source holds no items.
// There is nothing to dispatch, so it makes no backend call, records empty
// outputs, applies state_updates and leaves accept: unevaluated.
func (rc *runtimeContext) completeEmptyFan(node workflow.DispatchNode) (stop bool) {
	rc.logf("parallel_fan %s: fan_source holds no items — completing without a backend call", node.Node)
	if err := workflow.CompleteEmptyFan(rc.store.Workflows(), rc.runID, node.Node); err != nil {
		rc.logf("complete empty fan %s: %v", node.Node, err)
		return false
	}
	return rc.onlyWaveReached(node)
}

// nodeOutputs parses a node's final text into its outputs. A fan's joined
// text holds every item's answer, and parsing keeps only the last JSON
// object in it, so the whole joined text also goes under the fan's first
// declared output.
func nodeOutputs(node workflow.DispatchNode, finalText string) map[string]any {
	outputs := workflow.ExtractJSONOutput(finalText)
	if len(node.FanItems) > 0 && len(node.Outputs) > 0 {
		outputs[node.Outputs[0]] = finalText
	}
	return outputs
}
