package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/spf13/cobra"
)

// newMCPSmokeCmd implements `chb mcp-smoke`.
//
// Pipes a JSON-RPC 2.0 batch of `tools/call` requests into
// chb-mcp's stdin and verifies each tool's response shape.
// Pure Go: no python3, no jq, no sqlite3 binary dependency.
//
// Reuses Go database/sql to seed the workspace DB (one workflow_run +
// one node_state with synthetic rationale), os/exec to drive
// chb-mcp, encoding/json to parse responses.
func newMCPSmokeCmd() *cobra.Command {
	var (
		mcpBin     string
		chbBinFlag string
		timeoutSec int
	)
	cmd := &cobra.Command{
		Use:   "mcp-smoke",
		Short: "End-to-end smoke for chb-mcp over stdio",
		Long: `Pipes a JSON-RPC 2.0 batch into chb-mcp's stdin and asserts
each tool's response shape. Verifies:
  - initialize handshake returns protocolVersion
  - tools/list returns exactly 20 tools
  - 12 tool calls against a seeded database (summary, findings, status,
    run-totals, node-rationale, run-state, extract-findings, preflight,
    render-review, gen-implement-workflow, outcome-record,
    calibration-read), each required to succeed
  - run-totals on a missing run reports isError:true
  - every line chb-mcp writes to stdout is a JSON-RPC response

Plus dry-run proxies for the long-running tools (agent-run, implement)
to confirm preflight cleanly accepts them.

Exit 0 iff every check passes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			chbBin, err := smokeChbBin(chbBinFlag)
			if err != nil {
				return err
			}
			s := &mcpSmoke{
				mcpBin:     smokeMCPBin(mcpBin),
				chbBin:     chbBin,
				timeoutSec: timeoutSec,
				stdout:     cmd.OutOrStdout(),
				stderr:     cmd.ErrOrStderr(),
			}
			return s.run(context.Background())
		},
	}
	cmd.Flags().StringVar(&mcpBin, "mcp-bin", "",
		"path to chb-mcp binary (default: $MCP_BIN, else chb-mcp beside this binary, else on $PATH)")
	cmd.Flags().StringVar(&chbBinFlag, "chb-bin", "",
		"path to the chb binary used for seeding (default: this binary)")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 20,
		"hard timeout for the JSON-RPC batch (seconds)")
	return cmd
}

// smokeMCPBin is the chb-mcp the smoke drives: --mcp-bin, else $MCP_BIN,
// else chb-mcp beside this binary or on $PATH.
func smokeMCPBin(flag string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("MCP_BIN"); v != "" {
		return v
	}
	return siblingBinary("chb-mcp")
}

// smokeChbBin is the chb the smoke seeds with: --chb-bin, else this binary.
func smokeChbBin(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	return os.Executable()
}

type mcpSmoke struct {
	mcpBin         string
	chbBin         string
	timeoutSec     int
	stdout, stderr io.Writer
}

// run is the structured smoke-test orchestrator. Mirrors the bash
// version's stage layout but written entirely in Go.
func (s *mcpSmoke) run(ctx context.Context) error {
	ensureHarnessProviderEnv(os.Stderr)
	if _, err := os.Stat(s.mcpBin); err != nil {
		return fmt.Errorf("chb-mcp not found at %s — run: go build -o ~/bin/chb-mcp ./cmd/chb-mcp", s.mcpBin)
	}

	workspace, err := os.MkdirTemp("", "mcp-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	failed, err := s.runChecks(ctx, workspace)
	if err != nil {
		return err
	}
	return s.verdict(failed)
}

// runChecks seeds a workspace, runs the JSON-RPC batch and the dry-run
// proxies against it, and counts the checks that failed.
func (s *mcpSmoke) runChecks(ctx context.Context, workspace string) (int, error) {
	dbPath := filepath.Join(workspace, "hive.db")
	if err := s.seedDB(ctx, dbPath); err != nil {
		return 0, fmt.Errorf("seed db: %w", err)
	}
	withFindings, err := writeSmokeFindings(workspace)
	if err != nil {
		return 0, err
	}
	failed, err := s.batchChecks(ctx, dbPath, workspace)
	if err != nil {
		return 0, err
	}
	return failed + s.dryRunChecks(ctx, dbPath, workspace, withFindings), nil
}

// writeSmokeFindings writes the synthetic findings JSONs for the tools that
// take them on disk — findings.json, clean, and with-findings.json, one
// high finding — and returns the latter's path.
func writeSmokeFindings(workspace string) (string, error) {
	cleanFindings := filepath.Join(workspace, "findings.json")
	if err := os.WriteFile(cleanFindings, []byte(
		`{"by_lens":{"audit-smoke":{"lens":"smoke","verdict":"clean","summary":"ok","findings":[]}},`+
			`"all_findings":[],"totals":{"critical":0,"high":0,"medium":0,"low":0},"parse_failures":[]}`),
		0o644); err != nil {
		return "", err
	}
	withFindings := filepath.Join(workspace, "with-findings.json")
	if err := os.WriteFile(withFindings, []byte(
		`{"by_lens":{"audit-smoke":{"lens":"smoke","verdict":"needs_work","summary":"x",`+
			`"findings":[{"file":"a.go","line":1,"severity":"high","issue":"x","fix":"y"}]}},`+
			`"all_findings":[{"lens":"smoke","file":"a.go","line":1,"severity":"high","issue":"x","fix":"y"}],`+
			`"totals":{"critical":0,"high":1,"medium":0,"low":0},"parse_failures":[]}`),
		0o644); err != nil {
		return "", err
	}
	return withFindings, nil
}

// batchChecks pipes the JSON-RPC batch into chb-mcp and counts the failed
// checks of its answer. stdout is the protocol channel, so each line on it
// that is not a response with a usable id fails a check: a server printing
// diagnostics there does not go green.
func (s *mcpSmoke) batchChecks(ctx context.Context, dbPath, workspace string) (int, error) {
	results, stray, err := s.runJSONRPCBatch(ctx, dbPath, workspace)
	if err != nil {
		return 0, fmt.Errorf("json-rpc batch: %w", err)
	}
	failed := s.checkResults(results)
	for _, line := range stray {
		fmt.Fprintf(s.stderr, "  ✗ stdout line is not a JSON-RPC response with an id: %.200s\n", line)
		failed++
	}
	return failed, nil
}

// dryRunChecks runs the dry-run proxies for the long-running tools and
// counts the ones that failed.
func (s *mcpSmoke) dryRunChecks(ctx context.Context, dbPath, workspace, withFindings string) int {
	fmt.Fprintln(s.stdout, "")
	fmt.Fprintln(s.stdout, "── long-running tools (dry-run path proxies) ──")

	failed := 0
	// agent-run --dry-run proxy.
	if err := s.runDryAgentRun(ctx, dbPath, workspace); err != nil {
		fmt.Fprintf(s.stderr, "  ✗ agent-run --dry-run failed: %v\n", err)
		failed++
	} else {
		fmt.Fprintln(s.stdout, "  ✓ agent-run --dry-run on proof.yaml")
	}

	if err := s.runDryImplement(ctx, dbPath, workspace, withFindings); err != nil {
		fmt.Fprintf(s.stderr, "  ✗ implement --dry-run failed: %v\n", err)
		failed++
	} else {
		fmt.Fprintln(s.stdout, "  ✓ implement --dry-run (proxy: chb_self_implement)")
	}
	return failed
}

// verdict prints the smoke's outcome and fails when any check failed.
func (s *mcpSmoke) verdict(failed int) error {
	fmt.Fprintln(s.stdout, "")
	if failed > 0 {
		fmt.Fprintf(s.stderr, "MCP smoke: FAILURES (%d)\n", failed)
		return fmt.Errorf("%d MCP smoke checks failed", failed)
	}
	fmt.Fprintln(s.stdout, "MCP smoke: ALL GREEN")
	return nil
}

// seedDB inserts one workflow_run + one node_state with rationale so the
// DB-shape MCP tools have data, through database/sql directly.
func (s *mcpSmoke) seedDB(ctx context.Context, dbPath string) error {
	if err := s.initSmokeDB(ctx, dbPath); err != nil {
		return err
	}
	conn, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	return insertSmokeRows(conn)
}

// initSmokeDB bootstraps the schema via the binary's db-init, so the smoke
// test never keeps a second copy of the schema.
func (s *mcpSmoke) initSmokeDB(ctx context.Context, dbPath string) error {
	cmd := exec.CommandContext(ctx, s.chbBin, "--db", dbPath, "db-init")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("db-init: %w (output: %s)", err, string(out))
	}
	return nil
}

// insertSmokeRows inserts the workflow run, the finding and the node state
// the smoke's checks read.
func insertSmokeRows(conn *sql.DB) error {
	res, err := conn.Exec(
		`INSERT INTO workflow_runs (workflow_name, definition_yaml, inputs_json) VALUES (?, ?, ?)`,
		"smoke", "name: smoke", "{}",
	)
	if err != nil {
		return fmt.Errorf("insert workflow_runs: %w", err)
	}
	rid, _ := res.LastInsertId()
	// One finding, so summary, findings and status have something to report
	// and their checks can tell a working tool from an empty answer.
	if _, err := conn.Exec(
		`INSERT INTO findings (wave, agent, d1, mss_label, finding) VALUES (1, 'smoke', 0, 'definition', ?)`,
		smokeFinding,
	); err != nil {
		return fmt.Errorf("insert findings: %w", err)
	}
	rationale := "```json\n" +
		`{"lens":"smoke","verdict":"clean","summary":"ok","findings":[]}` +
		"\n```"
	if _, err := conn.Exec(
		`INSERT INTO workflow_node_states
		 (run_id, node_name, node_type, status, rationale, tokens_in, tokens_out, cost_usd_x10000, provider)
		 VALUES (?, 'audit-smoke', 'agent', 'completed', ?, 100, 200, 30, 'anthropic')`,
		rid, rationale,
	); err != nil {
		return fmt.Errorf("insert workflow_node_states: %w", err)
	}
	return nil
}

// smokeFinding is the text of the finding seedDB writes.
const smokeFinding = "mcp-smoke seeded finding"

// runJSONRPCBatch builds the JSON-RPC batch, pipes it into chb-mcp's
// stdin, and parses every response into a (id → result) map.
func (s *mcpSmoke) runJSONRPCBatch(ctx context.Context, dbPath, workspace string) (map[int]json.RawMessage, []string, error) {
	requests := buildSmokeBatch(workspace)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.mcpBin)
	cmd.Env = append(harnessChildEnv(), "HIVE_DB_PATH="+dbPath)
	var batchIn bytes.Buffer
	for _, r := range requests {
		batchIn.Write(r)
		batchIn.WriteByte('\n')
	}
	cmd.Stdin = &batchIn
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// timeout path
		if ctx.Err() != nil {
			fmt.Fprintf(s.stderr, "✗ chb-mcp timed out (>%ds)\n", s.timeoutSec)
			fmt.Fprintf(s.stderr, "stderr tail:\n%s\n", lastNLines(errBuf.String(), 20))
		}
		return nil, nil, fmt.Errorf("chb-mcp run: %w", err)
	}
	results, stray := parseRPCFrames(out.String())
	return results, stray, nil
}

