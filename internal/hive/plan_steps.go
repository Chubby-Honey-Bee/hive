package hive

import "fmt"

// planContext threads shared state through the priority pipeline so each
// handler can read suppressed coords, batch budgets, and emit actions
// without growing GeneratePlan's parameter list.
//
// Why a struct instead of closures: the previous GeneratePlan was a 213-line
// function with cyclomatic complexity ≈38, every priority block sharing
// six free variables (counter, dispatched, suppressed, wave, batchSize,
// state). Threading them explicitly here lets each handler be unit-tested
// in isolation and replaced without editing GeneratePlan — that's the
// Open/Closed payoff.
type planContext struct {
	state      *State
	wave       int
	batchSize  int
	suppressed map[planCoord]bool
	counter    int
	dispatched int // gap-fill budget tracker
	actions    []Action
}

// planCoord is a coordinate compared by value, one presence flag per axis,
// so a signal's *int axes and a gap row's int64 axes are equal when their
// values are. As `any` fields, interface equality would compare pointer
// addresses and dynamic types, and a pending stop_signal would never match a
// coordinate the plan dispatched to.
type planCoord [4]coordAxis

type coordAxis struct {
	v   int
	set bool
}

// coordOf reads a coordinate in any form it reaches the plan: *int from a
// signal, int64 from a database row, float64 from decoded JSON, or int. A nil
// or any other value is an absent axis.
func coordOf(d1, d2, d3, d4 any) planCoord {
	var c planCoord
	for i, v := range [4]any{d1, d2, d3, d4} {
		c[i] = axisOf(v)
	}
	return c
}

// axisOf reads one axis of a coordinate, as coordOf does.
func axisOf(v any) coordAxis {
	if p, ok := v.(*int); ok {
		if p == nil {
			return coordAxis{}
		}
		v = *p
	}
	if n, ok := toInt(v); ok {
		return coordAxis{v: n, set: true}
	}
	return coordAxis{}
}

func (c *planContext) nextID() string {
	c.counter++
	return fmt.Sprintf("action-%d", c.counter)
}

func (c *planContext) emit(a Action) {
	c.actions = append(c.actions, a)
}

// stepHandler emits zero or more actions for one priority slot. Returning
// nothing is the common case; the slice append happens via planContext.emit.
type stepHandler func(c *planContext, signals []Signal)

// planSteps lists the bee-colony priority pipeline in execution order.
// Adding a new behavior == appending to this slice; no edits to existing
// code. That's the OCP property the switch-ladder violated.
var planSteps = []stepHandler{
	stepQMP,             // 1. MSS invariant violations — fix before anything else
	stepAlarmCascade,    // 2. Alarm cascades — revert contradicted dependents
	stepConflictResolve, // 3. Conflict resolution — dispatch verifiers
	stepGapFill,         // 4. Waggle-dance + critical, then important, gap filling
	stepTrembleDance,    // 5. Tremble dance — lower batch size
	stepShakingSignal,   // 6. Shaking signal — raise batch size or model tier
	stepQuorumCap,       // 7. Quorum capping — seal a converged finding; it stays an assumption
	stepGateCheck,       // 8. Gate check — only if everything above is clean
}

// stepQMP emits a "fix_mss" action for every QMP signal.
func stepQMP(c *planContext, signals []Signal) {
	for _, s := range filterSignals(signals, "qmp") {
		c.emit(Action{
			ID:         c.nextID(),
			Type:       "fix_mss",
			Priority:   "critical",
			SignalType: "qmp",
			Payload:    s.Payload,
			Description: fmt.Sprintf("Fix MSS violations: %v laundering, %v untraceable",
				s.Payload["laundering_violations"], s.Payload["untraceable_guarantees"]),
		})
	}
}

// stepAlarmCascade dispatches a cascade-revert for each alarm signal.
func stepAlarmCascade(c *planContext, signals []Signal) {
	for _, s := range filterSignals(signals, "alarm") {
		loserID := loserFindingID(s.Payload)
		if loserID <= 0 {
			continue
		}
		c.emit(Action{
			ID:         c.nextID(),
			Type:       "cascade_revert",
			Priority:   "critical",
			SignalType: "alarm",
			Wave:       c.wave + 1,
			FindingID:  loserID,
			// The adjudicated conflict, so whoever runs the cascade can close
			// it with `chb db-write resolve_conflict`. While it stays open this
			// alarm fires again every iteration.
			Payload:     map[string]any{"conflict_id": s.Payload["conflict_id"]},
			Description: fmt.Sprintf("Alarm cascade: revert dependents of contradicted finding %d", loserID),
		})
	}
}

