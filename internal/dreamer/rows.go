package dreamer

import "database/sql"

// scanRows calls scan for each row of rows, stopping at its first error. It
// leaves rows.Err to the caller, which wraps a read cut short its own way.
func scanRows(rows *sql.Rows, scan func(*sql.Rows) error) error {
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return nil
}

// collectRows scans each row of rows with scan, stopping at its first
// error. It leaves rows.Err to the caller, as scanRows does.
func collectRows[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
