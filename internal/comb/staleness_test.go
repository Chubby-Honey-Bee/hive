package comb

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func storeForTest(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.NewStore(filepath.Join(t.TempDir(), "comb.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// refreshRegion writes region's digest as `chb comb refresh --region` does.
func refreshRegion(t *testing.T, store *db.Store, region Coords) {
	t.Helper()
	d, err := BuildDigest(store, region)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteRegionVantage(context.Background(), store, d, "comb.refresh"); err != nil {
		t.Fatal(err)
	}
}

func TestClassify_Missing(t *testing.T) {
	store := storeForTest(t)
	got, err := Classify(store, Coords{"d1": 0})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != StaleMissing {
		t.Errorf("got %q; want missing", got)
	}
}

func TestClassify_FreshNoFindings(t *testing.T) {
	store := storeForTest(t)
	refreshRegion(t, store, Coords{"d1": 0})
	got, err := Classify(store, Coords{"d1": 0})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != StaleFresh {
		t.Errorf("got %q; want fresh", got)
	}
}

// A row a refresh would not write is stale, though no finding came after it:
// this one says 50% where the tables give 100%.
func TestClassify_StaleWhenTheRowIsNotTheRefreshs(t *testing.T) {
	store := storeForTest(t)
	if err := store.Comb().Upsert(&db.CombRow{
		VantageKey: "d1=0", VantageKind: db.VantageRegion, Confidence: 50,
	}); err != nil {
		t.Fatal(err)
	}
	d, err := BuildDigest(store, Coords{"d1": 0})
	if err != nil {
		t.Fatal(err)
	}
	if d.Confidence == 50 {
		t.Fatalf("precondition: a refresh would write confidence %d", d.Confidence)
	}
	got, err := Classify(store, Coords{"d1": 0})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != StaleStale {
		t.Errorf("got %q; want stale", got)
	}
}

func TestClassify_FreshWithOlderFindings(t *testing.T) {
	store := storeForTest(t)
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "test",
		D1: intPtr(0), D2: intPtr(0), D3: intPtr(0), D4: intPtr(0),
		MSSLabel: "assumption", Finding: "finding before comb revision",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteDB.Exec(
		"UPDATE findings SET created_at='2000-01-01 00:00:00'",
	); err != nil {
		t.Fatalf("backdate finding created_at: %v", err)
	}
	refreshRegion(t, store, Coords{"d1": 0})
	got, err := Classify(store, Coords{"d1": 0})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != StaleFresh {
		t.Errorf("got %q; want fresh (a refresh would write the same digest)", got)
	}
}

// A row that differs from its refresh in any one field of the digest is
// stale, though its other fields, its narrative among them, are the ones a
// refresh would write. The row is edited by hand, since a refresh writes each
// field from one tally.
func TestClassify_EachFieldOfTheDigest(t *testing.T) {
	store := storeForTest(t)
	for _, label := range []string{"guarantee", "assumption", "assumption"} {
		if _, err := store.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, mss_label, finding) VALUES (1, 'test', 1, ?, 'x')`, label); err != nil {
			t.Fatal(err)
		}
	}
	region := Coords{"d1": 1}
	for _, edit := range []string{
		`narrative = narrative || '.'`,
		`confidence = confidence - 1`,
		`contested = 1 - contested`,
		`dominant_label = 'guarantee'`,
		`evidence_count = evidence_count + 1`,
		`open_questions_count = open_questions_count + 1`,
	} {
		refreshRegion(t, store, region)
		if got, err := Classify(store, region); err != nil || got != StaleFresh {
			t.Fatalf("after a refresh: %q, %v; want fresh", got, err)
		}
		if _, err := store.WriteDB.Exec(`UPDATE comb_state SET ` + edit + ` WHERE vantage_key = 'd1=1'`); err != nil {
			t.Fatal(err)
		}
		if w := staleOracle(t, store, "d1=1"); len(w.differ) != 1 {
			t.Fatalf("%s: the row differs from its refresh in %v; want one field", edit, w.differ)
		}
		if got, err := Classify(store, region); err != nil || got != StaleStale {
			t.Errorf("%s: %q, %v; want stale", edit, got, err)
		}
	}
}

// A finding written in the second of the refresh, where no timestamp orders
// it after the digest, makes the region stale: a refresh would count it.
func TestClassify_StaleInTheSecondOfTheRefresh(t *testing.T) {
	store := storeForTest(t)
	refreshRegion(t, store, Coords{"d1": 0, "d2": 3})
	if _, err := store.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, mss_label, finding, created_at)
		VALUES (1, 'test', 0, 3, 'assumption', 'x', (SELECT last_revised_at FROM comb_state))`); err != nil {
		t.Fatal(err)
	}
	got, err := Classify(store, Coords{"d1": 0, "d2": 3})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != StaleStale {
		t.Errorf("got %q; want stale", got)
	}
}

// staleWhy is comb.md § Staleness for one comb_state row, by its definition:
// for a region key, each field on which the row a refresh would write
// differs from the stored one. Any other key is never stale.
type staleWhy struct {
	region bool
	differ []string
}

func (w staleWhy) stale() bool { return len(w.differ) > 0 }

func staleOracle(t *testing.T, store *db.Store, key string) staleWhy {
	t.Helper()
	row, err := store.Comb().Get(key)
	if err != nil || row == nil {
		t.Fatalf("%q: row %v, %v", key, row, err)
	}
	var w staleWhy
	region, err := ParseRegionKey(key)
	if err != nil {
		return w
	}
	w.region = true
	d, err := BuildDigest(store, region)
	if err != nil {
		t.Fatal(err)
	}
	fresh := digestToRow(d)
	for _, f := range []struct {
		name string
		same bool
	}{
		{"narrative", row.Narrative == fresh.Narrative},
		{"confidence", row.Confidence == fresh.Confidence},
		{"contested", row.Contested == fresh.Contested},
		{"dominant_label", row.DominantLabel == fresh.DominantLabel},
		{"evidence_count", row.EvidenceCount == fresh.EvidenceCount},
		{"open_questions_count", row.OpenQuestionsCount == fresh.OpenQuestionsCount},
	} {
		if !f.same {
			w.differ = append(w.differ, f.name)
		}
	}
	return w
}

// stalenessFixture is the store after a refresh and one change per clause,
// each under its own d1. stale names the keys a clause must make stale with
// no flag set, fresh those that must stay fresh, and only the fields on
// which a row must differ from its refresh.
type stalenessFixture struct {
	stale map[string][]string
	fresh map[string]string
	only  map[string]string
}

func seedStaleness(t *testing.T, store *db.Store) stalenessFixture {
	t.Helper()
	ctx := context.Background()
	add := func(label string, ds ...any) int64 {
		t.Helper()
		cols := []string{"d1", "d2", "d3", "d4", "d5"}
		q := `INSERT INTO findings (wave, agent, mss_label, finding`
		vals := `VALUES (1, 'test', ?, 'x'`
		args := []any{label}
		for i, d := range ds {
			if d == nil {
				continue
			}
			q += ", " + cols[i]
			vals += ", ?"
			args = append(args, d)
		}
		res, err := store.WriteDB.Exec(q+") "+vals+")", args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := store.WriteDB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	gap := func(priority string, d1 int, d2, d3 *int) int64 {
		t.Helper()
		if err := store.Gaps().AddGap(1, "test", "gap", priority, intPtr(d1), d2, d3, nil); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := store.ReadDB.QueryRow(`SELECT MAX(id) FROM gaps`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	conflict := func(a, b int64) int64 {
		t.Helper()
		if err := store.Conflicts().AddConflict(1, a, b, "disagree"); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := store.ReadDB.QueryRow(`SELECT MAX(id) FROM conflicts`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// Untouched, with an absent axis and a pin past d4.
	add("assumption", 0, 0)
	add("assumption", 0, nil, 5)
	add("assumption", 0, 0, nil, nil, 4)
	gap("important", 0, intPtr(0), nil)
	f1a, f1b := add("assumption", 1, 0), add("assumption", 1, 1)
	f2a, f2b := add("assumption", 2, 0), add("assumption", 2, 1)
	c2 := conflict(f2a, f2b)
	f3 := add("assumption", 3, 0)
	g3 := gap("critical", 3, intPtr(0), nil)
	add("assumption", 4, 0)
	add("assumption", 5, 0)
	f6 := add("assumption", 6, 0)
	add("assumption", 6, 1)
	d7 := add("assumption", 7, 0)
	g7 := add("assumption", 7, 1)
	exec(`UPDATE findings SET mss_label = 'guarantee', depends_on_ids = ? WHERE id = ?`, fmt.Sprintf("[%d]", d7), g7)
	f8 := add("assumption", 8, 0)
	f9 := add("assumption", 9, 0)
	add("assumption", 10, 0)
	// 201 findings: a second conflict moves confidence from 99.5 to 99.0,
	// both 99 once rounded down, so only the narrative's count shows it.
	exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 201)
		INSERT INTO findings (wave, agent, d1, d2, mss_label, finding) SELECT 1, 'test', 12, 0, 'assumption', 'x' FROM n`)
	var f12 []int64
	rows, err := store.ReadDB.Query(`SELECT id FROM findings WHERE d1 = 12 ORDER BY id LIMIT 4`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		f12 = append(f12, id)
	}
	rows.Close()
	conflict(f12[0], f12[1])
	gap("important", 13, nil, nil)
	// Beside the clauses, rows the batch must count as one region does: a
	// conflict across two regions, a conflict of a finding with itself, a
	// gap on an absent axis, a resolved gap and an answered followup.
	f15, f16 := add("definition", 15, 0), add("guarantee", 16, 0)
	exec(`UPDATE findings SET depends_on_ids = ? WHERE id = ?`, fmt.Sprintf("[%d]", f15), f16)
	add("unknown", 15, nil, 2)
	conflict(f15, f16)
	conflict(f15, f15)
	gap("minor", 15, nil, intPtr(2))
	if err := store.Gaps().ResolveGap(gap("critical", 16, nil, nil), 1, "test", f16); err != nil {
		t.Fatal(err)
	}
	if err := store.Followups().AddFollowup(1, "test", "q", "important", intPtr(16), intPtr(0), nil, nil); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE followups SET answered = 1`)

	if _, err := BuildAllRegions(ctx, store); err != nil {
		t.Fatal(err)
	}
	refreshRegion(t, store, Coords{"d1": 13})
	refreshRegion(t, store, Coords{"d1": 14})

	// The clauses, an hour after the refresh where a time is written.
	conflict(f1a, f1b)
	if err := store.Conflicts().Resolve(c2, 2, "b is right"); err != nil {
		t.Fatal(err)
	}
	if err := store.Gaps().ResolveGap(g3, 2, "test", f3); err != nil {
		t.Fatal(err)
	}
	gap("important", 4, intPtr(1), nil)
	if err := store.Followups().AddFollowup(2, "test", "q", "important", intPtr(5), intPtr(0), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Findings().UpdateFinding(f6, map[string]any{"mss_label": "definition"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CascadeRevert(d7); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM findings WHERE id = ?`, f8)
	exec(`INSERT INTO findings (wave, agent, d1, d2, mss_label, finding, created_at)
		VALUES (2, 'test', 8, 0, 'assumption', 'y', datetime('now', '+1 hour'))`)
	exec(`INSERT INTO capped_findings (finding_id, capped_at) VALUES (?, datetime('now', '+1 hour'))`, f9)
	add("assumption", 10, 0)
	conflict(f12[2], f12[3])
	exec(`UPDATE gaps SET priority = 'critical' WHERE d1 = 13`)
	if err := store.Followups().AddFollowup(2, "test", "q", "important", intPtr(14), nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	return stalenessFixture{
		stale: map[string][]string{
			"a conflict opened after the digest":   {"d1=1", "d1=1;d2=0", "d1=1;d2=1"},
			"a conflict resolved after the digest": {"d1=2", "d1=2;d2=0", "d1=2;d2=1"},
			"a critical gap resolved after it":     {"d1=3", "d1=3;d2=0"},
			"a gap opened after it":                {"d1=4"},
			"a followup opened after it":           {"d1=5", "d1=5;d2=0"},
			"a label changed after it":             {"d1=6", "d1=6;d2=0"},
			"a label reverted by the cascade":      {"d1=7", "d1=7;d2=1"},
			"a finding added after it":             {"d1=10", "d1=10;d2=0"},
			"a second conflict among 201 findings": {"d1=12"},
			"a followup opened, no findings":       {"d1=14"},
		},
		fresh: map[string]string{
			"d1=13":     "a gap raised to critical beside no findings: confidence is 0 either way",
			"d1=0":      "untouched",
			"d1=0;d3=5": "untouched, d2 absent",
			"d1=4;d2=0": "a gap opened beside it, at d2=1",
			"d1=6;d2=1": "a label changed beside it",
			"d1=7;d2=0": "a label reverted beside it",
			"d1=8":      "a finding replaced by one of the same label",
			"d1=8;d2=0": "a finding replaced by one of the same label",
			"d1=9":      "a cap",
			"d1=9;d2=0": "a cap",
			"d1=15":     "a conflict across regions, one with itself",
			"d1=16":     "a resolved gap, an answered followup",
		},
		only: map[string]string{
			"d1=12": "narrative",
			"d1=14": "open_questions_count",
		},
	}
}

// Every comb_state row is stale by one rule, whichever surface asks.
// StaleVantages, Classify and the token agree with the rule's definition,
// row by row: a region is stale when a refresh would write it a different
// digest. Each clause the fixture applies after the refresh makes its
// regions stale; a cap, a replaced finding and a change beside a region
// leave it fresh. A forager vantage is never stale, whatever its evidence
// count.
func TestStaleVantages_OneRuleForEveryRow(t *testing.T) {
	store := storeForTest(t)
	fx := seedStaleness(t, store)
	for _, key := range []string{"forager:optimist", "forager:skeptic"} {
		if err := store.Comb().Upsert(&db.CombRow{VantageKey: key, VantageKind: db.VantageForager, EvidenceCount: 3}); err != nil {
			t.Fatal(err)
		}
	}

	for clause, keys := range fx.stale {
		for _, key := range keys {
			if w := staleOracle(t, store, key); !w.stale() {
				t.Errorf("fixture: %s leaves %q %+v; want stale", clause, key, w)
			}
		}
	}
	for key, why := range fx.fresh {
		if w := staleOracle(t, store, key); w.stale() {
			t.Errorf("fixture: %q (%s) is %+v; want fresh", key, why, w)
		}
	}
	for key, field := range fx.only {
		if w := staleOracle(t, store, key); len(w.differ) != 1 || w.differ[0] != field {
			t.Errorf("fixture: %q differs from its refresh in %v; want %s alone", key, w.differ, field)
		}
	}

	got, err := StaleVantages(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	keys := combKeys(t, store)
	if len(got) != len(keys) {
		t.Errorf("StaleVantages holds %d keys; comb_state holds %d", len(got), len(keys))
	}
	seen := map[string]int{}
	for _, key := range keys {
		w := staleOracle(t, store, key)
		stale, ok := got[key]
		if !ok || stale != w.stale() {
			t.Errorf("%q: StaleVantages %v (present %v); want %v (%+v)", key, stale, ok, w.stale(), w)
		}
		if tok := Resolve(store, key); strings.HasSuffix(tok, " [stale]") != w.stale() {
			t.Errorf("%q: {comb.%s} = %q; want [stale] %v (%+v)", key, key, tok, w.stale(), w)
		}
		switch {
		case !w.region:
			seen["forager"]++
		case w.stale():
			seen["region by its refresh"]++
		default:
			seen["region fresh"]++
		}
		if !w.region {
			continue
		}
		want := StaleFresh
		if w.stale() {
			want = StaleStale
		}
		if c, err := Classify(store, mustRegion(t, key)); err != nil || c != want {
			t.Errorf("%q: Classify %q (%v); want %q (%+v)", key, c, err, want, w)
		}
	}
	for _, kind := range []string{"forager", "region by its refresh", "region fresh"} {
		if seen[kind] == 0 {
			t.Errorf("no row is %s; the fixture must hold one (%v)", kind, seen)
		}
	}
}

// A comb_state that carries a stale column, one the schema does not declare,
// set on every row changes no vantage's staleness: the rule reads digests,
// and a forager vantage is never stale.
func TestStaleVantages_ExtraStaleColumnChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale-column.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE comb_state (
	vantage_key TEXT PRIMARY KEY,
	vantage_kind TEXT NOT NULL DEFAULT 'region'
		CHECK(vantage_kind IN ('region','forager')),
	d1 INTEGER, d2 INTEGER, d3 INTEGER, d4 INTEGER,
	d5 INTEGER, d6 INTEGER, d7 INTEGER, d8 INTEGER,
	narrative TEXT NOT NULL DEFAULT '',
	confidence INTEGER CHECK(confidence BETWEEN 0 AND 100) NOT NULL DEFAULT 0,
	contested INTEGER NOT NULL DEFAULT 0,
	dominant_label TEXT,
	evidence_count INTEGER NOT NULL DEFAULT 0,
	open_questions_count INTEGER NOT NULL DEFAULT 0,
	digest_method TEXT NOT NULL DEFAULT 'heuristic',
	stale INTEGER NOT NULL DEFAULT 0,
	raw_json TEXT,
	last_revised_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, mss_label, finding) VALUES (1, 'test', 0, 'definition', 'x')`); err != nil {
		t.Fatal(err)
	}
	refreshRegion(t, store, Coords{"d1": 0})
	if err := store.Comb().Upsert(&db.CombRow{VantageKey: "forager:optimist", VantageKind: db.VantageForager, EvidenceCount: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteDB.Exec(`UPDATE comb_state SET stale = 1`); err != nil {
		t.Fatal(err)
	}
	got, err := StaleVantages(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["d1=0"] || got["forager:optimist"] {
		t.Errorf("StaleVantages = %v; want both vantages fresh", got)
	}
	if c, err := Classify(store, Coords{"d1": 0}); err != nil || c != StaleFresh {
		t.Errorf("Classify = %q, %v; want fresh", c, err)
	}
	if tok := Resolve(store, "d1=0"); tok == "" || strings.HasSuffix(tok, " [stale]") {
		t.Errorf("{comb.d1=0} = %q; want the line, not marked stale", tok)
	}
}

func combKeys(t *testing.T, store *db.Store) []string {
	t.Helper()
	var keys []string
	rows, err := store.ReadDB.Query(`SELECT vantage_key FROM comb_state`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	return keys
}

// {comb.<key>} resolves to the covering region's line, and marks it [stale]
// by the rule: a key with no row of its own takes its covering region's
// staleness, and a region stale by a new conflict alone reads [stale].
func TestResolve_StaleByTheRule(t *testing.T) {
	store := storeForTest(t)
	seedStaleness(t, store)
	for _, c := range []struct{ key, covering string }{
		{"d1=1;d2=0;d3=9", "d1=1;d2=0"},
		{"d1=0;d2=0;d3=9", "d1=0;d2=0"},
	} {
		row, err := store.Comb().Get(c.covering)
		if err != nil || row == nil {
			t.Fatalf("%q: %v %v", c.covering, row, err)
		}
		w := staleOracle(t, store, c.covering)
		want := row.Narrative
		if w.stale() {
			want += " [stale]"
		}
		if got := Resolve(store, c.key); got != want {
			t.Errorf("{comb.%s} = %q; want %q (%+v)", c.key, got, want, w)
		}
	}
	if w := staleOracle(t, store, "d1=1;d2=0"); !w.stale() {
		t.Errorf("fixture: d1=1;d2=0 is %+v; want stale by its refresh", w)
	}
}

// The staleness read is one read transaction of at most six queries, each
// closed before the next, so a connection that allows one open query serves
// every row.
func TestStaleVantages_OneConnection(t *testing.T) {
	store := storeForTest(t)
	seedStaleness(t, store)
	want, err := StaleVantages(store.ReadDB)
	if err != nil {
		t.Fatal(err)
	}
	store.ReadDB.SetMaxOpenConns(1)
	type result struct {
		stale map[string]bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		got, err := StaleVantages(store.ReadDB)
		done <- result{got, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if fmt.Sprint(r.stale) != fmt.Sprint(want) {
			t.Errorf("on one connection %v; want %v", r.stale, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StaleVantages waits for a second connection")
	}
}

// The rule reads the rows and the tables in one read transaction, so a
// write that commits between its queries cannot mix two database states. A
// writer flips one finding's label and, in the same transaction, writes the
// digests a refresh gives each region for that label. Every state it commits
// is fresh in every region, so no read may call one stale. Read outside a
// transaction, the rows of one state met the findings of the next.
func TestStaleVantages_OneDatabaseState(t *testing.T) {
	store := storeForTest(t)
	flip, err := store.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, mss_label, finding) VALUES (1, 'test', 0, 0, 'assumption', 'x')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := flip.LastInsertId()
	if _, err := store.WriteDB.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 400)
		INSERT INTO findings (wave, agent, d1, d2, d3, mss_label, finding)
		SELECT 1, 'test', 1 + i % 9, i % 7, i % 3, 'assumption', 'x' FROM n`); err != nil {
		t.Fatal(err)
	}
	type digest struct {
		narrative           string
		confidence          int
		contested           bool
		dominant            sql.NullString
		evidence, questions int
	}
	labels := []string{"assumption", "definition"}
	states := make([]map[string]digest, len(labels))
	for i, label := range labels {
		if _, err := store.WriteDB.Exec(`UPDATE findings SET mss_label = ? WHERE id = ?`, label, id); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildAllRegions(context.Background(), store); err != nil {
			t.Fatal(err)
		}
		rows, err := store.Comb().List()
		if err != nil {
			t.Fatal(err)
		}
		states[i] = map[string]digest{}
		for _, r := range rows {
			states[i][r.VantageKey] = digest{r.Narrative, r.Confidence, r.Contested, r.DominantLabel, r.EvidenceCount, r.OpenQuestionsCount}
		}
	}
	var moved []string
	for key, d := range states[0] {
		if d != states[1][key] {
			moved = append(moved, key)
		}
	}
	if len(moved) == 0 {
		t.Fatal("fixture: the label moves no digest")
	}

	stop := make(chan struct{})
	wrote := make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				wrote <- nil
				return
			default:
			}
			s := i % 2
			tx, err := store.WriteDB.Begin()
			if err != nil {
				wrote <- err
				return
			}
			if _, err := tx.Exec(`UPDATE findings SET mss_label = ? WHERE id = ?`, labels[s], id); err != nil {
				tx.Rollback()
				wrote <- err
				return
			}
			for _, key := range moved {
				d := states[s][key]
				if _, err := tx.Exec(`UPDATE comb_state SET narrative = ?, confidence = ?, contested = ?, dominant_label = ?,
					evidence_count = ?, open_questions_count = ? WHERE vantage_key = ?`,
					d.narrative, d.confidence, d.contested, d.dominant, d.evidence, d.questions, key); err != nil {
					tx.Rollback()
					wrote <- err
					return
				}
			}
			if err := tx.Commit(); err != nil {
				wrote <- err
				return
			}
		}
	}()
	var failure string
	for i := 0; i < 100 && failure == ""; i++ {
		stale, err := StaleVantages(store.ReadDB)
		if err != nil {
			failure = err.Error()
		}
		for key, s := range stale {
			if s {
				failure = fmt.Sprintf("read %d: %q stale in a state every region of which is fresh", i, key)
			}
		}
	}
	close(stop)
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	if failure != "" {
		t.Fatal(failure)
	}
}
