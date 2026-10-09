package runner

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"gopkg.in/yaml.v3"
)

// shippedModels is the models config the binary embeds, read from its file,
// so a user's own models.yaml does not change what these tests expect.
func shippedModels(t *testing.T) *models.Config {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "models", "default-models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg models.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

// modelsFrom parses a models config holding only profiles.
func modelsFrom(t *testing.T, text string) *models.Config {
	t.Helper()
	var cfg models.Config
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

// Every shipped profile resolves, keeps every role on this machine, and
// carries each route as the config writes it.
func TestResolveProfile_Shipped(t *testing.T) {
	cfg := shippedModels(t)
	if len(cfg.Profiles) == 0 {
		t.Fatal("the shipped config has no profiles")
	}
	for name, mp := range cfg.Profiles {
		t.Run(name, func(t *testing.T) {
			p, err := resolveProfileFrom(cfg, name)
			if err != nil {
				t.Fatal(err)
			}
			if cloud := p.CloudRoles(); len(cloud) > 0 {
				t.Errorf("cloud roles %v in a shipped local profile", cloud)
			}
			if p.Provider != canonicalKind(mp.Provider) || p.Quality != mp.Quality {
				t.Errorf("provider %q quality %q, want %q and %q", p.Provider, p.Quality, mp.Provider, mp.Quality)
			}
			for role, r := range mp.Roles {
				got := p.Routes[role]
				want := Route{Role: role, Model: r.Model, Provider: canonicalKind(firstNonEmptyString(r.Provider, mp.Provider)), Reasoning: r.Reasoning, Tools: r.Tools, Because: r.Because}
				if r.TTL != "" {
					d, err := time.ParseDuration(r.TTL)
					if err != nil {
						t.Fatal(err)
					}
					want.TTL = d
				}
				if got.Model != want.Model || got.Provider != want.Provider || got.Reasoning != want.Reasoning || got.TTL != want.TTL || !equalTools(got.Tools, want.Tools) {
					t.Errorf("role %s = %+v, want %+v", role, got, want)
				}
			}
		})
	}
}

func equalTools(a, b *[]string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(*a, *b)
}

// A profile that would route a role wrongly, or off this machine without
// saying why, is refused naming the role and the problem.
func TestResolveProfile_Refusals(t *testing.T) {
	cases := []struct {
		name, roles, want string
	}{
		{"an unknown role", "lense: {model: m}", "role lense: not a role"},
		{"no model", "lens: {reasoning: none}", "role lens: names no model"},
		{"an unknown provider", "lens: {model: m, provider: ollama}", `role lens: provider "ollama" is not a known provider`},
		{"a bad reasoning level", "lens: {model: m, reasoning: extreme}", `role lens: reasoning "extreme" is not one of`},
		{"an unknown tool", "lens: {model: m, tools: [zsh]}", `role lens: tools names unknown tool "zsh"`},
		{"a bad ttl", "lens: {model: m, ttl: soon}", `role lens: ttl "soon" is not a positive duration`},
		{"a cloud route without because", "queen: {model: claude-opus-4-8, provider: anthropic}", "role queen: routes claude-opus-4-8 on anthropic, a cloud route: provider anthropic, whatever endpoint serves it"},
		{"an openai route, which names the local provider", "lens: {model: qwen3.5:4b, provider: openai}", "role lens: routes qwen3.5:4b on openai, a cloud route: provider openai, whatever endpoint serves it, since only provider local counts as this machine (a server on it, such as Ollama, is provider: local"},
		{"an Ollama cloud model without because", "lens: {model: gpt-oss:120b-cloud}", "role lens: routes gpt-oss:120b-cloud on local, a cloud route: an Ollama cloud model"},
		{"a misspelled route key", "lens: {model: m, reasonig: none}", `role lens: unknown key "reasonig" (known: ` + strings.Join(models.RouteKeys, ", ") + ")"},
		{"a repair route with a reasoning level", "implement-fix: {model: f}\n      implement-repair: {model: r, reasoning: none}", "role implement-repair: a repair runs with its node's reasoning, tools and ttl"},
		{"a repair route on another provider", "implement-fix: {model: f}\n      implement-repair: {model: r, provider: openai, because: x}", "role implement-repair: a repair runs on its node's provider"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := modelsFrom(t, "profiles:\n  p:\n    provider: local\n    roles:\n      "+c.roles+"\n")
			_, err := resolveProfileFrom(cfg, "p")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
		})
	}
	t.Run("bash, shell's alias, is a tool", func(t *testing.T) {
		if _, err := resolveProfileFrom(modelsFrom(t, "profiles:\n  p:\n    provider: local\n    roles:\n      lens: {model: m, tools: [bash]}\n"), "p"); err != nil {
			t.Fatalf("tools: [bash] refused: %v", err)
		}
	})
	t.Run("a misspelled profile key", func(t *testing.T) {
		_, err := resolveProfileFrom(modelsFrom(t, "profiles:\n  p:\n    provider: local\n    role:\n      lens: {model: m}\n"), "p")
		if want := `unknown key "role" (known: ` + strings.Join(models.ProfileKeys, ", ") + ")"; err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want one containing %q", err, want)
		}
	})
	t.Run("no provider anywhere", func(t *testing.T) {
		_, err := resolveProfileFrom(modelsFrom(t, "profiles:\n  p:\n    roles:\n      lens: {model: m}\n"), "p")
		if err == nil || !strings.Contains(err.Error(), "role lens: names no provider") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a cloud route that says why", func(t *testing.T) {
		const because = "Bench-1 report 12: the local Queen failed non-inferiority"
		p, err := resolveProfileFrom(modelsFrom(t, "profiles:\n  p:\n    provider: local\n    roles:\n      lens: {model: m}\n      queen: {model: claude-opus-4-8, provider: anthropic, because: \""+because+"\"}\n"), "p")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.CloudRoles(); !slices.Equal(got, []string{"queen"}) || p.Routes["queen"].Because != because {
			t.Fatalf("cloud roles %v, queen %+v", got, p.Routes["queen"])
		}
	})
}

