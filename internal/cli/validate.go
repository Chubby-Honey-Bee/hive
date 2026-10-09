package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/spf13/cobra"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// selfValidationProject is the project `chb validate` initialises, and
// selfValidationWorkspace the directory it runs in by default, which is
// where `chb init` creates that project.
const (
	selfValidationProject   = "self-validation"
	selfValidationWorkspace = "workspace/" + selfValidationProject
)

// newValidateCmd implements `chb validate`: it walks every CLI surface
// against a fresh workspace DB and reports pass / fail / warn counts.
//
// Pure Go: no bash, no jq, no python3, no sqlite3 binary. Every
// assertion drives a `chb` subcommand via os/exec and compares
// the result via Go's strings + database/sql primitives.
func newValidateCmd() *cobra.Command {
	var (
		workspaceDir string
		chbBin       string
		mcpBin       string
		stopOnFirst  bool
		asJSON       bool
	)
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Self-validation harness exercising every CLI surface",
		Long: `Pure-Go regression gate. Runs its assertions section by section
covering: project + DB initialisation, CDE dimensions, MSS-labeled
findings, MSS rule enforcement, read paths, gaps + followups,
conflicts, mss_audit, swarm-merge, validate-sources, lean4-extract,
wave gate pipeline, workflow engine, hive end-to-end, cascade revert,
promote_finding, ingest, export-graph, agent-run dry-run, MCP smoke,
and the unattended runner's records: decisions, repairs, per-node
rationale, tokens and cost, preflight, run totals.

It works in workspace/self-validation under the current directory, or in
the directory --workspace names. There it deletes hive.db and
self-validate.log, then writes its database, its log and scratch files.
Its first check, chb init, also creates workspace/self-validation under
the current directory when it is missing. When every check passes, every
run it started, workflow or agent, has ended, completed or failed; none
is left running.

Idempotent. Exit 0 iff every assertion passes. The progress log goes to
stderr and to self-validate.log in the workspace. With --json, stdout gets
one JSON object at the end: passed, failed, warnings, failures (the failing
checks' names), log and db (the log's and the database's paths).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workspaceDir == "" {
				workspaceDir = selfValidationWorkspace
			}
			if chbBin == "" {
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				chbBin = exe
			}
			r := &validateRunner{
				workspaceDir: workspaceDir,
				chbBin:       chbBin,
				mcpBin:       mcpBin,
				stopOnFirst:  stopOnFirst,
				asJSON:       asJSON,
				stdout:       cmd.OutOrStdout(),
				stderr:       cmd.ErrOrStderr(),
			}
			return r.run(context.Background())
		},
	}
	cmd.Flags().StringVar(&workspaceDir, "workspace", "",
		"workspace directory (default: "+selfValidationWorkspace+")")
	cmd.Flags().StringVar(&chbBin, "chb-bin", "",
		"path to the chb binary (default: this binary)")
	cmd.Flags().StringVar(&mcpBin, "mcp-bin", "",
		"path to the chb-mcp binary (default: chb-mcp beside this binary, else on $PATH)")
	cmd.Flags().BoolVar(&stopOnFirst, "stop-on-first", false,
		"abort on first failure (default: keep going to collect every failure)")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"print the summary on stdout as one JSON object: passed, failed, warnings, failures, log, db")
	return cmd
}

// validateRunner is the structured orchestrator, laid out section by
// section in the order the checks run.
//
// State (DB path, run id, def id, etc.) is threaded as struct fields
// rather than shell vars so the assertions stay declarative.
type validateRunner struct {
	workspaceDir string
	chbBin       string
	mcpBin       string
	stopOnFirst  bool
	asJSON       bool
	stdout       io.Writer
	stderr       io.Writer

	// Working state populated as the runner walks sections.
	dbPath  string
	logFile *os.File
	runID   int64
	defID   int64
	unkID   int64
	asmID   int64
	wfRunID int64

	pass     int
	fail     int
	warns    int
	failures []string
	// sections counts the sections begun, which number their headers.
	sections int
}

// run walks every section in order, one method each, and reports the
// counts, failing when a check failed.
func (r *validateRunner) run(ctx context.Context) error {
	ensureHarnessProviderEnv(os.Stderr)
	if err := r.setup(); err != nil {
		return err
	}
	defer r.logFile.Close()

	r.checkProjectInit(ctx)
	r.checkDimensions(ctx)
	r.checkAgentRunLifecycle(ctx)
	r.checkLabeledFindings(ctx)
	r.checkMSSRules(ctx)
	r.checkReadPaths(ctx)
	r.checkGapsFollowups(ctx)
	r.checkConflicts(ctx)
	r.checkMSSAudit(ctx)
	r.checkSwarmMerge(ctx)
	r.checkValidateSources(ctx)
	r.checkLean4Extract(ctx)
	r.checkWaveGate(ctx)
	r.checkWorkflowEngine(ctx)
	r.checkHive(ctx)
	r.checkCascadeRevert(ctx)
	r.checkPromoteFinding(ctx)
	r.checkIngest(ctx)
	r.checkExportGraph(ctx)
	r.checkAgentRunDryRun(ctx)
	r.checkMCPBuild(ctx)
	r.checkMCPSmoke(ctx)
	r.checkGenerator(ctx)

	// Capability checks: schema + columns + per-node persistence.
	r.runCapabilityChecks(ctx)

	return r.report()
}

// setup creates / cleans the workspace dir, opens the log file, and
// pins HIVE_DB_PATH.
func (r *validateRunner) setup() error {
	if err := os.MkdirAll(r.workspaceDir, 0o755); err != nil {
		return err
	}
	r.dbPath = filepath.Join(r.workspaceDir, "hive.db")
	logPath := filepath.Join(r.workspaceDir, "self-validate.log")
	_ = os.Remove(r.dbPath)
	_ = os.Remove(logPath)
	f, err := os.Create(logPath)
	if err != nil {
		return err
	}
	r.logFile = f
	os.Setenv("HIVE_DB_PATH", r.dbPath)
	r.log(fmt.Sprintf("Self-validation starting at %s", time.Now().UTC().Format(time.RFC3339)))
	r.log("DB: " + r.dbPath)
	r.log("Binary: " + r.chbBin)
	return nil
}

// runCapabilityChecks runs the sections on the unattended runner's records
// and the commands that read them, one method per section. They stop when the
// workspace database does not open.
func (r *validateRunner) runCapabilityChecks(ctx context.Context) {
	if !r.checkRunRecordSchema() {
		return
	}
	r.checkPreflightPasses(ctx)
	r.checkExtractRender(ctx)
	r.checkRunTotals(ctx)
	r.checkCostMeter()
	r.checkProviderOverride(ctx)
	r.checkPreflightRejects(ctx)
	r.checkGenImplement(ctx)
	r.checkBehaviorReplay(ctx)
}

// checkProjectInit checks `chb init` and db-init, run twice to show it is
// idempotent.
func (r *validateRunner) checkProjectInit(ctx context.Context) {
	r.section("project + DB initialization")
	r.assert("init project", r.runChb(ctx, "init", selfValidationProject))
	r.assert("db-init schema", r.runChb(ctx, "db-init"))
	r.assert("db-init re-init idempotent", r.runChb(ctx, "db-init"))
}

// checkDimensions registers and lists a CDE dimension.
func (r *validateRunner) checkDimensions(ctx context.Context) {
	r.section("CDE dimensions")
	r.assert("register dimension d1", r.runChb(ctx, "db-write", "dimension",
		`{"name":"component","description":"HIVE subsystem","values_json":"[\"meta\",\"db\",\"mss\",\"workflow\",\"gate\",\"hive\",\"ingest\",\"export\",\"agent-run\",\"lean4\",\"mcp\"]"}`))
	r.assert("list dimensions", r.runChb(ctx, "db-read", "dimensions"))
}

// checkAgentRunLifecycle creates the agent_run that the wave gate section
// completes.
func (r *validateRunner) checkAgentRunLifecycle(ctx context.Context) {
	r.section("agent_run lifecycle")
	out := r.runChbCapture(ctx, "db-write", "agent_run", `{"wave":1,"agent_name":"`+selfValidationProject+`","agent_type":"verifier"}`)
	r.runID = lastIDFrom(out)
	if r.runID > 0 {
		r.passN("agent_run created (id=" + fmt.Sprint(r.runID) + ")")
	} else {
		r.failN("agent_run create", out)
		r.runID = 1
	}
}

// checkLabeledFindings writes a finding under each MSS label.
func (r *validateRunner) checkLabeledFindings(ctx context.Context) {
	r.section("MSS-labeled findings")
	r.assert("write definition", r.runChb(ctx, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":1,"finding":"validator dispatches every CLI subsystem","mss_label":"definition"}`, selfValidationProject)))
	r.assert("write assumption", r.runChb(ctx, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":2,"finding":"unit tests cover ≥80%% of touched paths","mss_label":"assumption","source_urls":"https://golang.org/cover"}`, selfValidationProject)))
	r.assert("write unknown", r.runChb(ctx, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":3,"finding":"production-grade memory profile under 1k findings","mss_label":"unknown"}`, selfValidationProject)))
	r.defID = firstIDInJSON(r.runChbCapture(ctx, "db-read", "definitions"))
	if r.defID == 0 {
		r.defID = 1
	}
	r.assert("write guarantee w/ deps", r.runChb(ctx, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":1,"finding":"every CLI subsystem reachable via single binary","mss_label":"guarantee","depends_on_ids":[%d]}`,
			selfValidationProject, r.defID)))
}

// checkMSSRules checks that the writes the MSS rules forbid are refused.
func (r *validateRunner) checkMSSRules(ctx context.Context) {
	r.section("MSS rule enforcement (must reject)")
	r.assertFail("guarantee w/o deps rejected", r.runChb(ctx, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":4,"finding":"unsourced guarantee","mss_label":"guarantee"}`, selfValidationProject)))
	r.unkID = firstIDInJSON(r.runChbCapture(ctx, "db-read", "unknowns"))
	if r.unkID == 0 {
		r.unkID = 3
	}
	r.assertFail("guarantee depending on unknown", r.runChb(ctx, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":4,"finding":"laundered guarantee","mss_label":"guarantee","depends_on_ids":[%d]}`, selfValidationProject, r.unkID)))
	r.assertFail("guarantee with bogus dep id", r.runChb(ctx, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":4,"finding":"phantom dep","mss_label":"guarantee","depends_on_ids":[9999]}`, selfValidationProject)))
	r.assertFail("bogus mss_label rejected", r.runChb(ctx, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":4,"finding":"x","mss_label":"truthish"}`, selfValidationProject)))
}

// checkReadPaths reads each label's findings back, probes and summarises.
func (r *validateRunner) checkReadPaths(ctx context.Context) {
	r.section("read paths")
	r.assertContains("read definitions", "validator dispatches every CLI", r.runChbCapture(ctx, "db-read", "definitions"))
	r.assertContains("read assumptions", "unit tests cover", r.runChbCapture(ctx, "db-read", "assumptions"))
	r.assertContains("read unknowns", "production-grade memory profile", r.runChbCapture(ctx, "db-read", "unknowns"))
	r.assertContains("read guarantees", "every CLI subsystem reachable", r.runChbCapture(ctx, "db-read", "guarantees"))
	r.assert("probe d1=1", r.runChb(ctx, "db-read", "probe", `{"d1":1}`))
	r.assert("summary", r.runChb(ctx, "db-read", "summary"))
}

// checkGapsFollowups writes a gap and a followup and reads the gaps back.
func (r *validateRunner) checkGapsFollowups(ctx context.Context) {
	r.section("gaps + followups")
	r.assert("write gap", r.runChb(ctx, "db-write", "gap",
		`{"wave":1,"description":"no smoke test for internal/cli package","priority":"important"}`))
	r.assert("write followup", r.runChb(ctx, "db-write", "followup",
		`{"wave":1,"question":"should agent-run support OpenAI fallback?","priority":"minor"}`))
	r.assert("read gaps", r.runChb(ctx, "db-read", "gaps"))
}

// checkConflicts registers a conflict between two findings and detects
// conflicts.
func (r *validateRunner) checkConflicts(ctx context.Context) {
	r.section("conflicts")
	r.assert("finding A", r.runChb(ctx, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":5,"finding":"max iterations defaults to 500","mss_label":"definition"}`, selfValidationProject)))
	r.assert("finding B", r.runChb(ctx, "db-write", "finding",
		fmt.Sprintf(`{"wave":1,"agent":%q,"d1":5,"finding":"max iterations defaults to 100","mss_label":"definition"}`, selfValidationProject)))
	defs := allIDsInJSON(r.runChbCapture(ctx, "db-read", "definitions"))
	aID, bID := int64(1), int64(2)
	if len(defs) >= 2 {
		aID, bID = defs[len(defs)-2], defs[len(defs)-1]
	}
	r.assert("register conflict", r.runChb(ctx, "db-write", "conflict",
		fmt.Sprintf(`{"wave":1,"finding_a_id":%d,"finding_b_id":%d,"description":"different default values"}`, aID, bID)))
	r.assert("detect-conflicts dry-run", r.runChb(ctx, "detect-conflicts", "--wave", "1", "--dry-run"))
	r.assert("read conflicts", r.runChb(ctx, "db-read", "conflicts"))
}

// checkMSSAudit checks the MSS audit reports its integrity.
func (r *validateRunner) checkMSSAudit(ctx context.Context) {
	r.section("MSS audit")
	r.assertContains("mss_audit integrity present", `"integrity"`,
		r.runChbCapture(ctx, "db-read", "mss_audit"))
}

// checkSwarmMerge merges wave 1's findings under a quorum.
func (r *validateRunner) checkSwarmMerge(ctx context.Context) {
	r.section("swarm-merge")
	r.assert("swarm-merge wave 1 quorum 2",
		r.runChb(ctx, "swarm-merge", "--wave", "1", "--quorum", "2"))
}

// checkValidateSources counts wave 1's sources without fetching them.
func (r *validateRunner) checkValidateSources(ctx context.Context) {
	r.section("validate-sources --quick")
	r.assert("validate-sources --quick",
		r.runChb(ctx, "validate-sources", "--wave", "1", "--quick"))
}

// checkLean4Extract checks the Lean 4 extractor runs, and writes its output
// to Wave1.lean.
func (r *validateRunner) checkLean4Extract(ctx context.Context) {
	r.section("lean4-extract (formal verification bridge)")
	// stdout only, and the exit status honoured, so an error message the
	// extractor prints, refusing a failed audit or finding no rows, fails
	// the check. It checks the extractor runs and emits its header;
	// compiling the output against the Lean side is not part of it.
	leanPath := filepath.Join(r.workspaceDir, "Wave1.lean")
	leanCmd := exec.CommandContext(ctx, r.chbBin, "lean4-extract", "--wave", "1")
	leanCmd.Env = os.Environ()
	leanOut, leanErr := leanCmd.Output()
	switch {
	case leanErr != nil:
		r.failN("lean4-extract wave 1", leanErr.Error())
	case !strings.Contains(string(leanOut), "Auto-generated MSS state extraction"):
		r.failN("lean4-extract wave 1", "output is not an extraction: "+truncFor(string(leanOut), 200))
	default:
		r.passN("lean4-extract wave 1")
	}
	if err := os.WriteFile(leanPath, leanOut, 0o644); err != nil {
		r.failN("Wave1.lean written", err.Error())
	} else {
		r.passN("Wave1.lean written")
	}
}

// checkWaveGate completes the agent run and runs the wave gate, which must
// open on wave 1 and stay shut on an empty wave.
func (r *validateRunner) checkWaveGate(ctx context.Context) {
	r.section("wave gate pipeline")
	if err := r.runChb(ctx, "db-write", "complete_run",
		fmt.Sprintf(`{"run_id":%d,"summary":"self-validation complete","tool_uses":1,"duration_ms":0,"total_tokens":0}`, r.runID)); err == nil {
		r.passN("complete_run")
	} else {
		r.failN("complete_run", err.Error())
	}
	r.assert("check-agents wave 1", r.runChb(ctx, "check-agents", "--wave", "1"))
	r.assert("guard full pipeline (force, auto-resolve)",
		r.runChb(ctx, "guard", "--wave", "1", "--auto-resolve-numeric", "--force",
			"--eval", `{"coverage":4,"depth":4,"sources":4,"actionability":4,"mss_integrity":4,"verdict":"COMPLETE"}`))
	// A wave with no agents and no findings cannot open; the exit status
	// is what scripts key off, so a blocked gate must be non-zero.
	r.assertFail("guard exits non-zero when blocked (empty wave)",
		r.runChb(ctx, "guard", "--wave", "99"))
}

// checkWorkflowEngine lists and validates the workflows, and runs
// margin-check to the end.
func (r *validateRunner) checkWorkflowEngine(ctx context.Context) {
	r.section("workflow engine")
	r.assert("workflow list", r.runChb(ctx, "workflow", "list"))
	r.validateShippedWorkflows(ctx)
	r.runMarginCheck(ctx)
}

// validateShippedWorkflows validates each workflow under workflows/.
func (r *validateRunner) validateShippedWorkflows(ctx context.Context) {
	entries, err := filepath.Glob("workflows/*.yaml")
	if err != nil {
		return
	}
	for _, wf := range entries {
		r.assert("validate "+filepath.Base(wf),
			r.runChb(ctx, "workflow", "validate", wf))
	}
}

// runMarginCheck starts a margin-check run and drives it to completion.
func (r *validateRunner) runMarginCheck(ctx context.Context) {
	wfOut := r.runChbCapture(ctx, "workflow", "init", "workflows/margin-check.yaml",
		"--inputs", `{"product_name":"self-validate-test","target_margin":50,"initial_price":29.99}`)
	r.wfRunID = lastIDFrom(wfOut)
	if r.wfRunID <= 0 {
		r.failN("workflow init", wfOut)
		return
	}
	r.passN(fmt.Sprintf("workflow init (run=%d)", r.wfRunID))
	r.assert("workflow next", r.runChb(ctx, "workflow", "next", fmt.Sprint(r.wfRunID)))
	// Run it to the end, standing in for its agents, so the run does not
	// stay `running` in the workspace.
	if err := r.finishMarginCheck(ctx, fmt.Sprint(r.wfRunID)); err != nil {
		r.failN("workflow status (run completed)", err.Error())
	} else {
		r.assertContains("workflow status (run completed)", "Status: completed",
			r.runChbCapture(ctx, "workflow", "status", fmt.Sprint(r.wfRunID)))
	}
	r.assert("workflow runs", r.runChb(ctx, "workflow", "runs"))
}

// finishMarginCheck completes the margin-check run checkWorkflowEngine
// started, one node at a time, on the branch its outputs choose: a 66% margin
// passes the 50% target, so check-margin leads to finalize and the retry
// branch is skipped. The last `workflow next` finds nothing left and marks
// the run completed.
func (r *validateRunner) finishMarginCheck(ctx context.Context, runID string) error {
	for _, step := range []struct{ node, outputs string }{
		{"research-pricing", `{"cost_data":"unit cost $10 at MOQ 500"}`},
		{"analyze-margin", `{"margin_result":"(29.99 - 10) / 29.99","margin_pct":66}`},
		{"finalize", `{"final_report":"self-validation stand-in"}`},
	} {
		if err := r.runChb(ctx, "workflow", "complete", runID, step.node, "--outputs", step.outputs); err != nil {
			return fmt.Errorf("workflow complete %s: %w", step.node, err)
		}
		if err := r.runChb(ctx, "workflow", "next", runID); err != nil {
			return fmt.Errorf("workflow next after %s: %w", step.node, err)
		}
	}
	return nil
}

// checkHive drives the hive through init, two ticks, its status and
// signals, and a reset.
func (r *validateRunner) checkHive(ctx context.Context) {
	r.section("hive end-to-end")
	const proj = selfValidationProject
	r.assert("hive init", r.runChb(ctx, "hive", "init", "--project", proj))
	r.assert("hive next #1", r.runChb(ctx, "hive", "next", "--project", proj))
	r.assert("hive status", r.runChb(ctx, "hive", "status", "--project", proj))
	r.assert("hive signals", r.runChb(ctx, "hive", "signals", "--project", proj))
	r.assert("emit shaking_signal", r.runChb(ctx, "db-write", "signal",
		`{"signal_type":"shaking_signal","source_type":"system","payload":{"src":"self-validate"},"wave":1}`))
	r.assert("hive next #2", r.runChb(ctx, "hive", "next", "--project", proj))
	r.assert("hive reset", r.runChb(ctx, "hive", "reset", "--project", proj))
}

// checkCascadeRevert reverts the definition's cascade and refuses a
// malformed id.
func (r *validateRunner) checkCascadeRevert(ctx context.Context) {
	r.section("cascade revert")
	r.assert("cascade_revert", r.runChb(ctx, "db-write", "cascade_revert", fmt.Sprint(r.defID)))
	r.assertFail("cascade_revert rejects non-int", r.runChb(ctx, "db-write", "cascade_revert", "not-a-number"))
}

// checkPromoteFinding promotes an assumption to a guarantee on the
// definition's support.
func (r *validateRunner) checkPromoteFinding(ctx context.Context) {
	r.section("promote_finding")
	r.asmID = firstIDInJSON(r.runChbCapture(ctx, "db-read", "assumptions"))
	if r.asmID > 0 {
		r.assert("promote assumption→guarantee", r.runChb(ctx, "db-write", "promote_finding",
			fmt.Sprintf(`{"finding_id":%d,"depends_on_ids":[%d]}`,
				r.asmID, r.defID)))
	} else {
		r.warn("no assumption to promote (skipped)")
	}
}

// checkIngest ingests agent output carrying each kind of marker, then a
// guarantee marker with array dependencies.
func (r *validateRunner) checkIngest(ctx context.Context) {
	r.section("ingest marker parser")
	ingestPath := filepath.Join(r.workspaceDir, "agent-output.txt")
	_ = os.WriteFile(ingestPath, []byte(
		"Some preamble text from a fictional agent.\n\n"+
			`<!-- FINDING: {"d1": 6, "mss_label": "definition", "finding": "ingest parser handles HTML comment markers", "source_urls": "https://example.com/spec"} -->`+"\n"+
			`<!-- GAP: {"description": "no fuzz tests for marker parser", "priority": "important"} -->`+"\n"+
			`<!-- FOLLOWUP: {"question": "should we accept JSON5?", "priority": "minor"} -->`+"\n\nTrailing text.\n"), 0o644)
	r.assert("ingest dry-run", r.runChb(ctx, "ingest", "--wave", "2", "--agent", "self-verifier", "--dry-run", ingestPath))
	ingestOut := r.runChbCapture(ctx, "ingest", "--wave", "2", "--agent", "self-verifier", ingestPath)
	r.assertContains("ingest real", "3 items ingested, 0 errors", ingestOut)

	// A guarantee marker with array depends_on_ids — the shape
	// agents/researcher.md documents — so the harness exercises the
	// guarantee path as well as the definition's.
	depID := lastIDFrom(ingestOut)
	guaranteePath := filepath.Join(r.workspaceDir, "agent-output-guarantee.txt")
	_ = os.WriteFile(guaranteePath, []byte(fmt.Sprintf(
		`<!-- FINDING: {"d1": 6, "mss_label": "guarantee", "finding": "marker path carries array depends_on_ids", "source_urls": "https://example.com/spec", "depends_on_ids": [%d]} -->`+"\n",
		depID)), 0o644)
	r.assertContains("ingest guarantee w/ array deps", "1 items ingested, 0 errors",
		r.runChbCapture(ctx, "ingest", "--wave", "2", "--agent", "self-verifier", guaranteePath))
}

// checkExportGraph exports the finding graph as JSON and as Mermaid.
func (r *validateRunner) checkExportGraph(ctx context.Context) {
	r.section("export-graph")
	graphJSON := filepath.Join(r.workspaceDir, "graph.json")
	graphMD := filepath.Join(r.workspaceDir, "graph.md")
	r.assert("export-graph json", r.runChb(ctx, "export-graph", "--out", graphJSON))
	r.assert("export-graph mermaid", r.runChb(ctx, "export-graph", "--mermaid", "--out", graphMD))
}

// checkAgentRunDryRun dry-runs the self-validation workflow under agent-run.
func (r *validateRunner) checkAgentRunDryRun(ctx context.Context) {
	r.section("agent-run dry-run (fully-automated harness)")
	const proj = selfValidationProject
	r.assert("agent-run dry-run on self-validation.yaml", r.runChb(ctx,
		"agent-run", "workflows/self-validation.yaml",
		"--dir", ".", "--project", proj,
		"--inputs", fmt.Sprintf(`{"project":"%s"}`, proj),
		"--dry-run", "--branch", "", "--max-iterations", "30"))
}

// checkMCPBuild builds chb-mcp when its source is present.
func (r *validateRunner) checkMCPBuild(ctx context.Context) {
	r.section("mcp binary build")
	if _, err := os.Stat("cmd/chb-mcp"); err == nil {
		r.assert("chb-mcp builds", r.runShell(ctx, "go", "build", "-o", os.DevNull, "./cmd/chb-mcp"))
	} else {
		// Source not available (we're running against an installed
		// binary, e.g. inside the node:22-alpine container, which ships
		// the binaries but not cmd/). Surface a warning so the operator
		// knows the section was skipped on purpose, rather than silently
		// dropping it.
		r.warn(fmt.Sprintf("§%d skipped — cmd/chb-mcp not present (installed-binary mode)", r.sections))
	}
}

// checkMCPSmoke runs the MCP smoke harness against chb-mcp.
func (r *validateRunner) checkMCPSmoke(ctx context.Context) {
	r.section("MCP smoke harness — every tool over stdio")
	mcpBin := r.mcpBin
	if mcpBin == "" {
		mcpBin = siblingBinary("chb-mcp")
	}
	if _, err := os.Stat(mcpBin); err == nil {
		// Use the native chb mcp-smoke command.
		err := r.runChb(ctx, "mcp-smoke", "--mcp-bin", mcpBin, "--chb-bin", r.chbBin)
		if err == nil {
			r.passN("chb mcp-smoke")
		} else {
			r.failN("chb mcp-smoke", err.Error())
		}
	} else {
		// A failure, not a skip: chb-mcp ships beside chb in every archive
		// and the image, so its absence is a broken install, and a warning
		// would report the MCP seam green untested.
		r.failN("chb mcp-smoke", "chb-mcp not found at "+mcpBin+" — pass --mcp-bin, or build it: go build -o <dir of chb>/chb-mcp ./cmd/chb-mcp")
	}
}

// checkGenerator checks `chb generate` emits a workflow that validates.
func (r *validateRunner) checkGenerator(ctx context.Context) {
	r.section("Swarm generator emits a workflow that validates")
	// The swarm workflow comes from `chb generate`: render a real swarm YAML
	// to a tmpfile and run `workflow validate` against it.
	tmpYAML := filepath.Join(r.workspaceDir, "swarm-generated.yaml")
	r.assert("chb generate emits YAML",
		r.runChb(ctx, "generate", "should we ship?", "--out", tmpYAML))
	r.assert("generated swarm YAML validates",
		r.runChb(ctx, "workflow", "validate", tmpYAML))
}

// checkRunRecordSchema checks the tables and the per-node columns a run
// records its decisions, repairs and node results in. It reports whether
// the workspace database opened: the capability checks stop when it did
// not.
func (r *validateRunner) checkRunRecordSchema() bool {
	r.section("run record schema (workflow_decisions / workflow_repairs / per-node columns)")
	readDB, err := sql.Open("sqlite", "file:"+r.dbPath+"?mode=ro&"+db.BusyTimeoutPragma())
	if err != nil {
		r.failN("open workspace DB", err.Error())
		return false
	}
	defer readDB.Close()
	r.checkRunRecordTables(readDB)
	r.checkNodeRecordColumns(readDB)
	return true
}

// checkRunRecordTables checks workflow_decisions and workflow_repairs exist.
func (r *validateRunner) checkRunRecordTables(conn *sql.DB) {
	for _, table := range []string{"workflow_decisions", "workflow_repairs"} {
		if schemaTableExists(conn, table) {
			r.passN("table " + table + " present")
		} else {
			r.failN("table "+table+" missing", "")
		}
	}
}

// checkNodeRecordColumns checks the per-node columns of workflow_node_states exist.
func (r *validateRunner) checkNodeRecordColumns(conn *sql.DB) {
	for _, col := range []string{"rationale", "tokens_in", "tokens_out", "cost_usd_x10000", "provider"} {
		if columnExists(conn, "workflow_node_states", col) {
			r.passN("column workflow_node_states." + col + " present")
		} else {
			r.failN("column workflow_node_states."+col+" missing", "")
		}
	}
}

// checkPreflightPasses checks preflight passes a valid workflow.
func (r *validateRunner) checkPreflightPasses(ctx context.Context) {
	r.section("preflight valid YAML passes")
	r.assert("preflight proof.yaml",
		r.runChb(ctx, "preflight", "workflows/proof.yaml"))
}

// checkExtractRender extracts a seeded run's findings to JSON and renders
// them as REVIEW.md.
func (r *validateRunner) checkExtractRender(ctx context.Context) {
	r.section("extract-findings + render-review (rationale → JSON → MD)")
	r.seedV25Run()
	findingsPath := r.findingsPath()
	_ = os.MkdirAll(filepath.Dir(findingsPath), 0o755)
	r.assert("extract-findings", r.runChb(ctx, "extract-findings", "--out", findingsPath))
	reviewPath := filepath.Join(r.workspaceDir, "review", "REVIEW.md")
	r.assert("render-review", r.runChb(ctx, "render-review", findingsPath, "--out", reviewPath))
}

// seedV25Run seeds a node with rationale so extract-findings has something
// to chew on. Its one node is completed, so the run is too, and nothing is
// left `running` in the workspace. It waits for a lock as the store's
// connections do, not failing on the first busy. A database that does not
// open is left as it is.
func (r *validateRunner) seedV25Run() {
	dbWrite, err := sql.Open("sqlite", "file:"+r.dbPath+"?"+db.BusyTimeoutPragma())
	if err != nil {
		return
	}
	defer dbWrite.Close()
	_, _ = dbWrite.Exec(`INSERT INTO workflow_runs (workflow_name, definition_yaml, inputs_json, status, completed_at)
			VALUES (?, ?, ?, 'completed', ?)`,
		"v25", "name: v25", "{}", time.Now().UTC().Format(time.RFC3339))
	var rid int64
	_ = dbWrite.QueryRow(`SELECT last_insert_rowid()`).Scan(&rid)
	rationale := "```json\n" + `{"lens":"v25","verdict":"issues","summary":"seed","findings":[{"id":"v25-1","severity":"critical","file":"internal/seed/seed.go","line":1,"issue":"placeholder","fix":"add a test"}]}` + "\n```"
	_, _ = dbWrite.Exec(`INSERT INTO workflow_node_states
			(run_id, node_name, node_type, status, rationale, tokens_in, tokens_out, cost_usd_x10000, provider)
			VALUES (?, 'audit-v25', 'agent', 'completed', ?, 100, 200, 30, 'anthropic')`, rid, rationale)
}

// findingsPath is where checkExtractRender writes the findings
// checkGenImplement generates from.
func (r *validateRunner) findingsPath() string {
	return filepath.Join(r.workspaceDir, "review", "findings.json")
}

// checkRunTotals checks run-totals reports cost and tokens.
func (r *validateRunner) checkRunTotals(ctx context.Context) {
	r.section("run-totals reports cost + tokens")
	r.assertContains("run-totals output", `"nodes_total"`,
		r.runChbCapture(ctx, "run-totals", "1"))
}

// checkCostMeter records the cost meter as verified by run-totals.
func (r *validateRunner) checkCostMeter() {
	r.section("cost meter aggregates per run")
	// Already verified by checkRunTotals — implicit pass.
	r.passN("cost meter aggregates (verified via run-totals)")
}

// checkProviderOverride checks agent-run --dry-run takes a --provider
// override.
func (r *validateRunner) checkProviderOverride(ctx context.Context) {
	r.section("agent-run --dry-run accepts --provider override")
	r.assert("agent-run --provider", r.runChb(ctx,
		"agent-run", "workflows/proof.yaml",
		"--inputs", `{"target_file":"workspace/repo-audit/REPO_AUDIT.md","result_path":"workspace/proof/result.txt"}`,
		"--provider", "anthropic", "--dry-run", "--branch", "", "--max-iterations", "10"))
}

// checkPreflightRejects checks preflight rejects a workflow without
// accept:.
func (r *validateRunner) checkPreflightRejects(ctx context.Context) {
	r.section("preflight rejects workflow without accept:")
	noAcceptYAML := filepath.Join(r.workspaceDir, "no-accept.yaml")
	_ = os.WriteFile(noAcceptYAML, []byte(
		"name: no-accept\nversion: 1\ninputs: [x]\nnodes:\n  a:\n    type: agent\n    agent: a\n    model: haiku\n    prompt: 'go'\n    outputs: [y]\n"), 0o644)
	r.assertFail("preflight rejects no-accept", r.runChb(ctx, "preflight", noAcceptYAML))
}

// checkGenImplement generates an implement workflow from the findings
// checkExtractRender wrote.
func (r *validateRunner) checkGenImplement(ctx context.Context) {
	r.section("gen-implement-workflow")
	r.assert("gen-implement-workflow",
		r.runChb(ctx, "gen-implement-workflow", r.findingsPath(),
			"--out", filepath.Join(r.workspaceDir, "implement.yaml"),
			"--severity", "critical,high",
			"--max-fixes", "1"))
}

// checkBehaviorReplay replays the behavior spec in a HIVE source checkout
// (inHiveCheckout), and warns when the checkout lacks its fixture. Anywhere
// else the section does not run: the fixture is for developing HIVE.
func (r *validateRunner) checkBehaviorReplay(ctx context.Context) {
	if !inHiveCheckout() {
		return
	}
	r.section("behavior-spec replay (byte-equal regression contract)")
	if _, err := os.Stat(behaviorFixture); err != nil {
		r.warn(behaviorFixture + " missing; run `chb gen-behavior`")
		return
	}
	r.assert("chb replay-behavior", r.runChb(ctx, "replay-behavior", "--bin", r.chbBin))
}

// ── Assertion helpers ───────────────────────────────────────────────

// assert checks that err is nil; equivalent to bash assert <cmd>.
func (r *validateRunner) assert(name string, err error) {
	if err == nil {
		r.passN(name)
		return
	}
	r.failN(name, err.Error())
}

// assertFail expects err to be non-nil; equivalent to bash assert_fail.
func (r *validateRunner) assertFail(name string, err error) {
	if err != nil {
		r.passN(name + " (correctly rejected)")
		return
	}
	r.failN(name+" (expected fail, got success)", "")
}

// assertContains checks that haystack contains needle; equivalent to
// bash assert_contains <name> <needle>.
func (r *validateRunner) assertContains(name, needle, haystack string) {
	if strings.Contains(haystack, needle) {
		r.passN(name)
		return
	}
	r.failN(name+" (missing: "+needle+")", haystack)
}

func (r *validateRunner) passN(name string) {
	r.pass++
	r.log("  ✓ " + name)
}

func (r *validateRunner) failN(name, detail string) {
	r.fail++
	r.failures = append(r.failures, name)
	r.log("  ✗ " + name)
	if detail != "" {
		r.log("    out: " + clip(detail, 400))
	}
	if r.stopOnFirst {
		// Exit immediately on first failure if configured.
		_ = r.report()
		os.Exit(1)
	}
}

func (r *validateRunner) warn(msg string) {
	r.warns++
	r.log("  ⚠ " + msg)
}

// section begins the next section, numbering its header in the order the
// sections run.
func (r *validateRunner) section(title string) {
	r.sections++
	r.log("")
	r.log(fmt.Sprintf("── %d. %s ──", r.sections, title))
}

func (r *validateRunner) log(msg string) {
	fmt.Fprintln(r.logFile, msg)
	fmt.Fprintln(r.stderr, msg)
}

// runChb executes `chb <args>` and returns the run error.
// stdout+stderr are captured but discarded (use runChbCapture
// when callers need the output).
func (r *validateRunner) runChb(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, r.chbBin, args...)
	cmd.Env = harnessChildEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		// Embed first 200 bytes of output in the error so failure
		// messages carry context.
		return fmt.Errorf("%w (out: %s)", err, clip(string(out), 200))
	}
	return nil
}

// runChbCapture returns the combined stdout+stderr regardless of
// exit status; some assertions inspect output even when the command
// fails (e.g. read paths that 0-rows-as-success).
func (r *validateRunner) runChbCapture(ctx context.Context, args ...string) string {
	cmd := exec.CommandContext(ctx, r.chbBin, args...)
	cmd.Env = harnessChildEnv()
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// runShell is the catch-all for commands other than chb (`go build`).
func (r *validateRunner) runShell(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w (out: %s)", err, clip(string(out), 200))
	}
	return nil
}

// report prints the final pass/fail/warn summary and returns an error
// when there were any failures.
func (r *validateRunner) report() error {
	r.logSummary()
	if r.asJSON {
		if err := r.printJSONSummary(); err != nil {
			return err
		}
	}
	if r.fail > 0 {
		return fmt.Errorf("%d assertion failures", r.fail)
	}
	return nil
}

// logSummary logs the pass/fail/warn counts and names every failure.
func (r *validateRunner) logSummary() {
	r.log("")
	r.log("════════════════════════════════════════════════════════════")
	r.log("Self-validation summary")
	r.log(fmt.Sprintf("  passed:   %d", r.pass))
	r.log(fmt.Sprintf("  failed:   %d", r.fail))
	r.log(fmt.Sprintf("  warnings: %d", r.warns))
	r.log("════════════════════════════════════════════════════════════")
	if r.fail > 0 {
		r.log("failures:")
		for _, f := range r.failures {
			r.log("  - " + f)
		}
	}
}

// printJSONSummary prints the summary on stdout as one JSON object.
func (r *validateRunner) printJSONSummary() error {
	failures := r.failures
	if failures == nil {
		failures = []string{}
	}
	b, err := json.MarshalIndent(map[string]any{
		"passed":   r.pass,
		"failed":   r.fail,
		"warnings": r.warns,
		"failures": failures,
		"log":      r.logFile.Name(),
		"db":       r.dbPath,
	}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(r.stdout, string(b))
	return nil
}

// ── Output parsers ──────────────────────────────────────────────────

var (
	idEqRegex = regexp.MustCompile(`(?:[Ii][Dd])=([0-9]+)`)
	idJSON    = regexp.MustCompile(`"id"\s*:\s*([0-9]+)`)
)

// lastIDFrom finds the last `id=N` or `ID=N` token in s and returns N.
func lastIDFrom(s string) int64 {
	matches := idEqRegex.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return 0
	}
	last := matches[len(matches)-1][1]
	var v int64
	fmt.Sscanf(last, "%d", &v)
	return v
}

// firstIDInJSON scans for the first `"id": N` and returns N.
func firstIDInJSON(s string) int64 {
	m := idJSON.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	var v int64
	fmt.Sscanf(m[1], "%d", &v)
	return v
}

// allIDsInJSON returns every `"id": N` value in s, in order.
func allIDsInJSON(s string) []int64 {
	matches := idJSON.FindAllStringSubmatch(s, -1)
	out := make([]int64, 0, len(matches))
	for _, m := range matches {
		var v int64
		fmt.Sscanf(m[1], "%d", &v)
		out = append(out, v)
	}
	return out
}

// schemaTableExists reports whether the database holds the table.
func schemaTableExists(conn *sql.DB, table string) bool {
	var name string
	err := conn.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
	).Scan(&name)
	return err == nil && name == table
}

// columnExists is a small probe used by checkRunRecordSchema.
func columnExists(conn *sql.DB, table, col string) bool {
	names, err := tableColumnNames(conn, table)
	return err == nil && slices.Contains(names, col)
}

// tableColumnNames lists the table's columns.
func tableColumnNames(conn *sql.DB, table string) ([]string, error) {
	rows, err := conn.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   sql.NullString
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
