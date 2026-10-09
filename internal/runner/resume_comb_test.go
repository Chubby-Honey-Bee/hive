package runner

import (
	"context"
	"database/sql"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// Run returns only once its quorum sensor has drained. The sensor here takes
// longer to drain than the rest of Run takes to tear down, so a Run that
// only cancelled the sensor would return first.
func TestRun_WaitsForItsSensorToDrain(t *testing.T) {
	var drained atomic.Bool
	prev := startSensor
	startSensor = func(ctx context.Context, _ *db.Store, _ int64, _ map[string]any, _ bool, _ func(string, ...any)) <-chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-ctx.Done()
			time.Sleep(200 * time.Millisecond)
			drained.Store(true)
		}()
		return done
	}
	t.Cleanup(func() { startSensor = prev })

	dir := t.TempDir()
	wf := filepath.Join(dir, "one.yaml")
	mustWriteFile(t, wf, "name: one\nnodes:\n  only:\n    type: agent\n    model: haiku\n    prompt: \"answer\"\n")
	cfg := Config{WorkflowYAML: wf, ProjectDir: dir, Log: io.Discard,
		Backend: &promptCapturingBackend{fns: []func(RunRequest) (*RunResult, error){successResult("done")}}}
	if _, err := Run(context.Background(), newTempStore(t), cfg); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !drained.Load() {
		t.Fatal("Run returned before its quorum sensor finished draining")
	}
}

// A run paused at a human_review gate and resumed with --resume is one run:
// its writes after the resume anchor to its own swarm tick, which the resume
// reopens, and its ∇ sensor starts from the verdicts written before the
// pause, so a resonates pair whose verdicts straddle the pause converges.
func TestResumedRun_AnchorsToItsTickAndConvergesAcrossThePause(t *testing.T) {
	verdicts := map[string]string{"a": "support", "b": "support"}
	const wfYAML = `
name: resume-comb
resonates:
  - [a, b]
nodes:
  forager-a:
    type: agent
    model: haiku
    prompt: "be forager a"
  gate:
    type: human_review
  forager-b:
    type: agent
    model: haiku
    prompt: "be forager b"
edges:
  - {from: forager-a, to: gate}
  - {from: gate, to: forager-b}
`
	dir := t.TempDir()
	wf := filepath.Join(dir, "resume.yaml")
	mustWriteFile(t, wf, wfYAML)
	store := newTempStore(t)
	verdictOf := func(forager string) string {
		return `{"verdict":"` + verdicts[forager] + `","recommendation":"r","key_points":[],"evidence":[],"uncertainties":[]}`
	}
	cfg := Config{WorkflowYAML: wf, ProjectDir: dir, Log: io.Discard,
		Backend: &promptCapturingBackend{fns: []func(RunRequest) (*RunResult, error){successResult(verdictOf("a"))}}}

	first, err := Run(context.Background(), store, cfg)
	if err != nil {
		t.Fatalf("first segment: %v", err)
	}
	runID := first.RunID
	if err := workflow.ResumeHumanReview(store.Workflows(), runID, "approve", ""); err != nil {
		t.Fatalf("approve gate: %v", err)
	}
	cfg.ResumeRunID = runID
	cfg.Backend = &promptCapturingBackend{fns: []func(RunRequest) (*RunResult, error){successResult(verdictOf("b"))}}
	if _, err := Run(context.Background(), store, cfg); err != nil {
		t.Fatalf("resumed segment: %v", err)
	}

	rows, err := store.ReadDB.Query(`
		SELECT r.vantage_key, r.tick_id, t.run_id, t.ended_at
		FROM comb_revisions r LEFT JOIN time_wheel t ON t.id = r.tick_id
		WHERE r.vantage_kind = 'forager' ORDER BY r.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var key string
		var tick, tickRun sql.NullInt64
		var ended sql.NullString
		if err := rows.Scan(&key, &tick, &tickRun, &ended); err != nil {
			t.Fatal(err)
		}
		seen++
		if !tick.Valid || tickRun.Int64 != runID {
			t.Errorf("%s revision anchored to tick %v (run %v), want run %d's tick", key, tick, tickRun, runID)
		}
		if !ended.Valid {
			t.Errorf("%s: run %d's tick is still open after the run returned", key, runID)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != len(verdicts) {
		t.Fatalf("%d forager revisions, want %d", seen, len(verdicts))
	}

	want := 0
	if verdicts["a"] == verdicts["b"] && verdicts["a"] != "" && verdicts["a"] != "abstain" {
		want = 1
	}
	bonds, err := store.ForagerBonds().ListByRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	fired := 0
	for _, b := range bonds {
		if b.Kind == db.BondResonates && b.Fired {
			fired++
		}
	}
	if fired != want {
		t.Fatalf("%d fired resonates bonds for run %d, want %d", fired, runID, want)
	}
}
