// Package gate decides whether a wave's research may close: the gate
// pipeline (RunGatePipeline) and the passes it runs, conflict detection
// (DetectConflicts), convergence scoring (MergeFindings) and the
// evaluation a gate records (DeriveEvaluation).
package gate

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// GateResult holds the outcome of the gate pipeline.
type GateResult struct {
	Errors   []string
	Warnings []string
	Opened   bool
}

// pipeStepResult accumulates errors and warnings from a single pipeline step.
type pipeStepResult struct {
	errors   []string
	warnings []string

	// passed carries a step's own verdict to wave_gates, so a wave whose
	// sources are dead or whose conflicts are unresolved is recorded as
	// failing those checks.
	passed bool
}

// RunGatePipeline executes the gate check for a wave. requireOutcomeReview
// adds the opt-in precondition of cde-mss.md § Calibration consumers: a
// guarantee in a domain whose calibrated guarantee hit rate is under the
// floor blocks the gate until a human or an external source has resolved
// it.
func RunGatePipeline(store *db.Store, wave int, evalJSON map[string]any, autoResolveNumeric, force, requireOutcomeReview bool) (*GateResult, error) {
	steps, flags := runGateSteps(store, wave, evalJSON, autoResolveNumeric, requireOutcomeReview)
	result, err := pipeDecideGate(store.ReadDB, store.WriteDB, wave, collectStepResults(steps), force, flags)
	if err != nil || !result.Opened {
		return result, err
	}
	recordGateTick(store, wave)
	return result, nil
}

// runGateSteps runs the pipeline's checks in their order and returns each
// step's result with the flags wave_gates records for them.
func runGateSteps(store *db.Store, wave int, evalJSON map[string]any, autoResolveNumeric, requireOutcomeReview bool) ([]pipeStepResult, gateFlags) {
	rdb := store.ReadDB
	integrity, mssPassed := pipeAuditIntegrity(store)

	agents := pipeCheckAgents(rdb, wave)
	conflicts := pipeResolveConflicts(store, wave, autoResolveNumeric)
	sources := pipeCheckSources(store, wave)
	steps := []pipeStepResult{
		agents,
		conflicts,
		sources,
		pipeAuditMSS(rdb, wave),
		integrity,
	}
	if requireOutcomeReview {
		steps = append(steps, pipeOutcomeReview(store, wave))
	}
	steps = append(steps, pipeRecordEvaluation(rdb, store.WriteDB, wave, evalJSON))
	return steps, gateFlags{
		mss:       mssPassed,
		sources:   sources.passed,
		conflicts: conflicts.passed,
		agents:    agents.passed,
	}
}

// collectStepResults gathers every step's errors and warnings, in step
// order, into one GateResult.
func collectStepResults(steps []pipeStepResult) *GateResult {
	result := &GateResult{}
	for _, sr := range steps {
		result.Errors = append(result.Errors, sr.errors...)
		result.Warnings = append(result.Warnings, sr.warnings...)
	}
	return result
}

// recordGateTick marks the opened wave on the Time Wheel. A gated wave is a
// closed interval there: the "what did the hive believe at wave N?" anchor.
// It is recorded whichever command opens the gate, `chb guard` or `db-write
// gate_wave`. Best-effort.
func recordGateTick(store *db.Store, wave int) {
	if tickID, err := store.TimeWheel().Begin(
		fmt.Sprintf("wave-%d", wave), db.TickWave, 0, int64(wave), "gate opened",
	); err == nil {
		_ = store.TimeWheel().End(tickID)
	}
}

// gateFlags is what wave_gates records: one flag per check the pipeline
// actually ran, rather than a row of constants.
type gateFlags struct {
	mss, sources, conflicts, agents bool
}

// countRows runs a COUNT(*) query. A query that fails is an error, which
// fails the step that counted: read as zero, a failed count of the running
// agents would let the gate open on an error.
func countRows(rdb *sql.DB, query string, args ...any) (int, error) {
	var n int
	err := rdb.QueryRow(query, args...).Scan(&n)
	return n, err
}

