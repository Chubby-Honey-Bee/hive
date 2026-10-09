package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Chubby-Honey-Bee/hive/internal/review"
	"github.com/spf13/cobra"
)

// newRenderReviewCmd wires `chb render-review`. It reads the JSON
// produced by `chb extract-findings` and writes a Markdown report
// using the embedded template at internal/review/review.md.tmpl.
func newRenderReviewCmd() *cobra.Command {
	var (
		outPath   string
		workflow  string
		provider  string
		runID     string
		costUSD   float64
		tokensIn  int64
		tokensOut int64
	)
	cmd := &cobra.Command{
		Use:   "render-review <findings.json>",
		Short: "Render a self-review Markdown report from extract-findings output",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			meta := review.Meta{
				Workflow:  workflow,
				Provider:  provider,
				RunID:     runID,
				CostUSD:   costUSD,
				TokensIn:  tokensIn,
				TokensOut: tokensOut,
			}
			md, err := renderReviewMarkdown(args[0], meta)
			if err != nil {
				return err
			}
			return writeReviewMarkdown(cmd.OutOrStdout(), cmd.ErrOrStderr(), md, outPath)
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "", "Markdown output path (default: stdout)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "workflow YAML path (banner)")
	cmd.Flags().StringVar(&provider, "provider", "", "provider label (banner)")
	cmd.Flags().StringVar(&runID, "run-id", "", "run id (banner)")
	cmd.Flags().Float64Var(&costUSD, "cost-usd", 0, "cumulative cost in USD")
	cmd.Flags().Int64Var(&tokensIn, "tokens-in", 0, "cumulative input tokens")
	cmd.Flags().Int64Var(&tokensOut, "tokens-out", 0, "cumulative output tokens")
	return cmd
}

// renderReviewMarkdown renders the extract-findings JSON at path as the
// review's Markdown, with meta in its banner.
func renderReviewMarkdown(path string, meta review.Meta) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var agg review.Aggregate
	if err := json.Unmarshal(data, &agg); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	md, err := review.Render(&agg, meta)
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	return md, nil
}

// writeReviewMarkdown writes the review to outPath, or to stdout when
// outPath is empty.
func writeReviewMarkdown(stdout, stderr io.Writer, md, outPath string) error {
	if outPath == "" {
		fmt.Fprint(stdout, md)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(outPath, []byte(md), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Fprintf(stderr, "Wrote %d chars → %s\n", len(md), outPath)
	return nil
}
