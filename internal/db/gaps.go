package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Gap represents an MSS unknown with CDE coordinates.
type Gap struct {
	ID                  int64   `json:"id"`
	Wave                int     `json:"wave"`
	Agent               string  `json:"agent"`
	Description         string  `json:"description"`
	D1                  *int    `json:"d1"`
	D2                  *int    `json:"d2"`
	D3                  *int    `json:"d3"`
	D4                  *int    `json:"d4"`
	Priority            string  `json:"priority"`
	ResolvedByWave      *int    `json:"resolved_by_wave"`
	ResolvedByAgent     *string `json:"resolved_by_agent"`
	ResolutionFindingID *int64  `json:"resolution_finding_id"`
	CreatedAt           string  `json:"created_at"`
}

// GapsRepo owns the gaps table.
type GapsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// NewGapsRepo binds the two pools.
func NewGapsRepo(writeDB, readDB *sql.DB) *GapsRepo {
	return &GapsRepo{writeDB: writeDB, readDB: readDB}
}

// AddGap writes a gap record.
func (r *GapsRepo) AddGap(wave int, agent, description, priority string, d1, d2, d3, d4 *int) error {
	_, err := r.insertGap(wave, agent, description, priority, d1, d2, d3, d4)
	return err
}

// insertGap writes a gap record and returns its id: AddGap's insert, which
// the store's write path (WriteRecord) also makes.
func (r *GapsRepo) insertGap(wave int, agent, description, priority string, d1, d2, d3, d4 *int) (int64, error) {
	var id int64
	err := r.writeDB.QueryRow(
		"INSERT INTO gaps (wave, agent, description, priority, d1, d2, d3, d4) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
		wave, agent, description, priority, d1, d2, d3, d4,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add gap: %w", err)
	}
	return id, nil
}

// ResolveGap marks a gap answered by a finding: the wave and agent that
// answered it and the finding that did. It refuses an unknown gap, a gap
// already resolved, and a finding that does not exist.
//
// The columns have been in the schema from the start and nothing wrote them,
// so a gap could never close: the hive's termination check, which needs no
// open critical or important gap, was unreachable in any project that had
// recorded one, and its planner re-dispatched the same gap every iteration.
func (r *GapsRepo) ResolveGap(gapID int64, wave int, agent string, findingID int64) error {
	if err := r.checkResolution(gapID, agent, findingID); err != nil {
		return err
	}
	resolved, err := r.markGapResolved(gapID, wave, agent, findingID)
	if err != nil || resolved {
		return err
	}
	return r.whyGapNotResolved(gapID)
}

// checkResolution refuses a resolution with no agent, or by a finding that
// does not exist.
func (r *GapsRepo) checkResolution(gapID int64, agent string, findingID int64) error {
	if strings.TrimSpace(agent) == "" {
		return fmt.Errorf("resolve gap %d: agent is required", gapID)
	}
	var one int
	err := r.readDB.QueryRow("SELECT 1 FROM findings WHERE id = ?", findingID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("resolve gap %d: finding %d does not exist", gapID, findingID)
	}
	if err != nil {
		return fmt.Errorf("resolve gap %d: %w", gapID, err)
	}
	return nil
}

// markGapResolved resolves the gap if it is open, and reports whether it
// did.
func (r *GapsRepo) markGapResolved(gapID int64, wave int, agent string, findingID int64) (bool, error) {
	res, err := r.writeDB.Exec(
		"UPDATE gaps SET resolved_by_wave = ?, resolved_by_agent = ?, resolution_finding_id = ? WHERE id = ? AND resolved_by_wave IS NULL",
		wave, agent, findingID, gapID,
	)
	if err != nil {
		return false, fmt.Errorf("resolve gap %d: %w", gapID, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// whyGapNotResolved is the refusal for a gap the update left alone: there
// is no such gap, or it was resolved already.
func (r *GapsRepo) whyGapNotResolved(gapID int64) error {
	var resolvedIn sql.NullInt64
	err := r.readDB.QueryRow("SELECT resolved_by_wave FROM gaps WHERE id = ?", gapID).Scan(&resolvedIn)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("resolve gap %d: no such gap", gapID)
	}
	if err != nil {
		return fmt.Errorf("resolve gap %d: %w", gapID, err)
	}
	return fmt.Errorf("resolve gap %d: already resolved in wave %d", gapID, resolvedIn.Int64)
}

// QueryGaps returns gaps matching the given filters.
func (r *GapsRepo) QueryGaps(priority *string, unresolvedOnly bool, d1, d2, d3, d4 *int) ([]Gap, error) {
	q, args := gapsQuery(priority, unresolvedOnly, d1, d2, d3, d4)
	rows, err := r.readDB.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query gaps: %w", err)
	}
	defer rows.Close()
	return scanGaps(rows)
}

// gapsQuery is QueryGaps' SELECT and its arguments.
func gapsQuery(priority *string, unresolvedOnly bool, d1, d2, d3, d4 *int) (string, []any) {
	q := "SELECT id, wave, agent, description, d1, d2, d3, d4, priority, resolved_by_wave, resolved_by_agent, resolution_finding_id, created_at FROM gaps WHERE 1=1"
	var args []any
	if priority != nil {
		q += " AND priority = ?"
		args = append(args, *priority)
	}
	if unresolvedOnly {
		q += " AND resolved_by_wave IS NULL"
	}
	q, args = addCoordFilters(q, args, d1, d2, d3, d4)
	return q + " ORDER BY priority, created_at", args
}

// addCoordFilters filters on each coordinate given.
func addCoordFilters(q string, args []any, d1, d2, d3, d4 *int) (string, []any) {
	for _, pair := range []struct {
		name string
		val  *int
	}{{"d1", d1}, {"d2", d2}, {"d3", d3}, {"d4", d4}} {
		if pair.val != nil {
			q += fmt.Sprintf(" AND %s = ?", pair.name)
			args = append(args, *pair.val)
		}
	}
	return q, args
}

// scanGaps reads QueryGaps' rows.
func scanGaps(rows *sql.Rows) ([]Gap, error) {
	var gaps []Gap
	for rows.Next() {
		var g Gap
		if err := rows.Scan(&g.ID, &g.Wave, &g.Agent, &g.Description, &g.D1, &g.D2, &g.D3, &g.D4, &g.Priority, &g.ResolvedByWave, &g.ResolvedByAgent, &g.ResolutionFindingID, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan gap: %w", err)
		}
		gaps = append(gaps, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query gaps: %w", err)
	}
	return gaps, nil
}
