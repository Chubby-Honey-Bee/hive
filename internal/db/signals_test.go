package db

import (
	"strings"
	"testing"
)

func TestEmitSignal_HappyPath(t *testing.T) {
	s := newTestStore(t)
	repo := s.Signals()

	wave := 1
	id, err := repo.EmitSignal("waggle_dance", nil, nil, nil, nil, nil, nil, nil, &wave)
	if err != nil {
		t.Fatalf("EmitSignal happy path: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive insert id, got %d", id)
	}

	// Verify row landed in the table.
	var count int
	s.ReadDB.QueryRow("SELECT COUNT(*) FROM signals WHERE id=?", id).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 row for id=%d, got %d", id, count)
	}
}

func TestEmitSignal_AllSignalTypes(t *testing.T) {
	s := newTestStore(t)
	repo := s.Signals()

	types := []string{
		"waggle_dance", "stop_signal", "alarm",
		"tremble_dance", "shaking_signal", "quorum", "qmp",
	}
	for _, st := range types {
		t.Run(st, func(t *testing.T) {
			id, err := repo.EmitSignal(st, nil, nil, nil, nil, nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("EmitSignal(%q): %v", st, err)
			}
			if id <= 0 {
				t.Fatalf("EmitSignal(%q): expected positive id, got %d", st, id)
			}
		})
	}
}

func TestEmitSignal_NilPayloadDefaultsToEmptyObject(t *testing.T) {
	s := newTestStore(t)
	repo := s.Signals()

	id, err := repo.EmitSignal("waggle_dance", nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("EmitSignal nil payload: %v", err)
	}

	var payloadJSON string
	s.ReadDB.QueryRow("SELECT payload_json FROM signals WHERE id=?", id).Scan(&payloadJSON)
	if payloadJSON != "{}" {
		t.Fatalf("expected payload_json='{}', got %q", payloadJSON)
	}
}

func TestEmitSignal_NonNilPayloadMarshaled(t *testing.T) {
	s := newTestStore(t)
	repo := s.Signals()

	payload := map[string]any{"key": "value", "count": 42}
	id, err := repo.EmitSignal("stop_signal", nil, nil, nil, nil, nil, nil, payload, nil)
	if err != nil {
		t.Fatalf("EmitSignal with payload: %v", err)
	}

	var payloadJSON string
	s.ReadDB.QueryRow("SELECT payload_json FROM signals WHERE id=?", id).Scan(&payloadJSON)
	if !strings.Contains(payloadJSON, "key") || !strings.Contains(payloadJSON, "value") {
		t.Fatalf("expected marshaled payload in db, got %q", payloadJSON)
	}
}

func TestEmitSignal_UnmarshalablePayloadReturnsError(t *testing.T) {
	s := newTestStore(t)
	repo := s.Signals()

	// Channels cannot be marshaled to JSON.
	badPayload := make(chan int)
	_, err := repo.EmitSignal("waggle_dance", nil, nil, nil, nil, nil, nil, badPayload, nil)
	if err == nil {
		t.Fatal("expected marshal error for channel payload, got nil")
	}
	if !strings.Contains(err.Error(), "marshal signal payload") {
		t.Fatalf("expected 'marshal signal payload' in error, got: %v", err)
	}
}

func TestEmitSignal_InvalidSignalTypeRejected(t *testing.T) {
	s := newTestStore(t)
	repo := s.Signals()

	_, err := repo.EmitSignal("not_a_valid_type", nil, nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected CHECK constraint error for invalid signal_type, got nil")
	}
	if !strings.Contains(err.Error(), "emit signal") {
		t.Fatalf("expected 'emit signal' in error, got: %v", err)
	}
}

func TestEmitSignal_WithOptionalFields(t *testing.T) {
	s := newTestStore(t)
	repo := s.Signals()

	src := "system"
	var srcID int64 = 42
	d1, d2 := 1, 2
	wave := 3

	id, err := repo.EmitSignal("alarm", &src, &srcID, &d1, &d2, nil, nil, map[string]any{"reason": "test"}, &wave)
	if err != nil {
		t.Fatalf("EmitSignal with optional fields: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	var gotSrc string
	var gotD1, gotWave int
	s.ReadDB.QueryRow(
		"SELECT source_type, target_d1, wave FROM signals WHERE id=?", id,
	).Scan(&gotSrc, &gotD1, &gotWave)

	if gotSrc != src {
		t.Errorf("source_type: got %q, want %q", gotSrc, src)
	}
	if gotD1 != d1 {
		t.Errorf("target_d1: got %d, want %d", gotD1, d1)
	}
	if gotWave != wave {
		t.Errorf("wave: got %d, want %d", gotWave, wave)
	}
}
