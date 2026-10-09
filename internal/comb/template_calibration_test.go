package comb

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// The {comb.<key>} line ends with ` [calibrated confidence N%]` once the
// region's dominant label is calibrated in one of its scopes, and is the
// line it was before that.
func TestResolve_CalibratedConfidenceSuffix(t *testing.T) {
	s := storeForTemplateTest(t)
	d1 := 2
	def, err := s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "the base", D1: &d1})
	if err != nil {
		t.Fatal(err)
	}
	deps := "[" + strconv.FormatInt(def, 10) + "]"
	seed := func(n, hits int) {
		t.Helper()
		for i := 0; i < n; i++ {
			g, err := s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "guarantee", Finding: "claim " + strings.Repeat("y", i+1+n), D1: &d1, DependsOnIDs: &deps})
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
		if _, err := calibration.Recompute(s, calibration.Options{}); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildAllRegions(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	seed(9, 7)
	before := Resolve(s, "d1=2")
	if before == "" || strings.Contains(before, "calibrated confidence") {
		t.Fatalf("nine outcomes: %q", before)
	}
	seed(3, 3)
	row, err := s.Comb().Get("d1=2")
	if err != nil || row == nil {
		t.Fatal(err)
	}
	want := " [calibrated confidence " + strconv.Itoa(int(float64(row.Confidence)*10/12+0.5)) + "%]"
	got := Resolve(s, "d1=2")
	if !strings.HasSuffix(got, want) {
		t.Fatalf("Resolve = %q, want the suffix %q", got, want)
	}
	if got := Resolve(s, "forager:nobody"); got != "" {
		t.Fatalf("a missing forager = %q", got)
	}
}
