package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/review"
	"github.com/spf13/cobra"
)

// newGenImplementWorkflowCmd wires `chb gen-implement-workflow` —
// turns a self-review findings.json into a workflow YAML that an
// autonomous agent-run can dispatch end-to-end.
//
// One agent node per filtered finding, each gated on
// `outputs.compile_ok && outputs.tests_pass` with `on_reject:` escalation
// to tier synthesist (or --repair-model) for one repair attempt. Plus three
// final-gate command nodes that re-run go test, chb validate and
// chb replay-behavior, each failing the run on a non-zero exit.
//
// Use it with:
//
//	chb gen-implement-workflow workspace/self-review/findings.json \
//	    --severity high --max-fixes 5 --out /tmp/impl.yaml
//	chb preflight /tmp/impl.yaml
//	chb agent-run /tmp/impl.yaml --branch self-implement/<date> --auto-pr
func newGenImplementWorkflowCmd() *cobra.Command {
	var (
		outPath     string
		severities  []string
		maxFixes    int
		model       string
		repairModel string
		name        string
	)
	cmd := &cobra.Command{
		Use:   "gen-implement-workflow <findings.json>",
		Short: "Generate an autonomous-fix workflow YAML from a self-review findings.json",
		Long: `Reads a self-review findings.json (produced by 'chb extract-findings')
and writes a workflow YAML in which each agent node applies one finding's fix.

Each generated agent node has:
  - prompt embedding the file/line/issue/fix
  - tier: worker, or model: <--model> when --model is given
  - outputs: [compile_ok, tests_pass, diff_summary]
  - accept: outputs.compile_ok == true AND outputs.tests_pass == true
  - on_reject: one retry on tier: synthesist, or model: <--repair-model>
    when --repair-model is given

A tier resolves to a model under the run's budget mode (--budget-mode on
agent-run).

Three final command nodes run go test ./..., chb validate and chb
replay-behavior in turn, with no model: the runner records each exit code,
and a non-zero exit fails the run.

Run the generated YAML with:
  chb preflight <out>
  chb agent-run <out> --branch self-implement/<date> --auto-pr`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agg, err := readReviewAggregate(args[0])
			if err != nil {
				return err
			}
			yaml, count, err := review.GenerateImplementWorkflow(agg, review.ImplementOptions{
				Severities:  severities,
				MaxFixes:    maxFixes,
				Model:       model,
				RepairModel: repairModel,
				Name:        name,
			})
			if err != nil {
				return fmt.Errorf("generate: %w", err)
			}
			if outPath == "" {
				fmt.Fprint(cmd.OutOrStdout(), yaml)
				return nil
			}
			return writeImplementWorkflow(cmd.ErrOrStderr(), outPath, yaml, count, severities)
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "",
		"workflow YAML output path (default: stdout)")
	cmd.Flags().StringSliceVar(&severities, "severity", []string{"critical", "high"},
		"only generate fix nodes for findings of these severities (comma-separated)")
	cmd.Flags().IntVar(&maxFixes, "max-fixes", 5,
		"cap on the number of fix nodes generated; 0 = no cap")
	cmd.Flags().StringVar(&model, "model", "",
		"model for the initial fix attempt on every fix node (default: tier worker under the budget mode)")
	cmd.Flags().StringVar(&repairModel, "repair-model", "",
		"model for the on_reject: retry when a fix fails accept: (default: tier synthesist under the budget mode)")
	cmd.Flags().StringVar(&name, "name", "",
		"workflow name embedded in the YAML (default: self-implement-<date>)")
	return cmd
}

// readReviewAggregate reads a self-review findings.json.
func readReviewAggregate(path string) (*review.Aggregate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var agg review.Aggregate
	if err := json.Unmarshal(data, &agg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &agg, nil
}

// writeImplementWorkflow writes the generated workflow to outPath, creating
// its directory, and says on w what it wrote.
func writeImplementWorkflow(w io.Writer, outPath, yaml string, count int, severities []string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(outPath, []byte(yaml), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Fprintf(w,
		"Wrote %d-fix workflow → %s (severities: %s)\n",
		count, outPath, strings.Join(severities, ","),
	)
	return nil
}
