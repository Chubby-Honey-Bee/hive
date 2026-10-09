package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/spf13/cobra"
)

// newRunTotalsCmd wires `chb run-totals <run_id>` — a tiny helper for
// shell glue that needs to know the cumulative cost / tokens / node-count
// of a workflow run without parsing the agent-run JSON envelope or
// shelling out to sqlite3 + Python.
//
// Output is JSON to stdout (one object). With --field <name>, prints just
// the bare value for that field (so bash can do `VAR=$(chb run-totals
// 1 --field tokens_in)` without any JSON parsing).
func newRunTotalsCmd() *cobra.Command {
	var fieldName string
	cmd := &cobra.Command{
		Use:   "run-totals <run_id>",
		Short: "Print cumulative tokens / cost / node counts for a workflow run",
		Long: `Aggregates workflow_node_states for the given run id, plus the run's
constraint probes, which belong to no node, and prints:
  {
    "run_id":          int,
    "nodes_total":     int,
    "nodes_completed": int,
    "nodes_rejected":  int,
    "nodes_failed":    int,
    "tokens_in":       int,
    "tokens_out":      int,
    "cost_usd_x10000": int,    // 1/10000 USD; 10000 = $1.00, priced calls only
    "metered_calls":   int,    // model calls whose cost is known
    "unmetered_calls": int,    // model calls whose cost is not
    "cost_usd":        string  // dollars (4 decimals); "unmetered" when no
                               // call was priced; "<dollars> + unmetered
                               // (N calls)" when some were not
  }

A call is unmetered when its cost is unknown: its model has no entry in the
models config and the call left this machine, or its backend reports no
token counts, as a gemini CLI without --output-format json does. Such calls
add nothing to cost_usd_x10000 because their cost is unknown, not because
they were free. A call on this machine (provider local, at a loopback
endpoint) costs nothing, and is metered at $0.

With --field <name>, prints just the bare value for that field — useful for
bash glue that wants to assign an integer to a variable.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return printRunTotals(cmd.OutOrStdout(), args[0], fieldName)
		},
	}
	cmd.Flags().StringVar(&fieldName, "field", "",
		"print just this field's bare value instead of JSON")
	return cmd
}

// printRunTotals prints the totals of the run arg names as JSON or, when
// fieldName is set, the bare value of that one field.
func printRunTotals(out io.Writer, arg, fieldName string) error {
	runID, err := parseRunTotalsID(arg)
	if err != nil {
		return err
	}
	t, err := loadRunTotals(runID)
	if err != nil {
		return err
	}
	if fieldName != "" {
		return printRunTotalsField(out, t, fieldName)
	}
	return printRunTotalsJSON(out, t)
}

// parseRunTotalsID reads the run id argument, a positive integer.
func parseRunTotalsID(arg string) (int64, error) {
	runID, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || runID <= 0 {
		return 0, fmt.Errorf("invalid run_id %q (want positive integer)", arg)
	}
	return runID, nil
}

// printRunTotalsField prints the bare value of one field of the totals.
func printRunTotalsField(out io.Writer, t map[string]any, fieldName string) error {
	v, ok := t[fieldName]
	if !ok {
		return fmt.Errorf("unknown field %q (try: tokens_in, tokens_out, cost_usd_x10000, cost_usd, unmetered_calls, nodes_total)", fieldName)
	}
	fmt.Fprintln(out, v)
	return nil
}

// printRunTotalsJSON prints the totals as one indented JSON object.
func printRunTotalsJSON(out io.Writer, t map[string]any) error {
	payload, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(out, string(payload))
	return nil
}

// loadRunTotals reads workflow_node_states for the given run and returns
// the aggregated map. We use map[string]any so callers can range over
// it and so JSON output orders keys alphabetically.
func loadRunTotals(runID int64) (map[string]any, error) {
	row := store.ReadDB.QueryRow(
		`SELECT
		   COUNT(*) AS nodes_total,
		   COALESCE(SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END), 0) AS nodes_completed,
		   COALESCE(SUM(CASE WHEN status='rejected'  THEN 1 ELSE 0 END), 0) AS nodes_rejected,
		   COALESCE(SUM(CASE WHEN status='failed'    THEN 1 ELSE 0 END), 0) AS nodes_failed,
		   COALESCE(SUM(tokens_in),       0) AS tokens_in,
		   COALESCE(SUM(tokens_out),      0) AS tokens_out,
		   COALESCE(SUM(cost_usd_x10000), 0) AS cost_usd_x10000,
		   COALESCE(SUM(metered_calls),   0) AS metered_calls,
		   COALESCE(SUM(unmetered_calls), 0) AS unmetered_calls
		 FROM workflow_node_states WHERE run_id=?`,
		runID,
	)
	var (
		nTot, nDone, nRej, nFail int64
		tokIn, tokOut, costX10K  int64
		metered, unmetered       int64
	)
	if err := row.Scan(&nTot, &nDone, &nRej, &nFail, &tokIn, &tokOut, &costX10K, &metered, &unmetered); err != nil {
		return nil, fmt.Errorf("aggregate run %d: %w", runID, err)
	}
	// The constraint probes' calls belong to no node; the run's row
	// carries them.
	probe, err := db.ReadProbeUsage(store.ReadDB, runID)
	if err != nil {
		return nil, err
	}
	tokIn, tokOut, costX10K = tokIn+probe.TokensIn, tokOut+probe.TokensOut, costX10K+probe.CostUSDx10000
	metered, unmetered = metered+probe.MeteredCalls, unmetered+probe.UnmeteredCalls
	return map[string]any{
		"run_id":          runID,
		"nodes_total":     nTot,
		"nodes_completed": nDone,
		"nodes_rejected":  nRej,
		"nodes_failed":    nFail,
		"tokens_in":       tokIn,
		"tokens_out":      tokOut,
		"cost_usd_x10000": costX10K,
		"metered_calls":   metered,
		"unmetered_calls": unmetered,
		"cost_usd":        models.CostLabel(fmt.Sprintf("%.4f", float64(costX10K)/10000.0), metered, unmetered),
	}, nil
}
