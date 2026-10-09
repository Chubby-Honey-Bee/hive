package slug

import (
	"strings"
	"testing"
)

func TestHiddenBasics(t *testing.T) {
	cases := map[string]string{
		"Hello, World!":            "hello-world",
		"  Go 1.22 -- Released  ":  "go-1-22-released",
		"Ünïcode Title":            "n-code-title",
		"already-a-slug":           "already-a-slug",
		"MiXeD CASE":               "mixed-case",
		"tabs\tand\nnewlines here": "tabs-and-newlines-here",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHiddenUntitled(t *testing.T) {
	for _, in := range []string{"", "!!!", "   ", "日本語", "---"} {
		if got := Slugify(in); got != "untitled" {
			t.Errorf("Slugify(%q) = %q, want untitled", in, got)
		}
	}
}

func TestHiddenCutAtWordBoundary(t *testing.T) {
	in := "the quick brown fox jumps over the lazy dog again"
	if got, want := Slugify(in), "the-quick-brown-fox-jumps-over"; got != want {
		t.Errorf("Slugify(long) = %q, want %q", got, want)
	}
	// Exactly 32 bytes is kept whole.
	exact := "abcd-efgh-ijkl-mnop-qrst-uvwx-yz"
	if len(exact) != 32 {
		t.Fatalf("fixture is %d bytes", len(exact))
	}
	if got := Slugify(exact); got != exact {
		t.Errorf("Slugify(32 bytes) = %q, want it unchanged", got)
	}
}

func TestHiddenHardCutWithoutHyphen(t *testing.T) {
	in := strings.Repeat("a", 40)
	if got, want := Slugify(in), strings.Repeat("a", 32); got != want {
		t.Errorf("Slugify(40 a) = %q (%d bytes), want 32 a", got, len(got))
	}
	// A hyphen past byte 32 does not help: the first 32 bytes hold none.
	in = strings.Repeat("b", 35) + "-tail"
	if got, want := Slugify(in), strings.Repeat("b", 32); got != want {
		t.Errorf("Slugify(35 b + tail) = %q, want %q", got, want)
	}
}

func TestHiddenIdempotent(t *testing.T) {
	for _, in := range []string{"Hello, World!", "the quick brown fox jumps over the lazy dog again", strings.Repeat("a", 40), "!!!", "Ünïcode Title"} {
		once := Slugify(in)
		if twice := Slugify(once); twice != once {
			t.Errorf("Slugify(%q) = %q, but Slugify of that = %q", in, once, twice)
		}
	}
}

func TestHiddenNonASCIINeverKept(t *testing.T) {
	for _, in := range []string{"café", "naïve", "Straße"} {
		got := Slugify(in)
		for i := 0; i < len(got); i++ {
			if got[i] >= 0x80 {
				t.Errorf("Slugify(%q) = %q keeps a non-ASCII byte", in, got)
			}
		}
	}
	if got := Slugify("café au lait"); got != "caf-au-lait" {
		t.Errorf("Slugify(café au lait) = %q, want caf-au-lait", got)
	}
}
