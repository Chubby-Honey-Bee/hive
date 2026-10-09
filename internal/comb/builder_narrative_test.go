package comb

import (
	"strings"
	"testing"
)

func TestRenderNarrative_NoEvidence(t *testing.T) {
	d := &Digest{VantageKey: "d1=0", EvidenceCount: 0}
	got := renderNarrative(d, 0, 0)
	if !strings.Contains(got, "no findings yet") {
		t.Errorf("got %q; want 'no findings yet'", got)
	}
}

func TestRenderNarrative_GlobalLabel(t *testing.T) {
	d := &Digest{VantageKey: "", EvidenceCount: 0}
	got := renderNarrative(d, 0, 0)
	if !strings.Contains(got, "global") {
		t.Errorf("got %q; want 'global' label", got)
	}
}

func TestRenderNarrative_FullDigest(t *testing.T) {
	d := &Digest{
		VantageKey:         "d1=0",
		EvidenceCount:      5,
		DominantLabel:      "definition",
		Confidence:         75,
		OpenQuestionsCount: 2,
	}
	got := renderNarrative(d, 1, 0)
	for _, want := range []string{"d1=0", "evidence=5", "dominant=definition", "confidence=75%", "open=2", "conflicts=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("got %q; missing %q", got, want)
		}
	}
}

func TestRenderNarrative_ContestedFlag(t *testing.T) {
	d := &Digest{
		VantageKey:    "d1=0",
		EvidenceCount: 3,
		DominantLabel: "definition",
		Confidence:    50,
		Contested:     true,
	}
	got := renderNarrative(d, 0, 0)
	if !strings.Contains(got, "CONTESTED") {
		t.Errorf("got %q; want CONTESTED flag", got)
	}
}

func TestRenderNarrative_CriticalGaps(t *testing.T) {
	d := &Digest{
		VantageKey:    "d1=0",
		EvidenceCount: 3,
		DominantLabel: "definition",
		Confidence:    50,
	}
	got := renderNarrative(d, 0, 2)
	if !strings.Contains(got, "critical_gaps=2") {
		t.Errorf("got %q; want critical_gaps=2", got)
	}
}

func TestRenderNarrative_EmptyDominantLabel(t *testing.T) {
	d := &Digest{
		VantageKey:    "d1=0",
		EvidenceCount: 1,
		DominantLabel: "",
		Confidence:    100,
	}
	got := renderNarrative(d, 0, 0)
	if !strings.Contains(got, "dominant=—") {
		t.Errorf("got %q; want dominant=—", got)
	}
}
