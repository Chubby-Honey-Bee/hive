package cli

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestLastIDFrom asserts the last `id=N` token in a string is returned,
// surviving multiple matches and no-match.
func TestLastIDFrom(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"created id=1\nupdated id=42", 42},
		{"id=7", 7},
		{"ID=99 ok", 99},
		{"no ids here", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := lastIDFrom(tc.in); got != tc.want {
			t.Errorf("lastIDFrom(%q) = %d; want %d", tc.in, got, tc.want)
		}
	}
}

// TestFirstIDInJSON asserts the first JSON `"id": N` field is returned.
func TestFirstIDInJSON(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{`{"id": 5}`, 5},
		{`{"id":   13, "id": 99}`, 13}, // first match wins
		{`[{"id": 7}, {"id": 8}]`, 7},
		{`{"name": "x"}`, 0},
		{``, 0},
	}
	for _, tc := range cases {
		if got := firstIDInJSON(tc.in); got != tc.want {
			t.Errorf("firstIDInJSON(%q) = %d; want %d", tc.in, got, tc.want)
		}
	}
}

// TestAllIDsInJSON asserts every `"id": N` value is collected in order.
func TestAllIDsInJSON(t *testing.T) {
	cases := []struct {
		in   string
		want []int64
	}{
		{`[{"id":1},{"id":2},{"id":3}]`, []int64{1, 2, 3}},
		{`{"id":42}`, []int64{42}},
		{`{"name":"x"}`, []int64{}},
		{``, []int64{}},
	}
	for _, tc := range cases {
		got := allIDsInJSON(tc.in)
		if !reflect.DeepEqual(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
			t.Errorf("allIDsInJSON(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

// TestColumnExists_PresentAndAbsent exercises both branches of the
// column-existence probe. Sets up an in-memory sqlite3 DB with a
// known schema, asserts present/absent column detection.
func TestColumnExists_PresentAndAbsent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, payload BLOB)`); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		col  string
		want bool
	}{
		{"id", true},
		{"name", true},
		{"payload", true},
		{"missing", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := columnExists(db, "t", tc.col); got != tc.want {
			t.Errorf("columnExists(%q) = %v; want %v", tc.col, got, tc.want)
		}
	}

	// Non-existent table returns false (PRAGMA returns no rows).
	if got := columnExists(db, "no_such_table", "id"); got {
		t.Errorf("columnExists on missing table = true; want false")
	}
}

// The human gate takes any io.Writer and io.Reader.
func TestPauseForHuman_AnyWriterAndReader(t *testing.T) {
	var out bytes.Buffer
	if err := pauseForHuman(&out, strings.NewReader("\n"), "gate"); err != nil {
		t.Errorf("Enter should continue: %v", err)
	}
	if err := pauseForHuman(&out, strings.NewReader("n\n"), "gate"); err == nil {
		t.Error("'n' should abort")
	}
	if !strings.Contains(out.String(), "HUMAN GATE: gate") {
		t.Errorf("gate prompt not written: %q", out.String())
	}
}

// validateWithoutChb runs chb validate's harness in an empty working
// directory with no chb binary, so every check fails at once, and returns
// what it printed.
func validateWithoutChb(t *testing.T) string {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("HIVE_DB_PATH", "")
	t.Setenv("ANTHROPIC_API_KEY", os.Getenv("ANTHROPIC_API_KEY"))
	var stderr bytes.Buffer
	none := t.TempDir()
	r := &validateRunner{workspaceDir: filepath.Join(none, "validate"), chbBin: filepath.Join(none, "no-chb"),
		mcpBin: filepath.Join(none, "no-chb-mcp"), stdout: io.Discard, stderr: &stderr}
	_ = r.run(context.Background())
	return stderr.String()
}

// chb validate numbers its sections in the order they run: the headers it
// prints count 1, 2, 3 and on, none skipped, repeated or out of turn.
func TestValidate_NumbersItsSectionsInOrder(t *testing.T) {
	out := validateWithoutChb(t)
	headers := regexp.MustCompile(`(?m)^── (\d+)\. (.+) ──$`).FindAllStringSubmatch(out, -1)
	if len(headers) < 2 {
		t.Fatalf("%d section headers:\n%s", len(headers), out)
	}
	for i, h := range headers {
		if h[1] != strconv.Itoa(i+1) {
			t.Errorf("section %d, %q, is numbered %s", i+1, h[2], h[1])
		}
	}
}

// The behavior fixture is for developing HIVE: outside a HIVE source
// checkout chb validate replays none and says nothing of it.
func TestValidate_NoBehaviorFixtureOutsideACheckout(t *testing.T) {
	out := validateWithoutChb(t)
	for _, word := range []string{"cli-behavior", "behavior-spec", "gen-behavior"} {
		if strings.Contains(out, word) {
			t.Errorf("chb validate outside a checkout mentions %s:\n%s", word, out)
		}
	}
}
