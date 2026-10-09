package runner

// The dispatch loop's per-wave fan-out and per-node executor. The state a
// node's execution shares (cfg, store, ctx, the committer, the result and
// its mutex, the backend, the run id, the log) is threaded through
// *runtimeContext rather than captured by a closure, so each phase can be
// exercised by itself, without a full Run.
//
// An agent node runs in dispatch_agent.go, with its parameters
// (dispatch_params.go), its prompt's tokens (dispatch_tokens.go), its
// context window (dispatch_window.go) and its spend (dispatch_spend.go); a
// parallel_fan's items in dispatch_fanout.go and the dreamer in
// dispatch_dreamer.go.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// runtimeContext threads orchestration state through the per-node
// executor. It is created once at the top of Run and lives for the full
// duration of the run. It holds the run's context by design: every node
// of the run, and every call a node makes, runs under it.
type runtimeContext struct {
	ctx     context.Context
	cfg     Config
	store   *db.Store
	backend LLMBackend
	gc      *GitCommitter
	runID   int64
	logf    func(string, ...any)

	// parsedDefn is the workflow definition parsed once at startup and
	// reused by tryRepair to avoid a redundant DB fetch + YAML unmarshal
	// on every repair iteration.
	parsedDefn map[string]any

	// Outputs accumulated by the executor — guarded by resMu.
	res   *Result
	resMu sync.Mutex
	// unmetered holds the reasons already logged for unmetered calls (a
	// model with no price, or a provider that reports no token counts for
	// one), so each is logged once. Guarded by resMu.
	unmetered map[string]bool
	// usedModels holds the lines already logged naming the models a node's
	// calls used, where they differ from the node's model. Guarded by resMu.
	usedModels map[string]bool

	// probes holds the constraint probes run before the first dispatch, by
	// (endpoint, model, reasoning). Read-only once dispatch starts.
	probes map[probeKey]ConstraintProbe

	// windows holds the context window of each model an OpenAI-compatible
	// endpoint serves, by provider kind (openai or local) and model, once
	// resolved (contextWindowFor); discovery holds, per kind, the backend
	// that asks its endpoint, and servers the kind of server found there,
	// asked once a run. windowMu guards them.
	windowMu  sync.Mutex
	windows   map[windowKey]contextWindow
	discovery map[BackendKind]*OpenAIBackend
	servers   map[BackendKind]localServer
}

// makeLogf builds the prefixed logger used throughout the run. Extracted
// so tests can supply their own io.Writer without copying the prefix.
func makeLogf(w io.Writer) func(string, ...any) {
	// Every dispatch goroutine in a wave logs through this, so the Fprintf
	// needs a lock: an io.Writer carries no concurrency guarantee, and
	// interleaved writes to one were producing spliced lines.
	//
	// The guarantee is exactly this: logf's own writes are serialised. It
	// does not order logf against anything else writing to the same stderr
	// — the Ctrl-C handler in internal/cli/agent_run.go, or a CLI backend
	// child that inherited the fd.
	var mu sync.Mutex
	return func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "[agent-run] "+f+"\n", a...)
	}
}

// dispatchWave runs one batch of dispatchable nodes in parallel up to
// maxPar concurrent executions. Returns true when an OnlyWave stop has
// been requested (so the caller breaks out of the outer loop).
func (rc *runtimeContext) dispatchWave(dispatch []workflow.DispatchNode) (stop bool) {
	maxPar := waveParallelism(len(dispatch))
	rc.logf("dispatching %d nodes (max parallel: %d)", len(dispatch), maxPar)

	var (
		wg      sync.WaitGroup
		sem     = make(chan struct{}, maxPar)
		stopHit atomic.Bool
	)
	for _, node := range dispatch {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if rc.executeNodeRecovering(node) {
				stopHit.Store(true)
			}
		}()
	}
	wg.Wait()
	return stopHit.Load()
}

// waveParallelism is how many of a wave's n nodes run at once:
// HIVE_MAX_PARALLEL_NODES, else 4, and one for a wave of one.
func waveParallelism(n int) int {
	if n == 1 {
		return 1
	}
	return parallelismFromEnv(4)
}

