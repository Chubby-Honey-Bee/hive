package dreamer

import (
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
)

func TestPtrString(t *testing.T) {
	p := ptrString("foo")
	if p == nil {
		t.Fatal("ptrString returned nil")
	}
	if *p != "foo" {
		t.Errorf("*p = %q; want foo", *p)
	}
}

func TestPtrInt(t *testing.T) {
	p := ptrInt(42)
	if p == nil {
		t.Fatal("ptrInt returned nil")
	}
	if *p != 42 {
		t.Errorf("*p = %d; want 42", *p)
	}
}

func TestCoordsToInts_Empty(t *testing.T) {
	d1, d2, d3, d4 := coordsToInts(comb.Coords{})
	if d1 != nil || d2 != nil || d3 != nil || d4 != nil {
		t.Errorf("expected all nil for empty coords")
	}
}

func TestCoordsToInts_AllPresent(t *testing.T) {
	c := comb.Coords{"d1": 1, "d2": 2, "d3": 3, "d4": 4}
	d1, d2, d3, d4 := coordsToInts(c)
	for i, p := range []*int{d1, d2, d3, d4} {
		if p == nil {
			t.Errorf("d%d should not be nil", i+1)
			continue
		}
		if *p != i+1 {
			t.Errorf("d%d = %d; want %d", i+1, *p, i+1)
		}
	}
}

func TestCoordsToInts_PartialPresent(t *testing.T) {
	c := comb.Coords{"d1": 0, "d3": 5}
	d1, d2, d3, d4 := coordsToInts(c)
	if d1 == nil || *d1 != 0 {
		t.Errorf("d1 = %v; want pointer to 0", d1)
	}
	if d2 != nil {
		t.Error("d2 should be nil")
	}
	if d3 == nil || *d3 != 5 {
		t.Errorf("d3 = %v; want pointer to 5", d3)
	}
	if d4 != nil {
		t.Error("d4 should be nil")
	}
}

func TestErrString_Nil(t *testing.T) {
	if got := errString(nil); got != "" {
		t.Errorf("errString(nil) = %q; want empty", got)
	}
}

func TestErrString_NonNil(t *testing.T) {
	got := errString(stubErr("boom"))
	if got != "boom" {
		t.Errorf("errString = %q; want boom", got)
	}
}

type stubErr string

func (s stubErr) Error() string { return string(s) }
