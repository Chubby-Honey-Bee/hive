package dreamer

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// passHypothesize promotes long-open critical/important gaps into
// followups so the next dispatch wave has a concrete question. A gap is
// "long-open" if its created_at is older than opts.GapAgeMin (default
// 7 days) and resolved_by_wave IS NULL.
//
// Idempotent: a followup is only created if no followup with the same
// d1..d4 + question text exists yet.
func passHypothesize(ctx context.Context, store *db.Store, opts Options) (Result, error) {
	// SQLite's own text form, in UTC. created_at is CURRENT_TIMESTAMP
	// ("2006-01-02 15:04:05", UTC) compared as text; an RFC3339 cutoff in
	// local time sorted its 'T' after the space, so gaps younger than
	// --gap-age-days on the cutoff date were promoted.
	cutoff := opts.Now.UTC().Add(-opts.GapAgeMin).Format("2006-01-02 15:04:05")
	stale, err := readStaleGaps(ctx, store.ReadConn(), cutoff)
	if err != nil {
		return Result{Status: "failed"}, err
	}
	if len(stale) == 0 {
		return Result{
			Status: "completed",
			Notes:  map[string]any{"stale_gaps": 0, "cutoff": cutoff},
		}, nil
	}
	h := &hypothesizer{store: store, opts: opts, cutoff: cutoff, stale: len(stale)}
	return h.walk(ctx, stale)
}

// gapRow is one long-open gap hypothesize reads.
type gapRow struct {
	id             int64
	wave           int
	agent          string
	description    string
	priority       string
	d1, d2, d3, d4 sql.NullInt64
	createdAt      string
}

// readStaleGaps reads the open critical and important gaps created before
// cutoff, oldest first.
func readStaleGaps(ctx context.Context, read *sql.DB, cutoff string) ([]gapRow, error) {
	rows, err := read.QueryContext(ctx, `
		SELECT id, wave, agent, description, priority, d1, d2, d3, d4, created_at
		FROM gaps
		WHERE resolved_by_wave IS NULL
		  AND priority IN ('critical','important')
		  AND created_at < ?
		ORDER BY created_at ASC`, cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("scan gaps: %w", err)
	}
	defer rows.Close()
	stale, err := collectRows(rows, scanGap)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan gaps: %w", err)
	}
	return stale, nil
}

func scanGap(rows *sql.Rows) (gapRow, error) {
	var g gapRow
	err := rows.Scan(&g.id, &g.wave, &g.agent, &g.description, &g.priority,
		&g.d1, &g.d2, &g.d3, &g.d4, &g.createdAt)
	return g, err
}

// hypothesizer is one hypothesize pass over the stale gaps: what it read,
// and the followups it has created so far.
type hypothesizer struct {
	store   *db.Store
	opts    Options
	cutoff  string
	stale   int
	emitted int
}

// walk visits the stale gaps in turn while the pass may create another
// followup, and reports the pass. The cap counts followups created, not gaps
// read: as a LIMIT on the query, once the oldest MaxPerPass gaps had
// followups, every run would read only those, skip them all and never reach
// a newer stale gap.
func (h *hypothesizer) walk(ctx context.Context, stale []gapRow) (Result, error) {
	for _, g := range stale {
		ok, err := admit(ctx, h.emitted, h.opts.MaxPerPass)
		if !ok {
			return h.report(err)
		}
		if err := h.visit(g); err != nil {
			return Result{Status: "failed"}, err
		}
	}
	return h.report(nil)
}

// visit creates a followup for gap g, unless the run is dry or one exists
// already: dedup is against the same d1..d4 and the question derived from
// the gap's description.
func (h *hypothesizer) visit(g gapRow) error {
	question := "Resolve open gap: " + g.description
	exists, err := followupExists(h.store.ReadConn(), question, nullInt(g.d1), nullInt(g.d2), nullInt(g.d3), nullInt(g.d4))
	if err != nil || exists {
		return err
	}
	if h.opts.DryRun {
		h.emitted++
		return nil
	}
	return h.addFollowup(g, question)
}

// addFollowup writes gap g's followup asking question.
func (h *hypothesizer) addFollowup(g gapRow, question string) error {
	if err := h.store.Followups().AddFollowup(
		g.wave, "dreamer.hypothesize",
		question, g.priority,
		nullInt(g.d1), nullInt(g.d2), nullInt(g.d3), nullInt(g.d4),
	); err != nil {
		return fmt.Errorf("add followup: %w", err)
	}
	h.emitted++
	return nil
}

// report is the pass's outcome once its walk stops: failed with the
// followups already created when cut is the deadline's error, else
// complete.
func (h *hypothesizer) report(cut error) (Result, error) {
	if cut != nil {
		return Result{Status: "failed", Touched: h.emitted}, cut
	}
	return Result{
		Status:  passStatus(h.opts),
		Touched: h.emitted,
		Notes: map[string]any{
			"stale_gaps": h.stale,
			"cutoff":     h.cutoff,
			"emitted":    h.emitted,
		},
	}, nil
}

func followupExists(read *sql.DB, question string, d1, d2, d3, d4 *int) (bool, error) {
	q := `SELECT 1 FROM followups WHERE question = ?`
	args := []any{question}
	for i, v := range []*int{d1, d2, d3, d4} {
		var clause string
		clause, args = axisClause(dimName(i+1), v, args)
		q += clause
	}
	return existsRow(read, q+" LIMIT 1", args...)
}

// axisClause matches axis name to v: " AND <name> IS NULL" when v is nil,
// else " AND <name> = ?" with v appended to args.
func axisClause(name string, v *int, args []any) (string, []any) {
	if v == nil {
		return " AND " + name + " IS NULL", args
	}
	return " AND " + name + " = ?", append(args, *v)
}
