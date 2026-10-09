package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAddDimension_WriteError covers the error branch: when the write DB is closed, AddDimension must
// return a non-nil error that wraps the underlying failure.
func TestAddDimension_WriteError(t *testing.T) {
	s := newTestStore(t)
	// Obtain a reference to the underlying writeDB, then close it to force an Exec error.
	writeDB := s.dimensions.writeDB
	writeDB.Close()

	repo := s.Dimensions()
	err := repo.AddDimension("broken", "should fail", `["a"]`)
	if err == nil {
		t.Fatal("expected error from AddDimension after writeDB closed, got nil")
	}
	if !strings.Contains(err.Error(), "add dimension") {
		t.Errorf("error message should contain 'add dimension', got: %v", err)
	}
}

// `null` and `[]` parse as a []string, but an empty domain bounds every
// coordinate out (max=-1), which would refuse each later finding until the
// dimension was registered again, so they are refused at registration.
func TestAddDimension_RefusesAnEmptyDomain(t *testing.T) {
	s := newTestStore(t)
	for _, values := range []string{"null", "[]", " [ ] "} {
		if err := s.Dimensions().AddDimension("empty", "d", values); err == nil {
			t.Errorf("AddDimension(values=%s) succeeded; want it refused", values)
		}
	}
	dims, err := s.Dimensions().GetDimensions()
	if err != nil {
		t.Fatal(err)
	}
	if len(dims) != 0 {
		t.Fatalf("an empty-domain dimension was stored: %+v", dims)
	}
}

// TestGetDimensions_EmptyTable verifies GetDimensions returns empty slice with no error on an empty table.
func TestGetDimensions_EmptyTable(t *testing.T) {
	s := newTestStore(t)
	repo := s.Dimensions()

	dims, err := repo.GetDimensions()
	if err != nil {
		t.Fatalf("GetDimensions on empty table: %v", err)
	}
	if len(dims) != 0 {
		t.Errorf("expected 0 dimensions, got %d", len(dims))
	}
}

// TestGetDimensions_HappyPath verifies that dimensions inserted via AddDimension are returned by GetDimensions.
func TestGetDimensions_HappyPath(t *testing.T) {
	s := newTestStore(t)
	repo := s.Dimensions()

	cases := []struct {
		name, description, valuesJSON string
	}{
		{"market_segment", "Which market segment", `["B2B","B2C"]`},
		{"product_line", "Product category", `["hardware","software"]`},
		{"geo_region", "Geographic region", `["NA","EU","APAC"]`},
	}
	for _, c := range cases {
		if err := repo.AddDimension(c.name, c.description, c.valuesJSON); err != nil {
			t.Fatalf("AddDimension(%q): %v", c.name, err)
		}
	}

	dims, err := repo.GetDimensions()
	if err != nil {
		t.Fatalf("GetDimensions: %v", err)
	}
	if len(dims) != len(cases) {
		t.Fatalf("expected %d dimensions, got %d", len(cases), len(dims))
	}
	for i, c := range cases {
		if dims[i].Name != c.name {
			t.Errorf("[%d] Name: got %q, want %q", i, dims[i].Name, c.name)
		}
		if dims[i].Description != c.description {
			t.Errorf("[%d] Description: got %q, want %q", i, dims[i].Description, c.description)
		}
		if dims[i].ValuesJSON != c.valuesJSON {
			t.Errorf("[%d] ValuesJSON: got %q, want %q", i, dims[i].ValuesJSON, c.valuesJSON)
		}
		if dims[i].ID == 0 {
			t.Errorf("[%d] ID should be non-zero", i)
		}
		if dims[i].CreatedAt == "" {
			t.Errorf("[%d] CreatedAt should be non-empty", i)
		}
	}
}

// TestGetDimensions_OrderedByID verifies that GetDimensions returns rows ordered by id ascending.
func TestGetDimensions_OrderedByID(t *testing.T) {
	s := newTestStore(t)
	repo := s.Dimensions()

	names := []string{"zzz_dim", "aaa_dim", "mmm_dim"}
	for _, n := range names {
		if err := repo.AddDimension(n, "", `["a"]`); err != nil {
			t.Fatalf("AddDimension(%q): %v", n, err)
		}
	}

	dims, err := repo.GetDimensions()
	if err != nil {
		t.Fatalf("GetDimensions: %v", err)
	}
	if len(dims) != len(names) {
		t.Fatalf("expected %d dimensions, got %d", len(names), len(dims))
	}
	// IDs must be strictly ascending.
	for i := 1; i < len(dims); i++ {
		if dims[i].ID <= dims[i-1].ID {
			t.Errorf("dims not ordered by id: dims[%d].ID=%d <= dims[%d].ID=%d",
				i, dims[i].ID, i-1, dims[i-1].ID)
		}
	}
}

