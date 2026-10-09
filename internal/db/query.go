package db

import "database/sql"

// QueryToMaps runs a query and returns each row as map[col]value. The
// first failure — of the query, the columns, a row's scan or the
// iteration — is returned with the rows read before it, so a caller can
// tell a partial read from a whole one; with an error, the rows are not
// the result.
func QueryToMaps(rdb *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := rdb.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	results, err := rowMaps(rows, cols)
	if err != nil {
		return results, err
	}
	return results, rows.Err()
}

// rowMaps reads every row as map[col]value, stopping at the first that does
// not scan. The caller checks rows.Err, which says whether the read
// completed.
func rowMaps(rows *sql.Rows, cols []string) ([]map[string]any, error) {
	var results []map[string]any
	for rows.Next() {
		row, err := rowMap(rows, cols)
		if err != nil {
			return results, err
		}
		results = append(results, row)
	}
	return results, nil
}

// rowMap reads the current row as map[col]value.
func rowMap(rows *sql.Rows, cols []string) (map[string]any, error) {
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	row := make(map[string]any, len(cols))
	for i, col := range cols {
		row[col] = vals[i]
	}
	return row, nil
}
