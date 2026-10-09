package workflow

import "testing"

func TestNodeMaxRetriesFromYAML_BadYAML(t *testing.T) {
	if got := nodeMaxRetriesFromYAML("\t\t::\t::", "x"); got != 0 {
		t.Errorf("got %d; want 0 for bad YAML", got)
	}
}

func TestNodeMaxRetriesFromYAML_NoNodesField(t *testing.T) {
	if got := nodeMaxRetriesFromYAML("name: t", "a"); got != 0 {
		t.Errorf("got %d; want 0 when nodes absent", got)
	}
}

func TestNodeMaxRetriesFromYAML_NodeMissing(t *testing.T) {
	yaml := `name: t
nodes:
  a:
    type: agent
`
	if got := nodeMaxRetriesFromYAML(yaml, "ghost"); got != 0 {
		t.Errorf("got %d; want 0 for missing node", got)
	}
}

func TestNodeMaxRetriesFromYAML_MalformedNode(t *testing.T) {
	yaml := `name: t
nodes:
  a: just-a-string
`
	if got := nodeMaxRetriesFromYAML(yaml, "a"); got != 0 {
		t.Errorf("got %d; want 0 for non-map node", got)
	}
}

func TestNodeMaxRetriesFromYAML_NoMaxRetries(t *testing.T) {
	yaml := `name: t
nodes:
  a:
    type: agent
`
	if got := nodeMaxRetriesFromYAML(yaml, "a"); got != 0 {
		t.Errorf("got %d; want 0 when max_retries unset", got)
	}
}

func TestNodeMaxRetriesFromYAML_IntValue(t *testing.T) {
	yaml := `name: t
nodes:
  a:
    type: agent
    max_retries: 3
`
	if got := nodeMaxRetriesFromYAML(yaml, "a"); got != 3 {
		t.Errorf("got %d; want 3", got)
	}
}

func TestNodeMaxRetriesFromYAML_FloatValue(t *testing.T) {
	// JSON-decoded numbers can arrive as float64.
	yaml := `name: t
nodes:
  a:
    type: agent
    max_retries: 5.0
`
	if got := nodeMaxRetriesFromYAML(yaml, "a"); got != 5 {
		t.Errorf("got %d; want 5", got)
	}
}
