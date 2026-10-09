package runner

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// parallel_fan hands one ToolRegistry and one FileSystemRecorder to every
// child goroutine, so appends to Log and writes to Touched are guarded:
// unguarded, they race, and under load a "concurrent map writes" fatal error
// takes the whole run process down. Run with -race.
func TestToolRegistry_ConcurrentInvokeAndMark(t *testing.T) {
	fs := NewFSRecorder()
	r := &ToolRegistry{Handlers: map[string]ToolHandler{
		"touch": func(_ context.Context, in map[string]any) (string, error) {
			fs.mark(fmt.Sprint(in["p"]))
			return "ok", nil
		},
	}}
	const workers, perWorker = 32, 50
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				if _, isErr, err := r.Invoke(context.Background(), "touch", map[string]any{"p": fmt.Sprintf("%d-%d", i, j)}); err != nil || isErr {
					t.Errorf("invoke: isErr=%v err=%v", isErr, err)
				}
				_ = fs.TouchedPaths() // concurrent reader
			}
		}(i)
	}
	wg.Wait()
	if got := len(r.Invocations()); got != workers*perWorker {
		t.Fatalf("audit log has %d entries, want %d", got, workers*perWorker)
	}
	if got := len(fs.TouchedPaths()); got != workers*perWorker {
		t.Fatalf("recorder has %d paths, want %d", got, workers*perWorker)
	}
}
