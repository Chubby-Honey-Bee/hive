package hive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/dreamer"
)

// execer is a write pool or a db.Tx: what the hive's writers take, so one
// transaction can hold a scan's writes or a completion's.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Scan is what one `chb hive next` scan records.
type Scan struct {
	Project string
	State   *State
	// Terminal records a terminal scan: the metrics, the phase and
	// TerminalReason. It counts no iteration and applies nothing.
	Terminal       bool
	TerminalReason string
	// Fired are the signals the scan raised. They are recorded already
	// acted on: the plan is the one acting on them.
	Fired []Signal
	// Apply runs the plan's deterministic actions in the scan's
	// transaction: fix_mss, cascade_revert, cap_finding and adjust_params.
	Apply bool
	Plan  []Action
	// MaxIterations, above 0, is the hive's cap. A scan that is not terminal
	// and finds hive_state.iteration at the cap or above records nothing.
	MaxIterations int
}

// atCap reports whether a scan that is not terminal finds the hive at its
// cap or above.
func (s Scan) atCap(iteration int) bool {
	return !s.Terminal && s.MaxIterations > 0 && iteration >= s.MaxIterations
}

// ScanResult is what RecordScan recorded.
type ScanResult struct {
	// Capped is true when the scan found the hive at its cap and recorded
	// nothing.
	Capped bool
	// Iteration is hive_state.iteration after the scan.
	Iteration     int
	SignalIDs     []int64
	Caps          []CapResult
	ParamsApplied []map[string]any
	// MSSFixed are the laundering guarantees fix_mss demoted to assumption.
	MSSFixed []int64
	// Cascades are the cascade_revert actions run.
	Cascades []CascadeResult
}

// CascadeResult is one cascade_revert --apply ran: the contradicted
// finding, the findings the cascade reverted to unknown, and the conflict it
// closed (0 when the action named none).
type CascadeResult struct {
	FindingID  int64   `json:"finding_id"`
	Reverted   []int64 `json:"reverted"`
	ConflictID int64   `json:"conflict_id"`
}

// RecordScan writes what one scan records, in one write transaction. A
// terminal scan records the health metrics, the terminal phase and its
// reason. Any other scan, unless the hive is at its cap, records the
// metrics, the fired signals, the research state at the pass's start (for
// the stall rule), what --apply runs, and the iteration bump with the signal
// watermark. The cap is read inside the transaction, so two scans cannot
// both pass it. Any failed write rolls all of it back, so a scan that errors
// leaves the hive as it found it and can be run again.
func RecordScan(store *db.Store, scan Scan) (ScanResult, error) {
	tx, err := store.BeginImmediate(context.Background())
	if err != nil {
		return ScanResult{}, fmt.Errorf("record scan: %w", err)
	}
	defer tx.Rollback()
	return recordScanIn(tx, scan)
}

// recordScanIn is RecordScan inside its transaction.
func recordScanIn(tx *db.Tx, scan Scan) (ScanResult, error) {
	var res ScanResult
	if err := tx.QueryRow(`SELECT iteration FROM hive_state WHERE project=?`, scan.Project).Scan(&res.Iteration); err != nil {
		return res, stateReadError(scan.Project, err)
	}
	if scan.atCap(res.Iteration) {
		res.Capped = true
		return res, nil
	}
	if err := recordMetrics(tx, scan); err != nil {
		return res, err
	}
	return recordOutcome(tx, scan, res)
}

// recordOutcome writes what follows the metrics: a terminal scan's phase,
// or the pass of any other.
func recordOutcome(tx *db.Tx, scan Scan, res ScanResult) (ScanResult, error) {
	if scan.Terminal {
		return res, recordTerminal(tx, scan)
	}
	return recordPass(tx, scan, res)
}

// recordMetrics writes the scan's health metrics to hive_state.
func recordMetrics(tx *db.Tx, scan Scan) error {
	state := scan.State
	if _, err := tx.Exec(
		`UPDATE hive_state SET
		 total_findings=?, unresolved_gaps=?, unresolved_conflicts=?,
		 mss_integrity=?, label_skew=?, updated_at=CURRENT_TIMESTAMP
		 WHERE project=?`,
		state.TotalFindings, len(state.UnresolvedGaps),
		len(state.UnresolvedConflicts), state.MSSIntegrity,
		state.LabelSkew, scan.Project,
	); err != nil {
		return fmt.Errorf("record hive metrics: %w", err)
	}
	return nil
}

