package citations

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractDOIs_HappyPath(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"https://doi.org/10.1038/nature12373", []string{"10.1038/nature12373"}},
		{"see 10.1126/science.aaa8685.", []string{"10.1126/science.aaa8685"}},
		{"http://dx.doi.org/10.1145/3458817.3476203", []string{"10.1145/3458817.3476203"}},
		{`["https://doi.org/10.1038/nature12373","https://doi.org/10.1126/science.aaa8685"]`,
			[]string{"10.1038/nature12373", "10.1126/science.aaa8685"}},
		{"trailing punctuation 10.1234/abc-defg.", []string{"10.1234/abc-defg"}},
		{"comma in array 10.1234/abc, 10.1234/xyz", []string{"10.1234/abc", "10.1234/xyz"}},
	}
	for _, tc := range cases {
		got := ExtractDOIs(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ExtractDOIs(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

// Parentheses and semicolons are part of real DOIs (Elsevier's Lancet DOIs,
// SICI forms), so the match keeps them. Only a `)` that closes nothing in
// the DOI is prose, and trimmed.
func TestExtractDOIs_KeepsParenthesesAndSemicolons(t *testing.T) {
	const lancet = "10.1016/S0140-6736(20)30183-5"
	const sici = "10.1002/(SICI)1097-4636(199702)34:2<196::AID-JBM8>3.0.CO;2-#"
	cases := []struct {
		in   string
		want []string
	}{
		{"https://doi.org/" + lancet + ", doi:10.1038/nature12373",
			[]string{strings.ToLower(lancet), "10.1038/nature12373"}},
		{`["https://doi.org/` + lancet + `"]`, []string{strings.ToLower(lancet)}},
		{"(see " + lancet + ").", []string{strings.ToLower(lancet)}},
		{"cited as " + sici + ".", []string{strings.ToLower(sici)}},
		{"(10.1038/nature12373)", []string{"10.1038/nature12373"}},
		{"10.1038/nature12373; 10.1126/science.aaa8685;", []string{"10.1038/nature12373", "10.1126/science.aaa8685"}},
	}
	for _, tc := range cases {
		if got := ExtractDOIs(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ExtractDOIs(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestExtractDOIs_NoMatches(t *testing.T) {
	cases := []string{
		"",
		"no doi here",
		"https://example.com/whitepaper.pdf",
		"version 10.1 of the spec", // version number, not a DOI
	}
	for _, in := range cases {
		if got := ExtractDOIs(in); len(got) != 0 {
			t.Errorf("ExtractDOIs(%q) = %v; want empty", in, got)
		}
	}
}

func TestExtractDOIs_Deduplicates(t *testing.T) {
	in := "10.1038/nature12373 cited twice: 10.1038/nature12373"
	got := ExtractDOIs(in)
	if len(got) != 1 || got[0] != "10.1038/nature12373" {
		t.Errorf("expected deduplicated single entry, got %v", got)
	}
}

// ExtractDOIs returns the lowercased form it deduplicates on, so
// verify-citations, which lowercases before writing citation_oa_cache, and
// mss_audit, which reads it, reach the same verdict on a DOI that carries an
// uppercase letter.
func TestExtractDOIs_ReturnsTheNormalisedForm(t *testing.T) {
	got := ExtractDOIs("see https://doi.org/10.1000/AbC and 10.1000/abc again")
	if len(got) != 1 {
		t.Fatalf("got %v; want one DOI (the two differ only in case)", got)
	}
	if got[0] != "10.1000/abc" {
		t.Errorf("got %q; want the lowercased form, which is what the cache is keyed on", got[0])
	}
}
