package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

// newCalibrationExportCmd returns `chb calibration-export`: this
// workspace's calibration counts, keyed as the scores are, with no
// derived value.
func newCalibrationExportCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "calibration-export [--out PATH]",
		Short: "Emit this workspace's calibration counts, never its weights, for a cross-project merge",
		Long: `Fold the outcomes ledger as chb calibrate does and print the counts per
predictor and scope as one JSON object (format hive-calibration-counts/1):
confirmed, partial, refuted, the Brier sum and its count, every scope's
outcome total, and the provenance (the database, when, how many outcomes).
No hit rate, weight or calibrated flag: those are recomputed by whoever
merges. It opens no tick and writes nothing. chb calibration-merge sums
such files and re-runs the formula.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCalibrationExport(out)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write the JSON to this file instead of stdout")
	return cmd
}

// runCalibrationExport folds the outcomes ledger into counts and prints them
// as JSON, or writes them to out.
func runCalibrationExport(out string) error {
	names, err := foragerNames()
	if err != nil {
		return err
	}
	b, err := calibration.Export(store, store.Path, names)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return writeCalibrationCounts(out, append(raw, '\n'), b)
}

// writeCalibrationCounts writes the counts JSON to stdout, or to out with a
// line saying what it wrote.
func writeCalibrationCounts(out string, raw []byte, b *calibration.Bundle) error {
	if out == "" {
		_, err := os.Stdout.Write(raw)
		return err
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("calibration counts written to %s: %d outcomes, %d keys, %d scopes\n", out, b.Outcomes, len(b.Counts), len(b.ScopeTotals))
	return nil
}

// newCalibrationMergeCmd returns `chb calibration-merge <files…>`: the
// scores a recompute over the joined ledgers would write, printed, never
// stored.
func newCalibrationMergeCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "calibration-merge <counts.json>...",
		Short: "Sum calibration counts from several workspaces and re-run the formula",
		Long: `Read files chb calibration-export wrote, sum their counts and scope totals
key by key, and apply the formula over the union, deciding each scope's
eligibility over the summed total. Merging counts rather than weights keeps
the merge commutative and associative. The result is printed and written to
no database: calibration_scores stays a function of this workspace's own
ledger. A file of another format, a key a file lists twice, or counts that
do not add up is refused.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCalibrationMerge(args, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object")
	return cmd
}

// runCalibrationMerge sums the counts files and prints the scores the
// formula gives over the union.
func runCalibrationMerge(paths []string, asJSON bool) error {
	bundles, sources, err := readCalibrationBundles(paths)
	if err != nil {
		return err
	}
	rows, err := calibration.Merge(bundles...)
	if err != nil {
		return err
	}
	sortCalibrationSources(sources)
	if asJSON {
		return jsonPrint(map[string]any{"sources": sources, "scores": calibration.Views(rows), "note": calibration.CorrelationalNote})
	}
	printCalibrationMerge(len(bundles), rows)
	return nil
}

// readCalibrationBundles reads every counts file, returning the bundles and
// a description of where each came from.
func readCalibrationBundles(paths []string) ([]*calibration.Bundle, []map[string]any, error) {
	bundles := make([]*calibration.Bundle, 0, len(paths))
	sources := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		b, err := readCalibrationBundle(path)
		if err != nil {
			return nil, nil, err
		}
		bundles = append(bundles, b)
		sources = append(sources, map[string]any{"file": path, "source": b.Source, "exported_at": b.ExportedAt, "outcomes": b.Outcomes})
	}
	return bundles, sources, nil
}

// readCalibrationBundle reads one counts file; a decode error names the
// file.
func readCalibrationBundle(path string) (*calibration.Bundle, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b calibration.Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &b, nil
}

// sortCalibrationSources orders the merge's sources by source, then by
// export time.
func sortCalibrationSources(sources []map[string]any) {
	sort.Slice(sources, func(i, j int) bool {
		if sources[i]["source"] != sources[j]["source"] {
			return sources[i]["source"].(string) < sources[j]["source"].(string)
		}
		return sources[i]["exported_at"].(string) < sources[j]["exported_at"].(string)
	})
}

// printCalibrationMerge prints the merged scores, which no database stores.
func printCalibrationMerge(workspaces int, rows []*db.ScoreRow) {
	fmt.Printf("merged calibration scores over %d workspaces: %d\n", workspaces, len(rows))
	for _, r := range rows {
		fmt.Printf("  %s\n", formatScore(r))
	}
	fmt.Println("merged, not stored: this workspace's calibration_scores are unchanged")
	fmt.Println(calibration.CorrelationalNote)
}
