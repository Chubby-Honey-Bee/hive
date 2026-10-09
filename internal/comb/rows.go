package comb

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// eachRow calls scan for each row of rows, then closes it.
func eachRow(rows *sql.Rows, scan func() error) error {
	defer rows.Close()
	for rows.Next() {
		if err := scan(); err != nil {
			return err
		}
	}
	return rows.Err()
}

// queryEach runs query on q and calls scan with each row, then closes the
// rows. A failure is "scan <what>: ..." wrapping its cause.
func queryEach(q db.Conn, what, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := q.Query(query, args...)
	if err != nil {
		return fmt.Errorf("scan %s: %w", what, err)
	}
	if err := eachRow(rows, func() error { return scan(rows) }); err != nil {
		return fmt.Errorf("scan %s: %w", what, err)
	}
	return nil
}

// tableExists reports whether the database q reads holds the table name.
func tableExists(q db.Conn, name string) (bool, error) {
	var got string
	err := q.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// collectCoords runs query, which selects d1..d8, on read and returns the
// Coords of each row, a NULL axis left out.
func collectCoords(read *sql.DB, query string) ([]Coords, error) {
	rows, err := read.Query(query)
	if err != nil {
		return nil, err
	}
	var out []Coords
	err = eachRow(rows, func() error {
		var ds [8]sql.NullInt64
		if err := rows.Scan(&ds[0], &ds[1], &ds[2], &ds[3], &ds[4], &ds[5], &ds[6], &ds[7]); err != nil {
			return err
		}
		out = append(out, coordsOfRow(ds[:]))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// coordsOfRow is the Coords of a row's d1.. columns: each non-NULL one.
func coordsOfRow(ds []sql.NullInt64) Coords {
	dims := orderedDims()
	c := Coords{}
	for i, n := range ds {
		if n.Valid {
			c[dims[i]] = int(n.Int64)
		}
	}
	return c
}
