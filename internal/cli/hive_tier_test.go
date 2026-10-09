package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// tierHive initialises hive p in a fresh database with chb, under the models
// config at modelsPath and the extra environment, and adds a wave of five
// unknowns, which raises the shaking signal for a stronger tier. It returns
// the directory and the database path.
func tierHive(t *testing.T, modelsPath string, env []string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hive.db")
	if out, err := runChb(t, dir, modelsPath, env, "--db", dbPath, "hive", "init", "--project", "p"); err != nil {
		t.Fatalf("hive init: %v\n%s", err, out)
	}
	s, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 5; i++ {
		if _, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, mss_label, finding) VALUES (1,'a',1,?,'unknown','no answer')`, i); err != nil {
			t.Fatal(err)
		}
	}
	return dir, dbPath
}

// tierState reads hive p's model_tier and its latest hive_tier_log row.
func tierState(t *testing.T, dbPath string) (tier, outcome, reason string) {
	t.Helper()
	s, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ReadDB.QueryRow(`SELECT model_tier FROM hive_state WHERE project='p'`).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT outcome, reason FROM hive_tier_log WHERE project='p' ORDER BY id DESC LIMIT 1`).Scan(&outcome, &reason); err != nil {
		t.Fatal(err)
	}
	return tier, outcome, reason
}

// agent-run --budget-mode reaches the hive's scan, which runs as a command
// node: under cheap the tier stays at the start rung, the highest cheap
// allows; with no mode it rises one rung.
func TestHiveTier_AgentRunBudgetModeBoundsTheScan(t *testing.T) {
	ladder := models.Load().HiveLadder("")
	start := (len(ladder) - 1) / 2
	if start+1 >= len(ladder) {
		t.Fatalf("setup: ladder %v has no rung above its start", ladder)
	}
	for _, tc := range []struct{ mode, want, outcome string }{
		{"", ladder[start+1], "rise"},
		{"cheap", ladder[start], "hold"},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			modelsPath := filepath.Join(t.TempDir(), "absent.yaml")
			env := []string{"ANTHROPIC_API_KEY=unused: the workflow calls no model"}
			dir, dbPath := tierHive(t, modelsPath, env)
			wf := filepath.Join(dir, "scan.yaml")
			src := "name: scan\nversion: 1\nnodes:\n  scan:\n    type: command\n    argv: [chb, hive, next, --project, \"{project}\", --apply]\n    outputs_from: stdout_json\n    outputs: [phase]\nedges: []\n"
			if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"--db", dbPath, "agent-run", wf, "--inputs", `{"project":"p"}`}
			if tc.mode != "" {
				args = append(args, "--budget-mode", tc.mode)
			}
			if out, err := runChb(t, dir, modelsPath, env, args...); err != nil {
				t.Fatalf("agent-run: %v\n%s", err, out)
			}
			tier, outcome, reason := tierState(t, dbPath)
			if tier != tc.want || outcome != tc.outcome {
				t.Fatalf("tier %s, outcome %s (%s), want %s (%s)", tier, outcome, reason, tc.want, tc.outcome)
			}
			if tc.mode != "" && !strings.Contains(reason, "budget mode "+tc.mode) {
				t.Errorf("reason %q does not name budget mode %s", reason, tc.mode)
			}
		})
	}
}

// A user's local-only profile whose hive_tiers tops out at a cloud model
// never moves the hive onto it: each scan of a wave of unknowns records the
// rise as not applicable, naming the model, and the tier stays local.
func TestHiveTier_LocalOnlyProfileStaysLocal(t *testing.T) {
	const cloudTop = "claude-opus-4-8"
	modelsPath := userModels(t, `  local-only:
    quality: test
    provider: local
    hive_tiers: [qwen-small, qwen-mid, `+cloudTop+`]
    roles:
      hive-research: {model: qwen-mid, reasoning: none}
`)
	env := []string{"HIVE_PROFILE=local-only"}
	dir, dbPath := tierHive(t, modelsPath, env)
	for scan := 1; scan <= 3; scan++ {
		if out, err := runChb(t, dir, modelsPath, env, "--db", dbPath, "hive", "next", "--project", "p", "--apply"); err != nil {
			t.Fatalf("scan %d: %v\n%s", scan, err, out)
		}
		tier, outcome, reason := tierState(t, dbPath)
		if tier != "qwen-mid" || outcome != "not_applicable" || !strings.Contains(reason, cloudTop+" is off this machine") {
			t.Fatalf("scan %d: tier %s, outcome %s (%s), want qwen-mid held and the rise not applicable", scan, tier, outcome, reason)
		}
	}
}

// Under a local-only profile a rung off this machine is outside the range
// wherever it sits: `chb hive init` starts below a middle rung that is off
// it, and a tier already on such a rung, as another run or params may leave
// it, moves to the nearest rung on this machine on the next scan.
func TestHiveTier_LocalOnlyProfileLeavesNoCloudRung(t *testing.T) {
	const cloudMid, cloudTop = "gpt-oss:120b-cloud", "claude-opus-4-8"
	modelsPath := userModels(t, `  cloud-mid:
    quality: test
    provider: local
    hive_tiers: [qwen-small, `+cloudMid+`, `+cloudTop+`]
    roles:
      hive-research: {model: qwen-small, reasoning: none}
`)
	env := []string{"HIVE_PROFILE=cloud-mid"}
	dir, dbPath := tierHive(t, modelsPath, env)
	next := func() (tier, outcome, reason string) {
		t.Helper()
		if out, err := runChb(t, dir, modelsPath, env, "--db", dbPath, "hive", "next", "--project", "p", "--apply"); err != nil {
			t.Fatalf("hive next: %v\n%s", err, out)
		}
		return tierState(t, dbPath)
	}
	if tier, outcome, reason := next(); tier != "qwen-small" || outcome != "not_applicable" || !strings.Contains(reason, cloudMid+" is off this machine") {
		t.Fatalf("first scan: tier %s, outcome %s (%s), want init's qwen-small held and the rise onto %s not applicable", tier, outcome, reason, cloudMid)
	}

	s, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteDB.Exec(`UPDATE hive_state SET model_tier=? WHERE project='p'`, cloudTop)
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if tier, outcome, reason := next(); tier != "qwen-small" || outcome != "clamp" || !strings.Contains(reason, cloudTop+" is off this machine") {
		t.Fatalf("scan from %s: tier %s, outcome %s (%s), want a clamp to qwen-small", cloudTop, tier, outcome, reason)
	}
}
