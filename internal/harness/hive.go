package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// runHiveCase seeds a fresh database with one critical gap, runs the hive
// workflow against it, and grades the rows the run leaves behind. The seeded
// gap must be resolved by a finding the run wrote, that finding must answer
// the question, the next plan must not dispatch for the gap again, and the
// MSS audit must pass. Every gating check reads the database, not model
// text: a run can exit 0 and pass every node's accept: predicate while
// writing nothing at all.
func (h *agentHarness) runHiveCase(ctx context.Context, c harnessCase, r *caseRun) {
	project, iterations := c.hiveProject(), c.hiveIterations()
	if !h.seedHive(ctx, r, c.Gap, project) {
		return
	}
	hiveYAML, cleanup, err := resolveHiveWorkflow()
	if err != nil {
		r.check("locate workflows/hive.yaml", false, err.Error())
		return
	}
	defer cleanup()
	inputs, _ := json.Marshal(map[string]any{"project": project, "max_iterations": iterations})
	out, err := h.chbOnCase(ctx, r, "agent-run", "agent-run", hiveYAML, "--dir", r.ws, "--inputs", string(inputs),
		"--branch", "", "--budget-mode", h.BudgetMode)
	r.check("chb agent-run exits 0", err == nil, tail(out))
	if err != nil {
		return
	}
	gradeHiveRun(r.dbPath, project, c.Grade, iterations, r.check, r.warn)
}

// hiveProject is the hive case's project, harness by default.
func (c harnessCase) hiveProject() string {
	if c.Project == "" {
		return "harness"
	}
	return c.Project
}

// hiveIterations is the hive case's iteration cap, 1 by default.
func (c harnessCase) hiveIterations() int {
	if c.MaxIterations <= 0 {
		return 1
	}
	return c.MaxIterations
}

// seedHive creates the case's database, seeds gap as its one critical gap,
// initialises the hive project, and copies the agent personas into the
// workspace: agent-run resolves personas from --agents-dir inside --dir,
// and the agents run with --dir as their working directory, away from the
// tree. It is false when a step failed.
func (h *agentHarness) seedHive(ctx context.Context, r *caseRun, gap, project string) bool {
	gapJSON, _ := json.Marshal(map[string]any{
		"wave": 1, "agent": "seed", "description": strings.TrimSpace(gap), "priority": "critical",
		"d1": 0, "d2": 0, "d3": 0, "d4": 0,
	})
	for _, s := range []struct {
		step, check string
		args        []string
	}{
		{"db-init", "db-init", []string{"db-init"}},
		{"seed-gap", "seed the critical gap", []string{"db-write", "gap", string(gapJSON)}},
		{"hive-init", "chb hive init", []string{"hive", "init", "--project", project}},
	} {
		if out, err := h.chbOnCase(ctx, r, s.step, s.args...); err != nil {
			r.check(s.check, false, tail(out))
			return false
		}
	}
	if err := os.CopyFS(filepath.Join(r.ws, "agents"), os.DirFS("agents")); err != nil {
		r.check("copy the agent personas", false, err.Error())
		return false
	}
	return true
}

// resolveHiveWorkflow locates workflows/hive.yaml as an absolute path,
// with the cleanup workflow.ResolveFile asks for.
func resolveHiveWorkflow() (string, func(), error) {
	hiveYAML, _, cleanup, err := workflow.ResolveFile(filepath.Join("workflows", "hive.yaml"))
	if err != nil {
		return "", nil, err
	}
	abs, err := filepath.Abs(hiveYAML)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return abs, cleanup, nil
}

// gradeHiveRun reads what a hive run left in dbPath: the gap the seed agent
// wrote must be resolved by a finding another agent wrote, that finding must
// state the graded answer, the hive must have iterated, the next plan must
// not dispatch for the gap again, and the MSS audit must pass.
func gradeHiveRun(dbPath, project, grade string, iterations int, check, warn func(string, bool, string)) {
	store, err := db.NewStore(dbPath)
	if err != nil {
		check("open the run's database", false, err.Error())
		return
	}
	defer store.Close()
	gapID, findingID, ok := checkHiveSeededGap(store, check)
	if !ok {
		return
	}
	checkHiveResolvingFinding(store, findingID, grade, check)
	checkHiveIterations(store, project, iterations, check)
	if !checkHiveReplan(store, project, gapID, check) {
		return
	}
	warnHiveSources(store, warn)
	checkHarnessAudit(store, check)
}

