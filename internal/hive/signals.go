package hive

import (
	"fmt"
	"sort"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Tunable thresholds for the bee-colony rules. These are Definitions
// (chosen), not Guarantees (derived). Each carries a documented rationale
// so the gain controller's behavior can be reasoned about and tuned.
const (
	// shakingBatchIncrement is how much a shaking signal widens the dispatch
	// fanout when open gaps outnumber the batch. +3 is a compromise between
	// "+1 (won't catch up)" and "+5 (over-spends in transient backlog)".
	// Capped by BatchSizeMax.
	shakingBatchIncrement = 3

	// trembleBatchDecrement is how much a tremble dance narrows the dispatch
	// fanout while conflicts wait for verifiers: fewer new findings arrive
	// until the backlog clears. Floored at BatchSizeMin.
	trembleBatchDecrement = 2

	// unknownShakingShare and unknownShakingMinFindings rouse a stronger
	// model tier when unknowns make up more than half of the latest wave, and
	// that wave holds at least five findings. An unknown is an honest gap, so
	// such a wave is no integrity fault: it says the workers sent last are
	// not finding answers. Any other label mix rouses nothing. Honest research
	// is mostly assumptions, since a sourced fact the agent did not verify is
	// one. More than half is where the gaps outnumber the answers; five
	// findings matches the gate's floor for judging a wave's labels.
	unknownShakingShare       = 0.5
	unknownShakingMinFindings = 5
)

// Signal represents a new signal to emit.
type Signal struct {
	SignalType string         `json:"signal_type"`
	SourceType string         `json:"source_type,omitempty"`
	SourceID   *int64         `json:"source_id,omitempty"`
	TargetD1   *int           `json:"target_d1,omitempty"`
	TargetD2   *int           `json:"target_d2,omitempty"`
	TargetD3   *int           `json:"target_d3,omitempty"`
	TargetD4   *int           `json:"target_d4,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
	Wave       int            `json:"wave,omitempty"`
}

// EvaluateSignals applies the bee-colony rules to the current state and
// returns the signals they raise. Each rule is a small, independently
// testable function; this driver just concatenates their output in order,
// keeping the rule set flat and each rule's logic isolated.
func EvaluateSignals(store *db.Store, state *State) []Signal {
	var signals []Signal
	signals = append(signals, evalQMP(state)...)   // Rule 1
	signals = append(signals, evalAlarm(state)...) // Rule 2
	stops := evalStopSignal(store, state)          // Rule 3
	signals = append(signals, stops...)
	signals = append(signals, evalWaggleDance(store, state)...) // Rule 4
	signals = append(signals, evalTrembleDance(state)...)       // Rule 5
	// Rule 6 sees the stops the plan will honour: these and the pending rows.
	signals = append(signals, evalShaking(state, collectSuppressed(stops, state.PendingSignals))...)
	signals = append(signals, evalQuorum(store, state)...) // Rule 7
	return signals
}

// evalQMP — Rule 1: MSS invariant enforcement (non-overridable).
func evalQMP(state *State) []Signal {
	if state.MSSIntegrity != "FAIL" {
		return nil
	}
	return []Signal{{
		SignalType: "qmp",
		SourceType: "audit",
		Payload: map[string]any{
			"laundering_violations":  state.LaunderingViolations,
			"untraceable_guarantees": state.UntraceableGuarantees,
		},
		Wave: state.LatestWave,
	}}
}

// evalAlarm — Rule 2: contradicted dependencies trigger a cascade.
func evalAlarm(state *State) []Signal {
	var signals []Signal
	for _, conflict := range state.UnresolvedConflicts {
		if adjudicatedOpen(conflict) {
			signals = append(signals, alarmSignal(conflict, state.LatestWave))
		}
	}
	return signals
}

// adjudicatedOpen reports whether a conflict has a winner named and no
// resolution yet: the state its alarm fires in.
func adjudicatedOpen(conflict map[string]any) bool {
	winnerID, hasWinner := conflict["winner_finding_id"]
	_, hasResolution := conflict["resolution"]
	return hasWinner && winnerID != nil && (!hasResolution || conflict["resolution"] == nil)
}

// alarmSignal is the alarm for an adjudicated conflict, naming its loser.
func alarmSignal(conflict map[string]any, wave int) Signal {
	findingAID := toInt64(conflict["finding_a_id"])
	findingBID := toInt64(conflict["finding_b_id"])
	loserID := findingBID
	if toInt64(conflict["winner_finding_id"]) == findingBID {
		loserID = findingAID
	}
	return Signal{
		SignalType: "alarm",
		SourceType: "conflict",
		SourceID:   int64Ptr(toInt64(conflict["id"])),
		Payload: map[string]any{
			"loser_finding_id": loserID,
			"conflict_id":      toInt64(conflict["id"]),
		},
		Wave: wave,
	}
}

// evalStopSignal — Rule 3: the head-butt that stops recruitment to a
// contested site. While the conflict rate is over conflict_rate_threshold,
// each coordinate an unresolved conflict touches gets a stop signal, and the
// plan dispatches no scout there: a finding written on contested ground may
// rest on the claim that loses. The signal lifts once the rate falls.
func evalStopSignal(store *db.Store, state *State) []Signal {
	if state.ConflictRate <= state.Hive.ConflictRateThreshold {
		return nil
	}
	// One signal per coordinate, named for the oldest conflict touching it.
	// GROUP BY puts NULL axes together, as the plan's suppression matches an
	// absent axis only to an absent axis.
	contested := queryToMaps(store.ReadDB, `
		SELECT MIN(c.id) AS conflict_id, f.d1, f.d2, f.d3, f.d4
		FROM conflicts c
		JOIN findings f ON f.id IN (c.finding_a_id, c.finding_b_id)
		WHERE c.resolution IS NULL
		GROUP BY f.d1, f.d2, f.d3, f.d4
		ORDER BY MIN(c.id), f.d1, f.d2, f.d3, f.d4
	`)
	var signals []Signal
	for _, row := range contested {
		signals = append(signals, Signal{
			SignalType: "stop_signal",
			SourceType: "conflict",
			SourceID:   int64Ptr(toInt64(row["conflict_id"])),
			TargetD1:   intPtrFromAny(row["d1"]),
			TargetD2:   intPtrFromAny(row["d2"]),
			TargetD3:   intPtrFromAny(row["d3"]),
			TargetD4:   intPtrFromAny(row["d4"]),
			Payload: map[string]any{
				"reason": fmt.Sprintf("Conflict rate %.2f exceeds threshold %.2f; conflict %v contests this coordinate",
					state.ConflictRate, state.Hive.ConflictRateThreshold, row["conflict_id"]),
			},
			Wave: state.LatestWave,
		})
	}
	return signals
}

// evalWaggleDance — Rule 4: recruit to high-value coordinates with nearby gaps.
// A capped cell is sealed: no dance recruits to a gap at a capped finding's
// coordinate. Gap fill still dispatches a critical or important gap there,
// because only a finding closes a gap and termination waits for it.
func evalWaggleDance(store *db.Store, state *State) []Signal {
	var signals []Signal
	highConv := queryToMaps(store.ReadDB, `
		SELECT d1, d2, d3, d4, COUNT(*) as cnt
		FROM findings WHERE convergence_level = 'high'
		GROUP BY d1, d2, d3, d4
	`)
	targets := newDanceTargets(state.UnresolvedGaps, sealedCoords(store))
	for _, row := range highConv {
		if gc, ok := targets.claim(row["d1"]); ok {
			signals = append(signals, waggleSignal(row["d1"], gc, state.LatestWave))
		}
	}
	return signals
}

// sealedCoords are the coordinates of capped findings, where no dance
// recruits.
func sealedCoords(store *db.Store) map[planCoord]bool {
	sealed := make(map[planCoord]bool)
	for _, row := range queryToMaps(store.ReadDB, `
		SELECT DISTINCT f.d1, f.d2, f.d3, f.d4
		FROM capped_findings cf JOIN findings f ON f.id = cf.finding_id
	`) {
		sealed[coordOf(row["d1"], row["d2"], row["d3"], row["d4"])] = true
	}
	return sealed
}

// gapCoord is an open gap's coordinate as its database row holds it.
type gapCoord struct{ d1, d2, d3, d4 any }

// danceTargets are the open gaps a dance may recruit to, in coordinate
// order, and which of them a dance has claimed.
//
// Ordered, not a map: iterating a Go map is randomised, so the same
// database produced different dispatch coordinates run to run — and a plan
// that cannot be reproduced cannot be reviewed.
type danceTargets struct {
	order   []gapCoord
	claimed map[gapCoord]bool
}

// newDanceTargets lists the open gaps outside the sealed cells, sorted.
func newDanceTargets(gaps []map[string]any, sealed map[planCoord]bool) *danceTargets {
	t := &danceTargets{claimed: make(map[gapCoord]bool)}
	for _, g := range gaps {
		t.add(g, sealed)
	}
	sort.SliceStable(t.order, func(i, j int) bool {
		return coord4Key(t.order[i]) < coord4Key(t.order[j])
	})
	return t
}

// add lists a gap's coordinate unless its cell is sealed. Nothing is
// claimed while the list is built, so a coordinate two gaps share is listed
// twice; claim takes both at once, as they are one map key.
func (t *danceTargets) add(g map[string]any, sealed map[planCoord]bool) {
	if sealed[coordOf(g["d1"], g["d2"], g["d3"], g["d4"])] {
		return
	}
	c := gapCoord{g["d1"], g["d2"], g["d3"], g["d4"]}
	if !t.claimed[c] {
		t.claimed[c] = false
		t.order = append(t.order, c)
	}
}

// claim takes the first unclaimed gap whose d1 is d1.
func (t *danceTargets) claim(d1 any) (gapCoord, bool) {
	for _, gc := range t.order {
		if !t.claimed[gc] && gc.d1 == d1 {
			t.claimed[gc] = true
			return gc, true
		}
	}
	return gapCoord{}, false
}

// waggleSignal recruits to the gap gc from high convergence at d1.
func waggleSignal(d1 any, gc gapCoord, wave int) Signal {
	return Signal{
		SignalType: "waggle_dance",
		SourceType: "finding",
		TargetD1:   intPtrFromAny(gc.d1),
		TargetD2:   intPtrFromAny(gc.d2),
		TargetD3:   intPtrFromAny(gc.d3),
		TargetD4:   intPtrFromAny(gc.d4),
		Payload: map[string]any{
			"reason":     fmt.Sprintf("High convergence at d1=%v with gap at (%v,%v,%v,%v)", d1, gc.d1, gc.d2, gc.d3, gc.d4),
			"gap_coords": []any{gc.d1, gc.d2, gc.d3, gc.d4},
		},
		Wave: wave,
	}
}

// evalTrembleDance — Rule 5: foragers cannot unload. The verifiers are the
// receivers: when more conflicts wait for one than the batch dispatches,
// processing is the bottleneck. The plan answers by dispatching a verifier
// for every waiting conflict, up to BatchSizeMax, and by lowering batch_size
// so fewer new findings arrive until the queue clears.
func evalTrembleDance(state *State) []Signal {
	waiting := 0
	for _, conflict := range state.UnresolvedConflicts {
		if awaitsVerifier(conflict) {
			waiting++
		}
	}
	if waiting <= state.Hive.BatchSize {
		return nil
	}
	return []Signal{{
		SignalType: "tremble_dance",
		SourceType: "system",
		Payload: map[string]any{
			"reason":            fmt.Sprintf("%d conflicts wait for a verifier; batch_size dispatches %d", waiting, state.Hive.BatchSize),
			"awaiting_verifier": waiting,
			"batch_size":        state.Hive.BatchSize,
		},
		Wave: state.LatestWave,
	}}
}

// awaitsVerifier reports whether a conflict still needs adjudicating. One
// whose winner is named waits only for its alarm's cascade.
func awaitsVerifier(conflict map[string]any) bool {
	return conflict["winner_finding_id"] == nil
}

// evalShaking — Rule 6: the vibration signal rouses idle workers. When the
// gaps the gap-fill step can dispatch outnumber the batch, there is more
// work than the colony sends out: raise batch_size. A gap a stop signal
// covers is not work the colony can take up. When unknowns dominate the
// latest wave, the workers are not finding answers: rouse a more capable
// model tier.
func evalShaking(state *State, suppressed map[planCoord]bool) []Signal {
	var signals []Signal
	if s, ok := batchShaking(state, suppressed); ok {
		signals = append(signals, s)
	}
	if s, ok := tierShaking(state); ok {
		signals = append(signals, s)
	}
	return signals
}

// batchShaking raises batch_size when the gaps the gap-fill step can
// dispatch outnumber the batch.
func batchShaking(state *State, suppressed map[planCoord]bool) (Signal, bool) {
	gaps, level := gapFillQueue(state, suppressed)
	if len(gaps) <= state.Hive.BatchSize {
		return Signal{}, false
	}
	return Signal{
		SignalType: "shaking_signal",
		SourceType: "system",
		Payload: map[string]any{
			"reason": fmt.Sprintf("%d dispatchable %s gaps outnumber batch_size %d", len(gaps), level, state.Hive.BatchSize),
			"adjust": "raise_batch_size",
		},
		Wave: state.LatestWave,
	}, true
}

// tierShaking rouses a more capable model tier when unknowns dominate the
// latest wave.
func tierShaking(state *State) (Signal, bool) {
	inWave := waveSize(state.LatestWaveLabels)
	unknowns := state.LatestWaveLabels["unknown"]
	if !unknownsOverShare(unknowns, inWave) {
		return Signal{}, false
	}
	return Signal{
		SignalType: "shaking_signal",
		SourceType: "system",
		Payload: map[string]any{
			"reason": fmt.Sprintf("%d of %d findings in wave %d are unknown — the workers are not finding answers",
				unknowns, inWave, state.LatestWave),
			"adjust": "upgrade_model_tier",
		},
		Wave: state.LatestWave,
	}, true
}

// unknownsOverShare reports whether a wave of at least
// unknownShakingMinFindings findings is more than unknownShakingShare
// unknowns.
func unknownsOverShare(unknowns, inWave int) bool {
	return inWave >= unknownShakingMinFindings && float64(unknowns)/float64(inWave) > unknownShakingShare
}

// evalQuorum — Rule 7: convergence threshold met. A finding already capped
// is left out: the cap is durable, so this level-triggered rule acts on a
// converged finding once.
func evalQuorum(store *db.Store, state *State) []Signal {
	var signals []Signal
	threshold := state.Hive.ConvergenceThreshold
	quorumRows := queryToMaps(store.ReadDB, `
		SELECT f.id, f.d1, f.d2, f.d3, f.d4, f.convergence_count
		FROM findings f
		WHERE f.convergence_count >= ?
		  AND f.convergence_level = 'high'
		  AND f.mss_label = 'assumption'
		  AND NOT EXISTS (
			  SELECT 1 FROM conflicts c
			  WHERE (c.finding_a_id = f.id OR c.finding_b_id = f.id)
				AND c.resolution IS NULL
		  )
		  AND NOT EXISTS (SELECT 1 FROM capped_findings cf WHERE cf.finding_id = f.id)
	`, threshold)

	for _, cand := range quorumRows {
		id := toInt64(cand["id"])
		signals = append(signals, Signal{
			SignalType: "quorum",
			SourceType: "finding",
			SourceID:   int64Ptr(id),
			TargetD1:   intPtrFromAny(cand["d1"]),
			TargetD2:   intPtrFromAny(cand["d2"]),
			TargetD3:   intPtrFromAny(cand["d3"]),
			TargetD4:   intPtrFromAny(cand["d4"]),
			Payload: map[string]any{
				"finding_id":        id,
				"convergence_count": cand["convergence_count"],
			},
			Wave: state.LatestWave,
		})
	}
	return signals
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

func int64Ptr(v int64) *int64 { return &v }

func intPtrFromAny(v any) *int {
	switch n := v.(type) {
	case int64:
		i := int(n)
		return &i
	case float64:
		i := int(n)
		return &i
	case int:
		return &n
	}
	return nil
}

// coord4Key renders a gap coordinate as a sortable string so waggle-dance
// target selection is stable across runs. NULL renders as "~", which sorts
// after the zero-padded digits of any value.
func coord4Key(c struct{ d1, d2, d3, d4 any }) string {
	part := func(v any) string {
		if v == nil {
			return "~"
		}
		return fmt.Sprintf("%020v", v)
	}
	return part(c.d1) + "|" + part(c.d2) + "|" + part(c.d3) + "|" + part(c.d4)
}