// executeNodeRecovering is executeNode on a wave's goroutine, where a panic
// fails its node alone, through the retry rule; the wave's other nodes, and
// the process, run on.
func (rc *runtimeContext) executeNodeRecovering(node workflow.DispatchNode) (stop bool) {
	defer rc.recoverNodePanic(node)
	return rc.executeNode(node)
}

// recoverNodePanic, deferred, fails a node whose execution panicked, with
// the panic's text, and logs the stack.
func (rc *runtimeContext) recoverNodePanic(node workflow.DispatchNode) {
	if rec := recover(); rec != nil {
		msg := fmt.Sprintf("panic: %v", rec)
		rc.logf("node %s FAILED: %s\n%s", node.Node, msg, debug.Stack())
		rc.writeNodeFailure(node, msg)
	}
}

// releaseNode returns a node the run's cancellation stopped to pending, with
// the attempt its dispatch counted taken back (workflow.ReleaseNode): the
// node did not fail, the run stopped, so it spends no retry and --resume
// dispatches it again.
func (rc *runtimeContext) releaseNode(node workflow.DispatchNode) {
	rc.logf("node %s stopped: the run was cancelled; --resume %d runs it again", node.Node, rc.runID)
	if err := workflow.ReleaseNode(rc.store.Workflows(), rc.runID, node.Node); err != nil {
		rc.logf("release node: %v", err)
	}
}

// nodeExecutors runs each node type that does work, and reports whether
// the run halts after the node (OnlyWave reached, or a pause). A node of
// any other type, a decision, completes at once (completeDecisionNode).
var nodeExecutors = map[string]func(*runtimeContext, workflow.DispatchNode) bool{
	"command":      (*runtimeContext).executeCommandNode,
	"calibrate":    (*runtimeContext).executeCalibrateNode,
	"agent":        (*runtimeContext).executeAgentNode,
	"parallel_fan": (*runtimeContext).executeAgentNode,
	"human_review": (*runtimeContext).executeHumanReviewNode,
}

// executeNode routes a single workflow node to the appropriate handler based
// on node.Type and returns true if the run should halt after this node
// (OnlyWave reached). All shared-state mutations are guarded by rc.resMu.
func (rc *runtimeContext) executeNode(node workflow.DispatchNode) (stop bool) {
	rc.logNodeHeader(node)
	if execute, ok := nodeExecutors[node.Type]; ok {
		return execute(rc, node)
	}
	return rc.completeDecisionNode(node)
}

// logNodeHeader logs the line that opens a node's part of the run log.
func (rc *runtimeContext) logNodeHeader(node workflow.DispatchNode) {
	switch node.Type {
	case "command":
		rc.logf("--- node %s (type=command argv=%q) ---", node.Node, node.Argv)
	case "calibrate":
		rc.logf("--- node %s (type=calibrate rebuild=%v scope=%s) ---", node.Node, node.Rebuild, calibrateScopeLabel(node))
	default:
		rc.logf("--- node %s (type=%s agent=%s model=%s) ---", node.Node, node.Type, node.Agent, node.Model)
	}
}

// executeHumanReviewNode parks the node and pauses the run. Successors stay
// pending because waiting_human is not a finished state; the dispatcher
// sees no dispatchable work and exits without declaring the run complete.
func (rc *runtimeContext) executeHumanReviewNode(node workflow.DispatchNode) (stop bool) {
	if err := workflow.PauseForHuman(rc.store.Workflows(), rc.runID, node.Node); err != nil {
		rc.logf("human_review node %s: %v", node.Node, err)
		return false
	}
	rc.logf("human review required at %s — run paused. Resume with:", node.Node)
	rc.logf("  chb workflow resume %d approve --feedback \"…\"   (or reject / redirect)", rc.runID)
	return true
}

// completeDecisionNode completes a decision node, which has no prompt, at
// once, so the engine can continue walking the graph.
func (rc *runtimeContext) completeDecisionNode(node workflow.DispatchNode) (stop bool) {
	rc.logf("non-agent node → auto-complete")
	_ = workflow.CompleteNode(rc.store.Workflows(), rc.runID, node.Node, map[string]any{})
	return false
}

