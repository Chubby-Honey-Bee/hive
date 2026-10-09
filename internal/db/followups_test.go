package db

import (
	"testing"
)

func TestAddFollowup_HappyPath(t *testing.T) {
	s := newTestStore(t)
	repo := s.Followups()

	if err := repo.AddFollowup(1, "agent-a", "what is the market size?", "important", intPtr(1), intPtr(2), nil, nil); err != nil {
		t.Fatalf("AddFollowup: %v", err)
	}

	var count int
	if err := s.WriteDB.QueryRow("SELECT COUNT(*) FROM followups").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 followup, got %d", count)
	}
}

func TestAddFollowup_NilCoordinates(t *testing.T) {
	s := newTestStore(t)
	repo := s.Followups()

	if err := repo.AddFollowup(1, "agent-b", "follow-up with no coords?", "critical", nil, nil, nil, nil); err != nil {
		t.Fatalf("AddFollowup nil coords: %v", err)
	}
}

func TestAddFollowup_AllCoordinates(t *testing.T) {
	s := newTestStore(t)
	repo := s.Followups()

	if err := repo.AddFollowup(2, "agent-c", "full coords?", "minor", intPtr(1), intPtr(2), intPtr(3), intPtr(4)); err != nil {
		t.Fatalf("AddFollowup all coords: %v", err)
	}
}

func TestAddFollowup_TableDriven(t *testing.T) {
	cases := []struct {
		name     string
		wave     int
		agent    string
		question string
		priority string
		d1, d2   *int
	}{
		{"basic", 1, "agent-x", "question one?", "important", intPtr(0), nil},
		{"wave zero", 0, "agent-y", "question two?", "critical", nil, nil},
		{"high wave", 99, "agent-z", "question three?", "minor", intPtr(5), intPtr(7)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			repo := s.Followups()
			if err := repo.AddFollowup(tc.wave, tc.agent, tc.question, tc.priority, tc.d1, tc.d2, nil, nil); err != nil {
				t.Fatalf("AddFollowup(%s): %v", tc.name, err)
			}
		})
	}
}

func TestAddFollowup_ClosedDB(t *testing.T) {
	s := newTestStore(t)
	repo := s.Followups()

	// Close the write DB to force an error path.
	s.WriteDB.Close()

	err := repo.AddFollowup(1, "agent-a", "question?", "important", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error after DB close, got nil")
	}
}
