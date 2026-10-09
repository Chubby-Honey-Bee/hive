package hive

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// ExistingProject returns the name of a hive project already initialised in
// this database other than `project`, or "" when there is none.
//
// One database holds one meaningful hive: `ScanState` reads findings, gaps,
// conflicts and signals across the whole DB, so a second project would
// silently reason over the first one's data. Rather than pretend to be
// multi-tenant, the invariant is enforced at init and each project gets its
// own workspace — the pattern the docs already recommend.
func ExistingProject(store *db.Store, project string) (string, error) {
	var other string
	err := store.ReadDB.QueryRow(
		`SELECT project FROM hive_state WHERE project != ? ORDER BY id LIMIT 1`, project,
	).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("check existing hive projects: %w", err)
	}
	return other, nil
}

// InitProject records project's hive_state row unless the database already
// holds a hive. The check and the insert are one statement, so two
// concurrent inits of different projects cannot both succeed; a check
// followed by a separate insert let both through. It reports created=false
// with other="" when project is already on record, and names the other
// project when a different one holds the database. A new hive starts at the
// tier rule's start rung (defaultModelTier).
func InitProject(store *db.Store, project string) (created bool, other string, err error) {
	res, err := store.WriteDB.Exec(
		`INSERT INTO hive_state (project, model_tier)
		 SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM hive_state WHERE project != ?)
		 ON CONFLICT(project) DO NOTHING`,
		project, defaultModelTier(), project,
	)
	if err != nil {
		return false, "", fmt.Errorf("init hive %q: %w", project, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, "", fmt.Errorf("init hive %q: %w", project, err)
	}
	if n > 0 {
		return true, "", nil
	}
	other, err = ExistingProject(store, project)
	return false, other, err
}
