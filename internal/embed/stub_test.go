package embed

import (
	"context"
	"testing"
)

func TestStubProvider_Deterministic(t *testing.T) {
	p := NewStubProvider()
	ctx := context.Background()
	v1, err := p.Embed(ctx, "the moon is grey")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	v2, err := p.Embed(ctx, "the moon is grey")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(v1) != len(v2) {
		t.Fatalf("dim mismatch %d vs %d", len(v1), len(v2))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("non-deterministic at i=%d: %v vs %v", i, v1[i], v2[i])
		}
	}
}

func TestStubProvider_DifferentTextsDifferentVectors(t *testing.T) {
	p := NewStubProvider()
	ctx := context.Background()
	v1, _ := p.Embed(ctx, "alpha")
	v2, _ := p.Embed(ctx, "beta")
	// Highly unlikely to be byte-identical.
	identical := true
	for i := range v1 {
		if v1[i] != v2[i] {
			identical = false
			break
		}
	}
	if identical {
		t.Fatalf("stub returned identical embeddings for different inputs")
	}
}

func TestStubProvider_BatchEmbedMatchesEmbed(t *testing.T) {
	p := NewStubProvider()
	ctx := context.Background()
	texts := []string{"foo", "bar", "baz"}
	batch, err := p.BatchEmbed(ctx, texts)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	for i, t2 := range texts {
		single, _ := p.Embed(ctx, t2)
		for j := range single {
			if single[j] != batch[i][j] {
				t.Fatalf("batch[%d] differs from Embed at j=%d", i, j)
			}
		}
	}
}

func TestStubProvider_NameAndDim(t *testing.T) {
	p := NewStubProvider()
	if p.Name() != "stub/v1" {
		t.Errorf("Name = %q, want stub/v1", p.Name())
	}
	if p.Dim() != 64 {
		t.Errorf("Dim = %d, want 64", p.Dim())
	}
}
