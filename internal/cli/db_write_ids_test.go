package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

// Every db-write kind that names a row by id in its JSON refuses a payload
// whose id is missing, null, a string, a fraction or not positive, and the
// error names the field. The store holds finding 1 and run 1 throughout, so
// an id truncated or defaulted onto a real row would succeed.
func TestDBWrite_RefusesABadID(t *testing.T) {
	cases := []struct {
		name  string
		cmd   func() *cobra.Command
		key   string
		extra string
	}{
		{"complete_run", newWriteCompleteRunCmd, "run_id", `"summary":"s"`},
		{"fail_run", newWriteFailRunCmd, "run_id", `"error_summary":"e"`},
		{"update_finding", newWriteUpdateFindingCmd, "finding_id", `"finding":"rewritten"`},
		{"promote_finding", newWritePromoteFindingCmd, "finding_id", `"depends_on_ids":[2]`},
	}
	bad := []struct{ label, value string }{
		{"missing", ""}, {"null", "null"}, {"string", `"1"`}, {"fraction", "1.5"},
		{"zero", "0"}, {"negative", "-1"},
	}
	for _, c := range cases {
		for _, b := range bad {
			t.Run(c.name+"/"+b.label, func(t *testing.T) {
				seedIDRows(t)
				payload := "{" + c.extra
				if b.value != "" {
					payload += `,"` + c.key + `":` + b.value
				}
				payload += "}"
				err := runCmd(t, c.cmd(), payload)
				if err == nil || !strings.Contains(err.Error(), c.key) {
					t.Errorf("db-write %s %s: err=%v; want an error naming %s", c.name, payload, err, c.key)
				}
			})
		}
	}
}

// A well-formed id still reaches its row.
func TestDBWrite_AGoodIDReachesItsRow(t *testing.T) {
	s, runID, findingID, _ := seedIDRows(t)
	if err := runCmd(t, newWriteCompleteRunCmd(), fmt.Sprintf(`{"run_id":%d,"summary":"done"}`, runID)); err != nil {
		t.Fatalf("complete_run: %v", err)
	}
	var status string
	if err := s.ReadDB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status); err != nil || status != "completed" {
		t.Errorf("run %d status=%q (%v); want completed", runID, status, err)
	}
	if err := runCmd(t, newWriteUpdateFindingCmd(), fmt.Sprintf(`{"finding_id":%d,"finding":"rewritten"}`, findingID)); err != nil {
		t.Fatalf("update_finding: %v", err)
	}
	var text string
	if err := s.ReadDB.QueryRow(`SELECT finding FROM findings WHERE id=?`, findingID).Scan(&text); err != nil || text != "rewritten" {
		t.Errorf("finding %d text=%q (%v); want rewritten", findingID, text, err)
	}
}

// cascade_revert takes its id as an argument, and refuses "3abc" and "1.5"
// rather than reading them as 3 and 1.
func TestCascadeRevert_RefusesANonInteger(t *testing.T) {
	for _, arg := range []string{"1.5", "1abc", "0", "one"} {
		t.Run(arg, func(t *testing.T) {
			seedIDRows(t)
			if err := runCmd(t, newWriteCascadeRevertCmd(), arg); err == nil || !strings.Contains(err.Error(), "finding_id") {
				t.Errorf("cascade_revert %q: err=%v; want an error naming finding_id", arg, err)
			}
		})
	}
	_, _, findingID, _ := seedIDRows(t)
	if err := runCmd(t, newWriteCascadeRevertCmd(), fmt.Sprint(findingID)); err != nil {
		t.Errorf("cascade_revert %d: %v", findingID, err)
	}
}

// conflict refuses a missing finding id, naming the field, before the
// insert.
func TestWriteConflict_RefusesAMissingFindingID(t *testing.T) {
	s, _, a, b := seedIDRows(t)
	for _, payload := range []string{
		`{"wave":1,"description":"d"}`,
		fmt.Sprintf(`{"wave":1,"finding_a_id":%d,"description":"d"}`, a),
		fmt.Sprintf(`{"wave":1,"finding_b_id":%d,"description":"d"}`, b),
	} {
		if err := runCmd(t, newWriteConflictCmd(), payload); err == nil || !strings.Contains(err.Error(), "finding_") {
			t.Errorf("conflict %s: err=%v; want an error naming the finding ids", payload, err)
		}
	}
	if err := runCmd(t, newWriteConflictCmd(), fmt.Sprintf(`{"wave":1,"finding_a_id":%d,"finding_b_id":%d,"description":"d"}`, a, b)); err != nil {
		t.Fatalf("conflict with both ids: %v", err)
	}
	var n int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM conflicts WHERE finding_a_id=? AND finding_b_id=?`, a, b).Scan(&n); err != nil || n != 1 {
		t.Errorf("conflicts recorded for (%d,%d) = %d (%v); want 1", a, b, n, err)
	}
}

// seedIDRows gives a fresh store run 1, assumption finding 1 and definition
// finding 2.
func seedIDRows(t *testing.T) (s *db.Store, runID, assumption, definition int64) {
	t.Helper()
	s = useTestStore(t)
	runID, err := s.AgentRuns().AddAgentRun(1, "a", "researcher", "m", "p", nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	src := "https://example.test/source"
	for i, label := range []string{"assumption", "definition"} {
		id, err := s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: label, Finding: label + " finding", SourceURLs: &src})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			assumption = id
		} else {
			definition = id
		}
	}
	if runID != 1 || assumption != 1 || definition != 2 {
		t.Fatalf("seeded run %d and findings %d, %d; the payloads assume 1, 1 and 2", runID, assumption, definition)
	}
	return s, runID, assumption, definition
}
