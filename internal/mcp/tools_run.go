package mcp

// The run tools. chb_preflight, chb_agent_run, chb_swarm and
// chb_set_budget_mode start or configure runs; chb_run_totals,
// chb_node_rationale and chb_run_state read them back. The reads are
// in-process queries on the server's store. The starts spawn the same `chb`
// binary the operator would run by hand, detached, so the call returns at
// once with the run_id and the host polls chb_run_totals to follow progress.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

func preflightSpec() map[string]any {
	return map[string]any{
		"name":        "chb_preflight",
		"title":       "Preflight a workflow",
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		"description": "Pre-launch checklist for an autonomous workflow: validates YAML, every agent/parallel_fan node has accept:, every output an accept: judges is named in its node's prompt, every parallel_fan's fan_source is an input or declared upstream and its prompt holds its placeholder, prompt has negative-evidence language, ≥1 provider authenticated, target dir hygiene, pricing freshness. Returns structured pass/fail.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workflow_yaml": map[string]any{"type": "string", "description": "Path to the workflow YAML"},
				"provider":      map[string]any{"type": "string", "enum": []string{"anthropic", "gemini", "openai", "claude-cli", "gemini-cli"}, "description": "Optional: assert this provider is the resolved one"},
				"target_dir":    map[string]any{"type": "string", "description": "Optional: target dir hygiene check"},
			},
			"required": []string{"workflow_yaml"},
		},
	}
}

func agentRunSpec() map[string]any {
	return map[string]any{
		"name":        "chb_agent_run",
		"title":       "Run a workflow",
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true},
		"description": "Drive a workflow YAML to completion via agent-run. Spawns the run; returns immediately with {run_id, pid, log_path}. Poll chb_run_totals to follow progress. With `branch` set, the run refuses to start on a dirty working tree unless allow_dirty is true (auto-commit would sweep uncommitted work into the branch).",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workflow_yaml":  map[string]any{"type": "string", "description": "Path to the workflow YAML to run"},
				"inputs":         map[string]any{"type": "object", "description": "Workflow inputs (matches the YAML's inputs: list)"},
				"provider":       map[string]any{"type": "string", "enum": []string{"anthropic", "gemini", "openai", "claude-cli", "gemini-cli"}, "description": "LLM provider; omit to auto-detect"},
				"max_iterations": map[string]any{"type": "integer", "description": "Cap on workflow iterations (default 500, agent-run's own)"},
				"max_cost_usd":   map[string]any{"type": "number", "description": "Stop the run once cumulative cost reaches this many US dollars"},
				"branch":         map[string]any{"type": "string", "description": "Auto-commit branch; empty disables commits"},
				"allow_dirty":    map[string]any{"type": "boolean", "description": "Proceed with auto-commit even if the working tree has uncommitted changes (they will be swept into the branch)"},
				"dry_run":        map[string]any{"type": "boolean", "description": "Walk the workflow without calling any model; nodes complete with synthetic outputs"},
				"budget_mode":    map[string]any{"type": "string", "enum": []string{"premium", "standard", "cheap", "free"}, "description": "Cost dial for this run: premium|standard|cheap|free (default: the session's mode from chb_set_budget_mode, else HIVE_BUDGET_MODE, else standard)"},
			},
			"required": []string{"workflow_yaml"},
		},
	}
}

func runTotalsSpec() map[string]any {
	return map[string]any{
		"name":        "chb_run_totals",
		"title":       "Run totals",
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		"description": "Aggregated run state: nodes_total/completed/rejected/failed + tokens_in/out + metered_calls/unmetered_calls + cost_usd. cost_usd reads \"unmetered\" when no call's cost is known (its model has no price in the models config and it ran off this machine, or its backend, a gemini CLI without --output-format json, reports no token counts), since that cost is unknown, not zero; a call on this machine's server costs $0. Returns the same shape `chb run-totals <id>` emits on the CLI.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"run_id": map[string]any{"type": "integer", "description": "Workflow run to total"},
			},
			"required": []string{"run_id"},
		},
	}
}

