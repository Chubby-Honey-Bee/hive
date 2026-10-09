//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package endpointslot

// Tests for slots shared across processes: helper processes (TestMain's
// helper) take a server's slots through the lock files while the test
// process takes them too. No server is called; a slot is only held.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ceiling ends a wait that a correct implementation ends on its own. It
// fails a test only when something is stuck, never for a slow machine.
const ceiling = 2 * time.Minute

// startHelper starts this test binary as a helper with args. Its stdin is a
// pipe the test holds, and its end closes it.
func startHelper(t *testing.T, args ...string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	cmd.Stderr = &bytes.Buffer{}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd, stdin
}

// waitExit waits for a helper to exit and fails the test unless it exited 0.
func waitExit(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper %v: %v\n%s", cmd.Args[1:], err, cmd.Stderr)
		}
	case <-time.After(ceiling):
		t.Fatalf("helper %v still running after %s", cmd.Args[1:], ceiling)
	}
}

// waitFor polls cond until it holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(ceiling); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("still waiting after %s for %s", ceiling, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readLines(path string) []string {
	b, _ := os.ReadFile(path)
	return strings.FieldsFunc(string(b), func(r rune) bool { return r == '\n' })
}

// checkLog walks a helpers' log whose first holder is first ("" for none)
// and fails the test when a "start" comes while more than n hold a slot, or
// an "end" or "release" from one that holds none. It returns the starts per
// helper.
func checkLog(t *testing.T, path, first string, n int) map[string]int {
	t.Helper()
	holders := map[string]bool{}
	if first != "" {
		holders[first] = true
	}
	starts := map[string]int{}
	for i, line := range readLines(path) {
		verb, id, _ := strings.Cut(line, " ")
		switch verb {
		case "start":
			if len(holders) >= n {
				t.Errorf("log line %d: %s took a slot while %d of %d were held", i+1, id, len(holders), n)
			}
			holders[id] = true
			starts[id]++
		case "end", "release":
			if !holders[id] {
				t.Errorf("log line %d: %s let go of a slot it did not hold", i+1, id)
			}
			delete(holders, id)
		}
	}
	return starts
}

// TestAcquire_OneSlotAcrossProcessesIsTakenInTurn: two processes cycling on
// a server with one slot, and this one holding it when they start, never
// hold it at once, and each gets every turn it asked for.
func TestAcquire_OneSlotAcrossProcessesIsTakenInTurn(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	endpoint := fresh(t, "http://127.0.0.1:40101/v1")
	const cycles = 5
	work := t.TempDir()
	logPath := filepath.Join(work, "log")

	release, n, err := Acquire(context.Background(), endpoint)
	if err != nil || n != 1 {
		t.Fatalf("Acquire: n=%d err=%v, want the loopback default of one slot", n, err)
	}
	ids := []string{"a", "b"}
	var cmds []*exec.Cmd
	for _, id := range ids {
		cmd, _ := startHelper(t, "cycle", endpoint, work, id, strconv.Itoa(cycles))
		cmds = append(cmds, cmd)
	}
	for _, id := range ids {
		waitFor(t, "helper "+id+" to start", func() bool { return exists(filepath.Join(work, "ready-"+id)) })
	}
	// Room for a helper that took no turn to show it before this process
	// lets go.
	time.Sleep(10 * holdCycle)
	appendLine(logPath, "release parent")
	release()
	for _, cmd := range cmds {
		waitExit(t, cmd)
	}

	starts := checkLog(t, logPath, "parent", n)
	for _, id := range ids {
		if starts[id] != cycles {
			t.Errorf("helper %s took the slot %d times, want %d", id, starts[id], cycles)
		}
	}
}

// TestAcquire_TwoSlotsAcrossThreeProcesses: with two slots, three processes
// cycling never hold more than two at once.
func TestAcquire_TwoSlotsAcrossThreeProcesses(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "2")
	endpoint := fresh(t, "http://127.0.0.1:40102/v1")
	const cycles = 4
	work := t.TempDir()
	ids := []string{"a", "b", "c"}
	var cmds []*exec.Cmd
	for _, id := range ids {
		cmd, _ := startHelper(t, "cycle", endpoint, work, id, strconv.Itoa(cycles))
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		waitExit(t, cmd)
	}
	starts := checkLog(t, filepath.Join(work, "log"), "", Limit(endpoint))
	for _, id := range ids {
		if starts[id] != cycles {
			t.Errorf("helper %s took a slot %d times, want %d", id, starts[id], cycles)
		}
	}
}

// held reports whether another open holds path's lock. When none does, it
// takes the lock and lets go of it at once.
func held(t *testing.T, path string) bool {
	t.Helper()
	f, err := tryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		unlock(f)
		return false
	}
	return true
}

