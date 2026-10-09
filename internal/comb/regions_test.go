package comb

import (
	"reflect"
	"testing"
)

func TestRegionKey_Empty(t *testing.T) {
	if got := RegionKey(Coords{}); got != "" {
		t.Fatalf("empty Coords should render as empty string, got %q", got)
	}
}

func TestRegionKey_DeterministicOrder(t *testing.T) {
	a := RegionKey(Coords{"d2": 3, "d1": 0, "d4": 7})
	b := RegionKey(Coords{"d4": 7, "d1": 0, "d2": 3})
	if a != b {
		t.Fatalf("RegionKey is order-sensitive: %q vs %q", a, b)
	}
	if a != "d1=0;d2=3;d4=7" {
		t.Fatalf("unexpected canonical form: %q", a)
	}
}

func TestParseRegionKey_RoundTrip(t *testing.T) {
	cases := []Coords{
		{},
		{"d1": 0},
		{"d1": 0, "d2": 3},
		{"d1": 0, "d2": 3, "d4": 7, "d8": 9},
	}
	for _, c := range cases {
		key := RegionKey(c)
		got, err := ParseRegionKey(key)
		if err != nil {
			t.Fatalf("ParseRegionKey(%q): %v", key, err)
		}
		if !reflect.DeepEqual(got, c) {
			t.Fatalf("round trip drift: in=%v key=%q out=%v", c, key, got)
		}
	}
}

func TestParseRegionKey_BadInputs(t *testing.T) {
	cases := []string{
		"d1",        // no '='
		"=5",        // no key
		"d9=1",      // out-of-range dim
		"x=1",       // not a d-dim
		"d1=banana", // bad value
	}
	for _, s := range cases {
		if _, err := ParseRegionKey(s); err == nil {
			t.Fatalf("ParseRegionKey(%q) should have errored", s)
		}
	}
}

func TestPrefixes(t *testing.T) {
	c := Coords{"d1": 0, "d2": 3, "d4": 7}
	pfx := Prefixes(c)
	want := []Coords{
		{},
		{"d1": 0},
		{"d1": 0, "d2": 3},
		{"d1": 0, "d2": 3, "d4": 7},
	}
	if len(pfx) != len(want) {
		t.Fatalf("expected %d prefixes, got %d (%v)", len(want), len(pfx), pfx)
	}
	for i, w := range want {
		if !reflect.DeepEqual(pfx[i], w) {
			t.Fatalf("prefix[%d]: got %v want %v", i, pfx[i], w)
		}
	}
}

func TestIsForagerVantage(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"forager:optimist", true},
		{"forager:", true},
		{"", false},
		{"d1=0", false},
		{"Forager:x", false},
	}
	for _, tc := range cases {
		got := IsForagerVantage(tc.key)
		if got != tc.want {
			t.Errorf("IsForagerVantage(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestCoveringRegions_MostSpecificFirst(t *testing.T) {
	c := Coords{"d1": 0, "d2": 3}
	cov := CoveringRegions(c)
	if len(cov) != 3 {
		t.Fatalf("expected 3 covering regions, got %d", len(cov))
	}
	// most specific first
	if RegionKey(cov[0]) != "d1=0;d2=3" {
		t.Fatalf("expected most-specific first, got %q", RegionKey(cov[0]))
	}
	if RegionKey(cov[len(cov)-1]) != "" {
		t.Fatalf("expected global last, got %q", RegionKey(cov[len(cov)-1]))
	}
}
