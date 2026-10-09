package comb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// BuildForagerVantage writes a forager-vantage row to the Comb directly from
// a swarm run. The runner calls it *before* completing the node — so a
// downstream `cites:` forager never finds the row missing — and without
// regard to the accept: gate; a repair rewrites it.
//
//	ctx         caller's context — propagated to the Comb event-bus
//	            publish so a cancelled run stops emitting events.
//	foragerName  the forager's frontmatter `name` (e.g. "optimist")
//	verdict     parsed verdict JSON ({"verdict":..., "key_points":[...], ...})
//
// confidenceFromVerdict maps the verdict string to a confidence:
// support=80, oppose=20, conditional=50, abstain=30. dominant_label
// stays empty for forager vantages (MSS labels apply to findings, not
// verdicts). Best-effort: errors are returned but the runner only
// logs them — forager-vantage writes are never allowed to fail a node.
func BuildForagerVantage(ctx context.Context, store *db.Store, runID int64, foragerName string, rawJSON string, verdict map[string]any) error {
	key := db.ForagerVantageKey(foragerName)
	row := foragerRow(key, rawJSON, verdict)
	if err := store.Comb().Upsert(row); err != nil {
		return fmt.Errorf("comb forager upsert %s: %w", key, err)
	}
	// Append revision (chronomantic).
	rev := rowToRevision(row)
	rev.TickID = currentTickID(store, runID)
	// Not atomic with the upsert above, so a failed append is returned: a
	// head row with no history row leaves `comb at` and `comb history`
	// without that revision.
	if _, err := store.CombRevisions().Append(rev, "forager:"+foragerName); err != nil {
		return fmt.Errorf("comb revision %s: %w", key, err)
	}
	// Publish to the in-process Comb event bus. Non-blocking;
	// the subscriber (the quorum sensor) gets the
	// event or the bus drops it for slow consumers. The caller's ctx
	// is observed so a cancelled run stops emitting.
	Default.Publish(ctx, Event{
		Kind:        EventVantageWritten,
		VantageKey:  key,
		VantageKind: string(db.VantageForager),
		RunID:       runID,
		Payload: map[string]any{
			"forager":    foragerName,
			"verdict":    verdict["verdict"],
			"confidence": row.Confidence,
		},
	})
	return nil
}

// foragerRow is the comb_state row of a forager's verdict at key.
func foragerRow(key, rawJSON string, verdict map[string]any) *db.CombRow {
	verdictStr, _ := verdict["verdict"].(string)
	row := &db.CombRow{
		VantageKey:         key,
		VantageKind:        db.VantageForager,
		Narrative:          foragerNarrative(verdict, verdictStr),
		Confidence:         confidenceFromVerdict(verdictStr),
		Contested:          verdictStr == "" || verdictStr == "conditional",
		EvidenceCount:      countList(verdict, "evidence"),
		OpenQuestionsCount: countList(verdict, "uncertainties"),
		DigestMethod:       "forager",
	}
	if rawJSON != "" {
		row.RawJSON = sql.NullString{String: rawJSON, Valid: true}
	}
	return row
}

// foragerNarrative is the verdict's recommendation. A verdict that supplies
// none falls back to a structural summary, so downstream foragers always see
// something rendered for {comb.forager:<name>}.
func foragerNarrative(verdict map[string]any, verdictStr string) string {
	if rec, _ := verdict["recommendation"].(string); rec != "" {
		return rec
	}
	return fmt.Sprintf("verdict=%s (no recommendation supplied)", verdictStr)
}

// verdictConfidence is the confidence each forager verdict maps to.
var verdictConfidence = map[string]int{"support": 80, "oppose": 20, "conditional": 50, "abstain": 30}

func confidenceFromVerdict(v string) int {
	if c, ok := verdictConfidence[v]; ok {
		return c
	}
	return 50
}

func countList(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	if arr, ok := v.([]any); ok {
		return len(arr)
	}
	if arr, ok := v.([]string); ok {
		return len(arr)
	}
	return 0
}

// WriteRegionVantage is the single way a region vantage reaches the Comb:
// the state row, the immutable revision row that is the chronomantic
// history, and the event that tells the bus's subscribers a refresh
// happened, so every region write, `comb refresh --region` included, leaves
// history and publishes.
//
// It writes d as digestToRow converts it, the row the staleness rule
// compares with, so a region it refreshes reads fresh.
func WriteRegionVantage(ctx context.Context, store *db.Store, d *Digest, source string) error {
	row := digestToRow(d)
	if err := store.Comb().Upsert(row); err != nil {
		return fmt.Errorf("upsert digest %q: %w", row.VantageKey, err)
	}
	// Not atomic with the upsert above, so a failed append is returned: a
	// head row with no history row leaves `comb at` and `comb history`
	// without that revision.
	rev := rowToRevision(row)
	rev.TickID = currentTickID(store, 0)
	if _, err := store.CombRevisions().Append(rev, source); err != nil {
		return fmt.Errorf("comb revision %q: %w", row.VantageKey, err)
	}

	Default.Publish(ctx, Event{
		Kind:        EventVantageWritten,
		VantageKey:  row.VantageKey,
		VantageKind: string(row.VantageKind),
		Payload: map[string]any{
			"confidence": row.Confidence,
			"contested":  row.Contested,
			"source":     source,
		},
	})
	return nil
}

// digestToRow converts the in-memory Digest to an CombRow that can be
// passed to CombRepo.Upsert.
func digestToRow(d *Digest) *db.CombRow {
	row := &db.CombRow{
		VantageKey:         d.VantageKey,
		VantageKind:        db.VantageRegion,
		Narrative:          d.Narrative,
		Confidence:         d.Confidence,
		Contested:          d.Contested,
		EvidenceCount:      d.EvidenceCount,
		OpenQuestionsCount: d.OpenQuestionsCount,
		DigestMethod:       d.DigestMethod,
	}
	if d.DominantLabel != "" {
		row.DominantLabel = sql.NullString{String: d.DominantLabel, Valid: true}
	}
	dims := d.Coords.AsNullCoords()
	row.D1, row.D2, row.D3, row.D4 = dims[0], dims[1], dims[2], dims[3]
	row.D5, row.D6, row.D7, row.D8 = dims[4], dims[5], dims[6], dims[7]
	return row
}

// currentTickID returns the open Time Wheel tick a revision anchors to —
// the run's own tick when runID is set — or NULL when none is open.
// Best-effort: an error means the revision is anchored by timestamp alone,
// which is what every revision did before ticks had a producer.
func currentTickID(store *db.Store, runID int64) sql.NullInt64 {
	t, err := store.TimeWheel().CurrentOpen(runID)
	if err != nil || t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.ID, Valid: true}
}

// rowToRevision converts a CombRow into a CombRevisionRow for the
// chronomantic history append. tick_id is left null; callers set it.
func rowToRevision(row *db.CombRow) *db.CombRevisionRow {
	return &db.CombRevisionRow{
		VantageKey:         row.VantageKey,
		VantageKind:        row.VantageKind,
		Narrative:          row.Narrative,
		Confidence:         row.Confidence,
		Contested:          row.Contested,
		DominantLabel:      row.DominantLabel,
		EvidenceCount:      row.EvidenceCount,
		OpenQuestionsCount: row.OpenQuestionsCount,
		RawJSON:            row.RawJSON,
	}
}
