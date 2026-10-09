package db

import (
	"testing"
)

func TestAddEvaluation_HappyPath(t *testing.T) {
	s := newTestStore(t)
	notes := "all good"
	err := s.Evaluations().AddEvaluation(1, 4, 4, 4, 4, 3, "COMPLETE", 0, 0, 0, &notes)
	if err != nil {
		t.Fatalf("AddEvaluation happy path: %v", err)
	}
}

func TestAddEvaluation_NilNotes(t *testing.T) {
	s := newTestStore(t)
	err := s.Evaluations().AddEvaluation(2, 5, 5, 5, 5, 5, "COMPLETE", 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("AddEvaluation with nil notes: %v", err)
	}
}

func TestAddEvaluation_TableDriven(t *testing.T) {
	notes := "test notes"
	cases := []struct {
		name        string
		wave        int
		coverage    int
		depth       int
		sources     int
		action      int
		mssInt      int
		verdict     string
		laundering  int
		untraceable int
		redundant   int
		notes       *string
		wantErr     bool
	}{
		{
			name: "COMPLETE verdict",
			wave: 1, coverage: 4, depth: 4, sources: 4, action: 4, mssInt: 3,
			verdict: "COMPLETE", laundering: 0, untraceable: 0, redundant: 0,
			notes: &notes, wantErr: false,
		},
		{
			name: "NEEDS_MORE_WORK verdict",
			wave: 2, coverage: 2, depth: 2, sources: 2, action: 2, mssInt: 2,
			verdict: "NEEDS_MORE_WORK", laundering: 1, untraceable: 2, redundant: 3,
			notes: nil, wantErr: false,
		},
		{
			name: "NEEDS_MINOR_FOLLOWUP verdict",
			wave: 3, coverage: 3, depth: 3, sources: 3, action: 3, mssInt: 3,
			verdict: "NEEDS_MINOR_FOLLOWUP", laundering: 0, untraceable: 0, redundant: 0,
			notes: nil, wantErr: false,
		},
		{
			name: "invalid verdict violates CHECK constraint",
			wave: 4, coverage: 3, depth: 3, sources: 3, action: 3, mssInt: 3,
			verdict: "INVALID_VERDICT", laundering: 0, untraceable: 0, redundant: 0,
			notes: nil, wantErr: true,
		},
		{
			name: "score out of range violates CHECK constraint",
			wave: 5, coverage: 99, depth: 3, sources: 3, action: 3, mssInt: 3,
			verdict: "COMPLETE", laundering: 0, untraceable: 0, redundant: 0,
			notes: nil, wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			err := s.Evaluations().AddEvaluation(
				tc.wave, tc.coverage, tc.depth, tc.sources, tc.action, tc.mssInt,
				tc.verdict, tc.laundering, tc.untraceable, tc.redundant, tc.notes,
			)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
