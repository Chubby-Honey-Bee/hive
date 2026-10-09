package calibration

// Counts is what the formula reads for one predictor in one scope.
type Counts struct {
	Confirmed int
	Partial   int
	Refuted   int
	// BrierSum is the sum of (stated_confidence/100 - y)² over the BrierN
	// outcomes that stated a confidence.
	BrierSum float64
	BrierN   int
}

// N is the resolved outcomes: confirmed + partial + refuted.
func (c Counts) N() int { return c.Confirmed + c.Partial + c.Refuted }

// Hits counts a partial as half a confirmation.
func (c Counts) Hits() float64 { return float64(c.Confirmed) + 0.5*float64(c.Partial) }

// HitRate is the raw rate hits / n, 0 for an empty sample. It is what
// drift compares against GuaranteeConfirmFloor and DriftDelta.
func (c Counts) HitRate() float64 {
	if c.N() == 0 {
		return 0
	}
	return c.Hits() / float64(c.N())
}

// PHat is the posterior mean under the Beta(PriorA, PriorB) prior:
// (c + 0.5p + PriorA) / (n + PriorA + PriorB). It is 0.5 for an empty
// sample, which is what makes a pooled reference rate always defined.
func (c Counts) PHat() float64 {
	return (c.Hits() + PriorA) / (float64(c.N()) + PriorA + PriorB)
}

// Add folds one outcome into the counts.
func (c *Counts) Add(resolution string, statedConfidence *int) {
	c.tally(resolution)
	if statedConfidence != nil {
		c.BrierSum += BrierTerm(*statedConfidence, resolution)
		c.BrierN++
	}
}

// tally counts one resolution; a string that names none counts nothing.
func (c *Counts) tally(resolution string) {
	switch resolution {
	case Confirmed:
		c.Confirmed++
	case Partial:
		c.Partial++
	case Refuted:
		c.Refuted++
	}
}

// resolutions is c without its Brier fields: what a reference rate reads.
func (c Counts) resolutions() Counts {
	return Counts{Confirmed: c.Confirmed, Partial: c.Partial, Refuted: c.Refuted}
}

// anyNegative reports whether a resolution count of c is below zero.
func (c Counts) anyNegative() bool {
	return c.Confirmed < 0 || c.Partial < 0 || c.Refuted < 0
}

// Merge adds o's counts to c.
func (c *Counts) Merge(o Counts) {
	c.Confirmed += o.Confirmed
	c.Partial += o.Partial
	c.Refuted += o.Refuted
	c.BrierSum += o.BrierSum
	c.BrierN += o.BrierN
}

// Score is the formula's output for one predictor in one scope.
type Score struct {
	N          int
	HitRate    float64
	PHat       float64
	PBar       float64
	WRaw       float64
	W          float64
	Calibrated bool
	// BrierScore is the mean Brier term; HasBrier is false when no outcome
	// stated a confidence, and BrierScore is then 0.
	BrierScore float64
	HasBrier   bool
}

// Formula applies cde-mss.md § Calibration recompute to counts c against the
// reference rate pbar:
//
//	p̂     = (c + 0.5p + PriorA) / (n + PriorA + PriorB)
//	w_raw = clamp(p̂ / p̄, WMin, WMax)
//	w     = 1 + (w_raw − 1) · n / (n + KShrink)
//	calibrated = n ≥ NFloor
//
// Brier is a diagnostic and does not enter w.
func Formula(c Counts, pbar float64) Score {
	n := c.N()
	s := Score{
		N:          n,
		HitRate:    c.HitRate(),
		PHat:       c.PHat(),
		PBar:       pbar,
		Calibrated: n >= NFloor,
	}
	s.WRaw = clamp(s.PHat/pbar, WMin, WMax)
	s.W = 1.0 + (s.WRaw-1.0)*float64(n)/(float64(n)+KShrink)
	if c.BrierN > 0 {
		s.BrierScore = c.BrierSum / float64(c.BrierN)
		s.HasBrier = true
	}
	return s
}

// BrierTerm is (stated_confidence/100 − y)² with y = 1 for confirmed, 0 for
// refuted and 0.5 for partial.
func BrierTerm(statedConfidence int, resolution string) float64 {
	pr := float64(statedConfidence) / 100
	d := pr - outcomeValue(resolution)
	return d * d
}

func outcomeValue(resolution string) float64 {
	switch resolution {
	case Confirmed:
		return 1
	case Partial:
		return 0.5
	}
	return 0
}

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
