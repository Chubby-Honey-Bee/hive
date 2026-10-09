package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	hive "github.com/Chubby-Honey-Bee/hive"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// schemaServer is an OpenAI-compatible server on this machine that answers
// every call with the smallest object its response_format schema allows,
// and records what each call was sent.
type schemaServer struct {
	served []string

	mu    sync.Mutex
	calls []sentCall
	bad   []string
}

// sentCall is what one chat call carried: its schema's name (the node's), its
// model, its reasoning_effort (and whether it sent one) and whether it
// offered tools.
type sentCall struct {
	name, model, effort string
	sentEffort, tools   bool
}

func (s *schemaServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/show":
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "thinking"}})
		return
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		data := []map[string]string{}
		for _, m := range s.served {
			data = append(data, map[string]string{"id": m, "object": "model"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
		return
	case r.URL.Path != "/v1/chat/completions":
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	call := sentCall{}
	call.model, _ = body["model"].(string)
	call.effort, call.sentEffort = body["reasoning_effort"].(string)
	_, call.tools = body["tools"]
	rf, _ := body["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	call.name, _ = js["name"].(string)
	sch, _ := js["schema"].(map[string]any)
	s.mu.Lock()
	s.calls = append(s.calls, call)
	if sch == nil {
		s.bad = append(s.bad, "a call carried no schema: "+call.model)
	}
	s.mu.Unlock()
	content := mustJSON(smallest(sch))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 10},
	})
}

// smallest is the smallest value a schema allows: its const, its first enum
// value, an object of its required properties, an array of its minimum
// length (at least one item), a string of its minimum length, a number at
// its minimum, else one.
func smallest(s map[string]any) any {
	if s == nil {
		return map[string]any{}
	}
	if c, ok := s["const"]; ok {
		return c
	}
	if e, ok := s["enum"].([]any); ok && len(e) > 0 {
		return e[0]
	}
	typ, _ := s["type"].(string)
	if list, ok := s["type"].([]any); ok && len(list) > 0 {
		typ, _ = list[0].(string)
	}
	num := func(k string, def float64) float64 {
		if v, ok := s[k].(float64); ok {
			return v
		}
		return def
	}
	switch typ {
	case "object":
		props, _ := s["properties"].(map[string]any)
		out := map[string]any{}
		req, _ := s["required"].([]any)
		for _, k := range req {
			name, _ := k.(string)
			sub, _ := props[name].(map[string]any)
			out[name] = smallest(sub)
		}
		return out
	case "array":
		n := max(int(num("minItems", 1)), 1)
		if m, ok := s["maxItems"].(float64); ok && int(m) < n {
			n = int(m)
		}
		items, _ := s["items"].(map[string]any)
		out := make([]any, n)
		for i := range out {
			out[i] = smallest(items)
		}
		return out
	case "integer", "number":
		return num("minimum", 1)
	case "boolean":
		return true
	}
	return strings.Repeat("x", max(int(num("minLength", 1)), 1))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// buildChb compiles chb into dir, without the race detector, which would
// slow every run of it several times over.
func buildChb(t *testing.T, dir string) string {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go tool is not on PATH: %v", err)
	}
	out := filepath.Join(dir, "chb")
	cmd := exec.Command(gobin, "build", "-o", out, filepath.Join("..", "..", "cmd", "chb"))
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, msg)
	}
	return out
}

