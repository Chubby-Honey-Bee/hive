package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// fanRecordingBackend captures every call made to it so the test can
// assert N children fired with the right per-item prompt substitution.
// Tracks peak concurrency so true-parallelism can be verified (not
// just sequential dispatch with the right outputs).
type fanRecordingBackend struct {
	delay     time.Duration
	calls     atomic.Int64
	peak      atomic.Int64
	current   atomic.Int64
	prompts   chan string
	threadIDs chan int64
}

func newFanRecordingBackend(buf int, delay time.Duration) *fanRecordingBackend {
	return &fanRecordingBackend{
		delay:     delay,
		prompts:   make(chan string, buf),
		threadIDs: make(chan int64, buf),
	}
}

func (b *fanRecordingBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	cur := b.current.Add(1)
	defer b.current.Add(-1)
	for {
		p := b.peak.Load()
		if cur <= p || b.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	n := b.calls.Add(1)
	b.prompts <- req.Prompt
	b.threadIDs <- n
	select {
	case <-time.After(b.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &RunResult{
		FinalText:    "result for prompt: " + req.Prompt,
		InputTokens:  10,
		OutputTokens: 20,
		StopReason:   "end_turn",
	}, nil
}

// TestRunFanOut_DispatchesNConcurrentCalls asserts that a fan dispatches its
// items as concurrent calls:
//   - exactly len(FanItems) backend.Run calls fired
//   - peak concurrency > 1 (true parallelism, not sequential)
//   - each child got the per-item prompt substitution
//   - combined result aggregates tokens + concatenates outputs
func TestRunFanOut_DispatchesNConcurrentCalls(t *testing.T) {
	// The default bound (4), whatever the developer's shell sets.
	t.Setenv("HIVE_MAX_PARALLEL_FAN", "")
	t.Setenv("HIVE_MAX_PARALLEL_NODES", "")
	store := newParallelTestStore(t)
	backend := newFanRecordingBackend(10, 200*time.Millisecond)

	rc := &runtimeContext{
		ctx:     context.Background(),
		cfg:     Config{Log: discardWriter{}},
		store:   store,
		backend: backend,
		logf:    makeLogf(discardWriter{}),
	}
	node := workflow.DispatchNode{
		Node:               "fan",
		Type:               "parallel_fan",
		ResolvedPrompt:     "research the angle: {item}",
		FanItems:           []string{"alpha", "beta", "gamma", "delta"},
		FanItemPlaceholder: "{item}",
	}

	combined, err := rc.runFanOut(context.Background(), node, backend, RunRequest{System: "system", Model: "sonnet"})
	if err != nil {
		t.Fatalf("runFanOut: %v", err)
	}

	if got := backend.calls.Load(); got != 4 {
		t.Errorf("backend.Run calls = %d; want 4", got)
	}
	if peak := backend.peak.Load(); peak < 2 {
		t.Errorf("peak concurrency = %d; want ≥ 2 (need real parallelism, not sequential dispatch)", peak)
	}
	close(backend.prompts)
	got := map[string]bool{}
	for p := range backend.prompts {
		got[p] = true
	}
	for _, item := range []string{"alpha", "beta", "gamma", "delta"} {
		want := "research the angle: " + item
		if !got[want] {
			t.Errorf("expected child prompt %q; got %v", want, got)
		}
	}

	// Aggregate sanity
	if combined.InputTokens != 40 {
		t.Errorf("InputTokens = %d; want 40 (4 × 10)", combined.InputTokens)
	}
	if combined.OutputTokens != 80 {
		t.Errorf("OutputTokens = %d; want 80 (4 × 20)", combined.OutputTokens)
	}
	if !strings.Contains(combined.FinalText, "Item 1") || !strings.Contains(combined.FinalText, "Item 4") {
		t.Errorf("combined.FinalText missing expected section headers; got: %q", combined.FinalText)
	}
}

// TestRunFanOut_AllChildFailuresSurface asserts that when every child
// errors out, runFanOut returns a node-level error naming the failed
// items. Single-item fans always require their one success.
func TestRunFanOut_AllChildFailuresSurface(t *testing.T) {
	store := newParallelTestStore(t)
	backend := &erroringBackend{err: context.DeadlineExceeded}

	rc := &runtimeContext{
		ctx:     context.Background(),
		cfg:     Config{Log: discardWriter{}},
		store:   store,
		backend: backend,
		logf:    makeLogf(discardWriter{}),
	}
	node := workflow.DispatchNode{
		Node:               "fan",
		Type:               "parallel_fan",
		ResolvedPrompt:     "x: {item}",
		FanItems:           []string{"a", "b"},
		FanItemPlaceholder: "{item}",
	}

	_, err := rc.runFanOut(context.Background(), node, backend, RunRequest{Model: "sonnet"})
	if err == nil {
		t.Fatal("expected runFanOut to surface child error; got nil")
	}
	if !strings.Contains(err.Error(), "parallel_fan failed") {
		t.Errorf("error should label the wave failed; got %v", err)
	}
	if !strings.Contains(err.Error(), "item 1") || !strings.Contains(err.Error(), "item 2") {
		t.Errorf("error should name each failing item; got %v", err)
	}
}

// TestRunFanOut_PartialSuccessTolerated asserts that a fan with at least
// half the items succeeding returns the aggregated output, dropping the
// failed items with a log line, so a transient per-child timeout (CLI
// deadline, rate limit, 5xx) does not bring down a whole research wave.
func TestRunFanOut_PartialSuccessTolerated(t *testing.T) {
	store := newParallelTestStore(t)
	// alternatingBackend returns success for prompts containing "good"
	// and failure for prompts containing "bad". With 4 items where 2
	// are good and 2 are bad, success ratio = 2/4 = 50% which meets
	// the 1/2 threshold.
	backend := &alternatingBackend{}

	rc := &runtimeContext{
		ctx:     context.Background(),
		cfg:     Config{Log: discardWriter{}},
		store:   store,
		backend: backend,
		logf:    makeLogf(discardWriter{}),
	}
	node := workflow.DispatchNode{
		Node:               "fan",
		Type:               "parallel_fan",
		ResolvedPrompt:     "x: {item}",
		FanItems:           []string{"good-1", "bad-1", "good-2", "bad-2"},
		FanItemPlaceholder: "{item}",
	}

	combined, err := rc.runFanOut(context.Background(), node, backend, RunRequest{Model: "sonnet"})
	if err != nil {
		t.Fatalf("partial success should not error; got %v", err)
	}
	if combined == nil || combined.FinalText == "" {
		t.Fatal("expected non-empty aggregate output")
	}
	if !strings.Contains(combined.FinalText, "good-1") || !strings.Contains(combined.FinalText, "good-2") {
		t.Errorf("aggregate should include the successful items; got %q", combined.FinalText)
	}
}

// TestRunFanOut_BelowThresholdStillFails asserts that when fewer than
// half the items succeed (here 1 of 4), the wave is failed.
func TestRunFanOut_BelowThresholdStillFails(t *testing.T) {
	store := newParallelTestStore(t)
	backend := &mostlyFailingBackend{successPrompt: "x: keep-me"}

	rc := &runtimeContext{
		ctx:     context.Background(),
		cfg:     Config{Log: discardWriter{}},
		store:   store,
		backend: backend,
		logf:    makeLogf(discardWriter{}),
	}
	node := workflow.DispatchNode{
		Node:               "fan",
		Type:               "parallel_fan",
		ResolvedPrompt:     "x: {item}",
		FanItems:           []string{"keep-me", "drop-1", "drop-2", "drop-3"},
		FanItemPlaceholder: "{item}",
	}

	_, err := rc.runFanOut(context.Background(), node, backend, RunRequest{Model: "sonnet"})
	if err == nil {
		t.Fatal("1/4 success is below the 1/2 threshold — expected error")
	}
	if !strings.Contains(err.Error(), "1/4") {
		t.Errorf("error should report the success ratio; got %v", err)
	}
}

type alternatingBackend struct{}

func (a *alternatingBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if strings.Contains(req.Prompt, "bad") {
		return nil, context.DeadlineExceeded
	}
	return &RunResult{FinalText: "result for " + req.Prompt, InputTokens: 1, OutputTokens: 2, StopReason: "end_turn"}, nil
}

type mostlyFailingBackend struct{ successPrompt string }

func (m *mostlyFailingBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if strings.Contains(req.Prompt, m.successPrompt) {
		return &RunResult{FinalText: "ok", InputTokens: 1, OutputTokens: 2, StopReason: "end_turn"}, nil
	}
	return nil, context.DeadlineExceeded
}

// A fan succeeds when at least half its items, and at least one, return
// text. Every size from 1 to 7 and every success count is checked against
// that rule, so an odd fan cannot pass on less than half.
func TestRunFanOut_SucceedsOnlyWithAtLeastHalf(t *testing.T) {
	rc := &runtimeContext{
		ctx:   context.Background(),
		cfg:   Config{Log: discardWriter{}},
		store: newParallelTestStore(t),
		logf:  makeLogf(discardWriter{}),
	}
	backend := &mostlyFailingBackend{successPrompt: "keep"}
	for n := 1; n <= 7; n++ {
		for ok := 0; ok <= n; ok++ {
			var items []string
			for i := 0; i < n; i++ {
				if i < ok {
					items = append(items, fmt.Sprintf("keep-%d", i))
				} else {
					items = append(items, fmt.Sprintf("drop-%d", i))
				}
			}
			node := workflow.DispatchNode{
				Node:               "fan",
				Type:               "parallel_fan",
				ResolvedPrompt:     "x: {item}",
				FanItems:           items,
				FanItemPlaceholder: "{item}",
			}
			_, err := rc.runFanOut(context.Background(), node, backend, RunRequest{Model: "sonnet"})
			wantOK := ok >= 1 && 2*ok >= n
			if (err == nil) != wantOK {
				t.Errorf("%d of %d items returned text: err = %v, want success %v", ok, n, err, wantOK)
			}
		}
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// sectionBackend answers an item by its name: "bad" fails, "empty" returns
// no text, anything else returns text naming the prompt.
type sectionBackend struct{}

var errSectionItem = errors.New("item call failed")

func (sectionBackend) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	switch {
	case strings.Contains(req.Prompt, "bad"):
		return nil, errSectionItem
	case strings.Contains(req.Prompt, "empty"):
		return &RunResult{FinalText: "", StopReason: "end_turn"}, nil
	}
	return &RunResult{FinalText: "found: " + req.Prompt, StopReason: "end_turn"}, nil
}

// Every item of a fan keeps its `## Item N` section in the joined text, in
// order: its text, or a line saying why it has none, a failed call and an
// empty reply alike. So a reader can tell item 2 failed rather than lose it.
func TestRunFanOut_EveryItemKeepsItsSection(t *testing.T) {
	rc := &runtimeContext{
		ctx:   context.Background(),
		cfg:   Config{Log: discardWriter{}},
		store: newParallelTestStore(t),
		logf:  makeLogf(discardWriter{}),
	}
	items := []string{"good-1", "bad-2", "good-3", "empty-4", "good-5"}
	node := workflow.DispatchNode{
		Node: "fan", Type: "parallel_fan", ResolvedPrompt: "x: {item}",
		FanItems: items, FanItemPlaceholder: "{item}",
	}
	combined, err := rc.runFanOut(context.Background(), node, sectionBackend{}, RunRequest{Model: "m"})
	if err != nil {
		t.Fatalf("runFanOut: %v", err)
	}
	var want []string
	for i, it := range items {
		body := "found: x: " + it
		switch {
		case strings.Contains(it, "bad"):
			body = fmt.Sprintf("(no finding: the call failed: %v)", errSectionItem)
		case strings.Contains(it, "empty"):
			body = "(no finding: the reply was empty)"
		}
		want = append(want, fmt.Sprintf("## Item %d\n\n%s", i+1, body))
	}
	if got, w := combined.FinalText, strings.Join(want, "\n\n---\n\n"); got != w {
		t.Errorf("joined text:\n%s\nwant:\n%s", got, w)
	}
}
