package embed

import (
	"math"
	"testing"
)

func TestCosine_IdenticalVectorsScoreOne(t *testing.T) {
	a := []float32{1, 2, 3, 4}
	got := Cosine(a, a)
	if math.Abs(float64(got-1.0)) > 1e-5 {
		t.Fatalf("expected 1.0 for identical vectors, got %v", got)
	}
}

func TestCosine_OrthogonalVectorsScoreZero(t *testing.T) {
	a := []float32{1, 0, 0, 0}
	b := []float32{0, 1, 0, 0}
	got := Cosine(a, b)
	if math.Abs(float64(got)) > 1e-5 {
		t.Fatalf("expected 0 for orthogonal vectors, got %v", got)
	}
}

func TestCosine_OppositeVectorsScoreNegativeOne(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}
	got := Cosine(a, b)
	if math.Abs(float64(got+1.0)) > 1e-5 {
		t.Fatalf("expected -1.0 for opposite vectors, got %v", got)
	}
}

func TestCosine_KnownNumericReference(t *testing.T) {
	// Reference values computed in numpy:
	//   a = [1, 2, 3, 4]
	//   b = [4, 3, 2, 1]
	//   cosine = dot/(||a|| ||b||)
	//   dot = 4 + 6 + 6 + 4 = 20
	//   ||a|| = sqrt(30) ≈ 5.477
	//   ||b|| = sqrt(30) ≈ 5.477
	//   cosine = 20 / 30 ≈ 0.6666...
	a := []float32{1, 2, 3, 4}
	b := []float32{4, 3, 2, 1}
	got := Cosine(a, b)
	want := float32(20.0 / 30.0)
	if math.Abs(float64(got-want)) > 1e-5 {
		t.Fatalf("cosine = %v, want %v", got, want)
	}
}

func TestCosine_DimMismatchReturnsZero(t *testing.T) {
	got := Cosine([]float32{1, 2, 3}, []float32{1, 2})
	if got != 0 {
		t.Fatalf("expected 0 for dim mismatch, got %v", got)
	}
}

func TestCosine_ZeroVectorsScoreZero(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	got := Cosine(a, b)
	if got != 0 {
		t.Fatalf("expected 0 when one vector is zero, got %v", got)
	}
}