// ollamaCloudModel reads the tag after the last colon.
func TestOllamaCloudModel(t *testing.T) {
	for model, want := range map[string]bool{
		"gpt-oss:120b-cloud": true, "glm-4.6:cloud": true, "qwen3.5:4b": false, "cloud-model": false, "registry:5000/m:cloud": true, "m:Cloud": true,
	} {
		if got := ollamaCloudModel(model); got != want {
			t.Errorf("ollamaCloudModel(%q) = %v, want %v", model, got, want)
		}
	}
}

const profileWorkflow = `name: routed
nodes:
  lens-a:
    type: agent
    role: lens
    tier: synthesist
    reasoning: high
    prompt: go
    outputs: [zeta, alpha]
    output_schema:
      type: object
      required: [zeta, alpha]
      properties:
        zeta: {type: string}
        alpha: {type: string}
    on_reject:
      tier: planner
      max_repair_iterations: 1
  fix:
    type: agent
    role: implement-fix
    model: sonnet
    reasoning: low
    prompt: go
    on_reject:
      role: implement-repair
      tier: synthesist
      max_repair_iterations: 1
  plain:
    type: agent
    tier: worker
    prompt: go
edges:
  - {from: lens-a, to: fix}
  - {from: fix, to: plain}
`

const routingProfile = `profiles:
  p:
    provider: local
    roles:
      lens: {model: lens-model, reasoning: none, tools: [], ttl: 90s}
      implement-fix: {model: fix-model}
      implement-repair: {model: repair-model}
`

// A profile writes each routed node's route and leaves the rest as written;
// the schema keeps its declared order.
func TestApplyProfile(t *testing.T) {
	p, err := resolveProfileFrom(modelsFrom(t, routingProfile), "p")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ApplyProfile(profileWorkflow, p)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := workflow.LoadYAMLString(profileWorkflow)
	after, err := workflow.LoadYAMLString(out)
	if err != nil {
		t.Fatal(err)
	}
	node := func(defn map[string]any, name string) map[string]any {
		return defn["nodes"].(map[string]any)[name].(map[string]any)
	}

	lens, lr := node(after, "lens-a"), p.Routes["lens"]
	if lens["model"] != lr.Model || lens["provider"] != string(lr.Provider) || lens["reasoning"] != lr.Reasoning {
		t.Errorf("lens-a = %v, want model %s provider %s reasoning %s", lens, lr.Model, lr.Provider, lr.Reasoning)
	}
	if _, has := lens["tier"]; has {
		t.Error("lens-a keeps its tier: beside the routed model")
	}
	if tools, ok := lens["tools"].([]any); !ok || len(tools) != len(*lr.Tools) {
		t.Errorf("lens-a tools %v, want %v", lens["tools"], *lr.Tools)
	}
	if ttl, err := workflow.NodeTTL(lens); err != nil || ttl != lr.TTL {
		t.Errorf("lens-a ttl %v (%v), want %v", lens["ttl"], err, lr.TTL)
	}
	if block := lens["on_reject"].(map[string]any); block["tier"] != nil || block["model"] != nil {
		t.Errorf("lens-a on_reject %v names a model; with no role it repairs on the routed one", block)
	}

	fix, fr, rr := node(after, "fix"), p.Routes["implement-fix"], p.Routes["implement-repair"]
	if fix["model"] != fr.Model || fix["provider"] != string(fr.Provider) || fix["reasoning"] != node(before, "fix")["reasoning"] {
		t.Errorf("fix = %v, want model %s, provider %s and its own reasoning", fix, fr.Model, fr.Provider)
	}
	if block := fix["on_reject"].(map[string]any); block["model"] != rr.Model || block["tier"] != nil {
		t.Errorf("fix on_reject %v, want model %s and no tier", block, rr.Model)
	}

	if plain := node(after, "plain"); !mapsEqual(plain, node(before, "plain")) {
		t.Errorf("plain, which names no role, became %v", plain)
	}

	schemas, err := workflow.OutputSchemas(out)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, prop := range schemas["lens-a"].Properties {
		order = append(order, prop.Name)
	}
	if want := []string{"zeta", "alpha"}; !slices.Equal(order, want) {
		t.Errorf("schema order %v, want %v", order, want)
	}
}

