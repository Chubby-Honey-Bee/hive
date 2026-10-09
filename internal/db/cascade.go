package db

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// Cascade primitive operations: the storage-side hooks the MSS cascade
// engine in internal/mss needs. The cascade BFS lives in the mss package as
// domain logic, and the SQL stays here in the data layer.
//
// Each method is small and named, so CascadeRevert reads as the steps it
// takes.

// LoadDependencyEdges returns every finding that has a non-empty
// depends_on_ids list, with the deps already JSON-decoded. Returns the
// canonical mss.DependencyEdge type so the cascade engine consumes it
// without converting. Malformed rows are silently skipped, as the audit
// skips them: a corruption in one row shouldn't abort a whole cascade.
func (s *Store) LoadDependencyEdges() ([]mss.DependencyEdge, error) {
	return cascadeOn{read: s.ReadDB, write: s.WriteDB}.LoadDependencyEdges()
}

// cascadeOn is the cascade's storage on a pool pair or a Tx, so the store
// and a transaction run one set of statements.
type cascadeOn struct{ read, write Conn }

// CascadeRevert is Store.CascadeRevert inside the transaction: every revert,
// gap and alarm the cascade writes commits or rolls back with it.
func (t *Tx) CascadeRevert(findingID int64) ([]int64, error) {
	return mss.RunCascade(cascadeOn{read: t, write: t}, findingID)
}

func (c cascadeOn) LoadDependencyEdges() ([]mss.DependencyEdge, error) {
	rows, err := c.read.Query(
		`SELECT id, mss_label, depends_on_ids, d1, d2, d3, d4, wave, finding
		 FROM findings WHERE depends_on_ids IS NOT NULL`,
	)
	if err != nil {
		return nil, fmt.Errorf("load dependency edges: %w", err)
	}
	defer rows.Close()
	return scanDependencyEdges(rows)
}

// scanDependencyEdges reads every edge row, skipping one whose
// depends_on_ids is malformed.
func scanDependencyEdges(rows *sql.Rows) ([]mss.DependencyEdge, error) {
	var out []mss.DependencyEdge
	for rows.Next() {
		e, ok, err := scanDependencyEdge(rows)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// scanDependencyEdge reads one edge row; ok is false when its
// depends_on_ids is malformed — best-effort.
func scanDependencyEdge(rows *sql.Rows) (mss.DependencyEdge, bool, error) {
	var e mss.DependencyEdge
	var depsJSON string
	if err := rows.Scan(&e.ID, &e.Label, &depsJSON,
		&e.D1, &e.D2, &e.D3, &e.D4, &e.Wave, &e.Finding); err != nil {
		return e, false, fmt.Errorf("scan dependency edge: %w", err)
	}
	var deps []int64
	if err := json.Unmarshal([]byte(depsJSON), &deps); err != nil {
		return e, false, nil
	}
	e.Deps = deps
	return e, true, nil
}

// RevertFindingToUnknown sets a finding's label to 'unknown' and clears
// its depends_on_ids. This is the destructive half of an alarm cascade:
// the row's prior guarantee/assumption is being retracted because a
// dependency was contradicted.
func (s *Store) RevertFindingToUnknown(findingID int64) error {
	return cascadeOn{read: s.ReadDB, write: s.WriteDB}.RevertFindingToUnknown(findingID)
}

func (c cascadeOn) RevertFindingToUnknown(findingID int64) error {
	_, err := c.write.Exec(
		"UPDATE findings SET mss_label='unknown', depends_on_ids=NULL WHERE id=?",
		findingID,
	)
	if err != nil {
		return fmt.Errorf("revert finding %d: %w", findingID, err)
	}
	return nil
}

// RecordCascadeGap inserts a critical gap describing the cascade-revert
// event so a future wave can re-investigate. The agent column is
// hard-coded to 'hive-alarm' to make this provenance signal greppable.
func (s *Store) RecordCascadeGap(wave int, description string, d1, d2, d3, d4 *int) error {
	return cascadeOn{read: s.ReadDB, write: s.WriteDB}.RecordCascadeGap(wave, description, d1, d2, d3, d4)
}

func (c cascadeOn) RecordCascadeGap(wave int, description string, d1, d2, d3, d4 *int) error {
	_, err := c.write.Exec(
		`INSERT INTO gaps (wave, agent, description, priority, d1, d2, d3, d4)
		 VALUES (?, 'hive-alarm', ?, 'critical', ?, ?, ?, ?)`,
		wave, description, d1, d2, d3, d4,
	)
	if err != nil {
		return fmt.Errorf("record cascade gap: %w", err)
	}
	return nil
}

// EmitAlarmSignal writes the alarm signal that downstream HIVE iterations
// observe. Returns its caller to whatever cascade BFS produced it.
func (s *Store) EmitAlarmSignal(sourceID int64, d1, d2, d3, d4 *int, payload map[string]any, wave int) error {
	return cascadeOn{read: s.ReadDB, write: s.WriteDB}.EmitAlarmSignal(sourceID, d1, d2, d3, d4, payload, wave)
}

func (c cascadeOn) EmitAlarmSignal(sourceID int64, d1, d2, d3, d4 *int, payload map[string]any, wave int) error {
	pj, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal alarm payload: %w", err)
	}
	_, err = c.write.Exec(
		`INSERT INTO signals
		 (signal_type, source_type, source_id, target_d1, target_d2, target_d3, target_d4, payload_json, wave)
		 VALUES ('alarm', 'finding', ?, ?, ?, ?, ?, ?, ?)`,
		sourceID, d1, d2, d3, d4, string(pj), wave,
	)
	if err != nil {
		return fmt.Errorf("emit alarm signal: %w", err)
	}
	return nil
}