// recordTerminal writes the terminal phase and its reason, and commits.
func recordTerminal(tx *db.Tx, scan Scan) error {
	if _, err := tx.Exec(
		"UPDATE hive_state SET phase='terminal', terminal_reason=? WHERE project=?",
		scan.TerminalReason, scan.Project,
	); err != nil {
		return fmt.Errorf("record terminal phase: %w", err)
	}
	return tx.Commit()
}

// passWrites are what a scan that is not terminal writes, in order: the
// fired signals, the research state as the pass begins, what --apply runs,
// and the iteration bump with the signal watermark, which commits.
var passWrites = []func(tx *db.Tx, scan Scan, res *ScanResult) error{
	emitFired,
	recordPassStart,
	applyPlan,
	advanceHive,
}

// recordPass writes a pass that is not terminal and counts it.
func recordPass(tx *db.Tx, scan Scan, res ScanResult) (ScanResult, error) {
	for _, write := range passWrites {
		if err := write(tx, scan, &res); err != nil {
			return res, err
		}
	}
	res.Iteration++
	return res, nil
}

// emitFired records the scan's signals, already acted on, and their ids.
func emitFired(tx *db.Tx, scan Scan, res *ScanResult) error {
	res.SignalIDs = make([]int64, 0, len(scan.Fired))
	for _, sig := range scan.Fired {
		id, err := emitActedOn(tx, sig)
		if err != nil {
			return err
		}
		res.SignalIDs = append(res.SignalIDs, id)
	}
	return nil
}

// emitActedOn inserts one fired signal, acted on, and returns its id.
func emitActedOn(tx *db.Tx, sig Signal) (int64, error) {
	payloadJSON, _ := json.Marshal(sig.Payload)
	r, err := tx.Exec(
		`INSERT INTO signals
		 (signal_type, source_type, source_id, target_d1, target_d2, target_d3, target_d4, payload_json, wave, acted_on)
		 VALUES (?,?,?,?,?,?,?,?,?,1)`,
		sig.SignalType, sourceTypeArg(sig.SourceType), sig.SourceID,
		sig.TargetD1, sig.TargetD2, sig.TargetD3, sig.TargetD4,
		string(payloadJSON), sig.Wave,
	)
	if err != nil {
		return 0, fmt.Errorf("emit %s signal: %w", sig.SignalType, err)
	}
	id, _ := r.LastInsertId()
	return id, nil
}

// sourceTypeArg stores an empty source type as NULL.
func sourceTypeArg(sourceType string) any {
	if sourceType == "" {
		return nil
	}
	return sourceType
}

// recordPassStart records the research state as the pass begins, before
// --apply writes: the stall rule compares it with the state as the pass
// ends.
func recordPassStart(tx *db.Tx, scan Scan, res *ScanResult) error {
	start, err := readProgress(tx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO hive_iterations (project, iteration, start_progress) VALUES (?,?,?)`,
		scan.Project, res.Iteration+1, start.canonical(),
	); err != nil {
		return fmt.Errorf("record the start of pass %d: %w", res.Iteration+1, err)
	}
	return nil
}

// applyPlan runs the plan's deterministic actions under --apply and records
// the tier rule's decision.
func applyPlan(tx *db.Tx, scan Scan, res *ScanResult) error {
	if !scan.Apply {
		return nil
	}
	if err := applyActions(tx, scan, res); err != nil {
		return err
	}
	return recordTier(tx, scan, res.Iteration+1)
}

// applyActions runs the plan's repairs, caps and parameter adjustments.
func applyActions(tx *db.Tx, scan Scan, res *ScanResult) error {
	var err error
	if res.MSSFixed, res.Cascades, err = applyRepairs(tx, scan.Plan); err != nil {
		return err
	}
	if res.Caps, err = applyCaps(tx, scan.Plan); err != nil {
		return err
	}
	res.ParamsApplied, err = applyParamAdjustments(tx, scan.Project, scan.Plan)
	return err
}

// advanceHive counts the pass in hive_state with the watermark of the
// pending signals its plan read, and commits.
func advanceHive(tx *db.Tx, scan Scan, _ *ScanResult) error {
	if _, err := tx.Exec(
		"UPDATE hive_state SET iteration=iteration+1, phase='dispatching', signals_through=?, updated_at=CURRENT_TIMESTAMP WHERE project=?",
		scan.State.SignalsThrough(), scan.Project,
	); err != nil {
		return fmt.Errorf("advance hive state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record scan: %w", err)
	}
	return nil
}

// recordTier writes the model-tier rule's decision for the pass, with its
// reason and its clock, to hive_tier_log. The plan's adjust_params action
// made the same decision from the same state and signals, and --apply ran
// it above.
func recordTier(tx *db.Tx, scan Scan, pass int) error {
	d := decideTier(scan.State, scan.Fired, activeTierPolicy())
	holds := 0
	if d.Holds {
		holds = 1
	}
	if _, err := tx.Exec(
		`INSERT INTO hive_tier_log
		 (project, iteration, wave, unknowns_dominate, from_tier, to_tier, outcome, reason, wait_scans, clear_scans)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		scan.Project, pass, scan.State.LatestWave, holds, d.From, d.To, d.Outcome, d.Reason, d.Clock.Wait, d.Clock.Clear,
	); err != nil {
		return fmt.Errorf("record the model tier of pass %d: %w", pass, err)
	}
	return nil
}