func mapsEqual(a, b map[string]any) bool {
	x, _ := yaml.Marshal(a)
	y, _ := yaml.Marshal(b)
	return string(x) == string(y)
}

// A route that makes a node invalid, tools: [] on a node that needs a tool
// call, is refused naming the profile.
func TestApplyProfile_RefusesAnInvalidResult(t *testing.T) {
	p, err := resolveProfileFrom(modelsFrom(t, "profiles:\n  p:\n    provider: local\n    roles:\n      hive-research: {model: m, tools: []}\n"), "p")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ApplyProfile("name: t\nnodes:\n  d:\n    type: agent\n    role: hive-research\n    min_tool_calls: 1\n    prompt: go\n", p)
	if err == nil || !strings.Contains(err.Error(), "routing profile p on this workflow") || !strings.Contains(err.Error(), "min_tool_calls") {
		t.Fatalf("err = %v", err)
	}
}

const defaultProfile = `profiles:
  p:
    provider: local
    default: {model: default-model, reasoning: none, ttl: 2m}
    roles:
      lens: {model: lens-model}
`

const defaultWorkflow = `name: unrouted
nodes:
  plain:
    type: agent
    tier: worker
    prompt: go
    on_reject:
      tier: planner
      max_repair_iterations: 1
  fan:
    type: parallel_fan
    tier: worker
    prompt_template: go {item}
    fan_source: items
    fan_placeholder: "{item}"
  unrouted-role:
    type: agent
    role: queen
    tier: synthesist
    prompt: go
  lens-a:
    type: agent
    role: lens
    tier: synthesist
    prompt: go
  own-model:
    type: agent
    model: sonnet
    prompt: go
  own-provider:
    type: agent
    provider: anthropic
    tier: worker
    prompt: go
  gate:
    type: command
    argv: [echo, hi]
edges: []
`

// A profile's default route serves each node sent to a backend that no role
// route serves and that names no model and no provider of its own: such a
// node takes the route as a routed node takes its role's, its repair
// included. A node that names a model or a provider runs as written, and so
// does a node no backend serves. The default route keeps its calls on this
// machine: pointed at another host, they are refused.
func TestApplyProfile_DefaultRoute(t *testing.T) {
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	p, err := resolveProfileFrom(modelsFrom(t, defaultProfile), "p")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ApplyProfile(defaultWorkflow, p)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := workflow.LoadYAMLString(defaultWorkflow)
	after, err := workflow.LoadYAMLString(out)
	if err != nil {
		t.Fatal(err)
	}
	node := func(defn map[string]any, name string) map[string]any {
		return defn["nodes"].(map[string]any)[name].(map[string]any)
	}
	for _, name := range []string{"plain", "fan", "unrouted-role"} {
		n := node(after, name)
		if n["model"] != "default-model" || n["provider"] != "local" || n["reasoning"] != "none" || n["tier"] != nil || n["tools"] != nil {
			t.Errorf("%s = %v, want the default route's model, provider and reasoning, no tier, and its own tools", name, n)
		}
		if ttl, err := workflow.NodeTTL(n); err != nil || ttl != 2*time.Minute {
			t.Errorf("%s ttl %v (%v), want 2m", name, n["ttl"], err)
		}
	}
	if block := node(after, "plain")["on_reject"].(map[string]any); block["tier"] != nil || block["model"] != nil {
		t.Errorf("plain on_reject %v names a model; with no role it repairs on the routed one", block)
	}
	if lens := node(after, "lens-a"); lens["model"] != "lens-model" {
		t.Errorf("lens-a = %v, want its role's route", lens)
	}
	for _, name := range []string{"own-model", "own-provider", "gate"} {
		if n := node(after, name); !mapsEqual(n, node(before, name)) {
			t.Errorf("%s became %v; it runs as written", name, n)
		}
	}

	t.Setenv("HIVE_LOCAL_BASE_URL", "")
	lines := Routing(Config{Provider: "local"}, after)
	want := RouteLine{Nodes: []string{"fan", "plain"}, Provider: BackendLocal, Model: "default-model", Endpoint: defaultLocalBaseURL, Reasoning: "none"}.String()
	if report := RoutingReport(p, lines, after); !slices.Contains(report, want) {
		t.Errorf("report %q lacks %q", report, want)
	}
	t.Setenv("HIVE_LOCAL_BASE_URL", "http://10.1.2.3:11434/v1")
	if err := PreflightLocality(p, Routing(Config{Provider: "local"}, after)); err == nil || !strings.Contains(err.Error(), "[fan, plain]") {
		t.Errorf("err = %v, want the default route's calls to another host refused", err)
	}
}

