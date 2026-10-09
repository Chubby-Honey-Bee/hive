package artifact_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
)

// writeSample writes a built artifact and returns its path and bytes.
func writeSample(t *testing.T) (string, []byte) {
	t.Helper()
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
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, data
}

// A changed byte fails verification even when the file still decodes to the
// same artifact: whitespace, or a key whose case changed.
func TestVerifyArtifact_AnyByteChangeFails(t *testing.T) {
	path, data := writeSample(t)
	if matched, _, _, err := artifact.VerifyArtifact(path); err != nil || !matched {
		t.Fatalf("untouched artifact: matched=%v err=%v", matched, err)
	}

	for name, tamper := range map[string]func([]byte) []byte{
		"indent space to tab": func(b []byte) []byte { return bytes.Replace(b, []byte("\n  \""), []byte("\n\t\""), 1) },
		"key case":            func(b []byte) []byte { return bytes.Replace(b, []byte(`"question"`), []byte(`"Question"`), 1) },
		"trailing newline":    func(b []byte) []byte { return append(append([]byte{}, b...), '\n') },
	} {
		changed := tamper(data)
		if bytes.Equal(changed, data) {
			t.Fatalf("%s: tamper changed nothing", name)
		}
		tampered := filepath.Join(t.TempDir(), "artifact.json")
		if err := os.WriteFile(tampered, changed, 0o644); err != nil {
			t.Fatal(err)
		}
		matched, stored, recomputed, err := artifact.VerifyArtifact(tampered)
		if err != nil {
			t.Fatalf("%s: VerifyArtifact: %v", name, err)
		}
		if matched {
			t.Errorf("%s: matched=true, want a mismatch", name)
		}
		if stored == recomputed {
			t.Errorf("%s: stored and recomputed hashes are both %s, want them to differ", name, stored)
		}
	}
}

// The seed is recorded exactly. Decoding to float64 during canonicalisation
// rounded any seed above 2^53.
func TestEncode_KeepsALargeSeedExact(t *testing.T) {
	seed := int64(1)<<53 + 1 // the smallest positive integer float64 cannot hold
	a := &artifact.Artifact{
		SchemaVersion: artifact.SchemaVersion,
		Determinism:   artifact.Determinism{Enabled: true, Seed: &seed},
	}
	raw, err := a.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var back artifact.Artifact
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Determinism.Seed == nil {
		t.Fatalf("encoded artifact has no seed:\n%s", raw)
	}
	if *back.Determinism.Seed != seed {
		t.Errorf("encoded seed decodes to %d, want %d:\n%s", *back.Determinism.Seed, seed, raw)
	}
}
