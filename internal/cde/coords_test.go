package cde

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// newTestDB returns an in-memory SQLite with the dimensions table created
// and the supplied dimension rows pre-inserted.
func newTestDB(t *testing.T, dims []struct {
	name   string
	values string
}) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`CREATE TABLE dimensions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL,
		values_json TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, d := range dims {
		if _, err := db.Exec("INSERT INTO dimensions(name,values_json) VALUES(?,?)",
			d.name, d.values); err != nil {
			t.Fatalf("insert dim %s: %v", d.name, err)
		}
	}
	return db
}

func TestValidateCoords_NoDimensionsRegistered(t *testing.T) {
	// Empty registry → any (even all-nil) coords are valid.
	db := newTestDB(t, nil)
	if err := ValidateCoords(db, Coords{}); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateCoords_AllValid(t *testing.T) {
	db := newTestDB(t, []struct {
		name, values string
	}{
		{"d1_concern", `["go","solid","cde"]`},
		{"d2_pkg", `["a","b"]`},
	})

	c := Coords{IntPtr(0), IntPtr(1)}
	if err := ValidateCoords(db, c); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidateCoords_NullOnActiveDimension(t *testing.T) {
	db := newTestDB(t, []struct {
		name, values string
	}{
		{"d1_concern", `["x","y"]`},
	})

	// d1 is active but coord is nil — must reject.
	err := ValidateCoords(db, Coords{nil})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrNullCoordinate) {
		t.Errorf("expected ErrNullCoordinate, got %v", err)
	}
	var cdeErr *CDEError
	if !errors.As(err, &cdeErr) {
		t.Fatalf("expected *CDEError, got %T", err)
	}
	if cdeErr.Dimension != 1 || cdeErr.DimName != "d1_concern" {
		t.Errorf("wrong context: dim=%d name=%s", cdeErr.Dimension, cdeErr.DimName)
	}
}

func TestValidateCoords_OutOfBoundsHigh(t *testing.T) {
	db := newTestDB(t, []struct {
		name, values string
	}{
		{"severity", `["critical","high","med"]`}, // bound=2
	})

	err := ValidateCoords(db, Coords{IntPtr(5)})
	if !errors.Is(err, ErrOutOfBounds) {
		t.Fatalf("expected ErrOutOfBounds, got %v", err)
	}
	var cdeErr *CDEError
	if !errors.As(err, &cdeErr) || cdeErr.MaxBound != 2 {
		t.Errorf("expected MaxBound=2, got %v", cdeErr)
	}
}

func TestValidateCoords_OutOfBoundsNegative(t *testing.T) {
	db := newTestDB(t, []struct {
		name, values string
	}{
		{"severity", `["a","b"]`},
	})

	err := ValidateCoords(db, Coords{IntPtr(-1)})
	if !errors.Is(err, ErrOutOfBounds) {
		t.Fatalf("expected ErrOutOfBounds, got %v", err)
	}
}

// A registry wider than the coordinate space is refused: validating only the
// first eight dimensions would pass findings that carry no value at all on
// d9+, present in the table and unreachable by any probe on those
// dimensions.
func TestValidateCoords_RegistryWiderThanCoordinateSpaceIsRefused(t *testing.T) {
	var dims []struct {
		name, values string
	}
	for i := 0; i < 10; i++ {
		dims = append(dims, struct {
			name, values string
		}{
			name:   string(rune('a' + i)),
			values: `["x","y"]`,
		})
	}
	db := newTestDB(t, dims)

	// Provide 8 valid coords; the 9th and 10th dims must NOT be required.
	c := Coords{
		IntPtr(0), IntPtr(0), IntPtr(0), IntPtr(0),
		IntPtr(0), IntPtr(0), IntPtr(0), IntPtr(0),
	}
	err := ValidateCoords(db, c)
	if err == nil {
		t.Fatal("a 10-dimension registry validated clean against an 8-slot coordinate")
	}
	if !strings.Contains(err.Error(), "more than 8") {
		t.Errorf("error should name the cap, got %v", err)
	}
}

// A dimension whose values_json will not parse has no domain, so a
// coordinate on it cannot be shown to be in bounds, and is refused: skipping
// the check would turn one corrupt registry row into permanently unvalidated
// writes.
func TestValidateCoords_UnreadableDomainIsRefused(t *testing.T) {
	db := newTestDB(t, []struct {
		name, values string
	}{
		{"d1", `not-json`},
	})
	err := ValidateCoords(db, Coords{IntPtr(99999)})
	if err == nil {
		t.Fatal("d1=99999 validated clean against an unreadable domain")
	}
	if !errors.Is(err, ErrOutOfBounds) {
		t.Errorf("expected ErrOutOfBounds, got %v", err)
	}
}

func TestValidateCoords_QueryError(t *testing.T) {
	// Closing the DB before calling ValidateCoords triggers the db.Query error path.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Close() // force query failure

	err = ValidateCoords(db, Coords{})
	if err == nil {
		t.Fatal("expected error from closed DB, got nil")
	}
}

func TestIntPtr(t *testing.T) {
	p := IntPtr(42)
	if p == nil || *p != 42 {
		t.Errorf("IntPtr(42) → %v", p)
	}
}

func TestCDEError_Error(t *testing.T) {
	e := &CDEError{Dimension: 3, DimName: "severity", Reason: "test"}
	want := "CDE error (d3 severity): test"
	if got := e.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCDEError_Unwrap(t *testing.T) {
	e := &CDEError{Err: ErrNullCoordinate}
	if !errors.Is(e, ErrNullCoordinate) {
		t.Errorf("Unwrap chain broken")
	}
}

// IntPtr is a test helper; production code has no use for it.
func IntPtr(v int) *int { return &v }
