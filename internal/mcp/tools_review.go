package mcp

// The review tools: the self-review and self-implement loop.
// chb_extract_findings, chb_render_review and chb_gen_implement_workflow
// call the Go internals in process, with no subprocess and no parsing of CLI
// text; chb_self_review and chb_self_implement spawn `chb review` and
// `chb implement`, detached.

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/review"
)

func extractFindingsSpec() map[string]any {
	return map[string]any{
		"name":        "chb_extract_findings",
		"title":       "Extract findings",
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		"description": "Extract structured findings from workflow_node_states.rationale (the persisted agent output). Returns {by_lens, all_findings, totals, parse_failures}. Read-only; no run started.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"run_id":      map[string]any{"type": "integer", "description": "Workflow run to read; defaults to the most recent run"},
				"node_prefix": map[string]any{"type": "string", "description": "Default: 'audit-'"},
			},
		},
	}
}

func renderReviewSpec() map[string]any {
	return map[string]any{
		"name":        "chb_render_review",
		"title":       "Render review",
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true, "openWorldHint": false},
		"description": "Render a markdown review report from extracted findings. Reads findings.json, writes REVIEW.md.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"findings_json": map[string]any{"type": "string", "description": "Path to extract-findings JSON output"},
				"out":           map[string]any{"type": "string", "description": "Path for the rendered REVIEW.md"},
				"workflow":      map[string]any{"type": "string", "description": "Workflow name shown in the report header"},
				"provider":      map[string]any{"type": "string", "description": "Provider label shown in the report header"},
				"run_id":        map[string]any{"type": "integer", "description": "Workflow run id shown in the report header"},
				"cost_usd":      map[string]any{"type": "number", "description": "Run cost in US dollars, shown in the report header"},
				"tokens_in":     map[string]any{"type": "integer", "description": "Input tokens, shown in the report header"},
				"tokens_out":    map[string]any{"type": "integer", "description": "Output tokens, shown in the report header"},
			},
			"required": []string{"findings_json", "out"},
		},
	}
}

func genImplementWorkflowSpec() map[string]any {
	return map[string]any{
		"name":        "chb_gen_implement_workflow",
		"title":       "Generate fix workflow",
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true, "openWorldHint": false},
		"description": "Generate a self-implement workflow YAML from a findings.json. Each node applies one fix gated on `outputs.compile_ok && outputs.tests_pass`, with on_reject: escalation. Returns {workflow_yaml_path, fix_count}.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"findings_json": map[string]any{"type": "string", "description": "Path to a findings.json produced by chb_extract_findings"},
				"out":           map[string]any{"type": "string", "description": "Path to write the generated workflow YAML"},
				"severity":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Severities to include, e.g. [\"critical\",\"high\"] (default critical and high)"},
				"max_fixes":     map[string]any{"type": "integer", "description": "Cap on fix nodes generated; 0 means no cap"},
				"model":         map[string]any{"type": "string", "description": "Model for the fix workers (default: tier worker under the budget mode)"},
				"repair_model":  map[string]any{"type": "string", "description": "Model for the repair attempt when a fix fails acceptance (default: tier synthesist under the budget mode)"},
			},
			"required": []string{"findings_json", "out"},
		},
	}
}

func selfReviewSpec() map[string]any {
	return map[string]any{
		"name":        "chb_self_review",
		"title":       "Self-review this repo",
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true},
		"description": "Run `chb review` end-to-end: 5-lens audit + synthesis + extract + render + regression. This spends real LLM calls — called with no arguments it starts a paid run. Spawns the run and returns {run_id, pid, report_path, log_path}; stop a run by terminating pid.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"provider": map[string]any{"type": "string", "enum": []string{"anthropic", "gemini", "openai", "claude-cli", "gemini-cli"}, "description": "LLM provider; omit to auto-detect"},
			},
		},
	}
}

func selfImplementSpec() map[string]any {
	return map[string]any{
		"name":        "chb_self_implement",
		"title":       "Implement review findings",
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true},
		"description": "Run `chb implement`: read findings.json, generate a fix workflow, and run it with auto-commit to a branch — which spends real LLM calls, up to max_cost_usd (default $10), and opens a pull request unless no_pr is true. Called with no arguments it starts a paid run. Use dry_run to rehearse. Returns {run_id, pid, branch, log_path}; stop a run by terminating pid. Refuses to start on a dirty working tree unless allow_dirty is true.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"findings":     map[string]any{"type": "string", "description": "Path to findings.json (default: workspace/self-review/findings.json)"},
				"severity":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Severities to fix, e.g. [\"critical\",\"high\"] (the default)"},
				"max_fixes":    map[string]any{"type": "integer", "description": "Cap on fixes attempted (default 5); 0 means no cap"},
				"model":        map[string]any{"type": "string", "description": "Model for the fix workers (default: tier worker under the budget mode)"},
				"repair_model": map[string]any{"type": "string", "description": "Model for the repair attempt when a fix fails acceptance (default: tier synthesist under the budget mode)"},
				"provider":     map[string]any{"type": "string", "enum": []string{"anthropic", "gemini", "openai", "claude-cli", "gemini-cli"}, "description": "LLM provider; omit to auto-detect"},
				"allow_dirty":  map[string]any{"type": "boolean", "description": "Proceed even if the working tree has uncommitted changes (they will be swept into the fix branch)"},
				"branch":       map[string]any{"type": "string", "description": "Branch to commit to (default: self-implement/<utc-timestamp>, chosen by the server and returned)"},
				"dry_run":      map[string]any{"type": "boolean", "description": "Generate and walk the workflow without calling any model or committing"},
				"no_pr":        map[string]any{"type": "boolean", "description": "Commit to the branch but do not open a pull request"},
				"max_cost_usd": map[string]any{"type": "number", "description": "Stop the run once cumulative cost reaches this many US dollars (default 10)"},
			},
		},
	}
}