func nodeRationaleSpec() map[string]any {
	return map[string]any{
		"name":        "chb_node_rationale",
		"title":       "Node rationale",
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		"description": "Read a specific node's persisted rationale (the agent's final text) from workflow_node_states. " +
			"This is the canonical MCP path to retrieve a synthesize / decompose / evaluate output after a run completes — " +
			"required when consuming chb_agent_run results without shelling out. " +
			"Returns {run_id, node_name, status, rationale, tokens_in, tokens_out, cost_usd}; " +
			"cost_usd reads \"unmetered\" when the node's calls' cost is unknown: its model has no price in the models config and it ran off this machine, or it ran on a gemini CLI without --output-format json, which reports no token counts.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"run_id":    map[string]any{"type": "integer", "description": "Workflow run the node belongs to"},
				"node_name": map[string]any{"type": "string", "description": "Node whose final text to return"},
			},
			"required": []string{"run_id", "node_name"},
		},
	}
}

func runStateSpec() map[string]any {
	return map[string]any{
		"name":        "chb_run_state",
		"title":       "Run state",
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		"description": "Read the workflow run's accumulated state map. Returns the JSON object that GetWorkflowRun.StateJSON holds — every output that any completed node merged into state, plus the run's original inputs. " +
			"Pass a `key` to return just that field's value (string), or omit it to get the full map. " +
			"This is the canonical MCP path to read the *full* output of a node whose final text exceeds the 64k-char rationale cap (e.g. a parallel_fan aggregate or a long synthesis). " +
			"Returns either {key, value, length} or {state, keys}.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"run_id": map[string]any{"type": "integer", "description": "Workflow run whose state_json to read"},
				"key":    map[string]any{"type": "string", "description": "Optional: return just this state field"},
			},
			"required": []string{"run_id"},
		},
	}
}

func setBudgetModeSpec() map[string]any {
	return map[string]any{
		"name":        "chb_set_budget_mode",
		"title":       "Set cost mode",
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		"description": "Set the cost dial for subsequent runs in this session. Four positions: 'premium' (each tier's Primary — top quality, costs most), 'standard' (MidTier — the default), 'cheap' (the Cheap slot — an aggressive downshift), 'free' (FreeFallback — zero-multiplier models on Copilot, lower quality on direct APIs). Applies to every run that chb_research, chb_agent_run, chb_swarm, chb_self_review and chb_self_implement start from now on, until changed. A budget_mode argument on chb_agent_run or chb_swarm overrides it for that one run.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mode": map[string]any{"type": "string", "enum": []string{"premium", "standard", "cheap", "free"}, "description": "Budget mode to apply"},
			},
			"required": []string{"mode"},
		},
	}
}

func swarmSpec() map[string]any {
	return map[string]any{
		"name":        "chb_swarm",
		"title":       "Ask the forager swarm",
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true},
		"description": "Ask the hive a question. Each forager analyzes from a different lens; the default `balanced` preset dispatches nine (architect, empiricist, historian, optimist, pragmatist, scholar, skeptic, steward, timekeeper). The queen writes their answers up as one verdict. This spends real LLM calls. Spawns the run and returns {run_id, pid, log_path}; stop a run by terminating pid.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question":          map[string]any{"type": "string", "description": "The question to ask the hive"},
				"foragers":          map[string]any{"type": "string", "description": "Comma-separated forager names, or a preset: 'balanced' (9, the default), 'minimal' (7 axis-owners), 'default' (10 default:true foragers), 'all' (13)."},
				"provider":          map[string]any{"type": "string", "enum": []string{"anthropic", "gemini", "openai", "claude-cli", "gemini-cli"}, "description": "LLM provider override; omit to auto-detect"},
				"budget_mode":       map[string]any{"type": "string", "enum": []string{"premium", "standard", "cheap", "free"}, "description": "Cost dial for this run: premium|standard|cheap|free (default: the session's mode from chb_set_budget_mode, else HIVE_BUDGET_MODE, else standard)"},
				"model":             map[string]any{"type": "string", "description": "Pin a literal model for each forager (default: empty → tier system)"},
				"synthesizer_model": map[string]any{"type": "string", "description": "Pin a literal model for Queen (default: empty → tier system)"},
			},
			"required": []string{"question"},
		},
	}
}