// applyRepairs runs the plan's mechanical repairs in tx, in plan order. A
// fix_mss runs the dreamer's settle pass with apply once, however many qmp
// signals asked: it demotes each guarantee whose dependencies reach an
// unknown to an assumption. It repairs laundering only; an untraceable
// guarantee, a cycle or a partition violation stays, and qmp fires again.
// A cascade_revert reverts what rests on the contradicted finding (the
// conflict's loser) and closes the conflict its payload names, so its alarm
// does not fire again.
func applyRepairs(tx *db.Tx, plan []Action) ([]int64, []CascadeResult, error) {
	r := &repairs{fixed: []int64{}, cascades: []CascadeResult{}}
	for _, a := range plan {
		if err := r.apply(tx, a); err != nil {
			return nil, nil, err
		}
	}
	return r.fixed, r.cascades, nil
}

// repairs is what applyRepairs has done so far.
type repairs struct {
	fixed    []int64
	cascades []CascadeResult
	settled  bool
}

// apply runs one action when it is a repair.
func (r *repairs) apply(tx *db.Tx, a Action) error {
	switch a.Type {
	case "fix_mss":
		return r.settle(tx, a)
	case "cascade_revert":
		return r.cascade(tx, a)
	}
	return nil
}

// settle runs the dreamer's settle pass, the first time a fix_mss asks.
func (r *repairs) settle(tx *db.Tx, a Action) error {
	if r.settled {
		return nil
	}
	r.settled = true
	ids, err := dreamer.SettleInTx(context.Background(), tx)
	if err != nil {
		return fmt.Errorf("apply %s (settle): %w", a.ID, err)
	}
	r.fixed = append(r.fixed, ids...)
	return nil
}

// cascade reverts what rests on the action's finding and closes the
// conflict its payload names.
func (r *repairs) cascade(tx *db.Tx, a Action) error {
	if a.FindingID <= 0 {
		return nil
	}
	reverted, err := tx.CascadeRevert(a.FindingID)
	if err != nil {
		return fmt.Errorf("apply %s (cascade from finding %d): %w", a.ID, a.FindingID, err)
	}
	c := CascadeResult{FindingID: a.FindingID, Reverted: nonNilIDs(reverted), ConflictID: toInt64(a.Payload["conflict_id"])}
	if err := closeCascadeConflict(tx, a, c.ConflictID); err != nil {
		return err
	}
	r.cascades = append(r.cascades, c)
	return nil
}

// nonNilIDs is ids, or an empty list for nil.
func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// closeCascadeConflict resolves the conflict a cascade_revert names, if it
// names one.
func closeCascadeConflict(tx *db.Tx, a Action, conflictID int64) error {
	if conflictID <= 0 {
		return nil
	}
	if err := tx.ResolveConflict(conflictID, a.Wave, fmt.Sprintf("loser %d reverted", a.FindingID)); err != nil {
		return fmt.Errorf("apply %s: %w", a.ID, err)
	}
	return nil
}

// Completion is what `chb hive complete` records.
type Completion struct {
	Project string
	// Params are gain-control params, checked by CheckGainParams already;
	// nil applies none.
	Params map[string]any
	// ExpectIteration, above 0, is the iteration the pass's scan recorded.
	// A hive whose count differs is refused before anything is written.
	ExpectIteration int
}

// CompletionResult is what CompleteIteration recorded.
type CompletionResult struct {
	Iteration       int
	SignalsConsumed int64
	// Start and End are the research state as the pass began and as it
	// ended. Start is nil for a pass no scan recorded.
	Start, End *Progress
}

// ErrIterationMoved is the refusal CompleteIteration returns when the
// hive's count is not the one the pass's scan recorded.
var ErrIterationMoved = errors.New("the hive's iteration moved during the pass")

