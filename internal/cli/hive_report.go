package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/hive"
	"github.com/spf13/cobra"
)

// reportFinding is one finding as `chb hive report` lists it.
type reportFinding struct {
	ID               int64   `json:"id"`
	Wave             int     `json:"wave"`
	Agent            string  `json:"agent"`
	MSSLabel         string  `json:"mss_label"`
	ConvergenceLevel *string `json:"convergence_level"`
	Finding          string  `json:"finding"`
	Evidence         *string `json:"evidence"`
	SourceURLs       *string `json:"source_urls"`
	DependsOnIDs     *string `json:"depends_on_ids"`
}

func newHiveReportCmd() *cobra.Command {
	var maxIterations, since, limit, wave int
	cmd := &cobra.Command{
		Use:   "report --project <name>",
		Short: "Print what a final synthesis reads, as one JSON object",
		Long: `Prints the hive's state for a final synthesis as one JSON object:
project, phase, iteration, is_terminal, stop_reason, the db-read summary,
the findings (the --limit most converged, newest first, with
findings_total counting all of them), and the open gaps and conflicts.

stop_reason is the terminal reason when the hive is terminal. Otherwise,
with --max-iterations, it is what the loop rule says stops the loop (the cap
or a stall), and empty when the rule would run another pass.

With --since-iteration S it also prints passes_this_run: the passes counted
since the count was S, as a run that began there has run.

With --wave W the findings and findings_total are that wave's, and it also
prints wave and wave_sources: the wave's sources, each with the verdict
validate-sources recorded (unchecked when it has not checked one). The hive's
gate reads this before it is judged.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, _ := cmd.Flags().GetString("project")
			if err := checkHiveIterationFlag(cmd, "max-iterations", maxIterations); err != nil {
				return err
			}
			if limit < 1 {
				return fmt.Errorf("--limit must be at least 1, got %d", limit)
			}
			return runHiveReport(hiveReportOptions{
				project: project, maxIterations: maxIterations, since: since, limit: limit, wave: wave,
				bySince: cmd.Flags().Changed("since-iteration"), byWave: cmd.Flags().Changed("wave"),
			})
		},
	}
	cmd.Flags().IntVar(&maxIterations, "max-iterations", 0,
		"the hive's iteration cap, for stop_reason")
	cmd.Flags().IntVar(&since, "since-iteration", 0,
		"the count when this run began; prints passes_this_run")
	cmd.Flags().IntVar(&limit, "limit", 100,
		"the most findings to list")
	cmd.Flags().IntVar(&wave, "wave", 0,
		"list only this wave's findings, and its sources with their validation")
	return cmd
}

// hiveReportOptions holds chb hive report's flag values and which of the
// optional ones were given.
type hiveReportOptions struct {
	project       string
	maxIterations int
	since         int
	limit         int
	wave          int
	bySince       bool
	byWave        bool
}

// runHiveReport scans the project and prints what a final synthesis reads.
func runHiveReport(o hiveReportOptions) error {
	r, err := hiveStore(o.project)
	if err != nil {
		return err
	}
	state, err := hive.ScanState(store, o.project)
	if err != nil {
		return err
	}
	out, err := o.report(r, state)
	if err != nil {
		return err
	}
	return printJSON(out)
}

// report is the report's JSON object.
func (o hiveReportOptions) report(r *hive.Resolved, state *hive.State) (map[string]any, error) {
	isTerminal, reason := hive.CheckTermination(state)
	stop, err := o.stopReason(state, isTerminal, reason)
	if err != nil {
		return nil, err
	}
	summary, err := store.GetSummary()
	if err != nil {
		return nil, err
	}
	findings, total, err := o.listFindings(state.TotalFindings)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"project":        o.project,
		"db":             r.Path,
		"phase":          state.Hive.Phase,
		"iteration":      state.Hive.Iteration,
		"is_terminal":    isTerminal,
		"stop_reason":    stop,
		"summary":        summary,
		"findings_total": total,
		"findings":       findings,
		"open_gaps":      nonNilReportRows(state.UnresolvedGaps),
		"open_conflicts": nonNilReportRows(state.UnresolvedConflicts),
	}
	return out, o.addRunAndWave(out, state)
}

// stopReason is the terminal reason when the hive is terminal; otherwise,
// with --max-iterations, what the loop rule says stops the loop, empty when
// it would run another pass.
func (o hiveReportOptions) stopReason(state *hive.State, isTerminal bool, reason string) (string, error) {
	switch {
	case isTerminal:
		return reason, nil
	case o.maxIterations > 0:
		_, stop, err := hive.LoopDecision(store, o.project, state.Hive.Iteration, o.maxIterations)
		return stop, err
	}
	return "", nil
}

// listFindings lists the --limit most converged findings, newest first, of
// --wave when given, and counts all of them.
func (o hiveReportOptions) listFindings(totalFindings int) ([]reportFinding, int, error) {
	where, qargs, total := "", []any{o.limit}, totalFindings
	if o.byWave {
		where, qargs = "WHERE wave = ?", []any{o.wave, o.limit}
		if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM findings WHERE wave = ?`, o.wave).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("read findings: %w", err)
		}
	}
	findings, err := queryReportFindings(where, qargs)
	return findings, total, err
}

