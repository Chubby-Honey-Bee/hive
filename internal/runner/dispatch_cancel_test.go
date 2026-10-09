package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// blockingBackend holds every call until its context ends, as a model call
// a cancelled run interrupts. started receives each call's prompt as the
// call begins.
type blockingBackend struct{ started chan string }

func (b *blockingBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	b.started <- req.Prompt
	<-ctx.Done()
	return nil, ctx.Err()
}

// A run cancelled while its nodes are in flight (Ctrl-C in agent-run,
// chb-mcp's shutdown) stops: Run returns the cancellation, no node spends a
// retry, the run is not recorded as failed, and `--resume` completes it.
func TestRun_ACancelledRunSpendsNoRetryAndResumes(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	yaml := `name: cancel-resume
version: 1
nodes:
  a:
    type: agent
    model: sonnet
    prompt: "a"
    max_retries: 2
  b:
    type: agent
    model: sonnet
    prompt: "b"
    max_retries: 2
  c:
    type: agent
    model: sonnet
    prompt: "c"
edges:
  - from: a
    to: c
  - from: b
    to: c
`
	if err := os.WriteFile(wf, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newParallelTestStore(t)
	cfg := Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 20, Log: io.Discard}

	blocking := &blockingBackend{started: make(chan string, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		c := cfg
		c.Backend = blocking
		res, err := Run(ctx, store, c)
		done <- outcome{res, err}
	}()
	for range 2 {
		select {
		case <-blocking.started:
		case <-time.After(30 * time.Second):
			t.Fatal("the two start nodes never reached the backend")
		}
	}
	cancel()
	var out outcome
	select {
	case out = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the cancel")
	}
	if !errors.Is(out.err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", out.err)
	}
	if out.res == nil || out.res.RunID == 0 {
		t.Fatalf("Run returned no run id (result %+v)", out.res)
	}
	runID := out.res.RunID

	runStatus := func() string {
		t.Helper()
		run, err := store.Workflows().GetWorkflowRun(runID)
		if err != nil {
			t.Fatal(err)
		}
		return run.Status
	}
	if got := runStatus(); got != "running" {
		t.Errorf("after the cancel the run is %q, want running, which --resume continues", got)
	}
	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Status != "pending" || s.Attempt != 0 {
			t.Errorf("after the cancel node %s is %s at attempt %d, want pending at attempt 0: no retry spent", s.NodeName, s.Status, s.Attempt)
		}
	}

	resume := cfg
	resume.Backend = &stubBackend{}
	resume.ResumeRunID = runID
	if _, err := Run(context.Background(), store, resume); err != nil {
		t.Fatalf("--resume %d: %v", runID, err)
	}
	if got := runStatus(); got != "completed" {
		t.Errorf("after the resume the run is %q, want completed", got)
	}
	states, err = store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Status != "completed" || s.Attempt != 1 {
			t.Errorf("after the resume node %s is %s at attempt %d, want completed at attempt 1", s.NodeName, s.Status, s.Attempt)
		}
	}
}
