package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
)

// evalChainBackend plays every role in a generated swarm with the coverage
// pass on. It tells the roles apart by text each node's prompt carries, and
// answers the evaluator with a fixed gaps list.
type evalChainBackend struct {
	gaps []string

	mu       sync.Mutex
	followup []string
	queen    []string
}

func (b *evalChainBackend) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	p := req.Prompt
	var text string
	switch {
	case strings.Contains(p, "one forager of the HIVE swarm"):
		text = `{"forager":"x","verdict":"support","key_points":[],"evidence":[],"uncertainties":[],"recommendation":"r"}`
	case strings.Contains(p, "You are the swarm's coverage evaluator"):
		verdict := "COMPLETE"
		if len(b.gaps) > 0 {
			verdict = "NEEDS_FOLLOWUP"
		}
		out, _ := json.Marshal(map[string]any{"eval_verdict": verdict, "coverage": 3, "gaps": b.gaps})
		text = string(out)
	case strings.Contains(p, "Gap to fill:"):
		b.mu.Lock()
		b.followup = append(b.followup, p)
		b.mu.Unlock()
		finding := "unmatched"
		for _, g := range b.gaps {
			if strings.Contains(p, g) {
				finding = followupFindingFor(g)
			}
		}
		out, _ := json.Marshal(map[string]string{"followup_findings": finding})
		text = string(out)
	case strings.Contains(p, "You are Queen"):
		b.mu.Lock()
		b.queen = append(b.queen, p)
		b.mu.Unlock()
		text = `{"report":"## Swarm Verdict: q","convergence":"high","coverage":4,"gaps":[],"dissent_from_plurality":"","verdict":"support","recommendation":"r"}`
	default:
		text = "unrecognised prompt"
	}
	return &RunResult{FinalText: text, InputTokens: 1, OutputTokens: 1, StopReason: "end_turn"}, nil
}

func followupFindingFor(gap string) string { return "finding on: " + gap }

// The coverage pass `chb ask` runs by default: swarm-evaluate names gaps,
// swarm-followup fans one call per gap, and Queen reads every follow-up
// finding. The evaluator returns gaps as a JSON array, as its prompt asks,
// so the state value the fan reads is a list, not a string.
func TestSwarmEvaluateChain_FollowupFansEachGapIntoQueen(t *testing.T) {
	cases := map[string][]string{
		"two gaps": {"What about cost?", "What about latency?"},
		"one gap":  {"Who maintains it?"},
		"no gaps":  {},
	}
	for name, gaps := range cases {
		t.Run(name, func(t *testing.T) {
			swarm := []foragers.Forager{
				{Name: "optimist", Title: "The Optimist", Description: "Sees upside.", Body: "You are The Optimist.\n"},
				{Name: "skeptic", Title: "The Skeptic", Description: "Sees risk.", Body: "You are The Skeptic.\n"},
			}
			yamlText, err := foragers.GenerateWorkflow(swarm, foragers.WorkflowOptions{
				Name: "eval-chain", Model: "haiku", SynthesizerModel: "haiku", Evaluate: true,
			})
			if err != nil {
				t.Fatalf("GenerateWorkflow: %v", err)
			}
			dir := t.TempDir()
			wf := filepath.Join(dir, "swarm.yaml")
			if err := os.WriteFile(wf, []byte(yamlText), 0o644); err != nil {
				t.Fatal(err)
			}

			backend := &evalChainBackend{gaps: gaps}
			_, err = Run(context.Background(), newTempStore(t), Config{
				WorkflowYAML: wf,
				ProjectDir:   dir,
				Inputs:       map[string]any{"question": "Should we adopt X?", "context": ""},
				Backend:      backend,
				Log:          discardWriter{},
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if got := len(backend.followup); got != len(gaps) {
				t.Fatalf("follow-up backend calls = %d, want one per gap (%d)", got, len(gaps))
			}
			for _, g := range gaps {
				n := 0
				for _, p := range backend.followup {
					if strings.Contains(p, g) {
						n++
					}
				}
				if n != 1 {
					t.Errorf("gap %q reached %d follow-up prompts, want 1", g, n)
				}
			}
			for _, p := range backend.followup {
				if strings.Contains(p, "{gap}") {
					t.Errorf("follow-up prompt kept the literal {gap}: %q", p)
				}
			}

			if len(backend.queen) != 1 {
				t.Fatalf("queen calls = %d, want 1", len(backend.queen))
			}
			queen := backend.queen[0]
			for _, g := range gaps {
				if want := followupFindingFor(g); !strings.Contains(queen, want) {
					t.Errorf("queen's prompt lacks the follow-up finding %q", want)
				}
			}
			if len(gaps) > 0 && strings.Contains(queen, "{followup_findings}") {
				t.Error("queen's prompt kept the literal {followup_findings} although follow-ups ran")
			}
		})
	}
}
