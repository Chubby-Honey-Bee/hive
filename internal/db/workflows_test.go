package db

import (
	"database/sql"
	"errors"
	"testing"
)

// seedRepairAttempt seeds a workflow run + repair attempt and returns (runID, repairID).
func seedRepairAttempt(t *testing.T, s *Store) (int64, int64) {
	t.Helper()
	runID := seedWorkflowRun(t, s, "repair-node")
	repairID, err := s.Workflows().RecordRepairAttempt(runID, "repair-node", 1, "test failure", "fix prompt", "model-a", "openai", "accept")
	if err != nil {
		t.Fatalf("RecordRepairAttempt: %v", err)
	}
	return runID, repairID
}

// seedWorkflowRun is a helper that creates a workflow run with one node
// seeded as "pending". Returns the run ID.
func seedWorkflowRun(t *testing.T, s *Store, nodeName string) int64 {
	t.Helper()
	runID, err := s.Workflows().CreateWorkflowRun(
		"test-workflow", 1, "---", "{}",
		[]NodeSeed{{Name: nodeName, Type: "agent"}},
	)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return runID
}

func TestCreateWorkflowRun(t *testing.T) {
	t.Run("table-driven happy path: run row and node states are created", func(t *testing.T) {
		cases := []struct {
			name       string
			wfName     string
			version    int
			defnYAML   string
			inputsJSON string
			nodes      []NodeSeed
			wantNodes  int
		}{
			{
				name:       "no nodes: run row created, zero node states",
				wfName:     "wf-empty",
				version:    1,
				defnYAML:   "---",
				inputsJSON: "{}",
				nodes:      []NodeSeed{},
				wantNodes:  0,
			},
			{
				name:       "single node: run row and one node state created",
				wfName:     "wf-single",
				version:    2,
				defnYAML:   "def: yaml",
				inputsJSON: `{"key":"val"}`,
				nodes:      []NodeSeed{{Name: "step-1", Type: "agent"}},
				wantNodes:  1,
			},
			{
				name:       "multiple nodes: all node states seeded as pending",
				wfName:     "wf-multi",
				version:    3,
				defnYAML:   "multi: true",
				inputsJSON: `{"x":1}`,
				nodes: []NodeSeed{
					{Name: "a", Type: "agent"},
					{Name: "b", Type: "parallel_fan"},
					{Name: "c", Type: "decision"},
				},
				wantNodes: 3,
			},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				s := newTestStore(t)
				runID, err := s.Workflows().CreateWorkflowRun(tc.wfName, tc.version, tc.defnYAML, tc.inputsJSON, tc.nodes)
				if err != nil {
					t.Fatalf("CreateWorkflowRun: %v", err)
				}
				if runID <= 0 {
					t.Errorf("expected positive run ID, got %d", runID)
				}

				// Verify the workflow_runs row.
				var storedName string
				var storedVersion int
				var storedState string
				row := s.ReadDB.QueryRow(
					`SELECT workflow_name, workflow_version, state_json FROM workflow_runs WHERE id=?`, runID,
				)
				if err := row.Scan(&storedName, &storedVersion, &storedState); err != nil {
					t.Fatalf("reading workflow_runs: %v", err)
				}
				if storedName != tc.wfName {
					t.Errorf("workflow_name: want %q, got %q", tc.wfName, storedName)
				}
				if storedVersion != tc.version {
					t.Errorf("workflow_version: want %d, got %d", tc.version, storedVersion)
				}
				// state_json starts as a copy of inputs_json (engine convention).
				if storedState != tc.inputsJSON {
					t.Errorf("state_json: want %q (copy of inputs_json), got %q", tc.inputsJSON, storedState)
				}

				// Verify node states.
				var nodeCount int
				s.ReadDB.QueryRow(
					`SELECT COUNT(*) FROM workflow_node_states WHERE run_id=?`, runID,
				).Scan(&nodeCount)
				if nodeCount != tc.wantNodes {
					t.Errorf("node count: want %d, got %d", tc.wantNodes, nodeCount)
				}

				// All seeded nodes must start as 'pending'.
				var nonPending int
				s.ReadDB.QueryRow(
					`SELECT COUNT(*) FROM workflow_node_states WHERE run_id=? AND status != 'pending'`, runID,
				).Scan(&nonPending)
				if nonPending != 0 {
					t.Errorf("expected all nodes pending, got %d non-pending", nonPending)
				}
			})
		}
	})

	t.Run("sequential runs get distinct IDs", func(t *testing.T) {
		s := newTestStore(t)
		idA, err := s.Workflows().CreateWorkflowRun("wf", 1, "---", "{}", []NodeSeed{{Name: "n", Type: "agent"}})
		if err != nil {
			t.Fatalf("first CreateWorkflowRun: %v", err)
		}
		idB, err := s.Workflows().CreateWorkflowRun("wf", 1, "---", "{}", []NodeSeed{{Name: "n", Type: "agent"}})
		if err != nil {
			t.Fatalf("second CreateWorkflowRun: %v", err)
		}
		if idA == idB {
			t.Errorf("expected distinct run IDs, both returned %d", idA)
		}
	})

	t.Run("error path: closed write DB causes Begin() to fail", func(t *testing.T) {
		s := newTestStore(t)
		s.WriteDB.Close()

		_, err := s.Workflows().CreateWorkflowRun("wf", 1, "---", "{}", []NodeSeed{{Name: "n", Type: "agent"}})
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})

	t.Run("error path: duplicate node name triggers UNIQUE constraint on node seed", func(t *testing.T) {
		s := newTestStore(t)
		// workflow_node_states has UNIQUE(run_id, node_name); two nodes with the same name
		// in one call must fail on the second INSERT.
		_, err := s.Workflows().CreateWorkflowRun(
			"wf-dup", 1, "---", "{}",
			[]NodeSeed{
				{Name: "duplicate", Type: "agent"},
				{Name: "duplicate", Type: "agent"},
			},
		)
		if err == nil {
			t.Fatal("expected UNIQUE constraint error for duplicate node name, got nil")
		}
	})
}