// handleExtractFindings calls review.Extract directly (no subprocess).
func (s *mcpServer) handleExtractFindings(req rpcRequest, args map[string]any) {
	if s.store == nil {
		s.writeToolResult(req.ID, "", "HIVE_DB_PATH not set", true)
		return
	}
	prefix := cmp.Or(stringArg(args, "node_prefix"), "audit-")
	agg, err := review.Extract(s.store.ReadDB, prefix, optionalRunID(args))
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.writeJSONToolResult(req.ID, agg)
}

// optionalRunID is the call's run_id when it names a positive one, else 0.
func optionalRunID(args map[string]any) int64 {
	if v, ok := positiveArg(args, "run_id"); ok {
		return int64(v)
	}
	return 0
}

// handleRenderReview reads findings JSON + writes REVIEW.md via review.Render.
func (s *mcpServer) handleRenderReview(req rpcRequest, args map[string]any) {
	in, out := stringArg(args, "findings_json"), stringArg(args, "out")
	if in == "" || out == "" {
		s.writeToolResult(req.ID, "", "findings_json and out are required", true)
		return
	}
	md, err := renderReviewFile(in, out, review.Meta{
		Workflow:  stringArg(args, "workflow"),
		Provider:  stringArg(args, "provider"),
		RunID:     runIDString(args),
		CostUSD:   floatArg(args, "cost_usd"),
		TokensIn:  int64Arg(args, "tokens_in"),
		TokensOut: int64Arg(args, "tokens_out"),
	})
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.writeJSONToolResult(req.ID, map[string]any{
		"markdown_path": out,
		"char_count":    len(md),
	})
}

// renderReviewFile renders the findings JSON at in as a markdown review
// with meta in its header, writes it to out, and returns it.
func renderReviewFile(in, out string, meta review.Meta) (string, error) {
	agg, err := loadFindingsAggregate(in)
	if err != nil {
		return "", err
	}
	md, err := review.Render(agg, meta)
	if err != nil {
		return "", err
	}
	return md, writeToolOutputFile(out, md)
}

// handleGenImplementWorkflow calls review.GenerateImplementWorkflow directly.
func (s *mcpServer) handleGenImplementWorkflow(req rpcRequest, args map[string]any) {
	in, out := stringArg(args, "findings_json"), stringArg(args, "out")
	if in == "" || out == "" {
		s.writeToolResult(req.ID, "", "findings_json and out are required", true)
		return
	}
	fixCount, err := genImplementWorkflowFile(in, out, implementOptions(args))
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.writeJSONToolResult(req.ID, map[string]any{
		"workflow_yaml_path": out,
		"fix_count":          fixCount,
	})
}

// implementOptions is the fix workflow's options from a
// chb_gen_implement_workflow call. 0 means "no cap", so an absent max_fixes
// must not become one — an uncapped self-implement spawns a fix agent per
// finding. An explicit 0 stays 0.
func implementOptions(args map[string]any) review.ImplementOptions {
	maxFixes := 5
	if v, ok := args["max_fixes"].(float64); ok {
		maxFixes = int(v)
	}
	return review.ImplementOptions{
		Severities:  stringSliceArg(args, "severity"),
		MaxFixes:    maxFixes,
		Model:       stringArg(args, "model"),
		RepairModel: stringArg(args, "repair_model"),
	}
}

// genImplementWorkflowFile generates the fix workflow for the findings
// JSON at in, writes it to out, and returns how many fixes it holds.
func genImplementWorkflowFile(in, out string, opts review.ImplementOptions) (int, error) {
	agg, err := loadFindingsAggregate(in)
	if err != nil {
		return 0, err
	}
	yamlText, fixCount, err := review.GenerateImplementWorkflow(agg, opts)
	if err != nil {
		return 0, err
	}
	return fixCount, writeToolOutputFile(out, yamlText)
}

