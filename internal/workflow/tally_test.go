package workflow

import (
	"strings"
	"testing"
)

// Abstain casts no vote. The plurality is the one other verdict with the
// most votes; a tie, or a tally with only abstains, has none. The values
// the engine writes to state when the Queen completes are the same count:
// the plurality, the votes cast, the abstentions and the line. A rejected
// lens (verdict "" here) counts in neither the votes nor the abstentions.
func TestTally_AbstainCastsNoVote(t *testing.T) {
	cases := map[string]map[string]string{
		"abstain outnumbers support": {"a": "abstain", "b": "abstain", "c": "abstain", "d": "support"},
		"every lens abstains":        {"a": "abstain", "b": "abstain", "c": "abstain", "d": "abstain"},
		"a tie":                      {"a": "support", "b": "oppose", "c": "abstain", "d": "abstain"},
		"a clear plurality":          {"a": "oppose", "b": "oppose", "c": "support", "d": "conditional"},
		"a rejected lens":            {"a": "support", "b": "support", "c": "abstain", "d": ""},
	}
	for name, verdicts := range cases {
		t.Run(name, func(t *testing.T) {
			outputs := map[string]map[string]any{}
			var rejected []string
			for f, v := range verdicts {
				if v == "" {
					rejected = append(rejected, f)
					continue
				}
				outputs[f] = map[string]any{"verdict": v, "recommendation": "r"}
			}
			store := newTestStore(t)
			runID := startTokenRun(t, store, outputs, rejected...)
			want := wantPlurality(verdicts)
			next, err := GetNextNodes(store.Workflows(), runID)
			if err != nil || len(next) != 1 {
				t.Fatalf("next = %+v, %v", next, err)
			}
			tally := section(t, next[0].ResolvedPrompt, "TALLY:", "NABLA:")
			switch {
			case want != "":
				if !strings.HasSuffix(tally, "Plurality: "+want) {
					t.Errorf("tally %q, want plurality %s", tally, want)
				}
			case !strings.Contains(tally, "Plurality: none"):
				t.Errorf("tally %q, want no plurality", tally)
			}
			computed := computedState(store.Workflows(), runID, "queen")
			if got := computed[TallyPluralityKey]; got != want {
				t.Errorf("computed plurality %v, want %q", got, want)
			}
			votes, abstentions := 0, 0
			for _, v := range verdicts {
				switch v {
				case "":
				case "abstain":
					abstentions++
				default:
					votes++
				}
			}
			if got := computed[TallyVotesKey]; got != votes {
				t.Errorf("computed votes %v, want %d", got, votes)
			}
			if got := computed[TallyAbstentionsKey]; got != abstentions {
				t.Errorf("computed abstentions %v, want %d", got, abstentions)
			}
			if got := computed[TallyLineKey]; got != tally {
				t.Errorf("computed tally line %q, want the line the prompt showed, %q", got, tally)
			}
		})
	}
}
