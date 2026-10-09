package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	_ "modernc.org/sqlite"
)

// runProofInspection opens the workspace DB read-only and reports the
// post-run state of every primitive the proof workflow exercises:
// decisions persisted, repair attempts, cumulative cost, per-node
// metrics. Pure Go; no sqlite3 binary dependency. Returns an error
// when the structural invariants fail (no decisions persisted, or no
// node was enriched with rationale/tokens/cost).
func runProofInspection(stdout, stderr io.Writer, dbPath string) error {
	conn, err := sql.Open("sqlite",
		"file:"+dbPath+"?mode=ro&_pragma=journal_mode(WAL)")
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer conn.Close()
	if err := printProofTables(stdout, conn); err != nil {
		return err
	}
	return checkProofInvariants(stdout, conn)
}

// printProofTables prints the decisions persisted, the repair attempts, the
// cost total and the per-node metrics.
func printProofTables(out io.Writer, conn *sql.DB) error {
	if err := printProofSection(out, conn, "── DECISIONS PERSISTED ──",
		`SELECT node_name, condition, evaluated_value, COALESCE(branch_taken, '')
		 FROM workflow_decisions ORDER BY id`,
		[]string{"node_name", "condition", "evaluated", "branch"}); err != nil {
		return err
	}
	if err := printProofSection(out, conn, "── REPAIR ATTEMPTS ──",
		`SELECT node_name, attempt, accept_passed, substr(failure_reason, 1, 60)
		 FROM workflow_repairs ORDER BY id`,
		[]string{"node_name", "attempt", "accept_passed", "reason"}); err != nil {
		return err
	}
	if err := printProofCost(out, conn); err != nil {
		return err
	}
	return printProofSection(out, conn, "── PER-NODE METRICS ──",
		`SELECT node_name, status, tokens_in, tokens_out,
		        CASE
		          WHEN COALESCE(unmetered_calls, 0) = 0 THEN printf('$%0.4f', cost_usd_x10000/10000.0)
		          WHEN COALESCE(metered_calls, 0) = 0 THEN 'unmetered'
		          ELSE printf('$%0.4f + unmetered (%d calls)', cost_usd_x10000/10000.0, unmetered_calls)
		        END
		 FROM workflow_node_states ORDER BY id`,
		[]string{"node_name", "status", "in_tok", "out_tok", "cost"})
}

// printProofSection prints one titled table of the query's rows.
func printProofSection(out io.Writer, conn *sql.DB, title, query string, headers []string) error {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, title)
	return dumpRows(out, conn, query, headers)
}

// printProofCost prints the cost of the run's model calls: the node rows,
// plus the constraint probes each run's row carries.
func printProofCost(out io.Writer, conn *sql.DB) error {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "── COST TOTAL ──")
	var costX10K, metered, unmetered sql.NullInt64
	if err := conn.QueryRow(
		`SELECT n.cost + r.cost, n.metered + r.metered, n.unmetered + r.unmetered
		 FROM (SELECT COALESCE(SUM(cost_usd_x10000), 0) AS cost, COALESCE(SUM(metered_calls), 0) AS metered,
		              COALESCE(SUM(unmetered_calls), 0) AS unmetered FROM workflow_node_states) n,
		      (SELECT COALESCE(SUM(probe_cost_usd_x10000), 0) AS cost, COALESCE(SUM(probe_metered_calls), 0) AS metered,
		              COALESCE(SUM(probe_unmetered_calls), 0) AS unmetered FROM workflow_runs) r`,
	).Scan(&costX10K, &metered, &unmetered); err != nil {
		return fmt.Errorf("cost total: %w", err)
	}
	fmt.Fprintf(out, "  %s\n", models.CostLabel(fmt.Sprintf("$%.4f", float64(costX10K.Int64)/10000.0), metered.Int64, unmetered.Int64))
	return nil
}