// parseRPCFrames maps each response's id to its result, or to its error
// object. A line that does not parse, has no integer id, or carries neither
// result nor error is returned as stray rather than dropped.
func parseRPCFrames(out string) (map[int]json.RawMessage, []string) {
	results := make(map[int]json.RawMessage)
	var stray []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, payload, ok := parseRPCFrame(line)
		if !ok {
			stray = append(stray, line)
			continue
		}
		results[id] = payload
	}
	return results, stray
}

// parseRPCFrame reads one response line: its id, and its result, else its
// error object. It reports false for a line that does not parse, has no
// integer id, or carries neither result nor error.
func parseRPCFrame(line string) (int, json.RawMessage, bool) {
	var msg struct {
		ID     *int            `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	switch {
	case json.Unmarshal([]byte(line), &msg) != nil, msg.ID == nil:
		return 0, nil, false
	case msg.Result != nil:
		return *msg.ID, msg.Result, true
	case msg.Error != nil:
		return *msg.ID, msg.Error, true
	}
	return 0, nil, false
}

// buildSmokeBatch returns the canonical JSON-RPC payloads in id order.
// Mirrors the bash heredoc verbatim — same id assignments, same call
// shapes, so existing fixtures remain valid.
func buildSmokeBatch(workspace string) [][]byte {
	// Normalize to forward slashes so the paths can be embedded directly
	// into JSON string literals on Windows. Without this, `C:\Users\…`
	// becomes invalid JSON (`\U` is not a valid escape) and the MCP
	// server silently drops the malformed request.
	cleanFindings := filepath.ToSlash(filepath.Join(workspace, "findings.json"))
	withFindings := filepath.ToSlash(filepath.Join(workspace, "with-findings.json"))
	reviewOut := filepath.ToSlash(filepath.Join(workspace, "REVIEW.md"))
	implOut := filepath.ToSlash(filepath.Join(workspace, "impl.yaml"))

	mk := func(id int, name string, argsJSON string) []byte {
		return []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"%s","arguments":%s}}`,
			id, name, argsJSON))
	}

	return [][]byte{
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"mcp-smoke","version":"0"}}}`),
		[]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`),
		mk(100, "chb_summary", `{}`),
		mk(101, "chb_findings", `{"limit":5}`),
		mk(102, "chb_status", `{}`),
		mk(103, "chb_run_totals", `{"run_id":1}`),
		mk(108, "chb_node_rationale", `{"run_id":1,"node_name":"audit-smoke"}`),
		mk(109, "chb_run_state", `{"run_id":1}`),
		mk(104, "chb_extract_findings", `{"node_prefix":"audit-"}`),
		mk(105, "chb_preflight", `{"workflow_yaml":"workflows/proof.yaml"}`),
		mk(106, "chb_render_review", fmt.Sprintf(`{"findings_json":"%s","out":"%s"}`, cleanFindings, reviewOut)),
		mk(107, "chb_gen_implement_workflow", fmt.Sprintf(`{"findings_json":"%s","out":"%s","max_fixes":1}`, withFindings, implOut)),
		// The seeded finding is id 1: a human confirmation of it is the one
		// outcome the ledger holds.
		mk(111, "chb_outcome_record", `{"subject_kind":"finding","finding_id":1,"resolution":"confirmed","stated_confidence":80}`),
		mk(112, "chb_calibration_read", `{"kind":"label"}`),
		// A tool-originated failure must come back as isError:true inside a
		// result, not as a JSON-RPC protocol error.
		mk(110, "chb_run_totals", `{"run_id":999999}`),
	}
}

