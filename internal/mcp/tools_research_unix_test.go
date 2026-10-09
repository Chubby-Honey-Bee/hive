//go:build !windows

package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// asChbEnv, set in a test's environment, makes this test binary answer as
// a chb would if a command node ran it: one JSON object naming itself. So a
// run that resolved `chb` to the running binary shows it, rather than
// running this package's tests again.
const asChbEnv = "HIVE_MCP_TEST_AS_CHB"

func TestMain(m *testing.M) {
	if os.Getenv(asChbEnv) == "1" {
		arg := ""
		if len(os.Args) > 1 {
			arg = os.Args[1]
		}
		fmt.Printf(`{"ran":"the chb-mcp binary","arg":%q}`, arg)
		os.Exit(0)
	}
	// A call to a test server on this machine holds an endpoint slot's lock
	// file; keep them out of the user's cache directory.
	dir, err := os.MkdirTemp("", "endpointslot-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Setenv("HIVE_ENDPOINT_SLOT_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// chb_research runs its workflow in this process, whose binary is chb-mcp,
// not chb. A command node's `chb` must run the chb the server drives
// (chbBin), not this binary. The chb here is a script that answers with
// its own path; the run's state holds what the node read.
func TestResearch_CommandNodesRunTheServersChb(t *testing.T) {
	t.Setenv(asChbEnv, "1")
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-chb")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '{\"ran\":\"%s\",\"arg\":\"%s\"}' \"$0\" \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	wf := "name: relay\nnodes:\n  relay:\n    type: command\n    argv: [chb, hello]\n    outputs_from: stdout_json\n    outputs: [ran, arg]\n"
	if err := os.WriteFile(filepath.Join(dir, "workflows", "relay.yaml"), []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.store = newResearchStore(t)
	s.spawnLimit = &spawnLimiter{max: 1, window: time.Minute}
	s.binOnce.Do(func() { s.bin = fake })

	text, isErr, rpcErr := callTool(t, s, &buf, "chb_research", map[string]any{"topic": "relay", "workflow": "relay"})
	s.runsWG.Wait()
	if rpcErr != nil || isErr {
		t.Fatalf("chb_research: isError=%v rpc=%v text=%q", isErr, rpcErr, text)
	}
	var stateJSON, status string
	if err := s.store.ReadDB.QueryRow(`SELECT state_json, status FROM workflow_runs ORDER BY id DESC LIMIT 1`).Scan(&stateJSON, &status); err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || state["ran"] != fake || state["arg"] != "hello" {
		t.Fatalf("run %s with state %v, want completed by %s hello", status, state, fake)
	}
}