// checkProofInvariants checks the hard invariants — at least one decision
// persisted, and at least one node enriched with rationale, tokens or cost
// — and prints the pass.
func checkProofInvariants(out io.Writer, conn *sql.DB) error {
	decisions, err := requireProofRows(conn, `SELECT COUNT(*) FROM workflow_decisions`,
		"no decisions persisted in workflow_decisions — the run recorded none of its decision nodes")
	if err != nil {
		return err
	}
	enriched, err := requireProofRows(conn,
		`SELECT COUNT(*) FROM workflow_node_states
		 WHERE rationale IS NOT NULL OR tokens_in > 0 OR cost_usd_x10000 > 0`,
		"no nodes enriched with rationale/tokens/cost — the run recorded none of its node results")
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "PROOF OF CAPABILITY: PASS")
	fmt.Fprintf(out, "  decisions: %d row(s) persisted\n", decisions)
	fmt.Fprintf(out, "  enriched:  %d node(s) with rationale/tokens/cost\n", enriched)
	return nil
}

// requireProofRows runs a COUNT(*) query and fails with missing when it
// counts no rows.
func requireProofRows(conn *sql.DB, query, missing string) (int, error) {
	var n int
	if err := conn.QueryRow(query).Scan(&n); err != nil {
		return 0, err
	}
	if n < 1 {
		return 0, errors.New(missing)
	}
	return n, nil
}

// dumpRows runs the query and prints results as a fixed-width table, in the
// style of `sqlite3 -column`, without depending on a shell.
func dumpRows(out io.Writer, conn *sql.DB, query string, headers []string) error {
	// Capture all rows up-front to compute column widths.
	allRows, err := queryProofRows(conn, query, len(headers))
	if err != nil {
		return err
	}
	if len(allRows) == 0 {
		fmt.Fprintln(out, "  (no rows)")
		return nil
	}
	printProofTable(out, headers, allRows)
	return nil
}

// queryProofRows runs the query and renders each of the ncols cells of
// every row with %v.
func queryProofRows(conn *sql.DB, query string, ncols int) ([][]string, error) {
	rows, err := conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	var allRows [][]string
	for rows.Next() {
		row, err := scanProofRow(rows, ncols)
		if err != nil {
			return nil, err
		}
		allRows = append(allRows, row)
	}
	return allRows, rows.Err()
}

// scanProofRow scans the current row's ncols cells and renders each with
// %v.
func scanProofRow(rows *sql.Rows, ncols int) ([]string, error) {
	cells := make([]any, ncols)
	ptrs := make([]any, ncols)
	for i := range cells {
		ptrs[i] = &cells[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	row := make([]string, len(cells))
	for i, c := range cells {
		row[i] = fmt.Sprintf("%v", c)
	}
	return row, nil
}

// printProofTable prints the rows under the headers, each column as wide
// as its widest cell, with a dashed rule under the headers.
func printProofTable(out io.Writer, headers []string, allRows [][]string) {
	widths := proofColumnWidths(headers, allRows)
	printProofTableRow(out, headers, widths)
	fmt.Fprint(out, "  ")
	for _, w := range widths {
		fmt.Fprint(out, dashLine(w), "  ")
	}
	fmt.Fprintln(out)
	for _, row := range allRows {
		printProofTableRow(out, row, widths)
	}
}

// printProofTableRow prints one line of the table, each cell padded to its
// column's width.
func printProofTableRow(out io.Writer, cells []string, widths []int) {
	fmt.Fprint(out, "  ")
	for i, c := range cells {
		fmt.Fprintf(out, "%-*s  ", widths[i], c)
	}
	fmt.Fprintln(out)
}

// proofColumnWidths is the width of each column: its widest cell, the
// header included.
func proofColumnWidths(headers []string, allRows [][]string) []int {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range allRows {
		for i, c := range r {
			widths[i] = max(widths[i], len(c))
		}
	}
	return widths
}

func dashLine(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = '-'
	}
	return string(out)
}