// loserFindingID is the contradicted finding an alarm names, as an int64
// from the evaluator or a float64 from decoded JSON; 0 when it names none.
func loserFindingID(payload map[string]any) int64 {
	loserID, _ := payload["loser_finding_id"].(int64)
	if loserID == 0 {
		if f, ok := payload["loser_finding_id"].(float64); ok {
			loserID = int64(f)
		}
	}
	return loserID
}

// stepConflictResolve dispatches up to batchSize verifier agents on
// unresolved conflicts that have no winner yet. A conflict whose winner is
// named is already adjudicated: its alarm's cascade_revert closes it, and a
// second adjudication could revert the finding that won.
//
// A tremble dance recruits receivers: while it fires the budget is
// BatchSizeMax, so the verifiers are not cut back with the foraging batch
// the same dance lowers.
func stepConflictResolve(c *planContext, signals []Signal) {
	budget := verifierBudget(c.batchSize, signals)
	dispatched := 0
	for _, conflict := range c.state.UnresolvedConflicts {
		if dispatched >= budget {
			break
		}
		if !awaitsVerifier(conflict) {
			continue
		}
		dispatched++
		c.emit(c.verifierAction(conflict))
	}
}

// verifierBudget is how many verifiers a plan dispatches: the batch, or
// BatchSizeMax while a tremble dance fires.
func verifierBudget(batchSize int, signals []Signal) int {
	if len(filterSignals(signals, "tremble_dance")) > 0 && batchSize < BatchSizeMax {
		return BatchSizeMax
	}
	return batchSize
}

// verifierAction dispatches a verifier on one conflict.
func (c *planContext) verifierAction(conflict map[string]any) Action {
	return Action{
		ID:         c.nextID(),
		Type:       "dispatch_agent",
		Agent:      "verifier",
		Model:      "sonnet",
		Priority:   "high",
		SignalType: "conflict_resolution",
		Wave:       c.wave + 1,
		// The conflict's id, so the adjudicator can close it with
		// `chb db-write resolve_conflict` once the loser is reverted.
		Payload: map[string]any{"conflict_id": conflict["id"]},
		Prompt: fmt.Sprintf("Resolve conflict %v: %v. Compare findings %v and %v.",
			conflict["id"], clip(anyToStr(conflict["description"]), 200),
			conflict["finding_a_id"], conflict["finding_b_id"]),
		Description: fmt.Sprintf("Resolve conflict %v", conflict["id"]),
	}
}

// stepGapFill handles both waggle_dance signals and open gaps. They
// share the dispatch budget: the colony has finite foragers, so a
// high-priority gap and a recruited dance compete for the same slots.
// Critical gaps go first; important gaps are dispatched once no critical
// gap is open. Termination waits on both, and only a dispatch names a
// gap's id for resolve_gap to close it.
func stepGapFill(c *planContext, signals []Signal) {
	c.dispatchDances(filterSignals(signals, "waggle_dance"))
	c.dispatchGaps()
}

// dispatchDances sends a scout to each dance's gap, outside the suppressed
// coordinates, while the budget lasts.
func (c *planContext) dispatchDances(dances []Signal) {
	for _, s := range dances {
		if c.dispatched >= c.batchSize {
			break
		}
		if c.suppressed[coordOf(s.TargetD1, s.TargetD2, s.TargetD3, s.TargetD4)] {
			continue
		}
		c.emit(c.danceAction(s))
		c.dispatched++
	}
}