func TestMarkNodeCompleted(t *testing.T) {
	cases := []struct {
		name        string
		nodeName    string
		outputsJSON string
		completedAt string
		wantStatus  string
		wantOutputs string
	}{
		{
			name:        "happy path: status becomes completed",
			nodeName:    "node-a",
			outputsJSON: `{"result":"ok"}`,
			completedAt: "2025-01-01T00:00:00Z",
			wantStatus:  "completed",
			wantOutputs: `{"result":"ok"}`,
		},
		{
			name:        "empty outputs JSON is stored verbatim",
			nodeName:    "node-b",
			outputsJSON: "{}",
			completedAt: "2025-06-01T12:00:00Z",
			wantStatus:  "completed",
			wantOutputs: "{}",
		},
		{
			name:        "completed_at timestamp is stored",
			nodeName:    "node-c",
			outputsJSON: `{"x":1}`,
			completedAt: "2026-01-02T15:04:05Z",
			wantStatus:  "completed",
			wantOutputs: `{"x":1}`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			runID := seedWorkflowRun(t, s, tc.nodeName)

			if err := s.Workflows().MarkNodeCompleted(runID, tc.nodeName, tc.outputsJSON, tc.completedAt); err != nil {
				t.Fatalf("MarkNodeCompleted: %v", err)
			}

			// Verify the stored row.
			var status, outputs, completedAt string
			row := s.ReadDB.QueryRow(
				`SELECT status, COALESCE(outputs_json,''), COALESCE(completed_at,'')
				 FROM workflow_node_states WHERE run_id=? AND node_name=?`,
				runID, tc.nodeName,
			)
			if err := row.Scan(&status, &outputs, &completedAt); err != nil {
				t.Fatalf("reading node state: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status: want %q, got %q", tc.wantStatus, status)
			}
			if outputs != tc.wantOutputs {
				t.Errorf("outputs_json: want %q, got %q", tc.wantOutputs, outputs)
			}
			if completedAt != tc.completedAt {
				t.Errorf("completed_at: want %q, got %q", tc.completedAt, completedAt)
			}
		})
	}

	t.Run("no-op: non-existent node name does not error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "real-node")

		// MarkNodeCompleted wraps Exec — SQLite UPDATE with 0 rows affected is not an error.
		err := s.Workflows().MarkNodeCompleted(runID, "ghost-node", "{}", "2025-01-01T00:00:00Z")
		if err != nil {
			t.Fatalf("expected no error for missing node, got: %v", err)
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "some-node")
		s.WriteDB.Close()

		err := s.Workflows().MarkNodeCompleted(runID, "some-node", "{}", "2025-01-01T00:00:00Z")
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

func TestGetWorkflowNodeStates(t *testing.T) {
	t.Run("happy path: returns all nodes for run ordered by id", func(t *testing.T) {
		s := newTestStore(t)
		runID, err := s.Workflows().CreateWorkflowRun(
			"wf", 1, "---", "{}",
			[]NodeSeed{
				{Name: "step-1", Type: "agent"},
				{Name: "step-2", Type: "agent"},
				{Name: "step-3", Type: "agent"},
			},
		)
		if err != nil {
			t.Fatalf("CreateWorkflowRun: %v", err)
		}

		states, err := s.Workflows().GetWorkflowNodeStates(runID)
		if err != nil {
			t.Fatalf("GetWorkflowNodeStates: %v", err)
		}
		if len(states) != 3 {
			t.Fatalf("expected 3 states, got %d", len(states))
		}
		wantNames := []string{"step-1", "step-2", "step-3"}
		for i, name := range wantNames {
			if states[i].NodeName != name {
				t.Errorf("states[%d].NodeName: want %q, got %q", i, name, states[i].NodeName)
			}
			if states[i].RunID != runID {
				t.Errorf("states[%d].RunID: want %d, got %d", i, runID, states[i].RunID)
			}
			if states[i].Status != "pending" {
				t.Errorf("states[%d].Status: want %q, got %q", i, "pending", states[i].Status)
			}
		}
	})

	t.Run("empty result: unknown run_id returns empty slice", func(t *testing.T) {
		s := newTestStore(t)
		states, err := s.Workflows().GetWorkflowNodeStates(99999)
		if err != nil {
			t.Fatalf("expected no error for unknown run_id, got: %v", err)
		}
		if len(states) != 0 {
			t.Fatalf("expected empty slice, got %d rows", len(states))
		}
	})

	t.Run("run isolation: only returns nodes for the requested run", func(t *testing.T) {
		s := newTestStore(t)
		runA := seedWorkflowRun(t, s, "node-a")
		runB, err := s.Workflows().CreateWorkflowRun("wf", 1, "---", "{}", []NodeSeed{
			{Name: "node-b1", Type: "agent"},
			{Name: "node-b2", Type: "agent"},
		})
		if err != nil {
			t.Fatalf("CreateWorkflowRun B: %v", err)
		}

		statesA, err := s.Workflows().GetWorkflowNodeStates(runA)
		if err != nil {
			t.Fatalf("GetWorkflowNodeStates(runA): %v", err)
		}
		if len(statesA) != 1 || statesA[0].NodeName != "node-a" {
			t.Errorf("runA: want [node-a], got %v", statesA)
		}

		statesB, err := s.Workflows().GetWorkflowNodeStates(runB)
		if err != nil {
			t.Fatalf("GetWorkflowNodeStates(runB): %v", err)
		}
		if len(statesB) != 2 {
			t.Errorf("runB: want 2 nodes, got %d", len(statesB))
		}
	})

	t.Run("reflects updated status after MarkNodeCompleted", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "work-node")
		if err := s.Workflows().MarkNodeCompleted(runID, "work-node", `{"ok":true}`, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("MarkNodeCompleted: %v", err)
		}

		states, err := s.Workflows().GetWorkflowNodeStates(runID)
		if err != nil {
			t.Fatalf("GetWorkflowNodeStates: %v", err)
		}
		if len(states) != 1 {
			t.Fatalf("expected 1 state, got %d", len(states))
		}
		if states[0].Status != "completed" {
			t.Errorf("status: want %q, got %q", "completed", states[0].Status)
		}
		if !states[0].OutputsJSON.Valid || states[0].OutputsJSON.String != `{"ok":true}` {
			t.Errorf("outputs_json: want %q, got %v", `{"ok":true}`, states[0].OutputsJSON)
		}
	})

	t.Run("error path: closed read DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "node")
		s.ReadDB.Close()
		_, err := s.Workflows().GetWorkflowNodeStates(runID)
		if err == nil {
			t.Fatal("expected error from closed read DB, got nil")
		}
	})
}

func TestFindWaitingHumanNode(t *testing.T) {
	t.Run("no rows returns sql.ErrNoRows for run with no waiting_human node", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "step-1")
		_, err := s.Workflows().FindWaitingHumanNode(runID)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected sql.ErrNoRows, got %v", err)
		}
	})

	t.Run("no rows returns sql.ErrNoRows for nonexistent run_id", func(t *testing.T) {
		s := newTestStore(t)
		_, err := s.Workflows().FindWaitingHumanNode(99999)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected sql.ErrNoRows for unknown run_id, got %v", err)
		}
	})

	t.Run("happy path: returns node name when status is waiting_human", func(t *testing.T) {
		s := newTestStore(t)
		runID, err := s.Workflows().CreateWorkflowRun(
			"wf", 1, "---", "{}",
			[]NodeSeed{{Name: "human-review", Type: "human_review"}, {Name: "other", Type: "agent"}},
		)
		if err != nil {
			t.Fatalf("CreateWorkflowRun: %v", err)
		}
		if _, err := s.WriteDB.Exec(
			"UPDATE workflow_node_states SET status='waiting_human' WHERE run_id=? AND node_name=?",
			runID, "human-review",
		); err != nil {
			t.Fatalf("set waiting_human: %v", err)
		}

		got, err := s.Workflows().FindWaitingHumanNode(runID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "human-review" {
			t.Errorf("want %q, got %q", "human-review", got)
		}
	})

	t.Run("table-driven: non-waiting_human statuses return sql.ErrNoRows", func(t *testing.T) {
		statuses := []string{"pending", "running", "completed", "failed", "skipped", "rejected"}
		for _, status := range statuses {
			status := status
			t.Run("status="+status, func(t *testing.T) {
				s := newTestStore(t)
				runID := seedWorkflowRun(t, s, "node-a")
				if _, err := s.WriteDB.Exec(
					"UPDATE workflow_node_states SET status=? WHERE run_id=? AND node_name=?",
					status, runID, "node-a",
				); err != nil {
					t.Fatalf("set status %q: %v", status, err)
				}
				_, err := s.Workflows().FindWaitingHumanNode(runID)
				if !errors.Is(err, sql.ErrNoRows) {
					t.Errorf("status=%q: expected sql.ErrNoRows, got %v", status, err)
				}
			})
		}
	})

	t.Run("run isolation: waiting_human in run B does not affect run A", func(t *testing.T) {
		s := newTestStore(t)
		runA := seedWorkflowRun(t, s, "step")
		runB, err := s.Workflows().CreateWorkflowRun("wf", 1, "---", "{}", []NodeSeed{{Name: "step", Type: "agent"}})
		if err != nil {
			t.Fatalf("CreateWorkflowRun B: %v", err)
		}
		if _, err := s.WriteDB.Exec(
			"UPDATE workflow_node_states SET status='waiting_human' WHERE run_id=? AND node_name=?",
			runB, "step",
		); err != nil {
			t.Fatalf("set waiting_human in run B: %v", err)
		}

		// Run A should still return ErrNoRows.
		_, err = s.Workflows().FindWaitingHumanNode(runA)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("run A: expected sql.ErrNoRows, got %v", err)
		}
		// Run B should return the node.
		got, err := s.Workflows().FindWaitingHumanNode(runB)
		if err != nil {
			t.Fatalf("run B: unexpected error: %v", err)
		}
		if got != "step" {
			t.Errorf("run B: want %q, got %q", "step", got)
		}
	})

	t.Run("error path: closed read DB returns error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "node")
		s.ReadDB.Close()
		_, err := s.Workflows().FindWaitingHumanNode(runID)
		if err == nil {
			t.Fatal("expected error from closed read DB, got nil")
		}
	})
}