// loadFindingsAggregate reads the findings JSON at path.
func loadFindingsAggregate(path string) (*review.Aggregate, error) {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, err
	}
	var agg review.Aggregate
	if err := json.Unmarshal(data, &agg); err != nil {
		return nil, fmt.Errorf("parse findings: %w", err)
	}
	return &agg, nil
}

// writeToolOutputFile writes text to path, making its directory first.
func writeToolOutputFile(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// handleSelfReview spawns `chb review` (native cobra command —
// no shell script). Returns {run_id, pid, report_path, log_path}.
func (s *mcpServer) handleSelfReview(req rpcRequest, args map[string]any) {
	repoRoot := s.repoRoot()
	bin := s.chbBin()
	env := s.scriptEnv(args)
	reportDir := filepath.Join(repoRoot, "workspace", "self-review")
	// --target/--workspace are passed rather than inherited, so report_path
	// below is true because the server named the path it reports.
	cliArgs := []string{"review",
		"--target", repoRoot,
		"--workspace", reportDir,
	}

	logPath := s.runLogPath("self-review")
	runID, pid, err := s.spawnDetachedScriptWithEnv(bin, cliArgs, env, repoRoot, logPath)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	reportPath := filepath.Join(reportDir, "REVIEW.md")
	s.writeJSONToolResult(req.ID, map[string]any{
		"run_id":      runID,
		"pid":         pid,
		"log_path":    logPath,
		"report_path": reportPath,
		"started_at":  time.Now().UTC().Format(time.RFC3339),
		"status":      "spawned",
	})
}

// handleSelfImplement spawns `chb implement` (native cobra command).
// Supports --findings, --severity, --max-fixes flags.
func (s *mcpServer) handleSelfImplement(req rpcRequest, args map[string]any) {
	repoRoot := s.repoRoot()
	bin := s.chbBin()
	env := s.scriptEnv(args)
	branch := selfImplementBranch(args)
	cliArgs := selfImplementArgs(repoRoot, branch, args)
	logPath := s.runLogPath("self-implement")
	runID, pid, err := s.spawnDetachedScriptWithEnv(bin, cliArgs, env, repoRoot, logPath)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.writeJSONToolResult(req.ID, map[string]any{
		"run_id":     runID,
		"pid":        pid,
		"branch":     branch,
		"log_path":   logPath,
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"status":     "spawned",
	})
}

// selfImplementBranch is the branch a chb_self_implement run commits to: the
// call's, else one the server names. The server names the branch, so the
// branch it reports is the branch the run uses.
func selfImplementBranch(args map[string]any) string {
	if branch := stringArg(args, "branch"); branch != "" {
		return branch
	}
	return "self-implement/" + time.Now().UTC().Format("2006-01-02-150405")
}

// selfImplementArgs is the `chb implement` command line for a
// chb_self_implement call. The workspace is passed rather than inherited, so
// it is not whatever the host's cwd resolves it to.
func selfImplementArgs(repoRoot, branch string, args map[string]any) []string {
	cliArgs := []string{"implement",
		"--workspace", filepath.Join(repoRoot, "workspace", "self-implement"),
	}
	cliArgs = withStringFlag(cliArgs, "--findings", selfImplementFindings(repoRoot, stringArg(args, "findings")))
	cliArgs = withStringFlag(cliArgs, "--severity", strings.Join(stringSliceArg(args, "severity"), ","))
	// Absent leaves `chb implement` its default of 5; an explicit 0 (no cap)
	// is passed on rather than dropped.
	if v, ok := args["max_fixes"].(float64); ok {
		cliArgs = append(cliArgs, "--max-fixes", strconv.Itoa(int(v)))
	}
	// Absent, `chb implement` leaves the fix nodes on tier worker and their
	// repair on tier synthesist, as chb_gen_implement_workflow does.
	cliArgs = withStringFlag(cliArgs, "--model", stringArg(args, "model"))
	cliArgs = withStringFlag(cliArgs, "--repair-model", stringArg(args, "repair_model"))
	cliArgs = withBoolFlag(cliArgs, "--allow-dirty", boolArg(args, "allow_dirty"))
	cliArgs = append(cliArgs, "--branch", branch)
	// A host can rehearse this and keep it off GitHub: the command's
	// --dry-run, --no-pr and cost cap are passed on.
	cliArgs = withBoolFlag(cliArgs, "--dry-run", boolArg(args, "dry_run"))
	cliArgs = withBoolFlag(cliArgs, "--no-pr", boolArg(args, "no_pr"))
	if v, ok := positiveArg(args, "max_cost_usd"); ok {
		cliArgs = append(cliArgs, "--max-cost-usd", strconv.FormatFloat(v, 'f', -1, 64))
	}
	return cliArgs
}

// selfImplementFindings is the findings path a call names, resolved
// against the repo root when relative; "" when it names none.
func selfImplementFindings(repoRoot, findings string) string {
	if findings == "" || filepath.IsAbs(findings) {
		return findings
	}
	return filepath.Join(repoRoot, findings)
}
