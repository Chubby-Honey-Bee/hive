package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Conflict represents a pair of contradicting findings.
type Conflict struct {
	ID              int64   `json:"id"`
	Wave            int     `json:"wave"`
	FindingAID      int64   `json:"finding_a_id"`
	FindingBID      int64   `json:"finding_b_id"`
	Description     string  `json:"description"`
	Resolution      *string `json:"resolution"`
	ResolvedByWave  *int    `json:"resolved_by_wave"`
	WinnerFindingID *int64  `json:"winner_finding_id"`
	CreatedAt       string  `json:"created_at"`
}

// ConflictsRepo owns the conflicts table.
type ConflictsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// NewConflictsRepo binds the two pools.
func NewConflictsRepo(writeDB, readDB *sql.DB) *ConflictsRepo {
	return &ConflictsRepo{writeDB: writeDB, readDB: readDB}
}

// AddConflict records a conflict between two findings.
func (r *ConflictsRepo) AddConflict(wave int, findingAID, findingBID int64, description string) error {
	_, err := r.writeDB.Exec(
		"INSERT INTO conflicts (wave, finding_a_id, finding_b_id, description) VALUES (?,?,?,?)",
		wave, findingAID, findingBID, description,
	)
	if err != nil {
		return fmt.Errorf("add conflict: %w", err)
	}
	return nil
}

// conflictRecorded probes for a pair already carrying a description.
const conflictRecorded = `SELECT 1 FROM conflicts WHERE finding_a_id = ? AND finding_b_id = ? AND description = ?`

