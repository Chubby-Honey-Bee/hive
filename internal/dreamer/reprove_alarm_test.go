package dreamer

import (
	"context"
	"testing"
)

// reprove raises the alarm — a guarantee's foundation is suspect — and not
// the tremble dance, which means a processing backlog. Its alarm says a
// dependency changed, so it does not stand in for settle's alarm when the
// chain later reaches an unknown: settle still records its own.
func TestReprove_RaisesAnAlarmThatDoesNotSilenceSettle(t *testing.T) {
	store := freshStore(t)
	dep := mustAddFinding(t, store, "assumption", "the site is dry", nil)
	gid := mustAddFinding(t, store, "guarantee", "the site suits the colony", []int64{dep})
	// The guarantee predates the edit below by more than the one-second
	// timestamp resolution, without sleeping.
	if _, err := store.WriteDB.Exec(`UPDATE findings SET created_at = datetime('now', '-10 seconds') WHERE id = ?`, gid); err != nil {
		t.Fatal(err)
	}
	if err := store.Findings().UpdateFinding(dep, map[string]any{"finding": "the site is damp"}); err != nil {
		t.Fatal(err)
	}

	count := func(source string) int {
		t.Helper()
		var n int
		if err := store.ReadDB.QueryRow(
			`SELECT COUNT(*) FROM signals WHERE signal_type = ? AND source_id = ? AND json_extract(payload_json, '$.source') = ?`,
			"alarm", gid, source).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := passReprove(context.Background(), store, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	if got := count("dreamer.reprove"); got != 1 {
		t.Fatalf("%d reprove alarms on guarantee %d, want 1", got, gid)
	}
	if got := countSignals(t, store, "tremble_dance"); got != 0 {
		t.Fatalf("reprove wrote %d tremble_dance signals, want none", got)
	}

	// The write path refuses to relabel dep unknown under gid; the laundering
	// settle repairs is written past that check, as a stored state would be.
	if _, err := store.WriteDB.Exec(`UPDATE findings SET mss_label = 'unknown', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, dep); err != nil {
		t.Fatal(err)
	}
	if _, err := passSettle(context.Background(), store, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	if got := count("dreamer.settle"); got != 1 {
		t.Fatalf("%d settle alarms on guarantee %d after reprove flagged it, want 1", got, gid)
	}
}

// reprove measures from its own last alarm, not from any alarm on the
// guarantee. settle writes its alarm with the same source type and id, so a
// settle alarm written after a dependency edit must not pass for reprove
// having seen that edit.
func TestReprove_ASettleAlarmDoesNotHideADependencyEdit(t *testing.T) {
	store := freshStore(t)
	dep := mustAddFinding(t, store, "assumption", "the site is dry", nil)
	gid := mustAddFinding(t, store, "guarantee", "the site suits the colony", []int64{dep})
	// Written past the write path's refusal, as a stored state would be.
	if _, err := store.WriteDB.Exec(`UPDATE findings SET mss_label = 'unknown', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, dep); err != nil {
		t.Fatal(err)
	}
	// Recommend-only: settle raises its alarm and leaves the guarantee be.
	if _, err := passSettle(context.Background(), store, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	// The guarantee is written, then its dependency is edited, then settle
	// raises its alarm, each more than the one-second resolution apart.
	for _, step := range []struct {
		q  string
		id int64
	}{
		{`UPDATE findings SET created_at = datetime('now', '-10 seconds') WHERE id = ?`, gid},
		{`UPDATE findings SET updated_at = datetime('now', '-5 seconds') WHERE id = ?`, dep},
		{`UPDATE signals SET created_at = datetime('now', '-2 seconds')
		  WHERE signal_type = 'alarm' AND source_id = ? AND json_extract(payload_json, '$.source') = 'dreamer.settle'`, gid},
	} {
		res, err := store.WriteDB.Exec(step.q, step.id)
		if err != nil {
			t.Fatal(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("setup %q touched %d rows, want 1", step.q, n)
		}
	}

	if _, err := passReprove(context.Background(), store, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := store.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM signals WHERE signal_type = 'alarm' AND source_id = ? AND json_extract(payload_json, '$.source') = 'dreamer.reprove'`,
		gid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d reprove alarms on guarantee %d after its dependency was edited, want 1", n, gid)
	}
}
