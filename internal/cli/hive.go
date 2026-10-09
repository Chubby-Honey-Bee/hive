package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"github.com/spf13/cobra"
)

// hiveLookup is what the CLI knows when a hive command names a project: the
// database PersistentPreRunE opened, and whether --db named it.
func hiveLookup(project string) hive.Lookup {
	return hive.Lookup{Project: project, Named: store, NamedPath: store.Path, Pinned: dbPinned}
}

// hiveStore points the command at the database hive.Resolve chooses for
// project (hive.md § One hive per database) and returns the choice.
func hiveStore(project string) (*hive.Resolved, error) {
	return resolveHiveStore(hiveLookup(project))
}

// hiveLoopStore is hiveStore for verb, one that moves the hive's loop: next,
// complete or reset. It refuses the database of the run whose model drives
// this process (runner.AgentDBEnv): the hive workflow's command nodes run
// these once a pass, and one run from a model's shell would move the count
// under the pass. A database the model's run does not use, such as a
// replay's scratch one, is not refused.
func hiveLoopStore(project, verb string) (*hive.Resolved, error) {
	r, err := hiveStore(project)
	if err != nil {
		return nil, err
	}
	if run := os.Getenv(runner.AgentDBEnv); run != "" && sameFile(r.Path, run) {
		return nil, fmt.Errorf("chb hive %s moves the loop of the run whose model is running it (%s names that run's database); the hive workflow runs it itself, once a pass", verb, runner.AgentDBEnv)
	}
	return r, nil
}

// sameFile reports whether a and b name one file: os.SameFile when both
// exist, else the same absolute path.
func sameFile(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(ai, bi)
	}
	absA, _ := filepath.Abs(a)
	absB, _ := filepath.Abs(b)
	return absA == absB
}

// resolveHiveStore resolves the lookup's database and makes it the command's.
func resolveHiveStore(l hive.Lookup) (*hive.Resolved, error) {
	r, err := hive.Resolve(l)
	if err != nil {
		return nil, err
	}
	useResolved(r)
	return r, nil
}

// useResolved makes r's database the command's. The one PreRun opened is
// closed when r left it; PersistentPostRun closes whichever store is current.
func useResolved(r *hive.Resolved) {
	if r.Store != store {
		store.Close()
		store = r.Store
		dbPath = r.Path
	}
}

// databaseLine is the text report's line naming the database a hive command
// used, and why when it is not the one the caller named.
func databaseLine(r *hive.Resolved) string {
	if r.HostedBy == "" {
		return fmt.Sprintf("Database: %s", r.Path)
	}
	return fmt.Sprintf("Database: %s (%s hosts hive project '%s')", r.Path, r.Named, r.HostedBy)
}

// resolveHiveRun points `chb agent-run` at the database the hive rule
// chooses for the run's `project` input, when the workflow drives the hive:
// one of its command nodes runs `chb hive`. The whole run then uses it, so
// the dispatch agent's `chb db-write`, the gate's `chb guard` and the run's
// own rows land where the hive reads. The workspace database is created
// here, as `hive init` creates it, since the workflow's first node is `hive
// init`. It returns the database the run uses, or nil when the workflow does
// not drive a hive.
func resolveHiveRun(wfPath string, inputs map[string]any) (*hive.Resolved, error) {
	project, err := hiveRunProject(wfPath, inputs)
	if err != nil || project == "" {
		return nil, err
	}
	l := hiveLookup(project)
	l.CreateDB = true
	return resolveHiveStore(l)
}

// hiveRunProject is the run's project input when the workflow drives the
// hive, and "" when it names no project or does not drive a hive.
func hiveRunProject(wfPath string, inputs map[string]any) (string, error) {
	project, _ := inputs["project"].(string)
	if project == "" {
		return "", nil
	}
	defn, err := workflow.LoadYAML(wfPath)
	if err != nil {
		return "", err
	}
	if !drivesHive(defn) {
		return "", nil
	}
	return project, nil
}

