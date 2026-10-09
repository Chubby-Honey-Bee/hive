package cli

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

// Every db-write kind that takes a JSON payload refuses a key it does not
// read, naming the key and listing the keys it takes, and writes nothing,
// so {"agent_id":1} cannot write a finding with no agent. Each case's
// payload holds every key its kind reads, so it must be accepted, and the
// list the refusal gives must be exactly its keys. Keys match exactly, so
// "URL" or "Wave" is refused, not read as "url" or "wave". The kinds
// db-write shares with chb_db_write are refused by the store's write path,
// whose error calls them fields.
func TestDBWrite_RefusesAnUnknownKey(t *testing.T) {
	shared := map[string]bool{"finding": true, "gap": true, "source": true, "resolve_gap": true, "resolve_conflict": true}
	cases := []struct {
		kind  string
		cmd   func() *cobra.Command
		table string
		full  string
	}{
		{"dimension", newWriteDimensionCmd, "dimensions",
			`{"name":"component","description":"d","values_json":["a","b"]}`},
		{"finding", newWriteFindingCmd, "findings",
			`{"wave":1,"agent":"a","mss_label":"guarantee","finding":"f","evidence":"e","source_urls":["https://example.test/f"],"depends_on_ids":[2],
			"d1":0,"d2":0,"d3":0,"d4":0,"d5":0,"d6":0,"d7":0,"d8":0}`},
		{"update_finding", newWriteUpdateFindingCmd, "findings",
			`{"finding_id":1,"mss_label":"assumption","finding":"rewritten","evidence":"e","source_urls":"https://example.test/u","depends_on_ids":[2]}`},
		{"source", newWriteSourceCmd, "sources",
			`{"url":"https://example.test/s","title":"t","agent":"a","wave":1,"contribution":"c","primary_source":1}`},
		{"gap", newWriteGapCmd, "gaps",
			`{"wave":1,"agent":"a","description":"g","priority":"critical","d1":0,"d2":0,"d3":0,"d4":0}`},
		{"resolve_gap", newWriteResolveGapCmd, "gaps",
			`{"gap_id":1,"wave":1,"agent":"a","finding_id":1}`},
		{"conflict", newWriteConflictCmd, "conflicts",
			`{"wave":1,"finding_a_id":1,"finding_b_id":2,"description":"d"}`},
		{"conflict_winner", newWriteConflictWinnerCmd, "conflicts",
			`{"conflict_id":1,"winner_finding_id":2}`},
		{"resolve_conflict", newWriteResolveConflictCmd, "conflicts",
			`{"conflict_id":1,"wave":1,"resolution":"r"}`},
		{"followup", newWriteFollowupCmd, "followups",
			`{"wave":1,"agent":"a","question":"q","priority":"minor","d1":0,"d2":0,"d3":0,"d4":0}`},
		{"agent_run", newWriteAgentRunCmd, "agent_runs",
			`{"wave":1,"agent_name":"a","agent_type":"verifier","model":"m","prompt_summary":"p","target_d1":0,"target_d2":0,"target_d3":0,"target_d4":0}`},
		{"complete_run", newWriteCompleteRunCmd, "agent_runs",
			`{"run_id":1,"summary":"s","tool_uses":1,"duration_ms":2,"total_tokens":3}`},
		{"fail_run", newWriteFailRunCmd, "agent_runs",
			`{"run_id":1,"error_summary":"e"}`},
		{"evaluation", newWriteEvaluationCmd, "evaluations",
			`{"wave":1,"coverage":4,"depth":4,"sources":4,"actionability":4,"mss_integrity":4,"verdict":"COMPLETE","laundering":0,"untraceable":0,"redundant":0,"notes":"n"}`},
		{"signal", newWriteSignalCmd, "signals",
			`{"signal_type":"shaking_signal","source_type":"system","source_id":1,"payload":{"k":"v"},"wave":1,"target_d1":0,"target_d2":0,"target_d3":0,"target_d4":0}`},
		{"promote_finding", newWritePromoteFindingCmd, "findings",
			`{"finding_id":1,"depends_on_ids":[2]}`},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			var full map[string]any
			if err := json.Unmarshal([]byte(c.full), &full); err != nil {
				t.Fatal(err)
			}
			keys := map[string]bool{}
			for k := range full {
				keys[k] = true
			}
			want := slices.Sorted(maps.Keys(keys))

			for _, extra := range [][]string{{"agent_id"}, {"status", "agent_id"}, {strings.ToUpper(want[0])}} {
				s := seedKeyRows(t)
				bad := make(map[string]any, len(full)+len(extra))
				for k, v := range full {
					bad[k] = v
				}
				for _, k := range extra {
					bad[k] = 1
				}
				payload, _ := json.Marshal(bad)
				before := tableSnapshot(t, s, c.table)
				err := runCmd(t, c.cmd(), string(payload))
				if err == nil {
					t.Fatalf("db-write %s %s: accepted; want the unknown keys %v refused", c.kind, payload, extra)
				}
				msg := err.Error()
				for _, k := range extra {
					if !strings.Contains(msg, fmt.Sprintf("%q", k)) {
						t.Errorf("db-write %s: error %q does not name the unknown key %q", c.kind, msg, k)
					}
				}
				listed := "accepted keys: "
				if shared[c.kind] {
					listed = "accepted fields: "
				}
				_, list, ok := strings.Cut(msg, listed)
				if !ok {
					t.Fatalf("db-write %s: error %q lists no accepted keys", c.kind, msg)
				}
				got := strings.Split(list, ", ")
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Errorf("db-write %s: accepted keys %v; want the keys it reads, %v", c.kind, got, want)
				}
				if after := tableSnapshot(t, s, c.table); after != before {
					t.Errorf("db-write %s wrote to %s although it refused the payload:\nbefore %s\nafter  %s", c.kind, c.table, before, after)
				}
			}

			seedKeyRows(t)
			if err := runCmd(t, c.cmd(), c.full); err != nil {
				t.Errorf("db-write %s %s: %v; want every key it reads accepted", c.kind, c.full, err)
			}
		})
	}
}

// seedKeyRows gives a fresh store run 1, assumption finding 1, definition
// finding 2, an open gap 1 and an open conflict 1 between the two findings.
func seedKeyRows(t *testing.T) *db.Store {
	t.Helper()
	s, _, a, b := seedIDRows(t)
	if err := s.Gaps().AddGap(1, "a", "open gap", "important", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Conflicts().AddConflict(1, a, b, "open conflict"); err != nil {
		t.Fatal(err)
	}
	var gaps, conflicts int
	if err := s.ReadDB.QueryRow(`SELECT (SELECT MAX(id) FROM gaps), (SELECT MAX(id) FROM conflicts)`).Scan(&gaps, &conflicts); err != nil {
		t.Fatal(err)
	}
	if gaps != 1 || conflicts != 1 {
		t.Fatalf("seeded gap %d and conflict %d; the payloads assume 1 and 1", gaps, conflicts)
	}
	return s
}

// tableSnapshot renders every row of table, in rowid order.
func tableSnapshot(t *testing.T, s *db.Store, table string) string {
	t.Helper()
	rows, err := s.ReadDB.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%v\n", vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
