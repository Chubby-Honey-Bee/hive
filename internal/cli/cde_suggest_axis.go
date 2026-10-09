package cli

import (
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/cde"
	"github.com/spf13/cobra"
)

// newCDESuggestAxisCmd implements `chb cde suggest-axis` — prints the
// CDE axis-suggester's evidence: non-coordinate attributes (today:
// mss_label) that distinguish findings *within* the same coordinate cell.
// That clustering is the foundations.md signal that the workload is
// encoding by-attribute rather than by-coordinate — i.e. the attribute
// should become an axis in the proposed slot. The framer reads it through
// the {cde.axis-candidates} token. `chb wasp-scan-report` is the query-side
// counterpart, and it is operator evidence only: nothing feeds it to a
// forager.
func newCDESuggestAxisCmd() *cobra.Command {
	var minEvidence int
	cmd := &cobra.Command{
		Use:   "cde",
		Short: "CDE coordinate-encoding analysis",
	}
	sub := &cobra.Command{
		Use:   "suggest-axis",
		Short: "Suggest CDE axis candidates from how findings cluster by non-coordinate attributes",
		RunE: func(cmd *cobra.Command, args []string) error {
			cands, err := cde.SuggestAxes(store.ReadConn(), minEvidence)
			if err != nil {
				return fmt.Errorf("suggest-axis: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), cde.FormatAxisCandidates(cands))
			return nil
		},
	}
	sub.Flags().IntVar(&minEvidence, "min-evidence", 2, "minimum findings in a coordinate cell for it to count")
	cmd.AddCommand(sub)
	return cmd
}
