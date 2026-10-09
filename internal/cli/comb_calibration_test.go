package cli

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// seedGuaranteesAt writes n guarantees at d1=2 on one definition, records
// hits confirmed and the rest refuted by a human, and recomputes.
func seedGuaranteesAt(t *testing.T, s *db.Store, n, hits int) {
	t.Helper()
	seedGuaranteeOutcomes(t, s, n, hits)
	if _, err := calibration.Recompute(s, calibration.Options{}); err != nil {
		t.Fatal(err)
	}
}

// seedGuaranteeOutcomes writes n guarantees at d1=2, resting on one
// definition, and an outcome for each: the first hits confirmed, the rest
// refuted. It recomputes nothing.
func seedGuaranteeOutcomes(t *testing.T, s *db.Store, n, hits int) {
	t.Helper()
	d1 := 2
	def, err := s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "the base", D1: &d1})
	if err != nil {
		t.Fatal(err)
	}
	deps := "[" + strconv.FormatInt(def, 10) + "]"
	for i := 0; i < n; i++ {
		g, err := s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "guarantee", Finding: "claim " + strings.Repeat("x", i+1), D1: &d1, DependsOnIDs: &deps})
		if err != nil {
			t.Fatal(err)
		}
		res := calibration.Confirmed
		if i >= hits {
			res = calibration.Refuted
		}
		if _, err := calibration.Record(s, calibration.Outcome{SubjectKind: calibration.SubjectFinding, FindingID: g, Resolution: res, Source: calibration.SourceHuman}); err != nil {
			t.Fatal(err)
		}
	}
}

// comb query reports calibrated_confidence beside confidence once the
// region's dominant label is calibrated in one of its scopes: nothing at 9
// outcomes, confidence × hit_rate / target from the tenth.
func TestCombQuery_CalibratedConfidence(t *testing.T) {
	s := useTempStore(t)
	seedGuaranteesAt(t, s, 9, 7)
	if _, err := comb.BuildAllRegions(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(runCalibrateCmd(t, newCombQueryCmd(), "--set-d1", "--d1", "2", "--json")), &res); err != nil {
		t.Fatal(err)
	}
	if res["dominant_label"] != "guarantee" || res["calibrated_confidence"] != nil {
		t.Fatalf("nine outcomes: %v", res)
	}
	text := runCalibrateCmd(t, newCombQueryCmd(), "--set-d1", "--d1", "2")
	if strings.Contains(text, "calibrated_confidence") {
		t.Fatalf("nine outcomes printed %q", text)
	}

	seedGuaranteesAt(t, s, 3, 3)
	if _, err := comb.BuildAllRegions(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(runCalibrateCmd(t, newCombQueryCmd(), "--set-d1", "--d1", "2", "--json")), &res); err != nil {
		t.Fatal(err)
	}
	conf := res["confidence"].(float64)
	want := float64(int(conf*10/12 + 0.5))
	if got, _ := res["calibrated_confidence"].(float64); got != want {
		t.Fatalf("calibrated_confidence = %v, want %v (confidence %v × 10/12)", res["calibrated_confidence"], want, conf)
	}
	text = runCalibrateCmd(t, newCombQueryCmd(), "--set-d1", "--d1", "2")
	if !strings.Contains(text, "calibrated_confidence: "+strconv.Itoa(int(want))+"%") || !strings.Contains(text, "correlational") {
		t.Fatalf("comb query printed %q", text)
	}
}

// comb at --tick N uses the score current at tick N: after a later
// recompute moved the hit rate, the earlier tick still reads the earlier
// score, and the later tick the later one. --time reads the same way.
func TestCombAt_UsesTheScoreCurrentAtTheTick(t *testing.T) {
	s := useTempStore(t)
	seedGuaranteeOutcomes(t, s, 12, 10)
	if _, err := comb.BuildAllRegions(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// The recompute's tick opens after the build, so the region's revision,
	// which no tick anchors and which is placed by a timestamp to the second,
	// predates it whatever second each write lands in.
	if _, err := calibration.Recompute(s, calibration.Options{}); err != nil {
		t.Fatal(err)
	}
	ticks, err := s.TimeWheel().Recent(db.TickCalibrate, 10)
	if err != nil || len(ticks) != 1 {
		t.Fatalf("calibrate ticks = %v, %v", ticks, err)
	}
	first := ticks[0].ID
	at := func(tick int64) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal([]byte(runCalibrateCmd(t, newCombAtCmd(), "--vantage", "d1=2", "--tick", strconv.FormatInt(tick, 10), "--json")), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	rev := at(first)
	conf := rev["Confidence"].(float64)
	want := float64(int(conf*10/12 + 0.5))
	if got, _ := rev["calibrated_confidence"].(float64); got != want {
		t.Fatalf("at tick %d: calibrated_confidence = %v, want %v", first, rev["calibrated_confidence"], want)
	}

	// Eight more refutations: 10 of 20 held at the second tick.
	seedGuaranteesAt(t, s, 8, 0)
	ticks, _ = s.TimeWheel().Recent(db.TickCalibrate, 10)
	if len(ticks) != 2 {
		t.Fatalf("calibrate ticks = %d", len(ticks))
	}
	second := ticks[0].ID
	if got, _ := at(second)["calibrated_confidence"].(float64); got != float64(int(conf*0.5+0.5)) {
		t.Fatalf("at tick %d: calibrated_confidence = %v, want %v", second, at(second)["calibrated_confidence"], int(conf*0.5+0.5))
	}
	if got, _ := at(first)["calibrated_confidence"].(float64); got != want {
		t.Fatalf("at tick %d after the second recompute: calibrated_confidence = %v, want %v", first, at(first)["calibrated_confidence"], want)
	}
	text := runCalibrateCmd(t, newCombAtCmd(), "--vantage", "d1=2", "--tick", strconv.FormatInt(second, 10))
	if !strings.Contains(text, "calibrated_confidence="+strconv.Itoa(int(conf*0.5+0.5))+"%") {
		t.Fatalf("comb at printed %q", text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(runCalibrateCmd(t, newCombAtCmd(), "--vantage", "d1=2", "--time", "2100-01-01T00:00:00Z", "--json")), &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := out["calibrated_confidence"].(float64); got != float64(int(conf*0.5+0.5)) {
		t.Fatalf("at a later time: calibrated_confidence = %v", out["calibrated_confidence"])
	}
}
