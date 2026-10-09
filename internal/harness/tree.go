package harness

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/bench"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// prepareTree copies the personas a swarm reads — foragers/ and agents/ —
// into <dir>/tree, which becomes the run's working directory. The
// in-process file tools refuse paths outside it; the bash tool and the
// claude CLI's own tools do not, so a run can still write elsewhere.
// treeCheck catches only what a run writes inside the tree.
func (h *agentHarness) prepareTree(dir string) (string, error) {
	tree := filepath.Join(dir, "tree")
	if err := os.RemoveAll(tree); err != nil {
		return "", err
	}
	if err := os.CopyFS(filepath.Join(tree, "foragers"), h.Foragers().FS); err != nil {
		return "", fmt.Errorf("copy foragers: %w", err)
	}
	if err := os.CopyFS(filepath.Join(tree, "agents"), os.DirFS("agents")); err != nil {
		return "", fmt.Errorf("copy agents: %w", err)
	}
	return tree, nil
}

// treeEnv points the forager registry at the tree's copy, whatever
// HIVE_FORAGERS_DIR the caller had set.
func treeEnv(env []string, tree string) []string {
	return append(append([]string(nil), env...), "HIVE_FORAGERS_DIR="+filepath.Join(tree, "foragers"))
}

// treeManifest maps every path under dir to what it holds: a directory
// marker, a symlink's target, or the sha256 of a file's bytes.
func treeManifest(dir string) (map[string]string, error) {
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		v, err := harnessTreeEntry(path, d)
		if err != nil {
			return err
		}
		m[rel] = v
		return nil
	})
	return m, err
}

// harnessTreeEntry is what one path of a tree holds: a directory marker, a
// symlink's target, or the sha256 of a file's bytes.
func harnessTreeEntry(path string, d fs.DirEntry) (string, error) {
	switch {
	case d.IsDir():
		return "dir", nil
	case d.Type()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		return "link " + target, err
	}
	return treeFileSHA256(path)
}

// treeFileSHA256 is the hex sha256 of the file's bytes.
func treeFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// treeChanges names the paths added (+), removed (-) or changed (~)
// between two manifests, sorted.
func treeChanges(before, after map[string]string) []string {
	out := append(treeAddedOrChanged(before, after), treeRemoved(before, after)...)
	sort.Strings(out)
	return out
}

// treeAddedOrChanged names the paths of after that before lacks (+) or
// holds otherwise (~).
func treeAddedOrChanged(before, after map[string]string) []string {
	var out []string
	for p, v := range after {
		switch was, ok := before[p]; {
		case !ok:
			out = append(out, "+"+p)
		case was != v:
			out = append(out, "~"+p)
		}
	}
	return out
}

// treeRemoved names the paths of before that after lacks (-).
func treeRemoved(before, after map[string]string) []string {
	var out []string
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, "-"+p)
		}
	}
	return out
}

// treeCheck compares the tree against its manifest from before the run.
// It reports whether the tree is unchanged, and what changed if not.
func treeCheck(tree string, before map[string]string, beforeErr error) (bool, string) {
	if beforeErr != nil {
		return false, beforeErr.Error()
	}
	after, err := treeManifest(tree)
	if err != nil {
		return false, err.Error()
	}
	changes := treeChanges(before, after)
	return len(changes) == 0, strings.Join(changes, ", ")
}

// nodeStats reads every node of a workflow run: its model, status, tokens
// and wall time. Wall time has whole-second resolution because the engine
// stamps RFC 3339 times.
func nodeStats(store *db.Store, runID int64) ([]bench.NodeStat, error) {
	rows, err := store.ReadDB.Query(`SELECT node_name, COALESCE(status, ''), COALESCE(tokens_in, 0), COALESCE(tokens_out, 0),
		COALESCE(started_at, ''), COALESCE(completed_at, ''), COALESCE(resolved_model, '')
		FROM workflow_node_states WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bench.NodeStat
	for rows.Next() {
		n, err := harnessNodeStatRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// harnessNodeStatRow reads one node row of nodeStats's query.
func harnessNodeStatRow(rows *sql.Rows) (bench.NodeStat, error) {
	var n bench.NodeStat
	var started, completed string
	if err := rows.Scan(&n.Node, &n.Status, &n.TokensIn, &n.TokensOut, &started, &completed, &n.Model); err != nil {
		return n, err
	}
	n.WallSeconds = harnessNodeWallSeconds(started, completed)
	return n, nil
}

// harnessNodeWallSeconds is the time from started to completed, both RFC 3339;
// 0 when either does not parse.
func harnessNodeWallSeconds(started, completed string) float64 {
	s, e1 := time.Parse(time.RFC3339, started)
	c, e2 := time.Parse(time.RFC3339, completed)
	if e1 != nil || e2 != nil {
		return 0
	}
	return c.Sub(s).Seconds()
}