// TestGetDimensions_QueryError covers the error branch: when the read DB is closed,
// GetDimensions must return a non-nil error that wraps "query dimensions".
func TestGetDimensions_QueryError(t *testing.T) {
	s := newTestStore(t)
	readDB := s.dimensions.readDB
	readDB.Close()

	repo := s.Dimensions()
	_, err := repo.GetDimensions()
	if err == nil {
		t.Fatal("expected error from GetDimensions after readDB closed, got nil")
	}
	if !strings.Contains(err.Error(), "query dimensions") {
		t.Errorf("error message should contain 'query dimensions', got: %v", err)
	}
}

// TestGetDimensions_ScanError covers the scan-error branch: GetDimensions returns an error
// containing "scan dimension" when rows.Scan cannot convert a column value to the target type.
// We rename the real table and install a 5-column view that projects 'not-a-number' as the
// id column; the Query succeeds (all five columns exist) but Scan fails converting that text
// to int64.
func TestGetDimensions_ScanError(t *testing.T) {
	s := newTestStore(t)
	repo := s.Dimensions()

	// Insert a row so the loop body executes.
	if err := repo.AddDimension("dim_scan_err", "desc", `["a"]`); err != nil {
		t.Fatalf("AddDimension: %v", err)
	}

	// Swap the real table with a view that returns a non-numeric id so Scan fails.
	if _, err := s.WriteDB.Exec(`ALTER TABLE dimensions RENAME TO dimensions_real`); err != nil {
		t.Fatalf("RENAME TABLE: %v", err)
	}
	if _, err := s.WriteDB.Exec(`CREATE VIEW dimensions AS
		SELECT 'not-a-number' AS id, name, description, values_json, created_at
		FROM dimensions_real`); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}

	_, scanErr := repo.GetDimensions()
	if scanErr == nil {
		t.Fatal("expected scan error from GetDimensions with bad id column, got nil")
	}
	if !strings.Contains(scanErr.Error(), "scan dimension") {
		t.Errorf("error should contain 'scan dimension', got: %v", scanErr)
	}
}

// TestGetDimensions_UpsertReplaceIdempotent verifies that AddDimension with INSERT OR REPLACE
// updates an existing dimension and GetDimensions reflects the update.
func TestGetDimensions_UpsertReplaceIdempotent(t *testing.T) {
	s := newTestStore(t)
	repo := s.Dimensions()

	if err := repo.AddDimension("dim_x", "original", `["v1"]`); err != nil {
		t.Fatalf("first AddDimension: %v", err)
	}
	// Replace with updated description / values.
	if err := repo.AddDimension("dim_x", "updated", `["v1","v2"]`); err != nil {
		t.Fatalf("second AddDimension: %v", err)
	}

	dims, err := repo.GetDimensions()
	if err != nil {
		t.Fatalf("GetDimensions: %v", err)
	}
	if len(dims) != 1 {
		t.Fatalf("expected 1 dimension after upsert, got %d", len(dims))
	}
	if dims[0].Description != "updated" {
		t.Errorf("Description: got %q, want %q", dims[0].Description, "updated")
	}
	if dims[0].ValuesJSON != `["v1","v2"]` {
		t.Errorf("ValuesJSON: got %q, want %q", dims[0].ValuesJSON, `["v1","v2"]`)
	}
}

// The d-slot is derived from dimension row order (internal/cde/coords.go),
// so a re-registration updates in place and keeps the row's id: a new id
// would remap every slot behind every stored coordinate.
func TestAddDimension_ReRegisterKeepsIdAndOrder(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	repo := store.Dimensions()
	for _, d := range []struct{ n, desc string }{{"first", "one"}, {"second", "two"}} {
		if err := repo.AddDimension(d.n, d.desc, `["a"]`); err != nil {
			t.Fatal(err)
		}
	}
	before, err := repo.GetDimensions()
	if err != nil || len(before) != 2 {
		t.Fatalf("setup: %v %d", err, len(before))
	}

	if err := repo.AddDimension("first", "one, revised", `["a","b"]`); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetDimensions()
	if err != nil || len(after) != 2 {
		t.Fatalf("after: %v %d", err, len(after))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Name != before[i].Name {
			t.Errorf("slot %d moved: before %d/%s, after %d/%s", i, before[i].ID, before[i].Name, after[i].ID, after[i].Name)
		}
	}
	if after[0].Description != "one, revised" || after[0].ValuesJSON != `["a","b"]` {
		t.Errorf("re-registration did not update in place: %+v", after[0])
	}
}
