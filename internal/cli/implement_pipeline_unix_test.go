//go:build !windows

package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeStagesEnv makes this test binary stand in for chb when the implement
// pipeline re-executes itself for a stage. It names a directory: each stage
// appends its arguments to stages.log there. agent-run writes agent-run.ready
// and then waits for SIGTERM; every other stage exits 0 at once.
const fakeStagesEnv = "CHB_TEST_FAKE_STAGES"

func init() {
	dir := os.Getenv(fakeStagesEnv)
	if dir == "" {
		return
	}
	if f, err := os.OpenFile(filepath.Join(dir, "stages.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.WriteString(strings.Join(os.Args[1:], " ") + "\n")
		_ = f.Close()
	}
	if len(os.Args) > 1 && os.Args[1] == "agent-run" {
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		_ = os.WriteFile(filepath.Join(dir, "agent-run.ready"), nil, 0o644)
		select {
		case <-term:
		case <-time.After(30 * time.Second):
		}
		os.Exit(1)
	}
	os.Exit(0)
}

// implement treats a failed agent-run as non-fatal so it can still report
// run totals, but an agent-run a signal stopped fails the command: it
// reports the totals, then fails, rather than print IMPLEMENT COMPLETE and
// exit 0.
func TestImplement_ASignalDuringAgentRunFailsTheCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(fakeStagesEnv, dir)
	t.Setenv("HIVE_DB_PATH", "")
	findings := filepath.Join(dir, "findings.json")
	if err := os.WriteFile(findings, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	p := &implementPipeline{
		findingsPath: findings,
		severity:     "critical,high",
		maxFixes:     5,
		maxCostUSD:   1,
		maxIter:      10,
		model:        "sonnet",
		repairModel:  "opus",
		branch:       "self-implement/test",
		noPR:         true,
		workspaceDir: filepath.Join(dir, "ws"),
		stdout:       io.Discard,
		stderr:       &stderr,
	}

	done := make(chan error, 1)
	go func() { done <- p.run() }()

	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "agent-run.ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the pipeline never reached agent-run")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	var err error
	select {
	case err = <-done:
	case <-time.After(stageStopGrace + 20*time.Second):
		t.Fatal("implement kept running after it was signalled")
	}
	if err == nil {
		t.Fatal("an implement stopped by a signal returned nil, so chb implement exits 0")
	}
	if !errors.Is(err, errStageStopped) {
		t.Errorf("implement = %v, want the agent-run stage's signal stop", err)
	}
	if strings.Contains(stderr.String(), "IMPLEMENT COMPLETE") {
		t.Error("an interrupted implement still printed IMPLEMENT COMPLETE")
	}
	log, rerr := os.ReadFile(filepath.Join(dir, "stages.log"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	stages := strings.Split(strings.TrimSpace(string(log)), "\n")
	if last := stages[len(stages)-1]; last != "run-totals 1" {
		t.Errorf("last stage = %q, want the run-totals report after the stopped agent-run (stages: %q)", last, stages)
	}
}
