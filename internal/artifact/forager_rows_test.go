package artifact_test

import (
	"database/sql"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Forager vantages seeded the way the runner writes them, one fenced and one
// bare, give the artifact its foragers and their parsed verdicts.
func TestBuildArtifact_ForagerVantagesPopulateForagersAndVerdicts(t *testing.T) {
	store := newTestStore(t)
	runID := seedStore(t, store, []string{"optimist", "skeptic", "queen"})
	for name, verdict := range map[string]string{"optimist": "support", "skeptic": "oppose"} {
		row := &db.CombRow{
			VantageKey:   "forager:" + name,
			VantageKind:  db.VantageForager,
			Narrative:    "digest for " + name,
			Confidence:   3,
			DigestMethod: "forager",
			// skeptic's final text is fenced the way haiku returns it; optimist's is bare.
			RawJSON: sql.NullString{String: fence(name, `{"forager":"`+name+`","verdict":"`+verdict+`","key_points":["k"],"evidence":["e"],"uncertainties":["u"],"recommendation":"r"}`), Valid: true},
		}
		if err := store.Comb().Upsert(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Comb().Upsert(&db.CombRow{
		VantageKey: "forager:queen", VantageKind: db.VantageForager, Narrative: "queen digest", Confidence: 3, DigestMethod: "forager",
		RawJSON: sql.NullString{String: `{"verdict":"conditional","convergence":"partial","recommendation":"ship behind a flag"}`, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	art, err := artifact.BuildArtifact(store, runID, artifact.Determinism{})
	if err != nil {
		t.Fatal(err)
	}
	if len(art.Foragers) != 3 {
		t.Fatalf("Foragers = %v, want optimist, queen, skeptic", art.Foragers)
	}
	v, _ := art.Verdicts["skeptic"].(map[string]any)
	if v["verdict"] != "oppose" || v["forager"] != "skeptic" {
		t.Fatalf("skeptic verdict not carried from raw_json: %v", art.Verdicts["skeptic"])
	}
	if art.Synthesis["verdict"] != "conditional" || art.Synthesis["recommendation"] != "ship behind a flag" {
		t.Fatalf("queen synthesis not carried from raw_json: %v", art.Synthesis)
	}
	if art.Models["optimist"] == "" || art.Providers["optimist"] == "" {
		t.Fatalf("models/providers not resolved from the node records: %v / %v", art.Models, art.Providers)
	}
}

func fence(name, body string) string {
	if name == "skeptic" {
		return "```json\n" + body + "\n```"
	}
	return body
}

// A forager vantage another run left in the database is not this run's.
func TestBuildArtifact_OmitsOtherRunsForagers(t *testing.T) {
	store := newTestStore(t)
	runID := seedStore(t, store, []string{"optimist", "skeptic", "queen"})
	if err := store.Comb().Upsert(&db.CombRow{
		VantageKey: "forager:historian", VantageKind: db.VantageForager, Narrative: "an earlier run", Confidence: 80, DigestMethod: "forager",
	}); err != nil {
		t.Fatal(err)
	}
	art, err := artifact.BuildArtifact(store, runID, artifact.Determinism{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range art.Foragers {
		if f == "historian" {
			t.Fatalf("Foragers = %v includes a forager this run has no node for", art.Foragers)
		}
	}
	for _, c := range art.Comb {
		if c.VantageKey == "forager:historian" {
			t.Fatal("Comb snapshot carries another run's forager vantage")
		}
	}
}
