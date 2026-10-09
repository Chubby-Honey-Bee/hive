package harness

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/bench"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

// The harness's report: REPORT.md for the reader and report.json beside it.

// writeReport writes REPORT.md and report.json into the workspace: the
// run's inputs, the adherence rate, each case's result, and each run case's
// verdicts, nodes, bench or design report, checks and logs.
func (h *agentHarness) writeReport(results []harnessResult, suiteHash string) error {
	info := h.reportRunInfo()
	var md strings.Builder
	h.writeReportHeader(&md, info, suiteHash)
	writeHarnessPreamble(&md, results)
	writeHarnessCaseTable(&md, results)
	for _, r := range results {
		writeHarnessCaseSection(&md, r)
	}
	if err := os.WriteFile(filepath.Join(h.Workspace, "REPORT.md"), []byte(md.String()), 0o644); err != nil {
		return err
	}
	return h.writeReportJSON(results, suiteHash, info)
}

// harnessRunInfo is what the report says of the run beyond the flags: the
// chb build and the checkout's revision, the endpoint, and the lens and
// queen models and reasoning levels in effect.
type harnessRunInfo struct {
	version, revision, baseURL string
	lensModel, queenModel      string
	lensLevel, queenLevel      string
}

// reportRunInfo reads the run's build, revision and endpoint, and the
// models and reasoning in effect: under a profile its routes replace the
// flags it refuses beside it.
func (h *agentHarness) reportRunInfo() harnessRunInfo {
	ver, _ := exec.Command(h.Self, "--version").Output()
	rev, _ := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	info := harnessRunInfo{version: strings.TrimSpace(string(ver)), revision: strings.TrimSpace(string(rev)), baseURL: h.reportBaseURL(),
		lensModel: h.LensModel, queenModel: h.QueenModel, lensLevel: h.LensReasoning, queenLevel: h.QueenReasoning}
	if h.Profile != "" {
		info.lensModel, info.queenModel = h.profileLens.Model, h.profileQueen.Model
		info.lensLevel, info.queenLevel = h.profileLens.Reasoning, h.profileQueen.Reasoning
	}
	return info
}

// reportBaseURL is the endpoint the provider sends to: OPENAI_BASE_URL for
// openai, the local provider's for local, else "".
func (h *agentHarness) reportBaseURL() string {
	switch h.Provider {
	case string(runner.BackendOpenAI):
		return os.Getenv("OPENAI_BASE_URL")
	case string(runner.BackendLocal):
		return runner.LocalBaseURL()
	}
	return ""
}

// writeReportHeader writes the report's title and the run's inputs.
func (h *agentHarness) writeReportHeader(md *strings.Builder, info harnessRunInfo, suiteHash string) {
	fmt.Fprintf(md, "# Agent harness report\n\n")
	fmt.Fprintf(md, "- generated: %s\n- chb: %s\n", time.Now().UTC().Format(time.RFC3339), info.version)
	if h.onClaudeCLI() {
		cver, _ := exec.Command("claude", "--version").Output()
		fmt.Fprintf(md, "- claude CLI: %s\n", strings.TrimSpace(string(cver)))
	}
	fmt.Fprintf(md, "- git: %s\n- suite: `%s` (sha256 %s)\n- provider: `%s`", info.revision, h.Suite, suiteHash, h.Provider)
	if info.baseURL != "" {
		fmt.Fprintf(md, " at `%s`", info.baseURL)
	}
	if h.Profile != "" {
		fmt.Fprintf(md, "; routing profile `%s` (`chb preflight --profile %s` prints every role)", h.Profile, h.Profile)
	}
	fmt.Fprintf(md, "; swarm tier `--budget-mode %s`; lens model %s; queen model %s; lens reasoning %s; queen reasoning %s; persona profile %s; seed %s; bench reps %d; template model `%s`\n- config: `%s`\n\n",
		h.BudgetMode, harnessReportModel(info.lensModel), harnessReportModel(info.queenModel), harnessReportLevel(info.lensLevel), harnessReportLevel(info.queenLevel),
		personaLabel(h.PersonaProfile, h.PersonaSections), h.reportSeed(), h.Reps, h.Model, h.configName())
}

// harnessReportModel names a pinned model, or the tier system when none is.
func harnessReportModel(m string) string {
	if m == "" {
		return "tier system"
	}
	return "`" + m + "`"
}

// harnessReportLevel names a reasoning level, or the server's default when
// none is sent.
func harnessReportLevel(r string) string {
	if r == "" {
		return "the server's default"
	}
	return "`" + r + "`"
}

// reportSeed is the sampling seed, or none.
func (h *agentHarness) reportSeed() string {
	if !h.SeedSet {
		return "none"
	}
	return strconv.FormatInt(h.Seed, 10)
}

