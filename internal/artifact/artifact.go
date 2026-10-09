// Package artifact implements the canonical deterministic artifact for
// swarm runs. Each completed swarm run can emit a SHA256-hashed JSON bundle
// that captures the question, determinism settings, per-forager
// models/providers, verdicts, synthesizer output, and the final comb_state
// snapshot. Two runs of the same (question, seed, pinned models) triple
// produce byte-identical artifacts only on a provider that honours the
// seed; otherwise the hash is a drift detector.
package artifact

import "github.com/Chubby-Honey-Bee/hive/internal/workflow"

// SchemaVersion is the current artifact schema. Bump when the shape changes.
// The fields past the 1.0 core — base_urls, synthesizer and
// determinism.top_p (1.1), schema_enforcement and
// synthesizer.schema_enforcement (1.2), diversity (1.3) and calibration
// (1.4) — are omitted when empty, so a file written under an earlier schema,
// which has none of them, decodes and re-encodes to its own bytes
// (VerifyArtifact).
const SchemaVersion = "1.4"

// Artifact is the canonical output of a completed swarm run. Every
// field is included in the SHA256 hash except Hash itself (which is
// computed with that field zeroed).
type Artifact struct {
	SchemaVersion string            `json:"schema_version"` // the schema it was written under
	Question      string            `json:"question"`
	Determinism   Determinism       `json:"determinism"`
	Foragers      []string          `json:"foragers"`              // alphabetical
	Models        map[string]string `json:"models"`                // forager → canonical SDK id
	Providers     map[string]string `json:"providers"`             // forager → backend kind
	BaseURLs      map[string]string `json:"base_urls,omitempty"`   // forager → endpoint that served it; "" for the CLI backends
	Verdicts      map[string]any    `json:"verdicts"`              // forager → parsed verdict JSON
	Synthesis     map[string]any    `json:"synthesis"`             // synthesizer's JSON
	Synthesizer   *Synthesizer      `json:"synthesizer,omitempty"` // the synthesis node's model, provider and endpoint; absent without one
	Comb          []CombVantage     `json:"comb"`                  // sorted by vantage_key
	Hash          string            `json:"sha256"`                // SHA256 of canonical encoding with this field zeroed

	// SchemaEnforcement is forager → how its node's output_schema held its
	// verdict ("enforced at decode" or "post-hoc only"), for each forager
	// whose node recorded one. The encoding sorts keys, so its place here
	// does not matter.
	SchemaEnforcement map[string]string `json:"schema_enforcement,omitempty"`

	// Diversity is the run's lens diversity, as the Queen's {diversity} line
	// reads it (workflow.RunDiversity): low when every lens that returned a
	// verdict ran on one model and returned the same verdict; otherwise
	// not_low, unknown or not_checked. BuildArtifact always sets it.
	Diversity string `json:"diversity,omitempty"`

	// Calibration is how sure the swarm was before what it said: the Queen's
	// convergence, the tally and its margin, whether a dissent was written,
	// and the ∇ pairs that fired (workflow.RunCalibration). The canonical
	// encoding sorts keys, so it precedes synthesis. BuildArtifact always
	// sets it.
	Calibration *workflow.Calibration `json:"calibration,omitempty"`
}

// Determinism captures the reproducibility settings used for the run.
type Determinism struct {
	Enabled     bool     `json:"enabled"`
	Temperature *float64 `json:"temperature"`     // nil = backend default
	TopP        *float64 `json:"top_p,omitempty"` // nil = backend default
	Seed        *int64   `json:"seed"`            // nil = no seed
}

// Synthesizer is what wrote the synthesis: the synthesis node's model,
// provider and endpoint, as its node row records them, and how its
// output_schema held the synthesis.
type Synthesizer struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"` // "" for the CLI backends

	SchemaEnforcement string `json:"schema_enforcement,omitempty"` // "" when the node recorded none
}

// CombVantage is one row from comb_state, stripped of timestamps for
// deterministic encoding.
type CombVantage struct {
	VantageKey    string `json:"vantage_key"`
	VantageKind   string `json:"vantage_kind"`
	Narrative     string `json:"narrative"`
	Confidence    int    `json:"confidence"`
	DominantLabel string `json:"dominant_label"` // "" when NULL
	Contested     bool   `json:"contested"`
	EvidenceCount int    `json:"evidence_count"`
}