// smokeCheck is one assertion against the result object for a given
// JSON-RPC id. The check func reads the `result` field of the response
// and returns true if the required shape is present.
type smokeCheck struct {
	id   int
	name string
	test func(result json.RawMessage) bool
}

// checkResults runs every smoke assertion and prints pass/fail per id.
// Returns the count of failures.
func (s *mcpSmoke) checkResults(results map[int]json.RawMessage) int {
	checks := smokeChecks()
	failed := 0
	for _, c := range checks {
		if !s.checkResult(c, results) {
			failed++
		}
	}
	fmt.Fprintln(s.stdout, "")
	fmt.Fprintf(s.stdout, "  passed: %d / %d\n", len(checks)-failed, len(checks))
	if failed > 0 {
		fmt.Fprintf(s.stdout, "  failed: %d\n", failed)
	}
	return failed
}

// smokeChecks are the assertions against each JSON-RPC id's response, in
// the order they run.
func smokeChecks() []smokeCheck {
	return []smokeCheck{
		{1, "initialize", hasProtocolVersion},
		{2, "tools/list (count == 20)", listsTwentyTools},
		{100, "chb_summary", textContains(`"total_findings": 1`)},
		{101, "chb_findings", textContains(smokeFinding)},
		{102, "chb_status", textContains(`"findings_count": 1`)},
		{103, "chb_run_totals", textContains(`"nodes_total"`)},
		{108, "chb_node_rationale", textContains(`"rationale"`, `smoke`)},
		{109, "chb_run_state", textContains(`"keys"`)},
		{104, "chb_extract_findings", textContains(`"by_lens"`, `smoke`)},
		// `"pass": true`, not `"pass"`: the key is present on a failing
		// preflight too, including one where chb-mcp could not resolve chb.
		{105, "chb_preflight", textContains(`"pass": true`)},
		{106, "chb_render_review", textContains(`"markdown_path"`)},
		{110, "chb_run_totals (missing run → isError:true)", isErrorTrue},
		{107, "chb_gen_implement_workflow", textContains(`"workflow_yaml_path"`, `"fix_count"`)},
		{111, "chb_outcome_record", textContains(`"id": 1`, `"source": "human"`)},
		{112, "chb_calibration_read", textContains(`"scores"`, `correlational`)},
	}
}

