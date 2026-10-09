package runner

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// newTestStoreForRunner creates a fresh initialised Store in t.TempDir().
func newTestStoreForRunner(t *testing.T) *db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := db.NewStore(path)
	if err != nil {
		t.Fatalf("db.NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestOptInt(t *testing.T) {
	ptr := func(n int) *int { return &n }

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

// The chb_db_write description names the coordinates a gap stores, read here
// from the gaps table.
func TestDBWriteDescription_GapCoordinatesMatchTheSchema(t *testing.T) {
	store := newTestStoreForRunner(t)
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

	r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	r.registerDBWrite(store)
	var gapLine string
	for _, s := range r.Schemas {
		if s.OfTool != nil && s.OfTool.Name == "chb_db_write" {
			for _, line := range strings.Split(s.OfTool.Description.Value, "\n") {
				if strings.Contains(line, `"gap"`) {
					gapLine = line
				}
			}
		}
	}
	if want := fmt.Sprintf("d1-d%d?", highest); !strings.Contains(gapLine, want) {
		t.Errorf("chb_db_write gap line %q; want it to name %s, the coordinates a gap stores", gapLine, want)
	}
}
