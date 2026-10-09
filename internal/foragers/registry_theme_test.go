package foragers

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestNormalizeArchetype_DefaultsSigilAndAccent asserts that a forager
// with no theme metadata gets the documented defaults — the renderer
// must always have something to show, even for user-authored foragers
// that skip the theme fields.
func TestNormalizeArchetype_DefaultsSigilAndAccent(t *testing.T) {
	w := &Forager{Name: "stranger", Title: "T", Description: "d"}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatalf("NormalizeArchetype: %v", err)
	}
	if w.Sigil != DefaultSigil {
		t.Errorf("Sigil = %q; want %q", w.Sigil, DefaultSigil)
	}
	if w.Accent != DefaultAccent {
		t.Errorf("Accent = %q; want %q", w.Accent, DefaultAccent)
	}
}

// TestNormalizeArchetype_PreservesExplicitTheme covers the override path.
func TestNormalizeArchetype_PreservesExplicitTheme(t *testing.T) {
	w := &Forager{
		Name: "n", Title: "t", Description: "d",
		Sigil: "★", Accent: "#ABCDEF",
	}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatalf("NormalizeArchetype: %v", err)
	}
	if w.Sigil != "★" {
		t.Errorf("Sigil mutated: got %q; want ★", w.Sigil)
	}
	if w.Accent != "#ABCDEF" {
		t.Errorf("Accent mutated: got %q; want #ABCDEF", w.Accent)
	}
}

// TestNormalizeArchetype_AddsHashPrefixToBareHex asserts the helper
// rescues frontmatter that wrote `accent: ABCDEF` without the leading
// hash — caller code can blindly emit `style="color:{accent}"` with
// well-formed CSS regardless.
func TestNormalizeArchetype_AddsHashPrefixToBareHex(t *testing.T) {
	w := &Forager{Name: "n", Title: "t", Description: "d", Accent: "C0FFEE"}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatal(err)
	}
	if w.Accent != "#C0FFEE" {
		t.Errorf("Accent = %q; want #C0FFEE", w.Accent)
	}
}

// TestAccentRGB_ParsesValidHex covers the happy path used by
// AnsiPrefix to emit 24-bit color codes.
func TestAccentRGB_ParsesValidHex(t *testing.T) {
	cases := []struct {
		in      string
		r, g, b uint8
	}{
		{"#FFFFFF", 255, 255, 255},
		{"#000000", 0, 0, 0},
		{"#7E5DA8", 0x7E, 0x5D, 0xA8},
		{"#F2C94C", 0xF2, 0xC9, 0x4C},
	}
	for _, tc := range cases {
		w := Forager{Accent: tc.in}
		gotR, gotG, gotB := w.AccentRGB()
		// Special-case: AccentRGB returns (0,0,0) on parse failure;
		// test cases for valid input must not collide with the
		// "treat as parse failure" sentinel — except #000000 itself.
		if tc.in != "#000000" && (gotR == 0 && gotG == 0 && gotB == 0) {
			t.Errorf("AccentRGB(%q) = (0,0,0) — looks like a parse failure", tc.in)
		}
		if gotR != tc.r || gotG != tc.g || gotB != tc.b {
			t.Errorf("AccentRGB(%q) = (%d,%d,%d); want (%d,%d,%d)",
				tc.in, gotR, gotG, gotB, tc.r, tc.g, tc.b)
		}
	}
}

// TestAccentRGB_ParseFailureReturnsZero ensures the renderer's
// fallback path is exercised for malformed accent strings.
func TestAccentRGB_ParseFailureReturnsZero(t *testing.T) {
	cases := []string{"", "rgb(1,2,3)", "#NOTHEX", "purple", "#12345"}
	for _, in := range cases {
		w := Forager{Accent: in}
		r, g, b := w.AccentRGB()
		if r != 0 || g != 0 || b != 0 {
			t.Errorf("malformed %q produced (%d,%d,%d); want zeros", in, r, g, b)
		}
	}
}

// TestAnsiPrefix_NonZeroEmitsEscapeCode confirms the wrapper used by
// the swarm CLI generates a real 24-bit ANSI prefix for valid
// accents and an empty string for malformed ones.
func TestAnsiPrefix_NonZeroEmitsEscapeCode(t *testing.T) {
	w := Forager{Accent: "#7E5DA8"}
	got := w.AnsiPrefix()
	if !strings.HasPrefix(got, "\x1b[38;2;") {
		t.Errorf("AnsiPrefix did not emit 24-bit code: %q", got)
	}
	if !strings.HasSuffix(got, "m") {
		t.Errorf("AnsiPrefix not closed: %q", got)
	}
}

func TestAnsiPrefix_MalformedReturnsEmpty(t *testing.T) {
	w := Forager{Accent: "garbage"}
	if got := w.AnsiPrefix(); got != "" {
		t.Errorf("AnsiPrefix(garbage) = %q; want empty", got)
	}
}

// TestShippedForagers_AllHaveDistinctSigilsAndAccents asserts the
// repo's 13 foragers each carry a unique sigil + a distinct accent.
// Catches a drift where a copy-paste leaves two foragers with the same
// theme — which would defeat the entire visual identity.
func TestShippedForagers_AllHaveDistinctSigilsAndAccents(t *testing.T) {
	dir := repoForagersDir(t)
	all, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(%s): %v", dir, err)
	}
	if len(all) < 13 {
		t.Fatalf("expected ≥13 foragers in repo dir, got %d", len(all))
	}
	sigils := make(map[string]string)
	accents := make(map[string]string)
	for _, w := range all {
		if w.Sigil == "" || w.Sigil == DefaultSigil {
			t.Errorf("forager %q ships without a custom sigil", w.Name)
		}
		if w.Accent == "" || w.Accent == DefaultAccent {
			t.Errorf("forager %q ships without a custom accent", w.Name)
		}
		if dup, ok := sigils[w.Sigil]; ok {
			t.Errorf("sigil %q duplicated by %q and %q", w.Sigil, dup, w.Name)
		}
		sigils[w.Sigil] = w.Name
		if dup, ok := accents[w.Accent]; ok {
			t.Errorf("accent %q duplicated by %q and %q", w.Accent, dup, w.Name)
		}
		accents[w.Accent] = w.Name
	}
}

// repoForagersDir resolves the path to the in-repo foragers/ directory
// from the test binary's working directory. Tests run from the
// package's own dir, so this walks two levels up.
func repoForagersDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "foragers"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}
