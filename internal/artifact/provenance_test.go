package artifact_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
)

// TestBuildArtifact_RecordsTheSynthesizer: the synthesis node's model,
// provider and endpoint come from its node row.
func TestBuildArtifact_RecordsTheSynthesizer(t *testing.T) {
	store := newTestStore(t)
	runID := seedStore(t, store, []string{"optimist", "skeptic", "queen"})
	const model, provider, base = "qwen3.6:35b-a3b-q4_K_M", "openai", "http://127.0.0.1:11434/v1"
	if err := store.Workflows().UpdateNodeResolvedModel(runID, "queen", model); err != nil {
		t.Fatal(err)
	}
	if err := store.Workflows().UpdateNodeMetrics(runID, "queen", 0, 0, 0, provider, base, 0, 1); err != nil {
		t.Fatal(err)
	}
	art, err := artifact.BuildArtifact(store, runID, artifact.Determinism{})
	if err != nil {
		t.Fatal(err)
	}
	want := artifact.Synthesizer{Model: model, Provider: provider, BaseURL: base}
	if art.Synthesizer == nil || *art.Synthesizer != want {
		t.Fatalf("synthesizer = %+v, want %+v", art.Synthesizer, want)
	}

	// A run with no synthesis node records none.
	store2 := newTestStore(t)
	run2 := seedStore(t, store2, []string{"optimist"})
	if _, err := store2.WriteDB.Exec(`DELETE FROM workflow_node_states WHERE run_id=? AND node_name='queen'`, run2); err != nil {
		t.Fatal(err)
	}
	art2, err := artifact.BuildArtifact(store2, run2, artifact.Determinism{})
	if err != nil {
		t.Fatal(err)
	}
	if art2.Synthesizer != nil {
		t.Errorf("synthesizer = %+v, want none", art2.Synthesizer)
	}
}

// TestVerifyArtifact_A10FileVerifies: a schema 1.0 file, which has no
// base_urls, synthesizer or determinism.top_p, verifies. It is built here in
// 1.0's layout: its keys sorted, indented two spaces, hashed with sha256
// blank.
func TestVerifyArtifact_A10FileVerifies(t *testing.T) {
	old := map[string]any{
		"schema_version": "1.0",
		"question":       "Is the artifact deterministic?",
		"determinism":    map[string]any{"enabled": true, "temperature": 0.0, "seed": 42},
		"foragers":       []any{"optimist"},
		"models":         map[string]any{"optimist": "claude-haiku-4-5-20251001"},
		"providers":      map[string]any{"optimist": "anthropic"},
		"verdicts":       map[string]any{"optimist": map[string]any{"verdict": "yes", "confidence": 90}},
		"synthesis":      map[string]any{"synthesis": "balanced"},
		"comb":           []any{},
		"sha256":         "",
	}
	blank, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	old["sha256"] = fmt.Sprintf("%x", sha256.Sum256(blank))
	data, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "v1.0.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	matched, expected, actual, err := artifact.VerifyArtifact(path)
	if err != nil || !matched {
		t.Fatalf("a 1.0 file does not verify: matched=%v expected=%s actual=%s err=%v", matched, expected, actual, err)
	}
}

// TestBuildArtifact_RecordsEndpointAndTopP: each forager's base URL comes from
// its node row, and the sampling settings the caller passes are recorded.
func TestBuildArtifact_RecordsEndpointAndTopP(t *testing.T) {
	store := newTestStore(t)
	runID := seedStore(t, store, []string{"optimist", "skeptic", "queen"})
	endpoints := map[string]string{
		"optimist": "http://127.0.0.1:11434/v1",
		"skeptic":  "",
	}
	for node, base := range endpoints {
		if err := store.Workflows().UpdateNodeMetrics(runID, node, 0, 0, 0, "", base, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	temp, topP := 0.6, 0.9
	art, err := artifact.BuildArtifact(store, runID, artifact.Determinism{Temperature: &temp, TopP: &topP})
	if err != nil {
		t.Fatal(err)
	}
	for node, base := range endpoints {
		if got, ok := art.BaseURLs[node]; !ok || got != base {
			t.Errorf("base_urls[%s] = %q (present %v), want %q", node, got, ok, base)
		}
	}
	if art.Determinism.TopP == nil || *art.Determinism.TopP != topP || *art.Determinism.Temperature != temp {
		t.Errorf("determinism = %+v, want temperature %v and top_p %v", art.Determinism, temp, topP)
	}
}