func TestMarkNodeRunning(t *testing.T) {
	cases := []struct {
		name      string
		nodeName  string
		startedAt string
	}{
		{
			name:      "happy path: status becomes running and attempt increments",
			nodeName:  "node-run-a",
			startedAt: "2025-01-01T00:00:00Z",
		},
		{
			name:      "different timestamp is stored",
			nodeName:  "node-run-b",
			startedAt: "2026-03-15T08:30:00Z",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			runID := seedWorkflowRun(t, s, tc.nodeName)

			if err := s.Workflows().MarkNodeRunning(runID, tc.nodeName, tc.startedAt); err != nil {
				t.Fatalf("MarkNodeRunning: %v", err)
			}

			var status, startedAt string
			var attempt int
			row := s.ReadDB.QueryRow(
				`SELECT status, COALESCE(started_at,''), attempt
				 FROM workflow_node_states WHERE run_id=? AND node_name=?`,
				runID, tc.nodeName,
			)
			if err := row.Scan(&status, &startedAt, &attempt); err != nil {
				t.Fatalf("reading node state: %v", err)
			}
			if status != "running" {
				t.Errorf("status: want %q, got %q", "running", status)
			}
			if startedAt != tc.startedAt {
				t.Errorf("started_at: want %q, got %q", tc.startedAt, startedAt)
			}
			if attempt != 1 {
				t.Errorf("attempt: want 1, got %d", attempt)
			}
		})
	}

	// A claim is taken only from pending: a second claim of a running node,
	// another driver's, is refused and leaves the attempt counted once.
	t.Run("a repeated claim is refused", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "repeat-node")

		if err := s.Workflows().MarkNodeRunning(runID, "repeat-node", "2025-01-01T00:00:00Z"); err != nil {
			t.Fatalf("first MarkNodeRunning: %v", err)
		}
		for i := 2; i <= 3; i++ {
			err := s.Workflows().MarkNodeRunning(runID, "repeat-node", "2025-01-01T00:00:00Z")
			if !errors.Is(err, ErrNodeNotPending) {
				t.Fatalf("MarkNodeRunning call %d on a running node: %v, want ErrNodeNotPending", i, err)
			}
		}

		var attempt int
		s.ReadDB.QueryRow(
			`SELECT attempt FROM workflow_node_states WHERE run_id=? AND node_name=?`,
			runID, "repeat-node",
		).Scan(&attempt)
		if attempt != 1 {
			t.Errorf("attempt after 3 calls: want 1, got %d", attempt)
		}
	})

	t.Run("a node that does not exist is not claimed", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "real-node")

		err := s.Workflows().MarkNodeRunning(runID, "ghost-node", "2025-01-01T00:00:00Z")
		if !errors.Is(err, ErrNodeNotPending) {
			t.Fatalf("MarkNodeRunning on a missing node: %v, want ErrNodeNotPending", err)
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "some-node")
		s.WriteDB.Close()

		err := s.Workflows().MarkNodeRunning(runID, "some-node", "2025-01-01T00:00:00Z")
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

// ReleaseNode returns a running node to pending with the attempt its claim
// counted taken back, and leaves a node that is not running as it is.
func TestReleaseNode(t *testing.T) {
	s := newTestStore(t)
	runID := seedWorkflowRun(t, s, "n")
	read := func() (status string, attempt int) {
		t.Helper()
		if err := s.ReadDB.QueryRow(`SELECT status, attempt FROM workflow_node_states WHERE run_id=? AND node_name='n'`, runID).Scan(&status, &attempt); err != nil {
			t.Fatal(err)
		}
		return status, attempt
	}
	if err := s.Workflows().MarkNodeRunning(runID, "n", "2025-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.Workflows().ReleaseNode(runID, "n"); err != nil {
		t.Fatal(err)
	}
	if status, attempt := read(); status != "pending" || attempt != 0 {
		t.Errorf("released node is %s at attempt %d, want pending at 0", status, attempt)
	}
	if err := s.Workflows().ReleaseNode(runID, "n"); err != nil {
		t.Fatal(err)
	}
	if status, attempt := read(); status != "pending" || attempt != 0 {
		t.Errorf("a pending node released again is %s at attempt %d, want it left pending at 0", status, attempt)
	}
}

