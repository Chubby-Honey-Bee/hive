package cli

import (
	"strings"
	"testing"
)

// TestCanonicalise_TimestampReplacement asserts every supported
// timestamp format is canonicalised to "<ts>".
func TestCanonicalise_TimestampReplacement(t *testing.T) {
	cases := []struct{ in, want string }{
		{"started at 2026-04-12T15:30:45Z", "started at <ts>"},
		{"finished 2026-04-12T15:30:45.123Z", "finished <ts>"},
		{"raw 2026-04-12T15:30:45", "raw <ts>"},
		{"plain text, no timestamp", "plain text, no timestamp"},
	}
	for _, tc := range cases {
		if got := canonicalise(tc.in, ""); got != tc.want {
			t.Errorf("canonicalise(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestCanonicalise_IDReplacement asserts both `id=N` and JSON `"id": N`
// forms collapse to placeholders so fixtures stay byte-equal across
// runs (auto-increment IDs vary by replay order).
func TestCanonicalise_IDReplacement(t *testing.T) {
	cases := []struct{ in, want string }{
		{"created agent_run id=42", "created agent_run id=<int>"},
		{"created run ID=7", "created run ID=<int>"},
		{`{"id": 13, "wave": 1}`, `{"id":<int>, "wave": 1}`},
		{`{"id":  13}`, `{"id":<int>}`},
	}
	for _, tc := range cases {
		if got := canonicalise(tc.in, ""); got != tc.want {
			t.Errorf("canonicalise(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestCanonicalise_CreatedAtReplacement asserts the `"created_at": "..."`
// JSON field is replaced. The pattern intentionally requires double
// quotes around the value — single-quoted timestamps are left alone.
func TestCanonicalise_CreatedAtReplacement(t *testing.T) {
	in := `{"id": 1, "created_at": "2026-04-12T15:30:45Z", "name": "x"}`
	got := canonicalise(in, "")
	if !strings.Contains(got, `"created_at":"<ts>"`) {
		t.Errorf("created_at not canonicalised: %q", got)
	}
	if !strings.Contains(got, `"id":<int>`) {
		t.Errorf("id not canonicalised: %q", got)
	}
}

// TestCanonicalise_WorkspacePathStripped asserts a workspace path is
// rewritten to "<path>" so fixtures generated under TempDir replay
// against any other tmpdir.
func TestCanonicalise_WorkspacePathStripped(t *testing.T) {
	ws := "/tmp/abc-7f2e/workspace"
	in := "writing " + ws + "/hive.db ok"
	got := canonicalise(in, ws)
	if got != "writing <path>/hive.db ok" {
		t.Errorf("workspace not stripped: %q", got)
	}
}

// TestCanonicalise_TrailingWhitespaceStripped asserts each line has
// trailing spaces/tabs removed and the final \n is trimmed.
func TestCanonicalise_TrailingWhitespaceStripped(t *testing.T) {
	in := "line1   \nline2\t\t\nline3 \n\n"
	got := canonicalise(in, "")
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// TestCanonicalise_EmptyInput is the edge-case zero — must not panic.
func TestCanonicalise_EmptyInput(t *testing.T) {
	if got := canonicalise("", ""); got != "" {
		t.Errorf("empty input: got %q; want empty", got)
	}
	if got := canonicalise("", "/some/path"); got != "" {
		t.Errorf("empty + workspace: got %q", got)
	}
}

// A replay on Windows compares output that may carry CRLF against a fixture
// recorded with LF, so the comparator normalises CR as it does backslashes.
func TestCanonicalise_CRLF(t *testing.T) {
	if got := canonicalise("line one\r\nline two\r\n", ""); got != "line one\nline two" {
		t.Errorf("canonicalise(CRLF) = %q", got)
	}
}

// A release binary reports its tag and a dev build "dev"; the fixture must
// hold for both.
func TestCanonicalise_Version(t *testing.T) {
	for _, in := range []string{"chb version dev", "chb version 0.1.0", "chb version 0.0.0-SNAPSHOT-872db02"} {
		if got := canonicalise(in, ""); got != "chb version <version>" {
			t.Errorf("canonicalise(%q) = %q", in, got)
		}
	}
}

// Preflight's routing lines follow the recording machine's provider, and
// their number varies with it. Two machines that print different routing
// runs must canonicalise to the same text, and the lines around the run
// must be kept.
func TestCanonicalise_CollapsesTheRoutingRun(t *testing.T) {
	head := "  ✓ prompt negative-evidence language — ok\n"
	tail := "  ✓ behavior fixture — fixtures/cli-behavior.jsonl\n"
	devBox := head +
		"  ✓ routing — no role → claude-haiku-4-5 on claude-cli, reasoning unset [a]\n" +
		"  ⚠ routing — off this machine: no role → claude-haiku-4-5 on claude-cli [a]: the CLI chooses its own host\n" +
		"  ✓ routing — network: web_fetch and shell reach the network [a]\n" + tail
	ci := head +
		"  ✓ routing — no role → gpt-5.1 on openai, reasoning unset [a]\n" + tail
	// canonicalise also trims the text's trailing newline.
	want := strings.TrimRight(head+"  <routing-check>\n"+tail, "\n")
	for name, in := range map[string]string{"dev box": devBox, "ci": ci} {
		if got := canonicalise(in, ""); got != want {
			t.Errorf("%s: canonicalised to %q, want %q", name, got, want)
		}
	}
}
