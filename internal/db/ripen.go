package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// RipenLogRow is one pass entry in the ripen_log table — the
// Dreamer forager's per-pass audit trail. Ripening is what one forager
// archetype does to the Comb, not a separate subsystem.
type RipenLogRow struct {
	ID            int64
	PassName      string
	Status        string // completed|skipped|halted|dry_run|failed
	TouchedCount  int
	CostUSDx10000 int64
	Notes         map[string]any
	StartedAt     string
	CompletedAt   sql.NullString
}

// RipenRepo owns the ripen_log table.
type RipenRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// NewRipenRepo binds the two pools.
func NewRipenRepo(writeDB, readDB *sql.DB) *RipenRepo {
	return &RipenRepo{writeDB: writeDB, readDB: readDB}
}

// BeginPass writes a new ripen_log row in 'completed' state once
// CompletePass is called; for now it just inserts a placeholder so the
// pass has an id to attach diagnostics to later. status='dry_run' is
// reserved for the --dry-run path. Returns the row id.
func (r *RipenRepo) BeginPass(passName string) (int64, error) {
	res, err := r.writeDB.Exec(
		`INSERT INTO ripen_log (pass_name, status, notes_json)
		 VALUES (?, 'completed', '{}')`,
		passName,
	)
	if err != nil {
		return 0, fmt.Errorf("begin ripen pass: %w", err)
	}
	return res.LastInsertId()
}

// CompletePass updates an existing pass row with terminal status,
// touched count, cost, and notes.
func (r *RipenRepo) CompletePass(id int64, status string, touched int, costX10K int64, notes map[string]any) error {
	notesJSON := "{}"
	if notes != nil {
		b, err := json.Marshal(notes)
		if err != nil {
			return fmt.Errorf("marshal ripen notes: %w", err)
		}
		notesJSON = string(b)
	}
	_, err := r.writeDB.Exec(
		`UPDATE ripen_log
		 SET status=?, touched_count=?, cost_usd_x10000=?, notes_json=?,
		     completed_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		status, touched, costX10K, notesJSON, id,
	)
	if err != nil {
		return fmt.Errorf("complete ripen pass: %w", err)
	}
	return nil
}

// RecentPasses returns the last N pass rows ordered by started_at desc.
func (r *RipenRepo) RecentPasses(limit int) ([]*RipenLogRow, error) {
	rows, err := r.readDB.Query(
		`SELECT id, pass_name, status,
		        COALESCE(touched_count, 0),
		        COALESCE(cost_usd_x10000, 0),
		        COALESCE(notes_json, '{}'),
		        started_at,
		        completed_at
		 FROM ripen_log ORDER BY started_at DESC, id DESC LIMIT ?`,
		limitOr(limit, 50),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RipenLogRow
	for rows.Next() {
		row, err := scanRipenRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// limitOr is limit, or def when limit is not positive.
func limitOr(limit, def int) int {
	if limit <= 0 {
		return def
	}
	return limit
}

// scanRipenRow reads the current ripen_log row.
func scanRipenRow(rows *sql.Rows) (*RipenLogRow, error) {
	var (
		row       RipenLogRow
		notesJSON string
	)
	if err := rows.Scan(
		&row.ID, &row.PassName, &row.Status,
		&row.TouchedCount, &row.CostUSDx10000,
		&notesJSON, &row.StartedAt, &row.CompletedAt,
	); err != nil {
		return nil, err
	}
	row.Notes = decodeJSONObject(notesJSON)
	return &row, nil
}
