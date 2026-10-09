package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/Chubby-Honey-Bee/hive/internal/gate"
	"github.com/spf13/cobra"
)

func newGuardCmd() *cobra.Command {
	var o guardOptions

	cmd := &cobra.Command{
		Use:   "guard",
		Short: "One-command wave guard pipeline",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGuard(cmd.InOrStdin(), &o)
		},
	}
	cmd.Flags().IntVar(&o.wave, "wave", 0, "wave number to gate")
	cmd.Flags().StringVar(&o.evalStr, "eval", "", "evaluation scores as JSON")
	cmd.Flags().BoolVar(&o.evalStdin, "eval-stdin", false,
		"read the evaluation from stdin as {coverage, depth, sources, actionability, gaps}; chb derives the verdict (COMPLETE iff no gaps and every score is at least 4) and records the gaps as important gaps")
	cmd.Flags().BoolVar(&o.asJSON, "json", false,
		"print one JSON object: wave, opened, errors, warnings, and the verdict when an evaluation was given; the exit status still says whether the gate opened")
	cmd.Flags().BoolVar(&o.autoResolveNumeric, "auto-resolve-numeric", false, "auto-resolve numeric conflicts")
	cmd.Flags().BoolVar(&o.force, "force", false, "open gate despite warnings")
	cmd.Flags().BoolVar(&o.requireOutcomeReview, "require-outcome-review", false,
		"block on a guarantee in a domain whose calibrated guarantee hit rate is under 0.95 until a human or external outcome resolves it (off by default; --force does not waive it)")
	cmd.MarkFlagRequired("wave")
	return cmd
}

// guardOptions holds chb guard's flag values.
type guardOptions struct {
	wave                 int
	evalStr              string
	evalStdin            bool
	asJSON               bool
	autoResolveNumeric   bool
	force                bool
	requireOutcomeReview bool
}

// guardEvaluation is the evaluation the gate reads, nil when none was
// given, and how many gaps --eval-stdin recorded.
type guardEvaluation struct {
	scores      map[string]any
	gapsWritten int
}

// runGuard runs the wave's gate pipeline on the evaluation given and
// reports the outcome; a blocked gate is an error.
func runGuard(stdin io.Reader, o *guardOptions) error {
	ev, err := o.evaluation(stdin)
	if err != nil {
		return err
	}
	result, err := o.runPipeline(ev.scores)
	if err != nil {
		return err
	}
	if o.asJSON {
		return o.reportJSON(result, ev)
	}
	return o.reportText(result)
}

// evaluation reads the evaluation from --eval or, with --eval-stdin, from
// stdin, refusing both at once.
func (o *guardOptions) evaluation(stdin io.Reader) (guardEvaluation, error) {
	if o.evalStdin && o.evalStr != "" {
		return guardEvaluation{}, fmt.Errorf("--eval and --eval-stdin are exclusive")
	}
	if o.evalStdin {
		return readGuardEvalStdin(stdin, o.wave)
	}
	return parseGuardEval(o.evalStr)
}

// parseGuardEval parses --eval's scores; none when it is empty.
func parseGuardEval(evalStr string) (guardEvaluation, error) {
	var ev guardEvaluation
	if evalStr == "" {
		return ev, nil
	}
	if err := json.Unmarshal([]byte(evalStr), &ev.scores); err != nil {
		return ev, fmt.Errorf("parse eval JSON: %w", err)
	}
	return ev, nil
}

// readGuardEvalStdin derives the evaluation from the one on stdin and
// records its gaps before the pipeline, so a pipeline error cannot lose
// them.
func readGuardEvalStdin(stdin io.Reader, wave int) (guardEvaluation, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return guardEvaluation{}, fmt.Errorf("read evaluation from stdin: %w", err)
	}
	scores, gaps, err := gate.DeriveEvaluation(raw)
	if err != nil {
		return guardEvaluation{}, err
	}
	written, err := recordEvaluatorGaps(wave, gaps)
	return guardEvaluation{scores: scores, gapsWritten: written}, err
}

