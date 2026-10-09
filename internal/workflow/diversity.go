package workflow

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// LensDiversity is whether one model produced a unanimous swarm, read from
// the lenses of one run that returned a verdict.
type LensDiversity struct {
	Lenses     int      // lenses that returned a verdict
	Verdicts   []string // their distinct verdicts, sorted
	Models     []string // their distinct recorded models, sorted
	Unrecorded []string // those of them with no recorded model, sorted
}

// The states of a run's lens diversity, as the artifact records them.
const (
	DiversityLow        = "low"
	DiversityNotLow     = "not_low"
	DiversityUnknown    = "unknown"
	DiversityNotChecked = "not_checked"
)

// State is the check's finding. Not checked: fewer than two lenses
// returned a verdict. Not low: they returned two or more verdicts, or ran
// on two or more recorded models. Unknown: they agree, and a lens has no
// recorded model while the recorded ones number one or none. Low: two or
// more returned one verdict string, all on one recorded model. Models
// compare by name, so two sizes of one family are two models.
func (d LensDiversity) State() string {
	switch {
	case d.Lenses < 2:
		return DiversityNotChecked
	case d.varied():
		return DiversityNotLow
	case len(d.Unrecorded) > 0:
		return DiversityUnknown
	}
	return DiversityLow
}

// varied reports whether the lenses returned two or more verdicts, or ran on
// two or more recorded models.
func (d LensDiversity) varied() bool {
	return len(d.Verdicts) > 1 || len(d.Models) > 1
}

// String is the line the Queen reads for {diversity}.
func (d LensDiversity) String() string {
	switch d.State() {
	case DiversityNotChecked:
		return "not checked: fewer than two lenses returned a verdict"
	case DiversityNotLow:
		return d.notLowText()
	case DiversityUnknown:
		return fmt.Sprintf("unknown: all %d lenses returned %s, and no model is recorded for %s", d.Lenses, d.Verdicts[0], strings.Join(d.Unrecorded, ", "))
	}
	return fmt.Sprintf("low: all %d lenses returned %s, and all ran on one model, %s. One model's bias can pass for consensus, so this agreement is one model's view, not %d independent ones",
		d.Lenses, d.Verdicts[0], d.Models[0], d.Lenses)
}

// notLowText names the different verdicts or, when the verdicts agree, the
// models.
func (d LensDiversity) notLowText() string {
	if len(d.Verdicts) > 1 {
		return fmt.Sprintf("not low: the %d lenses returned %d different verdicts (%s)", d.Lenses, len(d.Verdicts), strings.Join(d.Verdicts, ", "))
	}
	return fmt.Sprintf("not low: all %d lenses returned %s, on %d models (%s)", d.Lenses, d.Verdicts[0], len(d.Models), strings.Join(d.Models, ", "))
}

// LensAnswer is one lens forager's node in a run: the verdict it returned,
// "" when it returned none, and the model its row records, "" when none is
// recorded.
type LensAnswer struct {
	Verdict string
	Model   string
}

// DiversityOf checks the lenses that returned a verdict.
func DiversityOf(lenses map[string]LensAnswer) LensDiversity {
	var d LensDiversity
	verdicts, models := map[string]bool{}, map[string]bool{}
	for name, a := range lenses {
		if a.Verdict == "" {
			continue
		}
		d.Lenses++
		verdicts[a.Verdict] = true
		if a.Model == "" {
			d.Unrecorded = append(d.Unrecorded, name)
		} else {
			models[a.Model] = true
		}
	}
	d.Verdicts = slices.Sorted(maps.Keys(verdicts))
	d.Models = slices.Sorted(maps.Keys(models))
	sort.Strings(d.Unrecorded)
	return d
}

// lensAnswers is each lens forager's answer among the run's outcomes.
func lensAnswers(outcomes map[string]foragerOutcome) map[string]LensAnswer {
	out := make(map[string]LensAnswer, len(outcomes))
	for name, o := range outcomes {
		out[name] = LensAnswer{Verdict: o.verdict(), Model: o.model}
	}
	return out
}

// RunLensAnswers reads every lens forager node of one run, whatever its
// status, as the run tokens read them: the verdict its completed outputs
// hold and the model its row records. It needs only the run's rows, so a
// run whose Queen failed still has them.
func RunLensAnswers(repo Store, runID int64) (map[string]LensAnswer, error) {
	_, outcomes, err := readRunForagers(repo, runID)
	if err != nil {
		return nil, err
	}
	return lensAnswers(outcomes), nil
}

// RunDiversity reads one run's lens diversity the way the Queen's
// {diversity} token does, for the artifact.
func RunDiversity(repo Store, runID int64) (LensDiversity, error) {
	lenses, err := RunLensAnswers(repo, runID)
	if err != nil {
		return LensDiversity{}, err
	}
	return DiversityOf(lenses), nil
}
