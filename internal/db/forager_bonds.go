package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// BondKind classifies a forager-to-forager bond.
//
//	cites        downstream forager reads upstream's verdict before
//	             producing its own. Substitutes {comb.forager:<up>} into
//	             the prompt directly.
//	contradicts  downstream forager's role is to construct the strongest
//	             possible inversion of upstream's verdict. The runner
//	             wraps the prompt with an "invert this" header.
//	resonates    no edge dependency; instead, the quorum sensor watches
//	             for both foragers converging on overlapping vantages
//	             within one swarm run. Convergence emits a `nabla`
//	             signal and promotes nothing.
type BondKind string

// The bond kinds forager_bonds.bond_kind accepts.
const (
	BondCites       BondKind = "cites"
	BondContradicts BondKind = "contradicts"
	BondResonates   BondKind = "resonates"
)

// ForagerBondRow is one row of the forager_bonds table — a typed,
// directed relation between two foragers observed during a swarm run
// (or declared in their frontmatter).
type ForagerBondRow struct {
	ID         int64
	RunID      sql.NullInt64
	From       string // upstream forager name
	To         string // downstream forager name
	Kind       BondKind
	Weight     float64
	Fired      bool
	Payload    map[string]any
	ObservedAt string
}

// ForagerBondsRepo owns the forager_bonds table.
type ForagerBondsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newForagerBondsRepo binds the two pools.
func newForagerBondsRepo(writeDB, readDB *sql.DB) *ForagerBondsRepo {
	return &ForagerBondsRepo{writeDB: writeDB, readDB: readDB}
}

// RecordFired writes a bond that has already fired, in one statement, so no
// reader — a test polling ListByRun, say — sees the bond with fired=0, a
// state the quorum sensor never means it to be in.
func (r *ForagerBondsRepo) RecordFired(runID int64, from, to string, kind BondKind, weight float64, payload map[string]any) (int64, error) {
	if err := checkBond(kind, from, to); err != nil {
		return 0, err
	}
	pj, err := marshalPayload(payload)
	if err != nil {
		return 0, err
	}
	res, err := r.writeDB.Exec(
		`INSERT INTO forager_bonds
		 (run_id, from_forager, to_forager, bond_kind, weight, fired, payload_json)
		 VALUES (?, ?, ?, ?, ?, 1, ?)`,
		nullIfZero(runID), from, to, string(kind), bondWeight(weight), pj,
	)
	if err != nil {
		return 0, fmt.Errorf("record fired bond: %w", err)
	}
	return res.LastInsertId()
}

// checkBond refuses an unknown bond kind and a bond missing an end.
func checkBond(kind BondKind, from, to string) error {
	if !validBondKind(kind) {
		return fmt.Errorf("invalid bond kind %q", string(kind))
	}
	if from == "" || to == "" {
		return errors.New("bond requires from + to")
	}
	return nil
}

// bondWeight is a bond's weight, 1.0 when it is unset.
func bondWeight(weight float64) float64 {
	if weight == 0 {
		return 1.0
	}
	return weight
}

// marshalPayload is a bond's payload as JSON, "{}" when there is none.
func marshalPayload(payload map[string]any) (string, error) {
	if payload == nil {
		return "{}", nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	return string(b), nil
}

// ListByRun returns every bond declared in a given workflow run.
func (r *ForagerBondsRepo) ListByRun(runID int64) ([]*ForagerBondRow, error) {
	rows, err := r.readDB.Query(
		`SELECT id, run_id, from_forager, to_forager, bond_kind,
		        weight, fired, payload_json, observed_at
		 FROM forager_bonds WHERE run_id = ?
		 ORDER BY id`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("list bonds: %w", err)
	}
	defer rows.Close()
	return scanBondRows(rows)
}

// ListByPair returns every bond observed between two foragers across all
// runs. Used by the bond-inference primitive (future) to surface
// implicit relationships from observed behavior.
func (r *ForagerBondsRepo) ListByPair(from, to string) ([]*ForagerBondRow, error) {
	rows, err := r.readDB.Query(
		`SELECT id, run_id, from_forager, to_forager, bond_kind,
		        weight, fired, payload_json, observed_at
		 FROM forager_bonds WHERE from_forager = ? AND to_forager = ?
		 ORDER BY observed_at DESC, id DESC`,
		from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("list bonds by pair: %w", err)
	}
	defer rows.Close()
	return scanBondRows(rows)
}

// CountByKind returns map kind → count across all bonds.
func (r *ForagerBondsRepo) CountByKind() (map[BondKind]int, error) {
	rows, err := r.readDB.Query(
		`SELECT bond_kind, COUNT(*) FROM forager_bonds GROUP BY bond_kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[BondKind]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[BondKind(k)] = n
	}
	return out, rows.Err()
}

func scanBondRows(rows *sql.Rows) ([]*ForagerBondRow, error) {
	var out []*ForagerBondRow
	for rows.Next() {
		b, err := scanBondRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// scanBondRow reads the current forager_bonds row.
func scanBondRow(rows *sql.Rows) (*ForagerBondRow, error) {
	b := &ForagerBondRow{}
	var kind, payloadJSON string
	var firedInt int
	if err := rows.Scan(
		&b.ID, &b.RunID, &b.From, &b.To, &kind,
		&b.Weight, &firedInt, &payloadJSON, &b.ObservedAt,
	); err != nil {
		return nil, err
	}
	b.Kind = BondKind(kind)
	b.Fired = firedInt != 0
	b.Payload = decodeJSONObject(payloadJSON)
	return b, nil
}

// decodeJSONObject decodes a stored JSON object, best-effort: "" and "{}"
// give an empty map, as does text that does not decode; JSON null gives nil.
func decodeJSONObject(s string) map[string]any {
	m := map[string]any{}
	if s != "" && s != "{}" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	return m
}

func validBondKind(k BondKind) bool {
	switch k {
	case BondCites, BondContradicts, BondResonates:
		return true
	}
	return false
}
