package hive

import "fmt"

// CheckTermination checks if the project has reached a terminal state.
//
// Terminal when:
// - No critical or important gaps remain
// - No unresolved conflicts
// - MSS audit passes
// - Latest evaluation is COMPLETE
// - At least one wave gate is open
func CheckTermination(state *State) (bool, string) {
	for _, unmet := range terminationChecks {
		if reason := unmet(state); reason != "" {
			return false, reason
		}
	}
	return true, "All conditions met"
}

// terminationChecks are the conditions a terminal state meets, in the order
// they are checked. Each returns why its condition is not met, or "".
var terminationChecks = []func(*State) string{
	criticalGapsRemain,
	importantGapsRemain,
	conflictsRemain,
	auditFails,
	noCompleteEvaluation,
	noWaveGate,
}

// criticalGapsRemain reports open critical gaps.
func criticalGapsRemain(state *State) string {
	if len(state.CriticalGaps) > 0 {
		return "Critical gaps remain"
	}
	return ""
}

// importantGapsRemain reports open gaps of critical or important priority.
func importantGapsRemain(state *State) string {
	if n := importantGapCount(state.UnresolvedGaps); n > 0 {
		return fmt.Sprintf("%d important+ gaps remain", n)
	}
	return ""
}

// importantGapCount counts the gaps of critical or important priority.
func importantGapCount(gaps []map[string]any) int {
	n := 0
	for _, g := range gaps {
		p, _ := g["priority"].(string)
		if p == "critical" || p == "important" {
			n++
		}
	}
	return n
}

// conflictsRemain reports unresolved conflicts.
func conflictsRemain(state *State) string {
	if len(state.UnresolvedConflicts) > 0 {
		return fmt.Sprintf("%d unresolved conflicts", len(state.UnresolvedConflicts))
	}
	return ""
}

// auditFails reports an MSS audit that does not pass.
func auditFails(state *State) string {
	if state.MSSIntegrity != "PASS" {
		return "MSS audit fails"
	}
	return ""
}

// noCompleteEvaluation reports a latest evaluation that is missing or not
// COMPLETE.
func noCompleteEvaluation(state *State) string {
	if verdict, _ := state.LatestEval["verdict"].(string); verdict != "COMPLETE" {
		return "No COMPLETE evaluation"
	}
	return ""
}

// noWaveGate reports that no wave gate is open.
func noWaveGate(state *State) string {
	if len(state.WaveGates) == 0 {
		return "No wave gates open"
	}
	return ""
}
