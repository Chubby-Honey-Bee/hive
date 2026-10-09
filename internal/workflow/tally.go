package workflow

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

const (
	// TallyPluralityKey is the state key the engine writes, when a node
	// whose prompt reads {tally} completes, before its accept: runs: the
	// tally's plurality, or "" when there is none. It is the engine's count,
	// not the model's, so it goes to state and never into the node's outputs.
	TallyPluralityKey = "tally_plurality"
	// TallyVotesKey, TallyAbstentionsKey and TallyLineKey are written
	// beside it: the votes cast, one for each forager whose verdict is not
	// abstain; the foragers that abstained; and the tally as {tally}
	// renders it. A forager with no verdict counts in neither.
	TallyVotesKey       = "tally_votes"
	TallyAbstentionsKey = "tally_abstentions"
	TallyLineKey        = "tally_line"
)

// computedState is what the engine writes to state when the node completes,
// before its accept: runs: for a node whose prompt reads {tally}, the
// tally's plurality under TallyPluralityKey, "" on a tie, with no vote, or
// when the node states cannot be read; the votes cast; the abstentions;
// and the tally's line. The forager nodes are final by then, because the
// node waited for them, so it is the count its prompt showed. It returns
// nil for any other node.
func computedState(repo Store, runID int64, nodeName string) map[string]any {
	defn := tallyReaderDefinition(repo, runID, nodeName)
	if defn == nil {
		return nil
	}
	outcomes, err := runForagers(repo, runID, defn)
	if err != nil {
		return map[string]any{TallyPluralityKey: "", TallyVotesKey: 0, TallyAbstentionsKey: 0,
			TallyLineKey: fmt.Sprintf("unavailable (%v)", err)}
	}
	t := tallyVerdicts(outcomes)
	return map[string]any{
		TallyPluralityKey:   t.plurality,
		TallyVotesKey:       t.votes(),
		TallyAbstentionsKey: len(t.counts[abstainVerdict]),
		TallyLineKey:        t.String(),
	}
}

// tallyReaderDefinition is the run's definition when nodeName's prompt
// reads {tally}, else nil.
func tallyReaderDefinition(repo Store, runID int64, nodeName string) map[string]any {
	defn := definitionHolding(repo, runID, tallyToken)
	nodes, _ := defn["nodes"].(map[string]any)
	node, _ := nodes[nodeName].(map[string]any)
	if !strings.Contains(nodePrompt(node), tallyToken) {
		return nil
	}
	return defn
}

// definitionHolding is a run's definition when its YAML text holds token,
// else nil, as it is when the run cannot be read or its definition does not
// parse. A definition without the token is not parsed.
func definitionHolding(repo Store, runID int64, token string) map[string]any {
	run, err := repo.GetWorkflowRun(runID)
	if err != nil || !strings.Contains(run.DefinitionYAML, token) {
		return nil
	}
	defn, _ := LoadYAMLString(run.DefinitionYAML)
	return defn
}

// abstainVerdict is the verdict that casts no vote: it counts in the tally's
// list but not toward the plurality, as it fires no ∇.
const abstainVerdict = "abstain"

// verdictTally counts this run's forager verdicts.
type verdictTally struct {
	counts    map[string][]string // verdict → the foragers that returned it, sorted
	missing   []string            // "<forager> (<status>)" for a forager with no verdict
	plurality string              // the voting verdict with strictly the most votes; "" on a tie or with no vote
}

// tallyVerdicts counts every verdict this run's foragers returned. Abstain
// is listed and casts no vote: the plurality is the one other verdict with
// strictly the most votes.
func tallyVerdicts(outcomes map[string]foragerOutcome) verdictTally {
	t := verdictTally{counts: map[string][]string{}}
	for name, o := range outcomes {
		if v := o.verdict(); v != "" {
			t.counts[v] = append(t.counts[v], name)
		} else {
			t.missing = append(t.missing, fmt.Sprintf("%s (%s)", name, o.status))
		}
	}
	for _, names := range t.counts {
		sort.Strings(names)
	}
	sort.Strings(t.missing)
	t.plurality = pluralityOf(t.counts)
	return t
}

// pluralityOf is the voting verdict with strictly the most votes, "" on a
// tie or with no vote.
func pluralityOf(counts map[string][]string) string {
	voting := maps.Clone(counts)
	delete(voting, abstainVerdict)
	best := maxVotes(voting)
	var leaders []string
	for v, names := range voting {
		if len(names) == best {
			leaders = append(leaders, v)
		}
	}
	if len(leaders) != 1 {
		return ""
	}
	return leaders[0]
}

func maxVotes(counts map[string][]string) int {
	best := 0
	for _, names := range counts {
		best = max(best, len(names))
	}
	return best
}

// votes is the votes cast: one for each forager whose verdict is not
// abstain.
func (t verdictTally) votes() int {
	n := 0
	for v, names := range t.counts {
		if v != abstainVerdict {
			n += len(names)
		}
	}
	return n
}

// total is the verdicts returned, abstentions included.
func (t verdictTally) total() int {
	n := 0
	for _, names := range t.counts {
		n += len(names)
	}
	return n
}

// String renders the tally on one line: each verdict with its count and
// foragers, most votes first, the foragers with no verdict, then the
// plurality, or why there is none.
func (t verdictTally) String() string {
	verdicts := verdictsByVotes(t.counts)
	return t.countsText(verdicts) + t.missingText() + t.pluralityText(verdicts)
}

// verdictsByVotes is a tally's verdicts, most votes first, ties in name
// order.
func verdictsByVotes(counts map[string][]string) []string {
	verdicts := slices.Collect(maps.Keys(counts))
	slices.SortFunc(verdicts, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(counts[b]), len(counts[a])), cmp.Compare(a, b))
	})
	return verdicts
}

func (t verdictTally) countsText(verdicts []string) string {
	total := t.total()
	if total == 0 {
		return "no forager returned a verdict"
	}
	parts := make([]string, 0, len(verdicts))
	for _, v := range verdicts {
		parts = append(parts, fmt.Sprintf("%s %d (%s)", v, len(t.counts[v]), strings.Join(t.counts[v], ", ")))
	}
	return fmt.Sprintf("%d verdicts: %s", total, strings.Join(parts, "; "))
}

func (t verdictTally) missingText() string {
	if len(t.missing) == 0 {
		return ""
	}
	return "; no verdict from " + strings.Join(t.missing, ", ")
}

// pluralityText is the plurality, or why there is none: no vote was cast,
// or the most voted verdicts tie.
func (t verdictTally) pluralityText(verdicts []string) string {
	voting := votingVerdicts(verdicts)
	switch {
	case t.plurality != "":
		return ". Plurality: " + t.plurality
	case len(voting) == 0:
		return t.noVoteText()
	}
	return ". Plurality: none, a tie between " + strings.Join(t.tied(voting), " and ")
}

// noVoteText says why no vote was cast: every verdict abstains, or there
// is none.
func (t verdictTally) noVoteText() string {
	if t.total() > 0 {
		return ". Plurality: none, every verdict abstains"
	}
	return ". Plurality: none"
}

// tied is the voting verdicts, most votes first, with as many votes as the
// first.
func (t verdictTally) tied(voting []string) []string {
	var tied []string
	for _, v := range voting {
		if len(t.counts[v]) == len(t.counts[voting[0]]) {
			tied = append(tied, v)
		}
	}
	return tied
}

// votingVerdicts is verdicts without abstain, in order.
func votingVerdicts(verdicts []string) []string {
	var voting []string
	for _, v := range verdicts {
		if v != abstainVerdict {
			voting = append(voting, v)
		}
	}
	return voting
}