// failNode logs the node's failure under label (FAILED, RATE_LIMITED) and
// fails it with msg (writeNodeFailure).
func (rc *runtimeContext) failNode(node workflow.DispatchNode, label, msg string) {
	rc.logf("node %s %s: %s", node.Node, label, msg)
	rc.writeNodeFailure(node, msg)
}

// writeNodeFailure fails the node with msg through the retry rule
// (workflow.FailNode). A write failure is logged.
func (rc *runtimeContext) writeNodeFailure(node workflow.DispatchNode, msg string) {
	if err := workflow.FailNode(rc.store.Workflows(), rc.runID, node.Node, msg); err != nil {
		rc.logf("fail node: %v", err)
	}
}

// markAcceptRejected marks a node an accept: predicate rejected, naming the
// predicate (acceptRejectionRationale). A write failure is logged.
func (rc *runtimeContext) markAcceptRejected(node workflow.DispatchNode, rej *workflow.AcceptRejection) {
	rationale := acceptRejectionRationale(rej)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := rc.store.Workflows().MarkNodeRejected(rc.runID, node.Node, rationale, now); err != nil {
		rc.logf("mark rejected (run %d node %s): %v", rc.runID, node.Node, err)
	}
	rc.logf("node %s REJECTED: %s", node.Node, rationale)
}

// acceptRejectionRationale is the rationale of a node an accept: predicate
// rejected: the predicate and its value, and the evaluation's error when
// it had one.
func acceptRejectionRationale(rej *workflow.AcceptRejection) string {
	rationale := fmt.Sprintf("accept rejected: %s = %s", rej.Predicate, rej.EvaluatedValue)
	if rej.EvalError != "" {
		rationale += " (" + rej.EvalError + ")"
	}
	return rationale
}

// refuseCompletion handles a completion the engine refused for a node
// with no repair: a rejected accept: predicate goes to reject, and is
// terminal; any other error fails the node.
func (rc *runtimeContext) refuseCompletion(node workflow.DispatchNode, err error, reject func(*workflow.AcceptRejection)) {
	var rej *workflow.AcceptRejection
	if errors.As(err, &rej) {
		reject(rej)
		return
	}
	rc.logf("complete node: %v", err)
	rc.writeNodeFailure(node, err.Error())
}

// completeDryRun marks a node completed with synthetic outputs, so a dry
// run walks the rest of the graph. It bypasses CompleteNode: a dry run has
// no real outputs, so accept: predicates would always reject.
func (rc *runtimeContext) completeDryRun(node workflow.DispatchNode, outputsJSON string) {
	now := time.Now().UTC().Format(time.RFC3339)
	_ = rc.store.Workflows().MarkNodeCompleted(rc.runID, node.Node, outputsJSON, now)
}

// countNodeRun counts a completed node in the run's NodesRun.
func (rc *runtimeContext) countNodeRun() {
	rc.resMu.Lock()
	rc.res.NodesRun++
	rc.resMu.Unlock()
}

// onlyWaveReached reports whether a completed node is in the wave
// --only-wave stops after.
func (rc *runtimeContext) onlyWaveReached(node workflow.DispatchNode) bool {
	return rc.cfg.OnlyWave > 0 && detectWave(node.Node) == rc.cfg.OnlyWave
}

// nodeRegistry builds the tool registry one call of the node runs with:
// every tool, confined to the project directory, narrowed to the node's
// `tools:` allowlist when it declares one.
func (rc *runtimeContext) nodeRegistry(node workflow.DispatchNode) *ToolRegistry {
	registry := NewToolRegistry(rc.cfg.ProjectDir, rc.store, NewFSRecorder())
	registry.Env = modelEnv(rc.cfg.DBPath)
	if node.Tools != nil {
		registry.Restrict(*node.Tools)
	}
	return registry
}

// noteFinalize logs an answer a tool loop's last call gave rather than its
// own turns: a wrap-up call after the turns ran out (RunResult's WrappedUp),
// or a finalize call that gave no answer (FinalizeError). what names the
// call: the node, a fan item or a repair.
func (rc *runtimeContext) noteFinalize(what string, r *RunResult) {
	if r == nil {
		return
	}
	if r.WrappedUp {
		rc.logf("%s: the tool loop used all its turns with the model still calling tools, so a wrap-up call with no tool call left gave the answer, checked as any answer is", what)
	}
	if r.FinalizeError != "" {
		rc.logf("%s: the finalize call gave no answer (%s), so the tool loop's answer stands and is checked against the schema", what, r.FinalizeError)
	}
}

