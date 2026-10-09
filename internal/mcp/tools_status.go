package mcp

// The status tools, in-process reads of the store: chb_status polls the
// current phase / wave / agent counts, chb_summary is the full summary
// from db.GetSummary(), and chb_findings queries findings by wave / label /
// limit.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
)

func statusSpec() map[string]any {
	return map[string]any{
		"name":        "chb_status",
		"title":       "Hive status",
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		"description": "Poll the current research status: phase, wave, agent counts, findings, gaps. With `project`, reads that project's hive where `chb hive` keeps it: the project's workspace database (workspace/<project>/hive.db) when hive.db hosts another project's hive. `db` in the result names the file read.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project": map[string]any{"type": "string", "description": "Project name to query (optional)"},
			},
		},
	}
}

func summarySpec() map[string]any {
	return map[string]any{
		"name":        "chb_summary",
		"title":       "Workspace summary",
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		"description": "Return the full research summary: total findings, MSS label breakdown, gaps, conflicts, sources, latest evaluation.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func findingsSpec() map[string]any {
	return map[string]any{
		"name":        "chb_findings",
		"title":       "Query findings",
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		"description": "Query findings from the workspace database (hive.db) by wave, MSS label, or limit.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"wave":  map[string]any{"type": "integer", "description": "Filter by wave number"},
				"label": map[string]any{"type": "string", "description": "Filter by MSS label: definition|guarantee|assumption|unknown"},
				"limit": map[string]any{"type": "integer", "description": "Max results (default 20, at most 500 — larger is refused, not clamped)"},
			},
		},
	}
}

// ─── chb_status ─────────────────────────────────────────────

func (s *mcpServer) handleStatus(req rpcRequest, args map[string]any) {
	if s.store == nil {
		s.writeToolResult(req.ID, "", "HIVE_DB_PATH is not set", true)
		return
	}
	project := stringArg(args, "project")
	store, dbPath, closeStore, err := s.statusStore(project)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	defer closeStore()

	result := projectStatus(store, project)
	result["db"] = dbPath
	b, _ := json.MarshalIndent(result, "", "  ")
	s.writeToolResult(req.ID, string(b), "", false)
}

// statusStore is the store chb_status reads for a project, its path, and
// what closes it. A project is read where `chb hive` keeps it (hive.md §
// One hive per database): this server's store, or the project's workspace
// database when the store hosts another project's hive. No project names
// no hive, and reads the store.
func (s *mcpServer) statusStore(project string) (*db.Store, string, func(), error) {
	if project == "" {
		return s.store, s.store.Path, func() {}, nil
	}
	res, err := hive.Resolve(hive.Lookup{Project: project, Named: s.store, NamedPath: s.store.Path})
	if err != nil {
		return nil, "", nil, err
	}
	return res.Store, res.Path, func() { _ = res.Close() }, nil
}

// projectStatus is chb_status' result read from store: the project's hive
// phase, iteration and terminal reason, the agent runs by status, the
// current wave, and the summary's counts. Each read is best-effort: one
// that fails leaves its value zero.
func projectStatus(store *db.Store, project string) map[string]any {
	var phase, termReason string
	var iteration int
	_ = store.ReadDB.QueryRow(
		`SELECT COALESCE(phase,''), COALESCE(iteration,0), COALESCE(terminal_reason,'')
		   FROM hive_state WHERE project=? ORDER BY updated_at DESC LIMIT 1`,
		project,
	).Scan(&phase, &iteration, &termReason)

	var running, completed, failed int
	_ = store.ReadDB.QueryRow("SELECT COUNT(*) FROM agent_runs WHERE status='running'").Scan(&running)
	_ = store.ReadDB.QueryRow("SELECT COUNT(*) FROM agent_runs WHERE status='completed'").Scan(&completed)
	_ = store.ReadDB.QueryRow("SELECT COUNT(*) FROM agent_runs WHERE status='failed'").Scan(&failed)

	var wave int
	_ = store.ReadDB.QueryRow("SELECT COALESCE(MAX(wave),0) FROM findings").Scan(&wave)

	findings, gaps, conflicts, mssLabels := summaryCounts(store)
	return map[string]any{
		"project":          project,
		"phase":            phase,
		"current_wave":     wave,
		"iteration":        iteration,
		"agents_running":   running,
		"agents_completed": completed,
		"agents_failed":    failed,
		"findings_count":   findings,
		"gaps_count":       gaps,
		"conflicts_count":  conflicts,
		"mss_labels":       mssLabels,
		"terminal_reason":  termReason,
	}
}

