package cli

import (
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/gate"
	"github.com/spf13/cobra"
)

func newSwarmMergeCmd() *cobra.Command {
	var wave int
	var quorumThreshold int
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "swarm-merge",
		Short: "Merge and score findings by convergence",
		RunE: func(cmd *cobra.Command, args []string) error {
			var wavePtr *int
			if cmd.Flags().Changed("wave") {
				wavePtr = &wave
			}
			if asJSON {
				return mergeFindingsJSON(wavePtr)
			}
			return printMergeFindings(wavePtr, quorumThreshold)
		},
	}
	cmd.Flags().IntVar(&wave, "wave", 0, "only merge findings from this wave")
	cmd.Flags().IntVar(&quorumThreshold, "quorum", 0, "quorum threshold for capping candidates")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"print the merge stats as one JSON object: total_findings, coordinate_groups, near_duplicates, convergence_updates, conflicts_detected")
	return cmd
}

// mergeFindingsJSON merges the findings of one wave, or of all, and prints
// the merge stats as one JSON object.
func mergeFindingsJSON(wave *int) error {
	stats, err := gate.MergeFindings(store, wave, 3, 2, 0.7)
	if err != nil {
		return err
	}
	return printJSON(stats)
}

// printMergeFindings merges the findings of one wave, or of all, and prints
// the merge stats as text.
func printMergeFindings(wave *int, quorumThreshold int) error {
	if wave != nil {
		fmt.Printf("Merging findings for wave %d...\n", *wave)
	} else {
		fmt.Println("Merging findings (all waves)...")
	}

	stats, err := gate.MergeFindings(store, wave, 3, 2, 0.7)
	if err != nil {
		return err
	}

	fmt.Printf("\nMerge Results:\n")
	fmt.Printf("  Total findings: %d\n", stats.TotalFindings)
	fmt.Printf("  Coordinate groups: %d\n", stats.CoordinateGroups)
	fmt.Printf("  Near-duplicates (clustered, not removed): %d\n", stats.NearDuplicates)
	fmt.Printf("  Convergence: %v\n", stats.ConvergenceUpdates)
	fmt.Printf("  Conflicts detected: %d\n", stats.ConflictsDetected)

	if quorumThreshold > 0 {
		// Quorum check via db-read
		fmt.Printf("\nQuorum check (threshold=%d): use 'chb db-read' for quorum queries\n", quorumThreshold)
	}

	return nil
}