// RecordOnce inserts a conflict unless the pair already carries the same
// description, resolved or not, and reports whether it inserted. Detection
// re-runs over the same findings (every ripen, every detect-conflicts and
// swarm-merge), and a plain insert would record each conflict again every
// time: the unresolved count behind a region's confidence would grow with
// each run, and a resolved conflict would come back unresolved.
func (r *ConflictsRepo) RecordOnce(wave int, findingAID, findingBID int64, description string) (bool, error) {
	res, err := r.writeDB.Exec(
		`INSERT INTO conflicts (wave, finding_a_id, finding_b_id, description)
		 SELECT ?, ?, ?, ? WHERE NOT EXISTS (`+conflictRecorded+`)`,
		wave, findingAID, findingBID, description, findingAID, findingBID, description,
	)
	if err != nil {
		return false, fmt.Errorf("record conflict: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Recorded reports whether the pair already carries this description.
func (r *ConflictsRepo) Recorded(findingAID, findingBID int64, description string) (bool, error) {
	var one int
	err := r.readDB.QueryRow(conflictRecorded, findingAID, findingBID, description).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check conflict: %w", err)
	}
	return true, nil
}

// SetWinner adjudicates a conflict: it names which of the two findings
// survives and leaves `resolution` NULL.
//
// That pair of states — a winner named, no resolution yet — is exactly what
// hive.evalAlarm looks for before emitting the alarm pheromone that cascades
// a revert onto the loser.
func (r *ConflictsRepo) SetWinner(conflictID, winnerFindingID int64) error {
	parties, err := r.readParties(conflictID)
	if err != nil {
		return err
	}
	if err := parties.checkWinner(conflictID, winnerFindingID); err != nil {
		return err
	}
	if _, err := r.writeDB.Exec(
		"UPDATE conflicts SET winner_finding_id=? WHERE id=?", winnerFindingID, conflictID,
	); err != nil {
		return fmt.Errorf("set winner on conflict %d: %w", conflictID, err)
	}
	return nil
}

// conflictParties is a conflict's two findings and its resolution.
type conflictParties struct {
	a, b       int64
	resolution sql.NullString
}

// readParties reads a conflict's parties and resolution.
func (r *ConflictsRepo) readParties(conflictID int64) (conflictParties, error) {
	var p conflictParties
	err := r.readDB.QueryRow(
		"SELECT finding_a_id, finding_b_id, resolution FROM conflicts WHERE id=?", conflictID,
	).Scan(&p.a, &p.b, &p.resolution)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("no conflict with id %d", conflictID)
	}
	if err != nil {
		return p, fmt.Errorf("read conflict %d: %w", conflictID, err)
	}
	return p, nil
}

// checkWinner refuses a winner that is not party to the conflict, and a
// conflict already resolved.
func (p conflictParties) checkWinner(conflictID, winnerFindingID int64) error {
	if winnerFindingID != p.a && winnerFindingID != p.b {
		return fmt.Errorf("finding %d is not party to conflict %d (which is between %d and %d)",
			winnerFindingID, conflictID, p.a, p.b)
	}
	if p.resolution.Valid {
		return fmt.Errorf("conflict %d is already resolved (%q); adjudicating it again would emit an alarm for a cascade that has already run",
			conflictID, p.resolution.String)
	}
	return nil
}

// Resolve closes a conflict: it records how the conflict was settled and the
// wave that settled it. It refuses an unknown conflict, one already
// resolved, and an empty resolution.
//
// The hive's termination check waits on every open conflict, and its plan
// re-dispatches one, re-arming the alarm once a winner is named, every
// iteration, so a conflict settled any way but the gate's numeric
// auto-resolve closes here.
func (r *ConflictsRepo) Resolve(conflictID int64, wave int, resolution string) error {
	return resolveConflict(r.writeDB, r.readDB, conflictID, wave, resolution)
}

// ResolveConflict is ConflictsRepo.Resolve inside the transaction.
func (t *Tx) ResolveConflict(conflictID int64, wave int, resolution string) error {
	return resolveConflict(t, t, conflictID, wave, resolution)
}

// resolveConflict is Resolve, writing through write and reading through read.
func resolveConflict(write, read Conn, conflictID int64, wave int, resolution string) error {
	if strings.TrimSpace(resolution) == "" {
		return fmt.Errorf("resolve conflict %d: resolution is required", conflictID)
	}
	resolved, err := markConflictResolved(write, conflictID, wave, resolution)
	if err != nil || resolved {
		return err
	}
	return whyConflictNotResolved(read, conflictID)
}

// markConflictResolved resolves the conflict if it is open, and reports
// whether it did.
func markConflictResolved(write Conn, conflictID int64, wave int, resolution string) (bool, error) {
	res, err := write.Exec(
		"UPDATE conflicts SET resolution = ?, resolved_by_wave = ? WHERE id = ? AND resolution IS NULL",
		resolution, wave, conflictID,
	)
	if err != nil {
		return false, fmt.Errorf("resolve conflict %d: %w", conflictID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("resolve conflict %d: %w", conflictID, err)
	}
	return n > 0, nil
}

// whyConflictNotResolved is the refusal for a conflict the update left
// alone: there is no such conflict, or it was resolved already.
func whyConflictNotResolved(read Conn, conflictID int64) error {
	var existing sql.NullString
	err := read.QueryRow("SELECT resolution FROM conflicts WHERE id = ?", conflictID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("resolve conflict %d: no such conflict", conflictID)
	}
	if err != nil {
		return fmt.Errorf("resolve conflict %d: %w", conflictID, err)
	}
	return fmt.Errorf("resolve conflict %d: already resolved (%q)", conflictID, existing.String)
}

// QueryConflicts returns conflicts, optionally filtered to unresolved only.
func (r *ConflictsRepo) QueryConflicts(unresolvedOnly bool) ([]Conflict, error) {
	q := "SELECT id, wave, finding_a_id, finding_b_id, description, resolution, resolved_by_wave, winner_finding_id, created_at FROM conflicts WHERE 1=1"
	if unresolvedOnly {
		q += " AND resolution IS NULL"
	}
	rows, err := r.readDB.Query(q)
	if err != nil {
		return nil, fmt.Errorf("query conflicts: %w", err)
	}
	defer rows.Close()
	return scanConflicts(rows)
}

// scanConflicts reads QueryConflicts' rows.
func scanConflicts(rows *sql.Rows) ([]Conflict, error) {
	var conflicts []Conflict
	for rows.Next() {
		var c Conflict
		if err := rows.Scan(&c.ID, &c.Wave, &c.FindingAID, &c.FindingBID, &c.Description, &c.Resolution, &c.ResolvedByWave, &c.WinnerFindingID, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan conflict: %w", err)
		}
		conflicts = append(conflicts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query conflicts: %w", err)
	}
	return conflicts, nil
}
