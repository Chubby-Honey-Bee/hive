package cli

import (
	"encoding/json"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/gate"
	"github.com/spf13/cobra"
)

func newDetectConflictsCmd() *cobra.Command {
	var wave int
	var dryRun bool
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "detect-conflicts",
		Short: "Detect structural conflicts between findings",
		RunE: func(cmd *cobra.Command, args []string) error {
			var wavePtr *int
			if cmd.Flags().Changed("wave") {
				wavePtr = &wave
			}

			report, err := gate.DetectConflicts(store, wavePtr, dryRun)
			if err != nil {
				return err
			}
			if asJSON {
				return printConflictReportJSON(report, dryRun)
			}
			printConflictReport(report, dryRun)
			return nil
		},
	}
	cmd.Flags().IntVar(&wave, "wave", 0, "only check findings from this wave")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report without writing")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"print one JSON object: dry_run, total, new, numeric, mss_label, negation, details")
	return cmd
}

// printConflictReportJSON prints the detection report as one JSON object,
// details an empty list rather than null when there are none.
func printConflictReportJSON(report *gate.ConflictReport, dryRun bool) error {
	details := report.Details
	if details == nil {
		details = []gate.ConflictItem{}
	}
	return printJSON(map[string]any{
		"dry_run":   dryRun,
		"total":     report.Total,
		"new":       report.New,
		"numeric":   report.Numeric,
		"mss_label": report.MSSLabel,
		"negation":  report.Negation,
		"details":   details,
	})
}

// printConflictReport prints the detection report as text.
func printConflictReport(report *gate.ConflictReport, dryRun bool) {
	fmt.Printf("--- Conflict Detection Report ---\n")
	fmt.Printf("Total conflicts found: %d\n", report.Total)
	fmt.Printf("  Numeric divergence:   %d\n", report.Numeric)
	fmt.Printf("  MSS label mismatch:   %d\n", report.MSSLabel)
	fmt.Printf("  Negation conflicts:   %d\n", report.Negation)

	if len(report.Details) > 0 {
		b, _ := json.MarshalIndent(report.Details, "", "  ")
		fmt.Printf("\nDetails:\n%s\n", string(b))
	}

	if dryRun {
		fmt.Printf("\n[--dry-run] %d new; no conflicts written to database.\n", report.New)
	} else {
		fmt.Printf("\n%d new conflicts written to database; %d already recorded.\n", report.New, report.Total-report.New)
	}
}
