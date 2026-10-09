package mcp

// The tools beyond the five in tools_db.go, tools_research.go and
// tools_status.go: the registry that lists them, the table that dispatches
// every tool, and the argument helpers every tool handler shares. The tools
// themselves sit with their families: tools_run.go starts and reads runs,
// tools_review.go is the self-review and self-implement loop,
// tools_repo_audit.go is the structural MSS audit of a repository and
// tools_calibration.go is the outcomes ledger. A tool that runs `chb` as a
// detached subprocess starts it through spawn.go.

import (
	"context"
	"encoding/json"
	"strconv"
)

// capabilityToolSpecs returns the tool definitions of the run, review,
// repo-audit and calibration families, which allToolSpecs appends to the
// store's tools.
func capabilityToolSpecs() []any {
	return []any{
		preflightSpec(),
		agentRunSpec(),
		runTotalsSpec(),
		nodeRationaleSpec(),
		runStateSpec(),
		extractFindingsSpec(),
		renderReviewSpec(),
		genImplementWorkflowSpec(),
		selfReviewSpec(),
		selfImplementSpec(),
		swarmSpec(),
		setBudgetModeSpec(),
		mssRepoAuditSpec(),
		outcomeRecordSpec(),
		calibrationReadSpec(),
	}
}

// toolHandler serves one tools/call: req for its id, and the call's
// arguments, already held to the tool's schema.
type toolHandler func(s *mcpServer, ctx context.Context, req rpcRequest, args map[string]any)

// withoutContext adapts a handler that takes no context, the request's
// cancellation reaching nothing it does.
func withoutContext(handle func(*mcpServer, rpcRequest, map[string]any)) toolHandler {
	return func(s *mcpServer, _ context.Context, req rpcRequest, args map[string]any) {
		handle(s, req, args)
	}
}

// toolHandlers routes each tool name to its handler: the five in
// tools_db.go, tools_research.go and tools_status.go, then the capability
// surface (preflight, agent-run, run-totals, extract-findings,
// render-review, gen-implement-workflow, self-review/-implement, …).
var toolHandlers = map[string]toolHandler{
	"chb_db_write": withoutContext((*mcpServer).handleDBWrite),
	"chb_research": withoutContext((*mcpServer).handleResearch),
	"chb_status":   withoutContext((*mcpServer).handleStatus),
	"chb_summary":  withoutContext((*mcpServer).handleSummary),
	"chb_findings": withoutContext((*mcpServer).handleFindings),

	"chb_preflight":              (*mcpServer).handlePreflight,
	"chb_agent_run":              withoutContext((*mcpServer).handleAgentRun),
	"chb_run_totals":             withoutContext((*mcpServer).handleRunTotals),
	"chb_node_rationale":         withoutContext((*mcpServer).handleNodeRationale),
	"chb_run_state":              withoutContext((*mcpServer).handleRunState),
	"chb_extract_findings":       withoutContext((*mcpServer).handleExtractFindings),
	"chb_render_review":          withoutContext((*mcpServer).handleRenderReview),
	"chb_gen_implement_workflow": withoutContext((*mcpServer).handleGenImplementWorkflow),
	"chb_self_review":            withoutContext((*mcpServer).handleSelfReview),
	"chb_self_implement":         withoutContext((*mcpServer).handleSelfImplement),
	"chb_swarm":                  withoutContext((*mcpServer).handleSwarm),
	"chb_set_budget_mode":        withoutContext((*mcpServer).handleSetBudgetMode),
	"chb_mss_repo_audit":         (*mcpServer).handleMSSRepoAudit,
	"chb_outcome_record":         withoutContext((*mcpServer).handleOutcomeRecord),
	"chb_calibration_read":       withoutContext((*mcpServer).handleCalibrationRead),
}

// boolArg pulls a boolean from the JSON-RPC args map. Missing or
// non-bool values resolve to false (the conservative default).
func boolArg(m map[string]any, k string) bool {
	v, ok := m[k].(bool)
	return ok && v
}

// writeJSONToolResult marshals v as pretty JSON and emits it as the
// tool's text content. All capability handlers go through this so MCP
// clients get a single consistent shape they can JSON.parse.
func (s *mcpServer) writeJSONToolResult(id json.RawMessage, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		s.writeToolResult(id, "", "marshal: "+err.Error(), true)
		return
	}
	s.writeToolResult(id, string(b), "", false)
}

// arg-extraction helpers — thin tolerant wrappers around map[string]any.

func stringArg(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func floatArg(m map[string]any, k string) float64 {
	if v, ok := m[k].(float64); ok {
		return v
	}
	return 0
}

func intArg(m map[string]any, k string) int {
	if v, ok := m[k].(float64); ok {
		return int(v)
	}
	if v, ok := m[k].(int); ok {
		return v
	}
	return 0
}

func intArgDefault(m map[string]any, k string, def int) int {
	if v := intArg(m, k); v > 0 {
		return v
	}
	return def
}

func int64Arg(m map[string]any, k string) int64 {
	return int64(intArg(m, k))
}

// positiveArg is a numeric argument, and whether it is present and above
// zero.
func positiveArg(m map[string]any, k string) (float64, bool) {
	v, ok := m[k].(float64)
	return v, ok && v > 0
}

func stringSliceArg(m map[string]any, k string) []string {
	raw, ok := m[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// runIDString renders the run_id argument, an integer on every tool, for
// display.
func runIDString(args map[string]any) string {
	if v, ok := positiveArg(args, "run_id"); ok {
		return strconv.FormatInt(int64(v), 10)
	}
	return ""
}

// withStringFlag appends a command line's flag and value when the value is
// set.
func withStringFlag(argv []string, flag, value string) []string {
	if value == "" {
		return argv
	}
	return append(argv, flag, value)
}

// withBoolFlag appends a command line's flag when on.
func withBoolFlag(argv []string, flag string, on bool) []string {
	if !on {
		return argv
	}
	return append(argv, flag)
}
