//go:build !windows

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// agentRunSignalHelperEnv, set in this test binary's environment, makes
// TestAgentRunSignalHelperProcess run `chb agent-run` in its own process,
// as chb ask does, and then send that process SIGINT.
const agentRunSignalHelperEnv = "HIVE_AGENT_RUN_SIGNAL_HELPER"

func TestAgentRunSignalHelperProcess(t *testing.T) {
	if os.Getenv(agentRunSignalHelperEnv) != "1" {
		t.Skip("the child of TestAgentRun_ACtrlCAfterTheRunHasItsDefaultEffect")
	}
	dir := t.TempDir()
	s, err := db.NewStore(filepath.Join(dir, "sig.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	store = s
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte("name: sig\nnodes:\n  relay:\n    type: command\n    argv: [\"true\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := newAgentRunCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{wf, "--dir", dir, "--branch", "", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent-run: %v", err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
}

// Once agent-run returns, it releases SIGINT: a Ctrl-C in a process that
// goes on, as chb ask does after the run it dispatches, ends it.
func TestAgentRun_ACtrlCAfterTheRunHasItsDefaultEffect(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestAgentRunSignalHelperProcess$")
	cmd.Env = append(os.Environ(), agentRunSignalHelperEnv+"=1", runAsChbEnv+"=")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(time.Minute, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	_ = cmd.Wait()
	ws := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Fatalf("the process exited with status %d (signaled %v by %v), want killed by the SIGINT sent after agent-run returned\n%s",
			ws.ExitStatus(), ws.Signaled(), ws.Signal(), out.String())
	}
}