// TestAcquire_TheQueuedWaiterGoesNext: a call that starts waiting while
// another process holds the slot holds next.lock as it waits, so when that
// process lets go and at once asks again, the waiting call goes first.
func TestAcquire_TheQueuedWaiterGoesNext(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	endpoint := fresh(t, "http://127.0.0.1:40103/v1")
	work := t.TempDir()
	logPath := filepath.Join(work, "log")
	cmd, stdin := startHelper(t, "again", endpoint, work, "a")
	waitFor(t, "the helper to hold the slot", func() bool { return exists(filepath.Join(work, "held")) })

	ctx, cancel := context.WithTimeout(context.Background(), ceiling)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		release, _, err := Acquire(ctx, endpoint)
		if err == nil {
			appendLine(logPath, "start parent")
			appendLine(logPath, "release parent")
			release()
		}
		done <- err
	}()
	// The helper holds the slot and waits for none, so only the waiting
	// call can hold next.lock.
	next := lookup(endpoint).files + "-next.lock"
	waitFor(t, "the waiting call to hold next.lock", func() bool { return held(t, next) })
	if _, err := io.WriteString(stdin, "go\n"); err != nil {
		t.Fatal(err)
	}
	waitExit(t, cmd)
	if err := <-done; err != nil {
		t.Fatalf("the waiting call: %v", err)
	}

	want := []string{"start parent", "release parent", "start a", "end a"}
	if got := readLines(logPath); !slices.Equal(got, want) {
		t.Errorf("turns:\n%q\nwant the waiting call before the helper's next call:\n%q", got, want)
	}
}

// TestAcquire_AStoppedWaiterDoesNotHoldBackAFreeSlot: a process stopped
// (SIGSTOP, as Ctrl-Z sends) while it waits keeps next.lock, but once the
// slot is free, a call in another process still takes it.
func TestAcquire_AStoppedWaiterDoesNotHoldBackAFreeSlot(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	endpoint := fresh(t, "http://127.0.0.1:40107/v1")
	holdWork, waitWork := t.TempDir(), t.TempDir()
	holder, holderIn := startHelper(t, "hold", endpoint, holdWork)
	waitFor(t, "the first helper to hold the slot", func() bool { return exists(filepath.Join(holdWork, "held")) })
	waiter, _ := startHelper(t, "hold", endpoint, waitWork)
	// The first helper holds the slot and waits for none, so only the
	// second can hold next.lock.
	next := lookup(endpoint).files + "-next.lock"
	waitFor(t, "the second helper to queue", func() bool { return held(t, next) })
	if err := waiter.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	holderIn.Close()
	waitExit(t, holder)
	if !held(t, next) {
		t.Fatal("precondition: the stopped helper no longer holds next.lock")
	}

	ctx, cancel := context.WithTimeout(context.Background(), ceiling)
	defer cancel()
	release, _, err := Acquire(ctx, endpoint)
	if err != nil {
		t.Fatalf("with the slot free and its queued waiter stopped: %v", err)
	}
	release()
}

// TestAcquire_ALargerBoundTakesAFileASmallerOnesWaiterCannot: while a process
// with one slot waits for slot file 0, holding next.lock, a process with two
// takes file 1, which that waiter never tries.
func TestAcquire_ALargerBoundTakesAFileASmallerOnesWaiterCannot(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "1")
	endpoint := fresh(t, "http://127.0.0.1:40109/v1")
	holdWork, waitWork := t.TempDir(), t.TempDir()
	startHelper(t, "hold", endpoint, holdWork)
	waitFor(t, "the first helper to hold file 0", func() bool { return exists(filepath.Join(holdWork, "held")) })
	startHelper(t, "hold", endpoint, waitWork)

	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "2")
	s := lookup(endpoint)
	waitFor(t, "the second helper to queue", func() bool { return held(t, s.files+"-next.lock") })
	ctx, cancel := context.WithTimeout(context.Background(), ceiling)
	defer cancel()
	release, n, err := Acquire(ctx, endpoint)
	if err != nil || n != 2 {
		t.Fatalf("Acquire: n=%d err=%v, want file 1 while the helpers hold file 0 and next.lock", n, err)
	}
	defer release()
	if !held(t, s.slotFile(1)) || !held(t, s.slotFile(0)) {
		t.Errorf("want this call on file 1 and the first helper still on file 0")
	}
}

