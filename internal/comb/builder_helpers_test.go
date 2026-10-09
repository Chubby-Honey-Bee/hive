package comb

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestConfidenceFromVerdict(t *testing.T) {
	cases := []struct {
		verdict string
		want    int
	}{
		{"support", 80},
		{"oppose", 20},
		{"conditional", 50},
		{"abstain", 30},
		{"unknown", 50}, // default
		{"", 50},        // default
	}
	for _, tc := range cases {
		if got := confidenceFromVerdict(tc.verdict); got != tc.want {
			t.Errorf("confidenceFromVerdict(%q) = %d; want %d", tc.verdict, got, tc.want)
		}
	}
}

func TestCountList_NilOrMissing(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]any
		key  string
		want int
	}{
		{"missing key", map[string]any{}, "missing", 0},
		{"nil map", nil, "x", 0},
		{"non-list value", map[string]any{"x": 42}, "x", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := countList(tc.m, tc.key)
			if got != tc.want {
				t.Errorf("countList = %d; want %d", got, tc.want)
			}
		})
	}
}

func TestCountList_AnyArray(t *testing.T) {
	m := map[string]any{"items": []any{1, 2, 3}}
	if got := countList(m, "items"); got != 3 {
		t.Errorf("countList []any = %d; want 3", got)
	}
}

func TestCountList_StringArray(t *testing.T) {
	m := map[string]any{"items": []string{"a", "b"}}
	if got := countList(m, "items"); got != 2 {
		t.Errorf("countList []string = %d; want 2", got)
	}
}

func TestClip(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"short", "hello", 5},
		{"exactly-512", strings.Repeat("a", 512), 512},
		{"over-512", strings.Repeat("a", 1000), 512},
		{"cut-inside-a-rune", strings.Repeat("a", 511) + "—", 511},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clip(tc.in)
			if len(got) != tc.want || !utf8.ValidString(got) {
				t.Errorf("len = %d, valid UTF-8 %v; want %d and valid", len(got), utf8.ValidString(got), tc.want)
			}
		})
	}
}
