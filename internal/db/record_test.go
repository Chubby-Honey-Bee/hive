package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
)

// The gap kind writes through GapsRepo's insert, the one AddGap makes, so a
// gap it cannot write is refused with AddGap's error.
func TestWriteRecord_GapGoesThroughAddGap(t *testing.T) {
	s := newTestStore(t)
	const priority = "urgent" // kept by gapPriority, refused by the gaps table's CHECK
	want := s.Gaps().AddGap(1, "a", "g", priority, nil, nil, nil, nil)
	if want == nil {
		t.Fatalf("AddGap accepted priority %q; the test needs a gap it refuses", priority)
	}
	_, _, err := s.WriteRecord("gap", map[string]any{"wave": 1.0, "agent": "a", "description": "g", "priority": priority})
	if err == nil || err.Error() != want.Error() {
		t.Errorf("the gap kind refused priority %q with %v; want AddGap's error %q", priority, err, want)
	}
}

// WriteRecord refuses a call with no fields and a kind it does not write.
func TestWriteRecord_RefusesNoFieldsAndAnUnknownKind(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.WriteRecord("finding", nil); err == nil || !strings.Contains(err.Error(), "fields missing") {
		t.Errorf("no fields: %v; want fields missing", err)
	}
	for _, kind := range []string{"bogus", ""} {
		if _, _, err := s.WriteRecord(kind, map[string]any{"x": 1}); err == nil || !strings.Contains(err.Error(), "unknown kind") {
			t.Errorf("kind %q: %v; want unknown kind", kind, err)
		}
	}
}

// WriteRecord returns the row it wrote or closed and the line chb_db_write
// replies with.
func TestWriteRecord_ReportsWhatItWrote(t *testing.T) {
	s := newTestStore(t)
	write := func(kind string, fields map[string]any, wantID int64, wantLine string) {
		t.Helper()
		id, line, err := s.WriteRecord(kind, fields)
		if err != nil {
			t.Fatalf("%s %v: %v", kind, fields, err)
		}
		if id != wantID || line != wantLine {
			t.Errorf("%s: id %d, line %q; want %d, %q", kind, id, line, wantID, wantLine)
		}
	}
	write("finding", map[string]any{"wave": 1.0, "agent": "a", "mss_label": "definition", "finding": "a"}, 1, "finding id=1")
	write("finding", map[string]any{"wave": 1.0, "agent": "a", "mss_label": "definition", "finding": "not a"}, 2, "finding id=2")
	write("gap", map[string]any{"wave": 1.0, "agent": "a", "description": "is it a?"}, 1, "gap id=1")
	write("source", map[string]any{"url": "https://example.test/s", "agent": "a"}, 1, "source id=1 url=https://example.test/s")
	write("resolve_gap", map[string]any{"gap_id": 1.0, "wave": 2.0, "agent": "a", "finding_id": 1.0}, 1, "gap 1 resolved by finding 1")
	if err := s.Conflicts().AddConflict(1, 1, 2, "a vs not a"); err != nil {
		t.Fatal(err)
	}
	write("resolve_conflict", map[string]any{"conflict_id": 1.0, "wave": 2.0, "resolution": "finding 1 survives"}, 1, "conflict 1 resolved")
}

