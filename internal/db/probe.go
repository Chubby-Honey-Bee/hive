package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// DefaultProbeLimit caps unbounded probes so a `chb db-read probe` with no
// filters cannot return the entire findings table — the WASP "Bounded Work"
// property at the boundary. Used by FindingsRepo.Probe.
const DefaultProbeLimit = 500

// MaxProbeLimit is the ceiling a caller may ask for, beside the default, so
// an explicit limit such as `limit=1000000` cannot read the whole table:
// bounded work holds for every caller.
//
// An over-limit request is refused, not clamped. Clamping silently would be
// the mirror image of the rule every probe surface enforces — a filter the
// server cannot honour is refused, not dropped — because a caller asking for
// 5000 and receiving exactly 500 cannot tell a cap from an exhausted table.
const MaxProbeLimit = 500

// ErrProbeLimit reports a limit above MaxProbeLimit.
var ErrProbeLimit = errors.New("limit exceeds the probe ceiling")

// probeDims are the coordinate dimensions a probe may filter on.
var probeDims = []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"}

// Probe performs a CDE coordinate lookup.
// coords maps dimension names ("d1".."d8") to single values or slices.
// If limit is nil, DefaultProbeLimit is applied.
func (r *FindingsRepo) Probe(coords map[string]any, wave *int, mssLabel, convergenceLevel *string, limit *int) ([]map[string]any, error) {
	q, args := probeQuery(coords, wave, mssLabel, convergenceLevel)
	effectiveLimit, err := probeLimit(limit)
	if err != nil {
		return nil, err
	}
	q += " ORDER BY convergence_count DESC, created_at DESC LIMIT ?"
	args = append(args, effectiveLimit)

	rows, err := r.readDB.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("probe: %w", err)
	}
	defer rows.Close()
	return scanProbeRows(rows)
}

// probeQuery is a probe's SELECT and its arguments, before its order and
// limit.
func probeQuery(coords map[string]any, wave *int, mssLabel, convergenceLevel *string) (string, []any) {
	q := "SELECT * FROM findings WHERE 1=1"
	var args []any
	for _, dim := range probeDims {
		if v, ok := coords[dim]; ok {
			cond, condArgs := coordCondition(dim, v)
			q += cond
			args = append(args, condArgs...)
		}
	}
	return addProbeFilters(q, args, wave, mssLabel, convergenceLevel)
}

// coordCondition filters one dimension on a single value or a list of
// values; any other value filters nothing.
func coordCondition(dim string, v any) (string, []any) {
	switch val := v.(type) {
	case float64:
		return fmt.Sprintf(" AND %s = ?", dim), []any{int(val)}
	case int:
		return fmt.Sprintf(" AND %s = ?", dim), []any{val}
	case []any:
		return inCondition(dim, val)
	}
	return "", nil
}

// inCondition filters one dimension on a list of values.
func inCondition(dim string, vals []any) (string, []any) {
	placeholders := "("
	var args []any
	for i, item := range vals {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, coordArg(item))
	}
	placeholders += ")"
	return fmt.Sprintf(" AND %s IN %s", dim, placeholders), args
}

// coordArg is a listed coordinate as the query takes it: a JSON number as
// an int.
func coordArg(item any) any {
	if n, ok := item.(float64); ok {
		return int(n)
	}
	return item
}

// addProbeFilters adds the wave, label and convergence filters a probe
// names.
func addProbeFilters(q string, args []any, wave *int, mssLabel, convergenceLevel *string) (string, []any) {
	if wave != nil {
		q += " AND wave = ?"
		args = append(args, *wave)
	}
	if mssLabel != nil {
		q += " AND mss_label = ?"
		args = append(args, *mssLabel)
	}
	if convergenceLevel != nil {
		q += " AND convergence_level = ?"
		args = append(args, *convergenceLevel)
	}
	return q, args
}

// probeLimit is the limit a probe runs with: DefaultProbeLimit unless a
// positive limit is asked for, and one above MaxProbeLimit is refused.
func probeLimit(limit *int) (int, error) {
	if limit == nil || *limit <= 0 {
		return DefaultProbeLimit, nil
	}
	if *limit > MaxProbeLimit {
		return 0, fmt.Errorf("%w: asked for %d, at most %d per probe — narrow the coordinates or page by wave",
			ErrProbeLimit, *limit, MaxProbeLimit)
	}
	return *limit, nil
}

// scanProbeRows reads each row of a probe as map[col]value.
func scanProbeRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, _ := rows.Columns()
	var results []map[string]any
	for rows.Next() {
		row, err := rowMap(rows, cols)
		if err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("probe: %w", err)
	}
	return results, nil
}

// GetByMSSLabel returns at most DefaultProbeLimit (500) findings with the
// given MSS label, most-converged first. Exactly 500 may mean more exist.
func (r *FindingsRepo) GetByMSSLabel(label string) ([]map[string]any, error) {
	return r.Probe(map[string]any{}, nil, &label, nil, nil)
}
