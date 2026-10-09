package mcp

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// The kind enum is enforced before the handler runs, so chb_db_write can
// close a gap or a conflict only if its schema admits both kinds.
func TestDBWrite_ClosesAGapAndAConflict(t *testing.T) {
	store, err := db.NewStore(filepath.Join(t.TempDir(), "hive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.store = store

	write := func(kind string, fields map[string]any) {
		t.Helper()
		text, isErr, rpcErr := callTool(t, s, &buf, "chb_db_write", map[string]any{"kind": kind, "fields": fields})
		if rpcErr != nil || isErr {
			t.Fatalf("%s: protocol error %v, tool error %v: %s", kind, rpcErr, isErr, text)
		}
	}
	write("finding", map[string]any{"wave": 1, "agent": "a", "mss_label": "definition", "finding": "a"})
	write("finding", map[string]any{"wave": 1, "agent": "a", "mss_label": "definition", "finding": "not a"})
	write("gap", map[string]any{"wave": 1, "agent": "a", "description": "is it a?", "priority": "important"})
	if err := store.Conflicts().AddConflict(1, 1, 2, "a vs not a"); err != nil {
		t.Fatal(err)
	}
	write("resolve_gap", map[string]any{"gap_id": 1, "wave": 2, "agent": "a", "finding_id": 1})
	write("resolve_conflict", map[string]any{"conflict_id": 1, "wave": 2, "resolution": "finding 1 survives"})

	var openGaps, openConflicts int
	if err := store.ReadDB.QueryRow("SELECT (SELECT COUNT(*) FROM gaps WHERE resolved_by_wave IS NULL), (SELECT COUNT(*) FROM conflicts WHERE resolution IS NULL)").Scan(&openGaps, &openConflicts); err != nil {
		t.Fatal(err)
	}
	if openGaps != 0 || openConflicts != 0 {
		t.Errorf("after both resolves: %d open gap(s), %d open conflict(s); want 0 and 0", openGaps, openConflicts)
	}
}

// The chb_db_write description names the coordinates a gap stores, read here
// from the gaps table.
func TestDBWriteDescription_GapCoordinatesMatchTheSchema(t *testing.T) {
	store, err := db.NewStore(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ReadDB.Query(`SELECT name FROM pragma_table_info('gaps')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	coord := regexp.MustCompile(`^d([1-9])$`)
	highest := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if m := coord.FindStringSubmatch(name); m != nil {
			var n int
			fmt.Sscan(m[1], &n)
			highest = max(highest, n)
		}
	}
	if highest == 0 {
		t.Fatal("the gaps table has no coordinate columns")
	}

	desc, _ := dbWriteSpec()["description"].(string)
	var gapLine string
	for _, line := range strings.Split(desc, "\n") {
		if strings.Contains(line, `"gap"`) {
			gapLine = line
		}
	}
	if want := fmt.Sprintf("d1-d%d?", highest); !strings.Contains(gapLine, want) {
		t.Errorf("chb_db_write gap line %q; want it to name %s, the coordinates a gap stores", gapLine, want)
	}
}

// chb_db_write refuses a text field that holds no string, as isError and
// writing nothing.
func TestDBWrite_RefusesATextFieldThatIsNotAString(t *testing.T) {
	store, err := db.NewStore(filepath.Join(t.TempDir(), "hive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.store = store
	fields := map[string]any{"wave": 1, "agent": 5, "mss_label": "definition", "finding": "f"}
	text, isErr, rpcErr := callTool(t, s, &buf, "chb_db_write", map[string]any{"kind": "finding", "fields": fields})
	if rpcErr != nil || !isErr || !strings.Contains(text, "agent") {
		t.Errorf("chb_db_write with agent 5: protocol error %v, tool error %v: %s; want a tool error naming agent", rpcErr, isErr, text)
	}
	var n int
	if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM findings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d finding(s) written; want none", n)
	}
}
