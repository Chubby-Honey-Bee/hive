package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/cde"
)

// Dimension represents a registered CDE dimension.
type Dimension struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ValuesJSON  string `json:"values_json"`
	CreatedAt   string `json:"created_at"`
}

// DimensionsRepo owns the dimensions registry.
type DimensionsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newDimensionsRepo binds the two pools.
func newDimensionsRepo(writeDB, readDB *sql.DB) *DimensionsRepo {
	return &DimensionsRepo{writeDB: writeDB, readDB: readDB}
}

// AddDimension registers a dimension (CDE Phase 1: workload analysis).
// Re-registering an existing name updates its description and values in
// place. The d-slot a dimension occupies is derived from row order, so the
// id must never move: an INSERT OR REPLACE would delete and re-insert the
// row, silently remapping every d1–d8 and invalidating every stored
// coordinate.
func (r *DimensionsRepo) AddDimension(name, description, valuesJSON string) error {
	values, err := dimensionDomain(name, valuesJSON)
	if err != nil {
		return err
	}
	if err := r.checkDimensionRoom(name); err != nil {
		return err
	}
	if _, err := r.writeDB.Exec(
		`INSERT INTO dimensions (name, description, values_json) VALUES (?,?,?)
		 ON CONFLICT(name) DO UPDATE SET
		   description = excluded.description,
		   values_json = excluded.values_json`,
		name, description, valuesJSON,
	); err != nil {
		return fmt.Errorf("add dimension: %w", err)
	}
	r.warnStaleCoordinates(name, len(values))
	return nil
}

// dimensionDomain checks a dimension's name and returns its domain.
//
// A nameless row is unusable and poisons every later registration: the
// UNIQUE(name) upsert target is "", so the next unnamed write would update
// it instead of inserting. A name is required.
//
// values_json is the dimension's domain, and cde.ValidateCoords bounds
// every coordinate against its length. A row that will not parse cannot
// bound anything, so it must not reach the registry.
// `null` and `[]` parse, but give an empty domain: every later finding
// would be refused as out of bounds (max=-1) until the dimension was
// registered again.
func dimensionDomain(name, valuesJSON string) ([]string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("dimension requires a name")
	}
	var values []string
	if err := json.Unmarshal([]byte(valuesJSON), &values); err != nil {
		return nil, fmt.Errorf("dimension %q: values must be a JSON array of strings, so that coordinates on it can be bounded: %w", name, err)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("dimension %q: values must be a non-empty JSON array of strings; got %s, which leaves no coordinate in bounds", name, valuesJSON)
	}
	return values, nil
}

// checkDimensionRoom refuses a dimension beyond the coordinate space. A
// coordinate has exactly cde.MaxDimensions slots. A ninth dimension would
// be registered but unaddressable, and findings would be accepted without
// ever being validated or probed on it.
func (r *DimensionsRepo) checkDimensionRoom(name string) error {
	var existing int
	if err := r.readDB.QueryRow(
		`SELECT COUNT(*) FROM dimensions WHERE name <> ?`, name,
	).Scan(&existing); err != nil {
		return fmt.Errorf("count dimensions: %w", err)
	}
	if existing >= cde.MaxDimensions {
		return fmt.Errorf("dimension %q: the coordinate space holds %d dimensions and %d are already registered", name, cde.MaxDimensions, existing)
	}
	return nil
}

// warnStaleCoordinates reports findings that a registration has just made
// invalid. ValidateCoords checks a coordinate when it is written, so rows
// stored while a slot was unregistered — or before its domain shrank — keep
// a NULL or out-of-domain value that no later check revisits. A NULL there
// leaves the finding invisible to every probe on that dimension.
//
// It warns rather than refuses. Refusing the registration would be a
// deadlock: nothing can rewrite a stored coordinate (UpdateFinding does not
// accept d1..d8), so the rows could never be fixed and the dimension never
// registered. Best-effort, like the source warning in AddFinding.
func (r *DimensionsRepo) warnStaleCoordinates(name string, domain int) {
	slot, ok := r.dimensionSlot(name)
	if !ok {
		return
	}
	col := fmt.Sprintf("d%d", slot)
	n, ok := r.staleCoordinates(col, domain)
	if !ok {
		return
	}
	fmt.Fprintf(os.Stderr,
		"  WARNING: registering %q as %s leaves %d stored finding(s) with a NULL or out-of-domain %s. "+
			"Those rows are invisible to probes on %s; they were written before the slot was bounded "+
			"and are not re-validated.\n", name, col, n, col, col)
}

// dimensionSlot is the d-slot, 1 to 8, a registered dimension occupies.
func (r *DimensionsRepo) dimensionSlot(name string) (int, bool) {
	var slot int
	if err := r.readDB.QueryRow(
		`SELECT COUNT(*) FROM dimensions WHERE id <= (SELECT id FROM dimensions WHERE name = ?)`, name,
	).Scan(&slot); err != nil || slot < 1 || slot > 8 {
		return 0, false
	}
	return slot, true
}

// staleCoordinates counts the findings whose coordinate in col is NULL or
// outside a domain of the given size; false when there are none.
func (r *DimensionsRepo) staleCoordinates(col string, domain int) (int, bool) {
	var n int
	if err := r.readDB.QueryRow(fmt.Sprintf(
		`SELECT COUNT(*) FROM findings WHERE %s IS NULL OR %s < 0 OR %s >= ?`, col, col, col), domain,
	).Scan(&n); err != nil || n == 0 {
		return 0, false
	}
	return n, true
}

// GetDimensions returns all registered dimensions ordered by ID.
func (r *DimensionsRepo) GetDimensions() ([]Dimension, error) {
	rows, err := r.readDB.Query("SELECT id, name, description, values_json, created_at FROM dimensions ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("query dimensions: %w", err)
	}
	defer rows.Close()
	return scanDimensions(rows)
}

// scanDimensions reads GetDimensions' rows.
func scanDimensions(rows *sql.Rows) ([]Dimension, error) {
	var dims []Dimension
	for rows.Next() {
		var d Dimension
		if err := rows.Scan(&d.ID, &d.Name, &d.Description, &d.ValuesJSON, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan dimension: %w", err)
		}
		dims = append(dims, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query dimensions: %w", err)
	}
	return dims, nil
}
