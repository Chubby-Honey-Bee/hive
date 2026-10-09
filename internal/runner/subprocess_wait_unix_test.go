//go:build !windows

package runner

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A command that leaves a child holding its output, as any `cmd &` does,
// does not hold the shell tool past its context. Each call here would
// otherwise wait out its child's 30 s sleep: cancelling ends the child too,
// so the wait for the output does not go on until the child closes it.
func TestShellTool_ABackgroundedChildDoesNotHoldTheCall(t *testing.T) {
	cases := []struct {
		name, command string
		within        time.Duration
	}{
		// The shell exits at once and leaves sleep holding the output: the
		// wait for it ends 5 s after the exit.
		{"the shell exits and its child holds the output", "sleep 30 & echo started $!", 15 * time.Second},
		// The shell still runs at the 1 s deadline: its whole group is
		// killed, so the output closes then, not 5 s after.
		{"the shell still runs at the deadline", "sleep 30 & wait", 5 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
			r.registerShell(t.TempDir(), NewFSRecorder(), hostShell())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			start := time.Now()
			out, _, err := r.Invoke(ctx, "shell", map[string]any{"command": c.command})
			elapsed := time.Since(start)
			// The first command prints its child's pid: a sleep the call
			// left behind is ended here.
			if f := strings.Fields(out); len(f) > 0 {
				if pid, perr := strconv.Atoi(f[len(f)-1]); perr == nil && pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
			if err != nil {
				t.Fatalf("shell: %v", err)
			}
			if elapsed > c.within {
				t.Fatalf("the call returned after %s, want within %s of its start: %q", elapsed.Round(time.Millisecond), c.within, out)
			}
		})
	}
}

// The claude and gemini CLIs are held to their context the same way: each
// fake here is still running at the 1 s deadline, with a child of its own
// holding stdout, and the call returns once the whole group is killed.
func TestCLIBackends_ABackgroundedChildDoesNotHoldTheCall(t *testing.T) {
	dir := t.TempDir()
	script := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	claude := script("claude", "cat > /dev/null\nsleep 30 &\nwait\n")
	// --help answers at once, as gemini-cli's does, so the call reaches
	// the CLI itself.
	gemini := script("gemini", "if [ \"$1\" = --help ]; then echo usage; exit 0; fi\ncat > /dev/null\nsleep 30 &\nwait\n")
	backends := []struct {
		name    string
		backend LLMBackend
	}{
		{"claude CLI", &CLIBackend{CLIPath: claude, ScratchDir: t.TempDir(), AllowedTools: []string{"Read"}}},
		{"gemini CLI", &GeminiCLIBackend{CLIPath: gemini}},
	}
	for _, c := range backends {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			start := time.Now()
			_, err := c.backend.Run(ctx, RunRequest{Prompt: "hi"})
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("the call returned after %s, want within 5s of its start (err: %v)", elapsed.Round(time.Millisecond), err)
			}
			if err == nil {
				t.Fatal("a call cut off at its deadline returned no error")
			}
		})
	}
}
