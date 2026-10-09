package artifact_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// The artifact records the run's calibration as RunCalibration reads it,
// under a key the canonical encoding writes before the synthesis, and the
// file verifies. An artifact without one encodes without the key, so an
// older file still re-encodes to its own bytes.
func TestBuildArtifact_Calibration(t *testing.T) {
	const defn = `name: swarm
resonates:
  - [a, b]
nodes:
  forager-a: {type: agent, prompt: a}
  forager-b: {type: agent, prompt: b}
  forager-c: {type: agent, prompt: c}
  queen: {type: agent, prompt: q}
`
	store := newTestStore(t)
	repo := store.Workflows()
	runID, err := repo.CreateWorkflowRun("swarm", 1, defn, `{"question":"q"}`, []db.NodeSeed{
		{Name: "forager-a", Type: "agent"}, {Name: "forager-b", Type: "agent"}, {Name: "forager-c", Type: "agent"}, {Name: "queen", Type: "agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for f, v := range map[string]string{"a": "oppose", "b": "oppose", "c": "support"} {
		out, _ := json.Marshal(map[string]string{"verdict": v, "recommendation": "r"})
		if err := repo.MarkNodeCompleted(runID, "forager-"+f, string(out), "2026-10-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	queen, _ := json.Marshal(map[string]any{"report": "## r", "convergence": "medium", "coverage": 3, "gaps": []string{}, "dissent_from_plurality": "", "verdict": "oppose", "recommendation": "hold"})
	if err := repo.MarkNodeCompleted(runID, "queen", string(queen), "2026-10-01T00:00:01Z"); err != nil {
		t.Fatal(err)
	}

	art, err := artifact.BuildArtifact(store, runID, artifact.Determinism{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := workflow.RunCalibration(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if art.Calibration == nil || art.SchemaVersion != "1.4" {
		t.Fatalf("calibration %v, schema %s; want one and 1.4", art.Calibration, art.SchemaVersion)
	}
	if got := *art.Calibration; got.Convergence != "medium" || got.Plurality != "oppose" || got.Margin != 1 || got.Votes != 3 || got.DissentWritten || !reflect.DeepEqual(got.NablaFired, []string{"a↔b (oppose)"}) || got.Tally != want.Tally {
		t.Errorf("calibration %+v, want convergence medium, plurality oppose by 1 of 3 votes, no dissent, a↔b fired, tally %q", got, want.Tally)
	}
	raw, err := art.Encode()
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if i, j := strings.Index(text, `"calibration"`), strings.Index(text, `"synthesis"`); i < 0 || j < 0 || i > j {
		t.Errorf("the encoding writes calibration at %d and synthesis at %d; want calibration first", i, j)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	cal, _ := decoded["calibration"].(map[string]any)
	if cal["convergence"] != "medium" || cal["plurality"] != "oppose" || cal["margin"] != float64(1) || cal["dissent_written"] != false || cal["queen_status"] != "completed" {
		t.Errorf("encoded calibration %v", cal)
	}
	path := filepath.Join(t.TempDir(), "artifact.json")
	if err := art.WriteTo(path); err != nil {
		t.Fatal(err)
	}
	if ok, _, _, err := artifact.VerifyArtifact(path); !ok || err != nil {
		t.Errorf("the artifact does not verify: %v", err)
	}

	art.Calibration = nil
	raw, err = art.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "calibration") {
		t.Errorf("an artifact without a calibration still encodes the key:\n%s", raw)
	}
}