// The acceptance run: chb ask --profile local-small against a server on this
// machine that answers every call from its schema. Each role's calls carry
// the model and reasoning level local-small routes it to, offer no tools,
// and go to HIVE_LOCAL_BASE_URL; the run log prints the routing and says
// nothing leaves the machine; each node row records its model and the local
// provider. What local-small routes is read from the models config the
// binary embeds, and the lenses from the live roster.
func TestAskProfileLocalSmall_EachRoleOnItsRoute(t *testing.T) {
	const profile = "local-small"
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "models", "default-models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var shipped models.Config
	if err := yaml.Unmarshal(raw, &shipped); err != nil {
		t.Fatal(err)
	}
	routes := shipped.Profiles[profile].Roles
	var served []string
	for _, role := range []string{"scope", "lens", "evaluate", "followup", "queen"} {
		if routes[role].Model == "" {
			t.Fatalf("%s routes no %s", profile, role)
		}
		if !slices.Contains(served, routes[role].Model) {
			served = append(served, routes[role].Model)
		}
	}
	foragersAbs, err := filepath.Abs(filepath.Join("..", "..", "foragers"))
	if err != nil {
		t.Fatal(err)
	}
	all, err := foragers.Load(foragersAbs)
	if err != nil {
		t.Fatal(err)
	}
	lenses, _ := foragers.SplitByArchetype(foragers.Minimal(all))

	srv := &schemaServer{served: served}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	chb := buildChb(t, t.TempDir())
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hive.db")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, chb, "--db", dbPath, "ask", "Should the profile route every role?",
		"--profile", profile, "--foragers", "minimal", "--scope", "--followups", "1",
		"--out", filepath.Join(dir, "swarm.yaml"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HIVE_LOCAL_BASE_URL="+ts.URL+"/v1",
		"HIVE_MODELS_PATH="+filepath.Join(dir, "no-user-models.yaml"),
		"HIVE_FORAGERS_DIR="+foragersAbs,
		"HIVE_PROFILE=", "HIVE_PROVIDER=", "OPENAI_BASE_URL=",
		"HIVE_PROVIDER_ALLOWLIST=", "HIVE_HTTP_TIMEOUT=", "HIVE_MAX_OUTPUT_TOKENS=",
		"HIVE_BUDGET_MODE=", "HIVE_DB_PATH=", "HIVE_RPM_LOCAL=600000",
		"XDG_CONFIG_HOME="+dir,
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("chb ask: %v\n%s", err, out.String())
	}

	srv.mu.Lock()
	calls, bad := slices.Clone(srv.calls), slices.Clone(srv.bad)
	srv.mu.Unlock()
	if len(bad) > 0 {
		t.Fatalf("%v", bad)
	}
	roleOf := func(name string) string {
		switch {
		case name == "scope":
			return "scope"
		case strings.HasPrefix(name, "forager-"):
			return "lens"
		case name == "swarm-evaluate":
			return "evaluate"
		case name == "swarm-followup":
			return "followup"
		case name == "queen":
			return "queen"
		}
		return ""
	}
	seen := map[string]int{}
	for _, c := range calls {
		if c.name == "hive_constraint_probe" {
			continue
		}
		role := roleOf(c.name)
		if role == "" {
			t.Errorf("a call for %q, which no swarm role names", c.name)
			continue
		}
		seen[role]++
		want := routes[role]
		if c.model != want.Model || c.effort != want.Reasoning || c.sentEffort != (want.Reasoning != "") {
			t.Errorf("%s (%s) sent model %q reasoning %q (sent %v), want %q and %q", c.name, role, c.model, c.effort, c.sentEffort, want.Model, want.Reasoning)
		}
		if c.tools {
			t.Errorf("%s offered tools", c.name)
		}
	}
	for role, want := range map[string]int{"scope": 1, "lens": len(lenses), "evaluate": 1, "followup": 1, "queen": 1} {
		if seen[role] != want {
			t.Errorf("%d %s call(s), want %d", seen[role], role, want)
		}
	}

	log := out.String()
	for _, role := range []string{"scope", "lens", "evaluate", "followup", "queen"} {
		want := "routing: role " + role + " → " + routes[role].Model + " on local at " + ts.URL + "/v1, reasoning " + routes[role].Reasoning
		if !strings.Contains(log, want) {
			t.Errorf("the run log lacks %q", want)
		}
	}
	if !strings.Contains(log, "routing: off this machine: nothing") {
		t.Errorf("the run log does not say nothing leaves the machine:\n%s", log)
	}

	s, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.ReadDB.Query(`SELECT node_name, COALESCE(resolved_model, ''), COALESCE(provider, '') FROM workflow_node_states WHERE provider IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var node, model, provider string
		if err := rows.Scan(&node, &model, &provider); err != nil {
			t.Fatal(err)
		}
		n++
		if want := routes[roleOf(node)].Model; model != want || provider != "local" {
			t.Errorf("row %s: %s on %s, want %s on local", node, model, provider, want)
		}
	}
	if want := len(lenses) + 4; n != want {
		t.Errorf("%d node rows with a provider, want %d", n, want)
	}
}

// chb preflight --profile prints each route, and fails a workflow whose node
// would leave the machine under a profile that keeps every role on it, as
// agent-run refuses it. What it prints is computed from the profile.
func TestPreflightProfile_RoutingAndRefusal(t *testing.T) {
	const profile = "local-fast"
	p, err := runner.ResolveProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.CloudRoles()) > 0 {
		t.Skipf("the models config in use gives %s cloud roles", profile)
	}
	t.Setenv("HIVE_PROFILE", "")
	t.Setenv("HIVE_PROVIDER", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_LOCAL_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	run := func(t *testing.T, src string) string {
		t.Helper()
		wf := filepath.Join(t.TempDir(), "wf.yaml")
		if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := newPreflightCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{wf, "--profile", profile})
		_ = cmd.Execute()
		return out.String()
	}
	const lensNode = "name: t\nnodes:\n  a:\n    type: agent\n    role: lens\n    tier: synthesist\n    prompt: verify it\n    accept: [\"outputs.ok != ''\"]\n    outputs: [ok]\n"
	lr := p.Routes["lens"]
	out := run(t, lensNode+"edges: []\n")
	want := "✓ routing — role lens → " + lr.Model + " on local at http://127.0.0.1:1/v1, reasoning " + lr.Reasoning + " [a]"
	if !strings.Contains(out, want) {
		t.Errorf("preflight lacks %q:\n%s", want, out)
	}
	if strings.Contains(out, "routing stays on this machine") {
		t.Errorf("preflight refused an all-local run:\n%s", out)
	}
	out = run(t, lensNode+"  b:\n    type: agent\n    provider: anthropic\n    model: opus\n    prompt: verify it\n    accept: [\"outputs.ok != ''\"]\n    outputs: [ok]\nedges: []\n")
	if !strings.Contains(out, "✗ routing stays on this machine") || !strings.Contains(out, "[b]") {
		t.Errorf("preflight did not refuse node b's call off the machine:\n%s", out)
	}
}

// requestCounter is a server that counts every request it gets and answers
// GET /v1/models with the models it serves.
type requestCounter struct {
	served []string
	mu     sync.Mutex
	paths  []string
}

func (c *requestCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.paths = append(c.paths, r.Method+" "+r.URL.Path)
	c.mu.Unlock()
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		data := []map[string]string{}
		for _, m := range c.served {
			data = append(data, map[string]string{"id": m, "object": "model"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
		return
	}
	http.NotFound(w, r)
}

// chb preflight --profile asks no endpoint anything once the routing is
// refused, the local one included; with the routing accepted it lists the
// local endpoint's models, so the first half is not vacuous.
func TestPreflightProfile_RefusedRoutingAsksNoEndpoint(t *testing.T) {
	const profile = "local-fast"
	p, err := runner.ResolveProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	local := &requestCounter{served: []string{p.Routes["lens"].Model}}
	srv := httptest.NewServer(local)
	defer srv.Close()
	t.Setenv("HIVE_PROFILE", "")
	t.Setenv("HIVE_PROVIDER", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_LOCAL_BASE_URL", srv.URL+"/v1")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	run := func(t *testing.T, src string) string {
		t.Helper()
		wf := filepath.Join(t.TempDir(), "wf.yaml")
		if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := newPreflightCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{wf, "--profile", profile})
		_ = cmd.Execute()
		return out.String()
	}
	const lensNode = "name: t\nnodes:\n  a:\n    type: agent\n    role: lens\n    prompt: verify it\n    accept: [\"outputs.ok != ''\"]\n    outputs: [ok]\n"
	out := run(t, lensNode+"  b:\n    type: agent\n    provider: anthropic\n    model: opus\n    prompt: verify it\n    accept: [\"outputs.ok != ''\"]\n    outputs: [ok]\nedges: []\n")
	if !strings.Contains(out, "✗ routing stays on this machine") || !strings.Contains(out, "⚠ endpoint models — not asked: the routing profile refuses a call off this machine") {
		t.Errorf("preflight did not refuse the routing and skip the endpoints:\n%s", out)
	}
	local.mu.Lock()
	asked := slices.Clone(local.paths)
	local.mu.Unlock()
	if len(asked) != 0 {
		t.Errorf("the refused run still asked the local endpoint %v", asked)
	}
	out = run(t, lensNode+"edges: []\n")
	local.mu.Lock()
	asked = slices.Clone(local.paths)
	local.mu.Unlock()
	if !slices.Contains(asked, "GET /v1/models") || strings.Contains(out, "routing stays on this machine") {
		t.Errorf("the accepted run asked %v, want the model listing:\n%s", asked, out)
	}
}

// chb preflight prints the routing without a profile too: each route, and
// ⚠ for one that leaves the machine.
func TestPreflight_RoutingWithoutAProfile(t *testing.T) {
	t.Setenv("HIVE_PROFILE", "")
	t.Setenv("HIVE_PROVIDER", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_LOCAL_BASE_URL", "http://127.0.0.1:1/v1")
	wf := filepath.Join(t.TempDir(), "wf.yaml")
	src := "name: t\nnodes:\n  here:\n    type: agent\n    provider: local\n    model: m\n    prompt: verify it\n    accept: [\"outputs.ok != ''\"]\n    outputs: [ok]\n" +
		"  there:\n    type: agent\n    provider: claude-cli\n    model: opus\n    prompt: verify it\n    accept: [\"outputs.ok != ''\"]\n    outputs: [ok]\nedges: []\n"
	if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := newPreflightCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{wf, "--skip-provider-check"})
	_ = cmd.Execute()
	here := runner.RouteLine{Nodes: []string{"here"}, Provider: runner.BackendLocal, Model: "m", Endpoint: "http://127.0.0.1:1/v1"}
	there := runner.RouteLine{Nodes: []string{"there"}, Provider: runner.BackendClaudeCLI, Model: "opus"}
	for _, want := range []string{
		"✓ routing — " + here.String(),
		"⚠ routing — off this machine: " + there.String() + ": the claude-cli backend's CLI chooses its own host",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("preflight lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "✗ routing") {
		t.Errorf("preflight refused a routing with no profile:\n%s", out.String())
	}
}

// chb preflight --profile --skip-provider-check on each workflow the binary
// carries resolves every model node, and every repair that names a model of
// its own, to a model the profile names on provider local, and refuses
// nothing in the routing.
func TestPreflightProfile_CarriedWorkflowsRouteToTheProfile(t *testing.T) {
	t.Setenv("HIVE_PROFILE", "")
	t.Setenv("HIVE_PROVIDER", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_LOCAL_BASE_URL", "")
	carried, err := fs.Glob(hive.Workflows, "*.yaml")
	if err != nil || len(carried) == 0 {
		t.Fatalf("carried workflows %v (%v)", carried, err)
	}
	route := regexp.MustCompile(`(?m)^  . routing — (?:role \S+|no role) → (.+?) on (\S+?)(?: at \S+)?, reasoning `)
	for name, names := range shippedProfileModels(t) {
		for _, f := range carried {
			t.Run(name+"/"+f, func(t *testing.T) {
				cmd := newPreflightCmd()
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				cmd.SetArgs([]string{"workflows/" + f, "--profile", name, "--skip-provider-check"})
				_ = cmd.Execute()
				lines := route.FindAllStringSubmatch(out.String(), -1)
				if len(lines) == 0 {
					t.Fatalf("preflight printed no route:\n%s", out.String())
				}
				for _, l := range lines {
					if l[2] != string(runner.BackendLocal) || !names[l[1]] {
						t.Errorf("%q: want a model %s names, on provider local", l[0], name)
					}
				}
				if strings.Contains(out.String(), "✗ routing") {
					t.Errorf("preflight refused the routing:\n%s", out.String())
				}
			})
		}
	}
}

// userModels writes a models config holding the given profiles text and
// returns its path.
func userModels(t *testing.T, profiles string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte("profiles:\n"+profiles), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// runChb runs this test binary as chb (TestMain, runAsChbEnv) in dir with
// the models config at models and the extra environment, and returns its
// output and error.
func runChb(t *testing.T, dir, models string, env []string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{runAsChbEnv + "=1", "HIVE_MODELS_PATH=" + models,
		"HIVE_PROFILE=", "HIVE_PROVIDER=", "HIVE_PROVIDER_ALLOWLIST=",
		"HIVE_HTTP_TIMEOUT=", "HIVE_BUDGET_MODE=", "HIVE_DB_PATH=", "HIVE_DISABLE_RATE_LIMIT=1"}, env...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// A profile that routes the lens to this machine and Queen to openai sends
// each server only its own role's calls in one run, and each node row
// records the provider and endpoint that served it.
func TestAgentRunProfile_EachRoleOnItsEndpoint(t *testing.T) {
	const lensModel, queenModel = "lens-model", "queen-model"
	models := userModels(t, `  split:
    quality: test
    provider: local
    roles:
      lens: {model: `+lensModel+`, reasoning: none, tools: []}
      queen: {model: `+queenModel+`, provider: openai, tools: [], because: "the test's evidence"}
`)
	localSrv, cloudSrv := &schemaServer{served: []string{lensModel}}, &schemaServer{served: []string{queenModel}}
	lts, cts := httptest.NewServer(localSrv), httptest.NewServer(cloudSrv)
	defer lts.Close()
	defer cts.Close()
	dir := t.TempDir()
	const schema = "    outputs: [answer]\n    output_schema: {type: object, required: [answer], properties: {answer: {type: string}}}\n"
	wf := filepath.Join(dir, "wf.yaml")
	src := "name: split\nnodes:\n  lens-a:\n    type: agent\n    role: lens\n    tier: worker\n    prompt: go\n" + schema +
		"  queen:\n    type: agent\n    role: queen\n    tier: synthesist\n    prompt: go\n" + schema +
		"edges:\n  - {from: lens-a, to: queen}\n"
	if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "hive.db")
	out, err := runChb(t, dir, models, []string{"HIVE_LOCAL_BASE_URL=" + lts.URL + "/v1", "OPENAI_BASE_URL=" + cts.URL + "/v1", "OPENAI_API_KEY=sk-test"},
		"--db", dbPath, "agent-run", wf, "--profile", "split")
	if err != nil {
		t.Fatalf("agent-run: %v\n%s", err, out)
	}
	for _, c := range []struct {
		srv         *schemaServer
		node, model string
	}{{localSrv, "lens-a", lensModel}, {cloudSrv, "queen", queenModel}} {
		c.srv.mu.Lock()
		calls, bad := slices.Clone(c.srv.calls), slices.Clone(c.srv.bad)
		c.srv.mu.Unlock()
		if len(bad) > 0 {
			t.Errorf("%v", bad)
		}
		n := 0
		for _, call := range calls {
			if call.model != c.model {
				t.Errorf("the %s endpoint was sent %s (%s)", c.node, call.model, call.name)
			}
			if call.name != "hive_constraint_probe" {
				n++
				if call.name != c.node {
					t.Errorf("the %s endpoint served %s", c.node, call.name)
				}
			}
		}
		if n != 1 {
			t.Errorf("the %s endpoint got %d node calls, want 1", c.node, n)
		}
	}
	for _, want := range []string{
		"routing: role lens → " + lensModel + " on local at " + lts.URL + "/v1, reasoning none [lens-a]",
		"routing: role queen → " + queenModel + " on openai at " + cts.URL + "/v1, reasoning unset [queen]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the run log lacks %q:\n%s", want, out)
		}
	}
	s, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for node, want := range map[string][2]string{"lens-a": {"local", lts.URL + "/v1"}, "queen": {"openai", cts.URL + "/v1"}} {
		var provider, base string
		if err := s.ReadDB.QueryRow(`SELECT COALESCE(provider, ''), COALESCE(base_url, '') FROM workflow_node_states WHERE node_name = ?`, node).Scan(&provider, &base); err != nil {
			t.Fatal(err)
		}
		if provider != want[0] || base != want[1] {
			t.Errorf("%s recorded %s at %s, want %s at %s", node, provider, base, want[0], want[1])
		}
	}
}

// chb ask refuses --lens-tools under a profile whose lens or followup route
// sets the tools, and takes it under one whose routes leave them.
func TestAskProfileLensToolsConflict(t *testing.T) {
	foragersAbs, err := filepath.Abs(filepath.Join("..", "..", "foragers"))
	if err != nil {
		t.Fatal(err)
	}
	models := userModels(t, `  lens-tools:
    provider: local
    roles:
      lens: {model: m, tools: [read_file]}
  followup-tools:
    provider: local
    roles:
      followup: {model: m, tools: [read_file]}
  no-tools:
    provider: local
    roles:
      lens: {model: m}
`)
	for profile, role := range map[string]string{"lens-tools": "lens", "followup-tools": "followup", "no-tools": ""} {
		t.Run(profile, func(t *testing.T) {
			dir := t.TempDir()
			out, err := runChb(t, dir, models, []string{"HIVE_FORAGERS_DIR=" + foragersAbs},
				"ask", "q", "--no-dispatch", "--out", "s.yaml", "--profile", profile, "--lens-tools", "read")
			want := "--lens-tools cannot be combined with routing profile " + profile + ", whose " + role + " route sets the tools"
			if role == "" {
				if err != nil {
					t.Fatalf("refused: %v\n%s", err, out)
				}
				return
			}
			if err == nil || !strings.Contains(out, want) {
				t.Fatalf("err = %v, want %q:\n%s", err, want, out)
			}
		})
	}
}

// chb ask refuses, under a profile, the flags it would replace.
func TestAskProfileConflicts(t *testing.T) {
	foragersAbs, err := filepath.Abs("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_FORAGERS_DIR", foragersAbs)
	t.Setenv("HIVE_PROFILE", "")
	t.Chdir(t.TempDir())
	const profile = "local-fast"
	p, err := runner.ResolveProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	lensToolsRefused := p.Routes["lens"].Tools != nil || p.Routes["followup"].Tools != nil
	cases := []struct {
		args    []string
		refused bool
	}{
		{nil, false},
		{[]string{"--model", "m"}, true},
		{[]string{"--synthesizer-model", "m"}, true},
		{[]string{"--forager-tier", "worker"}, true},
		{[]string{"--synthesizer-tier", "worker"}, true},
		{[]string{"--forager-reasoning", "none"}, true},
		{[]string{"--synthesizer-reasoning", "none"}, true},
		{[]string{"--lens-tools", "read"}, lensToolsRefused},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			root := &cobra.Command{Use: "chb", SilenceUsage: true, SilenceErrors: true}
			root.AddCommand(newSwarmAskCmd())
			var stderr bytes.Buffer
			root.SetErr(&stderr)
			root.SetOut(&bytes.Buffer{})
			root.SetArgs(append([]string{"ask", "q", "--no-dispatch", "--out", "s.yaml", "--profile", profile}, c.args...))
			err := root.Execute()
			if (err != nil) != c.refused {
				t.Fatalf("err = %v, want refused = %v", err, c.refused)
			}
			if err == nil && !strings.Contains(stderr.String(), shellJoin([]string{"--profile", profile})) {
				t.Errorf("the printed commands do not carry the profile:\n%s", stderr.String())
			}
		})
	}
}
