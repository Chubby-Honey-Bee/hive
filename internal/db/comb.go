package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// VantageKind classifies an Comb row. Two values today:
//
//	region   per-coordinate-prefix belief digest (the comb's first
//	         vantage kind). vantage_key is the canonical coord-prefix
//	         string ("", "d1=0", "d1=0;d2=3", …).
//	forager   per-forager verdict from a swarm run.
//	         vantage_key is "forager:<name>" (e.g. "forager:optimist").
//
// Vantages are *isomorphic* — a region and a forager are the same comb
// cell at different scales: same CombRow shape, same template hook,
// same revision history. The Comb doesn't care whether you're reading a
// region's belief or one forager's verdict.
type VantageKind string

// The vantage kinds comb_state.vantage_kind and comb_embeddings accept.
const (
	VantageRegion  VantageKind = "region"
	VantageForager VantageKind = "forager"
	VantageFinding VantageKind = "finding"
	// VantageQuestion is the question a swarm run was asked (embeddings only;
	// there is no comb_state row for a question).
	VantageQuestion VantageKind = "question"
)

// FindingVantageKey is the canonical vantage_key for a finding-level
// embedding. e.g. FindingVantageKey(42) → "finding:42".
//
// The Dreamer's semantic-prune pass parses this convention; mismatched
// keys are silently ignored by `vantageKeyToFindingID` in
// internal/dreamer/prune_semantic.go.
func FindingVantageKey(id int64) string {
	return fmt.Sprintf("finding:%d", id)
}

// QuestionVantageKey is the embedding key for the question a swarm run was
// asked. Recall matches against these: the question is what a later asker
// has in hand, so it is the right thing to compare against.
func QuestionVantageKey(runID int64) string {
	return fmt.Sprintf("question:%d", runID)
}

// CombRow is one vantage on the Comb — one cell of the hive's comb.
// Mirrors the comb_state schema column-for-column; nullable coords use
// sql.NullInt64 so callers can distinguish "this vantage doesn't pin
// that dimension" from "pinned to value 0". Every query of comb_state names
// its columns, so a table that carries a column the schema does not declare
// reads and writes as one without it.
type CombRow struct {
	VantageKey         string
	VantageKind        VantageKind
	D1, D2, D3, D4     sql.NullInt64
	D5, D6, D7, D8     sql.NullInt64
	Narrative          string
	Confidence         int
	Contested          bool
	DominantLabel      sql.NullString
	EvidenceCount      int
	OpenQuestionsCount int
	DigestMethod       string
	RawJSON            sql.NullString
	LastRevisedAt      string
	CreatedAt          string
}

// CombRepo owns the comb_state table — the hive's shared belief surface. It
// writes comb_state only: the history row in comb_revisions is appended
// separately by internal/comb's builders, after the upsert and outside any
// transaction, so the two are not atomic.
type CombRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// NewCombRepo binds the two pools.
func NewCombRepo(writeDB, readDB *sql.DB) *CombRepo {
	return &CombRepo{writeDB: writeDB, readDB: readDB}
}

// Upsert inserts or replaces one Comb row at the given vantage. Touches
// last_revised_at on every call. Defaults VantageKind to `region` when the
// caller leaves it empty.
func (r *CombRepo) Upsert(row *CombRow) error {
	if row.VantageKind == "" {
		row.VantageKind = VantageRegion
	}
	_, err := r.writeDB.Exec(
		`INSERT INTO comb_state
		 (vantage_key, vantage_kind, d1, d2, d3, d4, d5, d6, d7, d8,
		  narrative, confidence, contested, dominant_label,
		  evidence_count, open_questions_count, digest_method,
		  raw_json, last_revised_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		 ON CONFLICT(vantage_key) DO UPDATE SET
		   vantage_kind=excluded.vantage_kind,
		   d1=excluded.d1, d2=excluded.d2, d3=excluded.d3, d4=excluded.d4,
		   d5=excluded.d5, d6=excluded.d6, d7=excluded.d7, d8=excluded.d8,
		   narrative=excluded.narrative,
		   confidence=excluded.confidence,
		   contested=excluded.contested,
		   dominant_label=excluded.dominant_label,
		   evidence_count=excluded.evidence_count,
		   open_questions_count=excluded.open_questions_count,
		   digest_method=excluded.digest_method,
		   raw_json=excluded.raw_json,
		   last_revised_at=CURRENT_TIMESTAMP`,
		row.VantageKey, string(row.VantageKind),
		nullableInt(row.D1), nullableInt(row.D2), nullableInt(row.D3), nullableInt(row.D4),
		nullableInt(row.D5), nullableInt(row.D6), nullableInt(row.D7), nullableInt(row.D8),
		row.Narrative, row.Confidence, boolToInt(row.Contested),
		nullableString(row.DominantLabel),
		row.EvidenceCount, row.OpenQuestionsCount, row.DigestMethod,
		nullableString(row.RawJSON),
	)
	if err != nil {
		return fmt.Errorf("upsert comb_state: %w", err)
	}
	return nil
}

// Get returns the digest for an exact vantage_key. Returns (nil, nil)
// when the key isn't present (callers fall back to wider regions for
// `region` vantages, or treat absence as "no verdict yet" for foragers).
func (r *CombRepo) Get(vantageKey string) (*CombRow, error) {
	row := r.readDB.QueryRow(
		`SELECT vantage_key, vantage_kind,
		        d1, d2, d3, d4, d5, d6, d7, d8,
		        narrative, confidence, contested, dominant_label,
		        evidence_count, open_questions_count, digest_method,
		        raw_json, last_revised_at, created_at
		 FROM comb_state WHERE vantage_key = ?`,
		vantageKey,
	)
	out, err := scanCombRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get comb_state: %w", err)
	}
	return out, nil
}