// CompleteIteration ends the project's current pass in one write
// transaction: it consumes the signals the pass's plan read, applies the
// gain-control params, returns the phase to scanning, and records the
// research state at the pass's end for the stall rule. With ExpectIteration
// set, it first refuses a hive whose count is not the scan's, writing
// nothing: something else ran `chb hive next` during the pass, and that
// pass would otherwise count two iterations.
func CompleteIteration(store *db.Store, c Completion) (CompletionResult, error) {
	var res CompletionResult
	tx, err := store.BeginImmediate(context.Background())
	if err != nil {
		return res, fmt.Errorf("complete iteration: %w", err)
	}
	defer tx.Rollback()

	if err := completeIn(tx, c, &res); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("complete iteration: %w", err)
	}
	return res, nil
}

// completeIn is CompleteIteration's writes, inside its transaction.
func completeIn(tx *db.Tx, c Completion, res *CompletionResult) error {
	through, err := readPassState(tx, c, res)
	if err != nil {
		return err
	}
	if err := closePass(tx, c, through, res); err != nil {
		return err
	}
	return recordPassEnd(tx, c.Project, res)
}

// readPassState reads the hive's signal watermark and its iteration, and
// refuses a count that is not the one ExpectIteration names.
func readPassState(tx *db.Tx, c Completion, res *CompletionResult) (int64, error) {
	var through int64
	if err := tx.QueryRow(
		`SELECT signals_through, iteration FROM hive_state WHERE project=?`, c.Project,
	).Scan(&through, &res.Iteration); err != nil {
		return 0, stateReadError(c.Project, err)
	}
	if c.ExpectIteration > 0 && res.Iteration != c.ExpectIteration {
		return 0, fmt.Errorf("%w: hive_state.iteration is %d, but this pass's scan recorded %d; something else ran `chb hive next`",
			ErrIterationMoved, res.Iteration, c.ExpectIteration)
	}
	return through, nil
}

// closePass consumes the signals the plan read, applies the gain-control
// params, and returns the phase to scanning.
func closePass(tx *db.Tx, c Completion, through int64, res *CompletionResult) error {
	var err error
	if res.SignalsConsumed, err = consumeSignals(tx, through); err != nil {
		return err
	}
	if err := applyCompletionParams(tx, c); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"UPDATE hive_state SET phase='scanning', updated_at=CURRENT_TIMESTAMP WHERE project=?", c.Project,
	); err != nil {
		return fmt.Errorf("update hive phase: %w", err)
	}
	return nil
}

// applyCompletionParams applies the completion's gain-control params, when
// it has any.
func applyCompletionParams(tx *db.Tx, c Completion) error {
	if c.Params == nil {
		return nil
	}
	if err := applyGainControl(tx, c.Project, c.Params); err != nil {
		return fmt.Errorf("apply gain control: %w", err)
	}
	return nil
}

// recordPassEnd reads the research state as the pass ends and records it
// on the pass's hive_iterations row.
func recordPassEnd(tx *db.Tx, project string, res *CompletionResult) error {
	end, err := readProgress(tx)
	if err != nil {
		return err
	}
	res.End = &end
	return closeIterationRow(tx, project, end, res)
}

// closeIterationRow records the pass's end on the row its scan wrote and
// reads the start that row holds. A pass no scan recorded has no row and is
// left as it is.
func closeIterationRow(tx *db.Tx, project string, end Progress, res *CompletionResult) error {
	start, err := readPassStart(tx, project, res.Iteration)
	if err != nil || start == nil {
		return err
	}
	res.Start = start
	if _, err := tx.Exec(
		`UPDATE hive_iterations SET end_progress=?, completed_at=CURRENT_TIMESTAMP WHERE project=? AND iteration=?`,
		end.canonical(), project, res.Iteration,
	); err != nil {
		return fmt.Errorf("record the end of pass %d: %w", res.Iteration, err)
	}
	return nil
}

// readPassStart reads the research state the pass's scan recorded, or nil
// when no scan recorded the pass.
func readPassStart(tx *db.Tx, project string, iteration int) (*Progress, error) {
	var startJSON sql.NullString
	err := tx.QueryRow(
		`SELECT start_progress FROM hive_iterations WHERE project=? AND iteration=?`, project, iteration,
	).Scan(&startJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the start of pass %d: %w", iteration, err)
	}
	var start Progress
	if err := json.Unmarshal([]byte(startJSON.String), &start); err != nil {
		return nil, fmt.Errorf("read the start of pass %d: %w", iteration, err)
	}
	return &start, nil
}
