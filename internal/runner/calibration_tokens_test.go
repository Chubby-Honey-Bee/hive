package runner

import (
	"fmt"
	"strings"
	"testing"

	calib "github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// queenPrompt renders the swarm workflow for three lenses and a dreamer
// and returns its definition and the Queen's prompt as the engine loads it.
func queenPrompt(t *testing.T, synth foragers.Forager) (map[string]any, string) {
	t.Helper()
	swarm := []foragers.Forager{
		{Name: "optimist", Title: "The Optimist", Description: "x", Body: "y", Bonds: []foragers.Bond{{To: "skeptic", Kind: foragers.BondResonates}}},
		{Name: "skeptic", Title: "The Skeptic", Description: "x", Body: "y"},
		{Name: "steward", Title: "The Steward", Description: "x", Body: "y"},
		{Name: "dreamer", Title: "The Dreamer", Description: "x", Body: "y", Archetype: foragers.ArchetypeDreamer},
	}
	yamlText, err := foragers.GenerateWorkflow(swarm, foragers.WorkflowOptions{Name: "n", Synthesizer: synth})
	if err != nil {
		t.Fatalf("GenerateWorkflow: %v", err)
	}
	defn, err := workflow.LoadYAMLString(yamlText)
	if err != nil {
		t.Fatal(err)
	}
	queen, _ := defn["nodes"].(map[string]any)["queen"].(map[string]any)
	prompt, _ := queen["prompt"].(string)
	return defn, prompt
}

// seedLens records n outcomes for a lens, hits of them confirmed.
func seedLens(t *testing.T, store *db.Store, lens string, n, hits int) {
	t.Helper()
	run, err := store.Workflows().CreateWorkflowRun("tok", 1, "name: t", "{}", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		res := calib.Confirmed
		if i >= hits {
			res = calib.Refuted
		}
		if _, err := calib.Record(store, calib.Outcome{SubjectKind: calib.SubjectLensVerdict, Lens: lens, RunID: run, Resolution: res, Source: calib.SourceHuman}); err != nil {
			t.Fatal(err)
		}
	}
}

// With no calibrated lens the Queen's resolved prompt is byte-identical to
// the prompt without the token, in both her prompts.
func TestResolveCalibrationTokens_NoCalibratedLens_LeavesThePromptAsItWas(t *testing.T) {
	store := newTempStore(t)
	persona := foragers.Forager{Name: "queen", Title: "Queen", Description: "x", Body: "QUEEN PERSONA", Archetype: foragers.ArchetypeSynthesizer}
	for _, synth := range []foragers.Forager{{}, persona} {
		defn, prompt := queenPrompt(t, synth)
		if strings.Count(prompt, "{calibration.lenses}") != 1 {
			t.Fatalf("persona %q: the queen prompt holds the token %d times:\n%s", synth.Name, strings.Count(prompt, "{calibration.lenses}"), prompt)
		}
		got, left := resolveCalibrationTokens(prompt, nil, store, defn)
		if want := strings.Replace(prompt, "{calibration.lenses}", "", 1); got != want {
			t.Fatalf("persona %q: resolved prompt differs from today's:\n%q\n---\n%q", synth.Name, got, want)
		}
		for _, p := range left {
			if p.Token == "{calibration.lenses}" {
				t.Fatalf("the token was left unresolved")
			}
		}
	}

	// Nine outcomes are under the floor: still today's prompt.
	seedLens(t, store, "skeptic", 9, 9)
	if _, err := calib.Recompute(store, calib.Options{Foragers: []string{"skeptic", "optimist", "steward"}}); err != nil {
		t.Fatal(err)
	}
	defn, prompt := queenPrompt(t, foragers.Forager{})
	if got, _ := resolveCalibrationTokens(prompt, nil, store, defn); got != strings.Replace(prompt, "{calibration.lenses}", "", 1) {
		t.Fatalf("nine outcomes changed the prompt:\n%s", got)
	}
}

// With a calibrated lens the token renders every lens of the swarm: the
// calibrated one with its weight and n, the one under the floor and the one
// with no outcome as uncalibrated, and never the dreamer.
func TestResolveCalibrationTokens_RendersTheSwarmsTrackRecords(t *testing.T) {
	store := newTempStore(t)
	seedLens(t, store, "skeptic", 20, 15)
	seedLens(t, store, "optimist", 4, 4)
	if _, err := calib.Recompute(store, calib.Options{Foragers: []string{"skeptic", "optimist", "steward"}}); err != nil {
		t.Fatal(err)
	}
	defn, prompt := queenPrompt(t, foragers.Forager{})
	got, _ := resolveCalibrationTokens(prompt, nil, store, defn)
	if strings.Contains(got, "{calibration.lenses}") {
		t.Fatalf("token left in the prompt:\n%s", got)
	}
	skeptic := calib.Formula(calib.Counts{Confirmed: 15, Refuted: 5}, calib.Counts{Confirmed: 19, Refuted: 5}.PHat())
	for _, want := range []string{
		"model's view, not independent ones.\n\nLens track records, from the outcomes ledger. ",
		calib.CorrelationalNote,
		fmt.Sprintf("  skeptic: weight %.2f (hit rate 0.75, n=20)", skeptic.W),
		"  optimist: uncalibrated (n=4 < 10)",
		"  steward: uncalibrated (no outcomes)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("resolved prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "optimist: weight") || strings.Contains(got, "dreamer") {
		t.Fatalf("an uncalibrated lens got a weight, or the dreamer was listed:\n%s", got)
	}
}
