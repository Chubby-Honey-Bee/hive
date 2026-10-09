package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/spf13/cobra"
)

// toolWrite writes fields as a record of kind through chb_db_write, the
// runner's in-process tool, and returns its reply and whether it refused.
func toolWrite(t *testing.T, s *db.Store, kind string, fields map[string]any) (string, bool) {
	t.Helper()
	reg := runner.NewToolRegistry(t.TempDir(), s, runner.NewFSRecorder())
	out, refused, err := reg.Invoke(context.Background(), "chb_db_write", map[string]any{"kind": kind, "fields": fields})
	if err != nil {
		t.Fatal(err)
	}
	return out, refused
}

// db-write finding refuses a wave that is not a whole number with the
// refusal chb_db_write gives the same fields, so chb exits non-zero, and
// writes nothing.
func TestDBWriteFinding_RefusesAWaveThatIsNotAWholeNumber(t *testing.T) {
	for _, wave := range []string{`1.5`, `"2"`} {
		t.Run(wave, func(t *testing.T) {
			s := useTestStore(t)
			payload := `{"wave":` + wave + `,"agent":"a","mss_label":"definition","finding":"f"}`
			var fields map[string]any
			if err := json.Unmarshal([]byte(payload), &fields); err != nil {
				t.Fatal(err)
			}
			refusal, refused := toolWrite(t, s, "finding", fields)
			if !refused {
				t.Fatalf("chb_db_write accepted wave %s, so there is no refusal to compare: %s", wave, refusal)
			}
			err := runCmd(t, newWriteFindingCmd(), payload)
			if err == nil {
				t.Fatalf("db-write finding with wave %s: accepted; want chb_db_write's refusal %q", wave, refusal)
			}
			if err.Error() != refusal {
				t.Errorf("db-write finding with wave %s: %q; want chb_db_write's refusal %q", wave, err, refusal)
			}
			var n int
			if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM findings`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("%d finding(s) written; want none", n)
			}
		})
	}
}

// db-write gap stores the priority chb_db_write stores for the same fields:
// high and none are important, medium and low minor.
func TestDBWriteGap_StoresThePriorityTheToolStores(t *testing.T) {
	for _, priority := range []string{"high", "medium", "low", "critical", "important", "minor", ""} {
		t.Run("priority="+priority, func(t *testing.T) {
			s := useTestStore(t)
			fields := map[string]any{"wave": 1.0, "agent": "a", "description": "g"}
			if priority != "" {
				fields["priority"] = priority
			}
			if out, refused := toolWrite(t, s, "gap", fields); refused {
				t.Fatalf("chb_db_write refused priority %q: %s", priority, out)
			}
			payload, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := runCmd(t, newWriteGapCmd(), string(payload)); err != nil {
				t.Fatalf("db-write gap with priority %q: %v", priority, err)
			}
			var tool, cli string
			if err := s.ReadDB.QueryRow(`SELECT (SELECT priority FROM gaps WHERE id = 1), (SELECT priority FROM gaps WHERE id = 2)`).Scan(&tool, &cli); err != nil {
				t.Fatal(err)
			}
			if cli != tool {
				t.Errorf("priority %q: db-write gap stored %q, chb_db_write %q", priority, cli, tool)
			}
		})
	}
}

// Each kind db-write shares with chb_db_write prints its own line: a
// finding's id, nothing for a gap or a source, and what a resolve closed.
func TestDBWrite_SharedKindsPrintTheirOwnLine(t *testing.T) {
	s, _, a, _ := seedIDRows(t)
	if err := s.Gaps().AddGap(1, "a", "open gap", "important", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Conflicts().AddConflict(1, a, 2, "open conflict"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cmd     func() *cobra.Command
		payload string
		want    string
	}{
		{newWriteFindingCmd, `{"wave":1,"agent":"a","mss_label":"definition","finding":"third"}`, "Finding id=3\n"},
		{newWriteGapCmd, `{"wave":1,"agent":"a","description":"second gap"}`, ""},
		{newWriteSourceCmd, `{"url":"https://example.test/s","agent":"a","wave":1}`, ""},
		{newWriteResolveGapCmd, fmt.Sprintf(`{"gap_id":1,"wave":2,"agent":"a","finding_id":%d}`, a), fmt.Sprintf("gap 1 resolved by finding %d\n", a)},
		{newWriteResolveConflictCmd, `{"conflict_id":1,"wave":2,"resolution":"finding 1 survives"}`, "conflict 1 resolved\n"},
	}
	for _, c := range cases {
		cmd := c.cmd()
		cmd.SetArgs([]string{c.payload})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		out, err := captureStdout(t, cmd.Execute)
		if err != nil {
			t.Fatalf("db-write %s %s: %v", cmd.Name(), c.payload, err)
		}
		if out != c.want {
			t.Errorf("db-write %s printed %q; want %q", cmd.Name(), out, c.want)
		}
	}
}

// findingRefusal is chb_db_write's refusal of a finding whose field key holds
// raw, a JSON value: the wording every write surface gives that field.
func findingRefusal(t *testing.T, s *db.Store, key, raw string) string {
	t.Helper()
	fields := map[string]any{"wave": 1.0, "agent": "a", "mss_label": "definition", "finding": "f"}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	fields[key] = v
	refusal, refused := toolWrite(t, s, "finding", fields)
	if !refused {
		t.Fatalf("chb_db_write accepted a finding with %s %s, so there is no refusal to compare: %s", key, raw, refusal)
	}
	return refusal
}

// rowCount is the number of rows in table.
func rowCount(t *testing.T, s *db.Store, table string) int {
	t.Helper()
	var n int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// db-write followup refuses a wave that is not a whole number with the
// refusal chb_db_write gives a finding with the same wave, so chb exits
// non-zero, and writes nothing.
func TestDBWriteFollowup_RefusesAWaveThatIsNotAWholeNumber(t *testing.T) {
	for _, wave := range []string{`1.5`, `"2"`} {
		t.Run(wave, func(t *testing.T) {
			s := useTestStore(t)
			refusal := findingRefusal(t, s, "wave", wave)
			err := runCmd(t, newWriteFollowupCmd(), `{"wave":`+wave+`,"agent":"a","question":"what next?"}`)
			if err == nil || err.Error() != refusal {
				t.Errorf("db-write followup with wave %s: %v; want chb_db_write's refusal %q", wave, err, refusal)
			}
			if n := rowCount(t, s, "followups"); n != 0 {
				t.Errorf("%d followup(s) written; want none", n)
			}
		})
	}
}

// A text field given something other than a string is refused, by
// db-write finding and chb_db_write alike and with one error that names the
// field, and nothing is written. Both read "agent": 5 as no agent and wrote
// the finding.
func TestDBWriteFinding_RefusesATextFieldThatIsNotAString(t *testing.T) {
	s := useTestStore(t)
	refusal, refused := toolWrite(t, s, "finding", map[string]any{"wave": 1.0, "agent": 5.0, "mss_label": "definition", "finding": "f"})
	if !refused {
		t.Errorf("chb_db_write accepted agent 5: %s", refusal)
	} else if !strings.Contains(refusal, "agent") {
		t.Errorf("chb_db_write's refusal %q does not name the field", refusal)
	}
	err := runCmd(t, newWriteFindingCmd(), `{"wave":1,"agent":5,"mss_label":"definition","finding":"f"}`)
	if err == nil || err.Error() != refusal {
		t.Errorf("db-write finding with agent 5: %v; want chb_db_write's refusal %q", err, refusal)
	}
	if n := rowCount(t, s, "findings"); n != 0 {
		t.Errorf("%d finding(s) written; want none", n)
	}
}

// typeCase is one db-write kind for the field-type test: the table it
// writes, a payload holding every field it reads, and which of them hold an
// integer and which text.
type typeCase struct {
	cmd           func() *cobra.Command
	table         string
	full          string
	integer, text []string
}

// typeCases are every db-write kind that takes a JSON payload.
var typeCases = []typeCase{
	{newWriteDimensionCmd, "dimensions", `{"name":"component","description":"d","values_json":["a","b"]}`,
		nil, []string{"name", "description"}},
	{newWriteFindingCmd, "findings", `{"wave":1,"agent":"a","mss_label":"definition","finding":"f","evidence":"e","d1":0}`,
		[]string{"wave", "d1"}, []string{"agent", "mss_label", "finding", "evidence"}},
	{newWriteUpdateFindingCmd, "findings", `{"finding_id":1,"mss_label":"assumption","finding":"rewritten","evidence":"e"}`,
		[]string{"finding_id"}, []string{"mss_label", "finding", "evidence"}},
	{newWriteSourceCmd, "sources", `{"url":"https://example.test/s","title":"t","agent":"a","wave":1,"contribution":"c","primary_source":1}`,
		[]string{"wave", "primary_source"}, []string{"url", "title", "agent", "contribution"}},
	{newWriteGapCmd, "gaps", `{"wave":1,"agent":"a","description":"g","priority":"critical","d1":0}`,
		[]string{"wave", "d1"}, []string{"agent", "description", "priority"}},
	{newWriteResolveGapCmd, "gaps", `{"gap_id":1,"wave":1,"agent":"a","finding_id":1}`,
		[]string{"gap_id", "wave", "finding_id"}, []string{"agent"}},
	{newWriteConflictCmd, "conflicts", `{"wave":1,"finding_a_id":1,"finding_b_id":2,"description":"d"}`,
		[]string{"wave", "finding_a_id", "finding_b_id"}, []string{"description"}},
	{newWriteConflictWinnerCmd, "conflicts", `{"conflict_id":1,"winner_finding_id":2}`,
		[]string{"conflict_id", "winner_finding_id"}, nil},
	{newWriteResolveConflictCmd, "conflicts", `{"conflict_id":1,"wave":1,"resolution":"r"}`,
		[]string{"conflict_id", "wave"}, []string{"resolution"}},
	{newWriteFollowupCmd, "followups", `{"wave":1,"agent":"a","question":"q","priority":"minor","d1":0,"d2":0,"d3":0,"d4":0}`,
		[]string{"wave", "d1", "d2", "d3", "d4"}, []string{"agent", "question", "priority"}},
	{newWriteAgentRunCmd, "agent_runs", `{"wave":1,"agent_name":"a","agent_type":"verifier","model":"m","prompt_summary":"p","target_d1":0,"target_d2":0,"target_d3":0,"target_d4":0}`,
		[]string{"wave", "target_d1", "target_d2", "target_d3", "target_d4"}, []string{"agent_name", "agent_type", "model", "prompt_summary"}},
	{newWriteCompleteRunCmd, "agent_runs", `{"run_id":1,"summary":"s","tool_uses":1,"duration_ms":2,"total_tokens":3}`,
		[]string{"run_id", "tool_uses", "duration_ms", "total_tokens"}, []string{"summary"}},
	{newWriteFailRunCmd, "agent_runs", `{"run_id":1,"error_summary":"e"}`,
		[]string{"run_id"}, []string{"error_summary"}},
	{newWriteEvaluationCmd, "evaluations", `{"wave":1,"coverage":4,"depth":4,"sources":4,"actionability":4,"mss_integrity":4,"verdict":"COMPLETE","laundering":0,"untraceable":0,"redundant":0,"notes":"n"}`,
		[]string{"wave", "coverage", "depth", "sources", "actionability", "mss_integrity", "laundering", "untraceable", "redundant"}, []string{"verdict", "notes"}},
	{newWriteSignalCmd, "signals", `{"signal_type":"shaking_signal","source_type":"system","source_id":1,"payload":{"k":"v"},"wave":1,"target_d1":0,"target_d2":0,"target_d3":0,"target_d4":0}`,
		[]string{"source_id", "wave", "target_d1", "target_d2", "target_d3", "target_d4"}, []string{"signal_type", "source_type"}},
	{newWritePromoteFindingCmd, "findings", `{"finding_id":1,"depends_on_ids":[2]}`,
		[]string{"finding_id"}, nil},
}

// Every db-write kind holds each integer field to a whole number and each
// text field to a string. It refuses the payload with the error chb_db_write
// gives a finding's wave or agent, with the field's name in its place, and
// writes nothing. Each case's payload holds every field its kind reads and
// spoils one. The kinds db-write decoded itself turned 1.5 into 1 and 5 into
// "", or refused with the JSON decoder's error.
func TestDBWrite_EveryKindRefusesAFieldOfTheWrongType(t *testing.T) {
	ref := useTestStore(t)
	whole, text := findingRefusal(t, ref, "wave", `1.5`), findingRefusal(t, ref, "agent", `5`)
	for _, c := range typeCases {
		for _, key := range c.integer {
			checkWrongType(t, c, key, 1.5, strings.Replace(whole, "wave", key, 1))
		}
		for _, key := range c.text {
			checkWrongType(t, c, key, 5, strings.Replace(text, "agent", key, 1))
		}
	}
}

// checkWrongType runs c's payload with key set to value and checks it is
// refused with want, leaving c's table as it was.
func checkWrongType(t *testing.T, c typeCase, key string, value any, want string) {
	t.Run(c.cmd().Name()+"/"+key, func(t *testing.T) {
		s := seedKeyRows(t)
		var fields map[string]any
		if err := json.Unmarshal([]byte(c.full), &fields); err != nil {
			t.Fatal(err)
		}
		fields[key] = value
		payload, _ := json.Marshal(fields)
		before := tableSnapshot(t, s, c.table)
		if err := runCmd(t, c.cmd(), string(payload)); err == nil || err.Error() != want {
			t.Errorf("db-write %s %s: %v; want %q", c.cmd().Name(), payload, err, want)
		}
		if after := tableSnapshot(t, s, c.table); after != before {
			t.Errorf("db-write %s %s changed %s although it was refused:\nbefore %s\nafter  %s", c.cmd().Name(), payload, c.table, before, after)
		}
	})
}
