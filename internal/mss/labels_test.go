package mss

import (
	"errors"
	"testing"
)

func TestLabel_Valid(t *testing.T) {
	for _, tc := range []struct {
		l    Label
		want bool
	}{
		{Definition, true},
		{Guarantee, true},
		{Assumption, true},
		{Unknown, true},
		{Label(""), false},
		{Label("invalid"), false},
		{Label("DEFINITION"), false}, // case-sensitive
	} {
		if got := tc.l.Valid(); got != tc.want {
			t.Errorf("Label(%q).Valid() = %v, want %v", tc.l, got, tc.want)
		}
	}
}

func TestMSSError_Error(t *testing.T) {
	e := &MSSError{
		FindingID: 42,
		Label:     Guarantee,
		Reason:    "test reason",
	}
	want := "MSS error (finding 42, label guarantee): test reason"
	if got := e.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMSSError_UnwrapsToSentinel(t *testing.T) {
	e := &MSSError{Err: ErrLaundering}
	if !errors.Is(e, ErrLaundering) {
		t.Errorf("errors.Is(e, ErrLaundering) = false")
	}

	e2 := &MSSError{Err: ErrMissingDeps}
	if errors.Is(e2, ErrLaundering) {
		t.Errorf("errors.Is wrongly matched ErrLaundering on ErrMissingDeps")
	}
	if !errors.Is(e2, ErrMissingDeps) {
		t.Errorf("errors.Is(e2, ErrMissingDeps) = false")
	}
}