// The default route is checked as a role's route is, and stays on this
// machine: a call leaves it only on a role's cloud route, with its because:.
func TestResolveProfile_DefaultRouteRefusals(t *testing.T) {
	for _, c := range []struct {
		name, def string
		want      []string
	}{
		{"a cloud provider, even with because", "{model: claude-opus-4-8, provider: anthropic, because: x}", []string{"default route: routes claude-opus-4-8 on anthropic, a cloud route", "the default route stays on this machine"}},
		{"an Ollama cloud model", "{model: gpt-oss:120b-cloud}", []string{"default route: routes gpt-oss:120b-cloud on local, a cloud route: an Ollama cloud model", "the default route stays on this machine"}},
		{"no model and a misspelled key", "{reasonig: none}", []string{"default route: names no model", `default route: unknown key "reasonig"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveProfileFrom(modelsFrom(t, "profiles:\n  p:\n    provider: local\n    default: "+c.def+"\n"), "p")
			for _, want := range c.want {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want one containing %q", err, want)
				}
			}
		})
	}
}

// The routing names each role's model, provider, endpoint and reasoning,
// and what reaches the network without a model; a profile refuses a call
// that would leave this machine on a role it has no cloud route for,
// whatever takes it there, and names each cloud route with its because:.
func TestRoutingAndLocality(t *testing.T) {
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	local, err := resolveProfileFrom(modelsFrom(t, routingProfile), "p")
	if err != nil {
		t.Fatal(err)
	}
	routed := func(t *testing.T, p *Profile, extra string) map[string]any {
		t.Helper()
		text := strings.Replace(profileWorkflow, "edges:", extra+"edges:", 1)
		out, err := ApplyProfile(text, p)
		if err != nil {
			t.Fatal(err)
		}
		defn, _ := workflow.LoadYAMLString(out)
		return defn
	}
	cfg := Config{Provider: "local"}

	t.Run("every call on this machine", func(t *testing.T) {
		t.Setenv("HIVE_LOCAL_BASE_URL", "")
		defn := routed(t, local, "")
		lines := Routing(cfg, defn)
		if err := PreflightLocality(local, lines); err != nil {
			t.Fatal(err)
		}
		lr := local.Routes["lens"]
		want := RouteLine{Role: "lens", Nodes: []string{"lens-a"}, Provider: BackendLocal, Model: lr.Model, Endpoint: defaultLocalBaseURL, Reasoning: lr.Reasoning}.String()
		report := RoutingReport(local, lines, defn)
		if !slices.Contains(report, want) {
			t.Errorf("report %q lacks %q", report, want)
		}
		if !slices.Contains(report, "off this machine: nothing; every model call goes to an endpoint on it") {
			t.Errorf("report %q does not say nothing leaves", report)
		}
	})
	t.Run("what reaches the network without a model", func(t *testing.T) {
		defn := routed(t, local, "  gate:\n    type: command\n    argv: [echo, hi]\n"+
			"  sh:\n    type: agent\n    tier: worker\n    prompt: go\n    tools: [shell]\n"+
			"  ba:\n    type: agent\n    tier: worker\n    prompt: go\n    tools: [bash]\n"+
			"  ro:\n    type: agent\n    tier: worker\n    prompt: go\n    tools: [read_file]\n")
		var tools []string
		for name, raw := range defn["nodes"].(map[string]any) {
			node := raw.(map[string]any)
			list, has := node["tools"].([]any)
			if node["type"] == "agent" && (!has || slices.ContainsFunc(list, func(e any) bool {
				s, _ := e.(string)
				s = workflow.CanonicalTool(s)
				return s == "shell" || s == "web_fetch"
			})) {
				tools = append(tools, name)
			}
		}
		slices.Sort(tools)
		if !slices.Contains(tools, "sh") || !slices.Contains(tools, "ba") || slices.Contains(tools, "ro") {
			t.Fatalf("the nodes that keep a network tool are %v; want sh and ba among them and not ro", tools)
		}
		report := RoutingReport(local, Routing(cfg, defn), defn)
		for _, want := range []string{
			"network: web_fetch and shell reach the network without calling a model (shell runs any command, curl included); a tool loop may use them in " + strings.Join(tools, ", "),
			"network: command nodes run programs of their own, which may reach the network: gate",
		} {
			if !slices.Contains(report, want) {
				t.Errorf("report %q lacks %q", report, want)
			}
		}
	})
	offCases := []struct {
		name, extra, env, node string
	}{
		{"a node the profile does not route, on a cloud provider", "  outside:\n    type: agent\n    provider: anthropic\n    model: opus\n    prompt: go\n", "", "outside"},
		{"a node on a CLI", "  outside:\n    type: agent\n    provider: claude-cli\n    prompt: go\n", "", "outside"},
		{"a local endpoint on another host", "", "http://10.1.2.3:11434/v1", "lens-a"},
	}
	for _, c := range offCases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HIVE_LOCAL_BASE_URL", c.env)
			err := PreflightLocality(local, Routing(cfg, routed(t, local, c.extra)))
			if err == nil || !strings.Contains(err.Error(), "routing profile p sends data off this machine only on a cloud route with because: (it has none)") || !strings.Contains(err.Error(), "["+c.node+"]") {
				t.Fatalf("err = %v, want the refusal naming %s", err, c.node)
			}
		})
	}
	t.Run("an Ollama cloud model", func(t *testing.T) {
		t.Setenv("HIVE_LOCAL_BASE_URL", "")
		defn := routed(t, local, "  cloudy:\n    type: agent\n    provider: local\n    model: gpt-oss:120b-cloud\n    prompt: go\n")
		err := PreflightLocality(local, Routing(cfg, defn))
		if err == nil || !strings.Contains(err.Error(), "gpt-oss:120b-cloud is an Ollama cloud model") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a cloud route, named with its because", func(t *testing.T) {
		t.Setenv("HIVE_LOCAL_BASE_URL", "")
		const because = "Bench-1 report 12"
		cloudFix := strings.Replace(routingProfile, "implement-fix: {model: fix-model}", "implement-fix: {model: claude-sonnet-4-6, provider: anthropic, because: \""+because+"\"}", 1)
		if _, err := resolveProfileFrom(modelsFrom(t, cloudFix), "p"); err == nil {
			t.Fatal("a repair route left local beside a cloud fix route was not refused")
		}
		cloud, err := resolveProfileFrom(modelsFrom(t, strings.Replace(cloudFix, "implement-repair: {model: repair-model}", "implement-repair: {model: claude-opus-4-8, provider: anthropic, because: \""+because+"\"}", 1)), "p")
		if err != nil {
			t.Fatal(err)
		}
		defn := routed(t, cloud, "")
		lines := Routing(cfg, defn)
		if err := PreflightLocality(cloud, lines); err != nil {
			t.Fatalf("a profile with a cloud route refused: %v", err)
		}
		var named bool
		for _, l := range RoutingReport(cloud, lines, defn) {
			named = named || strings.HasPrefix(l, "off this machine: role implement-fix") && strings.Contains(l, "(because: "+because+")")
		}
		if !named {
			t.Errorf("report %q does not name the cloud role with its because", RoutingReport(cloud, lines, defn))
		}
		// The cloud route lets its own role leave, and no other: a local
		// endpoint on another host, or a node the profile does not route,
		// is refused beside it.
		for _, c := range []struct{ name, extra, env, node string }{
			{"a local endpoint on another host", "", "http://10.1.2.3:11434/v1", "lens-a"},
			{"a node the profile does not route", "  outside:\n    type: agent\n    provider: anthropic\n    model: opus\n    prompt: go\n", "", "outside"},
		} {
			t.Run(c.name, func(t *testing.T) {
				t.Setenv("HIVE_LOCAL_BASE_URL", c.env)
				err := PreflightLocality(cloud, Routing(cfg, routed(t, cloud, c.extra)))
				if err == nil || !strings.Contains(err.Error(), "(it has them for "+strings.Join(cloud.CloudRoles(), ", ")+")") || !strings.Contains(err.Error(), "["+c.node+"]") {
					t.Fatalf("err = %v, want the refusal naming %s", err, c.node)
				}
				if strings.Contains(err.Error(), "role implement-fix") {
					t.Errorf("the refusal names the cloud route itself: %v", err)
				}
			})
		}
	})
}

// headerRecorder records each request's Authorization before its handler
// answers.
type headerRecorder struct {
	next http.Handler
	mu   sync.Mutex
	auth []string
}

func (h *headerRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.auth = append(h.auth, r.Header.Get("Authorization"))
	h.mu.Unlock()
	h.next.ServeHTTP(w, r)
}

// A node on the local provider and one on openai reach two endpoints in one
// run: each is sent only its own node's model, each row records its own
// provider and endpoint, and the local endpoint never sees the OpenAI key.
// The run takes no profile, and its log still prints the routing.
func TestRun_LocalAndOpenAIServeDifferentNodes(t *testing.T) {
	const localModel, cloudModel, key = "local-model", "cloud-model", "sk-openai-test-key"
	reply := func(map[string]any) string { return chatReply(`{"answer":"ok"}`, "stop", 1, 1) }
	localFake := &fakeOpenAI{models: []string{localModel}, reply: reply}
	cloudFake := &fakeOpenAI{models: []string{cloudModel}, reply: reply}
	localRec := &headerRecorder{next: localFake}
	localSrv, cloudSrv := httptest.NewServer(localRec), httptest.NewServer(cloudFake)
	defer localSrv.Close()
	defer cloudSrv.Close()
	t.Setenv("HIVE_LOCAL_BASE_URL", localSrv.URL+"/v1")
	t.Setenv("OPENAI_BASE_URL", cloudSrv.URL+"/v1")
	t.Setenv("OPENAI_API_KEY", key)
	t.Setenv("HIVE_PROVIDER", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")

	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	src := "name: two\nnodes:\n  on-local:\n    type: agent\n    provider: local\n    model: " + localModel + "\n    tools: []\n    prompt: go\n    outputs: [answer]\n" +
		"    output_schema: {type: object, required: [answer], properties: {answer: {type: string}}}\n" +
		"  on-openai:\n    type: agent\n    provider: openai\n    model: " + cloudModel + "\n    prompt: go\n    outputs: [answer]\nedges: []\n"
	if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTempStore(t)
	var log bytes.Buffer
	res, err := Run(context.Background(), s, Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Provider: "local", Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []RouteLine{
		{Nodes: []string{"on-local"}, Provider: BackendLocal, Model: localModel, Endpoint: localSrv.URL + "/v1"},
		{Nodes: []string{"on-openai"}, Provider: BackendOpenAI, Model: cloudModel, Endpoint: cloudSrv.URL + "/v1"},
	} {
		if !strings.Contains(log.String(), "routing: "+l.String()) {
			t.Errorf("the run log lacks the routing %q:\n%s", l.String(), log.String())
		}
	}
	// Each endpoint is listed once by the model preflight, and only the
	// local one, whose node has a schema, gets a constraint probe.
	for _, c := range []struct {
		fake   *fakeOpenAI
		model  string
		probes int
	}{{localFake, localModel, 1}, {cloudFake, cloudModel, 0}} {
		if n := c.fake.lists.Load(); n != 1 {
			t.Errorf("the endpoint for %s was listed %d times, want 1", c.model, n)
		}
		probes := 0
		for _, b := range c.fake.bodies {
			if b["model"] != c.model {
				t.Errorf("the endpoint for %s was sent %v", c.model, b["model"])
			}
			if rf, _ := b["response_format"].(map[string]any); rf != nil {
				if js, _ := rf["json_schema"].(map[string]any); js["name"] == "hive_constraint_probe" {
					probes++
				}
			}
		}
		if probes != c.probes || len(c.fake.bodies) != c.probes+1 {
			t.Errorf("the endpoint for %s got %d calls, %d of them probes; want %d probes and one node call", c.model, len(c.fake.bodies), probes, c.probes)
		}
	}
	for _, a := range localRec.auth {
		if strings.Contains(a, key) {
			t.Errorf("the local endpoint was sent the OpenAI key: %q", a)
		}
	}
	for node, want := range map[string][2]string{"on-local": {"local", localSrv.URL + "/v1"}, "on-openai": {"openai", cloudSrv.URL + "/v1"}} {
		var provider, base string
		if err := s.ReadDB.QueryRow(`SELECT provider, base_url FROM workflow_node_states WHERE run_id = ? AND node_name = ?`, res.RunID, node).Scan(&provider, &base); err != nil {
			t.Fatal(err)
		}
		if provider != want[0] || base != want[1] {
			t.Errorf("%s recorded %s at %s, want %s at %s", node, provider, base, want[0], want[1])
		}
	}
}

// The local endpoint's model preflight refuses a model it does not serve,
// and a base URL without /v1 names HIVE_LOCAL_BASE_URL to fix.
func TestPreflightEndpointModels_Local(t *testing.T) {
	f := &fakeOpenAI{models: []string{"served"}}
	srv := httptest.NewServer(http.StripPrefix("/v1", f))
	defer srv.Close()
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	defn := func(model string) map[string]any {
		return map[string]any{"nodes": map[string]any{"n": map[string]any{"type": "agent", "provider": "local", "model": model}}}
	}
	t.Setenv("HIVE_LOCAL_BASE_URL", srv.URL+"/v1")
	checks, err := PreflightEndpointModels(context.Background(), Config{Provider: "local"}, defn("served"))
	if err != nil || len(checks) != 1 || checks[0].Provider != BackendLocal || !checks[0].Listed {
		t.Fatalf("checks = %+v, %v; want the local endpoint listed", checks, err)
	}
	if _, err := PreflightEndpointModels(context.Background(), Config{Provider: "local"}, defn("missing")); err == nil || !strings.Contains(err.Error(), `"missing" (node n)`) {
		t.Fatalf("err = %v, want the missing model refused", err)
	}
	t.Setenv("HIVE_LOCAL_BASE_URL", srv.URL)
	if _, err := PreflightEndpointModels(context.Background(), Config{Provider: "local"}, defn("served")); err == nil || !strings.Contains(err.Error(), "set HIVE_LOCAL_BASE_URL to "+srv.URL+"/v1") {
		t.Fatalf("err = %v, want it to name HIVE_LOCAL_BASE_URL", err)
	}
}

// A node's ttl: bounds its call: a reply slower than it fails the node,
// naming the bound.
func TestRun_NodeTTLBoundsTheCall(t *testing.T) {
	const ttl, delay = 80 * time.Millisecond, 600 * time.Millisecond
	f := &fakeOpenAI{models: []string{"m"}, delay: delay, reply: func(map[string]any) string { return chatReply(`{"answer":"ok"}`, "stop", 1, 1) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	t.Setenv("HIVE_LOCAL_BASE_URL", srv.URL+"/v1")
	t.Setenv("HIVE_HTTP_TIMEOUT", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte("name: ttl\nnodes:\n  slow:\n    type: agent\n    model: m\n    ttl: "+ttl.String()+"\n    prompt: go\n    outputs: [answer]\nedges: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTempStore(t)
	res, _ := Run(context.Background(), s, Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Provider: "local", Log: discardWriter{}})
	var status, errText string
	if err := s.ReadDB.QueryRow(`SELECT status, COALESCE(error, '') FROM workflow_node_states WHERE run_id = ? AND node_name = 'slow'`, res.RunID).Scan(&status, &errText); err != nil {
		t.Fatal(err)
	}
	if want := "no reply within " + ttl.String() + " (the call's TTL)"; status != "failed" || !strings.Contains(errText, want) {
		t.Fatalf("slow is %s with %q, want failed with %q", status, errText, want)
	}
}

// Run under a profile that keeps every role on this machine refuses a
// workflow with a node on a cloud provider before it creates the run.
func TestRun_ProfileRefusesACallOffTheMachine(t *testing.T) {
	const name = "local-fast"
	p, err := ResolveProfile(name)
	if err != nil {
		t.Fatal(err)
	}
	if cloud := p.CloudRoles(); len(cloud) > 0 {
		t.Skipf("the models config in use gives %s cloud roles %v", name, cloud)
	}
	t.Setenv("HIVE_PROVIDER", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_LOCAL_BASE_URL", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte("name: leak\nnodes:\n  lens-a:\n    type: agent\n    role: lens\n    prompt: go\n  outside:\n    type: agent\n    provider: anthropic\n    model: opus\n    prompt: go\nedges: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTempStore(t)
	// Set by the flag, or read from HIVE_PROFILE by Run itself, as for
	// chb-mcp and chb replicate, which set no Profile.
	for _, c := range []struct{ how, flag, env string }{{"the flag", name, ""}, {"HIVE_PROFILE", "", name}} {
		t.Run(c.how, func(t *testing.T) {
			t.Setenv("HIVE_PROFILE", c.env)
			_, err = Run(context.Background(), s, Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Profile: c.flag, Log: discardWriter{}})
			if err == nil || !strings.Contains(err.Error(), "routing profile "+name+" sends data off this machine only on a cloud route") || !strings.Contains(err.Error(), "[outside]") {
				t.Fatalf("err = %v, want the locality refusal naming outside", err)
			}
			runs, err := s.Workflows().ListWorkflowRuns(10)
			if err != nil || len(runs) != 0 {
				t.Fatalf("runs = %+v, %v; want none", runs, err)
			}
		})
	}
}

// Under a profile a node runs where it is routed or not at all. A routed
// node whose backend cannot be built refuses the run before it starts,
// whatever the run default; so does an Anthropic node with no key. Nothing
// is sent to the run default.
func TestRun_ProfileRefusesANodeItsProviderCannotServe(t *testing.T) {
	const name = "local-fast"
	anthropic := &headerRecorder{next: http.NotFoundHandler()}
	srv := httptest.NewServer(anthropic)
	defer srv.Close()
	t.Setenv("HIVE_PROFILE", "")
	t.Setenv("HIVE_PROVIDER", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_LOCAL_BASE_URL", "")
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte("name: fallback\nnodes:\n  lens-a:\n    type: agent\n    role: lens\n    prompt: go\nedges: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTempStore(t)
	t.Run("a local backend that cannot be built", func(t *testing.T) {
		t.Setenv("HIVE_HTTP_TIMEOUT", "bogus")
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
		_, err := Run(context.Background(), s, Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Profile: name, Provider: "anthropic", APIKey: "sk-ant-test", Log: discardWriter{}})
		_, wantErr := NewLocalBackend(Config{})
		if err == nil || wantErr == nil || !strings.Contains(err.Error(), "provider local cannot serve lens-a: "+wantErr.Error()) {
			t.Fatalf("err = %v, want the local backend's own error (%v) for lens-a", err, wantErr)
		}
	})
	t.Run("an Anthropic node with no key", func(t *testing.T) {
		t.Setenv("HIVE_HTTP_TIMEOUT", "")
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
		defn, err := workflow.LoadYAMLString("name: cloud-queen\nnodes:\n  lens-a:\n    type: agent\n    provider: local\n    model: m\n    prompt: go\n  queen:\n    type: agent\n    provider: anthropic\n    model: opus\n    prompt: go\nedges: []\n")
		if err != nil {
			t.Fatal(err)
		}
		if err := preflightProfileBackends(Config{Provider: "local", Profile: name}, defn); err == nil || !strings.Contains(err.Error(), "provider anthropic cannot serve queen: the anthropic backend needs ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN") || strings.Contains(err.Error(), "lens-a") {
			t.Fatalf("err = %v, want the missing key named for queen", err)
		}
	})
	runs, err := s.Workflows().ListWorkflowRuns(10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs = %+v, %v; want none", runs, err)
	}
	if len(anthropic.auth) != 0 {
		t.Errorf("the run default was sent %d requests", len(anthropic.auth))
	}
}

// Should a routed node's backend stop building mid-run, under a profile the
// node fails naming why, where without one it runs on the run default.
func TestResolveDispatchParams_NoFallbackUnderAProfile(t *testing.T) {
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_HTTP_TIMEOUT", "bogus")
	_, buildErr := NewLocalBackend(Config{})
	if buildErr == nil {
		t.Fatal("the local backend builds with HIVE_HTTP_TIMEOUT=bogus, so nothing is tested")
	}
	store := newTempStore(t)
	runID := seedAgentRun(t, store, "lens-a", "name: t\nnodes:\n  lens-a:\n    type: agent\n    prompt: go\n")
	node := workflow.DispatchNode{Node: "lens-a", Provider: "local", Model: "m"}
	runDefault := &countingBackend{}
	for _, profile := range []string{"", "local-fast"} {
		rc := buildAgentNodeRC(t, store, runID, nil, runDefault, Config{Provider: "anthropic", Profile: profile})
		p := rc.resolveDispatchParams(node)
		_, err := p.backend.Run(context.Background(), RunRequest{Prompt: "go"})
		if profile == "" {
			if err != nil || runDefault.calls != 1 {
				t.Errorf("with no profile: err %v, %d run-default calls; want the fallback", err, runDefault.calls)
			}
			continue
		}
		if want := "provider local cannot serve node lens-a: " + buildErr.Error(); err == nil || err.Error() != want || runDefault.calls != 1 {
			t.Errorf("under a profile: err %v, %d run-default calls; want %q and no call", err, runDefault.calls, want)
		}
		if p.kind != BackendLocal || p.provider != "local" {
			t.Errorf("under a profile the node is recorded on %s (%s), want local", p.provider, p.kind)
		}
	}
}

// countingBackend answers every call and counts them.
type countingBackend struct{ calls int }

func (b *countingBackend) Run(context.Context, RunRequest) (*RunResult, error) {
	b.calls++
	return &RunResult{FinalText: "{}"}, nil
}

// A resume dispatches the workflow its run row stores. Under a profile that
// must be the file as the profile routes it; a run started without the
// profile is refused before anything is asked, and one started under it
// resumes on its stored routing.
func TestRun_ResumeUnderAProfile(t *testing.T) {
	const name = "local-fast"
	t.Setenv("HIVE_PROFILE", "")
	t.Setenv("HIVE_PROVIDER", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_LOCAL_BASE_URL", "")
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	src := "name: resume\nnodes:\n  lens-a:\n    type: agent\n    role: lens\n    tier: synthesist\n    prompt: go\nedges: []\n"
	if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTempStore(t)
	unrouted, err := workflow.InitWorkflow(s.Workflows(), wf, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), s, Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Profile: name, ResumeRunID: unrouted, Log: discardWriter{}})
	if want := fmt.Sprintf("run %d did not start from this workflow as routing profile %s routes it", unrouted, name); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}

	p, err := ResolveProfile(name)
	if err != nil {
		t.Fatal(err)
	}
	routedText, err := ApplyProfile(src, p)
	if err != nil {
		t.Fatal(err)
	}
	routedPath := filepath.Join(t.TempDir(), "wf.yaml")
	if err := os.WriteFile(routedPath, []byte(routedText), 0o644); err != nil {
		t.Fatal(err)
	}
	started, err := workflow.InitWorkflow(s.Workflows(), routedPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defn, err := resumedDefinition(s, Config{WorkflowYAML: routedPath, ResumeRunID: started}, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := defn["nodes"].(map[string]any)["lens-a"].(map[string]any)["model"]; got != p.Routes["lens"].Model {
		t.Errorf("the resumed lens-a is routed to %v, want %s", got, p.Routes["lens"].Model)
	}
	// Without a profile the stored workflow is still the one routed.
	defn, err = resumedDefinition(s, Config{WorkflowYAML: wf, ResumeRunID: started}, nil)
	if err != nil || defn["nodes"].(map[string]any)["lens-a"].(map[string]any)["model"] != p.Routes["lens"].Model {
		t.Errorf("without a profile the resume routed %v (%v), want the stored workflow", defn, err)
	}
}
