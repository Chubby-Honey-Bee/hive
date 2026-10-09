package cli

import (
	"bytes"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

func runCmd(t *testing.T, cmd *cobra.Command, args ...string) error {
	t.Helper()
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	_, err := captureStdout(t, cmd.Execute)
	return err
}

// update_finding and promote_finding take depends_on_ids as a JSON array or
// a JSON-string array, like db-write finding, and store the string form as
// the list it holds.
func TestUpdateAndPromote_AcceptTheStringForm(t *testing.T) {
	s := useTestStore(t)
	add := func(label string) int64 {
		t.Helper()
		id, err := s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: label, Finding: label + " finding"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	def1, asm, def3, asm4 := add("definition"), add("assumption"), add("definition"), add("assumption")
	stored := func(id int64) (string, sql.NullString) {
		t.Helper()
		var label string
		var deps sql.NullString
		if err := s.ReadDB.QueryRow(`SELECT mss_label, depends_on_ids FROM findings WHERE id=?`, id).Scan(&label, &deps); err != nil {
			t.Fatal(err)
		}
		return label, deps
	}

	for _, form := range []string{fmt.Sprintf(`"[%d]"`, def3), fmt.Sprintf(`[%d]`, def3)} {
		if err := runCmd(t, newWriteUpdateFindingCmd(), fmt.Sprintf(`{"finding_id":%d,"depends_on_ids":%s}`, asm, form)); err != nil {
			t.Fatalf("update_finding with depends_on_ids %s: %v", form, err)
		}
		if _, deps := stored(asm); deps.String != fmt.Sprintf("[%d]", def3) {
			t.Errorf("depends_on_ids %s stored as %q; want [%d]", form, deps.String, def3)
		}
	}

	if err := runCmd(t, newWriteUpdateFindingCmd(),
		fmt.Sprintf(`{"finding_id":%d,"mss_label":"guarantee","depends_on_ids":"[%d]"}`, asm, def3)); err != nil {
		t.Fatalf("update_finding to guarantee with the string form: %v", err)
	}
	if label, deps := stored(asm); label != "guarantee" || deps.String != fmt.Sprintf("[%d]", def3) {
		t.Errorf("after update: label=%s deps=%q; want guarantee [%d]", label, deps.String, def3)
	}

	if err := runCmd(t, newWritePromoteFindingCmd(), fmt.Sprintf(`{"finding_id":%d,"depends_on_ids":"[%d]"}`, asm4, def1)); err != nil {
		t.Fatalf("promote_finding with the string form: %v", err)
	}
	if label, deps := stored(asm4); label != "guarantee" || deps.String != fmt.Sprintf("[%d]", def1) {
		t.Errorf("after promote: label=%s deps=%q; want guarantee [%d]", label, deps.String, def1)
	}

	// An empty list clears a non-guarantee's dependencies to NULL, and is
	// refused on a guarantee, which must name what it rests on.
	if err := runCmd(t, newWriteUpdateFindingCmd(), fmt.Sprintf(`{"finding_id":%d,"depends_on_ids":[%d]}`, def1, def3)); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, newWriteUpdateFindingCmd(), fmt.Sprintf(`{"finding_id":%d,"depends_on_ids":"[]"}`, def1)); err != nil {
		t.Fatalf("clearing a definition's dependencies: %v", err)
	}
	if _, deps := stored(def1); deps.Valid {
		t.Errorf("cleared depends_on_ids stored as %q; want NULL", deps.String)
	}
	if err := runCmd(t, newWriteUpdateFindingCmd(), fmt.Sprintf(`{"finding_id":%d,"depends_on_ids":[]}`, asm)); err == nil {
		t.Error("clearing a guarantee's dependencies was accepted")
	}
	if err := runCmd(t, newWritePromoteFindingCmd(), fmt.Sprintf(`{"finding_id":%d,"depends_on_ids":"[]"}`, def1)); err == nil {
		t.Error("promote_finding with no dependencies was accepted")
	}
}

// A label list returns at most DefaultProbeLimit rows. At exactly that many
// it says so on stderr, since a full page cannot be told from the whole
// label.
func TestReadByLabel_NoticeAtTheCap(t *testing.T) {
	for _, n := range []int{db.DefaultProbeLimit - 1, db.DefaultProbeLimit, db.DefaultProbeLimit + 1} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := useTestStore(t)
			tx, err := s.WriteDB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < n; i++ {
				if _, err := tx.Exec(`INSERT INTO findings (wave, agent, mss_label, finding) VALUES (1, 'a', 'assumption', ?)`, fmt.Sprint(i)); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			cmd := newReadByLabelCmd("assumptions", "assumption")
			var stderr bytes.Buffer
			cmd.SetErr(&stderr)
			if err := runCmd(t, cmd); err != nil {
				t.Fatal(err)
			}
			wantNotice := n >= db.DefaultProbeLimit
			if got := strings.Contains(stderr.String(), "there may be more"); got != wantNotice {
				t.Errorf("%d assumptions: notice=%v (stderr %q); want %v", n, got, stderr.String(), wantNotice)
			}
			if !strings.Contains(cmd.Short, fmt.Sprint(db.DefaultProbeLimit)) {
				t.Errorf("help %q does not state the %d cap", cmd.Short, db.DefaultProbeLimit)
			}
		})
	}
}