// noteContext logs the context-window guard's lines on a call (RunResult's
// ContextNotes), one per request, numbered when there are several: a tool
// loop's turns and its finalize call. what names the call: the node, a fan
// item or a repair.
func (rc *runtimeContext) noteContext(what string, r *RunResult) {
	if r == nil {
		return
	}
	for i, note := range r.ContextNotes {
		if len(r.ContextNotes) == 1 {
			rc.logf("%s: %s", what, note)
		} else {
			rc.logf("%s call %d: %s", what, i+1, note)
		}
	}
}

// maybeCommit auto-commits any file edits an agent made. No-op if git
// auto-commit is disabled. CommitIfDirty takes a package-level mutex so
// parallel nodes don't interleave staged files.
func (rc *runtimeContext) maybeCommit(node workflow.DispatchNode, finalText string) {
	if !rc.gc.Enabled {
		return
	}
	summary := firstLine(finalText)
	sha, err := rc.gc.CommitIfDirty(CommitMessageFor(detectWave(node.Node), node.Node, summary))
	if err != nil {
		rc.logf("commit: %v", err)
		return
	}
	if sha == "" {
		return
	}
	rc.resMu.Lock()
	rc.res.Commits = append(rc.res.Commits, sha)
	rc.resMu.Unlock()
	rc.logf("committed %s", sha[:12])
}

// ptrFloat64 returns a pointer to the given float64, for optional RunRequest
// fields (Temperature) where nil is distinguishable from zero.
func ptrFloat64(v float64) *float64 { return &v }

// recordToolInvocations persists one call's tool invocations, a dispatch's
// or a repair attempt's, to the tool_invocations audit trail under the node.
// SQLite's WriteDB pool (MaxOpenConns=1) serializes the inserts.
func (rc *runtimeContext) recordToolInvocations(node workflow.DispatchNode, invocations []ToolInvocation) {
	for _, inv := range invocations {
		isErr := 0
		if inv.IsError {
			isErr = 1
		}
		if _, err := rc.store.WriteDB.Exec(
			`INSERT INTO tool_invocations (run_id, node_name, tool, input, output, is_error, duration_s)
			 VALUES (?,?,?,?,?,?,?)`,
			rc.runID, node.Node, inv.Tool, inv.Input, truncate(inv.Output, 20_000), isErr, inv.DurationS,
		); err != nil {
			// Audit loss is bad but not fatal — surface so it doesn't disappear.
			rc.logf("audit insert (run %d node %s tool %s): %v", rc.runID, node.Node, inv.Tool, err)
		}
	}
}

// recordInvocations persists the tool_invocations audit trail for one
// node's dispatch and accumulates the token counters; the resMu guards
// Result.
func (rc *runtimeContext) recordInvocations(node workflow.DispatchNode, runResult *RunResult) {
	if runResult == nil {
		return
	}
	rc.recordToolInvocations(node, runResult.Invocations)
	rc.resMu.Lock()
	rc.res.InputTokens += runResult.InputTokens
	rc.res.OutputTokens += runResult.OutputTokens
	rc.resMu.Unlock()
}

// writeForagerVantage writes a forager node's reply to the Comb as its
// verdict, unless the reply breaks the node's output_schema: the engine will
// reject that reply, and a verdict in the Comb is read by `cites` foragers
// and can fire the ∇ quorum sensor, which fires each pair once. A repair
// that passes writes its own. A write failure is logged, never fatal.
func (rc *runtimeContext) writeForagerVantage(node workflow.DispatchNode, text string) {
	if node.OutputSchema != nil {
		if v := schemaViolations(node.OutputSchema, text); len(v) > 0 {
			rc.logf("comb forager write (%s) skipped: the reply breaks output_schema (%s)", node.ForagerName, workflow.SummarizeViolations(v))
			return
		}
	}
	if err := comb.BuildForagerVantage(rc.ctx, rc.store, rc.runID, node.ForagerName, text, workflow.ExtractJSONOutput(text)); err != nil {
		rc.logf("comb forager write (%s): %v", node.ForagerName, err)
	}
}
