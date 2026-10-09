package gate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// derivedScoreKeys are the scores an evaluation must carry for chb to
// derive its verdict.
var derivedScoreKeys = []string{"coverage", "depth", "sources", "actionability"}

// DeriveEvaluation reads an evaluator's scores and named gaps, as `chb guard
// --eval-stdin` receives them, and derives the verdict itself. The verdict is
// COMPLETE when the evaluator names no gap and scores each of coverage,
// depth, sources and actionability at least 4, and NEEDS_MORE_WORK
// otherwise. That rule is a definition. An evaluator that also states a
// verdict could state one its own gaps contradict, so an input carrying
// `verdict` is refused. It returns the evaluation for RunGatePipeline, with
// the derived verdict and without the gaps, and the gaps. It refuses input
// that is not one JSON object, a score that is missing or not a whole number
// from 1 to 5 (mss_integrity may be absent), and `gaps` missing or not a list
// of non-empty strings, so a forgotten key cannot read as "no gaps".
func DeriveEvaluation(raw []byte) (map[string]any, []string, error) {
	in, err := evaluationObject(raw)
	if err != nil {
		return nil, nil, err
	}
	complete, gaps, err := verdictInputs(in)
	if err != nil {
		return nil, nil, err
	}
	return derivedEvaluation(in, complete && len(gaps) == 0), gaps, nil
}

// evaluationObject decodes the evaluator's input: one JSON object that
// states no verdict.
func evaluationObject(raw []byte) (map[string]any, error) {
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil || in == nil {
		return nil, errors.New("evaluation is not one JSON object")
	}
	if _, has := in["verdict"]; has {
		return nil, errors.New("evaluation carries a verdict; chb derives it from the gaps and scores")
	}
	return in, nil
}

// verdictInputs checks the scores and the gaps, and reports whether every
// required score is at least 4.
func verdictInputs(in map[string]any) (bool, []string, error) {
	complete, err := requiredScoresComplete(in)
	if err != nil {
		return false, nil, err
	}
	// mss_integrity is optional; checked here too, so a bad one is refused
	// before the gaps are written rather than by the gate after.
	if err := checkMSSIntegrityScore(in["mss_integrity"]); err != nil {
		return false, nil, err
	}
	gaps, err := evaluationGaps(in["gaps"])
	return complete, gaps, err
}

// requiredScoresComplete checks each of derivedScoreKeys and reports
// whether all of them are at least 4.
func requiredScoresComplete(in map[string]any) (bool, error) {
	lowest := 5.0
	for _, key := range derivedScoreKeys {
		n, err := requiredScore(in, key)
		if err != nil {
			return false, err
		}
		lowest = min(lowest, n)
	}
	return lowest >= 4, nil
}

// requiredScore is a score that must be present as a whole number from 1
// to 5.
func requiredScore(in map[string]any, key string) (float64, error) {
	n, ok := in[key].(float64)
	if !ok || !wholeScore(n) {
		return 0, fmt.Errorf("evaluation %s must be a whole number from 1 to 5, got %v", key, in[key])
	}
	return n, nil
}

// checkMSSIntegrityScore refuses an mss_integrity that is present and not a
// whole number from 1 to 5.
func checkMSSIntegrityScore(v any) error {
	if v == nil {
		return nil
	}
	if n, ok := v.(float64); !ok || !wholeScore(n) {
		return fmt.Errorf("evaluation mss_integrity must be a whole number from 1 to 5, got %v", v)
	}
	return nil
}

// evaluationGaps reads gaps: a list of non-empty strings, trimmed.
func evaluationGaps(v any) ([]string, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("evaluation gaps must be a list, got %v", v)
	}
	gaps := make([]string, 0, len(list))
	for i, g := range list {
		s, ok := gapText(g)
		if !ok {
			return nil, fmt.Errorf("evaluation gaps[%d] is not a non-empty string", i)
		}
		gaps = append(gaps, s)
	}
	return gaps, nil
}

// gapText is a gap trimmed, and whether it is a non-empty string.
func gapText(g any) (string, bool) {
	s, ok := g.(string)
	s = strings.TrimSpace(s)
	return s, ok && s != ""
}

// derivedEvaluation is the evaluation RunGatePipeline records: the input
// less its gaps, with the derived verdict.
func derivedEvaluation(in map[string]any, complete bool) map[string]any {
	eval := make(map[string]any, len(in))
	for k, v := range in {
		if k != "gaps" {
			eval[k] = v
		}
	}
	eval["verdict"] = "NEEDS_MORE_WORK"
	if complete {
		eval["verdict"] = "COMPLETE"
	}
	return eval
}
