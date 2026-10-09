package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A marker that does not parse is reported on stderr by kind with the start
// of its body (each case's excerpt), the markers that parse are still
// written, and ingest exits non-zero.
//
// A marker with no closing brace, or with no -->, sits right before a valid
// marker of its kind: its body ends at its own --> or at the next head, so
// the valid marker after it is still written. A marker with no --> is
// unparseable even when its JSON is whole, as the last one shows.
func TestIngest_ReportsUnparseableMarkers(t *testing.T) {
	markers := []struct {
		text, kind, table, excerpt string
		valid                      bool
	}{
		{`<!-- FINDING: {"d1": 0, "finding": "no closing brace" -->`, "finding", "findings", `{"d1": 0, "finding": "no closing brace"`, false},
		{`<!-- FINDING: {"d1": 0, "mss_label": "definition", "finding": "a valid finding", "source_urls": "https://example.test/a"} -->`, "finding", "findings", "", true},
		{`<!-- FINDING: {"d1": 0, "mss_label": "definition" "finding": "missing comma"} -->`, "finding", "findings", `{"d1": 0, "mss_label": "definition" "finding"`, false},
		{`<!-- GAP: {"description": "no close", "priority": "minor"}`, "gap", "gaps", `{"description": "no close", "priority": "minor"}`, false},
		{`<!-- GAP: {"description": "an open question", "priority": "important"} -->`, "gap", "gaps", "", true},
		{`<!-- GAP: TODO fill in -->`, "gap", "gaps", "TODO fill in", false},
		{`<!-- FOLLOWUP: {"question": "what next?", "priority": "minor"} -->`, "followup", "followups", "", true},
		{`<!-- FOLLOWUP: {"question": "trailing comma",} -->`, "followup", "followups", `{"question": "trailing comma",}`, false},
		{`<!-- FOLLOWUP: {"question": "unclosed at the end", "priority": "minor"}`, "followup", "followups", `{"question": "unclosed at the end", "priority": "minor"}`, false},
	}
	var text strings.Builder
	wantRows := map[string]int{}
	wantBad := 0
	for _, m := range markers {
		text.WriteString("Some prose.\n" + m.text + "\n")
		if m.valid {
			wantRows[m.table]++
		} else {
			wantBad++
		}
	}

	s := useTestStore(t)
	path := filepath.Join(t.TempDir(), "output.md")
	if err := os.WriteFile(path, []byte(text.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := newIngestCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	err := runCmd(t, cmd, "--wave", "1", "--agent", "a", path)
	if err == nil {
		t.Fatalf("ingest exited 0 with %d unparseable markers", wantBad)
	}
	if got := strings.Count(stderr.String(), "unparseable"); got != wantBad {
		t.Errorf("stderr reports %d unparseable markers; want %d:\n%s", got, wantBad, stderr.String())
	}
	for _, m := range markers {
		if !m.valid && !strings.Contains(stderr.String(), "unparseable "+m.kind+" marker, skipped: "+m.excerpt) {
			t.Errorf("stderr does not report the %s marker %q:\n%s", m.kind, m.excerpt, stderr.String())
		}
	}
	for table, want := range wantRows {
		var n int
		if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s holds %d rows; want the %d valid markers written", table, n, want)
		}
	}

	// With only the valid markers, ingest exits 0 and reports nothing.
	var clean strings.Builder
	for _, m := range markers {
		if m.valid {
			clean.WriteString(m.text + "\n")
		}
	}
	useTestStore(t)
	if err := os.WriteFile(path, []byte(clean.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = newIngestCmd()
	stderr.Reset()
	cmd.SetErr(&stderr)
	if err := runCmd(t, cmd, "--wave", "1", "--agent", "a", path); err != nil || stderr.Len() != 0 {
		t.Errorf("clean ingest: err=%v stderr=%q; want exit 0 and nothing on stderr", err, stderr.String())
	}
}

// A marker whose integer field is not a whole number, or whose text field
// is not a string, fails to write with the refusal chb_db_write gives the
// field, and ingest exits non-zero. Ingest filed wave 1.5 under wave 1, an
// agent of 5 as no agent, and a d1 of "2" as no coordinate.
func TestIngest_RefusesAFieldOfTheWrongType(t *testing.T) {
	markers := []struct{ text, table, key, raw string }{
		{`<!-- FINDING: {"wave": 1.5, "d1": 0, "mss_label": "definition", "finding": "f"} -->`, "findings", "wave", `1.5`},
		{`<!-- GAP: {"agent": 5, "description": "g", "priority": "minor"} -->`, "gaps", "agent", `5`},
		{`<!-- FOLLOWUP: {"d1": "2", "question": "q?", "priority": "minor"} -->`, "followups", "d1", `"2"`},
	}
	for _, m := range markers {
		t.Run(m.table, func(t *testing.T) {
			s := useTestStore(t)
			refusal := findingRefusal(t, s, m.key, m.raw)
			path := filepath.Join(t.TempDir(), "output.md")
			if err := os.WriteFile(path, []byte("Some prose.\n"+m.text+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := newIngestCmd()
			cmd.SetArgs([]string{"--wave", "1", "--agent", "a", path})
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			out, err := captureStdout(t, cmd.Execute)
			if err == nil {
				t.Errorf("ingest exited 0 over %s", m.text)
			}
			if !strings.Contains(out, refusal) {
				t.Errorf("ingest output does not carry chb_db_write's refusal %q:\n%s", refusal, out)
			}
			if n := rowCount(t, s, m.table); n != 0 {
				t.Errorf("%s holds %d rows; want none", m.table, n)
			}
		})
	}
}