// handlePreflight shells out to `chb preflight` because the
// preflight checks live in internal/cli and would
// require a refactor to import here. Shelling out keeps the surface
// small and the output JSON-shaped.
func (s *mcpServer) handlePreflight(ctx context.Context, req rpcRequest, args map[string]any) {
	yamlPath := stringArg(args, "workflow_yaml")
	if yamlPath == "" {
		s.writeToolResult(req.ID, "", "workflow_yaml required", true)
		return
	}
	cliArgs := []string{"preflight", yamlPath}
	cliArgs = withStringFlag(cliArgs, "--provider", stringArg(args, "provider"))
	cliArgs = withStringFlag(cliArgs, "--target-dir", stringArg(args, "target_dir"))

	bin := s.chbBin()
	// CommandContext: a cancelled request kills the preflight subprocess
	// rather than leaving it to finish for a client that stopped waiting.
	out, err := exec.CommandContext(ctx, bin, cliArgs...).CombinedOutput() //nolint:gosec
	s.writeJSONToolResult(req.ID, preflightResult(bin, string(out), err))
}

// preflightResult is chb_preflight's result for the preflight's output and
// exit. `pass` requires the output to be a preflight report as well as a
// clean exit, so a binary named chb that resolves on PATH and exits cleanly
// does not report a passing preflight.
func preflightResult(bin, text string, err error) map[string]any {
	verdict, sawReport := preflightVerdict(text)
	result := map[string]any{
		"pass":   err == nil && sawReport && verdict,
		"stdout": text,
	}
	if exitErr := preflightExitError(bin, sawReport, err); exitErr != "" {
		result["exit_error"] = exitErr
	}
	return result
}

// preflightExitError says why a preflight's exit is not to be believed: it
// printed no report at all, or it exited with an error; "" when neither.
func preflightExitError(bin string, sawReport bool, err error) string {
	switch {
	case !sawReport:
		return fmt.Sprintf("%s produced no preflight report (is the resolved binary chb?)", bin)
	case err != nil:
		return err.Error()
	}
	return ""
}

// preflightVerdict reads the report's terminal line. Returns whether it
// says PASS, and whether a report was found at all.
func preflightVerdict(out string) (pass, found bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "preflight:") {
			continue
		}
		found = true
		pass = strings.Contains(line, "PASS")
	}
	return pass, found
}

// handleAgentRun spawns `chb agent-run` in a detached subprocess
// and returns immediately with the run id. We can't use
// runner.Run directly because it runs to completion; the MCP host
// expects the call to return promptly and poll for status.
func (s *mcpServer) handleAgentRun(req rpcRequest, args map[string]any) {
	yamlPath := stringArg(args, "workflow_yaml")
	if yamlPath == "" {
		s.writeToolResult(req.ID, "", "workflow_yaml required", true)
		return
	}

	cliArgs := agentRunArgs(yamlPath, args)
	logPath := s.runLogPath("agent-run")
	runID, pid, err := s.spawnDetachedRun(cliArgs, logPath)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.writeJSONToolResult(req.ID, map[string]any{
		"run_id":     runID,
		"pid":        pid,
		"log_path":   logPath,
		"workflow":   yamlPath,
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"status":     "spawned",
	})
}

// agentRunArgs is the `chb agent-run` command line for a chb_agent_run
// call.
func agentRunArgs(yamlPath string, args map[string]any) []string {
	cliArgs := []string{"agent-run", yamlPath}
	cliArgs = withStringFlag(cliArgs, "--budget-mode", stringArg(args, "budget_mode"))
	if v, ok := positiveArg(args, "max_iterations"); ok {
		cliArgs = append(cliArgs, "--max-iterations", strconv.Itoa(int(v)))
	}
	if v, ok := positiveArg(args, "max_cost_usd"); ok {
		cliArgs = append(cliArgs, "--max-cost-usd", fmt.Sprintf("%.4f", v))
	}
	cliArgs = withStringFlag(cliArgs, "--provider", stringArg(args, "provider"))
	// Always explicit: `chb agent-run`'s own default is
	// "validate/auto-corrections", so omitting the flag would turn on git
	// auto-commit to a branch the caller never named.
	cliArgs = append(cliArgs, "--branch", stringArg(args, "branch"))
	cliArgs = withBoolFlag(cliArgs, "--allow-dirty", boolArg(args, "allow_dirty"))
	cliArgs = withBoolFlag(cliArgs, "--dry-run", boolArg(args, "dry_run"))
	return withStringFlag(cliArgs, "--inputs", workflowInputsJSON(args))
}

