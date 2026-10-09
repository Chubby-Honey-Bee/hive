package runner

import (
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// TestNewClaudeRunner_Defaults verifies that NewClaudeRunner populates the
// struct fields with the documented defaults.
func TestNewClaudeRunner_Defaults(t *testing.T) {
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	r := NewClaudeRunner("", "sonnet", "you are helpful", reg)

	if r == nil {
		t.Fatal("expected non-nil ClaudeRunner")
	}
	if r.MaxTurns != 30 {
		t.Errorf("MaxTurns: want 30, got %d", r.MaxTurns)
	}
	if r.MaxTokens != 8192 {
		t.Errorf("MaxTokens: want 8192, got %d", r.MaxTokens)
	}
	if r.PerCallTTL != 5*time.Minute {
		t.Errorf("PerCallTTL: want 5m, got %v", r.PerCallTTL)
	}
	if r.System != "you are helpful" {
		t.Errorf("System: want %q, got %q", "you are helpful", r.System)
	}
	if r.Registry != reg {
		t.Error("Registry: want same pointer as provided")
	}
}

// TestNewClaudeRunner_ModelAlias verifies that model aliases are resolved.
func TestNewClaudeRunner_ModelAlias(t *testing.T) {
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}

	cases := []struct {
		alias string
		want  anthropic.Model
	}{
		{"sonnet", anthropic.ModelClaudeSonnet4_6},
		{"opus", anthropic.Model("claude-opus-4-8")},
		{"opus-4-6", anthropic.ModelClaudeOpus4_6},
		{"haiku", anthropic.Model("claude-haiku-4-5")},
		{"haiku-4-5", anthropic.ModelClaudeHaiku4_5_20251001},
		{"", anthropic.ModelClaudeSonnet4_6},
		{"claude-opus-4-8", anthropic.Model("claude-opus-4-8")}, // pass-through
	}

	for _, tc := range cases {
		t.Run("alias="+tc.alias, func(t *testing.T) {
			r := NewClaudeRunner("", tc.alias, "", reg)
			if r.Model != tc.want {
				t.Errorf("Model: want %q, got %q", tc.want, r.Model)
			}
		})
	}
}

// TestNewClaudeRunner_APIKey verifies that a non-empty apiKey does not panic
// and that a nil registry is accepted (Run would return an error later).
func TestNewClaudeRunner_APIKey(t *testing.T) {
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	r := NewClaudeRunner("sk-test-key", "haiku", "sys", reg)
	if r == nil {
		t.Fatal("expected non-nil ClaudeRunner with explicit apiKey")
	}
}

// TestNewClaudeRunner_NilRegistry verifies that NewClaudeRunner does not panic
// when passed a nil registry; error surfaces only at Run time.
func TestNewClaudeRunner_NilRegistry(t *testing.T) {
	defer func() {
		if rc := recover(); rc != nil {
			t.Fatalf("NewClaudeRunner panicked with nil registry: %v", rc)
		}
	}()
	r := NewClaudeRunner("", "sonnet", "", nil)
	if r == nil {
		t.Fatal("expected non-nil ClaudeRunner")
	}
	if r.Registry != nil {
		t.Error("expected Registry to be nil")
	}
}