// danceAction dispatches a scout to the gap a dance recruits to.
func (c *planContext) danceAction(s Signal) Action {
	d1 := "d1 absent"
	if s.TargetD1 != nil {
		d1 = fmt.Sprintf("d1=%d", *s.TargetD1)
	}
	return Action{
		ID:   c.nextID(),
		Type: "dispatch_agent",
		// The persona written for signal-dispatched research
		// (agents/hive-scout.md).
		Agent:        "hive-scout",
		Model:        c.state.Hive.ModelTier,
		Priority:     "normal",
		SignalType:   "waggle_dance",
		Wave:         c.wave + 1,
		TargetCoords: map[string]any{"d1": s.TargetD1, "d2": s.TargetD2, "d3": s.TargetD3, "d4": s.TargetD4},
		Prompt:       fmt.Sprintf("Investigate gap at coordinates. %s", mapGet(s.Payload, "reason")),
		Description:  "Waggle dance: investigate gap at " + d1,
	}
}

// dispatchGaps sends a scout to each gap in the gap-fill queue while the
// budget lasts.
func (c *planContext) dispatchGaps() {
	gaps, level := gapFillQueue(c.state, c.suppressed)
	priority := gapPriority(level)
	for _, gap := range gaps {
		if c.dispatched >= c.batchSize {
			break
		}
		c.emit(c.gapAction(gap, level, priority))
		c.dispatched++
	}
}

// gapPriority is the dispatch priority of a gap-fill level: high for
// critical gaps.
func gapPriority(level string) string {
	if level == "critical" {
		return "high"
	}
	return "normal"
}

// gapAction dispatches a scout to fill one gap.
func (c *planContext) gapAction(gap map[string]any, level, priority string) Action {
	return Action{
		ID:           c.nextID(),
		Type:         "dispatch_agent",
		Agent:        "hive-scout",
		Model:        c.state.Hive.ModelTier,
		Priority:     priority,
		SignalType:   "gap_fill",
		Wave:         c.wave + 1,
		TargetCoords: map[string]any{"d1": gap["d1"], "d2": gap["d2"], "d3": gap["d3"], "d4": gap["d4"]},
		// The gap's id, so whoever fills it can close it with
		// `chb db-write resolve_gap`. Without it an agent could answer the
		// gap and still not say which gap it answered.
		Payload:     map[string]any{"gap_id": gap["id"]},
		Prompt:      fmt.Sprintf("Fill %s gap %v: %s", level, gap["id"], clip(anyToStr(gap["description"]), 200)),
		Description: fmt.Sprintf("Fill %s gap: %s", level, clip(anyToStr(gap["description"]), 60)),
	}
}

// gapFillQueue is the open gaps the gap-fill step can dispatch: the
// critical ones, or the important ones when no critical gap is open, less
// those at a suppressed coordinate. A critical gap that is suppressed still
// holds the important ones back. The shaking signal counts the same queue,
// so it fires only for work this step can dispatch.
func gapFillQueue(state *State, suppressed map[planCoord]bool) (gaps []map[string]any, level string) {
	pool, level := gapPool(state)
	for _, g := range pool {
		if !suppressed[coordOf(g["d1"], g["d2"], g["d3"], g["d4"])] {
			gaps = append(gaps, g)
		}
	}
	return gaps, level
}

// gapPool is the open critical gaps, or the important ones when no critical
// gap is open, with their level.
func gapPool(state *State) ([]map[string]any, string) {
	if len(state.CriticalGaps) > 0 {
		return state.CriticalGaps, "critical"
	}
	var pool []map[string]any
	for _, g := range state.UnresolvedGaps {
		if g["priority"] == "important" {
			pool = append(pool, g)
		}
	}
	return pool, "important"
}

// stepTrembleDance damps recruitment while processing is the bottleneck:
// batch_size falls by trembleBatchDecrement, floored at BatchSizeMin. The
// verifiers the dance recruits are dispatched by stepConflictResolve.
func stepTrembleDance(c *planContext, signals []Signal) {
	if len(filterSignals(signals, "tremble_dance")) == 0 {
		return
	}
	current := c.state.Hive.BatchSize
	newSize := clampInt(current-trembleBatchDecrement, BatchSizeMin, BatchSizeMax)
	if newSize == current {
		return
	}
	c.emit(Action{
		ID:          c.nextID(),
		Type:        "adjust_params",
		Priority:    "normal",
		SignalType:  "tremble_dance",
		Params:      map[string]any{"batch_size": newSize},
		Description: fmt.Sprintf("Tremble dance: conflicts wait for verifiers; lower batch_size to %d", newSize),
	})
}

