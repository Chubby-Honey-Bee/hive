package interval

import (
	"reflect"
	"testing"
)

func TestHiddenInputUntouched(t *testing.T) {
	in := []Interval{{5, 7}, {1, 3}, {9, 2}}
	before := append([]Interval(nil), in...)
	Merge(in)
	if !reflect.DeepEqual(in, before) {
		t.Errorf("Merge modified its input: %v, was %v", in, before)
	}
}

func TestHiddenTouchingAndContiguousMerge(t *testing.T) {
	cases := []struct {
		in, want []Interval
	}{
		{[]Interval{{1, 3}, {3, 5}}, []Interval{{1, 5}}},
		{[]Interval{{1, 2}, {3, 4}}, []Interval{{1, 4}}},
		{[]Interval{{1, 2}, {4, 5}}, []Interval{{1, 2}, {4, 5}}},
		{[]Interval{{1, 10}, {2, 3}}, []Interval{{1, 10}}},
		{[]Interval{{1, 1}, {2, 2}, {3, 3}}, []Interval{{1, 3}}},
	}
	for _, c := range cases {
		if got := Merge(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Merge(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestHiddenUnsortedInput(t *testing.T) {
	got := Merge([]Interval{{8, 10}, {1, 3}, {2, 6}, {15, 18}})
	want := []Interval{{1, 6}, {8, 10}, {15, 18}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestHiddenReversedIntervalNormalised(t *testing.T) {
	got := Merge([]Interval{{5, 1}, {6, 8}})
	want := []Interval{{1, 8}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	got = Merge([]Interval{{10, 7}})
	if !reflect.DeepEqual(got, []Interval{{7, 10}}) {
		t.Errorf("single reversed: got %v", got)
	}
}

func TestHiddenEmptyAndSingle(t *testing.T) {
	if got := Merge(nil); len(got) != 0 {
		t.Errorf("Merge(nil) = %v, want length 0", got)
	}
	if got := Merge([]Interval{}); len(got) != 0 {
		t.Errorf("Merge(empty) = %v, want length 0", got)
	}
	if got := Merge([]Interval{{2, 2}}); !reflect.DeepEqual(got, []Interval{{2, 2}}) {
		t.Errorf("Merge(single) = %v", got)
	}
}

func TestHiddenCoversExactlyTheInputs(t *testing.T) {
	in := []Interval{{3, 1}, {10, 12}, {2, 4}, {13, 13}, {20, 20}}
	got := Merge(in)
	covered := func(ivs []Interval, x int) bool {
		for _, iv := range ivs {
			lo, hi := iv.Lo, iv.Hi
			if lo > hi {
				lo, hi = hi, lo
			}
			if x >= lo && x <= hi {
				return true
			}
		}
		return false
	}
	for x := -2; x < 25; x++ {
		if covered(in, x) != covered(got, x) {
			t.Errorf("%d: input covers %v, result covers %v", x, covered(in, x), covered(got, x))
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].Lo <= got[i-1].Hi+1 {
			t.Errorf("result %v holds touching intervals at %d", got, i)
		}
	}
}