// queryReportFindings runs the findings query under the WHERE clause.
func queryReportFindings(where string, qargs []any) ([]reportFinding, error) {
	rows, err := store.ReadDB.Query(
		`SELECT id, wave, agent, mss_label, convergence_level, finding, evidence, source_urls, depends_on_ids
		 FROM findings `+where+` ORDER BY convergence_count DESC, id DESC LIMIT ?`, qargs...)
	if err != nil {
		return nil, fmt.Errorf("read findings: %w", err)
	}
	defer rows.Close()
	findings, err := scanReportFindings(rows)
	if err != nil {
		return nil, fmt.Errorf("read findings: %w", err)
	}
	return findings, nil
}

// scanReportFindings reads every row of the findings query.
func scanReportFindings(rows *sql.Rows) ([]reportFinding, error) {
	findings := []reportFinding{}
	for rows.Next() {
		var f reportFinding
		if err := rows.Scan(&f.ID, &f.Wave, &f.Agent, &f.MSSLabel, &f.ConvergenceLevel,
			&f.Finding, &f.Evidence, &f.SourceURLs, &f.DependsOnIDs); err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	return findings, rows.Err()
}

// nonNilReportRows returns rows, or an empty list for nil, so JSON output
// carries [] rather than null.
func nonNilReportRows(rows []map[string]any) []map[string]any {
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}

// addRunAndWave adds passes_this_run under --since-iteration, and the wave
// and its sources under --wave.
func (o hiveReportOptions) addRunAndWave(out map[string]any, state *hive.State) error {
	if o.bySince {
		out["passes_this_run"] = state.Hive.Iteration - o.since
	}
	if !o.byWave {
		return nil
	}
	sources, err := waveSources(o.wave)
	if err != nil {
		return err
	}
	out["wave"] = o.wave
	out["wave_sources"] = sources
	return nil
}

// waveSource is one of a wave's sources as `chb hive report --wave` lists it.
type waveSource struct {
	URL              string  `json:"url"`
	ValidationStatus *string `json:"validation_status"`
	HTTPStatus       *int64  `json:"http_status"`
}

// waveSources lists the sources recorded at wave, in id order.
func waveSources(wave int) ([]waveSource, error) {
	rows, err := store.ReadDB.Query(
		`SELECT url, validation_status, http_status FROM sources WHERE wave = ? ORDER BY id`, wave)
	if err != nil {
		return nil, fmt.Errorf("read sources: %w", err)
	}
	defer rows.Close()
	out := []waveSource{}
	for rows.Next() {
		var s waveSource
		if err := rows.Scan(&s.URL, &s.ValidationStatus, &s.HTTPStatus); err != nil {
			return nil, fmt.Errorf("read sources: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func newHiveWriteSynthesisCmd() *cobra.Command {
	var out, openQuestions string
	cmd := &cobra.Command{
		Use:   "write-synthesis --project <name> --out <path>",
		Short: "Write a final synthesis read from stdin to a file",
		Long: `Reads a final synthesis as Markdown from stdin and writes it to --out,
creating its directory. --open-questions takes a JSON list of strings,
written after the synthesis under "## Open questions". Empty input is
refused, so a synthesis that never arrived writes no file. Prints one JSON
object: path and bytes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return writeHiveSynthesis(cmd.InOrStdin(), out, openQuestions)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "the file to write (required)")
	cmd.Flags().StringVar(&openQuestions, "open-questions", "", "a JSON list of open questions to append")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

// writeHiveSynthesis writes the synthesis read from stdin, with the open
// questions after it, to out and prints its path and size.
func writeHiveSynthesis(stdin io.Reader, out, openQuestions string) error {
	text, questions, err := readHiveSynthesis(stdin, openQuestions)
	if err != nil {
		return err
	}
	doc := renderHiveSynthesis(text, questions)
	if err := writeHiveSynthesisFile(out, doc); err != nil {
		return err
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		abs = out
	}
	return printJSON(map[string]any{"path": abs, "bytes": len(doc)})
}

// readHiveSynthesis reads the synthesis from stdin, refusing empty input,
// and parses --open-questions.
func readHiveSynthesis(stdin io.Reader, openQuestions string) (string, []string, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return "", nil, fmt.Errorf("read the synthesis from stdin: %w", err)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", nil, fmt.Errorf("the synthesis on stdin is empty")
	}
	questions, err := parseSynthesisOpenQuestions(openQuestions)
	return text, questions, err
}

// parseSynthesisOpenQuestions parses --open-questions, a JSON list of
// strings; none when it is empty.
func parseSynthesisOpenQuestions(openQuestions string) ([]string, error) {
	if openQuestions == "" {
		return nil, nil
	}
	var questions []string
	if err := json.Unmarshal([]byte(openQuestions), &questions); err != nil {
		return nil, fmt.Errorf("--open-questions must be a JSON list of strings: %w", err)
	}
	return questions, nil
}

// renderHiveSynthesis is the synthesis followed, when there are any, by the
// open questions under "## Open questions".
func renderHiveSynthesis(text string, questions []string) string {
	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n")
	if len(questions) > 0 {
		b.WriteString("\n## Open questions\n\n")
		for _, q := range questions {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(q))
		}
	}
	return b.String()
}

// writeHiveSynthesisFile writes the document to out, creating its
// directory.
func writeHiveSynthesisFile(out, doc string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(out), err)
	}
	return os.WriteFile(out, []byte(doc), 0o644)
}
