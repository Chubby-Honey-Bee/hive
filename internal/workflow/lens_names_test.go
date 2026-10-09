package workflow

import (
	"reflect"
	"testing"
)

// LensNames lists the lens foragers of a definition, sorted, by the rule
// the run tokens apply: forager_name, else a forager- prefix, and never a
// dreamer.
func TestLensNames(t *testing.T) {
	defn := mustDefn(t, tokenSwarm)
	if got, want := LensNames(defn), []string{"a", "b", "c", "d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("LensNames = %v, want %v", got, want)
	}
	named := mustDefn(t, `name: named
nodes:
  lens-one: {type: agent, forager_name: skeptic, prompt: "p"}
  forager-optimist: {type: agent, prompt: "p"}
  dreamer-z: {type: agent, archetype: Dreamer, forager_name: z, prompt: "ripen"}
  queen: {type: agent, prompt: "q"}
edges: []
`)
	if got, want := LensNames(named), []string{"optimist", "skeptic"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("LensNames = %v, want %v", got, want)
	}
	if got := LensNames(map[string]any{}); got != nil {
		t.Fatalf("LensNames of no nodes = %v", got)
	}
}
