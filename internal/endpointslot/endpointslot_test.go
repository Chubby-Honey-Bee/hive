package endpointslot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sharedLine and localLine are the lines a server's first call logs.
func sharedLine(name string, n int, files string) string {
	return fmt.Sprintf("endpoint slots: %s: %d call(s) in flight at a time, shared with other chb processes (lock files %s-*.lock)\n", name, n, files)
}

func localLine(name string, n int, reason error) string {
	return fmt.Sprintf("endpoint slots: %s: %d call(s) in flight at a time from this process; other chb processes are not counted (no lock files: %v)\n", name, n, reason)
}

func captureLog(t *testing.T) *bytes.Buffer {
	var log bytes.Buffer
	logw = &log
	t.Cleanup(func() { logw = os.Stderr })
	return &log
}

// fresh forgets endpoint's server before and after the test, so the test's
// first call sets it up under the test's environment however many times the
// test runs (-count).
func fresh(t *testing.T, endpoint string) string {
	forget := func() {
		mu.Lock()
		delete(servers, Key(endpoint))
		mu.Unlock()
	}
	forget()
	t.Cleanup(forget)
	return endpoint
}

// TestAcquire_LogsOnceWhichSlotsApply: a server's first call logs whether
// its slots are shared, once, whichever name for the server later calls
// use; and no lock file stays behind once its calls are done.
func TestAcquire_LogsOnceWhichSlotsApply(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	log := captureLog(t)
	fresh(t, "http://localhost:40201/v1")
	for _, endpoint := range []string{"http://localhost:40201/v1", "http://127.0.0.1:40201", "http://0.0.0.0:40201/api"} {
		release, _, err := Acquire(context.Background(), endpoint)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	sum := sha256.Sum256([]byte("localhost:40201"))
	files := filepath.Join(os.Getenv("HIVE_ENDPOINT_SLOT_DIR"), hex.EncodeToString(sum[:8]))
	want := sharedLine("localhost:40201", 1, files)
	if errNoFileLocks != nil {
		want = localLine("localhost:40201", 1, errNoFileLocks)
	}
	if log.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", log.String(), want)
	}
	if left, _ := filepath.Glob(files + "-*"); len(left) != 0 {
		t.Errorf("lock files left once every call let go: %v", left)
	}
}

// TestAcquire_AnEndpointWithNoHostIsNotLogged: an endpoint that does not
// parse to a host is its own key, raw text that may hold a credential, so
// the log does not name it.
func TestAcquire_AnEndpointWithNoHostIsNotLogged(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "1")
	log := captureLog(t)
	endpoint := fresh(t, "user:hunter2@localhost:40202")
	release, n, err := Acquire(context.Background(), endpoint)
	if err != nil || n != 1 {
		t.Fatalf("Acquire: n=%d err=%v", n, err)
	}
	release()
	if strings.Contains(log.String(), "hunter2") {
		t.Errorf("the log names the endpoint's credential:\n%s", log.String())
	}
	if !strings.HasPrefix(log.String(), "endpoint slots: an endpoint with no host: 1 call(s)") {
		t.Errorf("log = %q, want the server named as an endpoint with no host", log.String())
	}
}

// TestAcquire_WithoutLockFilesTheBoundIsThisProcesss: where no lock file can
// be made, the first call logs why, and the process's own calls are still
// bound.
func TestAcquire_WithoutLockFilesTheBoundIsThisProcesss(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(blocker, "slots")
	t.Setenv("HIVE_ENDPOINT_SLOT_DIR", dir)
	log := captureLog(t)
	endpoint := fresh(t, "http://127.0.0.1:40203/v1")

	release, n, err := Acquire(context.Background(), endpoint)
	if err != nil || n != 1 {
		t.Fatalf("Acquire: n=%d err=%v, want the loopback default of one slot", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Acquire(ctx, endpoint); !errors.Is(err, context.Canceled) {
		t.Errorf("a second call while the first holds the one slot: err = %v, want it to wait", err)
	}
	release()
	release, _, err = Acquire(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()

	reason := errNoFileLocks
	if reason == nil {
		reason = os.MkdirAll(dir, 0o700)
	}
	if reason == nil {
		t.Fatal("precondition: a directory under a regular file was made")
	}
	if want := localLine(Key(endpoint), 1, reason); log.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", log.String(), want)
	}
}

// Endpoints that reach one server have one key, however the host is spelt
// and whatever path a backend adds; different servers have different keys.
func TestKey_OneServerOneKey(t *testing.T) {
	cases := []struct{ endpoint, server string }{
		{"http://localhost:11434/v1", "ollama"},
		{"http://127.0.0.1:11434", "ollama"},
		{"http://[::1]:11434/v1", "ollama"},
		{"http://0.0.0.0:11434/v1", "ollama"},
		{"http://user:pw@LOCALHOST:11434/api", "ollama"},
		{"http://localhost:1234/v1", "lm studio"},
		{"https://api.openai.com/v1", "openai"},
		{"https://api.openai.com:443", "openai"},
		{"http://api.openai.com/v1", "openai over http"},
		{"https://api.anthropic.com", "anthropic"},
	}
	for _, a := range cases {
		for _, b := range cases {
			if same := Key(a.endpoint) == Key(b.endpoint); same != (a.server == b.server) {
				t.Errorf("%s (%s) and %s (%s) share a key = %v, want %v", a.endpoint, a.server, b.endpoint, b.server, same, !same)
			}
		}
	}
}