// pipeCheckSources checks the wave's sources: every URL its findings cite in
// source_urls and every source registered for it. A URL `chb
// validate-sources` found dead blocks the gate; one it never validated
// warns, because "unverified" is not "bad" and should not be recorded as
// either. Each cited URL is looked up by URL, whatever wave its sources row
// carries, since validate-sources writes its verdicts with none.
func pipeCheckSources(store *db.Store, wave int) pipeStepResult {
	statuses, err := waveSourceStatuses(store, wave)
	if err != nil {
		return pipeStepResult{errors: []string{fmt.Sprintf("source check did not run: %v", err)}}
	}
	return sourceVerdict(wave, statuses["dead"], statuses["unchecked"])
}

// waveSourceStatuses counts the wave's sources by validation status.
func waveSourceStatuses(store *db.Store, wave int) (map[string]int, error) {
	urls, err := store.Sources().WaveURLs(&wave)
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]int)
	for _, u := range urls {
		status, err := sourceStatus(store.ReadDB, u)
		if err != nil {
			return nil, err
		}
		statuses[status]++
	}
	return statuses, nil
}

// sourceStatus is the validation status recorded for a URL: "unchecked"
// when it has no row or no status.
func sourceStatus(rdb *sql.DB, u string) (string, error) {
	var status sql.NullString
	err := rdb.QueryRow(`SELECT validation_status FROM sources WHERE url=?`, u).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "unchecked", nil
	}
	if err != nil {
		return "", err
	}
	if !status.Valid {
		return "unchecked", nil
	}
	return status.String, nil
}

// sourceVerdict is the source check's result: a dead source blocks, an
// unvalidated one warns.
func sourceVerdict(wave, dead, unchecked int) pipeStepResult {
	var sr pipeStepResult
	if dead > 0 {
		sr.errors = append(sr.errors, fmt.Sprintf("%d dead source(s) in wave %d — run `chb validate-sources`", dead, wave))
		return sr
	}
	if unchecked > 0 {
		sr.warnings = append(sr.warnings,
			fmt.Sprintf("%d source(s) in wave %d were never validated", unchecked, wave))
	}
	sr.passed = true
	return sr
}

// pipeAuditIntegrity runs the full four-criteria MSS audit: transitive
// laundering, untraceable guarantees, dependency cycles and partition
// violations. pipeAuditMSS above is wave-scoped and catches only direct
// one-hop laundering, so without this step a `guarantee -> guarantee ->
// unknown` chain — the exact case the transitive BFS exists for — opens the
// gate. Returns whether the audit passed so wave_gates records the real
// outcome rather than an assumed one.
func pipeAuditIntegrity(store *db.Store) (pipeStepResult, bool) {
	var sr pipeStepResult

	res, err := store.MSSAudit()
	if err != nil {
		sr.errors = append(sr.errors, fmt.Sprintf("MSS audit did not run: %v", err))
		return sr, false
	}
	if res.Integrity == "PASS" {
		return sr, true
	}
	sr.errors = integrityViolations(res)
	return sr, false
}

// integrityViolations names each audit criterion with violations, and how
// many, sorted.
func integrityViolations(res *db.MSSAuditResult) []string {
	var errs []string
	for label, rows := range map[string][]map[string]any{
		"laundering violations":  res.LaunderingViolations,
		"untraceable guarantees": res.UntraceableGuarantees,
		"dependency cycles":      res.DependencyCycles,
		"partition violations":   res.PartitionViolations,
	} {
		if len(rows) > 0 {
			errs = append(errs, fmt.Sprintf("MSS audit: %d %s", len(rows), label))
		}
	}
	sort.Strings(errs)
	return errs
}

// agentRunCounts are a wave's agent runs by outcome.
type agentRunCounts struct {
	running, total, completed, failed int
}