// shakingAdjustments maps the symbolic adjustment instruction in a
// shaking_signal payload to a concrete params delta. Adding a new
// adjustment kind is one map entry — no edits to stepShakingSignal.
var shakingAdjustments = map[string]func(*planContext) map[string]any{
	"raise_batch_size": func(c *planContext) map[string]any {
		current := c.state.Hive.BatchSize
		newSize := clampInt(current+shakingBatchIncrement, BatchSizeMin, BatchSizeMax)
		if newSize == current {
			return nil // already at the ceiling
		}
		return map[string]any{"batch_size": newSize}
	},
}

// stepShakingSignal rouses the colony. A tremble dance in the same plan
// outranks it: while processing is the bottleneck, batch_size is lowered and
// not raised. The model tier moves by the tier rule (decideTier), which the
// signal for a wave of unknowns drives: at most one change per plan.
func stepShakingSignal(c *planContext, signals []Signal) {
	trembling := len(filterSignals(signals, "tremble_dance")) > 0
	for _, s := range filterSignals(signals, "shaking_signal") {
		if params := c.shakingParams(s, trembling); len(params) > 0 {
			c.emit(Action{
				ID:          c.nextID(),
				Type:        "adjust_params",
				Priority:    "normal",
				SignalType:  "shaking_signal",
				Params:      params,
				Description: fmt.Sprintf("Shaking signal: adjust %v", params),
			})
		}
	}
	c.emitTierChange(signals)
}

// shakingParams is the params change a shaking signal asks for, or nil: a
// tremble dance stops a batch raise, and an adjustment with no handler
// changes nothing.
func (c *planContext) shakingParams(s Signal, trembling bool) map[string]any {
	adjust, _ := s.Payload["adjust"].(string)
	if trembling && adjust == "raise_batch_size" {
		return nil
	}
	fn, ok := shakingAdjustments[adjust]
	if !ok {
		return nil
	}
	return fn(c)
}

// emitTierChange adjusts the model tier when the tier rule moves it.
func (c *planContext) emitTierChange(signals []Signal) {
	if d := decideTier(c.state, signals, activeTierPolicy()); d.changes() {
		c.emit(Action{
			ID:          c.nextID(),
			Type:        "adjust_params",
			Priority:    "normal",
			SignalType:  "shaking_signal",
			Params:      map[string]any{"model_tier": d.To},
			Description: fmt.Sprintf("Model tier %s: %s → %s: %s", d.Outcome, d.From, d.To, d.Reason),
		})
	}
}

// stepQuorumCap caps each finding the quorum signal names. Capping records
// that the cell converged, so quorum does not act on it again; it leaves the
// label alone, because agreement among agents is not a derivation.
func stepQuorumCap(c *planContext, signals []Signal) {
	for _, s := range filterSignals(signals, "quorum") {
		fid := toInt64(s.Payload["finding_id"])
		if fid <= 0 {
			continue
		}
		c.emit(Action{
			ID:          c.nextID(),
			Type:        "cap_finding",
			Priority:    "normal",
			SignalType:  "quorum",
			FindingID:   fid,
			Description: fmt.Sprintf("Quorum: cap finding %d (convergence=%v); it stays an assumption", fid, s.Payload["convergence_count"]),
		})
	}
}

// stepGateCheck only emits a gate action when nothing critical is on the
// table and the system is otherwise clean. Reads the actions accumulated
// so far via the context — must run last in planSteps.
func stepGateCheck(c *planContext, _ []Signal) {
	if c.hasCritical() || !gatePrerequisitesMet(c.state) {
		return
	}
	c.emit(Action{
		ID:          c.nextID(),
		Type:        "run_gate",
		Priority:    "normal",
		SignalType:  "gate_check",
		Wave:        c.wave,
		Description: fmt.Sprintf("Gate check: all prerequisites met for wave %d", c.wave),
	})
}

// hasCritical reports whether the plan so far holds a critical action.
func (c *planContext) hasCritical() bool {
	for _, a := range c.actions {
		if a.Priority == "critical" {
			return true
		}
	}
	return false
}

// gatePrerequisitesMet reports whether no critical gap or open conflict
// remains and the MSS audit passes.
func gatePrerequisitesMet(state *State) bool {
	return len(state.CriticalGaps) == 0 &&
		len(state.UnresolvedConflicts) == 0 &&
		state.MSSIntegrity == "PASS"
}