// A finding's optional fields are read when given, one with no mss_label is
// an assumption, and AddFinding's refusals stand.
func TestWriteRecord_Finding(t *testing.T) {
	tests := []struct {
		name      string
		fields    map[string]any
		wantErr   bool
		wantLabel string
	}{
		{"minimal fields", map[string]any{"wave": 1.0, "agent": "a", "mss_label": "definition", "finding": "price is $89"}, false, "definition"},
		{"every optional field", map[string]any{
			"wave": 2.0, "agent": "a", "mss_label": "assumption", "finding": "return rate is 22%",
			"evidence": "industry survey", "source_urls": "https://example.com/survey",
			"d1": 1.0, "d2": 2.0, "d3": 3.0, "d4": 0.0, "d5": 0.0,
		}, false, "assumption"},
		{"no mss_label", map[string]any{"wave": 1.0, "agent": "a", "finding": "unlabelled"}, false, "assumption"},
		{"an invalid mss_label", map[string]any{"wave": 1.0, "agent": "a", "mss_label": "INVALID_LABEL_XYZ", "finding": "x"}, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			id, _, err := s.WriteRecord("finding", tc.fields)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted finding %d; want it refused", id)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var label string
			if err := s.ReadDB.QueryRow(`SELECT mss_label FROM findings WHERE id = ?`, id).Scan(&label); err != nil {
				t.Fatal(err)
			}
			if label != tc.wantLabel {
				t.Errorf("stored label %q; want %q", label, tc.wantLabel)
			}
		})
	}
}

// A gap's priority is one the gaps table allows: high and none are
// important, medium and low minor, and the three allowed values pass
// through.
func TestWriteRecord_GapPriority(t *testing.T) {
	for priority, want := range map[string]string{
		"critical": "critical", "important": "important", "minor": "minor",
		"high": "important", "medium": "minor", "low": "minor", "": "important",
	} {
		t.Run("priority="+priority, func(t *testing.T) {
			s := newTestStore(t)
			id, _, err := s.WriteRecord("gap", map[string]any{"wave": 1.0, "agent": "a", "description": "g", "priority": priority})
			if err != nil {
				t.Fatal(err)
			}
			var stored string
			if err := s.ReadDB.QueryRow(`SELECT priority FROM gaps WHERE id = ?`, id).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != want {
				t.Errorf("priority %q stored as %q; want %q", priority, stored, want)
			}
		})
	}

	s := newTestStore(t)
	s.WriteDB.Close()
	if _, _, err := s.WriteRecord("gap", map[string]any{"wave": 1.0, "agent": "a", "description": "g", "priority": "critical"}); err == nil {
		t.Error("a gap was written through a closed pool")
	}
}

// A source is written with or without a wave, and a URL already on record is
// accepted again.
func TestWriteRecord_Source(t *testing.T) {
	s := newTestStore(t)
	for _, fields := range []map[string]any{
		{"url": "https://example.com/article", "title": "Example Article", "agent": "researcher-1", "wave": 1.0, "contribution": "background context"},
		{"url": "https://example.com/no-wave", "title": "No Wave Article", "agent": "researcher-2", "contribution": "additional context"},
		{"url": "https://example.com/article", "title": "Updated Title", "agent": "researcher-1", "wave": 2.0, "contribution": "new context"},
	} {
		if _, line, err := s.WriteRecord("source", fields); err != nil || !strings.HasPrefix(line, "source id=") {
			t.Errorf("source %v: line %q, err %v", fields, line, err)
		}
	}
}

