package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
)

// computeAndSetHash encodes the artifact with Hash="" and computes SHA256.
func (a *Artifact) computeAndSetHash() error {
	a.Hash = ""
	raw, err := a.encode()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	a.Hash = fmt.Sprintf("%x", sum)
	return nil
}

// Encode produces the canonical byte-deterministic JSON encoding of the
// artifact. Map keys are sorted alphabetically at every level, the Comb
// slice is sorted by vantage_key, and the Foragers slice is sorted
// alphabetically. A Encode → Decode → Encode roundtrip produces
// byte-identical output.
func (a *Artifact) Encode() ([]byte, error) {
	return a.encode()
}

// encode is the internal canonical encoder. It marshals the artifact
// through an ordered intermediate representation so that Go's random map
// iteration order never affects the output.
func (a *Artifact) encode() ([]byte, error) {
	// Marshal through encoding/json. Go's map key ordering is random, so
	// we build a sortedMap representation to guarantee stable output.
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("marshal artifact: %w", err)
	}

	// Re-parse into a generic structure so we can canonicalize map keys.
	// UseNumber keeps each number's text: decoding to float64 rounded the
	// int64 seed above 2^53.
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("unmarshal for canonicalization: %w", err)
	}

	// Canonicalize: sort all map keys recursively.
	canonical := canonicalize(generic)

	out, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal canonical: %w", err)
	}
	return out, nil
}

// WriteTo encodes the artifact and writes it to path. The directory is
// created if it does not exist. Idempotent — same artifact always
// produces the same bytes.
func (a *Artifact) WriteTo(path string) error {
	data, err := a.encode()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// VerifyArtifact re-reads the artifact at path, re-hashes its bytes, and
// reports whether the stored hash matches.
//
// The recomputed hash is SHA-256 over the file's own bytes with the stored
// sha256 value emptied, which for a canonical file is exactly the encoding
// the hash was taken over. A match also requires the file to be the
// canonical encoding of what it decodes to. Re-hashing only the decoded
// struct passed any change that decodes the same: whitespace, or a key
// whose case changed, since encoding/json matches field names ignoring case.
//
// Returns:
//   - matched: true when the file is canonical and its stored Hash equals
//     the recomputed hash
//   - expected: the hash stored in the file
//   - actual: the recomputed hash
//   - err: non-nil on IO or decode failures
func VerifyArtifact(path string) (matched bool, expected, actual string, err error) {
	data, art, err := readArtifact(path)
	if err != nil {
		return false, "", "", err
	}
	storedHash := art.Hash

	canonical, err := art.encode()
	if err != nil {
		return false, storedHash, "", fmt.Errorf("re-encode artifact: %w", err)
	}
	recomputed := hashWithoutStored(data, storedHash)
	return bytes.Equal(data, canonical) && storedHash == recomputed, storedHash, recomputed, nil
}

// readArtifact reads the file at path and decodes it into an Artifact, so
// the caller gets the stored Hash field.
func readArtifact(path string) ([]byte, *Artifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read artifact: %w", err)
	}
	var art Artifact
	if err := json.Unmarshal(data, &art); err != nil {
		return nil, nil, fmt.Errorf("decode artifact: %w", err)
	}
	return data, &art, nil
}

// hashWithoutStored is the SHA-256 of data with its stored hash value
// emptied.
func hashWithoutStored(data []byte, storedHash string) string {
	blanked := bytes.Replace(data, []byte(`"`+storedHash+`"`), []byte(`""`), 1)
	sum := sha256.Sum256(blanked)
	return fmt.Sprintf("%x", sum)
}

// canonicalize recursively sorts map keys, producing a stable JSON
// representation regardless of Go's random map iteration order.
// Slices are preserved in their existing order (callers are responsible
// for pre-sorting slices before calling canonicalize).
func canonicalize(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		return canonicalMap(tv)
	case []any:
		return canonicalSlice(tv)
	default:
		return v
	}
}

// canonicalMap is m with its keys sorted and each value canonical.
func canonicalMap(m map[string]any) orderedMap {
	keys := slices.Sorted(maps.Keys(m))
	out := make(orderedMap, 0, len(keys))
	for _, k := range keys {
		out = append(out, kv{Key: k, Val: canonicalize(m[k])})
	}
	return out
}

// canonicalSlice is s in its order with each item canonical.
func canonicalSlice(s []any) []any {
	out := make([]any, len(s))
	for i, item := range s {
		out[i] = canonicalize(item)
	}
	return out
}

// orderedMap is a slice of key-value pairs that marshals as a JSON
// object with keys in the order they appear in the slice. This lets us
// control key ordering without patching encoding/json.
type orderedMap []kv

type kv struct {
	Key string
	Val any
}

// MarshalJSON writes the pairs as one JSON object, in their order.
func (om orderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, pair := range om {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writePair(&buf, pair); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// writePair writes pair to buf as a JSON member, "key":value.
func writePair(buf *bytes.Buffer, pair kv) error {
	keyBytes, err := json.Marshal(pair.Key)
	if err != nil {
		return err
	}
	valBytes, err := json.Marshal(pair.Val)
	if err != nil {
		return err
	}
	buf.Write(keyBytes)
	buf.WriteByte(':')
	buf.Write(valBytes)
	return nil
}
