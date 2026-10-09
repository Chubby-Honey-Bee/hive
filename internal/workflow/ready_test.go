package workflow

import (
	"slices"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Ready nodes are handed out in name order, so a wave dispatches, logs and
// records its nodes in the same order on every run.
func TestGetNextNodes_HandsOutReadyNodesInNameOrder(t *testing.T) {
	const yaml = `name: t
nodes:
  zeta:  {type: agent, prompt: z, model: haiku}
  alpha: {type: agent, prompt: a, model: haiku}
  mu:    {type: agent, prompt: m, model: haiku}
  beta:  {type: agent, prompt: b, model: haiku}
  omega: {type: agent, prompt: o, model: haiku}
  delta: {type: agent, prompt: d, model: haiku}
`
	var seeds []db.NodeSeed
	for _, name := range []string{"zeta", "alpha", "mu", "beta", "omega", "delta"} {
		seeds = append(seeds, db.NodeSeed{Name: name, Type: "agent"})
	}
	store := newWFStore(t)
	for range 20 {
		id, err := store.Workflows().CreateWorkflowRun("t", 1, yaml, `{}`, seeds)
		if err != nil {
			t.Fatal(err)
		}
		nodes, err := GetNextNodes(store.Workflows(), id)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, len(nodes))
		for i, n := range nodes {
			names[i] = n.Node
		}
		if len(names) != len(seeds) || !slices.IsSorted(names) {
			t.Fatalf("handed out %v; want all six in name order", names)
		}
	}
}
