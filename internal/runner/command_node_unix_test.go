//go:build !windows

package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A cancelled run kills the command's whole process group. Killing only the
// direct child would leave its own children running, as `go test ./...`
// leaves test binaries and `chb validate` leaves chb subprocesses.
func TestCommandNode_CancelKillsTheProcessGroup(t *testing.T) {
	t.Setenv(commandHelperEnv, "1")
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "grandchild.pid")
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(`
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: [chb, spawn, "`+pidfile+`"]
edges: []
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newTempStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, store, Config{
			WorkflowYAML: wf, ProjectDir: dir, DBPath: store.Path,
			MaxIterations: 5, Backend: &stubBackend{}, Log: io.Discard,
		})
		done <- err
	}()

	var pid int
	deadline := time.Now().Add(20 * time.Second)
	for pid == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the command never started its child")
		}
		if b, err := os.ReadFile(pidfile); err == nil && len(b) > 0 {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after the cancel")
	}
	// The grandchild is reparented and reaped once killed; poll until it is
	// gone.
	for deadline = time.Now().Add(10 * time.Second); ; {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d still running after the run was cancelled (kill 0: %v)", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A cancelled run puts a command node it stopped back to pending: the
// program did not fail, the run stopped, so the node spends no retry and
// --resume runs it again.
func TestCommandNode_ACancelledRunReleasesTheNode(t *testing.T) {
	t.Setenv(commandHelperEnv, "1")
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "grandchild.pid")
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(`
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: [chb, spawn, "`+pidfile+`"]
edges: []
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newTempStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Run(ctx, store, Config{
			WorkflowYAML: wf, ProjectDir: dir, DBPath: store.Path,
			MaxIterations: 5, Backend: &stubBackend{}, Log: io.Discard,
		})
		done <- outcome{res, err}
	}()

	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(pidfile); err == nil && len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the command never started its child")
		}
	}
	cancel()
	var out outcome
	select {
	case out = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after the cancel")
	}
	if !errors.Is(out.err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", out.err)
	}
	if out.res == nil || out.res.RunID == 0 {
		t.Fatalf("Run returned no run id (result %+v)", out.res)
	}
	states, err := store.Workflows().GetWorkflowNodeStates(out.res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Status != "pending" || s.Attempt != 0 {
			t.Errorf("after the cancel node %s is %s at attempt %d, want pending at attempt 0", s.NodeName, s.Status, s.Attempt)
		}
	}
}