// scanCombRow reads one comb_state row.
func scanCombRow(s rowScanner) (*CombRow, error) {
	o := &CombRow{}
	var contestedInt int
	var kind string
	if err := s.Scan(
		&o.VantageKey, &kind,
		&o.D1, &o.D2, &o.D3, &o.D4,
		&o.D5, &o.D6, &o.D7, &o.D8,
		&o.Narrative, &o.Confidence, &contestedInt, &o.DominantLabel,
		&o.EvidenceCount, &o.OpenQuestionsCount, &o.DigestMethod,
		&o.RawJSON, &o.LastRevisedAt, &o.CreatedAt,
	); err != nil {
		return nil, err
	}
	o.VantageKind = VantageKind(kind)
	o.Contested = contestedInt != 0
	return o, nil
}

// List returns every Comb row, sorted by (vantage_kind, vantage_key) so
// regions and forager vantages are grouped in output.
func (r *CombRepo) List() ([]*CombRow, error) {
	return r.list("")
}

// ListKind returns every Comb row of the given kind.
func (r *CombRepo) ListKind(kind VantageKind) ([]*CombRow, error) {
	return r.list(string(kind))
}

func (r *CombRepo) list(kindFilter string) ([]*CombRow, error) {
	q, args := combListQuery(kindFilter)
	rows, err := r.readDB.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list comb_state: %w", err)
	}
	defer rows.Close()
	return scanCombRows(rows)
}

// combListQuery is list's SELECT and its arguments.
func combListQuery(kindFilter string) (string, []any) {
	q := `SELECT vantage_key, vantage_kind,
	             d1, d2, d3, d4, d5, d6, d7, d8,
	             narrative, confidence, contested, dominant_label,
	             evidence_count, open_questions_count, digest_method,
	             raw_json, last_revised_at, created_at
	      FROM comb_state`
	var args []any
	if kindFilter != "" {
		q += ` WHERE vantage_kind = ?`
		args = append(args, kindFilter)
	}
	return q + ` ORDER BY vantage_kind, vantage_key`, args
}

// scanCombRows reads every comb_state row.
func scanCombRows(rows *sql.Rows) ([]*CombRow, error) {
	var out []*CombRow
	for rows.Next() {
		o, err := scanCombRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CountContested returns how many rows are marked contested.
func (r *CombRepo) CountContested() (int, error) {
	var n int
	err := r.readDB.QueryRow(
		`SELECT COUNT(*) FROM comb_state WHERE contested=1`).Scan(&n)
	return n, err
}

// LastRefreshAt returns the most-recent last_revised_at across region rows.
// Empty string when there are none. Forager rows are left out: a forager
// verdict is no refresh.
func (r *CombRepo) LastRefreshAt() (string, error) {
	var t sql.NullString
	err := r.readDB.QueryRow(
		`SELECT MAX(last_revised_at) FROM comb_state WHERE vantage_kind = 'region'`).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !t.Valid {
		return "", nil
	}
	return t.String, nil
}

// nullableInt converts a sql.NullInt64 to a value sqlite can store as
// either INTEGER or NULL.
func nullableInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func nullableString(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ToCoordsMap returns a sparse coord map for the row (only Valid
// dimensions are present). Used by the digest builder + staleness check.
func (r *CombRow) ToCoordsMap() map[string]sql.NullInt64 {
	out := make(map[string]sql.NullInt64, 8)
	for _, p := range []struct {
		name string
		val  sql.NullInt64
	}{
		{"d1", r.D1}, {"d2", r.D2}, {"d3", r.D3}, {"d4", r.D4},
		{"d5", r.D5}, {"d6", r.D6}, {"d7", r.D7}, {"d8", r.D8},
	} {
		if p.val.Valid {
			out[p.name] = p.val
		}
	}
	return out
}

// Line renders a Comb row as the canonical narrative line that agents see
// when they consume {comb.<vantage>}, marked [stale] when stale says the
// vantage is stale by the Comb's staleness rule. Caps at ~512 chars so it
// can be inlined into a prompt without bloating context.
func (r *CombRow) Line(stale bool) string {
	return truncateBytes(r.body()+flagSuffix(r.Contested, " [contested]")+flagSuffix(stale, " [stale]"), 512)
}

// body is the row's narrative or, when it has none, its counts.
func (r *CombRow) body() string {
	if body := strings.TrimSpace(r.Narrative); body != "" {
		return body
	}
	return fmt.Sprintf(
		"vantage=%s evidence=%d open=%d dominant=%s confidence=%d%%",
		r.VantageKey, r.EvidenceCount, r.OpenQuestionsCount, r.dominant(), r.Confidence,
	)
}

// dominant is the row's dominant label, "—" when it has none.
func (r *CombRow) dominant() string {
	if r.DominantLabel.Valid {
		return r.DominantLabel.String
	}
	return "—"
}

// flagSuffix is suffix when the flag is set, else "".
func flagSuffix(set bool, suffix string) string {
	if set {
		return suffix
	}
	return ""
}

// ForagerVantageKey is the canonical vantage_key for a forager's verdict.
// e.g. ForagerVantageKey("optimist") → "forager:optimist".
func ForagerVantageKey(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "forager:")
	return "forager:" + name
}
