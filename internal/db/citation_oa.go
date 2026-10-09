package db

import (
	"database/sql"
	"time"
)

// CitationOAEntry is one row in the citation_oa_cache table.
type CitationOAEntry struct {
	DOI        string
	Source     string
	IsOA       bool
	OAURL      sql.NullString
	License    sql.NullString
	HostType   sql.NullString
	Title      sql.NullString
	Year       sql.NullInt64
	Error      sql.NullString
	VerifiedAt time.Time
}

// CitationOARepo is the cache layer for Unpaywall / OpenAlex lookups.
// Read first, miss → caller fires the HTTP verifier and Upserts the
// result.
type CitationOARepo struct {
	read  *sql.DB
	write *sql.DB
}

// CitationOA returns the cache repo bound to this store's read+write
// connections.
func (s *Store) CitationOA() *CitationOARepo {
	return &CitationOARepo{read: s.ReadDB, write: s.WriteDB}
}

// Get returns the cached entry for (doi, source), or sql.ErrNoRows.
func (r *CitationOARepo) Get(doi, source string) (*CitationOAEntry, error) {
	row := r.read.QueryRow(`
		SELECT doi, source, is_oa, oa_url, license, host_type, title, year, error, verified_at
		  FROM citation_oa_cache WHERE doi = ? AND source = ?`, doi, source)
	var e CitationOAEntry
	var isOA int
	var verified string
	if err := row.Scan(&e.DOI, &e.Source, &isOA, &e.OAURL, &e.License, &e.HostType,
		&e.Title, &e.Year, &e.Error, &verified); err != nil {
		return nil, err
	}
	e.IsOA = isOA == 1
	if t, err := time.Parse(time.RFC3339, verified); err == nil {
		e.VerifiedAt = t
	}
	return &e, nil
}

// Upsert writes (or replaces) a cache entry. Caller is responsible for
// passing the canonical lowercased DOI as the key.
func (r *CitationOARepo) Upsert(e *CitationOAEntry) error {
	isOA := 0
	if e.IsOA {
		isOA = 1
	}
	_, err := r.write.Exec(`
		INSERT INTO citation_oa_cache
		  (doi, source, is_oa, oa_url, license, host_type, title, year, error, verified_at)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(doi, source) DO UPDATE SET
		  is_oa = excluded.is_oa,
		  oa_url = excluded.oa_url,
		  license = excluded.license,
		  host_type = excluded.host_type,
		  title = excluded.title,
		  year = excluded.year,
		  error = excluded.error,
		  verified_at = excluded.verified_at`,
		e.DOI, e.Source, isOA, e.OAURL, e.License, e.HostType,
		e.Title, e.Year, e.Error, e.VerifiedAt.Format(time.RFC3339))
	return err
}

// CountByOA returns (n_oa, n_paywalled, n_unverified) across all rows.
// Used by mss_audit and the verify-citations CLI summary.
func (r *CitationOARepo) CountByOA() (int, int, int, error) {
	rows, err := r.read.Query(`SELECT is_oa, COALESCE(error, '') FROM citation_oa_cache`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	c, err := tallyOACache(rows)
	if err != nil {
		return 0, 0, 0, err
	}
	return c.oa, c.paywalled, c.unverified, nil
}

// oaCounts are cache rows by outcome.
type oaCounts struct {
	oa, paywalled, unverified int
}

// tallyOACache counts the cache rows by outcome. A row that will not scan
// is skipped.
func tallyOACache(rows *sql.Rows) (oaCounts, error) {
	var c oaCounts
	for rows.Next() {
		var isOA int
		var errStr string
		if err := rows.Scan(&isOA, &errStr); err != nil {
			continue
		}
		c.add(isOA, errStr)
	}
	return c, rows.Err()
}

// add counts one row: an error is unverified, is_oa=1 open, else paywalled.
func (c *oaCounts) add(isOA int, errStr string) {
	switch {
	case errStr != "":
		c.unverified++
	case isOA == 1:
		c.oa++
	default:
		c.paywalled++
	}
}