func TestMarkRepairCompleted(t *testing.T) {
	cases := []struct {
		name          string
		outputsJSON   string
		acceptPassed  bool
		tokensIn      int64
		tokensOut     int64
		costUSDx10000 int64
		completedAt   string
		wantPassed    int // 0 or 1 as stored in DB
	}{
		{
			name:          "happy path: accept_passed=true stored as 1",
			outputsJSON:   `{"compile_ok":true}`,
			acceptPassed:  true,
			tokensIn:      100,
			tokensOut:     50,
			costUSDx10000: 123,
			completedAt:   "2025-06-01T10:00:00Z",
			wantPassed:    1,
		},
		{
			name:          "accept_passed=false stored as 0",
			outputsJSON:   `{"compile_ok":false}`,
			acceptPassed:  false,
			tokensIn:      200,
			tokensOut:     80,
			costUSDx10000: 456,
			completedAt:   "2025-06-02T12:00:00Z",
			wantPassed:    0,
		},
		{
			name:          "empty outputs JSON stored verbatim",
			outputsJSON:   "{}",
			acceptPassed:  true,
			tokensIn:      0,
			tokensOut:     0,
			costUSDx10000: 0,
			completedAt:   "2025-06-03T00:00:00Z",
			wantPassed:    1,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			_, repairID := seedRepairAttempt(t, s)

			if err := s.Workflows().MarkRepairCompleted(
				repairID, tc.outputsJSON, tc.acceptPassed,
				tc.tokensIn, tc.tokensOut, tc.costUSDx10000, tc.completedAt,
			); err != nil {
				t.Fatalf("MarkRepairCompleted: %v", err)
			}

			var gotOutputs, gotCompletedAt string
			var gotPassed, gotTokensIn, gotTokensOut, gotCost int64
			row := s.ReadDB.QueryRow(
				`SELECT COALESCE(repair_outputs_json,''), accept_passed,
				        tokens_in, tokens_out, cost_usd_x10000, COALESCE(completed_at,'')
				 FROM workflow_repairs WHERE id=?`,
				repairID,
			)
			if err := row.Scan(&gotOutputs, &gotPassed, &gotTokensIn, &gotTokensOut, &gotCost, &gotCompletedAt); err != nil {
				t.Fatalf("reading repair row: %v", err)
			}
			if gotOutputs != tc.outputsJSON {
				t.Errorf("repair_outputs_json: want %q, got %q", tc.outputsJSON, gotOutputs)
			}
			if int(gotPassed) != tc.wantPassed {
				t.Errorf("accept_passed: want %d, got %d", tc.wantPassed, gotPassed)
			}
			if gotTokensIn != tc.tokensIn {
				t.Errorf("tokens_in: want %d, got %d", tc.tokensIn, gotTokensIn)
			}
			if gotTokensOut != tc.tokensOut {
				t.Errorf("tokens_out: want %d, got %d", tc.tokensOut, gotTokensOut)
			}
			if gotCost != tc.costUSDx10000 {
				t.Errorf("cost_usd_x10000: want %d, got %d", tc.costUSDx10000, gotCost)
			}
			if gotCompletedAt != tc.completedAt {
				t.Errorf("completed_at: want %q, got %q", tc.completedAt, gotCompletedAt)
			}
		})
	}

	t.Run("no-op: non-existent repairID does not error", func(t *testing.T) {
		s := newTestStore(t)
		// UPDATE on a missing row is not an error in SQLite.
		err := s.Workflows().MarkRepairCompleted(99999, "{}", false, 0, 0, 0, "2025-01-01T00:00:00Z")
		if err != nil {
			t.Fatalf("expected no error for missing repairID, got: %v", err)
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		_, repairID := seedRepairAttempt(t, s)
		s.WriteDB.Close()

		err := s.Workflows().MarkRepairCompleted(repairID, "{}", true, 0, 0, 0, "2025-01-01T00:00:00Z")
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

func TestRecordRepairAttempt(t *testing.T) {
	t.Run("happy path: returns positive id and row is stored", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "fix-node")

		const wantModel, wantProvider, wantTrigger = "qwen3.5:4b", "openai", "backend_error"
		id, err := s.Workflows().RecordRepairAttempt(runID, "fix-node", 1, "predicate failed", "retry prompt", wantModel, wantProvider, wantTrigger)
		if err != nil {
			t.Fatalf("RecordRepairAttempt: %v", err)
		}
		if id <= 0 {
			t.Errorf("expected positive id, got %d", id)
		}

		var storedRunID int64
		var nodeName, failureReason, repairPrompt, model, provider, trigger string
		var attempt int
		row := s.ReadDB.QueryRow(
			`SELECT run_id, node_name, attempt, failure_reason, repair_prompt, model, provider, trigger
			 FROM workflow_repairs WHERE id=?`, id,
		)
		if err := row.Scan(&storedRunID, &nodeName, &attempt, &failureReason, &repairPrompt, &model, &provider, &trigger); err != nil {
			t.Fatalf("reading workflow_repairs: %v", err)
		}
		if model != wantModel || provider != wantProvider || trigger != wantTrigger {
			t.Errorf("ledger = %q/%q/%q, want %q/%q/%q", model, provider, trigger, wantModel, wantProvider, wantTrigger)
		}
		if storedRunID != runID {
			t.Errorf("run_id: want %d, got %d", runID, storedRunID)
		}
		if nodeName != "fix-node" {
			t.Errorf("node_name: want %q, got %q", "fix-node", nodeName)
		}
		if attempt != 1 {
			t.Errorf("attempt: want 1, got %d", attempt)
		}
		if failureReason != "predicate failed" {
			t.Errorf("failure_reason: want %q, got %q", "predicate failed", failureReason)
		}
		if repairPrompt != "retry prompt" {
			t.Errorf("repair_prompt: want %q, got %q", "retry prompt", repairPrompt)
		}
	})

	t.Run("table-driven: multiple attempts for same node get distinct ids", func(t *testing.T) {
		cases := []struct {
			attempt       int
			failureReason string
			repairPrompt  string
		}{
			{1, "first failure", "first repair"},
			{2, "second failure", "second repair"},
			{3, "third failure", "third repair"},
		}

		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "multi-node")
		seen := make(map[int64]bool)

		for _, tc := range cases {
			id, err := s.Workflows().RecordRepairAttempt(runID, "multi-node", tc.attempt, tc.failureReason, tc.repairPrompt, "", "", "accept")
			if err != nil {
				t.Fatalf("attempt %d: RecordRepairAttempt: %v", tc.attempt, err)
			}
			if id <= 0 {
				t.Errorf("attempt %d: expected positive id, got %d", tc.attempt, id)
			}
			if seen[id] {
				t.Errorf("attempt %d: duplicate id %d returned", tc.attempt, id)
			}
			seen[id] = true
		}

		var count int
		s.ReadDB.QueryRow("SELECT COUNT(*) FROM workflow_repairs WHERE run_id=?", runID).Scan(&count)
		if count != len(cases) {
			t.Errorf("expected %d repair rows, got %d", len(cases), count)
		}
	})

	t.Run("error path: invalid run_id (FK violation) returns error", func(t *testing.T) {
		s := newTestStore(t)
		_, err := s.Workflows().RecordRepairAttempt(999999, "ghost-node", 1, "reason", "prompt", "", "", "accept")
		if err == nil {
			t.Fatal("expected FK violation error for non-existent run_id, got nil")
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "closed-node")
		s.WriteDB.Close()

		_, err := s.Workflows().RecordRepairAttempt(runID, "closed-node", 1, "reason", "prompt", "", "", "accept")
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

func TestUpdateNodeMetrics(t *testing.T) {
	cases := []struct {
		name          string
		tokensIn      int64
		tokensOut     int64
		costUSDx10000 int64
		provider      string
		wantProvider  string
	}{
		{
			name:          "happy path: all fields stored",
			tokensIn:      100,
			tokensOut:     50,
			costUSDx10000: 250,
			provider:      "anthropic",
			wantProvider:  "anthropic",
		},
		{
			name:          "zero metrics stored",
			tokensIn:      0,
			tokensOut:     0,
			costUSDx10000: 0,
			provider:      "openai",
			wantProvider:  "openai",
		},
		{
			name:          "large metric values stored",
			tokensIn:      1000000,
			tokensOut:     500000,
			costUSDx10000: 99999,
			provider:      "gemini",
			wantProvider:  "gemini",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			runID := seedWorkflowRun(t, s, "metrics-node")

			if err := s.Workflows().UpdateNodeMetrics(runID, "metrics-node", tc.tokensIn, tc.tokensOut, tc.costUSDx10000, tc.provider, "", 0, 0); err != nil {
				t.Fatalf("UpdateNodeMetrics: %v", err)
			}

			var gotIn, gotOut, gotCost int64
			var gotProvider string
			row := s.ReadDB.QueryRow(
				`SELECT COALESCE(tokens_in,0), COALESCE(tokens_out,0),
				        COALESCE(cost_usd_x10000,0), COALESCE(provider,'')
				 FROM workflow_node_states WHERE run_id=? AND node_name=?`,
				runID, "metrics-node",
			)
			if err := row.Scan(&gotIn, &gotOut, &gotCost, &gotProvider); err != nil {
				t.Fatalf("reading node state: %v", err)
			}
			if gotIn != tc.tokensIn {
				t.Errorf("tokens_in: want %d, got %d", tc.tokensIn, gotIn)
			}
			if gotOut != tc.tokensOut {
				t.Errorf("tokens_out: want %d, got %d", tc.tokensOut, gotOut)
			}
			if gotCost != tc.costUSDx10000 {
				t.Errorf("cost_usd_x10000: want %d, got %d", tc.costUSDx10000, gotCost)
			}
			if gotProvider != tc.wantProvider {
				t.Errorf("provider: want %q, got %q", tc.wantProvider, gotProvider)
			}
		})
	}

	t.Run("accumulation: two calls add up tokens and cost", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "acc-node")

		if err := s.Workflows().UpdateNodeMetrics(runID, "acc-node", 100, 50, 300, "anthropic", "", 0, 0); err != nil {
			t.Fatalf("first UpdateNodeMetrics: %v", err)
		}
		if err := s.Workflows().UpdateNodeMetrics(runID, "acc-node", 200, 75, 400, "anthropic", "", 0, 0); err != nil {
			t.Fatalf("second UpdateNodeMetrics: %v", err)
		}

		var gotIn, gotOut, gotCost int64
		s.ReadDB.QueryRow(
			`SELECT COALESCE(tokens_in,0), COALESCE(tokens_out,0), COALESCE(cost_usd_x10000,0)
			 FROM workflow_node_states WHERE run_id=? AND node_name=?`,
			runID, "acc-node",
		).Scan(&gotIn, &gotOut, &gotCost)

		if gotIn != 300 {
			t.Errorf("tokens_in accumulated: want 300, got %d", gotIn)
		}
		if gotOut != 125 {
			t.Errorf("tokens_out accumulated: want 125, got %d", gotOut)
		}
		if gotCost != 700 {
			t.Errorf("cost_usd_x10000 accumulated: want 700, got %d", gotCost)
		}
	})

	t.Run("empty provider does not overwrite existing provider", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "prov-node")

		if err := s.Workflows().UpdateNodeMetrics(runID, "prov-node", 10, 5, 50, "anthropic", "", 0, 0); err != nil {
			t.Fatalf("first UpdateNodeMetrics: %v", err)
		}
		// Second call with empty provider — should leave existing provider intact.
		if err := s.Workflows().UpdateNodeMetrics(runID, "prov-node", 20, 10, 100, "", "", 0, 0); err != nil {
			t.Fatalf("second UpdateNodeMetrics: %v", err)
		}

		var gotProvider string
		s.ReadDB.QueryRow(
			`SELECT COALESCE(provider,'') FROM workflow_node_states WHERE run_id=? AND node_name=?`,
			runID, "prov-node",
		).Scan(&gotProvider)

		if gotProvider != "anthropic" {
			t.Errorf("provider: want %q after empty-provider update, got %q", "anthropic", gotProvider)
		}
	})

	t.Run("non-empty provider overwrites existing provider", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "prov-node2")

		if err := s.Workflows().UpdateNodeMetrics(runID, "prov-node2", 10, 5, 50, "anthropic", "", 0, 0); err != nil {
			t.Fatalf("first UpdateNodeMetrics: %v", err)
		}
		if err := s.Workflows().UpdateNodeMetrics(runID, "prov-node2", 20, 10, 100, "openai", "", 0, 0); err != nil {
			t.Fatalf("second UpdateNodeMetrics: %v", err)
		}

		var gotProvider string
		s.ReadDB.QueryRow(
			`SELECT COALESCE(provider,'') FROM workflow_node_states WHERE run_id=? AND node_name=?`,
			runID, "prov-node2",
		).Scan(&gotProvider)

		if gotProvider != "openai" {
			t.Errorf("provider: want %q after override, got %q", "openai", gotProvider)
		}
	})

	t.Run("base_url: a set value is stored, an empty one leaves it", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "url-node")
		const first, second = "http://127.0.0.1:11434/v1", "http://127.0.0.1:1234/v1"
		steps := []struct{ set, want string }{{first, first}, {"", first}, {second, second}}
		for i, st := range steps {
			if err := s.Workflows().UpdateNodeMetrics(runID, "url-node", 1, 1, 0, "openai", st.set, 0, 0); err != nil {
				t.Fatalf("step %d: UpdateNodeMetrics: %v", i, err)
			}
			var got string
			if err := s.ReadDB.QueryRow(
				`SELECT COALESCE(base_url,'') FROM workflow_node_states WHERE run_id=? AND node_name=?`,
				runID, "url-node",
			).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != st.want {
				t.Errorf("step %d (set %q): base_url = %q, want %q", i, st.set, got, st.want)
			}
		}
	})

	t.Run("call counts: metered and unmetered calls add up apart", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "calls-node")
		steps := []struct{ metered, unmetered int64 }{{1, 0}, {0, 4}, {2, 3}}
		var wantM, wantU int64
		for i, st := range steps {
			if err := s.Workflows().UpdateNodeMetrics(runID, "calls-node", 1, 1, 0, "openai", "", st.metered, st.unmetered); err != nil {
				t.Fatalf("step %d: UpdateNodeMetrics: %v", i, err)
			}
			wantM += st.metered
			wantU += st.unmetered
		}
		var gotM, gotU int64
		if err := s.ReadDB.QueryRow(
			`SELECT metered_calls, unmetered_calls FROM workflow_node_states WHERE run_id=? AND node_name=?`,
			runID, "calls-node",
		).Scan(&gotM, &gotU); err != nil {
			t.Fatal(err)
		}
		if gotM != wantM || gotU != wantU {
			t.Errorf("metered/unmetered calls = %d/%d, want %d/%d", gotM, gotU, wantM, wantU)
		}
	})

	t.Run("no-op: non-existent node name does not error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "real-node")

		err := s.Workflows().UpdateNodeMetrics(runID, "ghost-node", 10, 5, 50, "anthropic", "", 0, 0)
		if err != nil {
			t.Fatalf("expected no error for missing node, got: %v", err)
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "err-node")
		s.WriteDB.Close()

		err := s.Workflows().UpdateNodeMetrics(runID, "err-node", 10, 5, 50, "anthropic", "", 0, 0)
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

func TestGetWorkflowRun(t *testing.T) {
	t.Run("happy path: all fields round-trip", func(t *testing.T) {
		s := newTestStore(t)
		runID, err := s.Workflows().CreateWorkflowRun(
			"my-workflow", 3, "def: yaml", `{"key":"val"}`,
			[]NodeSeed{{Name: "step-1", Type: "agent"}},
		)
		if err != nil {
			t.Fatalf("CreateWorkflowRun: %v", err)
		}

		got, err := s.Workflows().GetWorkflowRun(runID)
		if err != nil {
			t.Fatalf("GetWorkflowRun: %v", err)
		}
		if got.ID != runID {
			t.Errorf("ID: want %d, got %d", runID, got.ID)
		}
		if got.Name != "my-workflow" {
			t.Errorf("Name: want %q, got %q", "my-workflow", got.Name)
		}
		if got.Version != 3 {
			t.Errorf("Version: want 3, got %d", got.Version)
		}
		if got.DefinitionYAML != "def: yaml" {
			t.Errorf("DefinitionYAML: want %q, got %q", "def: yaml", got.DefinitionYAML)
		}
		if got.InputsJSON != `{"key":"val"}` {
			t.Errorf("InputsJSON: want %q, got %q", `{"key":"val"}`, got.InputsJSON)
		}
		if got.Status != "running" {
			t.Errorf("Status: want %q, got %q", "running", got.Status)
		}
	})

	t.Run("table-driven: multiple runs are independent", func(t *testing.T) {
		cases := []struct {
			wfName  string
			version int
		}{
			{"workflow-alpha", 1},
			{"workflow-beta", 2},
			{"workflow-gamma", 7},
		}

		s := newTestStore(t)
		for _, tc := range cases {
			tc := tc
			t.Run(tc.wfName, func(t *testing.T) {
				runID, err := s.Workflows().CreateWorkflowRun(
					tc.wfName, tc.version, "---", "{}",
					[]NodeSeed{{Name: "node", Type: "agent"}},
				)
				if err != nil {
					t.Fatalf("CreateWorkflowRun: %v", err)
				}
				got, err := s.Workflows().GetWorkflowRun(runID)
				if err != nil {
					t.Fatalf("GetWorkflowRun: %v", err)
				}
				if got.Name != tc.wfName {
					t.Errorf("Name: want %q, got %q", tc.wfName, got.Name)
				}
				if got.Version != tc.version {
					t.Errorf("Version: want %d, got %d", tc.version, got.Version)
				}
			})
		}
	})

	t.Run("error path: nonexistent run id returns sql.ErrNoRows", func(t *testing.T) {
		s := newTestStore(t)
		_, err := s.Workflows().GetWorkflowRun(99999)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected sql.ErrNoRows, got %v", err)
		}
	})

	t.Run("error path: closed read DB returns error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "some-node")
		s.ReadDB.Close()
		_, err := s.Workflows().GetWorkflowRun(runID)
		if err == nil {
			t.Fatal("expected error from closed read DB, got nil")
		}
	})
}

