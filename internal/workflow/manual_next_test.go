package workflow

import "testing"

// In the manual loop (`chb workflow next`) a ready human_review node is
// parked, so `chb workflow resume` finds it waiting; its ready sibling is
// handed out.
func TestGetNextNodesManual_ParksHumanReview(t *testing.T) {
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, `
name: hr
version: 1
nodes:
  a: {type: agent, prompt: a}
  review: {type: human_review}
  side: {type: agent, prompt: s}
  b: {type: agent, prompt: b}
edges:
  - {from: a, to: review}
  - {from: a, to: side}
  - {from: review, to: b}
`, nil)
	next, err := GetNextNodesManual(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Node != "a" {
		t.Fatalf("next = %+v, want a", next)
	}
	complete(t, repo, runID, "a", map[string]any{})

	next, err = GetNextNodesManual(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Node != "side" {
		t.Fatalf("next = %+v, want only side", next)
	}
	if got := nodeStatus(t, repo, runID, "review"); got != "waiting_human" {
		t.Errorf("review = %q, want waiting_human", got)
	}
	if s := runStatus(t, repo, runID); s != "paused" {
		t.Errorf("run status = %q, want paused", s)
	}
	if got := nodeStatus(t, repo, runID, "b"); got != "pending" {
		t.Errorf("b = %q, want pending", got)
	}

	// Paused: nothing more is handed out.
	next, err = GetNextNodesManual(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 0 {
		t.Fatalf("a paused run handed out %+v", next)
	}
}
