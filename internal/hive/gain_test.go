package hive

import "testing"

func TestToInt(t *testing.T) {
	tests := []struct {
		name   string
		in     any
		want   int
		wantOK bool
	}{
		{"int", int(42), 42, true},
		{"int negative", int(-7), -7, true},
		{"int64", int64(100), 100, true},
		{"int64 large", int64(1 << 32), 1 << 32, true},
		{"float64", float64(3.9), 3, true},
		{"float64 truncates", float64(-2.7), -2, true},
		{"string (unsupported)", "5", 0, false},
		{"nil (unsupported)", nil, 0, false},
		{"bool (unsupported)", true, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := toInt(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("toInt(%v) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// applyParamAdjustments applies the plan's adjust_params actions, clamped,
// and ignores other actions.
func TestApplyParamAdjustments(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.WriteDB.Exec(`INSERT INTO hive_state (project) VALUES ('p')`); err != nil {
		t.Fatal(err)
	}
	applied, err := applyParamAdjustments(s.WriteDB, "p", []Action{
		{ID: "a1", Type: "dispatch_agent"},
		{ID: "a2", Type: "adjust_params", Params: map[string]any{"batch_size": float64(99)}},
		{ID: "a3", Type: "adjust_params", Params: map[string]any{"convergence_threshold": float64(4)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 {
		t.Fatalf("applied %d adjustments, want 2: %v", len(applied), applied)
	}
	var batch, conv int
	if err := s.ReadDB.QueryRow(`SELECT batch_size, convergence_threshold FROM hive_state WHERE project='p'`).Scan(&batch, &conv); err != nil {
		t.Fatal(err)
	}
	if batch != BatchSizeMax || conv != 4 {
		t.Errorf("batch_size=%d convergence_threshold=%d, want %d and 4", batch, conv, BatchSizeMax)
	}
}

// A batch_size or convergence_threshold that is not a number is refused, and
// the call writes nothing, not even a valid parameter beside it.
func TestApplyGainControl_RefusesNonNumeric(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	read := func() [2]int {
		t.Helper()
		var v [2]int
		if err := s.ReadDB.QueryRow(`SELECT batch_size, convergence_threshold FROM hive_state WHERE project='p'`).Scan(&v[0], &v[1]); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := read()
	for _, params := range []map[string]any{
		{"batch_size": "12"},
		{"convergence_threshold": nil},
		{"batch_size": float64(before[0] + 1), "convergence_threshold": true},
	} {
		if err := applyGainControl(s.WriteDB, "p", params); err == nil {
			t.Errorf("applyGainControl(%v) succeeded, want a refusal", params)
		}
	}
	if after := read(); after != before {
		t.Errorf("batch_size, convergence_threshold = %v after refused calls, want %v unchanged", after, before)
	}
}