func TestMarkRunRunning(t *testing.T) {
	t.Run("happy path: sets status=running on an existing run", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "node-a")

		if err := s.Workflows().MarkRunRunning(runID); err != nil {
			t.Fatalf("MarkRunRunning: %v", err)
		}

		var status string
		row := s.ReadDB.QueryRow("SELECT status FROM workflow_runs WHERE id=?", runID)
		if err := row.Scan(&status); err != nil {
			t.Fatalf("reading workflow_runs: %v", err)
		}
		if status != "running" {
			t.Errorf("status: want %q, got %q", "running", status)
		}
	})

	t.Run("idempotent: second call leaves status=running", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "node-b")

		if err := s.Workflows().MarkRunRunning(runID); err != nil {
			t.Fatalf("first MarkRunRunning: %v", err)
		}
		if err := s.Workflows().MarkRunRunning(runID); err != nil {
			t.Fatalf("second MarkRunRunning: %v", err)
		}

		var status string
		s.ReadDB.QueryRow("SELECT status FROM workflow_runs WHERE id=?", runID).Scan(&status)
		if status != "running" {
			t.Errorf("status after second call: want %q, got %q", "running", status)
		}
	})

	t.Run("nonexistent run id: no error (UPDATE 0 rows is not a DB error)", func(t *testing.T) {
		s := newTestStore(t)
		err := s.Workflows().MarkRunRunning(9999999)
		if err != nil {
			t.Errorf("expected nil for nonexistent run, got: %v", err)
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "node-c")
		s.WriteDB.Close()

		err := s.Workflows().MarkRunRunning(runID)
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

func TestMarkRunCompleted(t *testing.T) {
	t.Run("happy path: sets status=completed and completed_at", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "node-a")

		completedAt := "2026-01-01T00:00:00Z"
		if err := s.Workflows().MarkRunCompleted(runID, completedAt); err != nil {
			t.Fatalf("MarkRunCompleted: %v", err)
		}

		var status, gotAt string
		row := s.ReadDB.QueryRow(
			"SELECT status, COALESCE(completed_at,'') FROM workflow_runs WHERE id=?", runID,
		)
		if err := row.Scan(&status, &gotAt); err != nil {
			t.Fatalf("reading workflow_runs: %v", err)
		}
		if status != "completed" {
			t.Errorf("status: want %q, got %q", "completed", status)
		}
		if gotAt != completedAt {
			t.Errorf("completed_at: want %q, got %q", completedAt, gotAt)
		}
	})

	t.Run("idempotent: second call overwrites completed_at", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "node-a")

		if err := s.Workflows().MarkRunCompleted(runID, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("first MarkRunCompleted: %v", err)
		}
		secondAt := "2026-06-01T12:00:00Z"
		if err := s.Workflows().MarkRunCompleted(runID, secondAt); err != nil {
			t.Fatalf("second MarkRunCompleted: %v", err)
		}

		var gotAt string
		s.ReadDB.QueryRow("SELECT COALESCE(completed_at,'') FROM workflow_runs WHERE id=?", runID).Scan(&gotAt)
		if gotAt != secondAt {
			t.Errorf("completed_at after second call: want %q, got %q", secondAt, gotAt)
		}
	})

	t.Run("nonexistent run id: no error (UPDATE 0 rows is not a DB error)", func(t *testing.T) {
		s := newTestStore(t)
		err := s.Workflows().MarkRunCompleted(9999999, "2026-01-01T00:00:00Z")
		if err != nil {
			t.Errorf("expected nil for nonexistent run, got: %v", err)
		}
	})

	t.Run("table-driven: various timestamps round-trip", func(t *testing.T) {
		cases := []struct {
			completedAt string
		}{
			{"2025-12-31T23:59:59Z"},
			{"2026-01-01T00:00:00Z"},
			{"2026-06-15T08:30:00Z"},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.completedAt, func(t *testing.T) {
				s := newTestStore(t)
				runID := seedWorkflowRun(t, s, "node-a")
				if err := s.Workflows().MarkRunCompleted(runID, tc.completedAt); err != nil {
					t.Fatalf("MarkRunCompleted(%q): %v", tc.completedAt, err)
				}
				var gotAt string
				s.ReadDB.QueryRow("SELECT COALESCE(completed_at,'') FROM workflow_runs WHERE id=?", runID).Scan(&gotAt)
				if gotAt != tc.completedAt {
					t.Errorf("completed_at: want %q, got %q", tc.completedAt, gotAt)
				}
			})
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "node-a")
		s.WriteDB.Close()
		err := s.Workflows().MarkRunCompleted(runID, "2026-01-01T00:00:00Z")
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

func TestListWorkflowRuns(t *testing.T) {
	t.Run("empty: no runs returns nil slice", func(t *testing.T) {
		s := newTestStore(t)
		runs, err := s.Workflows().ListWorkflowRuns(10)
		if err != nil {
			t.Fatalf("ListWorkflowRuns: %v", err)
		}
		if len(runs) != 0 {
			t.Fatalf("expected empty result, got %d rows", len(runs))
		}
	})

	t.Run("happy path: returns runs in descending id order", func(t *testing.T) {
		s := newTestStore(t)
		idA := seedWorkflowRun(t, s, "node-a")
		idB := seedWorkflowRun(t, s, "node-b")
		idC := seedWorkflowRun(t, s, "node-c")

		runs, err := s.Workflows().ListWorkflowRuns(10)
		if err != nil {
			t.Fatalf("ListWorkflowRuns: %v", err)
		}
		if len(runs) != 3 {
			t.Fatalf("expected 3 runs, got %d", len(runs))
		}
		// id DESC order: C, B, A
		wantIDs := []int64{idC, idB, idA}
		for i, wantID := range wantIDs {
			if runs[i].ID != wantID {
				t.Errorf("runs[%d].ID: want %d, got %d", i, wantID, runs[i].ID)
			}
		}
	})

	t.Run("table-driven: limit is respected", func(t *testing.T) {
		cases := []struct {
			limit   int
			wantLen int
		}{
			{0, 0},
			{1, 1},
			{2, 2},
			{5, 3}, // only 3 exist
		}

		s := newTestStore(t)
		seedWorkflowRun(t, s, "node-a")
		seedWorkflowRun(t, s, "node-b")
		seedWorkflowRun(t, s, "node-c")

		for _, tc := range cases {
			tc := tc
			t.Run("", func(t *testing.T) {
				runs, err := s.Workflows().ListWorkflowRuns(tc.limit)
				if err != nil {
					t.Fatalf("ListWorkflowRuns(limit=%d): %v", tc.limit, err)
				}
				if len(runs) != tc.wantLen {
					t.Errorf("limit=%d: want %d rows, got %d", tc.limit, tc.wantLen, len(runs))
				}
			})
		}
	})

	t.Run("fields: id, name, version, status are populated", func(t *testing.T) {
		s := newTestStore(t)
		runID, err := s.Workflows().CreateWorkflowRun(
			"my-workflow", 7, "def: yaml", `{"key":"val"}`,
			[]NodeSeed{{Name: "step-1", Type: "agent"}},
		)
		if err != nil {
			t.Fatalf("CreateWorkflowRun: %v", err)
		}

		runs, err := s.Workflows().ListWorkflowRuns(10)
		if err != nil {
			t.Fatalf("ListWorkflowRuns: %v", err)
		}
		if len(runs) != 1 {
			t.Fatalf("expected 1 run, got %d", len(runs))
		}
		r := runs[0]
		if r.ID != runID {
			t.Errorf("ID: want %d, got %d", runID, r.ID)
		}
		if r.Name != "my-workflow" {
			t.Errorf("Name: want %q, got %q", "my-workflow", r.Name)
		}
		if r.Version != 7 {
			t.Errorf("Version: want 7, got %d", r.Version)
		}
		if r.Status != "running" {
			t.Errorf("Status: want %q, got %q", "running", r.Status)
		}
	})

	t.Run("error path: closed read DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		seedWorkflowRun(t, s, "node")
		s.ReadDB.Close()
		_, err := s.Workflows().ListWorkflowRuns(10)
		if err == nil {
			t.Fatal("expected error from closed read DB, got nil")
		}
	})
}

