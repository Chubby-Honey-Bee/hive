package cli

import (
	"bytes"
	"strconv"
	"testing"
)

// The review extracts the run agent-run just made, by the id in agent-run's
// own log line, so a second review into one workspace reports its own
// findings, not the first's.
func TestRunIDWriter_KeepsTheRunIDAgentRunReports(t *testing.T) {
	for _, want := range []int64{1, 2, 417} {
		log := "[agent-run] initializing workflow: wf.yaml\n" +
			agentRunIDPrefix + strconv.FormatInt(want, 10) + "\n" +
			"[agent-run] time wheel: tick 1 open (swarm)\n" +
			agentRunIDPrefix + "999\n"
		// Split at every offset: a pipe hands the writer arbitrary chunks, so
		// the id line can arrive in two pieces.
		for cut := 0; cut <= len(log); cut++ {
			var out bytes.Buffer
			w := &runIDWriter{w: &out}
			if _, err := w.Write([]byte(log[:cut])); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(log[cut:])); err != nil {
				t.Fatal(err)
			}
			if w.id != want {
				t.Fatalf("cut at %d: id = %d, want the first reported id %d", cut, w.id, want)
			}
			if out.String() != log {
				t.Fatalf("cut at %d: the log was not passed through unchanged", cut)
			}
		}
	}

	w := &runIDWriter{w: &bytes.Buffer{}}
	_, _ = w.Write([]byte("model text mentioning workflow run ID: 5\n" + agentRunIDPrefix + "x\n"))
	if w.id != 0 {
		t.Errorf("id = %d from lines that are not agent-run's report", w.id)
	}
}

// chb review has no --model flag: the lens nodes run on their workflow
// tiers.
func TestReviewCmd_HasNoModelFlag(t *testing.T) {
	if f := newReviewCmd().Flags().Lookup("model"); f != nil {
		t.Errorf("chb review has --model (default %q), which reaches no node", f.DefValue)
	}
}
