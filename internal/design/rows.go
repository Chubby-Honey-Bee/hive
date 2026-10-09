package design

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/bench"
)

// Row is one arm's run of one task: the unit chb design report reads.
type Row struct {
	Config     string `json:"config"`
	Provider   string `json:"provider"`
	LensModel  string `json:"lens_model,omitempty"`
	QueenModel string `json:"queen_model,omitempty"`
	Task       string `json:"task"`
	Family     string `json:"family"`
	Twin       string `json:"twin,omitempty"`
	Arm        string `json:"arm"`

	TestsPassed int      `json:"tests_passed"`
	TestsTotal  int      `json:"tests_total"`
	PassRate    float64  `json:"pass_rate"`
	Built       bool     `json:"built"`
	Failed      []string `json:"failed_tests,omitempty"`

	Fidelity
	Coverage
	Completeness
	Labels

	// Designed: the plan call completed with a document. Executed: the
	// executor completed with a report. Completed: both, and the tests ran.
	Designed  bool `json:"designed"`
	Executed  bool `json:"executed"`
	Completed bool `json:"completed"`
	// TreeOK: the designer's tree hashed the same before and after the
	// swarm read it; true on the control arm, which has no swarm.
	TreeOK bool `json:"tree_ok"`

	WallSeconds   float64          `json:"wall_seconds"`
	DesignSeconds float64          `json:"design_seconds"`
	ExecSeconds   float64          `json:"exec_seconds"`
	TokensIn      int64            `json:"tokens_in"`
	TokensOut     int64            `json:"tokens_out"`
	Nodes         []bench.NodeStat `json:"nodes,omitempty"`
	Error         string           `json:"error,omitempty"`
}

// WriteRows writes rows as JSON lines.
func WriteRows(w io.Writer, rows []Row) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// ErrNotMeasured is why ReadRows refuses a file WriteNotMeasured wrote.
var ErrNotMeasured = errors.New("these rows are not a measurement")

// WriteNotMeasured writes rows after a first line {"not_measured": why},
// which makes ReadRows refuse the file. A case that stopped at an outage,
// or in which no run completed, writes its rows so: kept to read, not a
// result of the configuration.
func WriteNotMeasured(w io.Writer, why string, rows []Row) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]string{"not_measured": why}); err != nil {
		return err
	}
	return WriteRows(w, rows)
}

// ReadRows reads JSON lines written by WriteRows. It refuses a file with a
// not_measured line, with an error that wraps ErrNotMeasured and says why.
func ReadRows(r io.Reader) ([]Row, error) {
	var out []Row
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for line := 1; sc.Scan(); line++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		row, err := decodeRowLine(sc.Bytes())
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, row)
	}
	return out, sc.Err()
}

// decodeRowLine decodes one line of rows, refusing a not_measured line
// with an error that wraps ErrNotMeasured and says why.
func decodeRowLine(b []byte) (Row, error) {
	if why, ok := notMeasuredLine(b); ok {
		return Row{}, fmt.Errorf("%w: %s", ErrNotMeasured, why)
	}
	var row Row
	err := json.Unmarshal(b, &row)
	return row, err
}

// notMeasuredLine is why a line WriteNotMeasured wrote says the rows are
// not a measurement.
func notMeasuredLine(b []byte) (string, bool) {
	var mark struct {
		NotMeasured *string `json:"not_measured"`
	}
	if json.Unmarshal(b, &mark) != nil || mark.NotMeasured == nil {
		return "", false
	}
	return *mark.NotMeasured, true
}
