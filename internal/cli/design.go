package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Chubby-Honey-Bee/hive/internal/design"
	"github.com/Chubby-Honey-Bee/hive/internal/harness"
)

func newDesignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "design",
		Short: "Report on the executability benchmark's results and apply its rule",
	}
	cmd.AddCommand(newDesignReportCmd())
	return cmd
}

func newDesignReportCmd() *cobra.Command {
	var alpha float64
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "report [--alpha A] <results.jsonl>...",
		Short: "Print the per-task and per-arm table of design bench results and apply the pre-registered rule",
		Long: `Reads the results.jsonl files that chb agent-harness design cases write and
prints, per task and arm, the hidden tests passed, the plan's steps, the
deviations and additions the executor reported, the file coverage, the
claims' labels and the audit; then each arm's means; then the verdict of the
rule in docs/specs/bench-design.md: the designer arm is worth its cost
only when the sign test does not show it worse than the control on hidden
tests or on plan fidelity, and its mean is not below the control's on
either. A file marked not_measured is refused.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := readBenchRowFiles(args, design.ReadRows)
			if err != nil {
				return err
			}
			arms, d := design.Summarize(rows), design.Decide(rows, alpha)
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				enc.SetEscapeHTML(false)
				return enc.Encode(map[string]any{"rows": rows, "arms": arms, "decision": d})
			}
			var md strings.Builder
			harness.WriteDesignTables(&md, rows, arms, d)
			_, err = fmt.Fprint(cmd.OutOrStdout(), md.String())
			return err
		},
	}
	cmd.Flags().Float64Var(&alpha, "alpha", design.DefaultAlpha, "the sign test's level")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the rows, the means and the decision as JSON")
	return cmd
}
