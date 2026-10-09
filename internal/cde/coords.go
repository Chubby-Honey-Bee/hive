// Package cde enforces the CDE coordinate rules against the dimension
// registry — every active dimension holds a coordinate, within its domain —
// and suggests a new axis when an attribute splits findings the
// coordinates cannot.
package cde

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// MaxDimensions is the maximum number of CDE dimensions supported.
const MaxDimensions = 8

// Sentinel errors for CDE violations.
var (
	ErrNullCoordinate = errors.New("CDE null coordinate in active dimension")
	ErrOutOfBounds    = errors.New("CDE coordinate exceeds dimension bound")
)

// CDEError wraps a CDE sentinel with dimension-specific context.
type CDEError struct {
	Dimension int // 1-indexed (d1=1, d2=2, ...)
	DimName   string
	Value     *int
	MaxBound  int
	Reason    string
	Err       error // sentinel
}

// Error names the dimension and the reason.
func (e *CDEError) Error() string {
	return fmt.Sprintf("CDE error (d%d %s): %s", e.Dimension, e.DimName, e.Reason)
}

// Unwrap returns the sentinel, so errors.Is matches the violation.
func (e *CDEError) Unwrap() error { return e.Err }

// Coords holds coordinate values for d1..d8.
type Coords [MaxDimensions]*int

// ValidateCoords checks that all active dimensions (registered in the dimensions table)
// have non-NULL coordinates and that values are within bounds.
//
// Lean4 status, stated honestly — this is the project that refuses to launder
// an unknown into a guarantee, so its own citations have to hold:
//
//	W9 null_breaks_sufficiency — PROVED by `decide`, kernel-checked, on the
//	   constructed counterexample: a NULL d3 makes a finding invisible to a
//	   probe on d3.
//	W8 null_invisible          — PROVED, kernel-checked: a finding whose
//	   coordinate on an active dimension is none matches no probe
//	   coordinate (`coordMatch` is false). X3 `wasp_needs_cde_nonnull`
//	   builds on it: an incomplete encoding leaves some finding in no
//	   bucket at all.
//	C6                         — `RegistryBound` is a definition, not a
//	   theorem. `cde_within_registry` (C7) restates its own hypothesis.
//
// So: W8 and W9 say why a NULL on an active dimension must be refused, and
// the null check below refuses it; the bounds check enforces C6's
// definition. Both are enforcement this code performs, and that the Lean
// model matches this code is read from the code, not proved.
func ValidateCoords(db *sql.DB, coords Coords) error {
	rows, err := db.Query("SELECT id, name, values_json FROM dimensions ORDER BY id")
	if err != nil {
		return fmt.Errorf("query dimensions: %w", err)
	}
	defer rows.Close()
	return checkRegistered(rows, coords)
}

// checkRegistered checks coords against each dimension in rows, in id
// order: the N-th row is dN.
func checkRegistered(rows *sql.Rows, coords Coords) error {
	for dimIdx := 0; rows.Next(); dimIdx++ {
		if err := checkDimension(rows, dimIdx, coords); err != nil {
			return err
		}
	}
	// A truncated read validates fewer dimensions than are registered, so a
	// NULL coordinate on an unread dimension is accepted — the finding then
	// becomes invisible to probes, which is precisely what this check exists
	// to prevent.
	if err := rows.Err(); err != nil {
		return fmt.Errorf("dimension registry read did not complete, so coordinates are unvalidated: %w", err)
	}
	return nil
}

// checkDimension checks coords against the dimension rows is on, the one
// at index dimIdx.
func checkDimension(rows *sql.Rows, dimIdx int, coords Coords) error {
	// A registry wider than the coordinate space cannot be validated:
	// dimensions past the eighth have no slot in Coords, so a finding
	// would be accepted while invisible to any probe on them.
	if dimIdx >= MaxDimensions {
		return fmt.Errorf("dimension registry holds more than %d dimensions, so coordinates past d%d cannot be validated or probed", MaxDimensions, MaxDimensions)
	}
	var id int
	var name, valuesJSON string
	if err := rows.Scan(&id, &name, &valuesJSON); err != nil {
		return fmt.Errorf("scan dimension: %w", err)
	}
	return checkCoordinate(dimIdx, name, valuesJSON, coords[dimIdx])
}

// checkCoordinate checks v, the coordinate at index dimIdx, against the
// dimension named name, whose domain is valuesJSON.
func checkCoordinate(dimIdx int, name, valuesJSON string, v *int) error {
	// W8/W9: null check on active dimension
	if v == nil {
		return &CDEError{
			Dimension: dimIdx + 1,
			DimName:   name,
			Reason:    fmt.Sprintf("NULL coordinate for active dimension d%d (%s). WASP sufficiency requires all active dimensions to be non-NULL.", dimIdx+1, name),
			Err:       ErrNullCoordinate,
		}
	}

	// C6: bounds check. A values_json that will not parse leaves the
	// dimension with no domain, so the coordinate is unbounded rather
	// than in bounds — refuse it instead of waving it through.
	var values []string
	if err := json.Unmarshal([]byte(valuesJSON), &values); err != nil {
		return &CDEError{
			Dimension: dimIdx + 1,
			DimName:   name,
			Value:     v,
			Reason:    fmt.Sprintf("dimension '%s' has an unreadable domain, so d%d cannot be bounded: %v", name, dimIdx+1, err),
			Err:       ErrOutOfBounds,
		}
	}
	return checkBounds(dimIdx, name, values, v)
}

// checkBounds refuses v, the coordinate at index dimIdx, when it is not an
// index into values, the domain of the dimension named name.
func checkBounds(dimIdx int, name string, values []string, v *int) error {
	if *v >= len(values) || *v < 0 {
		return &CDEError{
			Dimension: dimIdx + 1,
			DimName:   name,
			Value:     v,
			MaxBound:  len(values) - 1,
			Reason:    fmt.Sprintf("d%d=%d exceeds dimension '%s' bound (max=%d)", dimIdx+1, *v, name, len(values)-1),
			Err:       ErrOutOfBounds,
		}
	}
	return nil
}