// workflowInputsJSON is a call's workflow inputs as JSON, "" when it names
// none.
func workflowInputsJSON(args map[string]any) string {
	inputs, _ := args["inputs"].(map[string]any)
	if len(inputs) == 0 {
		return ""
	}
	b, _ := json.Marshal(inputs)
	return string(b)
}

// handleRunTotals queries the same DB the MCP server already has open
// via s.store. Mirrors the query in internal/cli/run_totals.go but is
// re-implemented here to keep the two command packages independent.
func (s *mcpServer) handleRunTotals(req rpcRequest, args map[string]any) {
	runID, ok := s.storeRunID(req, args)
	if !ok {
		return
	}
	totals, err := s.runTotals(runID)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.writeJSONToolResult(req.ID, totals)
}

// storeRunID is the call's run_id once the server has a store and the call
// names a positive run; else it answers the call with why, and reports
// false.
func (s *mcpServer) storeRunID(req rpcRequest, args map[string]any) (int64, bool) {
	if s.store == nil {
		s.writeToolResult(req.ID, "", "HIVE_DB_PATH not set", true)
		return 0, false
	}
	v, ok := positiveArg(args, "run_id")
	if !ok {
		s.writeToolResult(req.ID, "", "run_id (positive integer) required", true)
		return 0, false
	}
	return int64(v), true
}

// runTotals is chb_run_totals' result: the run's node counts and spend,
// with the constraint probes' calls, which belong to no node: the run's
// row carries them.
func (s *mcpServer) runTotals(runID int64) (map[string]any, error) {
	if err := s.requireWorkflowRun(runID); err != nil {
		return nil, err
	}
	totals, err := s.nodeTotals(runID)
	if err != nil {
		return nil, err
	}
	probe, err := db.ReadProbeUsage(s.store.ReadDB, runID)
	if err != nil {
		return nil, err
	}
	totals.addProbeUsage(probe)
	return totals.result(runID), nil
}

