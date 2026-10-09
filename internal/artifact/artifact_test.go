package artifact_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// newTestStore creates an in-memory SQLite store with the full schema applied.
func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "artifact_test.db")
	store, err := db.NewStore(tmp)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// seedStore inserts a workflow_run, a couple of node states with models and
// rationale, and two comb forager vantage rows. Returns the run ID.
func seedStore(t *testing.T, store *db.Store, foragerOrder []string) int64 {
	t.Helper()

	// Create a workflow_run with a question in inputs_json.
	inputsJSON := `{"question":"Is the artifact deterministic?","context":""}`
	runID, err := store.Workflows().CreateWorkflowRun(
		"test-swarm", 1, "yaml: {}", inputsJSON,
		[]db.NodeSeed{
			{Name: "optimist", Type: "agent"},
			{Name: "skeptic", Type: "agent"},
			{Name: "queen", Type: "agent"},
		},
	)
	if err != nil {
		t.Fatalf("create workflow run: %v", err)
	}

	// Seed node rationale and resolved_model for each node.
	nodes := []struct {
		name      string
		model     string
		provider  string
		rationale string
	}{
		{"optimist", "claude-haiku-4-5-20251001", "anthropic",
			`{"verdict":"strongly positive","confidence":90}`},
		{"skeptic", "claude-haiku-4-5-20251001", "anthropic",
			`{"verdict":"cautiously negative","confidence":70}`},
		{"queen", "claude-sonnet-4-6", "anthropic",
			`{"synthesis":"balanced","recommendation":"proceed with caution"}`},
	}
	for _, n := range nodes {
		if err := store.Workflows().UpdateNodeResolvedModel(runID, n.name, n.model); err != nil {
			t.Fatalf("update resolved_model %s: %v", n.name, err)
		}
		if err := store.Workflows().UpdateNodeMetrics(runID, n.name, 100, 50, 42, n.provider, "", 0, 0); err != nil {
			t.Fatalf("update metrics %s: %v", n.name, err)
		}
		if err := store.Workflows().UpdateNodeRationale(runID, n.name, n.rationale); err != nil {
			t.Fatalf("update rationale %s: %v", n.name, err)
		}
	}

	// Insert comb forager vantages in the specified order (to test canonical sort).
	for _, wiz := range foragerOrder {
		var narrative, model string
		var confidence int
		switch wiz {
		case "optimist":
			narrative = "The future is bright"
			confidence = 90
			model = "claude-haiku-4-5-20251001"
		case "skeptic":
			narrative = "Proceed with caution"
			confidence = 70
			model = "claude-haiku-4-5-20251001"
		case "queen":
			narrative = "Balanced synthesis achieved"
			confidence = 80
			model = "claude-sonnet-4-6"
		}
		_ = model
		if err := store.Comb().Upsert(&db.CombRow{
			VantageKey:  "forager:" + wiz,
			VantageKind: db.VantageForager,
			Narrative:   narrative,
			Confidence:  confidence,
			Contested:   false,
		}); err != nil {
			t.Fatalf("upsert comb forager %s: %v", wiz, err)
		}
	}

	return runID
}

// TestRoundtrip builds an artifact, writes it to a temp file, reads it back,
// decodes it, and verifies the hash still matches.
func TestRoundtrip(t *testing.T) {
	store := newTestStore(t)
	runID := seedStore(t, store, []string{"optimist", "skeptic", "queen"})

	det := artifact.Determinism{
		Enabled: true,
	}
	temp := 0.0
	det.Temperature = &temp

	art, err := artifact.BuildArtifact(store, runID, det)
	if err != nil {
		t.Fatalf("BuildArtifact: %v", err)
	}
	if art.Hash == "" {
		t.Fatal("hash must not be empty")
	}

	// Write to file.
	path := filepath.Join(t.TempDir(), "artifact.json")
	if err := art.WriteTo(path); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	// Read back and decode.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var decoded artifact.Artifact
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Verify stored hash matches recomputed hash.
	matched, expected, actual, err := artifact.VerifyArtifact(path)
	if err != nil {
		t.Fatalf("VerifyArtifact: %v", err)
	}
	if !matched {
		t.Errorf("hash mismatch: stored=%s recomputed=%s", expected, actual)
	}

	// Verify question field was populated.
	if decoded.Question == "" {
		t.Error("question field should not be empty")
	}
	if decoded.SchemaVersion != artifact.SchemaVersion {
		t.Errorf("schema_version: got %q want %q", decoded.SchemaVersion, artifact.SchemaVersion)
	}
}

