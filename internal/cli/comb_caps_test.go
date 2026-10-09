package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
)

// statusCapsFixture is the Comb's capped-cell fixture F (internal/comb/caps_test.go).
var statusCapsFixture = []struct {
	capped bool
	at     comb.Coords
}{
	{true, comb.Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3}},
	{true, comb.Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3}},
	{true, comb.Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3, "d5": 1}},
	{true, comb.Coords{"d1": 0, "d3": 5}},
	{true, comb.Coords{"d1": 0, "d3": 5, "d5": 3}},
	{true, comb.Coords{"d1": 0, "d2": 2, "d3": 5}},
	{true, comb.Coords{"d1": 1, "d2": 4}},
	{true, comb.Coords{"d1": 1, "d2": 4, "d3": 0, "d4": 0}},
	{false, comb.Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 3, "d5": 2}},
	{false, comb.Coords{"d1": 0, "d2": 2, "d3": 6}},
	{false, comb.Coords{"d1": 0, "d2": 1, "d3": 2, "d4": 9}},
	{false, comb.Coords{"d1": 2, "d2": 0, "d3": 0, "d4": 0}},
	{false, comb.Coords{"d1": 1, "d2": 4, "d3": 9, "d4": 9}},
}

// comb status counts capped cells and capped findings from the caps
// themselves, so both are right with no refresh, and it names neither a
// bare "capped", which is the hive loop's iteration-cap phase.
func TestCombStatus_CountsCappedCells(t *testing.T) {
	s, err := db.NewStore(filepath.Join(t.TempDir(), "status.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	prev := store
	store = s
	t.Cleanup(func() { store = prev })

	var caps []hive.Action
	for _, f := range statusCapsFixture {
		d := f.at.AsNullCoords()
		res, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, d3, d4, d5, d6, d7, d8, mss_label, finding)
			VALUES (1, 'test', ?, ?, ?, ?, ?, ?, ?, ?, 'assumption', 'fixture')`,
			d[0], d[1], d[2], d[3], d[4], d[5], d[6], d[7])
		if err != nil {
			t.Fatal(err)
		}
		if f.capped {
			id, _ := res.LastInsertId()
			caps = append(caps, hive.Action{Type: "cap_finding", FindingID: id})
		}
	}
	if _, err := hive.ApplyCaps(s, caps); err != nil {
		t.Fatal(err)
	}

	var cells, findings int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM (SELECT DISTINCT f.d1, f.d2, f.d3, f.d4
		FROM capped_findings cf JOIN findings f ON f.id = cf.finding_id)`).Scan(&cells); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM capped_findings`).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if cells == findings {
		t.Fatalf("the fixture has %d capped cells and %d capped findings; they must differ", cells, findings)
	}

	run := func(args ...string) string {
		cmd := newCombStatusCmd()
		cmd.SetArgs(args)
		out, err := captureStdout(t, func() error { return cmd.ExecuteContext(context.Background()) })
		if err != nil {
			t.Fatalf("comb status %v: %v", args, err)
		}
		return out
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(run("--json")), &got); err != nil {
		t.Fatal(err)
	}
	if got["capped_cells"] != float64(cells) || got["capped_findings"] != float64(findings) {
		t.Errorf("status --json: capped_cells %v, capped_findings %v; want %d, %d", got["capped_cells"], got["capped_findings"], cells, findings)
	}

	text := run()
	if want := fmt.Sprintf("capped_cells=%d capped_findings=%d", cells, findings); !strings.Contains(text, want) {
		t.Errorf("status = %q; want it to hold %q", text, want)
	}
	for _, m := range regexp.MustCompile(`(?i)capped`).FindAllStringIndex(text, -1) {
		rest := text[m[1]:]
		if !strings.HasPrefix(rest, " cell") && !strings.HasPrefix(rest, " finding") && !strings.HasPrefix(rest, "_") {
			t.Errorf("status says a bare \"capped\": %q", text)
		}
	}
}
