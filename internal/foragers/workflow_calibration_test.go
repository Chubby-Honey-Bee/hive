package foragers

import (
	"strings"
	"testing"
)

// The Queen's prompt carries {calibration.lenses} once, in both her prompts
// (the persona and the inlined fallback), after the diversity paragraph
// and with nothing between the paragraph's last character and the token, so
// the runner's empty resolution leaves the prompt byte-identical to one
// without it. No other node reads it: the lenses would be weighing
// themselves.
func TestGenerateWorkflow_QueenReadsTheLensTrackRecords(t *testing.T) {
	persona := Forager{Name: "queen", Title: "Queen", Description: "x", Body: "QUEEN PERSONA", Archetype: ArchetypeSynthesizer}
	for _, synth := range []Forager{{}, persona} {
		for _, eval := range []bool{false, true} {
			nodes := swarmNodes(t, WorkflowOptions{Name: "n", Evaluate: eval, Synthesizer: synth})
			for name, node := range nodes {
				prompt, _ := node["prompt"].(string)
				if prompt == "" {
					prompt, _ = node["prompt_template"].(string)
				}
				n := strings.Count(prompt, "{calibration.lenses}")
				if name == "queen" {
					if n != 1 {
						t.Errorf("persona %q eval %v: queen holds the token %d times", synth.Name, eval, n)
					}
					if !strings.Contains(prompt, "not independent ones.{calibration.lenses}") {
						t.Errorf("persona %q eval %v: the token is not glued to the diversity paragraph:\n%s", synth.Name, eval, prompt)
					}
					continue
				}
				if n != 0 {
					t.Errorf("persona %q eval %v: %s reads {calibration.lenses}", synth.Name, eval, name)
				}
			}
		}
	}
}
