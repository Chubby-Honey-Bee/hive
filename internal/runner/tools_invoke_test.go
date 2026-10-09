package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// newMinimalRegistry builds a ToolRegistry with no real filesystem or DB, just
// a couple of hand-wired handlers, so Invoke can be tested in isolation.
func newMinimalRegistry() *ToolRegistry {
	r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	r.Handlers["echo"] = func(_ context.Context, in map[string]any) (string, error) {
		msg, _ := in["msg"].(string)
		return "echo: " + msg, nil
	}
	r.Handlers["fail"] = func(_ context.Context, _ map[string]any) (string, error) {
		return "", errors.New("handler failure")
	}
	return r
}

func TestInvoke(t *testing.T) {
	ctx := context.Background()

	t.Run("happy path: known tool returns output", func(t *testing.T) {
		r := newMinimalRegistry()
		out, isErr, err := r.Invoke(ctx, "echo", map[string]any{"msg": "hello"})
		if err != nil {
			t.Fatalf("expected no plumbing error, got %v", err)
		}
		if isErr {
			t.Fatal("expected isError=false for successful tool call")
		}
		if out != "echo: hello" {
			t.Fatalf("expected output %q, got %q", "echo: hello", out)
		}
	})

	// A tool the registry does not hold, one a node's allowlist removed
	// included, is refused as an error result the backend hands back to
	// the model: it names the tool and the tools the registry holds, and
	// the audit log records it.
	t.Run("unknown tool: isError=true, err nil, output is the refusal", func(t *testing.T) {
		for name, r := range map[string]*ToolRegistry{"with tools": newMinimalRegistry(), "no tools": {Handlers: map[string]ToolHandler{}}} {
			out, isErr, err := r.Invoke(ctx, "no_such_tool", nil)
			if err != nil || !isErr {
				t.Fatalf("%s: err %v, isError %v; want nil and true", name, err, isErr)
			}
			if !strings.Contains(out, "unknown tool: no_such_tool") {
				t.Errorf("%s: refusal %q does not name the tool", name, out)
			}
			for held := range r.Handlers {
				if !strings.Contains(out, held) {
					t.Errorf("%s: refusal %q does not name the held tool %s", name, out, held)
				}
			}
			if len(r.Handlers) == 0 && !strings.Contains(out, "no tools are available") {
				t.Errorf("%s: refusal %q does not say no tools are available", name, out)
			}
			if len(r.Log) != 1 || r.Log[0].Tool != "no_such_tool" || !r.Log[0].IsError || r.Log[0].Output != out {
				t.Errorf("%s: audit log %+v, want the one refused call", name, r.Log)
			}
		}
	})

	t.Run("handler error: isError=true, err nil, output is error message", func(t *testing.T) {
		r := newMinimalRegistry()
		out, isErr, err := r.Invoke(ctx, "fail", map[string]any{})
		if err != nil {
			t.Fatalf("expected nil plumbing error when handler fails, got %v", err)
		}
		if !isErr {
			t.Fatal("expected isError=true when handler returns an error")
		}
		if out != "handler failure" {
			t.Fatalf("expected output to be handler error message, got %q", out)
		}
	})

	t.Run("log populated on success", func(t *testing.T) {
		r := newMinimalRegistry()
		if len(r.Log) != 0 {
			t.Fatalf("expected empty log before invocation, got %d entries", len(r.Log))
		}
		r.Invoke(ctx, "echo", map[string]any{"msg": "logged"}) //nolint:errcheck
		if len(r.Log) != 1 {
			t.Fatalf("expected 1 log entry after invocation, got %d", len(r.Log))
		}
		inv := r.Log[0]
		if inv.Tool != "echo" {
			t.Errorf("Log.Tool: want %q, got %q", "echo", inv.Tool)
		}
		if inv.IsError {
			t.Error("Log.IsError: want false for successful call")
		}
		if inv.Output != "echo: logged" {
			t.Errorf("Log.Output: want %q, got %q", "echo: logged", inv.Output)
		}
		if inv.Timestamp == "" {
			t.Error("Log.Timestamp: want non-empty")
		}
		if inv.DurationS < 0 {
			t.Errorf("Log.DurationS: want >= 0, got %f", inv.DurationS)
		}
	})

	t.Run("log populated on handler error", func(t *testing.T) {
		r := newMinimalRegistry()
		r.Invoke(ctx, "fail", map[string]any{}) //nolint:errcheck
		if len(r.Log) != 1 {
			t.Fatalf("expected 1 log entry after failing invocation, got %d", len(r.Log))
		}
		inv := r.Log[0]
		if inv.Tool != "fail" {
			t.Errorf("Log.Tool: want %q, got %q", "fail", inv.Tool)
		}
		if !inv.IsError {
			t.Error("Log.IsError: want true for failed handler")
		}
		if inv.Output != "handler failure" {
			t.Errorf("Log.Output: want %q, got %q", "handler failure", inv.Output)
		}
	})

	t.Run("nil input map is marshalled without panic", func(t *testing.T) {
		r := newMinimalRegistry()
		// echo handler does a type-assert on in["msg"] which returns zero value for nil map
		out, isErr, err := r.Invoke(ctx, "echo", nil)
		if err != nil {
			t.Fatalf("unexpected plumbing error: %v", err)
		}
		if isErr {
			t.Fatal("expected isError=false")
		}
		// msg is "" so output should be "echo: "
		if out != "echo: " {
			t.Fatalf("expected %q, got %q", "echo: ", out)
		}
	})

	t.Run("multiple invocations accumulate in log", func(t *testing.T) {
		r := newMinimalRegistry()
		r.Invoke(ctx, "echo", map[string]any{"msg": "one"}) //nolint:errcheck
		r.Invoke(ctx, "echo", map[string]any{"msg": "two"}) //nolint:errcheck
		r.Invoke(ctx, "fail", map[string]any{})             //nolint:errcheck
		if len(r.Log) != 3 {
			t.Fatalf("expected 3 log entries, got %d", len(r.Log))
		}
	})
}