// readAgentRunCounts counts a wave's agent runs, stopping at the first count
// that fails.
func readAgentRunCounts(rdb *sql.DB, wave int) (agentRunCounts, error) {
	var c agentRunCounts
	for _, q := range []struct {
		dest  *int
		query string
	}{
		{&c.running, "SELECT COUNT(*) FROM agent_runs WHERE wave=? AND status='running'"},
		{&c.total, "SELECT COUNT(*) FROM agent_runs WHERE wave=?"},
		{&c.completed, "SELECT COUNT(*) FROM agent_runs WHERE wave=? AND status='completed'"},
		{&c.failed, "SELECT COUNT(*) FROM agent_runs WHERE wave=? AND status='failed'"},
	} {
		n, err := countRows(rdb, q.query, wave)
		if err != nil {
			return agentRunCounts{}, err
		}
		*q.dest = n
	}
	return c, nil
}

// allCompleted reports whether every agent that ran finished, which is what
// the agents_completed column records.
func (c agentRunCounts) allCompleted() bool {
	return c.total == 0 || c.completed == c.total
}

// waveIsEmpty reports whether a wave has neither agent runs nor findings.
func waveIsEmpty(rdb *sql.DB, wave int, runs agentRunCounts) (bool, error) {
	if runs.total > 0 {
		return false, nil
	}
	findings, err := countRows(rdb, "SELECT COUNT(*) FROM findings WHERE wave=?", wave)
	return findings == 0, err
}

// pipeCheckAgents verifies all agents for the wave have completed. A count
// that fails fails the step.
func pipeCheckAgents(rdb *sql.DB, wave int) pipeStepResult {
	runs, err := readAgentRunCounts(rdb, wave)
	if err != nil {
		return pipeStepResult{errors: []string{fmt.Sprintf("agent check did not run: %v", err)}}
	}
	empty, err := waveIsEmpty(rdb, wave, runs)
	if err != nil {
		return pipeStepResult{errors: []string{fmt.Sprintf("agent check did not run: %v", err)}}
	}
	return agentsVerdict(wave, runs, empty)
}

// agentsVerdict is the agent check's result: an agent still running, or a
// wave with no agents and no findings, blocks; a failed agent warns.
func agentsVerdict(wave int, runs agentRunCounts, empty bool) pipeStepResult {
	var sr pipeStepResult
	if runs.running > 0 {
		sr.errors = append(sr.errors, fmt.Sprintf("%d agents still running", runs.running))
		return sr
	}
	if empty {
		sr.errors = append(sr.errors, fmt.Sprintf("No agents or findings for wave %d", wave))
		return sr
	}
	if runs.failed > 0 {
		sr.warnings = append(sr.warnings,
			fmt.Sprintf("%d of %d agents in wave %d failed", runs.failed, runs.total, wave))
	}
	sr.passed = runs.allCompleted()
	return sr
}

// unresolvedConflictsQuery counts a wave's open conflicts.
const unresolvedConflictsQuery = "SELECT COUNT(*) FROM conflicts WHERE wave=? AND resolution IS NULL"

// pipeResolveConflicts runs conflict detection over the wave, then checks
// for unresolved conflicts, auto-resolving numeric ones if requested, so a
// wave nobody ran `chb detect-conflicts` on cannot pass with real conflicts
// in it.
func pipeResolveConflicts(store *db.Store, wave int, autoResolveNumeric bool) pipeStepResult {
	var sr pipeStepResult
	if _, err := DetectConflicts(store, &wave, false); err != nil {
		sr.errors = append(sr.errors, fmt.Sprintf("conflict detection did not run: %v", err))
		return sr
	}
	unresolved, err := unresolvedConflicts(store, wave, autoResolveNumeric)
	if err != nil {
		sr.errors = append(sr.errors, fmt.Sprintf("conflict check did not run: %v", err))
		return sr
	}
	if unresolved > 0 {
		sr.warnings = append(sr.warnings, fmt.Sprintf("%d unresolved conflicts", unresolved))
		return sr
	}
	sr.passed = true
	return sr
}

// unresolvedConflicts counts the wave's open conflicts, after closing the
// numeric ones when autoResolveNumeric asks for it.
func unresolvedConflicts(store *db.Store, wave int, autoResolveNumeric bool) (int, error) {
	unresolved, err := countRows(store.ReadDB, unresolvedConflictsQuery, wave)
	if err != nil || !autoResolveNumeric || unresolved == 0 {
		return unresolved, err
	}
	return autoResolvedCount(store, wave)
}

