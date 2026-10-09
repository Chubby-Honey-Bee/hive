package db

import (
	"context"
	"testing"
)

// A transaction whose context is cancelled still rolls back: ROLLBACK runs
// outside that context. Under it the driver would return before sending
// ROLLBACK, and the write pool's one connection would go back inside BEGIN
// IMMEDIATE, where the next write would join the abandoned transaction and
// the next transaction could not begin.
func TestTx_RollbackAfterItsContextIsCancelled(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := s.BeginImmediate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO gaps (wave, agent, description) VALUES (1, 'a', 'abandoned')`); err != nil {
		t.Fatal(err)
	}
	cancel()
	tx.Rollback()

	if err := s.Gaps().AddGap(1, "a", "after", "important", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	var abandoned, after int
	if err := s.ReadDB.QueryRow(`SELECT
		(SELECT COUNT(*) FROM gaps WHERE description = 'abandoned'),
		(SELECT COUNT(*) FROM gaps WHERE description = 'after')`).Scan(&abandoned, &after); err != nil {
		t.Fatal(err)
	}
	if abandoned != 0 || after != 1 {
		t.Errorf("another connection sees %d abandoned and %d later gap(s); want 0 and 1, the later write committed on its own", abandoned, after)
	}
	next, err := s.BeginImmediate(context.Background())
	if err != nil {
		t.Fatalf("the next transaction: %v", err)
	}
	next.Rollback()
}
