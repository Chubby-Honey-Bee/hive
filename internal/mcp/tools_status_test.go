package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
)

// chb_findings returns a finding whose convergence_count is NULL with the
// count a finding gets on write, 1. A row that does not scan at all is the
// call's error, not a row of zero values.
func TestFindings_ReadsANullConvergenceCountAsOne(t *testing.T) {
	var buf bytes.Buffer
	s, store := newCalibrationTestServer(t, &buf)
	if _, err := store.WriteDB.Exec(`INSERT INTO findings (wave, agent, mss_label, finding, convergence_count) VALUES (1, 'seed', 'definition', 'counted by no one', NULL)`); err != nil {
		t.Fatal(err)
	}
	text, isErr, rpcErr := callTool(t, s, &buf, "chb_findings", map[string]any{})
	if rpcErr != nil || isErr {
		t.Fatalf("chb_findings: %v %s", rpcErr, text)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		t.Fatalf("result %q: %v", text, err)
	}
	if len(rows) != 1 || rows[0]["finding"] != "counted by no one" || rows[0]["convergence_count"] != float64(1) {
		t.Fatalf("chb_findings = %v, want the one finding with convergence_count 1", rows)
	}

	if _, err := store.WriteDB.Exec(`INSERT INTO findings (wave, agent, mss_label, finding) VALUES ('two', 'seed', 'definition', 'a wave in words')`); err != nil {
		t.Fatal(err)
	}
	text, isErr, rpcErr = callTool(t, s, &buf, "chb_findings", map[string]any{})
	if rpcErr != nil || !isErr || !strings.Contains(text, `"wave"`) {
		t.Fatalf("a row whose wave does not scan: isError=%v rpc=%v text=%q; want an error naming the column", isErr, rpcErr, text)
	}
}

// chb_status with a project reads that project's hive where `chb hive`
// keeps it: the server's own database, or the project's workspace database
// when the server's hosts another project's hive. The result's db names the
// file read. Without a project it reads the server's database; a project
// with no hive anywhere is an error naming the database it would use.
func TestStatus_ReadsAProjectWhereChbHiveKeepsIt(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "workspace", "hive.db")
	if err := os.MkdirAll(filepath.Dir(primary), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(primary)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	seed := func(s *db.Store, project string, iteration, findings int) {
		t.Helper()
		if _, err := s.WriteDB.Exec(`UPDATE hive_state SET iteration=? WHERE project=?`, iteration, project); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < findings; i++ {
			if _, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, mss_label, finding) VALUES (1, 'seed', 0, 'definition', ?)`, project); err != nil {
				t.Fatal(err)
			}
		}
	}
	rA, _, err := hive.Init(hive.Lookup{Project: "A", Named: store, NamedPath: primary})
	if err != nil {
		t.Fatal(err)
	}
	seed(rA.Store, "A", 2, 2)
	rB, _, err := hive.Init(hive.Lookup{Project: "B", Named: store, NamedPath: primary})
	if err != nil {
		t.Fatal(err)
	}
	wantB := filepath.Join(root, "workspace", "B", "hive.db")
	if rB.Path != wantB {
		t.Fatalf("B's hive is in %s, want %s", rB.Path, wantB)
	}
	seed(rB.Store, "B", 3, 1)
	rB.Close()

	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.store = store

	got := callToolJSON(t, s, &buf, "chb_status", map[string]any{"project": "B"})
	if got["db"] != wantB || got["iteration"] != float64(3) || got["findings_count"] != float64(1) {
		t.Fatalf("chb_status B = %v, want db %s, iteration 3 and B's one finding", got, wantB)
	}
	got = callToolJSON(t, s, &buf, "chb_status", map[string]any{"project": "A"})
	if got["db"] != primary || got["iteration"] != float64(2) || got["findings_count"] != float64(2) {
		t.Fatalf("chb_status A = %v, want db %s, iteration 2 and A's two findings", got, primary)
	}
	got = callToolJSON(t, s, &buf, "chb_status", map[string]any{})
	if got["db"] != primary || got["findings_count"] != float64(2) {
		t.Fatalf("chb_status with no project = %v, want the server's database", got)
	}
	text, isErr, rpcErr := callTool(t, s, &buf, "chb_status", map[string]any{"project": "C"})
	wantC := filepath.Join(root, "workspace", "C", "hive.db")
	if rpcErr != nil || !isErr || !strings.Contains(text, wantC) || !strings.Contains(text, `"A"`) {
		t.Fatalf("chb_status C: isError=%v rpc=%v text=%q; want an error naming %s and A", isErr, rpcErr, text, wantC)
	}
	if _, err := os.Stat(wantC); !os.IsNotExist(err) {
		t.Fatalf("a status read created %s (%v)", wantC, err)
	}
}
