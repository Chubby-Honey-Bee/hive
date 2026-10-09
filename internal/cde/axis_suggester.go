package cde

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// AxisCandidate is a non-coordinate attribute that the data suggests
// should become a CDE axis: it distinguishes findings *within* the same
// coordinate cell, which is the foundations.md heuristic for "you're
// encoding by-attribute, not by-coordinate — claim an axis."
type AxisCandidate struct {
	Attribute    string // e.g. "mss_label"
	SplitCells   int    // coordinate cells where the attribute takes ≥2 values
	TotalCells   int    // populated coordinate cells considered
	Evidence     int    // findings living in the split cells
	ProposedSlot string // next free d-slot, e.g. "d6", or "" if all 8 used
}

// SuggestAxes analyses the findings table for axis candidates. It is a
// bounded probe: one GROUP BY over (d1..d5, mss_label). The signal is
// data-side (does an attribute distinguish findings the coordinates
// can't?), complementary to the WASP scan detector's query-side signal.
//
// Honest scope: it reads whatever findings exist in the workspace. A cold
// `chb ask` workspace with no findings yields no candidates; a workspace
// layered on a research/swarm run yields real ones. It takes a raw
// *sql.DB (the read pool) — the cde package is intentionally db-free.
//
// minEvidence is the minimum findings in a coordinate cell for it to
// count (filters noise from sparse cells).
func SuggestAxes(conn *sql.DB, minEvidence int) ([]AxisCandidate, error) {
	cells, err := labelCounts(conn)
	if err != nil {
		return nil, err
	}
	split := splitByLabel(cells, evidenceFloor(minEvidence))
	if split.splitCells == 0 {
		return nil, nil
	}
	return labelCandidate(conn, split)
}

// evidenceFloor is minEvidence, or 2 when it is below 1.
func evidenceFloor(minEvidence int) int {
	if minEvidence < 1 {
		return 2
	}
	return minEvidence
}

// cell is a coordinate cell: d1..d5, an absent axis read as -1.
type cell [5]int64

// labelCounts reads how many findings each cell holds of each label.
func labelCounts(conn *sql.DB) (map[cell]map[string]int, error) {
	rows, err := conn.Query(`
		SELECT COALESCE(d1,-1), COALESCE(d2,-1), COALESCE(d3,-1), COALESCE(d4,-1), COALESCE(d5,-1),
		       mss_label, COUNT(*)
		FROM findings
		GROUP BY d1, d2, d3, d4, d5, mss_label`)
	if err != nil {
		return nil, fmt.Errorf("axis-suggester query: %w", err)
	}
	defer rows.Close()
	return scanLabelCounts(rows)
}

// scanLabelCounts reads each (cell, label, count) row of rows: cell → label
// → count.
func scanLabelCounts(rows *sql.Rows) (map[cell]map[string]int, error) {
	cells := map[cell]map[string]int{}
	for rows.Next() {
		if err := addLabelCount(rows, cells); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cells, nil
}

// addLabelCount scans one (cell, label, count) row from rows into cells.
func addLabelCount(rows *sql.Rows, cells map[cell]map[string]int) error {
	var c cell
	var label string
	var n int
	if err := rows.Scan(&c[0], &c[1], &c[2], &c[3], &c[4], &label, &n); err != nil {
		return fmt.Errorf("axis-suggester scan: %w", err)
	}
	if cells[c] == nil {
		cells[c] = map[string]int{}
	}
	cells[c][label] += n
	return nil
}

// labelSplit is how far mss_label splits the cells: the cells that clear
// the evidence floor, the split ones among them, and the findings in those.
type labelSplit struct {
	totalCells, splitCells, evidence int
}

// splitByLabel counts the cells mss_label splits. A cell is "split by
// mss_label" when ≥2 labels share one coordinate AND the cell clears the
// evidence floor — the label is doing the distinguishing work an axis
// should do.
func splitByLabel(cells map[cell]map[string]int, minEvidence int) labelSplit {
	var s labelSplit
	for _, labels := range cells {
		s.add(labels, minEvidence)
	}
	return s
}

// add counts one cell, by the findings it holds of each label.
func (s *labelSplit) add(labels map[string]int, minEvidence int) {
	total := 0
	for _, n := range labels {
		total += n
	}
	if total < minEvidence {
		return
	}
	s.totalCells++
	if len(labels) >= 2 {
		s.splitCells++
		s.evidence += total
	}
}

// labelCandidate is mss_label as the axis candidate split describes, with
// the slot it would take.
func labelCandidate(conn *sql.DB, split labelSplit) ([]AxisCandidate, error) {
	slot, err := nextFreeSlot(conn)
	if err != nil {
		return nil, err
	}
	return []AxisCandidate{{
		Attribute:    "mss_label",
		SplitCells:   split.splitCells,
		TotalCells:   split.totalCells,
		Evidence:     split.evidence,
		ProposedSlot: slot,
	}}, nil
}

// nextFreeSlot returns the next unused CDE dimension slot ("d<N+1>")
// given the count of registered dimensions, or "" when all 8 are used.
func nextFreeSlot(conn *sql.DB) (string, error) {
	var registered int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM dimensions`).Scan(&registered); err != nil {
		// No dimensions table / empty workspace: the first registration
		// lands at d1, positionally.
		return "d1", nil //nolint:nilerr — absence is a clean default, not a failure
	}
	// Slots are positional: ValidateCoords walks the dimensions table in id
	// order and maps row N onto dN. So the next registration lands at
	// registered+1 and nowhere else.
	next := registered + 1
	if next > MaxDimensions {
		return "", nil
	}
	return fmt.Sprintf("d%d", next), nil
}

// FormatAxisCandidates renders candidates as a compact evidence block for
// the framer's prompt. Returns an honest "none" line when empty
// so the forager never has to invent evidence.
func FormatAxisCandidates(cands []AxisCandidate) string {
	if len(cands) == 0 {
		return "(no axis candidates — no findings in this workspace cluster by a non-coordinate attribute yet)"
	}
	var b strings.Builder
	sort.Slice(cands, func(i, j int) bool { return cands[i].SplitCells > cands[j].SplitCells })
	for _, c := range cands {
		slot := c.ProposedSlot
		if slot == "" {
			slot = "(all 8 slots used)"
		}
		fmt.Fprintf(&b, "- `%s` distinguishes findings within %d/%d coordinate cell(s) (%d findings) — encoding by-attribute, not by-coordinate. Candidate axis → propose slot %s.\n",
			c.Attribute, c.SplitCells, c.TotalCells, c.Evidence, slot)
	}
	return strings.TrimRight(b.String(), "\n")
}