// summaryCounts is the store's summary counts: its findings, unresolved
// gaps and conflicts, and its MSS labels; zero when it has no summary.
func summaryCounts(store *db.Store) (findings, gaps, conflicts int, mssLabels map[string]int) {
	sum, _ := store.GetSummary()
	if sum == nil {
		return 0, 0, 0, nil
	}
	return sum.TotalFindings, sum.UnresolvedGaps, sum.UnresolvedConflicts, sum.MSSLabels
}

// ─── chb_summary ────────────────────────────────────────────

func (s *mcpServer) handleSummary(req rpcRequest, _ map[string]any) {
	if s.store == nil {
		s.writeToolResult(req.ID, "", "HIVE_DB_PATH is not set", true)
		return
	}
	sum, err := s.store.GetSummary()
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	b, _ := json.MarshalIndent(sum, "", "  ")
	s.writeToolResult(req.ID, string(b), "", false)
}

// ─── chb_findings ───────────────────────────────────────────

func (s *mcpServer) handleFindings(req rpcRequest, args map[string]any) {
	if s.store == nil {
		s.writeToolResult(req.ID, "", "HIVE_DB_PATH is not set", true)
		return
	}

	query, qArgs, err := findingsQuery(args)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	results, err := s.queryFindings(query, qArgs)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	b, _ := json.MarshalIndent(results, "", "  ")
	s.writeToolResult(req.ID, string(b), "", false)
}

// findingsSelect is chb_findings' query before its filters. A NULL
// convergence_count reads as 1, the count a finding gets on write
// (cde-mss.md § Convergence).
const findingsSelect = `SELECT id, wave, agent, mss_label, finding,
		COALESCE(evidence,'') AS evidence,
		COALESCE(source_urls,'') AS source_urls,
		COALESCE(convergence_count,1) AS convergence_count FROM findings`

// findingsQuery is chb_findings' query for the call's filters and limit,
// and its arguments. A filter the server cannot read is refused, not
// dropped: dropping it turns a bounded probe into a full scan and reports
// success on it.
func findingsQuery(args map[string]any) (string, []any, error) {
	if err := numericToolArgs(args, "wave", "limit"); err != nil {
		return "", nil, err
	}
	limit, err := findingsLimit(args)
	if err != nil {
		return "", nil, err
	}
	where, qArgs := findingsFilter(args)
	return findingsSelect + where + fmt.Sprintf(" ORDER BY wave, id LIMIT %d", limit), qArgs, nil
}

// findingsLimit is the call's limit, 20 when it names none. A limit that is
// not positive is refused, and so is one above the ceiling, the same one
// `chb db-read probe` applies, since the limit goes straight into LIMIT.
func findingsLimit(args map[string]any) (int, error) {
	l, ok := args["limit"].(float64)
	switch {
	case !ok:
		return 20, nil
	case l <= 0:
		return 0, fmt.Errorf("limit must be a positive integer, got %v", l)
	case l > db.MaxProbeLimit:
		return 0, fmt.Errorf("limit %v exceeds the ceiling of %d — narrow by wave or label", l, db.MaxProbeLimit)
	}
	return int(l), nil
}

// findingsFilter is the WHERE clause for the call's wave and label, ""
// for neither, and its arguments.
func findingsFilter(args map[string]any) (string, []any) {
	var conditions []string
	var qArgs []any
	if w, ok := positiveArg(args, "wave"); ok {
		conditions = append(conditions, "wave=?")
		qArgs = append(qArgs, int(w))
	}
	if lbl := stringArg(args, "label"); lbl != "" {
		conditions = append(conditions, "mss_label=?")
		qArgs = append(qArgs, lbl)
	}
	if len(conditions) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conditions, " AND "), qArgs
}

// findingRow is one finding chb_findings returns.
type findingRow struct {
	ID               int64  `json:"id"`
	Wave             int    `json:"wave"`
	Agent            string `json:"agent"`
	MSSLabel         string `json:"mss_label"`
	Finding          string `json:"finding"`
	Evidence         string `json:"evidence,omitempty"`
	SourceURLs       string `json:"source_urls,omitempty"`
	ConvergenceCount int    `json:"convergence_count"`
}

// queryFindings runs a findings query. A row that does not scan is the
// query's error, never returned half-scanned, its other fields zero, as
// though that were the finding.
func (s *mcpServer) queryFindings(query string, qArgs []any) ([]findingRow, error) {
	rows, err := s.store.ReadDB.Query(query, qArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []findingRow{}
	for rows.Next() {
		var f findingRow
		if err := rows.Scan(&f.ID, &f.Wave, &f.Agent, &f.MSSLabel, &f.Finding,
			&f.Evidence, &f.SourceURLs, &f.ConvergenceCount); err != nil {
			return nil, err
		}
		results = append(results, f)
	}
	return results, rows.Err()
}
