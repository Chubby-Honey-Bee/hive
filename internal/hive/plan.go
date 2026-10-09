package hive

import (
	"fmt"
	"math"
	"unicode/utf8"
)

// Action represents a single dispatch action in the plan.
type Action struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Priority     string         `json:"priority"`
	SignalType   string         `json:"signal_type"`
	Agent        string         `json:"agent,omitempty"`
	Model        string         `json:"model,omitempty"`
	Wave         int            `json:"wave,omitempty"`
	TargetCoords map[string]any `json:"target_coords,omitempty"`
	Prompt       string         `json:"prompt,omitempty"`
	FindingID    int64          `json:"finding_id,omitempty"`
	Params       map[string]any `json:"params,omitempty"`
	Payload      map[string]any `json:"payload,omitempty"`
	Description  string         `json:"description"`
}

// GeneratePlan converts signals into a prioritized dispatch plan.
//
// Priority ordering is encoded in planSteps (see plan_steps.go); each
// entry is a stepHandler that emits zero or more actions for one slot in
// the bee-colony pipeline. Adding a new behavior == appending to planSteps;
// no edits to GeneratePlan itself (Open/Closed).
//
// Priority order:
//  1. QMP violations          5. Tremble dance
//  2. Alarm cascades          6. Shaking signal
//  3. Conflict resolution     7. Quorum capping
//  4. Gap filling             8. Gate check
//
// Dispatches go to the wave after the latest one, so a latest wave that
// cannot be incremented is refused rather than wrapped to a negative wave.
func GeneratePlan(state *State, signals []Signal) ([]Action, error) {
	if state.LatestWave == math.MaxInt {
		return nil, fmt.Errorf("latest wave %d cannot be incremented: the plan dispatches to the next wave", state.LatestWave)
	}
	c := &planContext{
		state:      state,
		wave:       state.LatestWave,
		batchSize:  state.Hive.BatchSize,
		suppressed: collectSuppressed(signals, state.PendingSignals),
		// An empty plan is [], not null, in `chb hive next`.
		actions: []Action{},
	}
	for _, step := range planSteps {
		step(c, signals)
	}
	return c.actions, nil
}

// collectSuppressed gathers stop_signal targets from both the freshly
// computed signals and any unacted PendingSignals from the DB.
func collectSuppressed(fresh []Signal, pending []map[string]any) map[planCoord]bool {
	out := make(map[planCoord]bool)
	combined := append([]Signal{}, fresh...)
	combined = append(combined, signalsFromMaps(pending)...)
	for _, sig := range combined {
		if sig.SignalType == "stop_signal" {
			out[coordOf(sig.TargetD1, sig.TargetD2, sig.TargetD3, sig.TargetD4)] = true
		}
	}
	return out
}

func filterSignals(signals []Signal, sigType string) []Signal {
	var out []Signal
	for _, s := range signals {
		if s.SignalType == sigType {
			out = append(out, s)
		}
	}
	return out
}

func signalsFromMaps(maps []map[string]any) []Signal {
	var out []Signal
	for _, m := range maps {
		out = append(out, Signal{
			SignalType: anyToStr(m["signal_type"]),
			TargetD1:   intPtrFromAny(m["target_d1"]),
			TargetD2:   intPtrFromAny(m["target_d2"]),
			TargetD3:   intPtrFromAny(m["target_d3"]),
			TargetD4:   intPtrFromAny(m["target_d4"]),
		})
	}
	return out
}

// clip returns s truncated to at most n characters. Unlike runner.truncate
// it does NOT append an ellipsis — used for prompt/description fields where
// trailing punctuation would change agent behavior.
// clip shortens s to at most n bytes without splitting a UTF-8 sequence,
// which a plain byte slice of a non-ASCII gap description did.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func anyToStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func mapGet(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		return anyToStr(v)
	}
	return ""
}
