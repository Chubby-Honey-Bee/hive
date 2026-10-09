package db

import (
	"database/sql"
	"strings"
)

// ScanEvent is one recorded full-table-scan query shape.
type ScanEvent struct {
	ID         int64
	QueryShape string
	TableName  string
	Detail     string
	HitCount   int64
	LastSeen   string
}

// ClassifyScan runs EXPLAIN QUERY PLAN over a SELECT and reports whether
// SQLite resolves it as a full SCAN — of the table, or of an index — rather
// than an indexed SEARCH. This is the authoritative WASP probe-vs-scan check:
// it asks the planner what it will actually do, rather than guessing from the
// predicate columns.
//
// SQLite's EXPLAIN QUERY PLAN emits a `detail` per plan step:
//   - "SCAN <table>"                    → full table scan (unbounded work)
//   - "SCAN <table> USING INDEX …"      → full *index* scan; still O(n), and
//     still reported here, because walking every index entry is unbounded
//     work whatever the index is called
//   - "SEARCH <table> USING INDEX …"    → bounded probe via an index
//
// args are the read's own arguments, one per `?`. The driver refuses a query
// whose placeholders are unbound, so without them no parameterised read was
// ever classified. Returns scanned=true with the matching detail line, or
// false. Best-effort: any error (bad SQL, closed conn) returns
// (false, "", err) and callers degrade to "unknown", never failing the
// underlying read.
func ClassifyScan(conn *sql.DB, query, tableName string, args ...any) (scanned bool, detail string, err error) {
	rows, err := conn.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		return false, "", err
	}
	defer rows.Close()

	detail, err = lastFullScan(rows, tableName)
	if err != nil {
		return false, "", err
	}
	return detail != "", detail, nil
}

// lastFullScan reads a query plan and returns the detail of its last step
// that fully scans tableName, or "" when no step does.
func lastFullScan(rows *sql.Rows, tableName string) (string, error) {
	detail := ""
	for rows.Next() {
		d, err := planStepDetail(rows)
		if err != nil {
			return "", err
		}
		if fullScanOf(d, tableName) {
			detail = d
		}
	}
	return detail, rows.Err()
}

// planStepDetail reads one plan step's detail. EXPLAIN QUERY PLAN columns:
// id, parent, notused, detail.
func planStepDetail(rows *sql.Rows) (string, error) {
	var id, parent, notused int
	var d string
	if err := rows.Scan(&id, &parent, &notused, &d); err != nil {
		return "", err
	}
	return d, nil
}

// fullScanOf reports whether a plan step fully scans the table: the detail
// starts with "SCAN" and names the table; an indexed access says "SEARCH …
// USING INDEX".
func fullScanOf(detail, tableName string) bool {
	return strings.HasPrefix(detail, "SCAN ") && strings.Contains(detail, tableName)
}

// normalizeShape collapses whitespace so two textually-different but
// structurally-identical queries dedupe to the same shape. Our read
// queries already use `?` placeholders (not inline literals), so the
// query text IS the shape once whitespace is normalized.
func normalizeShape(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

// RecordScanIfFullScan classifies query, run with args, and, when it is a
// full scan of tableName, records the (deduped) shape in scan_events.
// Entirely best-effort — every error is swallowed so the detector never
// affects the read path or the run. scan_events is operator evidence, read
// by `chb wasp-scan-report`; nothing feeds it to a forager.
//
// There is no "expected scan" flag. One existed, but no caller ever set it,
// so every row was a surprise and the flag sorted and labelled nothing.
// Which scans the workload accepts is cde-mss.md's to say.
func (s *Store) RecordScanIfFullScan(query, tableName string, args ...any) {
	scanned, detail, err := ClassifyScan(s.ReadDB, query, tableName, args...)
	if err != nil || !scanned {
		return
	}
	shape := normalizeShape(query)
	// UNIQUE(query_shape): first sighting inserts; repeats bump the count.
	_, _ = s.WriteDB.Exec(`
		INSERT INTO scan_events (query_shape, table_name, detail)
		VALUES (?, ?, ?)
		ON CONFLICT(query_shape) DO UPDATE SET
			hit_count = hit_count + 1,
			last_seen = CURRENT_TIMESTAMP`,
		shape, tableName, detail,
	)
}

// ScanEvents returns every recorded scanning query shape, most hits first.
// Its only reader is `chb wasp-scan-report`.
func (s *Store) ScanEvents() ([]ScanEvent, error) {
	rows, err := s.ReadDB.Query(`
		SELECT id, query_shape, table_name, detail, hit_count, last_seen
		FROM scan_events
		ORDER BY hit_count DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScanEvent
	for rows.Next() {
		var e ScanEvent
		if err := rows.Scan(&e.ID, &e.QueryShape, &e.TableName, &e.Detail, &e.HitCount, &e.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
