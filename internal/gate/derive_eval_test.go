package gate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The verdict is derived, never taken from the evaluator: COMPLETE exactly
// when no gap is named and every one of the four scores is at least 4.
func TestDeriveEvaluation_Verdict(t *testing.T) {
	scoreSets := [][4]int{{4, 4, 4, 4}, {5, 5, 5, 5}, {3, 5, 5, 5}, {5, 5, 5, 1}}
	gapSets := [][]string{{}, {"which regions?"}}
	for _, scores := range scoreSets {
		for _, gaps := range gapSets {
			in := map[string]any{"gaps": gaps, "mss_integrity": 4}
			allHigh := true
			for i, key := range derivedScoreKeys {
				in[key] = scores[i]
				if scores[i] < 4 {
					allHigh = false
				}
			}
			want := "NEEDS_MORE_WORK"
			if allHigh && len(gaps) == 0 {
				want = "COMPLETE"
			}
			raw, _ := json.Marshal(in)
			eval, gotGaps, err := DeriveEvaluation(raw)
			if err != nil {
				t.Fatalf("%s: %v", raw, err)
			}
			if eval["verdict"] != want {
				t.Errorf("%s: verdict %v, want %s", raw, eval["verdict"], want)
			}
			if _, has := eval["gaps"]; has {
				t.Errorf("%s: gaps left in the evaluation handed to the gate", raw)
			}
			if eval["mss_integrity"] != float64(4) {
				t.Errorf("%s: mss_integrity %v not passed through", raw, eval["mss_integrity"])
			}
			if !reflect.DeepEqual(gotGaps, gaps) {
				t.Errorf("%s: gaps %q, want %q", raw, gotGaps, gaps)
			}
		}
	}
}

// Input that would let a verdict slip through is refused.
func TestDeriveEvaluation_Refusals(t *testing.T) {
	cases := map[string]string{
		`{"coverage":4,"depth":4,"sources":4,"actionability":4,"gaps":[],"verdict":"COMPLETE"}`: "carries a verdict",
		`{"coverage":4,"depth":4,"sources":4,"gaps":[]}`:                                        "actionability must be a whole number",
		`{"coverage":4.5,"depth":4,"sources":4,"actionability":4,"gaps":[]}`:                    "coverage must be a whole number",
		`{"coverage":"4","depth":4,"sources":4,"actionability":4,"gaps":[]}`:                    "coverage must be a whole number",
		`{"coverage":6,"depth":4,"sources":4,"actionability":4,"gaps":[]}`:                      "coverage must be a whole number",
		`{"coverage":4,"depth":4,"sources":4,"actionability":4}`:                                "gaps must be a list",
		`{"coverage":4,"depth":4,"sources":4,"actionability":4,"gaps":"none"}`:                  "gaps must be a list",
		`{"coverage":4,"depth":4,"sources":4,"actionability":4,"gaps":[" "]}`:                   "gaps[0] is not a non-empty string",
		`{"coverage":4,"depth":4,"sources":4,"actionability":4,"mss_integrity":7,"gaps":[]}`:    "mss_integrity must be a whole number",
		`[1,2]`: "not one JSON object",
		`null`:  "not one JSON object",
	}
	for in, want := range cases {
		if _, _, err := DeriveEvaluation([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}
