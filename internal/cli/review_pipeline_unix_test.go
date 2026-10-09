//go:build !windows

package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The run id travels through a real pipe from the child's stderr.
func TestRunShellTo_ReadsTheRunIDFromTheChild(t *testing.T) {
	p := &reviewPipeline{target: t.TempDir(), stdout: io.Discard, stderr: io.Discard}
	sniff := &runIDWriter{w: io.Discard}
	if err := p.runShellTo(sniff, "/bin/sh", "-c", `echo "`+agentRunIDPrefix+`2" >&2`); err != nil {
		t.Fatal(err)
	}
	if sniff.id != 2 {
		t.Errorf("id = %d, want 2", sniff.id)
	}
}

// A SIGTERM that reaches the pipeline while a stage runs must reach the
// stage's child as SIGTERM, which agent-run handles by cancelling its run,
// and the stage must fail so the pipeline stops.
func TestRunShellTo_PassesASignalOnToTheStage(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	script := `trap 'echo TERM > "$0"; exit 0' TERM; echo up > "$0.ready"; while :; do sleep 0.05; done`
	p := &reviewPipeline{target: dir, stdout: io.Discard, stderr: io.Discard}

	done := make(chan error, 1)
	go func() { done <- p.runShellTo(io.Discard, "/bin/sh", "-c", script, marker) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stage's child never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "signal") {
			t.Errorf("runShellTo = %v, want the stage to fail as stopped by a signal", err)
		}
	case <-time.After(stageStopGrace + 5*time.Second):
		t.Fatal("the stage kept running after the pipeline was signalled")
	}
	got, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(got)) != "TERM" {
		t.Errorf("the child did not receive SIGTERM (marker %q, %v)", got, err)
	}
}