// recordEvaluatorGaps records each gap as an important evaluator gap of the
// wave and returns how many it wrote. A gap the wave already holds open,
// word for word, is not written again: an evaluator that repeats itself
// each pass would otherwise pile up copies and read as progress.
func recordEvaluatorGaps(wave int, gaps []string) (int, error) {
	written := 0
	for _, g := range gaps {
		wrote, err := recordEvaluatorGap(wave, g)
		if err != nil {
			return written, err
		}
		if wrote {
			written++
		}
	}
	return written, nil
}

// recordEvaluatorGap records one gap unless the wave holds it open already,
// and reports whether it wrote it.
func recordEvaluatorGap(wave int, g string) (bool, error) {
	var open int
	if err := store.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM gaps WHERE wave=? AND description=? AND resolved_by_wave IS NULL`, wave, g,
	).Scan(&open); err != nil {
		return false, fmt.Errorf("read open gaps: %w", err)
	}
	if open > 0 {
		return false, nil
	}
	if err := store.Gaps().AddGap(wave, "evaluator", g, "important", nil, nil, nil, nil); err != nil {
		return false, err
	}
	return true, nil
}

// runPipeline runs the gate pipeline, under a heading unless --json.
func (o *guardOptions) runPipeline(scores map[string]any) (*gate.GateResult, error) {
	if !o.asJSON {
		fmt.Printf("=== Wave %d Gate Pipeline ===\n\n", o.wave)
	}
	return gate.RunGatePipeline(store, o.wave, scores, o.autoResolveNumeric, o.force, o.requireOutcomeReview)
}

// reportJSON prints the outcome as one JSON object; a blocked gate is still
// an error.
func (o *guardOptions) reportJSON(result *gate.GateResult, ev guardEvaluation) error {
	out := map[string]any{
		"wave":     o.wave,
		"opened":   result.Opened,
		"errors":   nonNilStrings(result.Errors),
		"warnings": nonNilStrings(result.Warnings),
	}
	if ev.scores != nil {
		out["verdict"] = ev.scores["verdict"]
	}
	if o.evalStdin {
		out["gaps_written"] = ev.gapsWritten
	}
	if err := printJSON(out); err != nil {
		return err
	}
	return guardOutcome(o.wave, result.Opened)
}

// guardOutcome is nil for a gate that opened and the blocked error for one
// that did not.
func guardOutcome(wave int, opened bool) error {
	if opened {
		return nil
	}
	return fmt.Errorf("wave %d gate BLOCKED", wave)
}

// reportText prints the pipeline's errors and warnings and whether the gate
// opened; a blocked gate is an error.
func (o *guardOptions) reportText(result *gate.GateResult) error {
	printGuardList("Gate BLOCKED. Fix these errors:", result.Errors)
	printGuardList("Warnings:", result.Warnings)
	if result.Opened {
		fmt.Printf("\nWave %d gate OPENED.\n", o.wave)
		return nil
	}
	if guardOnlyWarnings(result) && !o.force {
		fmt.Println("\nUse --force to open gate with warnings.")
	}
	// Scripts and the MCP tool key off the exit status, so a blocked gate
	// exits non-zero and synthesis does not proceed past laundering.
	return fmt.Errorf("wave %d gate BLOCKED", o.wave)
}

// printGuardList prints a titled list, nothing when it is empty.
func printGuardList(title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Println(title)
	for _, it := range items {
		fmt.Printf("  - %s\n", it)
	}
}

// guardOnlyWarnings reports whether the pipeline raised warnings and no
// error.
func guardOnlyWarnings(result *gate.GateResult) bool {
	return len(result.Errors) == 0 && len(result.Warnings) > 0
}

// nonNilStrings returns s, or an empty list for nil, so JSON output carries
// [] rather than null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