// TestAcquire_AKilledHolderFreesItsSlot: a process killed while it holds the
// slot leaves its lock file behind, and the kernel's release of its lock
// frees the slot.
func TestAcquire_AKilledHolderFreesItsSlot(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	endpoint := fresh(t, "http://127.0.0.1:40104/v1")
	work := t.TempDir()
	cmd, _ := startHelper(t, "hold", endpoint, work)
	waitFor(t, "the helper to hold the slot", func() bool { return exists(filepath.Join(work, "held")) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*holdCycle)
	defer cancel()
	if _, _, err := Acquire(ctx, endpoint); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("while another process holds the slot: err = %v, want its deadline's", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if file := lookup(endpoint).files + "-0.lock"; !exists(file) {
		t.Fatalf("%s is gone; the killed holder could not have removed it", file)
	}

	ctx, cancel = context.WithTimeout(context.Background(), ceiling)
	defer cancel()
	release, _, err := Acquire(ctx, endpoint)
	if err != nil {
		t.Fatalf("after the holder was killed: %v", err)
	}
	release()
}

// TestAcquire_CancelledWhileAnotherProcessHoldsTheSlot: a call waiting for a
// slot another process holds returns when its context is cancelled, while
// the slot is still held, and leaves the queue free for the next call.
func TestAcquire_CancelledWhileAnotherProcessHoldsTheSlot(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	endpoint := fresh(t, "http://127.0.0.1:40105/v1")
	work := t.TempDir()
	cmd, stdin := startHelper(t, "hold", endpoint, work)
	waitFor(t, "the helper to hold the slot", func() bool { return exists(filepath.Join(work, "held")) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		release, _, err := Acquire(ctx, endpoint)
		if err == nil {
			release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Acquire returned while another process held the slot: %v", err)
	case <-time.After(10 * holdCycle):
	}
	cancel()
	// The helper holds its slot until its stdin closes, so Acquire returns
	// now for the cancellation alone.
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want the cancellation's", err)
		}
	case <-time.After(ceiling):
		t.Fatalf("Acquire still waiting %s after its context was cancelled", ceiling)
	}

	stdin.Close()
	waitExit(t, cmd)
	ctx, cancel = context.WithTimeout(context.Background(), ceiling)
	defer cancel()
	release, _, err := Acquire(ctx, endpoint)
	if err != nil {
		t.Fatalf("after the cancelled call and the holder let go: %v", err)
	}
	release()
}

// TestAcquire_ALockFileThatFailsLaterLeavesTheProcessBound: when the lock
// directory cannot be made again mid-run, calls go ahead bound in this
// process, logged once; once a lock file works again, calls share the
// slots again, logged once.
func TestAcquire_ALockFileThatFailsLaterLeavesTheProcessBound(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	dir := filepath.Join(t.TempDir(), "slots")
	t.Setenv("HIVE_ENDPOINT_SLOT_DIR", dir)
	log := captureLog(t)
	endpoint := fresh(t, "http://127.0.0.1:40106/v1")

	release, _, err := Acquire(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
	s := lookup(endpoint)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, reason := os.OpenFile(s.files+"-next.lock", os.O_RDWR|os.O_CREATE, 0o600)
	if reason == nil {
		t.Fatal("precondition: a lock file under a regular file opened")
	}

	release, _, err = Acquire(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Acquire once the lock files fail: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*holdCycle)
	defer cancel()
	if _, _, err := Acquire(ctx, endpoint); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a second call while the first holds the one slot: err = %v, want its deadline's", err)
	}
	release()
	release, _, err = Acquire(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()

	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	release, _, err = Acquire(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Acquire once the directory can be made again: %v", err)
	}
	if !held(t, s.slotFile(0)) {
		t.Errorf("the call does not hold %s once the lock files work again", s.slotFile(0))
	}
	release()

	key := Key(endpoint)
	want := sharedLine(key, 1, s.files) +
		fmt.Sprintf("endpoint slots: %s: a lock file failed (%v); until one works again, calls are counted in this process only\n", key, reason) +
		fmt.Sprintf("endpoint slots: %s: the lock files work again; calls are shared with other chb processes\n", key)
	if log.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", log.String(), want)
	}
}

// TestAcquire_ALockDirectoryRemovedMidRunIsMadeAgain: when a cache cleaner
// removes the lock directory between calls, the next call makes it again
// and holds a lock file, with nothing logged.
func TestAcquire_ALockDirectoryRemovedMidRunIsMadeAgain(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	dir := filepath.Join(t.TempDir(), "slots")
	t.Setenv("HIVE_ENDPOINT_SLOT_DIR", dir)
	log := captureLog(t)
	endpoint := fresh(t, "http://127.0.0.1:40110/v1")

	release, _, err := Acquire(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	release, _, err = Acquire(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	s := lookup(endpoint)
	if !held(t, s.slotFile(0)) {
		t.Errorf("the call does not hold %s in the directory made again", s.slotFile(0))
	}
	release()
	if want := sharedLine(Key(endpoint), 1, s.files); log.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", log.String(), want)
	}
}

// TestAcquire_AWaitForAnotherProcessNamesIt: a call waiting for a slot
// another process holds logs once, past noticeAfter, the pid that holds it,
// and a call whose context ends first fails naming that pid.
func TestAcquire_AWaitForAnotherProcessNamesIt(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	noticeAfter = 0
	t.Cleanup(func() { noticeAfter = 5 * time.Second })
	log := captureLog(t)
	endpoint := fresh(t, "http://127.0.0.1:40108/v1")
	work := t.TempDir()
	cmd, _ := startHelper(t, "hold", endpoint, work)
	waitFor(t, "the helper to hold the slot", func() bool { return exists(filepath.Join(work, "held")) })

	holders := fmt.Sprintf("other chb processes hold (pid %d)", cmd.Process.Pid)
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*holdCycle)
		_, _, err := Acquire(ctx, endpoint)
		cancel()
		want := fmt.Sprintf("waiting for a slot %s: %v", holders, context.DeadlineExceeded)
		if !errors.Is(err, context.DeadlineExceeded) || err.Error() != want {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
	key := Key(endpoint)
	want := sharedLine(key, 1, lookup(endpoint).files) +
		fmt.Sprintf("endpoint slots: %s: waiting for a slot %s; the call's timeout starts once it has one\n", key, holders)
	if log.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", log.String(), want)
	}
}

// TestAcquire_ARelativeSlotDirectoryIsRefused: a relative
// HIVE_ENDPOINT_SLOT_DIR would be a different directory for processes
// started in different ones, so the process keeps its own slots and says
// why, and makes no directory.
func TestAcquire_ARelativeSlotDirectoryIsRefused(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	t.Chdir(t.TempDir())
	t.Setenv("HIVE_ENDPOINT_SLOT_DIR", "slots")
	log := captureLog(t)
	endpoint := fresh(t, "http://127.0.0.1:40111/v1")
	release, _, err := Acquire(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
	reason := fmt.Errorf("HIVE_ENDPOINT_SLOT_DIR=%s is not an absolute path", "slots")
	if want := localLine(Key(endpoint), 1, reason); log.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", log.String(), want)
	}
	if exists("slots") {
		t.Error("a relative lock directory was made")
	}
}

// TestAcquire_ASlotDirectoryUnderTildeIsInTheHomeDirectory: a leading ~,
// which a shell would expand but an MCP client's env block does not, is the
// user's home directory.
func TestAcquire_ASlotDirectoryUnderTildeIsInTheHomeDirectory(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HIVE_ENDPOINT_SLOT_DIR", "~/slots")
	log := captureLog(t)
	endpoint := fresh(t, "http://127.0.0.1:40112/v1")
	release, _, err := Acquire(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
	sum := sha256.Sum256([]byte(Key(endpoint)))
	files := filepath.Join(home, "slots", hex.EncodeToString(sum[:8]))
	if want := sharedLine(Key(endpoint), 1, files); log.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", log.String(), want)
	}
}

// TestLockOpen_AFileNoLongerAtItsPathIsNotHeld: a caller that opened a slot
// file before its holder let go, and locks it after a third caller made a
// new file at the path, does not hold the slot: the third caller does.
func TestLockOpen_AFileNoLongerAtItsPathIsNotHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "0.lock")
	holder, err := tryLock(path)
	if holder == nil || err != nil {
		t.Fatalf("tryLock: %v, %v", holder, err)
	}
	stale, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	unlock(holder)
	third, err := tryLock(path)
	if third == nil || err != nil {
		t.Fatalf("tryLock after the holder let go: %v, %v", third, err)
	}
	defer unlock(third)
	if got, err := lockOpen(stale); got != nil || err != nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("the file its holder removed counts as held (err %v) beside the file now at its path", err)
	}
}

// TestUnlock_RemovesTheFileBeforeItLetsGo: at the moment unlock lets go of
// the lock, a caller that had the file open cannot take the slot through
// it, since the file is no longer at its path.
func TestUnlock_RemovesTheFileBeforeItLetsGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "0.lock")
	f, err := tryLock(path)
	if f == nil || err != nil {
		t.Fatalf("tryLock: %v, %v", f, err)
	}
	stale, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	closeFile = func(g *os.File) error {
		err := g.Close()
		if got, _ := lockOpen(stale); got != nil {
			got.Close()
			t.Error("a caller that had the file open took the slot as unlock let go")
		}
		return err
	}
	t.Cleanup(func() { closeFile = (*os.File).Close })
	unlock(f)
}