// Re-citing a URL keeps the stored record, its title included when the new
// write has none, and reports that record's id, not LastInsertId, which
// names another table's row after an update.
func TestWriteRecord_SourceRewriteKeepsRecord(t *testing.T) {
	s := newTestStore(t)
	const url = "https://example.com/cited-twice"
	_, first, err := s.WriteRecord("source", map[string]any{"url": url, "title": "Kept", "agent": "a"})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"g1", "g2"} {
		if _, _, err := s.WriteRecord("gap", map[string]any{"wave": 1.0, "agent": "a", "description": d}); err != nil {
			t.Fatal(err)
		}
	}
	_, again, err := s.WriteRecord("source", map[string]any{"url": url, "agent": "b"})
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Errorf("rewrite reported %q, want the original record %q", again, first)
	}
	var title string
	if err := s.ReadDB.QueryRow(`SELECT COALESCE(title,'') FROM sources WHERE url=?`, url).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Kept" {
		t.Errorf("title = %q after an untitled rewrite, want %q", title, "Kept")
	}

	const untitled = "https://example.com/untitled-first"
	if _, _, err := s.WriteRecord("source", map[string]any{"url": untitled, "agent": "a"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.WriteRecord("source", map[string]any{"url": untitled, "title": "Filled", "agent": "b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT COALESCE(title,'') FROM sources WHERE url=?`, untitled).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Filled" {
		t.Errorf("missing title not filled: got %q", title)
	}
}

// Re-citing a URL fills a missing wave and keeps one on record. A row
// `chb validate-sources` wrote without --wave has none, and so has a source
// written without one.
func TestWriteRecord_SourceFillsMissingWave(t *testing.T) {
	s := newTestStore(t)
	const validated, cited, unwaved = "https://example.com/validated-first", "https://example.com/cited-first", "https://example.com/no-wave-first"
	if _, err := s.WriteDB.Exec(`INSERT INTO sources (url, validation_status) VALUES (?, 'dead')`, validated); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.WriteRecord("source", map[string]any{"url": unwaved, "agent": "a"}); err != nil {
		t.Fatal(err)
	}
	first, later := 2.0, 3.0
	for _, u := range []string{validated, cited, unwaved} {
		if _, _, err := s.WriteRecord("source", map[string]any{"url": u, "agent": "a", "wave": first}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.WriteRecord("source", map[string]any{"url": cited, "agent": "a", "wave": later}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{validated, cited, unwaved} {
		var wave int // -1 for no wave
		if err := s.ReadDB.QueryRow(`SELECT COALESCE(wave, -1) FROM sources WHERE url=?`, u).Scan(&wave); err != nil {
			t.Fatal(err)
		}
		if wave != int(first) {
			t.Errorf("%s: wave = %d; want %d", u, wave, int(first))
		}
	}
}

// Every kind refuses a field it does not read, naming the field and listing
// the fields it takes, and writes nothing, so an agent's "agent_id" for
// "agent" cannot write a finding with no agent and report success. Each
// case's fields are every field its kind reads, so they must be accepted,
// and the list the refusal gives must be exactly those fields. Fields match
// exactly, case included.
func TestWriteRecord_RefusesAnUnknownField(t *testing.T) {
	cases := []struct {
		kind, table string
		full        map[string]any
	}{
		{"finding", "findings", map[string]any{
			"wave": 1.0, "agent": "a", "mss_label": "guarantee", "finding": "f", "evidence": "e",
			"source_urls": []any{"https://example.test/f"}, "depends_on_ids": []any{2.0},
			"d1": 0.0, "d2": 0.0, "d3": 0.0, "d4": 0.0, "d5": 0.0, "d6": 0.0, "d7": 0.0, "d8": 0.0,
		}},
		{"gap", "gaps", map[string]any{
			"wave": 1.0, "agent": "a", "description": "g", "priority": "critical",
			"d1": 0.0, "d2": 0.0, "d3": 0.0, "d4": 0.0,
		}},
		{"source", "sources", map[string]any{
			"url": "https://example.test/s", "title": "t", "wave": 1.0, "agent": "a", "contribution": "c", "primary_source": 1.0,
		}},
		{"resolve_gap", "gaps", map[string]any{"gap_id": 1.0, "wave": 1.0, "agent": "a", "finding_id": 1.0}},
		{"resolve_conflict", "conflicts", map[string]any{"conflict_id": 1.0, "wave": 1.0, "resolution": "r"}},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			want := make([]string, 0, len(c.full))
			for k := range c.full {
				want = append(want, k)
			}
			slices.Sort(want)

			for _, extra := range [][]string{{"agent_id"}, {"status", "agent_id"}, {strings.ToUpper(want[0])}} {
				s := seedRecordRows(t)
				fields := make(map[string]any, len(c.full)+len(extra))
				for k, v := range c.full {
					fields[k] = v
				}
				for _, k := range extra {
					fields[k] = 1.0
				}
				before := recordTableRows(t, s, c.table)
				_, _, err := s.WriteRecord(c.kind, fields)
				if err == nil {
					t.Fatalf("%s with %v: accepted; want the unknown fields refused", c.kind, extra)
				}
				msg := err.Error()
				for _, k := range extra {
					if !strings.Contains(msg, fmt.Sprintf("%q", k)) {
						t.Errorf("%s: error %q does not name the unknown field %q", c.kind, msg, k)
					}
				}
				_, list, ok := strings.Cut(msg, "accepted fields: ")
				if !ok {
					t.Fatalf("%s: error %q lists no accepted fields", c.kind, msg)
				}
				got := strings.Split(list, ", ")
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Errorf("%s: accepted fields %v; want the fields it reads, %v", c.kind, got, want)
				}
				if after := recordTableRows(t, s, c.table); after != before {
					t.Errorf("%s wrote to %s although it refused the fields:\nbefore %s\nafter  %s", c.kind, c.table, before, after)
				}
			}

			if _, _, err := seedRecordRows(t).WriteRecord(c.kind, c.full); err != nil {
				t.Errorf("%s %v: %v; want every field it reads accepted", c.kind, c.full, err)
			}
		})
	}
}

// seedRecordRows gives a fresh store assumption finding 1, definition
// finding 2, an open gap 1 and an open conflict 1 between the two findings.
func seedRecordRows(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	var ids []int64
	for _, label := range []string{"assumption", "definition"} {
		id, err := s.Findings().AddFinding(&Finding{Wave: 1, Agent: "a", MSSLabel: label, Finding: label + " finding"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := s.Gaps().AddGap(1, "a", "open gap", "important", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Conflicts().AddConflict(1, ids[0], ids[1], "open conflict"); err != nil {
		t.Fatal(err)
	}
	var gap, conflict int64
	if err := s.ReadDB.QueryRow(`SELECT (SELECT MAX(id) FROM gaps), (SELECT MAX(id) FROM conflicts)`).Scan(&gap, &conflict); err != nil {
		t.Fatal(err)
	}
	if ids[0] != 1 || ids[1] != 2 || gap != 1 || conflict != 1 {
		t.Fatalf("seeded findings %v, gap %d and conflict %d; the fields assume 1, 2, 1 and 1", ids, gap, conflict)
	}
	return s
}

// recordTableRows renders every row of table, in rowid order.
func recordTableRows(t *testing.T, s *Store, table string) string {
	t.Helper()
	rows, err := s.ReadDB.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%v\n", vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The finding, gap and source kinds checked only that a number was a
// number. A fractional wave or coordinate was truncated, which files the
// record somewhere else, and one past the range of int was saturated or
// wrapped, as Go leaves to the platform. They refuse what the resolve kinds
// refuse.
func TestWriteRecord_RecordKindsRefuseAnythingButAWholeInt(t *testing.T) {
	kinds := []struct {
		kind, table string
		base        map[string]any
		keys        []string
	}{
		{"finding", "findings", map[string]any{"wave": 1.0, "agent": "a", "mss_label": "definition", "finding": "f"},
			[]string{"wave", "d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"}},
		{"gap", "gaps", map[string]any{"wave": 1.0, "agent": "a", "description": "g", "priority": "minor"},
			[]string{"wave", "d1", "d2", "d3", "d4"}},
		{"source", "sources", map[string]any{"url": "https://example.com/s", "agent": "a"},
			[]string{"wave", "primary_source"}},
	}
	refused := map[string]float64{"fractional": 2.5, "past int": pastMaxInt, "far past int": 1e19, "below int": -1e19}
	with := func(base map[string]any, set map[string]any) map[string]any {
		f := map[string]any{}
		for k, v := range base {
			f[k] = v
		}
		for k, v := range set {
			f[k] = v
		}
		return f
	}
	s := newTestStore(t)
	for _, k := range kinds {
		for _, key := range k.keys {
			for name, v := range refused {
				if _, _, err := s.WriteRecord(k.kind, with(k.base, map[string]any{key: v})); err == nil {
					t.Errorf("%s with a %s %s (%v) was accepted", k.kind, name, key, v)
				}
			}
		}
		var rows int
		if err := s.ReadDB.QueryRow("SELECT COUNT(*) FROM " + k.table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Errorf("%s: refused writes left %d rows; want 0", k.kind, rows)
		}

		// The largest float64 below the edge is a whole int, so it is
		// written, and stored as exactly that int.
		all := map[string]any{}
		for _, key := range k.keys {
			all[key] = lastInt
		}
		if _, _, err := s.WriteRecord(k.kind, with(k.base, all)); err != nil {
			t.Fatalf("%s with every field at %v: %v", k.kind, lastInt, err)
		}
		for _, key := range k.keys {
			var got int64
			if err := s.ReadDB.QueryRow("SELECT " + key + " FROM " + k.table + " ORDER BY id DESC LIMIT 1").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if want := int64(lastInt); got != want {
				t.Errorf("%s %s stored as %d; want %d", k.kind, key, got, want)
			}
		}
	}
}

// pastMaxInt is the smallest float64 past math.MaxInt. int(pastMaxInt) is
// not defined by Go: it saturated on arm64 and wrapped on amd64.
var pastMaxInt = -float64(math.MinInt)

// lastInt is the largest float64 below pastMaxInt, a whole int that every
// numeric field takes.
var lastInt = math.Nextafter(pastMaxInt, 0)

// resolve_gap closes a gap only through GapsRepo.ResolveGap, refusing a
// missing, textual, fractional or out-of-range id or wave, a missing agent,
// and a gap or finding that does not exist.
func TestWriteRecord_ResolveGap(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.WriteRecord("finding", map[string]any{"wave": 1.0, "agent": "a", "mss_label": "definition", "finding": "answer"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.WriteRecord("gap", map[string]any{"wave": 1.0, "agent": "a", "description": "question", "priority": "important"}); err != nil {
		t.Fatal(err)
	}
	var findingID, gapID float64
	if err := s.ReadDB.QueryRow("SELECT (SELECT id FROM findings), (SELECT id FROM gaps)").Scan(&findingID, &gapID); err != nil {
		t.Fatal(err)
	}
	ok := map[string]any{"gap_id": gapID, "wave": lastInt, "agent": "hive-scout", "finding_id": findingID}
	with := func(k string, v any) map[string]any {
		f := map[string]any{}
		for kk, vv := range ok {
			f[kk] = vv
		}
		if v == nil {
			delete(f, k)
		} else {
			f[k] = v
		}
		return f
	}
	refused := map[string]map[string]any{
		"no gap_id":          with("gap_id", nil),
		"no finding_id":      with("finding_id", nil),
		"text gap_id":        with("gap_id", fmt.Sprint(gapID)),
		"fractional gap_id":  with("gap_id", gapID+0.5),
		"no agent":           with("agent", nil),
		"unknown finding":    with("finding_id", findingID+1),
		"unknown gap":        with("gap_id", gapID+1),
		"fractional wave":    with("wave", 2.5),
		"negative finding":   with("finding_id", -findingID),
		"text finding_id":    with("finding_id", "1"),
		"fractional finding": with("finding_id", findingID+0.5),
		"wave past int":      with("wave", pastMaxInt),
		"wave far past int":  with("wave", 1e19),
		"wave below int":     with("wave", -1e19),
		"gap_id past int":    with("gap_id", pastMaxInt),
	}
	open := func() bool {
		var resolved sql.NullInt64
		if err := s.ReadDB.QueryRow("SELECT resolved_by_wave FROM gaps WHERE id = ?", int64(gapID)).Scan(&resolved); err != nil {
			t.Fatal(err)
		}
		return !resolved.Valid
	}
	for name, f := range refused {
		if _, _, err := s.WriteRecord("resolve_gap", f); err == nil {
			t.Errorf("%s: %v was accepted", name, f)
		}
		if !open() {
			t.Fatalf("%s: the gap was closed by a refused write", name)
		}
	}

	if _, _, err := s.WriteRecord("resolve_gap", ok); err != nil {
		t.Fatalf("resolve_gap %v: %v", ok, err)
	}
	var wave int
	var agent string
	var by int64
	if err := s.ReadDB.QueryRow("SELECT resolved_by_wave, resolved_by_agent, resolution_finding_id FROM gaps WHERE id = ?", int64(gapID)).Scan(&wave, &agent, &by); err != nil {
		t.Fatal(err)
	}
	if wave != int(ok["wave"].(float64)) || agent != ok["agent"] || by != int64(findingID) {
		t.Errorf("gap resolved as wave=%d agent=%q finding=%d, want %v", wave, agent, by, ok)
	}
	if _, _, err := s.WriteRecord("resolve_gap", ok); err == nil || !strings.Contains(err.Error(), "already resolved") {
		t.Errorf("resolving twice: got %v, want already resolved", err)
	}
}

// resolve_conflict closes a conflict only through ConflictsRepo.Resolve,
// refusing a missing, textual, fractional, non-positive or out-of-range id,
// a wave out of range, an empty resolution and an unknown conflict.
func TestWriteRecord_ResolveConflict(t *testing.T) {
	s := newTestStore(t)
	for _, text := range []string{"a", "not a"} {
		if _, _, err := s.WriteRecord("finding", map[string]any{"wave": 1.0, "agent": "a", "mss_label": "definition", "finding": text}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Conflicts().AddConflict(1, 1, 2, "a vs not a"); err != nil {
		t.Fatal(err)
	}
	var conflictID float64
	if err := s.ReadDB.QueryRow("SELECT id FROM conflicts").Scan(&conflictID); err != nil {
		t.Fatal(err)
	}
	ok := map[string]any{"conflict_id": conflictID, "wave": lastInt, "resolution": "finding 1 survives: sourced"}
	refused := map[string]map[string]any{
		"no conflict_id":        {"wave": 2.0, "resolution": "r"},
		"text conflict_id":      {"conflict_id": "1", "wave": 2.0, "resolution": "r"},
		"fractional id":         {"conflict_id": conflictID + 0.5, "wave": 2.0, "resolution": "r"},
		"empty resolution":      {"conflict_id": conflictID, "wave": 2.0, "resolution": " "},
		"unknown conflict":      {"conflict_id": conflictID + 1, "wave": 2.0, "resolution": "r"},
		"non-positive conflict": {"conflict_id": 0.0, "wave": 2.0, "resolution": "r"},
		"wave past int":         {"conflict_id": conflictID, "wave": pastMaxInt, "resolution": "r"},
		"wave below int":        {"conflict_id": conflictID, "wave": -1e19, "resolution": "r"},
		"conflict_id past int":  {"conflict_id": pastMaxInt, "wave": 2.0, "resolution": "r"},
	}
	open := func() bool {
		var resolution sql.NullString
		if err := s.ReadDB.QueryRow("SELECT resolution FROM conflicts WHERE id = ?", int64(conflictID)).Scan(&resolution); err != nil {
			t.Fatal(err)
		}
		return !resolution.Valid
	}
	for name, f := range refused {
		if _, _, err := s.WriteRecord("resolve_conflict", f); err == nil {
			t.Errorf("%s: %v was accepted", name, f)
		}
		if !open() {
			t.Fatalf("%s: the conflict was closed by a refused write", name)
		}
	}

	if _, _, err := s.WriteRecord("resolve_conflict", ok); err != nil {
		t.Fatalf("resolve_conflict %v: %v", ok, err)
	}
	var resolution string
	var wave int
	if err := s.ReadDB.QueryRow("SELECT resolution, resolved_by_wave FROM conflicts WHERE id = ?", int64(conflictID)).Scan(&resolution, &wave); err != nil {
		t.Fatal(err)
	}
	if resolution != ok["resolution"] || wave != int(ok["wave"].(float64)) {
		t.Errorf("conflict resolved as %q in wave %d, want %v", resolution, wave, ok)
	}
	if _, _, err := s.WriteRecord("resolve_conflict", ok); err == nil || !strings.Contains(err.Error(), "already resolved") {
		t.Errorf("resolving twice: got %v, want already resolved", err)
	}
}

// A finding carries d1–d8, and the write reads all eight, so a project with
// a sixth dimension writes findings that carry it.
func TestWriteRecord_CarriesD6ToD8(t *testing.T) {
	s := newTestStore(t)
	for i := 1; i <= 8; i++ {
		if err := s.Dimensions().AddDimension(fmt.Sprintf("dim%d", i), "", `["a","b","c"]`); err != nil {
			t.Fatal(err)
		}
	}
	fields := map[string]any{"wave": 1.0, "agent": "a", "mss_label": "definition", "finding": "eight coordinates"}
	want := map[string]int{}
	for i := 1; i <= 8; i++ {
		k := fmt.Sprintf("d%d", i)
		want[k] = i % 3
		fields[k] = float64(want[k])
	}
	if _, _, err := s.WriteRecord("finding", fields); err != nil {
		t.Fatalf("a finding carrying d1–d8 was refused: %v", err)
	}
	for k, v := range want {
		var got int
		if err := s.ReadDB.QueryRow("SELECT " + k + " FROM findings WHERE finding = 'eight coordinates'").Scan(&got); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if got != v {
			t.Errorf("%s = %d; want %d", k, got, v)
		}
	}
	// A non-numeric d7 is refused like any other coordinate.
	fields["d7"] = "2"
	if _, _, err := s.WriteRecord("finding", fields); err == nil || !strings.Contains(err.Error(), "must be numeric") {
		t.Errorf("d7 as text: got %v, want a numeric-field error", err)
	}
}

// optInt returns nil for anything it cannot read, and reqInt turns that into
// 0, so a numeric field arriving as a string would write a wave-0 finding
// and report success, or drop a coordinate and leave the record NULL on it.
// It is refused.
func TestWriteRecord_NonNumericFieldIsRefused(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		kind   string
		fields map[string]any
	}{
		{"finding", map[string]any{"wave": "3", "agent": "a", "mss_label": "definition", "finding": "x"}},
		{"finding", map[string]any{"wave": 1, "d1": "2", "agent": "a", "mss_label": "definition", "finding": "x"}},
		{"gap", map[string]any{"wave": []any{1.0}, "description": "g"}},
		{"source", map[string]any{"url": "http://x", "wave": "nope"}},
		{"source", map[string]any{"url": "http://x", "primary_source": true}},
	}
	for _, c := range cases {
		if _, _, err := s.WriteRecord(c.kind, c.fields); err == nil {
			t.Errorf("%s %v was accepted; want a numeric-field error", c.kind, c.fields)
		} else if !strings.Contains(err.Error(), "must be numeric") {
			t.Errorf("%s %v: got %v, want a numeric-field error", c.kind, c.fields, err)
		}
	}
	// A well-formed write is unaffected, and a JSON number still arrives as
	// float64 from any real client.
	if _, _, err := s.WriteRecord("finding", map[string]any{
		"wave": 1.0, "d1": 2.0, "agent": "a", "mss_label": "definition", "finding": "ok",
	}); err != nil {
		t.Errorf("a valid finding was refused: %v", err)
	}
}

func TestOptInt(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]any
		k    string
		want *int
	}{
		{"missing key", map[string]any{}, "x", nil},
		{"nil value", map[string]any{"x": nil}, "x", nil},
		{"int value", map[string]any{"x": 42}, "x", ptr(42)},
		{"int64 value", map[string]any{"x": int64(99)}, "x", ptr(99)},
		{"float64 value", map[string]any{"x": float64(7)}, "x", ptr(7)},
		{"json.Number valid", map[string]any{"x": json.Number("123")}, "x", ptr(123)},
		{"json.Number invalid", map[string]any{"x": json.Number("not-a-number")}, "x", nil},
		{"unsupported type string", map[string]any{"x": "hello"}, "x", nil},
		{"unsupported type bool", map[string]any{"x": true}, "x", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := optInt(tc.m, tc.k)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("want nil, got %d", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("want %d, got nil", *tc.want)
			}
			if *got != *tc.want {
				t.Fatalf("want %d, got %d", *tc.want, *got)
			}
		})
	}
}

func TestReqInt(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]any
		k    string
		want int
	}{
		{"happy path int", map[string]any{"x": 42}, "x", 42},
		{"happy path int64", map[string]any{"x": int64(7)}, "x", 7},
		{"happy path float64", map[string]any{"x": float64(3)}, "x", 3},
		{"happy path json.Number", map[string]any{"x": json.Number("99")}, "x", 99},
		{"missing key returns 0", map[string]any{}, "x", 0},
		{"nil value returns 0", map[string]any{"x": nil}, "x", 0},
		{"invalid type returns 0", map[string]any{"x": "hello"}, "x", 0},
		{"invalid json.Number returns 0", map[string]any{"x": json.Number("nan")}, "x", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reqInt(tc.m, tc.k)
			if got != tc.want {
				t.Fatalf("reqInt(%q) = %d, want %d", tc.k, got, tc.want)
			}
		})
	}
}

func TestOptStr(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    map[string]any
		want *string
	}{
		{"a non-empty string", map[string]any{"key": "hello"}, ptr("hello")},
		{"an empty string", map[string]any{"key": ""}, nil},
		{"no key", map[string]any{}, nil},
		{"an int", map[string]any{"key": 42}, nil},
		{"a bool", map[string]any{"key": true}, nil},
	} {
		got := optStr(tc.m, "key")
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: optStr = %v; want %v", tc.name, got, tc.want)
		}
	}
}

// Every kind refuses a text field that holds anything but a string, naming
// the field, and writes nothing: reqStr and optStr would read 5 as "" and
// nil, writing an agent of 5 as no agent.
func TestWriteRecord_RefusesATextFieldThatIsNotAString(t *testing.T) {
	kinds := []struct {
		kind, table string
		base        map[string]any
		keys        []string
	}{
		{"finding", "findings", map[string]any{"wave": 1.0, "agent": "a", "mss_label": "definition", "finding": "f"},
			[]string{"agent", "mss_label", "finding", "evidence"}},
		{"gap", "gaps", map[string]any{"wave": 1.0, "agent": "a", "description": "g", "priority": "minor"},
			[]string{"agent", "description", "priority"}},
		{"source", "sources", map[string]any{"url": "https://example.com/s", "agent": "a"},
			[]string{"url", "title", "agent", "contribution"}},
		{"resolve_gap", "gaps", map[string]any{"gap_id": 1.0, "wave": 1.0, "agent": "a", "finding_id": 1.0},
			[]string{"agent"}},
		{"resolve_conflict", "conflicts", map[string]any{"conflict_id": 1.0, "wave": 1.0, "resolution": "r"},
			[]string{"resolution"}},
	}
	for _, k := range kinds {
		for _, key := range k.keys {
			for _, v := range []any{5.0, true, []any{"a"}, map[string]any{"a": "b"}} {
				s := seedRecordRows(t)
				f := map[string]any{}
				for fk, fv := range k.base {
					f[fk] = fv
				}
				f[key] = v
				before := recordTableRows(t, s, k.table)
				if _, _, err := s.WriteRecord(k.kind, f); err == nil || !strings.Contains(err.Error(), key) {
					t.Errorf("%s with %s %v: err=%v; want a refusal naming %s", k.kind, key, v, err, key)
				}
				if after := recordTableRows(t, s, k.table); after != before {
					t.Errorf("%s with %s %v changed %s although it was refused", k.kind, key, v, k.table)
				}
			}
		}
	}
}
