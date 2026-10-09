package gate

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// seedGuaranteeDomain writes a definition at d1 and n guarantees on it in
// wave 1, records hits of them confirmed and the rest refuted by a human,
// and recomputes, so label/guarantee in scope d1=<d1> is scored. Texts hold
// no digit, so detection sees no numeric divergence between them.
func seedGuaranteeDomain(t *testing.T, store *db.Store, d1, n, hits int) int64 {
	t.Helper()
	def, err := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "the base", D1: &d1})
	if err != nil {
		t.Fatal(err)
	}
	deps := "[" + strconv.FormatInt(def, 10) + "]"
	for i := 0; i < n; i++ {
		text := "guarantee " + strings.Repeat("x", i+1)
		g, err := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "guarantee", Finding: text, D1: &d1, DependsOnIDs: &deps})
		if err != nil {
			t.Fatal(err)
		}
		res := calibration.Confirmed
		if i >= hits {
			res = calibration.Refuted
		}
		if _, err := calibration.Record(store, calibration.Outcome{SubjectKind: calibration.SubjectFinding, FindingID: g, Resolution: res, Source: calibration.SourceHuman}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := calibration.Recompute(store, calibration.Options{}); err != nil {
		t.Fatal(err)
	}
	return def
}

func reviewErrors(res *GateResult) []string {
	var out []string
	for _, e := range res.Errors {
		if strings.Contains(e, "needs outcome review") {
			out = append(out, e)
		}
	}
	return out
}

// --require-outcome-review blocks on a guarantee in a domain whose
// calibrated guarantee hit rate is under the floor until a human or an
// external outcome resolves it: a downstream_run refutation is no review, a
// human outcome is. Off, the same wave opens; and with the domain at 9
// outcomes, under the floor, nothing blocks.
func TestRunGatePipeline_RequireOutcomeReview(t *testing.T) {
	store := newGateStore(t)
	def := seedGuaranteeDomain(t, store, 2, 12, 10)
	d1 := 2
	deps := "[" + strconv.FormatInt(def, 10) + "]"
	g, err := store.Findings().AddFinding(&db.Finding{Wave: 2, Agent: "a", MSSLabel: "guarantee", Finding: "the new claim", D1: &d1, DependsOnIDs: &deps})
	if err != nil {
		t.Fatal(err)
	}
	eval := map[string]any{"verdict": "COMPLETE"}

	res, err := RunGatePipeline(store, 2, eval, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Opened || len(reviewErrors(res)) != 0 {
		t.Fatalf("with the precondition off the gate did not open: %+v", res)
	}

	res, err = RunGatePipeline(store, 2, eval, false, true, true)
	if err != nil {
		t.Fatal(err)
	}
	errs := reviewErrors(res)
	if res.Opened || len(errs) != 1 {
		t.Fatalf("an unreviewed guarantee in a sub-floor domain opened the gate: %+v", res)
	}
	for _, want := range []string{"guarantee " + strconv.FormatInt(g, 10), "scope d1=2", "0.83", "n=12", "0.95", "chb outcome-record"} {
		if !strings.Contains(errs[0], want) {
			t.Errorf("the refusal lacks %q: %s", want, errs[0])
		}
	}

	// An adjudication's refutation is downstream_run: no review.
	if _, err := calibration.Record(store, calibration.Outcome{SubjectKind: calibration.SubjectFinding, FindingID: g, Resolution: calibration.Refuted, Source: calibration.SourceDownstreamRun}); err != nil {
		t.Fatal(err)
	}
	res, err = RunGatePipeline(store, 2, eval, false, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Opened || len(reviewErrors(res)) != 1 {
		t.Fatalf("a downstream_run outcome counted as a review: %+v", res)
	}

	// A human outcome is a review, whatever it says.
	if _, err := calibration.Record(store, calibration.Outcome{SubjectKind: calibration.SubjectFinding, FindingID: g, Resolution: calibration.Confirmed, Source: calibration.SourceHuman}); err != nil {
		t.Fatal(err)
	}
	res, err = RunGatePipeline(store, 2, eval, false, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Opened || len(reviewErrors(res)) != 0 {
		t.Fatalf("a reviewed guarantee still blocked: %+v", res)
	}

	// Above the floor nothing is asked: 12 of 12 held.
	above := newGateStore(t)
	def = seedGuaranteeDomain(t, above, 2, 12, 12)
	deps = "[" + strconv.FormatInt(def, 10) + "]"
	if _, err := above.Findings().AddFinding(&db.Finding{Wave: 2, Agent: "a", MSSLabel: "guarantee", Finding: "the new claim", D1: &d1, DependsOnIDs: &deps}); err != nil {
		t.Fatal(err)
	}
	if res, err = RunGatePipeline(above, 2, eval, false, false, true); err != nil || !res.Opened {
		t.Fatalf("a domain above the floor blocked: %+v, %v", res, err)
	}

	// Under the floor of outcomes, 9 of them, the domain is uncalibrated
	// and governs nothing.
	few := newGateStore(t)
	def = seedGuaranteeDomain(t, few, 2, 9, 5)
	deps = "[" + strconv.FormatInt(def, 10) + "]"
	if _, err := few.Findings().AddFinding(&db.Finding{Wave: 2, Agent: "a", MSSLabel: "guarantee", Finding: "the new claim", D1: &d1, DependsOnIDs: &deps}); err != nil {
		t.Fatal(err)
	}
	if res, err = RunGatePipeline(few, 2, eval, false, false, true); err != nil || !res.Opened {
		t.Fatalf("nine outcomes blocked the gate: %+v, %v", res, err)
	}
}
