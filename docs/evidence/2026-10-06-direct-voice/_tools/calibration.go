// Command calibration prints one JSON line per bench run directory: the
// run's calibration as workflow.RunCalibration reads it from the run's
// hive.db (the code chb ask prints), and each forager node's status and
// verdict. decide.py joins these lines to results.jsonl.
//
//	go run ./docs/evidence/2026-10-06-direct-voice/_tools <run-dir>...
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

type line struct {
	Dir         string               `json:"dir"`
	RunID       int64                `json:"run_id"`
	RunStatus   string               `json:"run_status"`
	Calibration workflow.Calibration `json:"calibration"`
	Verdicts    map[string]string    `json:"verdicts"`
	Statuses    map[string]string    `json:"statuses"`
}

func main() {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	for _, dir := range os.Args[1:] {
		l, err := read(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", dir, err)
			os.Exit(1)
		}
		if err := enc.Encode(l); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func read(dir string) (line, error) {
	s, err := db.NewStore(filepath.Join(dir, "hive.db"))
	if err != nil {
		return line{}, err
	}
	defer s.Close()
	runs, err := s.Workflows().ListWorkflowRuns(10)
	if err != nil {
		return line{}, err
	}
	if len(runs) != 1 {
		return line{}, fmt.Errorf("%d workflow runs, want 1", len(runs))
	}
	run := runs[0]
	cal, err := workflow.RunCalibration(s.Workflows(), run.ID)
	if err != nil {
		return line{}, err
	}
	states, err := s.Workflows().GetWorkflowNodeStates(run.ID)
	if err != nil {
		return line{}, err
	}
	l := line{Dir: dir, RunID: run.ID, RunStatus: run.Status, Calibration: cal, Verdicts: map[string]string{}, Statuses: map[string]string{}}
	for _, st := range states {
		name, ok := strings.CutPrefix(st.NodeName, "forager-")
		if !ok {
			continue
		}
		l.Statuses[name] = st.Status
		verdict := ""
		var out map[string]any
		if st.Status == "completed" && st.OutputsJSON.Valid && json.Unmarshal([]byte(st.OutputsJSON.String), &out) == nil {
			verdict, _ = out["verdict"].(string)
		}
		l.Verdicts[name] = verdict
	}
	return l, nil
}
