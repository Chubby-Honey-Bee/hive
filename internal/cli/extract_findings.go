package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Chubby-Honey-Bee/hive/internal/review"
	"github.com/spf13/cobra"
)

// newExtractFindingsCmd wires `chb extract-findings`. It reads
// workflow_node_states.rationale, where the runner stores each agent node's
// final text, parses each agent's structured output with review.Extract,
// which reads it with workflow.ExtractJSONOutput, and writes a consolidated
// JSON aggregate.
//
// Honors the global --db flag (the same flag every other chb
// subcommand uses) so the DB the run wrote is the one we extract from.
func newExtractFindingsCmd() *cobra.Command {
	var (
		runID      int64
		nodePrefix string
		outPath    string
	)
	cmd := &cobra.Command{
		Use:   "extract-findings",
		Short: "Extract self-review audit findings from workflow_node_states.rationale",
		Long: `extract-findings reads the rationale column from a self-review run
(where the runner stores each agent node's final text), parses each agent's
JSON output with the extractor the runner uses, and writes an aggregated
findings.json.

The output JSON has this shape:
  {
    "by_lens":         { "<node>": { "lens", "verdict", "summary", "findings" } },
    "all_findings":    [ ... flat list, sorted by severity ],
    "totals":          { "critical": N, "high": N, "medium": N, "low": N },
    "parse_failures":  [ "<node>", ... ]
  }

Use 'chb render-review' to turn this into a Markdown report.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			agg, err := review.Extract(store.ReadDB, nodePrefix, runID)
			if err != nil {
				return fmt.Errorf("extract: %w", err)
			}
			payload, err := json.MarshalIndent(agg, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal: %w", err)
			}
			if outPath == "" {
				fmt.Fprintln(cmd.OutOrStdout(), string(payload))
				return nil
			}
			return writeExtractedFindings(cmd.ErrOrStderr(), outPath, payload, agg)
		},
	}
	cmd.Flags().Int64Var(&runID, "run-id", 0,
		"workflow run to read (default: the most recent run)")
	cmd.Flags().StringVar(&nodePrefix, "node-prefix", "audit-",
		"only extract from nodes whose name starts with this prefix")
	cmd.Flags().StringVar(&outPath, "out", "",
		"write aggregated JSON here (default: stdout)")
	return cmd
}

// writeExtractedFindings writes the aggregate's JSON to outPath and
// summarises it on w.
func writeExtractedFindings(w io.Writer, outPath string, payload []byte, agg *review.Aggregate) error {
	if err := os.WriteFile(outPath, payload, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	total := agg.Totals["critical"] + agg.Totals["high"] + agg.Totals["medium"] + agg.Totals["low"]
	fmt.Fprintf(w,
		"Extracted %d lens reports → %s; %d findings (%d/%d/%d/%d crit/high/med/low). Parse failures: %d.\n",
		len(agg.ByLens), outPath, total,
		agg.Totals["critical"], agg.Totals["high"], agg.Totals["medium"], agg.Totals["low"],
		len(agg.ParseFailures),
	)
	return nil
}
