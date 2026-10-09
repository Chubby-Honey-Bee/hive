// Package embed implements the Swarm's semantic-similarity layer.
//
// Pure-Go cosine over BLOB-stored float32 vectors. Embedding providers
// are pluggable (OpenAI, Ollama-style local HTTP, deterministic stub
// for tests). Storage rides the same SQLite DB as the rest of the
// system — single-binary deployment preserved.
//
// Why pure Go? modernc.org/sqlite is a pure-Go port; loading C
// extensions like sqlite-vec across our cross-platform builds (Linux
// ARM, macOS, Windows distroless) is brittle. Brute-force cosine over
// 10K vectors × 1536 dims is sub-100ms — well under the threshold
// where ANN structures pay back complexity.
package embed

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Provider is the abstraction every embedding backend implements. A
// Provider must be safe for concurrent use; the Searcher will call
// BatchEmbed from many goroutines.
type Provider interface {
	// Name returns the model identifier as it appears in
	// comb_embeddings.model. e.g. "openai/text-embedding-3-small",
	// "localhttp/nomic-embed-text", "stub/v1".
	Name() string

	// Dim returns the vector dimensionality the provider produces.
	Dim() int

	// Embed produces a single embedding for one input string.
	Embed(ctx context.Context, text string) ([]float32, error)

	// BatchEmbed produces embeddings for many inputs in one call.
	// Length of the returned slice must match len(texts); on partial
	// failure callers expect a non-nil error and may discard the
	// (possibly partial) result.
	BatchEmbed(ctx context.Context, texts []string) ([][]float32, error)
}

// Detect picks a Provider based on environment variables, in this
// precedence:
//
//  1. HIVE_EMBED_PROVIDER, resolved by SelectByName — an unknown
//     name is an error, not a silent fall-through to auto-detection
//  2. HIVE_EMBED_MODEL    (forces a specific model with the
//     auto-detected provider; e.g. text-embedding-3-large)
//  3. Auto: HIVE_LOCAL_EMBED_URL set ⇒ localhttp; OPENAI_API_KEY
//     set ⇒ openai; otherwise stub (with stderr warning). The local URL
//     wins because setting it asks for local embeddings and nothing else,
//     while OPENAI_API_KEY is also set for a local OpenAI-compatible
//     server's model calls.
//
// Tests pass `--provider stub` explicitly so they're isolated.
func Detect() (Provider, error) {
	if name := strings.TrimSpace(os.Getenv("HIVE_EMBED_PROVIDER")); name != "" {
		return SelectByName(name)
	}
	// Auto-detect.
	if u := os.Getenv("HIVE_LOCAL_EMBED_URL"); u != "" {
		return NewLocalHTTPProvider(u,
			modelOrDefault(os.Getenv("HIVE_EMBED_MODEL"), defaultLocalModel))
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		return NewOpenAIProvider(os.Getenv("OPENAI_API_KEY"),
			modelOrDefault(os.Getenv("HIVE_EMBED_MODEL"), defaultOpenAIModel))
	}
	fmt.Fprintln(os.Stderr,
		"[embed] WARNING: no embedding provider configured (set "+
			"HIVE_LOCAL_EMBED_URL or OPENAI_API_KEY). Falling back to "+
			"deterministic stub — results will not be semantically meaningful.")
	return NewStubProvider(), nil
}

// SelectByName returns a Provider by explicit name.
func SelectByName(name string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openai":
		return NewOpenAIProvider(os.Getenv("OPENAI_API_KEY"),
			modelOrDefault(os.Getenv("HIVE_EMBED_MODEL"), defaultOpenAIModel))
	case "localhttp", "ollama":
		return NewLocalHTTPProvider(os.Getenv("HIVE_LOCAL_EMBED_URL"),
			modelOrDefault(os.Getenv("HIVE_EMBED_MODEL"), defaultLocalModel))
	case "stub":
		return NewStubProvider(), nil
	}
	return nil, fmt.Errorf("unknown embedding provider %q (want openai|localhttp|stub)", name)
}

func modelOrDefault(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}

const (
	defaultOpenAIModel = "text-embedding-3-small"
	defaultLocalModel  = "nomic-embed-text"
)
