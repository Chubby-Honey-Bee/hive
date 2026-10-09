package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/embed"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"github.com/spf13/cobra"
)

// newSwarmRecallCmd implements `chb recall "<question>"`
// — semantic retrieval of prior swarms whose questions are close to
// the new one. Returns the K nearest prior questions and their
// Queen verdicts (where available).
//
//	chb recall "should we ship the bee plushie?"
//	chb recall "<q>" --top 5 --provider stub
func newSwarmRecallCmd() *cobra.Command {
	var o recallOptions
	cmd := &cobra.Command{
		Use:   "recall <question>",
		Short: "Semantically retrieve prior swarm runs whose question was close to yours",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd.OutOrStdout(), strings.Join(args, " "))
		},
	}
	cmd.Flags().IntVar(&o.top, "top", 5, "max matches to return")
	cmd.Flags().StringVar(&o.provider, "provider", "", "embedding provider (openai|localhttp|stub)")
	cmd.Flags().StringVar(&o.modelName, "model", "", "embedding model")
	cmd.Flags().BoolVar(&o.emitJSON, "json", false, "emit JSON")
	return cmd
}

// recallOptions holds the flags of chb recall.
type recallOptions struct {
	top       int
	provider  string
	modelName string
	emitJSON  bool
}

// recallMatch is one prior question close to the new one, with the Queen's
// answer from the run that asked it.
type recallMatch struct {
	Score          float32 `json:"score"`
	RunID          int64   `json:"run_id"`
	Question       string  `json:"question"`
	Verdict        string  `json:"verdict,omitempty"`
	Recommendation string  `json:"recommendation,omitempty"`
	Report         string  `json:"report,omitempty"`
}

// run recalls the prior questions nearest question and prints them.
func (o *recallOptions) run(out io.Writer, question string) error {
	if err := store.Init(); err != nil {
		return err
	}
	matches, err := o.nearestQuestions(question)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		fmt.Fprintf(out,
			"nothing to recall: this workspace has no embedded questions.\n"+
				"  chb comb embed --questions    # index the questions prior runs were asked\n")
		return nil
	}
	return o.print(out, question, recallMatches(matches))
}

// nearestQuestions embeds question and returns the --top nearest prior
// questions. It matches questions against questions: `chb comb embed
// --questions` stores one embedding per run of the question it was asked,
// and that is what a new asker actually holds.
func (o *recallOptions) nearestQuestions(question string) ([]embed.Match, error) {
	p, err := selectProvider(o.provider, o.modelName)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	vec, err := p.Embed(ctx, question)
	if err != nil {
		return nil, fmt.Errorf("embed question: %w", err)
	}
	searcher := embed.NewSearcher(store, p.Name())
	return searcher.NearestK(vec, o.top, db.VantageQuestion, "")
}

// recallMatches pairs each match with the answer of the run that asked it.
func recallMatches(matches []embed.Match) []recallMatch {
	out := make([]recallMatch, 0, len(matches))
	for _, m := range matches {
		out = append(out, recallMatchFor(m))
	}
	return out
}

// recallMatchFor is the match with that run's own verdict: the queen node's
// final text in the run that asked the matched question. The Comb's
// forager:queen vantage cannot serve here: the queen is not a forager node,
// so nothing writes it, and a vantage holds only the latest run.
func recallMatchFor(m embed.Match) recallMatch {
	rm := recallMatch{Score: m.Score, Question: m.SourceText}
	fmt.Sscanf(m.VantageKey, "question:%d", &rm.RunID)
	var text, outputs string
	if err := store.ReadDB.QueryRow(
		`SELECT COALESCE(rationale, ''), COALESCE(outputs_json, '') FROM workflow_node_states WHERE run_id = ? AND node_name = 'queen'`,
		rm.RunID,
	).Scan(&text, &outputs); err == nil {
		rm.Verdict, rm.Recommendation, rm.Report = queenReply(text, outputs)
	}
	return rm
}

// print prints the matches, as indented JSON with --json.
func (o *recallOptions) print(out io.Writer, question string, matches []recallMatch) error {
	if o.emitJSON {
		b, _ := json.MarshalIndent(matches, "", "  ")
		fmt.Fprintln(out, string(b))
		return nil
	}
	fmt.Fprintf(out, "recalled %d prior questions similar to: %s\n", len(matches), question)
	// Each match carries its run's queen output here too, on one line and
	// truncated.
	for _, m := range matches {
		printRecallMatch(out, m)
	}
	return nil
}

// printRecallMatch prints one match and, when there is one, its run's queen
// output.
func printRecallMatch(out io.Writer, m recallMatch) {
	fmt.Fprintf(out, "  %.4f  run %-5d %s\n", m.Score, m.RunID, truncFor(m.Question, 100))
	if v := m.queenLine(); v != "" {
		fmt.Fprintf(out, "          queen: %s\n", truncFor(v, 200))
	}
}

// queenLine is the run's queen output on one line: her verdict with her
// recommendation, else her report.
func (m recallMatch) queenLine() string {
	v := m.Verdict
	if v != "" && strings.TrimSpace(m.Recommendation) != "" {
		v += " — " + m.Recommendation
	}
	if v == "" {
		v = m.Report
	}
	return strings.Join(strings.Fields(v), " ")
}

// queenReply reads a Queen's answer from her node row: her JSON object's
// verdict, recommendation and report, from her stored outputs, else from her
// text (ExtractJSONOutput, which also reads an object in a code fence or
// after a preamble). A Queen whose object holds no verdict wrote prose, and
// her whole text is her report.
func queenReply(text, outputsJSON string) (verdict, recommendation, report string) {
	var o map[string]any
	if json.Unmarshal([]byte(outputsJSON), &o) != nil || o["verdict"] == nil {
		o = workflow.ExtractJSONOutput(text)
	}
	verdict, _ = o["verdict"].(string)
	if verdict == "" {
		return "", "", text
	}
	recommendation, _ = o["recommendation"].(string)
	report, _ = o["report"].(string)
	return verdict, recommendation, report
}
