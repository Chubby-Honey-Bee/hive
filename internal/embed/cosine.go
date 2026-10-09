package embed

import "math"

// Cosine returns the cosine similarity of two float32 vectors.
// Returns 0 for mismatched or zero-length inputs. Result is in [-1, 1].
//
// For pre-normalised inputs cosine reduces to a dot product; we
// compute the magnitudes anyway so callers don't need to track
// normalisation state. The arithmetic cost (one extra pass per
// vector) is dwarfed by network round-trips in any realistic flow.
func Cosine(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	dot, magA, magB := products(a, b)
	if magA == 0 || magB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(magA) * math.Sqrt(magB)))
}

// products is the dot product of a and b, of equal length, and the squares
// of their magnitudes.
func products(a, b []float32) (dot, magA, magB float64) {
	// Inner loop unrolled by 4 — compilers in 2026 vectorise this
	// adequately on amd64; keep the unrolled form for portability.
	i := 0
	for ; i+4 <= len(a); i += 4 {
		dot += float64(a[i])*float64(b[i]) +
			float64(a[i+1])*float64(b[i+1]) +
			float64(a[i+2])*float64(b[i+2]) +
			float64(a[i+3])*float64(b[i+3])
		magA += float64(a[i])*float64(a[i]) +
			float64(a[i+1])*float64(a[i+1]) +
			float64(a[i+2])*float64(a[i+2]) +
			float64(a[i+3])*float64(a[i+3])
		magB += float64(b[i])*float64(b[i]) +
			float64(b[i+1])*float64(b[i+1]) +
			float64(b[i+2])*float64(b[i+2]) +
			float64(b[i+3])*float64(b[i+3])
	}
	for ; i < len(a); i++ {
		dot += float64(a[i]) * float64(b[i])
		magA += float64(a[i]) * float64(a[i])
		magB += float64(b[i]) * float64(b[i])
	}
	return dot, magA, magB
}