// writeHarnessPreamble says what the report's checks mean, and gives the
// run's adherence rate.
func writeHarnessPreamble(md *strings.Builder, results []harnessResult) {
	md.WriteString("What is deterministic here is the contract, not the prose: the same suite, preset, provider and tier are run every time, and each case passes only if every declared contract holds. The artifact sha256 is recorded so two runs can be compared, but model text is expected to differ between runs; a changed hash is a signal to read the verdict table, not a failure.\n\n")
	md.WriteString("A **contract** check fails the case — it is deterministic given correct code. An **adherence** check (⚠) reports instead: whether a model at this tier honours a style rule varies between runs of a correct system, so the rate below is the signal, not any single run. `--strict` makes adherence fail the case too.\n\n")
	clean, total := adherenceRate(results)
	fmt.Fprintf(md, "**Adherence this run: %d/%d checks clean.**\n\n", clean, total)
}

// writeHarnessCaseTable writes one line per case: its kind, result,
// duration, cost and artifact hash.
func writeHarnessCaseTable(md *strings.Builder, results []harnessResult) {
	md.WriteString("| Case | Kind | Result | Duration | Cost | Artifact sha256 |\n|---|---|---|---|---|---|\n")
	for _, r := range results {
		fmt.Fprintf(md, "| %s | %s | %s | %s | %s | %s |\n", r.Name, r.Kind, r.status(), r.Duration, r.Cost, short(r.Hash))
	}
}

// status is the case's result in the report: skipped, FAIL or PASS.
func (r harnessResult) status() string {
	switch {
	case r.Skipped:
		return "skipped"
	case !r.OK:
		return "**FAIL**"
	}
	return "PASS"
}

// writeHarnessCaseSection writes a case's section, unless it was skipped:
// its verdicts and models, its nodes, its bench or design report, its
// checks and where its logs are.
func writeHarnessCaseSection(md *strings.Builder, r harnessResult) {
	if r.Skipped {
		return
	}
	fmt.Fprintf(md, "\n## %s\n\n", r.Name)
	writeHarnessVerdicts(md, r)
	writeHarnessNodes(md, r.Nodes)
	if r.Bench != nil {
		writeBenchReport(md, r.Bench)
	}
	if r.Design != nil {
		writeDesignReport(md, r.Design)
	}
	writeHarnessChecks(md, r.Checks)
	fmt.Fprintf(md, "\nlogs: `%s`\n", r.Log)
}

// writeHarnessVerdicts writes each forager's verdict and model, by name.
func writeHarnessVerdicts(md *strings.Builder, r harnessResult) {
	if len(r.Models) == 0 {
		return
	}
	md.WriteString("| Forager | Verdict | Model |\n|---|---|---|\n")
	for _, n := range slices.Sorted(maps.Keys(r.Models)) {
		fmt.Fprintf(md, "| %s | %s | %s |\n", n, r.Verdicts[n], r.Models[n])
	}
	md.WriteString("\n")
}

// writeHarnessNodes writes each node's model, status, tokens and wall time.
func writeHarnessNodes(md *strings.Builder, nodes []bench.NodeStat) {
	if len(nodes) == 0 {
		return
	}
	md.WriteString("| Node | Model | Status | Tokens in | Tokens out | Wall s |\n|---|---|---|---|---|---|\n")
	for _, n := range nodes {
		fmt.Fprintf(md, "| %s | %s | %s | %d | %d | %.0f |\n", n.Node, n.Model, n.Status, n.TokensIn, n.TokensOut, n.WallSeconds)
	}
	md.WriteString("\n")
}

// writeHarnessChecks writes one line per check, with its sign and detail.
func writeHarnessChecks(md *strings.Builder, checks []harnessCheck) {
	for _, ck := range checks {
		fmt.Fprintf(md, "- %s %s%s\n", ck.mark(), ck.Name, detailSuffix(ck.Detail))
	}
}

// writeReportJSON writes report.json: the run's inputs and every result.
func (h *agentHarness) writeReportJSON(results []harnessResult, suiteHash string, info harnessRunInfo) error {
	js, _ := json.MarshalIndent(map[string]any{
		"generated":    time.Now().UTC().Format(time.RFC3339),
		"suite":        h.Suite,
		"suite_sha256": suiteHash,
		"provider":     h.Provider,
		"base_url":     info.baseURL,
		"lens_model":   info.lensModel,
		"queen_model":  info.queenModel,
		"seed":         seedJSON(h.SeedSet, h.Seed),
		"reps":         h.Reps,
		"config":       h.configName(),
		"persona":      personaLabel(h.PersonaProfile, h.PersonaSections),
		"budget_mode":  h.BudgetMode,
		"model":        h.Model,
		"results":      results,
	}, "", "  ")
	return os.WriteFile(filepath.Join(h.Workspace, "report.json"), js, 0o644)
}

// seedJSON is the seed for report.json, null when none was given.
func seedJSON(set bool, seed int64) any {
	if !set {
		return nil
	}
	return seed
}

// short is a hash cut to 16 characters.
func short(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	return h
}
