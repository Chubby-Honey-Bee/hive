package db

import (
	"database/sql"
	"fmt"
)

// FollowupsRepo owns the followups table.
type FollowupsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newFollowupsRepo binds the two pools.
func newFollowupsRepo(writeDB, readDB *sql.DB) *FollowupsRepo {
	return &FollowupsRepo{writeDB: writeDB, readDB: readDB}
}

// AddFollowup records a follow-up question.
func (r *FollowupsRepo) AddFollowup(wave int, agent, question, priority string, d1, d2, d3, d4 *int) error {
	_, err := r.writeDB.Exec(
		"INSERT INTO followups (wave, agent, question, priority, d1, d2, d3, d4) VALUES (?,?,?,?,?,?,?,?)",
		wave, agent, question, priority, d1, d2, d3, d4,
	)
	if err != nil {
		return fmt.Errorf("add followup: %w", err)
	}
	return nil
}
