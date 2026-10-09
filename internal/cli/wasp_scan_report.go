package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newWASPScanReportCmd implements `chb wasp-scan-report` — prints the
// WASP runtime scan detector's evidence log. Each row is a read-query
// shape that EXPLAIN QUERY PLAN reported as a full table SCAN rather than
// an indexed SEARCH. It is operator evidence that a workload is scanning;
// nothing feeds it to a forager.
func newWASPScanReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "wasp-scan-report",
		Short: "Show the WASP scan detector's log of full-table-scan queries (probe-vs-scan evidence)",
		Long: `Lists the read-query shapes that EXPLAIN QUERY PLAN resolved as a
full table SCAN (unbounded work) instead of an indexed SEARCH (a bounded
probe). The detector records these as the hot read paths run, deduped by
query shape.

Most-hit shapes come first. The report is operator evidence that a
workload is scanning; nothing feeds it to a forager. Whether a scan is one
the workload accepts is recorded in docs/specs/cde-mss.md, not here.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			events, err := store.ScanEvents()
			if err != nil {
				return fmt.Errorf("read scan_events: %w", err)
			}
			out := cmd.OutOrStdout()
			if len(events) == 0 {
				fmt.Fprintln(out, "No scans recorded. Either the hot read paths haven't run in this workspace, or every query resolved to a bounded probe (indexed SEARCH).")
				return nil
			}
			fmt.Fprintf(out, "WASP scan detector — %d distinct scanning query shape(s)\n\n", len(events))
			for _, e := range events {
				fmt.Fprintf(out, "⚠  [%s ×%d]  %s\n", e.TableName, e.HitCount, e.Detail)
				fmt.Fprintf(out, "    %s\n", e.QueryShape)
			}
			fmt.Fprintln(out, "\nOperator evidence: the encoding may be missing an axis for this workload. Nothing feeds this report to a forager.")
			return nil
		},
	}
}