// requireWorkflowRun refuses a run that does not exist. Such a run
// aggregates to all-zero totals, which read as "this run exists and did
// nothing". Say so instead: an unknown run is a tool-level error
// (isError:true), not a zeroed success.
func (s *mcpServer) requireWorkflowRun(runID int64) error {
	var exists int
	if err := s.store.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM workflow_runs WHERE id=?`, runID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("look up run %d: %w", runID, err)
	}
	if exists == 0 {
		return fmt.Errorf("no workflow run %d in this workspace", runID)
	}
	return nil
}

// runNodeTotals is a run's node counts and spend.
type runNodeTotals struct {
	total, completed, rejected, failed int64
	tokensIn, tokensOut, costX10000    int64
	metered, unmetered                 int64
}

// nodeTotals sums the run's workflow_node_states rows.
func (s *mcpServer) nodeTotals(runID int64) (*runNodeTotals, error) {
	row := s.store.ReadDB.QueryRow(`
		SELECT
		   COUNT(*),
		   COALESCE(SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END), 0),
		   COALESCE(SUM(CASE WHEN status='rejected'  THEN 1 ELSE 0 END), 0),
		   COALESCE(SUM(CASE WHEN status='failed'    THEN 1 ELSE 0 END), 0),
		   COALESCE(SUM(tokens_in),       0),
		   COALESCE(SUM(tokens_out),      0),
		   COALESCE(SUM(cost_usd_x10000), 0),
		   COALESCE(SUM(metered_calls),   0),
		   COALESCE(SUM(unmetered_calls), 0)
		 FROM workflow_node_states WHERE run_id=?`, runID)
	t := &runNodeTotals{}
	if err := row.Scan(&t.total, &t.completed, &t.rejected, &t.failed,
		&t.tokensIn, &t.tokensOut, &t.costX10000, &t.metered, &t.unmetered); err != nil {
		return nil, fmt.Errorf("aggregate run %d: %w", runID, err)
	}
	return t, nil
}

// addProbeUsage adds the run's constraint-probe calls to its spend.
func (t *runNodeTotals) addProbeUsage(probe db.ProbeUsage) {
	t.tokensIn += probe.TokensIn
	t.tokensOut += probe.TokensOut
	t.costX10000 += probe.CostUSDx10000
	t.metered += probe.MeteredCalls
	t.unmetered += probe.UnmeteredCalls
}

// result is the totals in the shape `chb run-totals <id>` emits.
func (t *runNodeTotals) result(runID int64) map[string]any {
	return map[string]any{
		"run_id":          runID,
		"nodes_total":     t.total,
		"nodes_completed": t.completed,
		"nodes_rejected":  t.rejected,
		"nodes_failed":    t.failed,
		"tokens_in":       t.tokensIn,
		"tokens_out":      t.tokensOut,
		"cost_usd_x10000": t.costX10000,
		"metered_calls":   t.metered,
		"unmetered_calls": t.unmetered,
		"cost_usd":        costUSDLabel(t.costX10000, t.metered, t.unmetered),
	}
}

// costUSDLabel renders a cost in 1/10000 USD as dollars to four places, or
// "unmetered" where no call's cost is known (models.CostLabel).
func costUSDLabel(costX10000, metered, unmetered int64) string {
	return models.CostLabel(fmt.Sprintf("%.4f", float64(costX10000)/10000.0), metered, unmetered)
}

// handleNodeRationale returns the persisted rationale (final text) for one
// node of one run: the MCP read path for the output of synthesize /
// decompose / evaluate, which extract_findings (JSON from `audit-` prefixed
// nodes), run_totals (aggregates) and the summary/findings tools (the CDE
// findings table) do not give. An MCP host can spawn a workflow, poll until
// done, and read the answer without shelling out to the CLI.
func (s *mcpServer) handleNodeRationale(req rpcRequest, args map[string]any) {
	runID, ok := s.storeRunID(req, args)
	if !ok {
		return
	}
	nodeName := stringArg(args, "node_name")
	if nodeName == "" {
		s.writeToolResult(req.ID, "", "node_name required", true)
		return
	}
	rationale, err := s.nodeRationale(runID, nodeName)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.writeJSONToolResult(req.ID, rationale)
}

// nodeRationale is chb_node_rationale's result: the node's status,
// rationale and spend.
func (s *mcpServer) nodeRationale(runID int64, nodeName string) (map[string]any, error) {
	row := s.store.ReadDB.QueryRow(`
		SELECT status, COALESCE(rationale,''), COALESCE(tokens_in,0),
		       COALESCE(tokens_out,0), COALESCE(cost_usd_x10000,0),
		       COALESCE(metered_calls,0), COALESCE(unmetered_calls,0)
		  FROM workflow_node_states WHERE run_id=? AND node_name=?`,
		runID, nodeName)
	var status, rationale string
	var tIn, tOut, costX, metered, unmetered int64
	if err := row.Scan(&status, &rationale, &tIn, &tOut, &costX, &metered, &unmetered); err != nil {
		return nil, fmt.Errorf("read node %q on run %d: %w", nodeName, runID, err)
	}
	return map[string]any{
		"run_id":     runID,
		"node_name":  nodeName,
		"status":     status,
		"rationale":  rationale,
		"tokens_in":  tIn,
		"tokens_out": tOut,
		"cost_usd":   costUSDLabel(costX, metered, unmetered),
	}, nil
}

// handleRunState reads workflow_runs.state_json and returns either the
// full map (when key is empty) or one specific field.
//
// state_json holds each completed node's *parsed outputs*, merged into one
// flat namespace. It is not an untruncated mirror of the rationale: the final
// text survives only when the agent emitted prose (ExtractJSONOutput's
// final_text fallback) and no later node reused that key, and parallel
// siblings sharing an output key keep only the last to commit. Reach for
// workflow_node_states.rationale for per-node text, capped at 64,000 bytes.
func (s *mcpServer) handleRunState(req rpcRequest, args map[string]any) {
	runID, ok := s.storeRunID(req, args)
	if !ok {
		return
	}
	state, err := s.runState(runID)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	if key := stringArg(args, "key"); key != "" {
		s.writeJSONToolResult(req.ID, runStateField(runID, state, key))
		return
	}
	s.writeJSONToolResult(req.ID, map[string]any{
		"run_id": runID,
		"state":  state,
		"keys":   stateKeys(state),
	})
}

// runState is the run's state_json, an empty map for none.
func (s *mcpServer) runState(runID int64) (map[string]any, error) {
	var stateJSON string
	row := s.store.ReadDB.QueryRow(`SELECT COALESCE(state_json,'{}') FROM workflow_runs WHERE id=?`, runID)
	if err := row.Scan(&stateJSON); err != nil {
		return nil, fmt.Errorf("read state for run %d: %w", runID, err)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, fmt.Errorf("parse state_json for run %d: %w", runID, err)
	}
	if state == nil {
		state = map[string]any{}
	}
	return state, nil
}

// runStateField is chb_run_state's result for one state field: its value,
// with its length when it is a string, or, when the state has no such
// key, the keys it has.
func runStateField(runID int64, state map[string]any, key string) map[string]any {
	val, present := state[key]
	if !present {
		return map[string]any{
			"run_id":         runID,
			"key":            key,
			"value":          nil,
			"length":         0,
			"present":        false,
			"available_keys": stateKeys(state),
		}
	}
	out := map[string]any{
		"run_id":  runID,
		"key":     key,
		"value":   val,
		"present": true,
	}
	if valStr, isStr := val.(string); isStr {
		out["length"] = len(valStr)
	}
	return out
}

// stateKeys is the keys of a run's state, in no particular order.
func stateKeys(state map[string]any) []string {
	keys := make([]string, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	return keys
}

// handleSwarm spawns `chb ask <question> --dispatch` and
// returns {run_id, pid, log_path}. The host polls run_totals to follow
// progress.
func (s *mcpServer) handleSwarm(req rpcRequest, args map[string]any) {
	question := stringArg(args, "question")
	if question == "" {
		s.writeToolResult(req.ID, "", "question required", true)
		return
	}
	bin := s.chbBin()
	env := s.scriptEnv(args)

	// `chb ask` dispatches by default; --no-dispatch is the
	// explicit opt-out. We omit it here so the spawn actually runs.
	cliArgs := []string{"ask", question}
	cliArgs = withStringFlag(cliArgs, "--foragers", stringArg(args, "foragers"))
	cliArgs = withStringFlag(cliArgs, "--budget-mode", stringArg(args, "budget_mode"))
	cliArgs = withStringFlag(cliArgs, "--model", stringArg(args, "model"))
	cliArgs = withStringFlag(cliArgs, "--synthesizer-model", stringArg(args, "synthesizer_model"))

	logPath := s.runLogPath("swarm")
	runID, pid, err := s.spawnDetachedScriptWithEnv(bin, cliArgs, env, "", logPath)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.writeJSONToolResult(req.ID, map[string]any{
		"run_id":     runID,
		"pid":        pid,
		"log_path":   logPath,
		"question":   question,
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"status":     "spawned",
	})
}

// handleSetBudgetMode mutates the server's session-scoped budget
// override. It reaches every later run for the lifetime of this MCP
// session: through the env of the runs chb_agent_run, chb_swarm,
// chb_self_review and chb_self_implement spawn, and through the
// runner config of chb_research's in-process run. Survives only until
// the MCP server exits.
func (s *mcpServer) handleSetBudgetMode(req rpcRequest, args map[string]any) {
	mode := strings.ToLower(strings.TrimSpace(stringArg(args, "mode")))
	switch mode {
	case "premium", "standard", "cheap", "free":
		// The four positions of the cost dial, as every tool spec advertises
		// them.
	default:
		s.writeToolResult(req.ID, "", fmt.Sprintf("invalid mode %q (want premium|standard|cheap|free)", mode), true)
		return
	}
	s.mu.Lock()
	s.budgetMode = mode
	s.mu.Unlock()
	s.writeJSONToolResult(req.ID, map[string]any{
		"mode":   mode,
		"effect": fmt.Sprintf("runs started from now on use budget mode %s", mode),
	})
}
