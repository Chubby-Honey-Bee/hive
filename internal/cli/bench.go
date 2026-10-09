package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Chubby-Honey-Bee/hive/internal/bench"
)

func newBenchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Decide between model configurations from graded twin runs",
	}
	cmd.AddCommand(newBenchDecideCmd())
	return cmd
}

func newBenchDecideCmd() *cobra.Command {
	var rulePath string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "decide [--rule RULE.yaml] <results.jsonl>...",
		Short: "Apply the pre-registered decision rule to bench results",
		Long: `Reads the results.jsonl files that chb agent-harness bench cases write —
one per configuration run — and applies the decision rule in RULE.yaml
(docs/specs/bench.md): validity (the reference passes every guard, the
negative control is rejected), the hard guards, paired non-inferiority
against the reference, HIVE lift over the solo control, one choice per
machine class, and confirmation on fresh seeds. "inconclusive" is a normal
outcome: it means the runs cannot tell.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBenchDecide(cmd.OutOrStdout(), rulePath, args, asJSON)
		},
	}
	cmd.Flags().StringVar(&rulePath, "rule", "fixtures/bench/rule.yaml", "the pre-registered rule")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the decision as JSON")
	return cmd
}

// runBenchDecide applies the rule at rulePath to the rows of the results
// files and prints the decision.
func runBenchDecide(w io.Writer, rulePath string, paths []string, asJSON bool) error {
	rule, err := bench.LoadRule(rulePath)
	if err != nil {
		return err
	}
	rows, err := readBenchRowFiles(paths, bench.ReadRows)
	if err != nil {
		return err
	}
	d := bench.Decide(rows, rule)
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(d)
	}
	printDecision(w, d, rule)
	return nil
}

// readBenchRowFiles reads every results file's rows with read, in the order
// the paths are given.
func readBenchRowFiles[R any](paths []string, read func(io.Reader) ([]R, error)) ([]R, error) {
	var rows []R
	for _, path := range paths {
		rs, err := readBenchRowFile(path, read)
		if err != nil {
			return nil, err
		}
		rows = append(rows, rs...)
	}
	return rows, nil
}

// readBenchRowFile reads one results file's rows with read; a read error
// names the file.
func readBenchRowFile[R any](path string, read func(io.Reader) ([]R, error)) ([]R, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	rs, err := read(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rs, nil
}

func printDecision(w io.Writer, d bench.Decision, rule bench.Rule) {
	fmt.Fprintf(w, "verdict: %s\n", d.Verdict)
	for _, r := range d.Reasons {
		fmt.Fprintf(w, "  - %s\n", r)
	}
	if d.Ignored > 0 {
		fmt.Fprintf(w, "  (%d row(s) on neither seed list were ignored)\n", d.Ignored)
	}
	fmt.Fprintf(w, "\nreference %s · negative control %s · delta %.2f · alpha %.2f\n\n", rule.Reference, rule.Negative, rule.Delta, rule.Alpha)
	for _, c := range d.Configs {
		printBenchConfig(w, c)
	}
	printBenchChoices(w, d.Choices)
}

// printBenchConfig prints one configuration's outcome, accuracy, stages and
// reasons.
func printBenchConfig(w io.Writer, c bench.ConfigResult) {
	fmt.Fprintf(w, "%s (%s): %s\n", c.Config, c.Role, orDash(c.Outcome))
	fmt.Fprintf(w, "  class-balanced accuracy: swarm %s, solo %s, lift %s; median wall %.1fs\n",
		fmtPtr(c.SwarmCBA), fmtPtr(c.SoloCBA), fmtSigned(c.Lift), c.MedianWall)
	printStage(w, "selection", c.Selection)
	if c.Confirmation != nil {
		printStage(w, "confirmation", *c.Confirmation)
	}
	for _, r := range c.Reasons {
		fmt.Fprintf(w, "  → %s\n", r)
	}
}

// printBenchChoices prints the choice per machine class, nothing when there
// is none.
func printBenchChoices(w io.Writer, choices []bench.Choice) {
	if len(choices) == 0 {
		return
	}
	fmt.Fprintln(w, "\nchoices:")
	for _, ch := range choices {
		fmt.Fprintf(w, "  %s: %s %s (tried: %s)\n", ch.Class, ch.Outcome, ch.Config, strings.Join(ch.Tried, ", "))
	}
}

func printStage(w io.Writer, name string, st bench.Stage) {
	if len(st.Guards) == 0 && len(st.Margins) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s guards: %s\n", name, strings.Join(benchStageGuards(st.Guards), "; "))
	for _, m := range st.Margins {
		printBenchMargin(w, name, m)
	}
}

// benchStageGuards describes each guard that did not pass, or is "all pass"
// when every one did.
func benchStageGuards(checks []bench.Check) []string {
	var guards []string
	for _, g := range checks {
		if g.Status != bench.Pass {
			guards = append(guards, fmt.Sprintf("%s %s (%s)", g.Name, g.Status, g.Detail))
		}
	}
	if len(guards) == 0 {
		return []string{"all pass"}
	}
	return guards
}

// printBenchMargin prints one non-inferiority margin of a stage.
func printBenchMargin(w io.Writer, name string, m bench.Margin) {
	if m.Status == bench.Unmeasured {
		fmt.Fprintf(w, "  %s %s: unmeasured (%s)\n", name, m.Metric, m.Detail)
		return
	}
	fmt.Fprintf(w, "  %s %s vs reference: %+.3f [%+.3f, %+.3f] over %d twin pairs — %s\n", name, m.Metric, m.Diff, m.Lower, m.Upper, m.Pairs, m.Status)
}

// orDash is s, or a dash when s is empty.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func fmtPtr(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *v)
}

func fmtSigned(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%+.2f", *v)
}