// checkResult runs one assertion against its id's response and prints the
// outcome. It reports whether the assertion passed.
func (s *mcpSmoke) checkResult(c smokeCheck, results map[int]json.RawMessage) bool {
	res, ok := results[c.id]
	if !ok {
		fmt.Fprintf(s.stderr, "  ✗ id=%-3d %-38s no response\n", c.id, c.name)
		return false
	}
	if !c.test(res) {
		fmt.Fprintf(s.stderr, "  ✗ id=%-3d %-38s check failed\n", c.id, c.name)
		fmt.Fprintln(s.stderr, smokeSnippet(res))
		return false
	}
	fmt.Fprintf(s.stdout, "  ✓ id=%-3d %s\n", c.id, c.name)
	return true
}

// smokeSnippet is the start of a response that failed its check: at most
// 300 bytes, then an ellipsis.
func smokeSnippet(res json.RawMessage) string {
	snippet := string(res)
	if len(snippet) > 300 {
		snippet = clip(snippet, 300) + "…"
	}
	return snippet
}

// hasProtocolVersion asserts the initialize handshake returned a
// protocolVersion.
func hasProtocolVersion(r json.RawMessage) bool {
	return strings.Contains(string(r), `"protocolVersion"`)
}

// listsTwentyTools asserts tools/list returned exactly 20 tools.
func listsTwentyTools(r json.RawMessage) bool {
	var v struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(r, &v); err != nil {
		return false
	}
	return len(v.Tools) == 20
}

