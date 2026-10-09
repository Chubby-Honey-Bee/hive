package design

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRows_RoundTripAndNotMeasured(t *testing.T) {
	rate := 0.5
	rows := []Row{
		{Task: "a", Arm: ArmDesigner, TestsPassed: 3, TestsTotal: 4, PassRate: 0.75, Fidelity: Fidelity{Steps: 2, Deviations: 1, Rate: &rate}, Labels: Labels{Claims: 2, AuditPass: true}, Completed: true, TreeOK: true},
		{Task: "a", Arm: ArmControl, TestsTotal: 4, Error: "the executor ran out of turns"},
	}
	var buf bytes.Buffer
	if err := WriteRows(&buf, rows); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRows(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rows) {
		t.Errorf("round trip changed the rows:\n%+v\n%+v", got, rows)
	}
	buf.Reset()
	if err := WriteNotMeasured(&buf, "stopped at run 2 of 4", rows); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	if !strings.HasPrefix(raw, `{"not_measured":"stopped at run 2 of 4"}`+"\n") || strings.Count(raw, "\n") != 3 {
		t.Errorf("not_measured file:\n%s", raw)
	}
	_, err = ReadRows(strings.NewReader(raw))
	if !errors.Is(err, ErrNotMeasured) || !strings.Contains(err.Error(), "stopped at run 2 of 4") {
		t.Errorf("ReadRows = %v; want it refused with why", err)
	}
}