func TestMarkNodeFailed(t *testing.T) {
	cases := []struct {
		name        string
		nodeName    string
		errMsg      string
		completedAt string
		retryable   bool
		wantStatus  string
		wantNullAt  bool // true when completed_at should be NULL (retryable path)
	}{
		{
			name:        "non-retryable: status becomes failed and completed_at is set",
			nodeName:    "fail-node-a",
			errMsg:      "something went wrong",
			completedAt: "2025-01-01T00:00:00Z",
			retryable:   false,
			wantStatus:  "failed",
			wantNullAt:  false,
		},
		{
			name:        "retryable: status becomes pending and completed_at is NULL",
			nodeName:    "fail-node-b",
			errMsg:      "transient error",
			completedAt: "2025-06-01T12:00:00Z",
			retryable:   true,
			wantStatus:  "pending",
			wantNullAt:  true,
		},
		{
			name:        "non-retryable: empty error message stored verbatim",
			nodeName:    "fail-node-c",
			errMsg:      "",
			completedAt: "2026-01-02T08:00:00Z",
			retryable:   false,
			wantStatus:  "failed",
			wantNullAt:  false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			runID := seedWorkflowRun(t, s, tc.nodeName)

			if err := s.Workflows().MarkNodeFailed(runID, tc.nodeName, tc.errMsg, tc.completedAt, tc.retryable); err != nil {
				t.Fatalf("MarkNodeFailed: %v", err)
			}

			var status, gotErr string
			var completedAtValid bool
			row := s.ReadDB.QueryRow(
				`SELECT status, COALESCE(error,''), completed_at IS NOT NULL
				 FROM workflow_node_states WHERE run_id=? AND node_name=?`,
				runID, tc.nodeName,
			)
			if err := row.Scan(&status, &gotErr, &completedAtValid); err != nil {
				t.Fatalf("reading node state: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status: want %q, got %q", tc.wantStatus, status)
			}
			if gotErr != tc.errMsg {
				t.Errorf("error: want %q, got %q", tc.errMsg, gotErr)
			}
			if tc.wantNullAt && completedAtValid {
				t.Errorf("completed_at: expected NULL for retryable path, got non-NULL")
			}
			if !tc.wantNullAt && !completedAtValid {
				t.Errorf("completed_at: expected non-NULL for terminal failure, got NULL")
			}
		})
	}

	t.Run("no-op: non-existent node name does not error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "real-node")

		// SQLite UPDATE with 0 rows affected is not an error.
		err := s.Workflows().MarkNodeFailed(runID, "ghost-node", "err", "2025-01-01T00:00:00Z", false)
		if err != nil {
			t.Fatalf("expected no error for missing node, got: %v", err)
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "some-node")
		s.WriteDB.Close()

		err := s.Workflows().MarkNodeFailed(runID, "some-node", "err", "2025-01-01T00:00:00Z", false)
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

func TestNullableTime(t *testing.T) {
	cases := []struct {
		name      string
		t         string
		retryable bool
		wantValid bool
		wantStr   string
	}{
		{
			name:      "not retryable: returns valid NullString with the timestamp",
			t:         "2025-01-01T00:00:00Z",
			retryable: false,
			wantValid: true,
			wantStr:   "2025-01-01T00:00:00Z",
		},
		{
			name:      "retryable: returns null NullString (Valid=false, String empty)",
			t:         "2025-01-01T00:00:00Z",
			retryable: true,
			wantValid: false,
			wantStr:   "",
		},
		{
			name:      "not retryable: empty timestamp string stored as-is",
			t:         "",
			retryable: false,
			wantValid: true,
			wantStr:   "",
		},
		{
			name:      "retryable with empty timestamp still returns null NullString",
			t:         "",
			retryable: true,
			wantValid: false,
			wantStr:   "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := nullableTime(tc.t, tc.retryable)
			if got.Valid != tc.wantValid {
				t.Errorf("Valid: want %v, got %v", tc.wantValid, got.Valid)
			}
			if got.String != tc.wantStr {
				t.Errorf("String: want %q, got %q", tc.wantStr, got.String)
			}
		})
	}
}