// isErrorTrue asserts the tool reported its own failure in-band:
// isError=true on the result, rather than a JSON-RPC protocol error.
// That is the MCP contract for errors that originate inside a tool.
func isErrorTrue(r json.RawMessage) bool {
	var v struct {
		IsError *bool `json:"isError"`
	}
	if err := json.Unmarshal(r, &v); err != nil {
		return false
	}
	return v.IsError != nil && *v.IsError
}

// textContains returns a check that the tool succeeded (isError:false)
// and the "text" field of its first content block contains every supplied
// substring.
func textContains(substrs ...string) func(json.RawMessage) bool {
	return func(r json.RawMessage) bool {
		text, ok := smokeToolText(r)
		return ok && smokeTextHasAll(text, substrs)
	}
}

// smokeToolResult is the part of a tools/call result the checks read.
type smokeToolResult struct {
	IsError *bool `json:"isError"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

// succeeded reports whether the tool reported success (isError:false) and
// returned content.
func (t smokeToolResult) succeeded() bool {
	return t.IsError != nil && !*t.IsError && len(t.Content) > 0
}

// smokeToolText is the text of the first content block of a tool's
// successful result, or false when the result does not parse or the tool
// did not succeed.
func smokeToolText(r json.RawMessage) (string, bool) {
	var v smokeToolResult
	if err := json.Unmarshal(r, &v); err != nil || !v.succeeded() {
		return "", false
	}
	return v.Content[0].Text, true
}

// smokeTextHasAll reports whether text contains every one of substrs.
func smokeTextHasAll(text string, substrs []string) bool {
	for _, s := range substrs {
		if !strings.Contains(text, s) {
			return false
		}
	}
	return true
}

// runDryAgentRun proxies the long-running chb_agent_run MCP tool
// by invoking `chb agent-run --dry-run` against a known-good
// workflow. Confirms preflight + dispatch wiring without burning cost.
func (s *mcpSmoke) runDryAgentRun(ctx context.Context, dbPath, workspace string) error {
	cmd := exec.CommandContext(ctx, s.chbBin,
		"agent-run", "workflows/proof.yaml",
		"--inputs", `{"target_file":"workspace/repo-audit/REPO_AUDIT.md","result_path":"workspace/proof/result.txt"}`,
		"--dry-run", "--branch", "", "--max-iterations", "20",
	)
	cmd.Env = append(os.Environ(), "HIVE_DB_PATH="+dbPath)
	logPath := filepath.Join(workspace, "agent-run.log")
	logFile, _ := os.Create(logPath)
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
}

// runDryImplement proxies chb_self_implement via the native
// `chb implement --dry-run` (no shell-script dependency).
func (s *mcpSmoke) runDryImplement(ctx context.Context, dbPath, workspace, findings string) error {
	cmd := exec.CommandContext(ctx, s.chbBin,
		"implement", "--dry-run", "--max-fixes", "1",
		"--findings", findings,
		"--workspace", filepath.Join(workspace, "self-implement"),
		"--no-pr",
	)
	cmd.Env = append(os.Environ(), "HIVE_DB_PATH="+dbPath)
	logPath := filepath.Join(workspace, "self-impl.log")
	logFile, _ := os.Create(logPath)
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
}

// lastNLines returns the trailing n lines of s (debug-helper).
func lastNLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// siblingBinary is the named binary beside the running chb — resolved
// through symlinks, with .exe on Windows — else the name as found on $PATH,
// else the bare name.
func siblingBinary(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if candidate, ok := besideExecutable(name); ok {
		return candidate
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

// besideExecutable is name in the running binary's directory, the binary
// resolved through symlinks, when that file exists.
func besideExecutable(name string) (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	candidate := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(candidate); err != nil {
		return "", false
	}
	return candidate, true
}
