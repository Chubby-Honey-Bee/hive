package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// role: names a role on a model node, a repair role only on its on_reject:
// block, and ttl: is a positive duration on a model node. CheckNodeFields
// refuses anything else, naming the node, and InitWorkflow starts no run.
func TestCheckNodeFields_RoleAndTTL(t *testing.T) {
	node, repair := models.Roles[0], models.RepairRoles[0]
	cases := []struct {
		name  string
		nodes map[string]any
		want  string // "" when the definition passes
	}{
		{"a node role", map[string]any{"n": map[string]any{"type": "agent", "role": node, "ttl": "90s"}}, ""},
		{"a repair role on the block", map[string]any{"n": map[string]any{"type": "agent", "on_reject": map[string]any{"role": repair}}}, ""},
		{"a fan role", map[string]any{"n": map[string]any{"type": "parallel_fan", "role": node}}, ""},
		{"an unknown role", map[string]any{"n": map[string]any{"type": "agent", "role": "lense"}}, `node "n": role: lense is not a role`},
		{"a repair role on a node", map[string]any{"n": map[string]any{"type": "agent", "role": repair}}, `node "n": role: ` + repair + ` names a repair`},
		{"a node role on the block", map[string]any{"n": map[string]any{"type": "agent", "on_reject": map[string]any{"role": node}}}, `node "n": on_reject: role: ` + node + ` is not a repair role`},
		{"a role on a decision", map[string]any{"d": map[string]any{"type": "decision", "role": node}}, `node "d": role: applies to agent and parallel_fan nodes only`},
		{"a ttl that is not a duration", map[string]any{"n": map[string]any{"type": "agent", "ttl": "soon"}}, `node "n": ttl: soon is not a positive duration`},
		{"a zero ttl", map[string]any{"n": map[string]any{"type": "agent", "ttl": "0s"}}, `node "n": ttl: 0s is not a positive duration`},
		{"a number ttl", map[string]any{"n": map[string]any{"type": "agent", "ttl": 30}}, `node "n": ttl: 30 is not a positive duration`},
		{"a ttl on a decision", map[string]any{"d": map[string]any{"type": "decision", "ttl": "1m"}}, `node "d": ttl: applies to agent and parallel_fan nodes only`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckNodeFields(map[string]any{"nodes": c.nodes})
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
		})
	}
}

// A command node takes neither: role: and ttl: are keys it refuses.
func TestCheckNodeFields_CommandNodeTakesNoRole(t *testing.T) {
	for _, key := range []string{"role", "ttl"} {
		err := CheckNodeFields(map[string]any{"nodes": map[string]any{"c": map[string]any{"type": "command", "argv": []any{"true"}, key: "x"}}})
		if err == nil || !strings.Contains(err.Error(), `unknown key "`+key+`"`) {
			t.Errorf("%s on a command node: err = %v", key, err)
		}
	}
}

// NodeTTL reads the duration as written, and a dispatch node carries it.
func TestNodeTTL_OnTheDispatchNode(t *testing.T) {
	const ttl = "2m30s"
	want, err := time.ParseDuration(ttl)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := NodeTTL(map[string]any{"ttl": ttl}); err != nil || got != want {
		t.Fatalf("NodeTTL = %v, %v; want %v", got, err, want)
	}
	if got, err := NodeTTL(map[string]any{}); err != nil || got != 0 {
		t.Fatalf("NodeTTL of a node without one = %v, %v; want 0", got, err)
	}
	path := writeWorkflow(t, "name: t\nnodes:\n  a:\n    type: agent\n    role: lens\n    ttl: "+ttl+"\n    prompt: go\n")
	s := newTestStore(t)
	runID, err := InitWorkflow(s.Workflows(), path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := GetNextNodes(s.Workflows(), runID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("next = %+v, %v", nodes, err)
	}
	if nodes[0].TTL != want {
		t.Errorf("dispatch node TTL %v, want %v", nodes[0].TTL, want)
	}
}
