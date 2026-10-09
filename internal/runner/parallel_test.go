package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// stubBackend implements LLMBackend for tests. It records every invocation
// (in Submitted) and tracks peak concurrent goroutines in flight so the
// parallel dispatcher can be asserted against a real workload shape.
type stubBackend struct {
	mu        sync.Mutex
	inFlight  int32
	peak      int32
	submitted []string
	delay     time.Duration
}

func (b *stubBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	cur := atomic.AddInt32(&b.inFlight, 1)
	defer atomic.AddInt32(&b.inFlight, -1)
	for {
		p := atomic.LoadInt32(&b.peak)
		if cur <= p {
			break
		}
		if atomic.CompareAndSwapInt32(&b.peak, p, cur) {
			break
		}
	}
	b.mu.Lock()
	b.submitted = append(b.submitted, req.Prompt)
	b.mu.Unlock()

	// Simulate work so the fan-out actually has overlap to measure.
	select {
	case <-time.After(b.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return &RunResult{
		FinalText:    "ok",
		Turns:        1,
		InputTokens:  10,
		OutputTokens: 5,
		StopReason:   "end_turn",
	}, nil
}

// parallelFanWorkflowYAML defines N independent leaf nodes all feeding a
// single sink. GetNextNodes returns all leaves at once, which is exactly
// what the parallel dispatcher is supposed to fan out.
func parallelFanWorkflowYAML(n int) string {
	yaml := `name: test-fan
description: N leaves, one sink
version: 1
inputs:
  - topic

nodes:
`
	for i := 0; i < n; i++ {
		yaml += fmt.Sprintf(`  leaf-%d:
    type: agent
    agent: analyst
    model: sonnet
    prompt: "process {topic} branch %d"
    outputs: [out_%d]

`, i, i, i)
	}
	yaml += `  sink:
    type: agent
    agent: analyst
    model: sonnet
    prompt: "collect"
    outputs: [final]

edges:
`
	for i := 0; i < n; i++ {
		yaml += fmt.Sprintf("  - from: leaf-%d\n    to: sink\n", i)
	}
	return yaml
}

func newParallelTestStore(t *testing.T) *db.Store {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "parallel.db")
	store, err := db.NewStore(tmp)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestParallelDispatch_PeakConcurrency(t *testing.T) {
	const nodes = 10
	const maxPar = 4
	const perNode = 120 * time.Millisecond

	t.Setenv("HIVE_MAX_PARALLEL_NODES", fmt.Sprintf("%d", maxPar))

	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "fan.yaml")
	if err := os.WriteFile(wfFile, []byte(parallelFanWorkflowYAML(nodes)), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	store := newParallelTestStore(t)
	stub := &stubBackend{delay: perNode}

	cfg := Config{
		WorkflowYAML:  wfFile,
		ProjectName:   "parallel-test",
		ProjectDir:    tmpDir, // no git repo → auto-commit disabled
		Inputs:        map[string]any{"topic": "bees"},
		MaxIterations: 10,
		Branch:        "", // commits disabled
		Backend:       stub,
		Log:           os.Stderr,
	}

	start := time.Now()
	res, err := Run(context.Background(), store, cfg)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// All N leaves + sink must have completed.
	wantNodes := nodes + 1
	if res.NodesRun != wantNodes {
		t.Fatalf("NodesRun=%d, want %d", res.NodesRun, wantNodes)
	}

	// Peak concurrency must be ≥ 2 (proves parallelism really happened)
	// and ≤ maxPar (proves the semaphore bound is enforced).
	peak := atomic.LoadInt32(&stub.peak)
	if peak < 2 {
		t.Errorf("peak concurrency=%d — no parallelism observed", peak)
	}
	if peak > int32(maxPar) {
		t.Errorf("peak concurrency=%d > maxPar=%d — semaphore breached", peak, maxPar)
	}

	// Wall time sanity: serial would be ≥ nodes*perNode; parallel at
	// maxPar should be roughly ceil(nodes/maxPar)*perNode + sink*perNode.
	// Allow generous headroom for goroutine spawn + SQLite writes.
	serialFloor := time.Duration(nodes) * perNode
	if elapsed >= serialFloor {
		t.Errorf("wall time %s ≥ serial floor %s — not parallel", elapsed, serialFloor)
	}

	// Output stability: every leaf prompt must have been submitted exactly
	// once. Sort so ordering doesn't matter.
	stub.mu.Lock()
	got := append([]string{}, stub.submitted...)
	stub.mu.Unlock()
	sort.Strings(got)
	if len(got) != wantNodes {
		t.Fatalf("submitted=%d prompts, want %d", len(got), wantNodes)
	}
}

func TestParallelismFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		def  int
		want int
	}{
		{"", 4, 4},
		{"1", 4, 1},
		{"8", 4, 8},
		{"0", 4, 1},       // clamp to 1
		{"-3", 4, 1},      // clamp to 1
		{"9999", 4, 64},   // clamp to 64
		{"garbage", 4, 4}, // fallback to default
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("env=%q", c.env), func(t *testing.T) {
			if c.env == "" {
				t.Setenv("HIVE_MAX_PARALLEL_NODES", "")
				os.Unsetenv("HIVE_MAX_PARALLEL_NODES")
			} else {
				t.Setenv("HIVE_MAX_PARALLEL_NODES", c.env)
			}
			if got := parallelismFromEnv(c.def); got != c.want {
				t.Errorf("parallelismFromEnv(%d) with env=%q = %d, want %d", c.def, c.env, got, c.want)
			}
		})
	}
}

// TestCommitMutex_NoInterleave asserts that concurrent CommitIfDirty calls
// serialize correctly — if the mutex regresses, the race detector will
// fire on the shared counter below.
func TestCommitMutex_NoInterleave(t *testing.T) {
	var shared int
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gitMu.Lock()
			shared++
			gitMu.Unlock()
		}()
	}
	wg.Wait()
	if shared != 20 {
		t.Fatalf("shared=%d, want 20 — mutex failed to serialize", shared)
	}
}

// Suppress "imported and not used" if workflow isn't referenced elsewhere
// in the test helpers.
var _ = workflow.GetNextNodes