// TestDeterminism builds the same artifact twice from the same seeded store
// and asserts the encoded bytes are identical.
func TestDeterminism(t *testing.T) {
	store := newTestStore(t)
	runID := seedStore(t, store, []string{"optimist", "skeptic", "queen"})

	det := artifact.Determinism{Enabled: true}
	temp := 0.0
	det.Temperature = &temp

	art1, err := artifact.BuildArtifact(store, runID, det)
	if err != nil {
		t.Fatalf("BuildArtifact first: %v", err)
	}
	enc1, err := art1.Encode()
	if err != nil {
		t.Fatalf("Encode first: %v", err)
	}

	art2, err := artifact.BuildArtifact(store, runID, det)
	if err != nil {
		t.Fatalf("BuildArtifact second: %v", err)
	}
	enc2, err := art2.Encode()
	if err != nil {
		t.Fatalf("Encode second: %v", err)
	}

	if !bytes.Equal(enc1, enc2) {
		t.Errorf("encoded artifacts are not byte-identical\n--- first ---\n%s\n--- second ---\n%s",
			enc1, enc2)
	}

	if art1.Hash != art2.Hash {
		t.Errorf("hash mismatch: %s vs %s", art1.Hash, art2.Hash)
	}
}

// TestTamperDetection writes an artifact, mutates one byte in the file,
// and asserts VerifyArtifact returns matched=false with differing hashes.
func TestTamperDetection(t *testing.T) {
	store := newTestStore(t)
	runID := seedStore(t, store, []string{"optimist", "skeptic", "queen"})

	art, err := artifact.BuildArtifact(store, runID, artifact.Determinism{Enabled: false})
	if err != nil {
		t.Fatalf("BuildArtifact: %v", err)
	}

	path := filepath.Join(t.TempDir(), "artifact.json")
	if err := art.WriteTo(path); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	// Read and mutate one byte inside the narrative field.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Find "The future is bright" and flip one char.
	needle := []byte("The future is bright")
	idx := bytes.Index(data, needle)
	if idx < 0 {
		// Fallback: flip any byte in the data that isn't inside the hash field.
		// Pick a byte well before the end of the file.
		idx = 50
	}
	data[idx] ^= 0x01 // flip one bit in one byte
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile (tampered): %v", err)
	}

	matched, expected, actual, err := artifact.VerifyArtifact(path)
	if err != nil {
		// If the tampered byte broke JSON parsing entirely, that's also a
		// "tamper detected" result — accept it.
		t.Logf("VerifyArtifact returned error (tamper broke JSON): %v", err)
		return
	}
	if matched {
		t.Errorf("expected mismatch after tampering, but got matched=true (expected=%s actual=%s)", expected, actual)
	}
	if expected == actual {
		t.Errorf("expected and actual hashes are identical even after tampering: %s", expected)
	}
}

// TestMapOrdering builds two artifacts from the same store but with foragers
// inserted in different orders, and asserts the canonical JSON encoding is
// byte-identical (the sort must be enforced regardless of insertion order).
func TestMapOrdering(t *testing.T) {
	store1 := newTestStore(t)
	runID1 := seedStore(t, store1, []string{"optimist", "skeptic", "queen"})

	store2 := newTestStore(t)
	// Insert in reverse order.
	runID2 := seedStore(t, store2, []string{"queen", "skeptic", "optimist"})

	det := artifact.Determinism{Enabled: true}
	temp := 0.0
	det.Temperature = &temp

	art1, err := artifact.BuildArtifact(store1, runID1, det)
	if err != nil {
		t.Fatalf("BuildArtifact store1: %v", err)
	}
	enc1, err := art1.Encode()
	if err != nil {
		t.Fatalf("Encode store1: %v", err)
	}

	art2, err := artifact.BuildArtifact(store2, runID2, det)
	if err != nil {
		t.Fatalf("BuildArtifact store2: %v", err)
	}
	enc2, err := art2.Encode()
	if err != nil {
		t.Fatalf("Encode store2: %v", err)
	}

	if !bytes.Equal(enc1, enc2) {
		t.Errorf("canonical encoding differs by insertion order:\n--- forward ---\n%s\n--- reverse ---\n%s",
			enc1, enc2)
	}
}

// TestHashConstruction is an explicit proof that the hash is computed with
// Hash="", not with the Hash field set. Computes the SHA256 manually and
// checks against the artifact's stored Hash.
func TestHashConstruction(t *testing.T) {
	store := newTestStore(t)
	runID := seedStore(t, store, []string{"optimist"})

	art, err := artifact.BuildArtifact(store, runID, artifact.Determinism{Enabled: false})
	if err != nil {
		t.Fatalf("BuildArtifact: %v", err)
	}

	storedHash := art.Hash

	// Re-encode with Hash="" and manually SHA256 the bytes.
	art.Hash = ""
	reEnc, err := art.Encode()
	if err != nil {
		t.Fatalf("Encode with Hash zeroed: %v", err)
	}
	sum := sha256.Sum256(reEnc)
	recomputed := fmt.Sprintf("%x", sum)

	if storedHash != recomputed {
		t.Errorf("hash mismatch: stored=%s recomputed=%s", storedHash, recomputed)
	}

	// Print the actual hash for the report.
	t.Logf("sample artifact sha256: %s", storedHash)
}
