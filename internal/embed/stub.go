package embed

import (
	"context"
	"hash/fnv"
	"math"
)

// StubProvider produces deterministic, hash-based pseudo-embeddings.
// Useful only for tests + offline development — clearly named so
// production never silently falls into it. The embedding has weak
// semantic signal (similar strings share some hash neighbourhoods)
// but is nowhere near a real model.
type StubProvider struct{ dim int }

// NewStubProvider returns a 64-dim stub. 64 is deliberately small so
// tests run instantly and the BLOB shape stays compact.
func NewStubProvider() *StubProvider {
	return &StubProvider{dim: 64}
}

// Name returns the canonical stub identifier — used as the model
// column in comb_embeddings, so test-stored embeddings are clearly
// distinguishable from production.
func (p *StubProvider) Name() string { return "stub/v1" }

// Dim is fixed at 64.
func (p *StubProvider) Dim() int { return p.dim }

// Embed hashes the lowercased text into a 64-dim float32 vector via
// FNV-1a, then unit-normalises so cosine similarity stays bounded
// in [-1, 1]. Re-running on the same text is byte-equal — tests rely
// on this.
func (p *StubProvider) Embed(_ context.Context, text string) ([]float32, error) {
	v := make([]float32, p.dim)
	// Token-level hash to give similar texts overlapping signal.
	for _, tok := range tokenise(text) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tok))
		seed := h.Sum64()
		// Spread the hash across the vector so each token contributes
		// a fixed pattern; same tokens always hit the same dims.
		for i := 0; i < p.dim; i++ {
			s := seed*1103515245 + uint64(i)*2654435761
			// Map to [-1, 1] in float space: integer division would collapse
			// every value to zero.
			v[i] += float32(int64(s%2000)-1000) / 1000.0
		}
	}
	normalise(v)
	return v, nil
}

// BatchEmbed is a trivial loop — no parallelism required at stub speeds.
func (p *StubProvider) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := p.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// tokenise lowercases + splits on non-alphanumeric runs. Cheap enough
// for stub use.
func tokenise(s string) []string {
	out := []string{}
	curr := []byte{}
	for i := 0; i < len(s); i++ {
		if c, ok := tokenByte(s[i]); ok {
			curr = append(curr, c)
			continue
		}
		out, curr = endToken(out, curr)
	}
	out, _ = endToken(out, curr)
	return out
}

// tokenByte is c lowercased when it is an ASCII letter or digit; ok is
// false for any other byte, which separates tokens.
func tokenByte(c byte) (byte, bool) {
	if c >= 'A' && c <= 'Z' {
		return c + 32, true
	}
	return c, isLowerAlnum(c)
}

// isLowerAlnum reports whether c is a lowercase ASCII letter or a digit.
func isLowerAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// endToken appends the token in curr, when there is one, to out and empties
// curr.
func endToken(out []string, curr []byte) ([]string, []byte) {
	if len(curr) == 0 {
		return out, curr
	}
	return append(out, string(curr)), curr[:0]
}

// normalise scales v in place so its L2 norm is 1. Zero vectors are
// left alone (avoids divide-by-zero).
func normalise(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1.0 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}