func TestMarkNodeSkipped(t *testing.T) {
	cases := []struct {
		name        string
		nodeName    string
		completedAt string
	}{
		{
			name:        "happy path: status becomes skipped",
			nodeName:    "skip-node-a",
			completedAt: "2025-01-01T00:00:00Z",
		},
		{
			name:        "different timestamp is stored",
			nodeName:    "skip-node-b",
			completedAt: "2026-06-15T08:30:00Z",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			runID := seedWorkflowRun(t, s, tc.nodeName)

			if err := s.Workflows().MarkNodeSkipped(runID, tc.nodeName, tc.completedAt); err != nil {
				t.Fatalf("MarkNodeSkipped: %v", err)
			}

			var status, completedAt string
			row := s.ReadDB.QueryRow(
				`SELECT status, COALESCE(completed_at,'')
				 FROM workflow_node_states WHERE run_id=? AND node_name=?`,
				runID, tc.nodeName,
			)
			if err := row.Scan(&status, &completedAt); err != nil {
				t.Fatalf("reading node state: %v", err)
			}
			if status != "skipped" {
				t.Errorf("status: want %q, got %q", "skipped", status)
			}
			if completedAt != tc.completedAt {
				t.Errorf("completed_at: want %q, got %q", tc.completedAt, completedAt)
			}
		})
	}

	t.Run("no-op: non-existent node name does not error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "real-node")

		// SQLite UPDATE with 0 rows affected is not an error.
		err := s.Workflows().MarkNodeSkipped(runID, "ghost-node", "2025-01-01T00:00:00Z")
		if err != nil {
			t.Fatalf("expected no error for missing node, got: %v", err)
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "some-node")
		s.WriteDB.Close()

		err := s.Workflows().MarkNodeSkipped(runID, "some-node", "2025-01-01T00:00:00Z")
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

func TestMarkNodeRejected(t *testing.T) {
	cases := []struct {
		name        string
		nodeName    string
		rationale   string
		completedAt string
		wantStatus  string
	}{
		{
			name:        "happy path: status becomes rejected",
			nodeName:    "node-a",
			rationale:   "accept predicate failed",
			completedAt: "2025-01-01T00:00:00Z",
			wantStatus:  "rejected",
		},
		{
			name:        "empty rationale is stored verbatim",
			nodeName:    "node-b",
			rationale:   "",
			completedAt: "2025-06-01T12:00:00Z",
			wantStatus:  "rejected",
		},
		{
			name:        "rationale and completed_at are persisted",
			nodeName:    "node-c",
			rationale:   "repair budget exhausted",
			completedAt: "2026-01-02T15:04:05Z",
			wantStatus:  "rejected",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			runID := seedWorkflowRun(t, s, tc.nodeName)

			if err := s.Workflows().MarkNodeRejected(runID, tc.nodeName, tc.rationale, tc.completedAt); err != nil {
				t.Fatalf("MarkNodeRejected: %v", err)
			}

			var status, rationale, completedAt string
			row := s.ReadDB.QueryRow(
				`SELECT status, COALESCE(rationale,''), COALESCE(completed_at,'')
				 FROM workflow_node_states WHERE run_id=? AND node_name=?`,
				runID, tc.nodeName,
			)
			if err := row.Scan(&status, &rationale, &completedAt); err != nil {
				t.Fatalf("reading node state: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status: want %q, got %q", tc.wantStatus, status)
			}
			if rationale != tc.rationale {
				t.Errorf("rationale: want %q, got %q", tc.rationale, rationale)
			}
			if completedAt != tc.completedAt {
				t.Errorf("completed_at: want %q, got %q", tc.completedAt, completedAt)
			}
		})
	}

	t.Run("no-op: non-existent node name does not error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "real-node")

		// SQLite UPDATE with 0 rows affected is not an error.
		err := s.Workflows().MarkNodeRejected(runID, "ghost-node", "reason", "2025-01-01T00:00:00Z")
		if err != nil {
			t.Fatalf("expected no error for missing node, got: %v", err)
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "some-node")
		s.WriteDB.Close()

		err := s.Workflows().MarkNodeRejected(runID, "some-node", "reason", "2025-01-01T00:00:00Z")
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

func TestRecordDecision(t *testing.T) {
	cases := []struct {
		name           string
		nodeName       string
		condition      string
		evaluatedValue string
		branchTaken    string
		evalErr        string
		wantBTNull     bool // true when branch_taken should be stored as NULL
		wantEENull     bool // true when eval_error should be stored as NULL
	}{
		{
			name:           "happy path: branch_taken non-empty, evalErr empty",
			nodeName:       "decide-node",
			condition:      "outputs.ok == true",
			evaluatedValue: "true",
			branchTaken:    "yes-branch",
			evalErr:        "",
			wantBTNull:     false,
			wantEENull:     true,
		},
		{
			name:           "empty branchTaken stored as NULL",
			nodeName:       "decide-node",
			condition:      "outputs.count > 0",
			evaluatedValue: "false",
			branchTaken:    "",
			evalErr:        "",
			wantBTNull:     true,
			wantEENull:     true,
		},
		{
			name:           "non-empty evalErr stored; branchTaken empty stays NULL",
			nodeName:       "decide-node",
			condition:      "outputs.x == 1",
			evaluatedValue: "",
			branchTaken:    "",
			evalErr:        "parse error: unexpected token",
			wantBTNull:     true,
			wantEENull:     false,
		},
		{
			name:           "both branchTaken and evalErr non-empty",
			nodeName:       "decide-node",
			condition:      "len(outputs.items) >= 3",
			evaluatedValue: "4",
			branchTaken:    "loop",
			evalErr:        "partial eval",
			wantBTNull:     false,
			wantEENull:     false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			runID := seedWorkflowRun(t, s, tc.nodeName)

			if err := s.Workflows().RecordDecision(runID, tc.nodeName, tc.condition, tc.evaluatedValue, tc.branchTaken, tc.evalErr); err != nil {
				t.Fatalf("RecordDecision: %v", err)
			}

			var storedRunID int64
			var nodeName, condition, evaluatedValue string
			var btValid, eeValid bool
			var bt, ee string
			row := s.ReadDB.QueryRow(
				`SELECT run_id, node_name, condition, evaluated_value,
				        branch_taken IS NOT NULL, COALESCE(branch_taken,''),
				        eval_error IS NOT NULL,   COALESCE(eval_error,'')
				 FROM workflow_decisions WHERE run_id=? ORDER BY id DESC LIMIT 1`,
				runID,
			)
			if err := row.Scan(&storedRunID, &nodeName, &condition, &evaluatedValue, &btValid, &bt, &eeValid, &ee); err != nil {
				t.Fatalf("reading workflow_decisions: %v", err)
			}
			if storedRunID != runID {
				t.Errorf("run_id: want %d, got %d", runID, storedRunID)
			}
			if nodeName != tc.nodeName {
				t.Errorf("node_name: want %q, got %q", tc.nodeName, nodeName)
			}
			if condition != tc.condition {
				t.Errorf("condition: want %q, got %q", tc.condition, condition)
			}
			if evaluatedValue != tc.evaluatedValue {
				t.Errorf("evaluated_value: want %q, got %q", tc.evaluatedValue, evaluatedValue)
			}
			if tc.wantBTNull && btValid {
				t.Errorf("branch_taken: expected NULL, got %q", bt)
			}
			if !tc.wantBTNull && !btValid {
				t.Errorf("branch_taken: expected %q, got NULL", tc.branchTaken)
			}
			if !tc.wantBTNull && bt != tc.branchTaken {
				t.Errorf("branch_taken: want %q, got %q", tc.branchTaken, bt)
			}
			if tc.wantEENull && eeValid {
				t.Errorf("eval_error: expected NULL, got %q", ee)
			}
			if !tc.wantEENull && !eeValid {
				t.Errorf("eval_error: expected %q, got NULL", tc.evalErr)
			}
			if !tc.wantEENull && ee != tc.evalErr {
				t.Errorf("eval_error: want %q, got %q", tc.evalErr, ee)
			}
		})
	}

	t.Run("multiple decisions for same run accumulate as separate rows", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "multi-decide")

		for i := 0; i < 3; i++ {
			if err := s.Workflows().RecordDecision(runID, "multi-decide", "cond", "val", "branch", ""); err != nil {
				t.Fatalf("RecordDecision %d: %v", i, err)
			}
		}

		var count int
		s.ReadDB.QueryRow("SELECT COUNT(*) FROM workflow_decisions WHERE run_id=?", runID).Scan(&count)
		if count != 3 {
			t.Errorf("expected 3 decision rows, got %d", count)
		}
	})

	t.Run("run isolation: decisions from different runs don't bleed", func(t *testing.T) {
		s := newTestStore(t)
		runA := seedWorkflowRun(t, s, "node-a")
		runB := seedWorkflowRun(t, s, "node-b")

		if err := s.Workflows().RecordDecision(runA, "node-a", "cond-a", "true", "a-branch", ""); err != nil {
			t.Fatalf("RecordDecision(runA): %v", err)
		}
		if err := s.Workflows().RecordDecision(runB, "node-b", "cond-b", "false", "", "some err"); err != nil {
			t.Fatalf("RecordDecision(runB): %v", err)
		}

		var countA, countB int
		s.ReadDB.QueryRow("SELECT COUNT(*) FROM workflow_decisions WHERE run_id=?", runA).Scan(&countA)
		s.ReadDB.QueryRow("SELECT COUNT(*) FROM workflow_decisions WHERE run_id=?", runB).Scan(&countB)
		if countA != 1 {
			t.Errorf("runA: expected 1 decision row, got %d", countA)
		}
		if countB != 1 {
			t.Errorf("runB: expected 1 decision row, got %d", countB)
		}
	})

	t.Run("error path: invalid run_id (FK violation) returns error", func(t *testing.T) {
		s := newTestStore(t)
		err := s.Workflows().RecordDecision(999999, "ghost-node", "cond", "val", "branch", "")
		if err == nil {
			t.Fatal("expected FK violation error for non-existent run_id, got nil")
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "closed-node")
		s.WriteDB.Close()

		err := s.Workflows().RecordDecision(runID, "closed-node", "cond", "val", "branch", "")
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}

func TestUpdateNodeRationale(t *testing.T) {
	cases := []struct {
		name      string
		nodeName  string
		rationale string
	}{
		{
			name:      "happy path: rationale stored on existing node",
			nodeName:  "node-a",
			rationale: "agent finished successfully",
		},
		{
			name:      "empty rationale stored verbatim",
			nodeName:  "node-b",
			rationale: "",
		},
		{
			name:      "long rationale stored verbatim",
			nodeName:  "node-c",
			rationale: "line1\nline2\nline3",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			runID := seedWorkflowRun(t, s, tc.nodeName)

			if err := s.Workflows().UpdateNodeRationale(runID, tc.nodeName, tc.rationale); err != nil {
				t.Fatalf("UpdateNodeRationale: %v", err)
			}

			var got sql.NullString
			row := s.ReadDB.QueryRow(
				`SELECT rationale FROM workflow_node_states WHERE run_id=? AND node_name=?`,
				runID, tc.nodeName,
			)
			if err := row.Scan(&got); err != nil {
				t.Fatalf("reading rationale: %v", err)
			}
			want := tc.rationale
			gotStr := got.String
			if !got.Valid {
				gotStr = ""
			}
			if gotStr != want {
				t.Errorf("rationale: want %q, got %q", want, gotStr)
			}
		})
	}

	t.Run("no-op: non-existent node does not error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "real-node")

		// UPDATE with 0 rows affected is not an error in SQLite.
		err := s.Workflows().UpdateNodeRationale(runID, "ghost-node", "some text")
		if err != nil {
			t.Fatalf("expected no error for missing node, got: %v", err)
		}
	})

	t.Run("error path: closed write DB returns wrapped error", func(t *testing.T) {
		s := newTestStore(t)
		runID := seedWorkflowRun(t, s, "some-node")
		s.WriteDB.Close()

		err := s.Workflows().UpdateNodeRationale(runID, "some-node", "text")
		if err == nil {
			t.Fatal("expected error from closed write DB, got nil")
		}
	})
}
