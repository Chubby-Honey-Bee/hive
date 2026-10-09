package slug

import (
	"strings"
	"testing"
)

func TestHiddenBasics(t *testing.T) {
	cases := map[string]string{
		"Hello, World!":           "hello-world",
		"  Go 1.22 -- Released  ": "go-1-22-released",
		"Ünïcode Title":           "ünïcode-title",
		"Straße":                  "straße",
		"日本語 テスト":                 "日本語-テスト",
		"MiXeD CASE":              "mixed-case",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHiddenApostrophe(t *testing.T) {
	cases := map[string]string{
		"don't stop":    "dont-stop",
		"rock 'n' roll": "rock-n-roll",
		"'quoted'":      "quoted",
		"it's":          "its",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHiddenUntitled(t *testing.T) {
	for _, in := range []string{"", "!!!", "   ", "---", "'"} {
		if got := Slugify(in); got != "untitled" {
			t.Errorf("Slugify(%q) = %q, want untitled", in, got)
		}
	}
}

func TestHiddenNoLengthLimit(t *testing.T) {
	in := "the quick brown fox jumps over the lazy dog again"
	if got, want := Slugify(in), "the-quick-brown-fox-jumps-over-the-lazy-dog-again"; got != want {
		t.Errorf("Slugify(long) = %q, want %q", got, want)
	}
	if got := Slugify(strings.Repeat("a", 40)); len(got) != 40 {
		t.Errorf("Slugify(40 a) is %d bytes, want 40", len(got))
	}
}

func TestHiddenIdempotent(t *testing.T) {
	for _, in := range []string{"Hello, World!", "Ünïcode Title", "don't stop", "日本語 テスト", "!!!"} {
		once := Slugify(in)
		if twice := Slugify(once); twice != once {
			t.Errorf("Slugify(%q) = %q, but Slugify of that = %q", in, once, twice)
		}
	}
}

func TestHiddenNonASCIIKept(t *testing.T) {
	if got := Slugify("café au lait"); got != "café-au-lait" {
		t.Errorf("Slugify(café au lait) = %q, want café-au-lait", got)
	}
	if got := Slugify("Ελληνικά 123"); got != "ελληνικά-123" {
		t.Errorf("Slugify(Greek) = %q, want ελληνικά-123", got)
	}
}