// drivesHive reports whether a workflow definition has a command node that
// runs `chb hive`.
func drivesHive(defn map[string]any) bool {
	nodes, _ := defn["nodes"].(map[string]any)
	for _, raw := range nodes {
		if runsChbHive(raw) {
			return true
		}
	}
	return false
}

// runsChbHive reports whether a workflow node is a command node whose argv
// runs `chb hive`.
func runsChbHive(raw any) bool {
	node, _ := raw.(map[string]any)
	if node["type"] != "command" {
		return false
	}
	argv, _ := node["argv"].([]any)
	return len(argv) >= 2 && argv[0] == "chb" && argv[1] == "hive"
}

func newHiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hive",
		Short: "Bee colony-inspired autonomous orchestration",
	}

	cmd.PersistentFlags().String("project", "", "project name (required)")
	cmd.MarkPersistentFlagRequired("project")

	cmd.AddCommand(
		newHiveInitCmd(),
		newHiveNextCmd(),
		newHiveCompleteCmd(),
		newHiveStatusCmd(),
		newHiveSignalsCmd(),
		newHiveResetCmd(),
		newHiveReportCmd(),
		newHiveWriteSynthesisCmd(),
	)

	return cmd
}

func newHiveInitCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "init --project <name>",
		Short: "Initialize hive state for a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			project, _ := cmd.Flags().GetString("project")
			r, created, err := hive.Init(hiveLookup(project))
			if err != nil {
				return err
			}
			useResolved(r)
			if asJSON {
				return printHiveInitJSON(project, created, r)
			}
			printHiveInit(project, created, r)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"print one JSON object: project, created, phase, iteration, db")
	return cmd
}

// printHiveInitJSON prints the initialised hive's state as one JSON object.
func printHiveInitJSON(project string, created bool, r *hive.Resolved) error {
	var phase string
	var iteration int
	if err := store.ReadDB.QueryRow(
		"SELECT phase, iteration FROM hive_state WHERE project=?", project,
	).Scan(&phase, &iteration); err != nil {
		return fmt.Errorf("read hive state for %q: %w", project, err)
	}
	return printJSON(map[string]any{
		"project": project, "created": created, "phase": phase, "iteration": iteration, "db": r.Path,
	})
}

// printHiveInit says whether the hive was initialised or already was, and
// names its database.
func printHiveInit(project string, created bool, r *hive.Resolved) {
	if !created {
		fmt.Printf("Hive already initialized for project '%s'\n", project)
	} else {
		fmt.Printf("Hive initialized for project '%s'\n", project)
	}
	fmt.Println(databaseLine(r))
}

// printJSON writes v to stdout as one indented JSON object, the shape a
// workflow command node reads with outputs_from: stdout_json.
func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// checkHiveIterationFlag refuses an iteration flag given with a value below
// 1.
func checkHiveIterationFlag(cmd *cobra.Command, name string, v int) error {
	if cmd.Flags().Changed(name) && v < 1 {
		return fmt.Errorf("--%s must be at least 1, got %d", name, v)
	}
	return nil
}

func newHiveCompleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "complete --project <name> --action <id>",
		Short: "Mark an action as complete",
		Long: `Ends the project's current pass in one transaction: it consumes the signals
the pass's plan read, applies any gain-control params in --outputs, returns
the phase to scanning, and records the research state at the pass's end.

With --expect-iteration N it first refuses, writing nothing, a hive whose
iteration is not N, the count the pass's scan recorded: something else ran
chb hive next during the pass.

With --max-iterations N and --json it prints the loop rule, computed rather
than judged: should_continue is true while the hive has run fewer than N
passes and the last two passes did not both leave the research state as
they found it. stop_reason says what stopped it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := hiveCompleteFlags(cmd)
			if err != nil {
				return err
			}
			return runHiveComplete(o)
		},
	}
	cmd.Flags().String("action", "", "action ID")
	cmd.Flags().String("outputs", "{}", "JSON outputs")
	cmd.Flags().Bool("json", false,
		"print one JSON object: project, action, phase, iteration, signals_consumed, db, what the pass changed (findings_added, gaps_resolved, changed), and with --max-iterations should_continue and stop_reason")
	cmd.Flags().Int("max-iterations", 0,
		"the hive's iteration cap; with --json, should_continue is true while hive_state.iteration is below it and the hive has not stalled")
	cmd.Flags().Int("expect-iteration", 0,
		"refuse, writing nothing, unless hive_state.iteration is this: the count the pass's scan recorded")
	cmd.MarkFlagRequired("action")
	return cmd
}

// hiveCompleteOptions holds chb hive complete's flag values.
type hiveCompleteOptions struct {
	project       string
	action        string
	outputs       string
	asJSON        bool
	maxIterations int
	expect        int
}

// hiveCompleteFlags reads chb hive complete's flags, refusing an iteration
// flag below 1.
func hiveCompleteFlags(cmd *cobra.Command) (hiveCompleteOptions, error) {
	var o hiveCompleteOptions
	o.project, _ = cmd.Flags().GetString("project")
	o.action, _ = cmd.Flags().GetString("action")
	o.outputs, _ = cmd.Flags().GetString("outputs")
	o.asJSON, _ = cmd.Flags().GetBool("json")
	o.maxIterations, _ = cmd.Flags().GetInt("max-iterations")
	o.expect, _ = cmd.Flags().GetInt("expect-iteration")
	if err := checkHiveIterationFlag(cmd, "max-iterations", o.maxIterations); err != nil {
		return o, err
	}
	return o, checkHiveIterationFlag(cmd, "expect-iteration", o.expect)
}

// runHiveComplete ends the project's current pass and reports it.
func runHiveComplete(o hiveCompleteOptions) error {
	params, err := hiveCompletionParams(o.outputs)
	if err != nil {
		return err
	}
	r, err := hiveLoopStore(o.project, "complete")
	if err != nil {
		return err
	}

	// Consumes the signals the plan read — not every pending one.
	done, err := hive.CompleteIteration(store, hive.Completion{
		Project: o.project, Params: params, ExpectIteration: o.expect,
	})
	if err != nil {
		return err
	}
	return o.report(r, done)
}

// hiveCompletionParams reads the gain-control params in --outputs before
// anything changes, refusing outputs that do not parse and params out of
// range.
func hiveCompletionParams(outputsStr string) (map[string]any, error) {
	var params map[string]any
	if outputsStr != "" {
		var outputs map[string]any
		if err := json.Unmarshal([]byte(outputsStr), &outputs); err != nil {
			return nil, fmt.Errorf("parse --outputs: %w", err)
		}
		params, _ = outputs["params"].(map[string]any)
	}
	if err := hive.CheckGainParams(params); err != nil {
		return nil, fmt.Errorf("--outputs params: %w", err)
	}
	return params, nil
}

// report prints the completed pass as text, or as one JSON object under
// --json.
func (o hiveCompleteOptions) report(r *hive.Resolved, done hive.CompletionResult) error {
	if o.asJSON {
		return o.printCompletionJSON(r, done)
	}
	fmt.Printf("Action %s completed for project '%s'\n", o.action, o.project)
	fmt.Println(databaseLine(r))
	return nil
}

// printCompletionJSON prints the completed pass, what it changed and, under
// --max-iterations, the loop rule's decision.
func (o hiveCompleteOptions) printCompletionJSON(r *hive.Resolved, done hive.CompletionResult) error {
	out := map[string]any{
		"project":          o.project,
		"action":           o.action,
		"phase":            "scanning",
		"iteration":        done.Iteration,
		"signals_consumed": done.SignalsConsumed,
		"db":               r.Path,
	}
	// What the pass changed, read from the database, beside whatever
	// the pass's agent reported. A pass no scan recorded has no start.
	if done.Start != nil {
		addHivePassChange(out, done)
	}
	if o.maxIterations > 0 {
		if err := addHiveLoopDecision(out, o.project, done.Iteration, o.maxIterations); err != nil {
			return err
		}
	}
	return printJSON(out)
}

// addHivePassChange sets how many findings the pass added and gaps it
// resolved, and whether it changed the research state.
func addHivePassChange(out map[string]any, done hive.CompletionResult) {
	resolved := func(p *hive.Progress) int64 { return p.Gaps - p.OpenGaps }
	out["findings_added"] = done.End.Findings - done.Start.Findings
	out["gaps_resolved"] = resolved(done.End) - resolved(done.Start)
	out["changed"] = *done.Start != *done.End
}

// addHiveLoopDecision sets the loop rule's decision: whether to run another
// pass, and what stops the loop.
func addHiveLoopDecision(out map[string]any, project string, iteration, maxIterations int) error {
	more, why, err := hive.LoopDecision(store, project, iteration, maxIterations)
	if err != nil {
		return err
	}
	out["max_iterations"] = maxIterations
	out["should_continue"] = more
	out["stop_reason"] = why
	return nil
}

func newHiveStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status --project <name>",
		Short: "Show hive status",
		RunE: func(cmd *cobra.Command, args []string) error {
			project, _ := cmd.Flags().GetString("project")
			showJSON, _ := cmd.Flags().GetBool("json")
			return runHiveStatus(project, showJSON)
		},
	}
	cmd.Flags().Bool("json", false, "print one JSON object instead of the status report: its figures, db, is_terminal, reason, and the raw scanned state")
	return cmd
}

// runHiveStatus scans the project and prints its status.
func runHiveStatus(project string, showJSON bool) error {
	r, err := hiveStore(project)
	if err != nil {
		return err
	}

	state, err := hive.ScanState(store, project)
	if err != nil {
		return err
	}
	isTerminal, reason := hive.CheckTermination(state)

	// JSON only, so a workflow command node can read stdout as one object.
	if showJSON {
		return printJSON(hiveStatusJSON(project, r, state, isTerminal, reason))
	}
	printHiveStatus(project, r, state, isTerminal, reason)
	return nil
}

// hiveStatusJSON is the status as one JSON object: its figures, db,
// is_terminal, reason, and the raw scanned state.
func hiveStatusJSON(project string, r *hive.Resolved, state *hive.State, isTerminal bool, reason string) map[string]any {
	return map[string]any{
		"project":                 project,
		"db":                      r.Path,
		"phase":                   state.Hive.Phase,
		"iteration":               state.Hive.Iteration,
		"wave":                    state.LatestWave,
		"total_findings":          state.TotalFindings,
		"labels":                  state.Labels,
		"mss_integrity":           state.MSSIntegrity,
		"unresolved_gaps":         len(state.UnresolvedGaps),
		"critical_gaps":           len(state.CriticalGaps),
		"unresolved_conflicts":    len(state.UnresolvedConflicts),
		"gates_opened":            len(state.WaveGates),
		"pending_signals":         len(state.PendingSignals),
		"running_agents":          len(state.RunningAgents),
		"batch_size":              state.Hive.BatchSize,
		"convergence_threshold":   state.Hive.ConvergenceThreshold,
		"model_tier":              state.Hive.ModelTier,
		"conflict_rate_threshold": state.Hive.ConflictRateThreshold,
		"is_terminal":             isTerminal,
		"reason":                  reason,
		"state":                   state,
	}
}

// printHiveStatus prints the status report.
func printHiveStatus(project string, r *hive.Resolved, state *hive.State, isTerminal bool, reason string) {
	fmt.Printf("=== Hive Status: %s ===\n", project)
	fmt.Printf("Database:   %s\n", r.Path)
	fmt.Printf("Phase:      %s\n", state.Hive.Phase)
	fmt.Printf("Iteration:  %d\n", state.Hive.Iteration)
	fmt.Printf("Wave:       %d\n", state.LatestWave)
	fmt.Printf("Findings:   %d\n", state.TotalFindings)
	fmt.Printf("Labels:     %s\n", formatLabelCounts(state.Labels))
	fmt.Printf("MSS:        %s\n", state.MSSIntegrity)
	fmt.Printf("Gaps:       %d (%d critical)\n", len(state.UnresolvedGaps), len(state.CriticalGaps))
	fmt.Printf("Conflicts:  %d\n", len(state.UnresolvedConflicts))
	fmt.Printf("Gates:      %d opened\n", len(state.WaveGates))
	fmt.Printf("Signals:    %d pending\n", len(state.PendingSignals))
	fmt.Printf("Running:    %d agents\n", len(state.RunningAgents))
	fmt.Printf("--- Gain Control ---\n")
	fmt.Printf("Batch size: %d\n", state.Hive.BatchSize)
	fmt.Printf("Convergence threshold: %d\n", state.Hive.ConvergenceThreshold)
	fmt.Printf("Model tier: %s\n", state.Hive.ModelTier)
	fmt.Printf("Conflict rate threshold: %.2f\n", state.Hive.ConflictRateThreshold)

	if isTerminal {
		fmt.Printf("\nTERMINAL: %s\n", reason)
	} else {
		fmt.Printf("\nContinuing: %s\n", reason)
	}
}

func newHiveSignalsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "signals --project <name>",
		Short: "List recent signals",
		RunE: func(cmd *cobra.Command, args []string) error {
			project, _ := cmd.Flags().GetString("project")
			sigType, _ := cmd.Flags().GetString("type")
			pending, _ := cmd.Flags().GetBool("pending")
			return runHiveSignals(project, sigType, pending)
		},
	}
	cmd.Flags().String("type", "", "filter by signal type")
	cmd.Flags().Bool("pending", false, "only show pending signals")
	return cmd
}

// runHiveSignals lists the 50 most recent signals, of one type and only the
// pending ones when asked.
func runHiveSignals(project, sigType string, pending bool) error {
	r, err := hiveStore(project)
	if err != nil {
		return err
	}
	fmt.Println(databaseLine(r))

	query, params := hiveSignalsQuery(sigType, pending)
	rows, err := store.ReadDB.Query(query, params...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return printHiveSignalRows(rows)
}

// hiveSignalsQuery selects the 50 most recent signals under the filters,
// newest first; of signals written in one second, the higher id first.
func hiveSignalsQuery(sigType string, pending bool) (string, []any) {
	// Named, not SELECT * — the scan below is positional, so a column
	// added to the middle of the table would silently misread every row.
	query := `SELECT id, signal_type, source_type, source_id,
				target_d1, target_d2, target_d3, target_d4,
				payload_json, acted_on, wave, created_at FROM signals`
	var params []any
	var conditions []string
	if sigType != "" {
		conditions = append(conditions, "signal_type = ?")
		params = append(params, sigType)
	}
	if pending {
		conditions = append(conditions, "acted_on = 0")
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	return query + " ORDER BY created_at DESC, id DESC LIMIT 50", params
}

// printHiveSignalRows prints each signal row, refusing a listing that did
// not complete.
func printHiveSignalRows(rows *sql.Rows) error {
	for rows.Next() {
		if err := printHiveSignalRow(rows); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("signal listing did not complete: %w", err)
	}
	return nil
}

// printHiveSignalRow prints one signal: its status, type, wave and
// coordinates, then its payload when it has one.
func printHiveSignalRow(rows *sql.Rows) error {
	var id int64
	var sigType, createdAt string
	var sourceType, payloadJSON *string
	var sourceID *int64
	var d1, d2, d3, d4, wave *int
	var actedOn int

	if err := rows.Scan(&id, &sigType, &sourceType, &sourceID, &d1, &d2, &d3, &d4,
		&payloadJSON, &actedOn, &wave, &createdAt); err != nil {
		return fmt.Errorf("read signal row: %w", err)
	}

	status := "PENDING"
	if actedOn != 0 {
		status = "ACTED"
	}
	// %v on a *int prints the pointer, so each coordinate goes through
	// optIntStr.
	fmt.Printf("[%s] %-16s wave=%s coords=(d1=%s,d2=%s,d3=%s,d4=%s) %s\n",
		status, sigType, optIntStr(wave),
		optIntStr(d1), optIntStr(d2), optIntStr(d3), optIntStr(d4), createdAt)
	printHiveSignalPayload(payloadJSON)
	return nil
}

// printHiveSignalPayload prints the first 100 bytes of a payload other than
// the empty object.
func printHiveSignalPayload(payloadJSON *string) {
	if payloadJSON == nil || *payloadJSON == "{}" {
		return
	}
	fmt.Printf("         payload: %s\n", clip(*payloadJSON, 100))
}

func newHiveResetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reset --project <name>",
		Short: "Reset hive state (keeps data, resets iteration and phase)",
		RunE: func(cmd *cobra.Command, args []string) error {
			project, _ := cmd.Flags().GetString("project")
			return runHiveReset(project)
		},
	}
	return cmd
}

// runHiveReset resets the project's iteration and phase and discards what
// its passes left behind.
func runHiveReset(project string) error {
	r, err := hiveLoopStore(project, "reset")
	if err != nil {
		return err
	}
	retired, err := resetHive(project)
	if err != nil {
		return err
	}
	fmt.Printf("Hive reset for project %q: iteration=0 phase=scanning, %d pending signal(s) retired.\n",
		project, retired)
	fmt.Println(databaseLine(r))
	return nil
}

// resetHive writes the reset in one transaction, so a reset that fails
// partway leaves the hive as it was, and returns how many pending signals it
// retired.
func resetHive(project string) (int64, error) {
	tx, err := store.BeginImmediate(context.Background())
	if err != nil {
		return 0, fmt.Errorf("reset hive: %w", err)
	}
	defer tx.Rollback()
	retired, err := resetHiveIn(tx, project)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("reset hive: %w", err)
	}
	return retired, nil
}

// resetHiveIn writes the reset inside tx and returns how many pending
// signals it retired.
func resetHiveIn(tx *db.Tx, project string) (int64, error) {
	if err := resetHiveState(tx, project); err != nil {
		return 0, err
	}
	return discardHivePasses(tx, project)
}

// resetHiveState sets the project's iteration to 0 and its phase to
// scanning, and refuses a project with no hive state, so the most
// destructive command the hive has never reports success for a project that
// does not exist.
func resetHiveState(tx *db.Tx, project string) error {
	res, err := tx.Exec(
		`UPDATE hive_state SET
				 iteration=0, phase='scanning', terminal_reason=NULL,
				 updated_at=CURRENT_TIMESTAMP
				 WHERE project=?`, project,
	)
	if err != nil {
		return fmt.Errorf("reset hive state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reset hive state: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("no hive state for project %q — nothing was reset", project)
	}
	return nil
}

// discardHivePasses retires every pending signal and clears the project's
// pass history, returning how many signals it retired.
func discardHivePasses(tx *db.Tx, project string) (int64, error) {
	// One database holds exactly one hive (see docs/specs/hive.md),
	// so every signal in it belongs to this project.
	sig, err := tx.Exec("UPDATE signals SET acted_on=1 WHERE acted_on=0")
	if err != nil {
		return 0, fmt.Errorf("retire pending signals: %w", err)
	}
	retired, _ := sig.RowsAffected()
	// The pass history restarts with the count, so the stall rule
	// never compares a new pass with one from before the reset.
	if _, err := tx.Exec("DELETE FROM hive_iterations WHERE project=?", project); err != nil {
		return 0, fmt.Errorf("clear hive pass history: %w", err)
	}
	return retired, nil
}

// optIntStr renders a nullable coordinate: its value, or "—" when it is
// unset.
func optIntStr(p *int) string {
	if p == nil {
		return "—"
	}
	return strconv.Itoa(*p)
}

// formatLabelCounts renders a label→count map as "assumption=3, guarantee=1",
// sorted by label, or "none" when the map is empty.
func formatLabelCounts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}
