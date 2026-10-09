package review

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A node may use coder-fix only when its outputs are exactly the persona's
// contract: coder-fix's persona (the system prompt) requires every response
// to end with {compile_ok, tests_pass, diff_summary}, which a node whose
// prompt and accept gate want plan_ok cannot meet.
func TestGenerateImplementWorkflow_PersonaContractsMatchNodeOutputs(t *testing.T) {
	agents := filepath.Join("..", "..", "agents")
	persona, err := os.ReadFile(filepath.Join(agents, "coder-fix.md"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile("(?s)## Output contract.*?```json\n(.*?)```").FindSubmatch(persona)
	if block == nil {
		t.Fatal("coder-fix.md has no Output contract JSON block")
	}
	var contract []string
	for _, m := range regexp.MustCompile(`"(\w+)":`).FindAllSubmatch(block[1], -1) {
		contract = append(contract, string(m[1]))
	}
	sort.Strings(contract)

	text, _, err := GenerateImplementWorkflow(sampleAggregate(), ImplementOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Nodes map[string]struct {
			Type    string   `yaml:"type"`
			Agent   string   `yaml:"agent"`
			Outputs []string `yaml:"outputs"`
		} `yaml:"nodes"`
	}
	if err := yaml.Unmarshal([]byte(text), &wf); err != nil {
		t.Fatal(err)
	}
	for name, n := range wf.Nodes {
		// A command node calls no model, so it has no persona.
		if n.Type == "command" {
			continue
		}
		if _, err := os.Stat(filepath.Join(agents, n.Agent+".md")); err != nil {
			t.Errorf("%s: persona %q not found: %v", name, n.Agent, err)
		}
		if n.Agent != "coder-fix" {
			continue
		}
		got := append([]string(nil), n.Outputs...)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(contract, ",") {
			t.Errorf("%s uses coder-fix, whose contract is %v, but outputs %v", name, contract, n.Outputs)
		}
	}
}

// coder-fix's steps are for one fix to one file. A node that runs as
// coder-fix but is no fix node — final-gate, which edits nothing — must be
// one the persona names and describes, or the agent follows the fix steps.
func TestGenerateImplementWorkflow_CoderFixNamesItsNonFixNodes(t *testing.T) {
	persona, err := os.ReadFile(filepath.Join("..", "..", "agents", "coder-fix.md"))
	if err != nil {
		t.Fatal(err)
	}
	text, _, err := GenerateImplementWorkflow(sampleAggregate(), ImplementOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Nodes map[string]struct {
			Agent string `yaml:"agent"`
		} `yaml:"nodes"`
	}
	if err := yaml.Unmarshal([]byte(text), &wf); err != nil {
		t.Fatal(err)
	}
	fixNode := regexp.MustCompile(`^fix-\d+$`)
	for name, n := range wf.Nodes {
		if n.Agent != "coder-fix" || fixNode.MatchString(name) {
			continue
		}
		if !strings.Contains(string(persona), "`"+name+"`") {
			t.Errorf("%s runs as coder-fix, but coder-fix.md never names it", name)
		}
	}
}
