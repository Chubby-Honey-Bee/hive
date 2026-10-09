package calibration

import "github.com/Chubby-Honey-Bee/hive/internal/db"

// State is a finding's confirmation state: the resolution of the outcome
// that ranks highest by source, `external` over `human` over
// `downstream_run`, and the latest within a source. Reviewed says a human
// or an external source resolved it. A finding with no outcome has the
// zero State. The scores count every row; the precedence decides the
// state, not the weight.
type State struct {
	Resolution string
	Source     string
	OutcomeID  int64
	Outcomes   int
	Reviewed   bool
}

// sourceRank orders the sources: the highest rank decides the state.
var sourceRank = map[string]int{
	SourceExternal:      3,
	SourceHuman:         2,
	SourceDownstreamRun: 1,
}

// ConfirmationState reads a finding's outcomes and applies the precedence.
func ConfirmationState(store *db.Store, findingID int64) (State, error) {
	rows, err := store.Outcomes().ListBySubject(SubjectFinding, findingID, "", 0)
	if err != nil {
		return State{}, err
	}
	return stateOf(rows), nil
}

// stateOf applies the precedence to one subject's outcomes, oldest first:
// a later row of the same rank replaces an earlier one.
func stateOf(rows []*db.OutcomeRow) State {
	st := State{Outcomes: len(rows)}
	best := 0
	for _, o := range rows {
		if r := sourceRank[o.Source]; r >= best {
			best = r
			st.Resolution, st.Source, st.OutcomeID = o.Resolution, o.Source, o.ID
		}
	}
	st.Reviewed = st.Source == SourceHuman || st.Source == SourceExternal
	return st
}
