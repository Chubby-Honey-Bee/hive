package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Calibration is how sure a swarm run was, read from its rows before what
// it said: the Queen's convergence, the engine's tally with its margin,
// whether she wrote a dissent, and the ∇ pairs that fired. chb ask prints
// it ahead of the verdict and the artifact records it (swarm.md § The
// verdict leads with its calibration).
type Calibration struct {
	// QueenStatus is the queen node's status; "" when the run has none.
	QueenStatus string `json:"queen_status"`
	// Convergence is her convergence field; "" when she left no outputs.
	Convergence string `json:"convergence"`
	// DissentWritten: her dissent_from_plurality is not blank.
	DissentWritten bool `json:"dissent_written"`
	// Tally is the tally as {tally} rendered it for her.
	Tally string `json:"tally"`
	// Plurality is the voting verdict with strictly the most votes; "" on a
	// tie or with no vote. Margin is its votes less the runner-up's, 0 when
	// there is none. Votes and Abstentions count the lenses that cast a vote
	// and that abstained.
	Plurality   string `json:"plurality"`
	Margin      int    `json:"margin"`
	Votes       int    `json:"votes"`
	Abstentions int    `json:"abstentions"`
	// NablaFired lists the resonates pairs whose verdicts converged, each
	// `a↔b (verdict)`, sorted; empty when none did.
	NablaFired []string `json:"nabla_fired"`

	counts         map[string][]string
	missing        []string
	pairs          int
	verdict        string
	recommendation string
	report         string
}

// queenNode is the synthesis node's name in a generated swarm.
const queenNode = "queen"

// RunCalibration reads one run's calibration from its node rows and its
// workflow's resonates pairs, as the Queen's tokens read them.
func RunCalibration(repo Store, runID int64) (Calibration, error) {
	defn, outcomes, err := readRunForagers(repo, runID)
	if err != nil {
		return Calibration{}, err
	}
	states, err := repo.GetWorkflowNodeStates(runID)
	if err != nil {
		return Calibration{}, err
	}
	c := tallyCalibration(tallyVerdicts(outcomes))
	pairs := ResonatesPairs(defn)
	c.pairs, c.NablaFired = len(pairs), firedPairs(pairs, outcomes)
	readQueen(&c, states)
	return c, nil
}

// tallyCalibration is the part of a calibration the tally gives: its line,
// the plurality and its margin, the votes and the abstentions.
func tallyCalibration(t verdictTally) Calibration {
	c := Calibration{Tally: t.String(), Plurality: t.plurality, counts: t.counts, missing: t.missing}
	var sizes []int
	for v, names := range t.counts {
		if v == abstainVerdict {
			c.Abstentions = len(names)
			continue
		}
		c.Votes += len(names)
		sizes = append(sizes, len(names))
	}
	c.Margin = pluralityMargin(t.plurality, sizes)
	return c
}

// pluralityMargin is the plurality's votes less the runner-up's, given each
// voting verdict's votes; 0 when there is no plurality.
func pluralityMargin(plurality string, sizes []int) int {
	if plurality == "" {
		return 0
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	if len(sizes) > 1 {
		return sizes[0] - sizes[1]
	}
	return sizes[0]
}

// readQueen reads the queen node's status into c and, when she completed
// with outputs that parse, what she wrote.
func readQueen(c *Calibration, states []db.WorkflowNodeState) {
	for _, s := range states {
		if s.NodeName == queenNode {
			c.QueenStatus = s.Status
			readQueenOutputs(c, s)
		}
	}
}

// readQueenOutputs reads the Queen's convergence, whether she wrote a
// dissent, her verdict, recommendation and report.
func readQueenOutputs(c *Calibration, s db.WorkflowNodeState) {
	var out map[string]any
	if s.Status != "completed" || !s.OutputsJSON.Valid || json.Unmarshal([]byte(s.OutputsJSON.String), &out) != nil {
		return
	}
	c.Convergence, _ = out["convergence"].(string)
	d, _ := out["dissent_from_plurality"].(string)
	c.DissentWritten = strings.TrimSpace(d) != ""
	c.verdict, _ = out["verdict"].(string)
	c.recommendation, _ = out["recommendation"].(string)
	c.report, _ = out["report"].(string)
}

// Verdict is the Queen's verdict, "" when she did not complete. Verdict,
// Recommendation and Report are not part of the calibration's JSON, which
// the artifact records: chb ask --json carries them beside it.
func (c Calibration) Verdict() string { return c.verdict }

// Recommendation is the Queen's recommendation, "" when she did not
// complete.
func (c Calibration) Recommendation() string { return c.recommendation }

// Report is the Queen's report, "" when she did not complete.
func (c Calibration) Report() string { return c.report }

// Lines is what chb ask prints: how sure the swarm was, then the ∇ pairs,
// then the verdict, so a reader sees the first before the last.
func (c Calibration) Lines() []string {
	return []string{c.quorumLine(), "∇ fired: " + c.nablaText(), c.verdictLine()}
}

func (c Calibration) quorumLine() string {
	conv := "convergence " + c.Convergence
	if c.Convergence == "" {
		conv = "convergence none (" + c.queenWhy() + ")"
	}
	dissent := "no dissent written"
	if c.DissentWritten {
		dissent = "dissent written"
	}
	return fmt.Sprintf("Quorum: %s; tally %s; %s; %s", conv, c.tallyText(), c.pluralityText(), dissent)
}

// tallyText is the tally without the lenses' names: each verdict with its
// count, most votes first, then the lenses with no verdict.
func (c Calibration) tallyText() string {
	parts := make([]string, 0, len(c.counts))
	for _, v := range verdictsByVotes(c.counts) {
		parts = append(parts, fmt.Sprintf("%s %d", v, len(c.counts[v])))
	}
	text := strings.Join(parts, ", ")
	if text == "" {
		text = "no verdict"
	}
	if len(c.missing) > 0 {
		text += "; no verdict from " + strings.Join(c.missing, ", ")
	}
	return text
}

func (c Calibration) pluralityText() string {
	switch {
	case c.Plurality != "":
		return fmt.Sprintf("plurality %s, margin %d", c.Plurality, c.Margin)
	case c.Votes == 0:
		return "plurality none (no vote cast)"
	}
	return "plurality none (tie)"
}

func (c Calibration) nablaText() string {
	switch {
	case c.pairs == 0:
		return "none — this swarm declares no resonates pairs"
	case len(c.NablaFired) == 0:
		return "none"
	}
	return strings.Join(c.NablaFired, "; ")
}

func (c Calibration) verdictLine() string {
	if c.verdict == "" {
		return "Verdict: none (" + c.queenWhy() + ")"
	}
	if c.recommendation == "" {
		return "Verdict: " + c.verdict
	}
	return "Verdict: " + c.verdict + " — " + c.recommendation
}

func (c Calibration) queenWhy() string {
	if c.QueenStatus == "" {
		return "this run has no queen node"
	}
	return fmt.Sprintf("%s is %s in this run", queenNode, c.QueenStatus)
}
