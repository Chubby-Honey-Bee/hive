package runner

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCommitMessageFor_WaveZero(t *testing.T) {
	got := CommitMessageFor(0, "fix-1", "tweak")
	want := "validate: fix-1 — tweak"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestCommitMessageFor_WavePositive(t *testing.T) {
	got := CommitMessageFor(2, "fix-3", "edit foo")
	want := "validate(wave-2): fix-3 — edit foo"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestCommitMessageFor_EmptySummary(t *testing.T) {
	got := CommitMessageFor(0, "n", "")
	if !strings.Contains(got, "agent edit") {
		t.Errorf("expected 'agent edit' fallback; got %q", got)
	}
}

func TestCommitMessageFor_SummaryTrimming(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := CommitMessageFor(0, "n", long)
	// summary truncated to 77 + "..." (= 80 chars)
	if !strings.Contains(got, "...") {
		t.Errorf("expected ellipsis on long summary; got %q", got)
	}
}

func TestCommitMessageFor_TrimsWhitespace(t *testing.T) {
	got := CommitMessageFor(1, "n", "   trimmed   ")
	if !strings.Contains(got, "— trimmed") {
		t.Errorf("expected trimmed summary; got %q", got)
	}
	if strings.Contains(got, "  trimmed") {
		t.Errorf("expected leading whitespace removed; got %q", got)
	}
}

func TestTimestamp_RFC3339Z(t *testing.T) {
	got := Timestamp()
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Fatalf("Timestamp() = %q; not RFC3339: %v", got, err)
	}
	if !strings.HasSuffix(got, "Z") {
		t.Errorf("expected UTC 'Z' suffix; got %q", got)
	}
}

// A long summary is cut on a rune boundary, so the commit subject is valid
// UTF-8.
func TestCommitMessageFor_CutsOnARuneBoundary(t *testing.T) {
	got := CommitMessageFor(1, "fix-1", strings.Repeat("a", 76)+"—"+strings.Repeat("b", 10))
	if !utf8.ValidString(got) || !strings.HasSuffix(got, strings.Repeat("a", 76)+"...") {
		t.Errorf("subject %q, want valid UTF-8 ending in the summary's start and ...", got)
	}
}
