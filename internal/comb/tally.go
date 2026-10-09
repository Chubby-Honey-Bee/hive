package comb

import (
	"database/sql"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// regionTally is what a region's digest is computed from: its findings and
// their labels, the unresolved conflicts that touch it, and its open gaps and
// unanswered followups.
type regionTally struct {
	findings     int
	labels       map[string]int
	conflicts    int
	criticalGaps int
	openGaps     int
	followups    int
}

// regionTallies reads the tally of each region in regions, keyed by its
// pins, in four queries whatever the number of regions: the findings grouped
// by cell and label, the unresolved conflicts with the cells of their two
// findings, and the open gaps and unanswered followups grouped by d1..d4. It
// closes each query before the next. Every region must lie within within,
// pinning each axis within pins to the same value; the queries read only the
// rows within it. A row adds to each region it lies in: for each set of axes
// some region pins, the one region holding the row's values on those axes. So
// the work is rows times sets of axes, not rows times regions.
//
// A finding is in a region when it holds each value the region pins. A
// conflict touches a region when either of its findings is in it, and counts
// once. Gaps and followups carry only d1..d4, so a pin past d4 does not
// narrow them.
func regionTallies(q db.Conn, within Coords, regions []Coords) (map[pins]*regionTally, error) {
	t := newTallier(regions)
	for _, read := range []func(db.Conn, Coords) error{t.readFindings, t.readConflicts, t.readGaps, t.readFollowups} {
		if err := read(q, within); err != nil {
			return nil, err
		}
	}
	t.settleQuestions()
	return t.tallies, nil
}

// questions is the open gaps, the critical ones among them, and the
// unanswered followups of a region's pins on d1..d4.
type questions struct{ critical, gaps, followups int }

// tallier accumulates the tallies regionTallies reads: one per region, keyed
// by its pins, and the questions of each region's pins on d1..d4.
type tallier struct {
	dims    []string
	tallies map[pins]*regionTally
	asked   map[pins]*questions
	masks   []uint8 // each set of axes some region pins, once
	qmasks  []uint8 // each set of axes among d1..d4 some region pins, once
}

func newTallier(regions []Coords) *tallier {
	t := &tallier{
		dims:    orderedDims(),
		tallies: make(map[pins]*regionTally, len(regions)),
		asked:   map[pins]*questions{},
	}
	seen, qseen := map[uint8]bool{}, map[uint8]bool{}
	for _, r := range regions {
		p := pinsOf(r, t.dims)
		t.tallies[p] = &regionTally{labels: map[string]int{}}
		t.masks = appendMask(t.masks, seen, p.mask)
		g := p.questionPins()
		t.asked[g] = &questions{}
		t.qmasks = appendMask(t.qmasks, qseen, g.mask)
	}
	return t
}

// appendMask appends mask to masks unless seen holds it, and marks it seen.
func appendMask(masks []uint8, seen map[uint8]bool, mask uint8) []uint8 {
	if seen[mask] {
		return masks
	}
	seen[mask] = true
	return append(masks, mask)
}

// readFindings adds the findings within within, grouped by cell and label, to
// each region they lie in.
func (t *tallier) readFindings(q db.Conn, within Coords) error {
	pin, args := pinClause("", within, t.dims)
	return queryEach(q, "findings", `SELECT d1, d2, d3, d4, d5, d6, d7, d8, mss_label, COUNT(*)
		FROM findings WHERE 1=1`+pin+` GROUP BY d1, d2, d3, d4, d5, d6, d7, d8, mss_label`, args, func(rows *sql.Rows) error {
		var ds [8]sql.NullInt64
		var label string
		var n int
		if err := rows.Scan(&ds[0], &ds[1], &ds[2], &ds[3], &ds[4], &ds[5], &ds[6], &ds[7], &label, &n); err != nil {
			return err
		}
		t.addFindings(pinsOfRow(ds[:]), label, n)
		return nil
	})
}

// addFindings adds n findings labelled label at cell to each region holding
// the cell.
func (t *tallier) addFindings(cell pins, label string, n int) {
	for _, m := range t.masks {
		if r := t.tallyIn(cell, m); r != nil {
			r.findings += n
			r.labels[label] += n
		}
	}
}

// readConflicts counts each unresolved conflict with a finding within within
// in the regions its findings lie in.
func (t *tallier) readConflicts(q db.Conn, within Coords) error {
	touch, args := conflictTouchClause(within, t.dims)
	return queryEach(q, "conflicts", `SELECT a.id IS NOT NULL, a.d1, a.d2, a.d3, a.d4, a.d5, a.d6, a.d7, a.d8,
		       b.id IS NOT NULL, b.d1, b.d2, b.d3, b.d4, b.d5, b.d6, b.d7, b.d8
		FROM conflicts c
		LEFT JOIN findings a ON a.id = c.finding_a_id
		LEFT JOIN findings b ON b.id = c.finding_b_id
		WHERE c.resolution IS NULL`+touch, args, func(rows *sql.Rows) error {
		var a, b conflictEnd
		var ca, cb [8]sql.NullInt64
		if err := rows.Scan(&a.exists, &ca[0], &ca[1], &ca[2], &ca[3], &ca[4], &ca[5], &ca[6], &ca[7],
			&b.exists, &cb[0], &cb[1], &cb[2], &cb[3], &cb[4], &cb[5], &cb[6], &cb[7]); err != nil {
			return err
		}
		a.cell, b.cell = pinsOfRow(ca[:]), pinsOfRow(cb[:])
		for _, m := range t.masks {
			t.addConflict(m, a, b)
		}
		return nil
	})
}

// conflictTouchClause narrows the conflicts to those with a finding within
// within, with its arguments; it is empty when within pins no axis.
func conflictTouchClause(within Coords, dims []string) (string, []any) {
	pinA, argsA := pinClause("a.", within, dims)
	pinB, argsB := pinClause("b.", within, dims)
	args := append(argsA, argsB...)
	if pinA == "" {
		return "", args
	}
	return " AND ((1=1" + pinA + ") OR (1=1" + pinB + "))", args
}

// conflictEnd is one finding of a conflict: its cell, and whether the
// finding exists.
type conflictEnd struct {
	exists bool
	cell   pins
}

// addConflict counts a conflict in each region with axes mask that holds one
// of its findings, once when both findings lie in the same region.
func (t *tallier) addConflict(mask uint8, a, b conflictEnd) {
	ra, rb := t.conflictTally(a, mask), t.conflictTally(b, mask)
	if ra != nil {
		ra.conflicts++
	}
	if rb != nil && rb != ra {
		rb.conflicts++
	}
}

// conflictTally is the tally of the region with axes mask that holds the
// finding e, or nil when the finding does not exist or no region holds it.
func (t *tallier) conflictTally(e conflictEnd, mask uint8) *regionTally {
	if !e.exists {
		return nil
	}
	return t.tallyIn(e.cell, mask)
}

// readGaps adds the open gaps within within, grouped by d1..d4 and priority,
// to the questions of each region's pins on d1..d4 that hold them.
func (t *tallier) readGaps(q db.Conn, within Coords) error {
	pin, args := pinClause("", within, t.dims[:4])
	return queryEach(q, "gaps", `SELECT d1, d2, d3, d4, priority, COUNT(*) FROM gaps
		WHERE resolved_by_wave IS NULL`+pin+` GROUP BY d1, d2, d3, d4, priority`, args, func(rows *sql.Rows) error {
		var ds [4]sql.NullInt64
		var priority string
		var n int
		if err := rows.Scan(&ds[0], &ds[1], &ds[2], &ds[3], &priority, &n); err != nil {
			return err
		}
		t.eachAsked(pinsOfRow(ds[:]), func(a *questions) {
			a.gaps += n
			if priority == "critical" {
				a.critical += n
			}
		})
		return nil
	})
}

// readFollowups adds the unanswered followups within within, grouped by
// d1..d4, to the questions of each region's pins on d1..d4 that hold them.
func (t *tallier) readFollowups(q db.Conn, within Coords) error {
	pin, args := pinClause("", within, t.dims[:4])
	return queryEach(q, "followups", `SELECT d1, d2, d3, d4, COUNT(*) FROM followups
		WHERE answered = 0`+pin+` GROUP BY d1, d2, d3, d4`, args, func(rows *sql.Rows) error {
		var ds [4]sql.NullInt64
		var n int
		if err := rows.Scan(&ds[0], &ds[1], &ds[2], &ds[3], &n); err != nil {
			return err
		}
		t.eachAsked(pinsOfRow(ds[:]), func(a *questions) { a.followups += n })
		return nil
	})
}

// tallyIn is the tally of the region with axes mask that holds cell, or nil
// when no tallied region does.
func (t *tallier) tallyIn(cell pins, mask uint8) *regionTally {
	k, ok := cell.onto(mask)
	if !ok {
		return nil
	}
	return t.tallies[k]
}

// eachAsked calls add with the questions of each region's pins on d1..d4
// that hold cell.
func (t *tallier) eachAsked(cell pins, add func(*questions)) {
	for _, m := range t.qmasks {
		k, ok := cell.onto(m)
		if a := t.asked[k]; ok && a != nil {
			add(a)
		}
	}
}

// settleQuestions copies into each region's tally the questions of its pins
// on d1..d4.
func (t *tallier) settleQuestions() {
	for p, r := range t.tallies {
		a := t.asked[p.questionPins()]
		r.criticalGaps, r.openGaps, r.followups = a.critical, a.gaps, a.followups
	}
}

// pins is a set of axes among d1..d8 with a value on each, as a map key: bit
// i of mask is set when axis i of orderedDims holds v[i]. A value on an axis
// outside mask is 0.
type pins struct {
	mask uint8
	v    [8]int
}

func pinsOf(c Coords, dims []string) pins {
	var p pins
	for i, d := range dims {
		if v, ok := c[d]; ok {
			p.mask |= 1 << i
			p.v[i] = v
		}
	}
	return p
}

// onto is the region with axes mask that holds p, by the rule a region's
// evidence count uses: p's value on each of those axes. ok is false when p
// leaves one of them absent, so no such region holds it.
func (p pins) onto(mask uint8) (pins, bool) {
	if p.mask&mask != mask {
		return pins{}, false
	}
	out := pins{mask: mask}
	for i := range p.v {
		if mask&(1<<i) != 0 {
			out.v[i] = p.v[i]
		}
	}
	return out, true
}

// questionPins is p's pins on d1..d4, the axes gaps and followups carry.
func (p pins) questionPins() pins {
	g, _ := p.onto(p.mask & 0x0F)
	return g
}

// pinClause is " AND <prefix>dN = ?" for each axis among dims that region
// pins, with its values.
func pinClause(prefix string, region Coords, dims []string) (string, []any) {
	var clause string
	var args []any
	for _, d := range dims {
		if v, ok := region[d]; ok {
			clause += " AND " + prefix + d + " = ?"
			args = append(args, v)
		}
	}
	return clause, args
}

// pinsOfRow is the pins of a row's d1.. columns, in order: each non-NULL one.
func pinsOfRow(ds []sql.NullInt64) pins {
	var p pins
	for i, d := range ds {
		if d.Valid {
			p.mask |= 1 << i
			p.v[i] = int(d.Int64)
		}
	}
	return p
}
