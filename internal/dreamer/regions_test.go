package dreamer

import (
	"testing"
)

func TestReadFindingCoords_NotFound(t *testing.T) {
	store := freshStore(t)
	_, err := readFindingCoords(store.ReadConn(), 99999)
	if err == nil {
		t.Fatal("expected error for non-existent finding, got nil")
	}
}

func TestReadFindingCoords_Found(t *testing.T) {
	store := freshStore(t)
	id := mustAddFinding(t, store, "definition", "coords test finding", nil)
	coords, err := readFindingCoords(store.ReadConn(), id)
	if err != nil {
		t.Fatalf("readFindingCoords: %v", err)
	}
	// mustAddFinding sets D1=0, so d1 must be present.
	if _, ok := coords["d1"]; !ok {
		t.Errorf("expected d1 in coords, got %v", coords)
	}
}

func TestDimName(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{1, "d1"}, {2, "d2"}, {3, "d3"}, {4, "d4"},
		{5, "d5"}, {6, "d6"}, {7, "d7"}, {8, "d8"},
		{0, ""}, {-1, ""}, {9, ""}, {100, ""},
	}
	for _, tc := range cases {
		if got := dimName(tc.in); got != tc.want {
			t.Errorf("dimName(%d) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