// autoResolvedCount closes the wave's numeric conflicts, then counts those
// still open.
func autoResolvedCount(store *db.Store, wave int) (int, error) {
	if err := autoResolveNumericConflicts(store, wave); err != nil {
		return 0, fmt.Errorf("auto-resolve: %w", err)
	}
	return countRows(store.ReadDB, unresolvedConflictsQuery, wave)
}

// autoResolveNumericConflicts closes the wave's open numeric-divergence
// conflicts. An update that fails leaves its conflict open, and the count
// that follows reports it.
func autoResolveNumericConflicts(store *db.Store, wave int) error {
	rows, err := store.ReadDB.Query("SELECT id, description FROM conflicts WHERE wave=? AND resolution IS NULL", wave)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := resolveIfNumeric(store.WriteDB, rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// resolveIfNumeric reads one open conflict and closes it when it records a
// numeric divergence.
func resolveIfNumeric(wdb *sql.DB, rows *sql.Rows) error {
	var id int64
	var desc string
	if err := rows.Scan(&id, &desc); err != nil {
		return err
	}
	if isNumericConflict(desc) {
		_, _ = wdb.Exec("UPDATE conflicts SET resolution=? WHERE id=?",
			"Auto-resolved: numeric divergence", id)
	}
	return nil
}

// isNumericConflict reports whether a conflict's description records a
// numeric divergence.
func isNumericConflict(desc string) bool {
	return (len(desc) > 9 && desc[:9] == "[numeric]") || strings.Contains(desc, "Numeric divergence")
}

// launderingQuery counts the wave's guarantees that depend directly on an
// unknown.
const launderingQuery = `
		SELECT COUNT(*) FROM findings f1
		WHERE f1.wave=? AND f1.mss_label='guarantee' AND f1.depends_on_ids IS NOT NULL
		AND EXISTS (
			SELECT 1 FROM findings f2
			WHERE f2.mss_label='unknown'
			AND f2.id IN (SELECT value FROM json_each(f1.depends_on_ids))
		)
	`

// pipeAuditMSS checks for MSS laundering violations and for a wave skewed
// toward the strong labels. A wave of mostly assumptions or unknowns is how
// honest research reads: a sourced fact the agent did not verify is an
// assumption, and a gap is an unknown. A wave of five or more findings where
// definitions, or guarantees, pass 80% is the sign of overclaiming: facts
// labelled as choices, or conclusions labelled as derived.
func pipeAuditMSS(rdb *sql.DB, wave int) pipeStepResult {
	var sr pipeStepResult
	laundering, err := countRows(rdb, launderingQuery, wave)
	if err != nil {
		sr.errors = append(sr.errors, fmt.Sprintf("MSS laundering check did not run: %v", err))
	} else if laundering > 0 {
		sr.errors = append(sr.errors, fmt.Sprintf("%d MSS laundering violations", laundering))
	}
	labels, total, err := waveLabelCounts(rdb, wave)
	if err != nil {
		sr.errors = append(sr.errors, fmt.Sprintf("MSS label check did not run: %v", err))
		return sr
	}
	sr.warnings = labelSkewWarnings(labels, total, wave)
	return sr
}

// waveLabelCounts counts the wave's findings per MSS label, and in all.
func waveLabelCounts(rdb *sql.DB, wave int) (map[string]int, int, error) {
	rows, err := rdb.Query("SELECT mss_label, COUNT(*) FROM findings WHERE wave=? GROUP BY mss_label", wave)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	labels := make(map[string]int)
	total := 0
	for rows.Next() {
		var label string
		var cnt int
		if err := rows.Scan(&label, &cnt); err != nil {
			return nil, 0, err
		}
		labels[label] = cnt
		total += cnt
	}
	return labels, total, rows.Err()
}

// labelSkewWarnings warns of a wave of five or more findings in which
// definitions, or guarantees, pass 80%.
func labelSkewWarnings(labels map[string]int, total, wave int) []string {
	if total < 5 {
		return nil
	}
	var warnings []string
	for _, label := range []string{"definition", "guarantee"} {
		// More than 80%, in integers: cnt/total > 4/5.
		if cnt := labels[label]; cnt*5 > total*4 {
			warnings = append(warnings, fmt.Sprintf(
				"MSS skew: %d/%d findings in wave %d are %s — check them for overclaiming", cnt, total, wave, label))
		}
	}
	return warnings
}

// pipeOutcomeReview is the opt-in outcome-review precondition. For each
// guarantee in the wave it reads the calibrated label/guarantee score of
// the most specific of its scopes that has one (calibration.LabelScore);
// when that hit rate is under GuaranteeConfirmFloor, the guarantee needs a
// human or external outcome (calibration.ConfirmationState: a
// downstream_run outcome alone is no review), and one without blocks. A
// domain with no calibrated guarantee score asks for nothing, so with
// fewer than NFloor outcomes everywhere the step passes.
func pipeOutcomeReview(store *db.Store, wave int) pipeStepResult {
	var sr pipeStepResult
	problems, err := outcomeReviewProblems(store, wave)
	sr.errors = problems
	if err != nil {
		sr.errors = append(sr.errors, fmt.Sprintf("outcome review did not run: %v", err))
		return sr
	}
	sr.passed = len(sr.errors) == 0
	return sr
}

// reviewCandidate is a guarantee of the wave and the coordinates its
// calibration scopes come from.
type reviewCandidate struct {
	id     int64
	d1, d2 sql.NullInt64
}

// outcomeReviewProblems names each of the wave's guarantees that needs an
// outcome review and has none. With an error it returns the problems found
// before it.
func outcomeReviewProblems(store *db.Store, wave int) ([]string, error) {
	rows, err := store.Calibration().ListScores(calibration.KindLabel, nil)
	if err != nil {
		return nil, err
	}
	guarantees, err := waveGuarantees(store.ReadDB, wave)
	if err != nil {
		return nil, err
	}
	return reviewGuarantees(store, calibration.ScoresFrom(rows), guarantees)
}

// waveGuarantees reads the wave's guarantees in id order.
func waveGuarantees(rdb *sql.DB, wave int) ([]reviewCandidate, error) {
	q, err := rdb.Query(`SELECT id, d1, d2 FROM findings WHERE wave = ? AND mss_label = 'guarantee' ORDER BY id`, wave)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	var guarantees []reviewCandidate
	for q.Next() {
		var g reviewCandidate
		if err := q.Scan(&g.id, &g.d1, &g.d2); err != nil {
			return nil, err
		}
		guarantees = append(guarantees, g)
	}
	return guarantees, q.Err()
}

// reviewGuarantees checks each guarantee in turn and names those that need
// a review. With an error it returns the problems found before it.
func reviewGuarantees(store *db.Store, scores calibration.Scores, guarantees []reviewCandidate) ([]string, error) {
	var problems []string
	for _, g := range guarantees {
		problem, err := guaranteeReviewProblem(store, scores, g)
		if err != nil {
			return problems, err
		}
		if problem != "" {
			problems = append(problems, problem)
		}
	}
	return problems, nil
}

// reviewNotNeeded reports whether a guarantee's calibrated score asks for no
// review: there is none, or its hit rate is at or above the floor.
func reviewNotNeeded(score *db.ScoreRow) bool {
	return score == nil || score.HitRate >= calibration.GuaranteeConfirmFloor
}

// guaranteeReviewProblem is the gate error for a guarantee that needs an
// outcome review and has none, or "" when it needs none or has one.
func guaranteeReviewProblem(store *db.Store, scores calibration.Scores, g reviewCandidate) (string, error) {
	score := scores.LabelScore("guarantee", calibration.ScopesFor(g.d1, g.d2))
	if reviewNotNeeded(score) {
		return "", nil
	}
	state, err := calibration.ConfirmationState(store, g.id)
	if err != nil {
		return "", err
	}
	if state.Reviewed {
		return "", nil
	}
	return fmt.Sprintf(
		"guarantee %d needs outcome review: guarantees in scope %s hold at %.2f (n=%d), under %.2f, and no human or external outcome resolves it — record one with `chb outcome-record` or `chb outcome-import`",
		g.id, scopeName(score.ScopeKey), score.HitRate, score.NResolved, calibration.GuaranteeConfirmFloor), nil
}

// scopeName names a calibration scope, "global" for the empty key.
func scopeName(key string) string {
	if key == "" {
		return "global"
	}
	return key
}

// evalVerdicts are the verdicts the evaluations table accepts.
var evalVerdicts = map[string]bool{"COMPLETE": true, "NEEDS_MORE_WORK": true, "NEEDS_MINOR_FOLLOWUP": true}

// evalScoreKeys are the evaluation's scores in the INSERT's column order.
var evalScoreKeys = []string{"coverage", "depth", "sources", "actionability", "mss_integrity"}

// normalizeVerdict upper-cases a verdict and turns spaces and hyphens into
// underscores, so "needs more work" reads as NEEDS_MORE_WORK.
func normalizeVerdict(v string) string {
	return strings.NewReplacer(" ", "_", "-", "_").Replace(strings.ToUpper(strings.TrimSpace(v)))
}

// pipeRecordEvaluation inserts or validates the evaluation record for the wave.
//
// An evaluation passed in must be recorded, or the gate stays shut: a score
// outside 1–5, or a verdict the table's CHECK refuses, such as the
// evaluator's own "NEEDS MORE WORK", records nothing and blocks.
func pipeRecordEvaluation(rdb *sql.DB, wdb *sql.DB, wave int, evalJSON map[string]any) pipeStepResult {
	if evalJSON != nil {
		return recordGivenEvaluation(wdb, wave, evalJSON)
	}
	return checkRecordedEvaluation(rdb, wave)
}

// recordGivenEvaluation records the evaluation passed in. A verdict of
// NEEDS_MORE_WORK blocks once it is recorded.
func recordGivenEvaluation(wdb *sql.DB, wave int, evalJSON map[string]any) pipeStepResult {
	var sr pipeStepResult
	args, verdict, err := evaluationInsertArgs(wave, evalJSON)
	if err != nil {
		sr.errors = append(sr.errors, err.Error())
		return sr
	}
	if _, err := wdb.Exec(
		`INSERT INTO evaluations
			 (wave, coverage_score, depth_score, source_score, actionability_score, mss_integrity_score, verdict)
			 VALUES (?,?,?,?,?,?,?)`,
		args...,
	); err != nil {
		sr.errors = append(sr.errors, fmt.Sprintf("evaluation not recorded: %v", err))
		return sr
	}
	if verdict == "NEEDS_MORE_WORK" {
		sr.errors = append(sr.errors, "Evaluation verdict: NEEDS_MORE_WORK")
	}
	return sr
}

// evaluationInsertArgs are the evaluations INSERT's arguments for evalJSON,
// with its normalized verdict, or the reason it cannot be recorded.
func evaluationInsertArgs(wave int, evalJSON map[string]any) ([]any, string, error) {
	verdict, err := evalVerdict(evalJSON)
	if err != nil {
		return nil, "", err
	}
	scores, err := evalScores(evalJSON)
	if err != nil {
		return nil, "", err
	}
	args := append([]any{wave}, scores...)
	return append(args, verdict), verdict, nil
}

// evalVerdict is the evaluation's verdict, normalized, or why the table
// would refuse it.
func evalVerdict(evalJSON map[string]any) (string, error) {
	raw, _ := evalJSON["verdict"].(string)
	verdict := normalizeVerdict(raw)
	if !evalVerdicts[verdict] {
		// The verdict as the JSON it was given: null when omitted.
		given, _ := json.Marshal(evalJSON["verdict"])
		return "", fmt.Errorf("evaluation not recorded: verdict %s is not COMPLETE, NEEDS_MORE_WORK or NEEDS_MINOR_FOLLOWUP", given)
	}
	return verdict, nil
}

// evalScores are the evaluation's scores in the INSERT's column order. An
// absent score is NULL, which the table allows.
func evalScores(evalJSON map[string]any) ([]any, error) {
	scores := make([]any, 0, len(evalScoreKeys))
	for _, key := range evalScoreKeys {
		score, err := evalScore(key, evalJSON[key])
		if err != nil {
			return nil, err
		}
		scores = append(scores, score)
	}
	return scores, nil
}

// evalScore is one score as the table stores it: nil (NULL) when absent,
// else a whole number from 1 to 5.
func evalScore(key string, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	n, isNum := scoreNumber(v)
	if !isNum || !wholeScore(n) {
		return nil, fmt.Errorf("evaluation not recorded: %s must be a whole number from 1 to 5, got %v", key, v)
	}
	return int(n), nil
}

// scoreNumber reads a score given as a float64 or an int.
func scoreNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

// wholeScore reports whether n is a whole number from 1 to 5.
func wholeScore(n float64) bool {
	return n >= 1 && n <= 5 && n == float64(int(n))
}

// checkRecordedEvaluation reads the wave's latest recorded evaluation: none
// warns, and a verdict of NEEDS_MORE_WORK blocks.
func checkRecordedEvaluation(rdb *sql.DB, wave int) pipeStepResult {
	var sr pipeStepResult
	var evalID int64
	var verdict string
	err := rdb.QueryRow("SELECT id, verdict FROM evaluations WHERE wave=? ORDER BY id DESC LIMIT 1", wave).Scan(&evalID, &verdict)
	if err != nil {
		sr.warnings = append(sr.warnings, "No evaluation recorded")
	} else if verdict == "NEEDS_MORE_WORK" {
		sr.errors = append(sr.errors, "Evaluation verdict: NEEDS_MORE_WORK")
	}
	return sr
}

// pipeDecideGate opens the wave gate if there are no blocking errors
// (and no warnings when force is false).
func pipeDecideGate(rdb *sql.DB, wdb *sql.DB, wave int, result *GateResult, force bool, flags gateFlags) (*GateResult, error) {
	if gateBlocked(result, force) {
		return result, nil
	}
	if err := recordWaveGate(rdb, wdb, wave, flags); err != nil {
		return result, err
	}
	result.Opened = true
	return result, nil
}

// gateBlocked reports whether the pipeline's findings keep the gate shut:
// any error, or any warning unless forced.
func gateBlocked(result *GateResult, force bool) bool {
	return len(result.Errors) > 0 || (len(result.Warnings) > 0 && !force)
}

// recordWaveGate writes the wave's wave_gates row: its latest evaluation
// and the flags of the checks the pipeline ran.
func recordWaveGate(rdb *sql.DB, wdb *sql.DB, wave int, flags gateFlags) error {
	// Nullable, not zero: evaluation_id has a foreign key onto evaluations,
	// which a 0 for a wave with no evaluation row would fail.
	var evalID sql.NullInt64
	if err := rdb.QueryRow(
		"SELECT id FROM evaluations WHERE wave=? ORDER BY id DESC LIMIT 1", wave,
	).Scan(&evalID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read the wave's evaluation: %w", err)
	}
	if _, err := wdb.Exec(
		`INSERT OR REPLACE INTO wave_gates
		 (wave, evaluation_id, mss_audit_passed, source_check_passed, conflict_check_passed, agents_completed)
		 VALUES (?,?,?,?,?,?)`,
		wave, evalID, boolFlag(flags.mss), boolFlag(flags.sources),
		boolFlag(flags.conflicts), boolFlag(flags.agents),
	); err != nil {
		// The gate row is the audit trail. Opening the gate without
		// recording why is worse than not opening it.
		return fmt.Errorf("record the wave gate: %w", err)
	}
	return nil
}

// boolFlag stores a flag as SQLite's 1 or 0.
func boolFlag(b bool) int {
	if b {
		return 1
	}
	return 0
}
