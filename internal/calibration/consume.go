package calibration

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// CorrelationalNote goes with every weight a surface shows.
const CorrelationalNote = "Calibration is correlational, not proof of skill: a weight says how often a predictor's claims held, not why."

// Scores is a set of calibration rows by key: the table as it stands, or
// the revisions current at a tick or a moment. Every consumer reads
// through it, and every consumer treats an uncalibrated row as absent.
type Scores map[db.ScoreKey]*db.ScoreRow

// ScoresFrom indexes rows by key.
func ScoresFrom(rows []*db.ScoreRow) Scores {
	s := make(Scores, len(rows))
	for _, r := range rows {
		s[r.ScoreKey] = r
	}
	return s
}

// ScopesFor lists a subject's scopes, most specific first: `d1=x;d2=y`
// when both coordinates are set, `d1=x` when the first is, then the global
// scope.
func ScopesFor(d1, d2 sql.NullInt64) []string {
	var scopes []string
	if d1.Valid {
		first := fmt.Sprintf("d1=%d", d1.Int64)
		if d2.Valid {
			scopes = append(scopes, fmt.Sprintf("%s;d2=%d", first, d2.Int64))
		}
		scopes = append(scopes, first)
	}
	return append(scopes, ScopeGlobal)
}

// LabelScore is the calibrated `label` score that governs a subject: the
// row for its label in the first of its scopes, most specific first, that
// holds a calibrated one. nil when no scope does, so a label with fewer
// than NFloor outcomes everywhere governs nothing.
func (s Scores) LabelScore(label string, scopes []string) *db.ScoreRow {
	for _, scope := range scopes {
		if r := s[db.ScoreKey{PredictorKind: KindLabel, PredictorKey: label, ScopeKey: scope}]; r != nil && r.Calibrated {
			return r
		}
	}
	return nil
}

// CalibratedConfidence rescales a stored confidence by its dominant label's
// calibrated hit rate against the label's target, comb.md § Calibrated
// confidence: clamp(confidence × hit_rate / target, 0, 100), rounded to
// the nearest whole percent. ok is false, and nothing is reported, when
// score is nil or uncalibrated or the label has no target.
func CalibratedConfidence(confidence int, label string, score *db.ScoreRow) (int, bool) {
	// A label with no target reads as target 0.
	target := LabelTargets[label]
	if score == nil || !score.Calibrated || target <= 0 {
		return 0, false
	}
	v := math.Round(float64(confidence) * score.HitRate / target)
	return int(math.Min(100, math.Max(0, v))), true
}

// LensTrackRecord renders `{calibration.lenses}` for the lenses of one
// swarm from their global `lens` scores: the empty string when none is
// calibrated, so the Queen's prompt is byte-identical to one without the
// token; otherwise a paragraph that opens with the instruction and the
// correlational note, then one line per lens in name order, with the
// weight, hit rate and n of a calibrated lens and `uncalibrated` for one
// under the floor or with no outcome.
func LensTrackRecord(s Scores, lenses []string) string {
	names := append([]string(nil), lenses...)
	sort.Strings(names)
	calibrated := 0
	lines := make([]string, 0, len(names))
	for _, name := range names {
		line, ok := s.lensLine(name)
		if ok {
			calibrated++
		}
		lines = append(lines, line)
	}
	if calibrated == 0 {
		return ""
	}
	return "\n\nLens track records, from the outcomes ledger. " + CorrelationalNote +
		" Weight each lens's agreement in Consensus by its weight; an uncalibrated lens weighs 1:\n" +
		strings.Join(lines, "\n")
}

// lensLine is lens name's line in the track record, from its global `lens`
// score, and whether that score is calibrated.
func (s Scores) lensLine(name string) (string, bool) {
	r := s[db.ScoreKey{PredictorKind: KindLens, PredictorKey: name, ScopeKey: ScopeGlobal}]
	switch {
	case r == nil:
		return fmt.Sprintf("  %s: uncalibrated (no outcomes)", name), false
	case !r.Calibrated:
		return fmt.Sprintf("  %s: uncalibrated (n=%d < %d)", name, r.NResolved, NFloor), false
	}
	return fmt.Sprintf("  %s: weight %.2f (hit rate %.2f, n=%d)", name, r.Weight, r.HitRate, r.NResolved), true
}

// ScoreView is a score as every JSON surface prints it: `chb calibrate`,
// `chb db-read calibration` and `chb_calibration_read`.
type ScoreView struct {
	PredictorKind string   `json:"predictor_kind"`
	PredictorKey  string   `json:"predictor_key"`
	ScopeKey      string   `json:"scope_key"`
	NResolved     int      `json:"n_resolved"`
	NConfirmed    int      `json:"n_confirmed"`
	NRefuted      int      `json:"n_refuted"`
	NPartial      int      `json:"n_partial"`
	HitRate       float64  `json:"hit_rate"`
	BrierScore    *float64 `json:"brier_score"`
	Weight        float64  `json:"weight"`
	Calibrated    bool     `json:"calibrated"`
	UpdatedTickID *int64   `json:"updated_tick_id"`
}

// View is one row's ScoreView. A revision's UpdatedTickID is its tick.
func View(r *db.ScoreRow) ScoreView {
	v := ScoreView{
		PredictorKind: r.PredictorKind, PredictorKey: r.PredictorKey, ScopeKey: r.ScopeKey,
		NResolved: r.NResolved, NConfirmed: r.NConfirmed, NRefuted: r.NRefuted, NPartial: r.NPartial,
		HitRate: r.HitRate, Weight: r.Weight, Calibrated: r.Calibrated,
	}
	if r.BrierScore.Valid {
		b := r.BrierScore.Float64
		v.BrierScore = &b
	}
	if r.UpdatedTickID.Valid {
		t := r.UpdatedTickID.Int64
		v.UpdatedTickID = &t
	}
	return v
}

// Views is every row's ScoreView, never nil.
func Views(rows []*db.ScoreRow) []ScoreView {
	out := make([]ScoreView, 0, len(rows))
	for _, r := range rows {
		out = append(out, View(r))
	}
	return out
}

// validKind reports whether kind names a predictor kind.
func validKind(kind string) bool {
	switch kind {
	case KindLens, KindLabel, KindConvergence, KindSynthesizer:
		return true
	}
	return false
}
