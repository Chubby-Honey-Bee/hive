//go:build !windows

package mcp

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// serverHelperEnv, set in this test binary's environment, makes
// TestMCPServerHelperProcess serve as chb-mcp over the process's stdin and
// stdout, as cmd/chb-mcp does: the tests below start it as a child to see
// what a broken pipe or a signal does to the process.
const serverHelperEnv = "HIVE_MCP_TEST_SERVER"

func TestMCPServerHelperProcess(t *testing.T) {
	if os.Getenv(serverHelperEnv) != "1" {
		t.Skip("the chb-mcp child of the tests in this file")
	}
	Main("dev")
}

// startServer starts this binary as chb-mcp on its own database. Its stdin
// is the returned writer; its stdout and stderr are what the caller gives.
func startServer(t *testing.T, stdout, stderr io.Writer, env ...string) (*exec.Cmd, *os.File) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPServerHelperProcess$")
	cmd.Env = append(os.Environ(), append([]string{
		serverHelperEnv + "=1",
		asChbEnv + "=",
		"HIVE_DB_PATH=" + filepath.Join(t.TempDir(), "e.db"),
	}, env...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	inR.Close()
	t.Cleanup(func() {
		inW.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, inW
}

// waitStatus waits for cmd to exit, within d, and returns how it ended.
func waitStatus(t *testing.T, cmd *exec.Cmd, d time.Duration) syscall.WaitStatus {
	t.Helper()
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("chb-mcp did not exit within %s", d)
	}
	return cmd.ProcessState.Sys().(syscall.WaitStatus)
}

// A host that goes away with a request in flight leaves the server writing
// to a pipe nothing reads. The write fails and is dropped, and the server
// runs its ordered shutdown when stdin closes and exits 0.
func TestMain_AHostThatClosesStdoutDoesNotKillTheServer(t *testing.T) {
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd, stdin := startServer(t, outW, nil)
	outW.Close()
	outR.Close()
	if _, err := stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	stdin.Close()
	ws := waitStatus(t, cmd, time.Minute)
	if ws.Signaled() {
		t.Fatalf("chb-mcp was killed by %v when its answer met a closed pipe", ws.Signal())
	}
	if ws.ExitStatus() != 0 {
		t.Fatalf("chb-mcp exited %d, want 0 after its shutdown", ws.ExitStatus())
	}
}

// The programs the server starts keep SIGPIPE's default, which an ignored
// SIGPIPE would not: an ignored signal stays ignored across exec, so in a
// pipeline `yes | head -1` an agent's shell runs, yes would see EPIPE and
// exit 1 rather than end with SIGPIPE (128+13). The chb here is the
// program chb_preflight starts; it records how its pipeline's yes ended.
func TestMain_ProgramsTheServerStartsKeepSIGPIPE(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "yes-status")
	fake := "#!/usr/bin/env bash\nyes | head -1 >/dev/null\necho \"${PIPESTATUS[0]}\" > \"$MARK\"\n"
	if err := os.WriteFile(filepath.Join(dir, "chb"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, stdin := startServer(t, nil, nil, "MARK="+mark, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"chb_preflight","arguments":{"workflow_yaml":"x.yaml"}}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	stdin.Close()
	waitStatus(t, cmd, time.Minute)
	got, err := os.ReadFile(mark)
	if err != nil {
		t.Fatalf("chb never ran: %v", err)
	}
	if status := strings.TrimSpace(string(got)); status != "141" {
		t.Errorf("yes in a pipeline of a program chb-mcp started exited %s, want 141: killed by SIGPIPE", status)
	}
}

// The first SIGINT starts the ordered shutdown; a second one, while the
// drain waits on a request, ends the process at once with SIGINT's default
// effect. The request is chb_preflight, held by a chb that waits for its
// parent to be gone.
func TestMain_ASecondSignalEndsTheDrain(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "started")
	fake := "#!/bin/sh\ntouch \"$MARK\"\nwhile kill -0 $PPID 2>/dev/null; do sleep 0.1; done\n"
	if err := os.WriteFile(filepath.Join(dir, "chb"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd, stdin := startServer(t, nil, errW, "MARK="+mark, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	errW.Close()
	lines := make(chan string, 64)
	go func() {
		defer errR.Close()
		sc := bufio.NewScanner(errR)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			default:
			}
		}
	}()
	if _, err := stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"chb_preflight","arguments":{"workflow_yaml":"x.yaml"}}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(mark); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the request never reached chb")
		}
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	for timeout := time.After(30 * time.Second); ; {
		var line string
		select {
		case line = <-lines:
		case <-timeout:
			t.Fatal("chb-mcp never said it was shutting down")
		}
		if strings.Contains(line, "shutting down") {
			break
		}
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	ws := waitStatus(t, cmd, time.Minute)
	if !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Fatalf("after the second SIGINT chb-mcp exited with status %d (signaled %v), want killed by SIGINT", ws.ExitStatus(), ws.Signaled())
	}
}
