package calibration

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// The formula fixtures at n ∈ {0, 5, 10, 50}. Each expected value is the
// formula written out by hand from cde-mss.md § Calibration recompute, with
// the constants as literals, so a change to the formula or a constant fails
// here rather than being pinned from output.
func TestFormula_Fixtures(t *testing.T) {
	conf := func(v int) *int { return &v }

	// n = 0 against the assumption target 0.6: the prior alone.
	{
		s := Formula(Counts{}, 0.6)
		pHat := (0.0 + 1.0) / (0.0 + 1.0 + 1.0) // 0.5
		wRaw := pHat / 0.6                      // 0.8333…, inside [0.5, 2]
		w := 1.0 + (wRaw-1.0)*0.0/(0.0+10.0)    // 1.0: shrinkage pins an empty sample to neutral
		if s.N != 0 || !near(s.PHat, pHat) || !near(s.WRaw, wRaw) || !near(s.W, w) || w != 1.0 {
			t.Fatalf("n=0: %+v, want p̂=%v w_raw=%v w=%v", s, pHat, wRaw, w)
		}
		if s.Calibrated || s.HasBrier || s.HitRate != 0 {
			t.Fatalf("n=0: calibrated=%v brier=%v hit=%v", s.Calibrated, s.HasBrier, s.HitRate)
		}
	}

	// n = 5: four confirmed, one refuted, each stated at 80, against 0.6.
	{
		var c Counts
		for i := 0; i < 4; i++ {
			c.Add(Confirmed, conf(80))
		}
		c.Add(Refuted, conf(80))
		s := Formula(c, 0.6)
		pHat := (4.0 + 0.5*0.0 + 1.0) / (5.0 + 1.0 + 1.0)          // 5/7
		wRaw := pHat / 0.6                                         // 1.1904…
		w := 1.0 + (wRaw-1.0)*5.0/(5.0+10.0)                       // 1 + 0.1904…/3
		brier := (4*(0.8-1.0)*(0.8-1.0) + (0.8-0.0)*(0.8-0.0)) / 5 // (0.16 + 0.64)/5
		if s.N != 5 || !near(s.PHat, pHat) || !near(s.WRaw, wRaw) || !near(s.W, w) {
			t.Fatalf("n=5: %+v, want p̂=%v w_raw=%v w=%v", s, pHat, wRaw, w)
		}
		if s.Calibrated || !s.HasBrier || !near(s.BrierScore, brier) || !near(s.HitRate, 4.0/5.0) {
			t.Fatalf("n=5: calibrated=%v brier=%v (want %v) hit=%v", s.Calibrated, s.BrierScore, brier, s.HitRate)
		}
	}

	// n = 10, the floor: seven confirmed, two partial, one refuted, all
	// stated at 90, against the guarantee target 1.0.
	{
		var c Counts
		for i := 0; i < 7; i++ {
			c.Add(Confirmed, conf(90))
		}
		c.Add(Partial, conf(90))
		c.Add(Partial, conf(90))
		c.Add(Refuted, conf(90))
		s := Formula(c, 1.0)
		hits := 7.0 + 0.5*2.0                     // 8
		pHat := (hits + 1.0) / (10.0 + 1.0 + 1.0) // 0.75
		wRaw := pHat / 1.0
		w := 1.0 + (wRaw-1.0)*10.0/(10.0+10.0) // 0.875
		brier := (7*(0.9-1.0)*(0.9-1.0) + 2*(0.9-0.5)*(0.9-0.5) + (0.9-0.0)*(0.9-0.0)) / 10
		if s.N != 10 || !near(s.PHat, pHat) || !near(s.WRaw, wRaw) || !near(s.W, w) || !near(s.HitRate, hits/10.0) {
			t.Fatalf("n=10: %+v, want p̂=%v w_raw=%v w=%v hit=%v", s, pHat, wRaw, w, hits/10.0)
		}
		if !s.Calibrated || !near(s.BrierScore, brier) {
			t.Fatalf("n=10: calibrated=%v (want true at the floor) brier=%v want %v", s.Calibrated, s.BrierScore, brier)
		}
	}

	// n = 50: forty-five confirmed, five refuted, no confidence stated,
	// against a pooled rate of 0.5.
	{
		var c Counts
		for i := 0; i < 45; i++ {
			c.Add(Confirmed, nil)
		}
		for i := 0; i < 5; i++ {
			c.Add(Refuted, nil)
		}
		s := Formula(c, 0.5)
		pHat := (45.0 + 1.0) / (50.0 + 1.0 + 1.0) // 46/52
		wRaw := pHat / 0.5                        // 1.769…, inside the clamp
		w := 1.0 + (wRaw-1.0)*50.0/(50.0+10.0)
		if s.N != 50 || !near(s.PHat, pHat) || !near(s.WRaw, wRaw) || !near(s.W, w) || !s.Calibrated || s.HasBrier {
			t.Fatalf("n=50: %+v, want p̂=%v w_raw=%v w=%v calibrated, no brier", s, pHat, wRaw, w)
		}
		// The clamp binds against a low reference: 46/52 / 0.4 > 2.
		s = Formula(c, 0.4)
		if s.WRaw != 2.0 || !near(s.W, 1.0+(2.0-1.0)*50.0/60.0) {
			t.Fatalf("n=50 clamped: w_raw=%v w=%v, want 2 and %v", s.WRaw, s.W, 1.0+50.0/60.0)
		}
		// And the floor of the clamp against a high reference: 46/52 / 1.9 < 0.5.
		s = Formula(c, 1.9)
		if s.WRaw != 0.5 || !near(s.W, 1.0+(0.5-1.0)*50.0/60.0) {
			t.Fatalf("n=50 floor-clamped: w_raw=%v w=%v", s.WRaw, s.W)
		}
	}
}

// calibrated flips at exactly NFloor, never below it.
func TestFormula_CalibratedAtTheFloorOnly(t *testing.T) {
	for n := 0; n <= 2*NFloor; n++ {
		var c Counts
		for i := 0; i < n; i++ {
			c.Add(Confirmed, nil)
		}
		if got := Formula(c, 0.6).Calibrated; got != (n >= NFloor) {
			t.Fatalf("n=%d: calibrated=%v", n, got)
		}
	}
}

// Shrinkage: the weight moves from 1 toward w_raw as n grows, by exactly
// n/(n+K), so at n = K it sits halfway.
func TestFormula_ShrinkageHalfwayAtK(t *testing.T) {
	var c Counts
	for i := 0; i < int(KShrink); i++ {
		c.Add(Confirmed, nil)
	}
	s := Formula(c, 0.5)
	if !near(s.W-1.0, (s.WRaw-1.0)/2) {
		t.Fatalf("at n=K the weight should sit halfway: w=%v w_raw=%v", s.W, s.WRaw)
	}
}

// The Brier term per resolution.
func TestBrierTerm(t *testing.T) {
	cases := []struct {
		conf       int
		resolution string
		want       float64
	}{
		{100, Confirmed, 0},
		{0, Refuted, 0},
		{50, Partial, 0},
		{80, Confirmed, 0.04},
		{80, Refuted, 0.64},
		{80, Partial, 0.09},
	}
	for _, c := range cases {
		if got := BrierTerm(c.conf, c.resolution); !near(got, c.want) {
			t.Errorf("BrierTerm(%d, %s) = %v, want %v", c.conf, c.resolution, got, c.want)
		}
	}
}
