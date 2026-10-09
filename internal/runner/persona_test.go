package runner

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	hive "github.com/Chubby-Honey-Bee/hive"
)

func TestLoadAgentPersona_EmptyAgentName(t *testing.T) {
	got, err := loadAgentPersona(".", "agents", "")
	if err != nil {
		t.Fatalf("loadAgentPersona empty name: %v", err)
	}
	if got != "" {
		t.Errorf("got %q; want empty for empty name", got)
	}
}

func TestLoadAgentPersona_MissingFile(t *testing.T) {
	dir := t.TempDir()
	got, err := loadAgentPersona(dir, "agents", "ghost")
	if err != nil {
		t.Fatalf("missing file should not error; got %v", err)
	}
	if got != "" {
		t.Errorf("got %q; want empty for missing file", got)
	}
}

func TestLoadAgentPersona_HappyPath(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "researcher.md"), []byte("# Researcher\n\nDo research."), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadAgentPersona(dir, "agents", "researcher")
	if err != nil {
		t.Fatalf("loadAgentPersona: %v", err)
	}
	if got != "# Researcher\n\nDo research." {
		t.Errorf("got %q; want full content", got)
	}
}

// With no --agents-dir and no agents/ under the project, the persona is the
// one the binary carries.
func TestLoadAgentPersona_CarriedCopyWhenNoAgentsDir(t *testing.T) {
	want, err := fs.ReadFile(hive.Agents, "researcher.md")
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadAgentPersona(t.TempDir(), "", "researcher")
	if err != nil {
		t.Fatalf("loadAgentPersona: %v", err)
	}
	if got != string(want) {
		t.Errorf("got %d bytes; want the carried researcher.md (%d bytes)", len(got), len(want))
	}
}

// agents/ under the project overrides the carried copy.
func TestLoadAgentPersona_ProjectAgentsDirOverridesTheCarriedCopy(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "researcher.md"), []byte("# mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadAgentPersona(dir, "", "researcher")
	if err != nil {
		t.Fatalf("loadAgentPersona: %v", err)
	}
	if got != "# mine" {
		t.Errorf("got %q; want the project's file", got)
	}
}

// An explicit --agents-dir is the only place read: a persona missing there
// is missing, carried copy or not. And a name the carried copy lacks is
// missing under the default too.
func TestLoadAgentPersona_ExplicitDirHasNoFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "personas"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := loadAgentPersona(dir, "personas", "researcher")
	if err != nil {
		t.Fatalf("loadAgentPersona: %v", err)
	}
	if got != "" {
		t.Errorf("explicit personas/ fell back to the carried copy: %d bytes", len(got))
	}
	got, err = loadAgentPersona(dir, "", "ghost")
	if err != nil {
		t.Fatalf("loadAgentPersona: %v", err)
	}
	if got != "" {
		t.Errorf("got %q; want empty for a persona nowhere", got)
	}
}

func TestLoadAgentPersona_PermissionError(t *testing.T) {
	// Pass an absurd path that is structurally invalid (NUL byte) to
	// surface a non-IsNotExist error from os.ReadFile.
	_, err := loadAgentPersona("/", "\x00bad", "agent")
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"abc", 10, "abc"},
		{"abcdefghij", 10, "abcdefghij"},
		// n bytes in all: the 3-byte ellipsis leaves room for 2 of the input.
		{"abcdefghijk", 5, "ab…"},
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q; want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

// The stored rationale is at most 64,000 bytes, ellipsis included, and never
// ends in half a character.
func TestTruncate_BoundAndRuneBoundary(t *testing.T) {
	const ellipsis = "…"
	for _, tc := range []struct {
		in string
		n  int
	}{
		{strings.Repeat("a", 70_000), 64_000},
		{strings.Repeat("é", 40_000), 64_000}, // 2-byte runes; 64000-3 is odd
		{strings.Repeat("€", 30_000), 64_000}, // 3-byte runes
	} {
		got := truncate(tc.in, tc.n)
		if len(got) > tc.n {
			t.Errorf("len=%d > %d", len(got), tc.n)
		}
		if !utf8.ValidString(got) || !strings.HasSuffix(got, ellipsis) {
			t.Errorf("truncate of %d bytes: invalid UTF-8 or no ellipsis at the end", len(tc.in))
		}
		body := strings.TrimSuffix(got, ellipsis)
		if !strings.HasPrefix(tc.in, body) {
			t.Errorf("kept text is not a prefix of the input")
		}
		// Nothing more fits: the next whole rune would exceed n.
		_, size := utf8.DecodeRuneInString(tc.in[len(body):])
		if len(body)+size+len(ellipsis) <= tc.n {
			t.Errorf("cut at %d bytes leaves room for another rune under %d", len(body), tc.n)
		}
	}
}
