package runner

import (
	"fmt"
	"os"

	calib "github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// calibrationLensesKey is the token the Queen's prompt carries, between its
// braces.
const calibrationLensesKey = "calibration.lenses"

// resolveCalibrationTokens substitutes {calibration.lenses} with the track
// records of the run's lens foragers (workflow.LensNames) from their
// global lens scores (calib.LensTrackRecord; the package is aliased because
// this package has a calibration type of its own): the empty string when no
// lens is calibrated, so the prompt is byte-identical to one without the
// token. Only the placeholders the template left are read (left, as
// resolveCombTokens), and it returns those still left. Resolution never
// fails dispatch: a read error resolves to empty with a stderr warning.
func resolveCalibrationTokens(prompt string, left []workflow.Placeholder, store *db.Store, defn map[string]any) (string, []workflow.Placeholder) {
	var (
		text string
		done bool
	)
	return workflow.FillLeft(prompt, left, func(key string) (string, bool) {
		if key != calibrationLensesKey {
			return "", false
		}
		if !done {
			done = true
			global := calib.ScopeGlobal
			rows, err := store.Calibration().ListScores(calib.KindLens, &global)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[calibration] template warning: %v\n", err)
			} else {
				text = calib.LensTrackRecord(calib.ScoresFrom(rows), workflow.LensNames(defn))
			}
		}
		return text, true
	})
}
