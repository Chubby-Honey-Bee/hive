package runner

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
)

// finishRun is what a run whose dispatch loop ended without an error does
// last: it writes the artifact (--artifact), opens the PR (--auto-pr) and
// logs the run's totals.
func (rc *runtimeContext) finishRun() {
	rc.writeArtifact()
	maybeOpenPR(rc.cfg, rc.gc, rc.res, rc.logf)
	rc.logRunDone()
}

// writeArtifact writes the canonical artifact when --artifact <path> is
// set. Best-effort: a write failure is logged but never fails the run.
func (rc *runtimeContext) writeArtifact() {
	if rc.cfg.ArtifactPath == "" {
		return
	}
	art, err := artifact.BuildArtifact(rc.store, rc.runID, runDeterminism(rc.cfg))
	if err != nil {
		rc.logf("artifact build failed: %v", err)
		return
	}
	if err := art.WriteTo(rc.cfg.ArtifactPath); err != nil {
		rc.logf("artifact write failed: %v", err)
		return
	}
	rc.logf("[swarm] artifact written: %s sha256=%s", rc.cfg.ArtifactPath, art.Hash)
}

// runDeterminism is the determinism settings the artifact records: the
// run's sampling, --deterministic as temperature 0, and its seed.
func runDeterminism(cfg Config) artifact.Determinism {
	det := artifact.Determinism{
		Enabled:     cfg.Deterministic,
		Temperature: cfg.Temperature,
		TopP:        cfg.TopP,
		Seed:        cfg.Seed,
	}
	if cfg.Deterministic {
		t := 0.0
		det.Temperature = &t
	}
	return det
}

// logRunDone logs the run's totals. The credit total only appears when
// the user has opted into Copilot billing.
func (rc *runtimeContext) logRunDone() {
	res := rc.res
	cost := formatCost(res.CostUSDx10000, res.MeteredCalls, res.UnmeteredCalls)
	if isCopilotBilling() {
		rc.logf("done. iterations=%d nodes=%d commits=%d tokens=%d/%d cost=%s credits=%.2f",
			res.Iterations, res.NodesRun, len(res.Commits),
			res.InputTokens, res.OutputTokens,
			cost,
			float64(res.CopilotCreditsX1000)/1000.0,
		)
		return
	}
	rc.logf("done. iterations=%d nodes=%d commits=%d tokens=%d/%d cost=%s",
		res.Iterations, res.NodesRun, len(res.Commits),
		res.InputTokens, res.OutputTokens,
		cost,
	)
}

// maybeOpenPR pushes the auto-commit branch and opens a PR. No-op if
// AutoPR is disabled, git is disabled (a dry run has no committer at all),
// or no commits were made.
func maybeOpenPR(cfg Config, gc *GitCommitter, res *Result, logf func(string, ...any)) {
	if !autoPRWanted(cfg, gc, res) {
		return
	}
	logf("pushing branch and opening PR")
	if err := gc.Push(); err != nil {
		logf("push: %v", err)
		return
	}
	prURL, err := OpenPR(cfg.ProjectDir, cfg.Branch, autoPRTitle(cfg), autoPRBody(cfg, res))
	if err != nil {
		logf("open PR: %v", err)
		return
	}
	res.PRURL = prURL
	logf("PR opened: %s", prURL)
}

// autoPRWanted reports whether the run opens a PR: AutoPR is set, the
// committer is enabled, and the run made commits.
func autoPRWanted(cfg Config, gc *GitCommitter, res *Result) bool {
	return cfg.AutoPR && gc != nil && gc.Enabled && len(res.Commits) > 0
}

// autoPRTitle is the PR's title: --pr-title, else one naming the project.
func autoPRTitle(cfg Config) string {
	if cfg.PRTitle != "" {
		return cfg.PRTitle
	}
	return fmt.Sprintf("validate: automated corrections from %s", cfg.ProjectName)
}

// autoPRBody is the PR's body: --pr-body, else the run's id, commits and
// tokens.
func autoPRBody(cfg Config, res *Result) string {
	if cfg.PRBody != "" {
		return cfg.PRBody
	}
	return fmt.Sprintf(
		"Automated validation run for **%s**.\n\nWorkflow run: #%d  \nCommits: %d  \nTokens: %d in / %d out\n\nOpened by `chb agent-run`.\n",
		cfg.ProjectName, res.RunID, len(res.Commits), res.InputTokens, res.OutputTokens,
	)
}

// PrintRunSummary writes a compact one-screen run summary. Callers use it
// to emit a machine-parseable trailer to stdout on exit.
func PrintRunSummary(w io.Writer, res *Result) {
	b, _ := json.MarshalIndent(RunSummary(res), "", "  ")
	fmt.Fprintln(w, string(b))
}

// RunSummary is the object PrintRunSummary prints: the run's id, counts
// and tokens. chb ask --json carries it under "run".
func RunSummary(res *Result) map[string]any {
	return map[string]any{
		"run_id":        res.RunID,
		"iterations":    res.Iterations,
		"nodes_run":     res.NodesRun,
		"commits":       res.Commits,
		"pr_url":        res.PRURL,
		"input_tokens":  res.InputTokens,
		"output_tokens": res.OutputTokens,
	}
}