// checkHiveSeededGap finds the gap the seed agent wrote and checks that it
// is resolved; false when it is not on record.
func checkHiveSeededGap(store *db.Store, check func(string, bool, string)) (int64, sql.NullInt64, bool) {
	var gapID int64
	var resolvedWave, findingID sql.NullInt64
	if err := store.ReadDB.QueryRow(
		`SELECT id, resolved_by_wave, resolution_finding_id FROM gaps WHERE agent = 'seed'`,
	).Scan(&gapID, &resolvedWave, &findingID); err != nil {
		check("the seeded gap is on record", false, err.Error())
		return 0, findingID, false
	}
	check("the seeded gap is resolved", resolvedWave.Valid && findingID.Valid,
		fmt.Sprintf("resolved_by_wave=%v resolution_finding_id=%v", nullInt(resolvedWave), nullInt(findingID)))
	return gapID, findingID, true
}

// checkHiveResolvingFinding checks that the finding that resolved the gap
// exists and that a hive agent wrote it, and, when the case is graded, that
// it states the graded answer.
func checkHiveResolvingFinding(store *db.Store, findingID sql.NullInt64, grade string, check func(string, bool, string)) {
	var text, author string
	ferr := store.ReadDB.QueryRow(`SELECT finding, agent FROM findings WHERE id = ?`, findingID.Int64).Scan(&text, &author)
	check("the resolving finding exists and a hive agent wrote it", ferr == nil && author != "seed",
		fmt.Sprintf("finding %d by %q", findingID.Int64, author))
	if grade != "" {
		checkHiveGradedAnswer(grade, text, check)
	}
}

// checkHiveGradedAnswer checks that the finding's text states the answer
// grade computes.
func checkHiveGradedAnswer(grade, text string, check func(string, bool, string)) {
	want, detail, err := gradeAnswer(grade)
	if err != nil {
		check("graded answer computes ("+grade+")", false, err.Error())
		return
	}
	check("the resolving finding answers the gap — "+detail, mentionsNumber(text, want), clip(text, 200))
}

// checkHiveIterations checks that the hive ran between one pass and the
// cap. The scan is a command node that runs `chb hive next` once per pass,
// and complete-iteration refuses a pass whose count moved, so the counter
// is the passes run. The loop stops at the cap, or earlier once two passes
// in a row change nothing.
func checkHiveIterations(store *db.Store, project string, iterations int, check func(string, bool, string)) {
	var iteration int
	_ = store.ReadDB.QueryRow(`SELECT iteration FROM hive_state WHERE project = ?`, project).Scan(&iteration)
	check(fmt.Sprintf("the hive ran 1 to %d iteration(s)", iterations), iteration >= 1 && iteration <= iterations, fmt.Sprintf("iteration=%d", iteration))
}

// checkHiveReplan checks that the next plan holds no gap-fill for the
// resolved gap; false when the plan could not be made.
func checkHiveReplan(store *db.Store, project string, gapID int64, check func(string, bool, string)) bool {
	state, err := hive.ScanState(store, project)
	if err != nil {
		check("scan the hive state", false, err.Error())
		return false
	}
	plan, err := hive.GeneratePlan(state, hive.EvaluateSignals(store, state))
	if err != nil {
		check("generate the next plan", false, err.Error())
		return false
	}
	check("the next plan holds no gap-fill for the resolved gap", !hivePlansGapFill(plan, gapID), "")
	return true
}

// hivePlansGapFill: the plan dispatches a gap-fill for the gap.
func hivePlansGapFill(plan []hive.Action, gapID int64) bool {
	for _, a := range plan {
		if id, _ := a.Payload["gap_id"].(int64); a.SignalType == "gap_fill" && id == gapID {
			return true
		}
	}
	return false
}

// warnHiveSources reports whether the run registered a source.
func warnHiveSources(store *db.Store, warn func(string, bool, string)) {
	var sources int
	_ = store.ReadDB.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&sources)
	warn("the run registered at least one source", sources > 0, fmt.Sprintf("%d source(s)", sources))
}

// mentionsNumber reports whether text states n as a whole number, allowing
// thousands separators ("4096", "4,096", "4 096").
func mentionsNumber(text, n string) bool {
	stripped := regexp.MustCompile(`(\d)[,\x{00a0}\x{202f} ](\d{3})`).ReplaceAllString(text, "$1$2")
	return regexp.MustCompile(`(^|\D)` + regexp.QuoteMeta(n) + `(\D|$)`).MatchString(stripped)
}

// nullInt is v's value, nil when it is NULL.
func nullInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// clip is s trimmed, cut to n runes with an ellipsis when longer.
func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
