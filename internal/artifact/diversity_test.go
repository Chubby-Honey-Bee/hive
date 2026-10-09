package artifact_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// wantDiversity restates the diversity rule over the lenses that returned
// a verdict: fewer than two, not_checked; two or more verdicts or recorded
// models, not_low; else a lens with no recorded model, unknown; else low.
func wantDiversity(verdicts, models map[string]string) string {
	vs, ms := map[string]bool{}, map[string]bool{}
	unrecorded := false
	for f, v := range verdicts {
		vs[v] = true
		if models[f] == "" {
			unrecorded = true
		} else {
			ms[models[f]] = true
		}
	}
	switch {
	case len(verdicts) < 2:
		return "not_checked"
	case len(vs) > 1 || len(ms) > 1:
		return "not_low"
	case unrecorded:
		return "unknown"
	}
	return "low"
}

// The artifact records the run's lens diversity in the state the restated
// rule gives, so an unknown stays unknown rather than reading as not low,
// and the state survives writing and verifying.
func TestBuildArtifact_Diversity(t *testing.T) {
	const defn = `name: swarm
nodes:
  forager-a: {type: agent, prompt: a}
  forager-b: {type: agent, prompt: b}
  forager-c: {type: agent, prompt: c}
  queen: {type: agent, prompt: q}
`
	cases := []struct {
		name     string
		verdicts map[string]string
		models   map[string]string
	}{
		{"one model, unanimous", map[string]string{"a": "support", "b": "support", "c": "support"},
			map[string]string{"a": "qwen3.5:4b", "b": "qwen3.5:4b", "c": "qwen3.5:4b"}},
		{"two models, unanimous", map[string]string{"a": "support", "b": "support", "c": "support"},
			map[string]string{"a": "qwen3.5:4b", "b": "ministral-3:8b", "c": "qwen3.5:4b"}},
		{"one model, split", map[string]string{"a": "support", "b": "oppose", "c": "support"},
			map[string]string{"a": "qwen3.5:4b", "b": "qwen3.5:4b", "c": "qwen3.5:4b"}},
		{"a model unrecorded", map[string]string{"a": "support", "b": "support", "c": "support"},
			map[string]string{"a": "qwen3.5:4b", "b": "qwen3.5:4b"}},
		{"one lens answered", map[string]string{"a": "oppose"},
			map[string]string{"a": "qwen3.5:4b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newTestStore(t)
			repo := store.Workflows()
			runID, err := repo.CreateWorkflowRun("swarm", 1, defn, `{"question":"q"}`, []db.NodeSeed{
				{Name: "forager-a", Type: "agent"}, {Name: "forager-b", Type: "agent"}, {Name: "forager-c", Type: "agent"}, {Name: "queen", Type: "agent"},
			})
			if err != nil {
				t.Fatal(err)
			}
			for f, v := range c.verdicts {
				out, _ := json.Marshal(map[string]string{"verdict": v, "recommendation": "r"})
				if err := repo.MarkNodeCompleted(runID, "forager-"+f, string(out), "2026-09-28T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
				if m := c.models[f]; m != "" {
					if err := repo.UpdateNodeResolvedModel(runID, "forager-"+f, m); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := wantDiversity(c.verdicts, c.models)

			art, err := artifact.BuildArtifact(store, runID, artifact.Determinism{})
			if err != nil {
				t.Fatal(err)
			}
			if art.Diversity != want || art.SchemaVersion != "1.4" {
				t.Fatalf("diversity %q, schema %s; want %q and 1.4", art.Diversity, art.SchemaVersion, want)
			}
			path := filepath.Join(t.TempDir(), "artifact.json")
			if err := art.WriteTo(path); err != nil {
				t.Fatal(err)
			}
			raw, _ := art.Encode()
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil || decoded["diversity"] != want {
				t.Errorf("the encoding holds diversity %v (%v), want %q", decoded["diversity"], err, want)
			}
			if ok, _, _, err := artifact.VerifyArtifact(path); !ok || err != nil {
				t.Errorf("the artifact does not verify: %v", err)
			}
		})
	}
}
