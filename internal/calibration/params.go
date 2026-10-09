// Package calibration records whether the hive's beliefs held and scores
// each predictor against the outcomes ledger. It writes no finding label:
// labels change only through the MSS write path, the cascade and the
// dreamer's settle pass (CALIB-1, cde-mss.md § Calibration never writes
// labels).
package calibration

// The formula's constants. Each is a choice; cde-mss.md § Calibration
// recompute states the formula they enter.
const (
	// NFloor is the resolved outcomes a predictor needs before its score is
	// calibrated. Below it every consumer treats the weight as 1.0.
	NFloor = 10
	// KShrink shrinks the weight toward 1.0 at low n.
	KShrink = 10.0
	// WMin and WMax clamp the raw weight: no lens is silenced, none dominates.
	WMin = 0.5
	WMax = 2.0
	// PriorA and PriorB are the Beta(1,1) prior on the hit rate.
	PriorA = 1.0
	PriorB = 1.0
	// GuaranteeConfirmFloor is the guarantee hit rate below which, at
	// n >= NFloor, a score is reported as drift.
	GuaranteeConfirmFloor = 0.95
	// DriftDelta is the least fall in a hit rate since its previous revision
	// that is reported as drift.
	DriftDelta = 0.20
)

// LabelTargets is the reference rate for each MSS label: the share of
// outcomes a label's findings are expected to confirm.
var LabelTargets = map[string]float64{
	"guarantee":  1.0,
	"definition": 0.95,
	"assumption": 0.6,
	"unknown":    0.5,
}

// Predictor kinds, the first column of a calibration_scores key.
const (
	KindLens        = "lens"
	KindLabel       = "label"
	KindConvergence = "convergence"
	KindSynthesizer = "synthesizer"
)

// Predictor keys fixed by the design: the synthesizer is the Queen, and the
// convergence predictor is the ∇ signal (a run with a fired resonates bond).
const (
	KeyQueen = "queen"
	KeyNabla = "nabla"
)

// ScopeGlobal is the scope every recompute computes; the others are
// coordinate prefixes ("d1=2", "d1=2;d2=0") with at least NFloor outcomes.
const ScopeGlobal = ""

// Outcome subject kinds, resolutions and sources, as the outcomes table's
// CHECK constraints admit them.
const (
	SubjectFinding          = "finding"
	SubjectLensVerdict      = "lens_verdict"
	SubjectSynthesisVerdict = "synthesis_verdict"

	Confirmed = "confirmed"
	Refuted   = "refuted"
	Partial   = "partial"

	SourceHuman         = "human"
	SourceDownstreamRun = "downstream_run"
	SourceExternal      = "external"
)
