package workflow

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// calibSwarm is the shape RunCalibration reads: four lenses, the direct
// voice, two resonates pairs, and a Queen whose prompt reads the tally.
const calibSwarm = `name: calib
inputs: [question]
resonates:
  - [a, b]
  - [c, d]
nodes:
  forager-a: {type: agent, prompt: "a {question}"}
  forager-b: {type: agent, prompt: "b {question}"}
  forager-c: {type: agent, prompt: "c {question}"}
  forager-d: {type: agent, prompt: "d {question}"}
  forager-direct: {type: agent, prompt: "{question}"}
  queen: {type: agent, join: settled, prompt: "{tally} {nabla.fired}"}
edges:
  - {from: forager-a, to: queen}
  - {from: forager-b, to: queen}
  - {from: forager-c, to: queen}
  - {from: forager-d, to: queen}
  - {from: forager-direct, to: queen}
`

// The leading lines come from the run's rows: the Queen's convergence and
// dissent, the engine's tally with its margin, the direct voice counted as
// one vote, and the fired ∇ pairs; and they come before the verdict.
func TestRunCalibration_ReadsTheRowsAndLeadsWithThem(t *testing.T) {
	store := newTestStore(t)
	repo := store.Workflows()
	runID, err := InitWorkflow(repo, writeWorkflow(t, calibSwarm), map[string]any{"question": "q"})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{"a": "support", "b": "support", "c": "oppose", "d": "abstain", "direct": "support"} {
		if err := CompleteNode(repo, runID, "forager-"+name, map[string]any{"verdict": v, "recommendation": "r-" + name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := CompleteNode(repo, runID, "queen", map[string]any{"verdict": "support", "convergence": "high", "dissent_from_plurality": "  ", "recommendation": "ship it"}); err != nil {
		t.Fatal(err)
	}
	c, err := RunCalibration(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	want := Calibration{QueenStatus: "completed", Convergence: "high", DissentWritten: false, Tally: c.Tally,
		Plurality: "support", Margin: 2, Votes: 4, Abstentions: 1, NablaFired: []string{"a↔b (support)"}}
	got := c
	got.counts, got.missing, got.pairs, got.verdict, got.recommendation = nil, nil, 0, "", ""
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calibration %+v, want %+v", got, want)
	}
	if !strings.HasPrefix(c.Tally, "5 verdicts: support 3 (a, b, direct); abstain 1 (d); oppose 1 (c)") {
		t.Errorf("tally %q does not count the direct voice as one vote", c.Tally)
	}
	lines := c.Lines()
	wantLines := []string{
		"Quorum: convergence high; tally support 3, abstain 1, oppose 1; plurality support, margin 2; no dissent written",
		"∇ fired: a↔b (support)",
		"Verdict: support — ship it",
	}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(wantLines, "\n"))
	}
	if !strings.HasPrefix(lines[0], "Quorum:") || !strings.HasPrefix(lines[len(lines)-1], "Verdict:") {
		t.Errorf("the verdict must come last, after the calibration: %q", lines)
	}
}

// A tie leaves no plurality and a margin of 0; a Queen who was rejected
// leaves no convergence and no verdict, and the lines say so; a lens with
// no verdict is named and counts as neither vote nor abstention.
func TestRunCalibration_TieAndARejectedQueen(t *testing.T) {
	store := newTestStore(t)
	repo := store.Workflows()
	runID, err := InitWorkflow(repo, writeWorkflow(t, calibSwarm), map[string]any{"question": "q"})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{"a": "support", "b": "oppose", "c": "abstain"} {
		if err := CompleteNode(repo, runID, "forager-"+name, map[string]any{"verdict": v}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.MarkNodeRejected(runID, "forager-d", "accept rejected", now); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkNodeRejected(runID, "queen", "accept rejected", now); err != nil {
		t.Fatal(err)
	}
	c, err := RunCalibration(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Plurality != "" || c.Margin != 0 || c.Votes != 2 || c.Abstentions != 1 || c.QueenStatus != "rejected" || c.Convergence != "" || len(c.NablaFired) != 0 {
		t.Errorf("calibration %+v, want a tie, two votes, one abstention, a rejected queen and no ∇", c)
	}
	lines := c.Lines()
	wantLines := []string{
		"Quorum: convergence none (queen is rejected in this run); tally abstain 1, oppose 1, support 1; no verdict from d (rejected), direct (pending); plurality none (tie); no dissent written",
		"∇ fired: none",
		"Verdict: none (queen is rejected in this run)",
	}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(wantLines, "\n"))
	}
}

// A written dissent is reported, and a swarm with no resonates pairs says
// why no ∇ fired.
func TestRunCalibration_DissentAndNoPairs(t *testing.T) {
	store := newTestStore(t)
	repo := store.Workflows()
	noPairs := strings.Replace(calibSwarm, "resonates:\n  - [a, b]\n  - [c, d]\n", "", 1)
	runID, err := InitWorkflow(repo, writeWorkflow(t, noPairs), map[string]any{"question": "q"})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{"a": "support", "b": "support", "c": "support"} {
		if err := CompleteNode(repo, runID, "forager-"+name, map[string]any{"verdict": v}); err != nil {
			t.Fatal(err)
		}
	}
	if err := CompleteNode(repo, runID, "queen", map[string]any{"verdict": "oppose", "convergence": "low", "dissent_from_plurality": "c's evidence outweighs the rest", "recommendation": ""}); err != nil {
		t.Fatal(err)
	}
	c, err := RunCalibration(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !c.DissentWritten || c.Convergence != "low" || c.Margin != 3 {
		t.Errorf("calibration %+v, want a written dissent, low convergence and margin 3", c)
	}
	lines := c.Lines()
	if !strings.HasSuffix(lines[0], "plurality support, margin 3; dissent written") || lines[1] != "∇ fired: none — this swarm declares no resonates pairs" || lines[2] != "Verdict: oppose" {
		t.Errorf("lines %q", lines)
	}
}
