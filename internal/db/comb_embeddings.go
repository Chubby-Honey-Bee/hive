package db

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// CombEmbeddingRow is one stored embedding for a vantage. The embedding
// itself is a packed float32 BLOB; dim records the vector size so
// queries can reject mixed-dim corpora before they corrupt rankings.
type CombEmbeddingRow struct {
	ID          int64
	VantageKey  string
	VantageKind VantageKind
	Model       string
	Dim         int
	Embedding   []float32
	SourceText  sql.NullString
	CreatedAt   string
}

// CombEmbeddingsRepo owns the comb_embeddings table — the semantic
// sidecar over the structural Comb. Pure-Go vector storage; the actual
// nearest-neighbour search lives in internal/embed (so scale-out
// to HNSW or sqlite-vec stays a one-package swap).
type CombEmbeddingsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newCombEmbeddingsRepo wires a repo over the supplied connection pools.
func newCombEmbeddingsRepo(writeDB, readDB *sql.DB) *CombEmbeddingsRepo {
	return &CombEmbeddingsRepo{writeDB: writeDB, readDB: readDB}
}

// Upsert writes (or replaces) an embedding for a (vantage_key, model)
// pair. Idempotent — re-running with a fresh embedding overwrites.
func (r *CombEmbeddingsRepo) Upsert(row *CombEmbeddingRow) error {
	if err := settleEmbeddingDim(row); err != nil {
		return err
	}
	if row.VantageKind == "" {
		row.VantageKind = VantageRegion
	}
	blob := PackFloat32(row.Embedding)
	// REPLACE rather than an in-place update, so every write takes a fresh
	// AUTOINCREMENT id and "most recently written" is exactly the highest
	// id: created_at has one-second resolution, so ordering by it cannot
	// break a same-second tie. Nothing references these ids.
	_, err := r.writeDB.Exec(
		`INSERT OR REPLACE INTO comb_embeddings
		 (vantage_key, vantage_kind, model, dim, embedding, source_text)
		 VALUES (?,?,?,?,?,?)`,
		row.VantageKey, string(row.VantageKind), row.Model, row.Dim,
		blob, nullableString(row.SourceText),
	)
	if err != nil {
		return fmt.Errorf("upsert comb_embeddings: %w", err)
	}
	return nil
}

// settleEmbeddingDim defaults Dim to the vector's length, and refuses a Dim
// the vector does not have.
func settleEmbeddingDim(row *CombEmbeddingRow) error {
	if row.Dim <= 0 {
		row.Dim = len(row.Embedding)
	}
	if row.Dim != len(row.Embedding) {
		return fmt.Errorf("embedding dim %d != Dim %d", len(row.Embedding), row.Dim)
	}
	return nil
}

// Get returns the embedding for (vantage_key, model). Returns
// (nil, nil) if no row exists — callers treat that as "not yet
// embedded".
func (r *CombEmbeddingsRepo) Get(vantageKey, model string) (*CombEmbeddingRow, error) {
	row := r.readDB.QueryRow(
		`SELECT id, vantage_key, vantage_kind, model, dim,
		        embedding, source_text, created_at
		 FROM comb_embeddings WHERE vantage_key = ? AND model = ?`,
		vantageKey, model,
	)
	return scanEmbedRow(row)
}

// ListByModel streams every embedding for a given model. Used by the
// in-memory Searcher's lazy load. Caller-supplied kindFilter is
// optional (empty = all kinds).
func (r *CombEmbeddingsRepo) ListByModel(model string, kindFilter VantageKind) ([]*CombEmbeddingRow, error) {
	q, args := embeddingsByModelQuery(model, kindFilter)
	rows, err := r.readDB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CombEmbeddingRow
	for rows.Next() {
		o, err := scanEmbedRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// embeddingsByModelQuery is ListByModel's SELECT and its arguments.
func embeddingsByModelQuery(model string, kindFilter VantageKind) (string, []any) {
	q := `SELECT id, vantage_key, vantage_kind, model, dim,
	             embedding, source_text, created_at
	      FROM comb_embeddings WHERE model = ?`
	args := []any{model}
	if kindFilter != "" {
		q += " AND vantage_kind = ?"
		args = append(args, string(kindFilter))
	}
	return q, args
}

// PackFloat32 encodes a slice of float32 to little-endian bytes,
// suitable for SQLite BLOB storage. Zero-allocation on the read side
// when paired with UnpackFloat32.
func PackFloat32(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// UnpackFloat32 decodes a little-endian byte slice back into a float32
// vector. Returns an error if len(b) is not a multiple of 4.
func UnpackFloat32(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("packed float32 length %d not multiple of 4", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}

func scanEmbedRow(s rowScanner) (*CombEmbeddingRow, error) {
	o := &CombEmbeddingRow{}
	var kind string
	var blob []byte
	err := s.Scan(
		&o.ID, &o.VantageKey, &kind, &o.Model, &o.Dim,
		&blob, &o.SourceText, &o.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan comb_embeddings: %w", err)
	}
	o.VantageKind = VantageKind(kind)
	vec, err := UnpackFloat32(blob)
	if err != nil {
		return nil, fmt.Errorf("unpack embedding for %s: %w", o.VantageKey, err)
	}
	o.Embedding = vec
	return o, nil
}

func scanEmbedRows(rows *sql.Rows) (*CombEmbeddingRow, error) {
	o := &CombEmbeddingRow{}
	var kind string
	var blob []byte
	if err := rows.Scan(
		&o.ID, &o.VantageKey, &kind, &o.Model, &o.Dim,
		&blob, &o.SourceText, &o.CreatedAt,
	); err != nil {
		return nil, err
	}
	o.VantageKind = VantageKind(kind)
	vec, err := UnpackFloat32(blob)
	if err != nil {
		return nil, fmt.Errorf("unpack embedding for %s: %w", o.VantageKey, err)
	}
	o.Embedding = vec
	return o, nil
}
