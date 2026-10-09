package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// A node's tools: is validated against workflow.ToolNames, so that list has
// to be the registry's.
func TestToolNames_AreTheRegistrys(t *testing.T) {
	r := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	if got := r.Names(); !reflect.DeepEqual(got, workflow.ToolNames) {
		t.Fatalf("registry tools %v, workflow.ToolNames %v", got, workflow.ToolNames)
	}
}

// Restrict keeps exactly the named tools, schema and handler, a tool's alias
// naming the tool; an empty list keeps none and leaves no schemas at all, so
// no backend sends a tools field.
func TestToolRegistry_Restrict(t *testing.T) {
	for _, keep := range [][]string{{}, {"read_file", "glob", "grep"}, {"shell"}, {"bash"}} {
		r := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
		all := r.Names()
		r.Restrict(keep)
		if !r.Restricted() {
			t.Errorf("%v: Restricted() = false", keep)
		}
		kept := func(n string) bool {
			return slices.ContainsFunc(keep, func(k string) bool { return workflow.CanonicalTool(k) == n })
		}
		var want []string
		for _, n := range all {
			if kept(n) {
				want = append(want, n)
			}
		}
		if got := r.Names(); !slices.Equal(got, want) {
			t.Errorf("%v: Names() = %v, want %v", keep, got, want)
		}
		if len(keep) == 0 && r.Schemas != nil {
			t.Errorf("no tools kept, but Schemas = %v, want nil", r.Schemas)
		}
		for _, n := range all {
			_, has := r.Handlers[n]
			if has != kept(n) {
				t.Errorf("%v: handler %s present = %v", keep, n, has)
			}
		}
		if len(openaiToolsFromRegistry(r)) != len(want) {
			t.Errorf("%v: the OpenAI backend would send %d tools, want %d", keep, len(openaiToolsFromRegistry(r)), len(want))
		}
		if g := geminiToolsFromRegistry(r); (len(want) == 0) != (g == nil) {
			t.Errorf("%v: the Gemini backend would send %v", keep, g)
		}
	}
}

// The OpenAI-compatible backend sends a node with `tools: []` no tools
// array at all — what Ollama needs for a model without the tools
// capability — and a node with a list exactly those tools. A node with no
// tools: sends every tool, as before.
func TestOpenAIBackend_HonoursTheToolsAllowlist(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The model preflight lists the endpoint's models first.
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]string{{"id": "m", "object": "model"}}})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		msgs, _ := body["messages"].([]any)
		last, _ := msgs[len(msgs)-1].(map[string]any)
		prompt, _ := last["content"].(string)
		mu.Lock()
		bodies[prompt] = body
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"out":"ok"}`}}},
		})
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "test")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")

	lines := map[string]string{"absent": "", "none": "    tools: []\n", "read": "    tools: [read_file, glob, grep]\n"}
	src := "name: t\nnodes:\n"
	for name, line := range lines {
		src += "  " + name + ":\n    type: agent\n    model: m\n    prompt: node " + name + "\n    outputs: [out]\n" + line
	}
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), newTempStore(t), Config{
		WorkflowYAML: wf, ProjectDir: dir, Provider: "openai", Log: discardWriter{},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	full := NewToolRegistry(dir, nil, NewFSRecorder()).Names()
	want := map[string][]string{"absent": full, "none": nil, "read": {"read_file", "glob", "grep"}}
	for name, names := range want {
		body := bodies["node "+name]
		if body == nil {
			t.Fatalf("no request for node %s", name)
		}
		raw, has := body["tools"]
		if names == nil {
			if has {
				t.Errorf("node %s: request carries tools %v, want no tools field", name, raw)
			}
			continue
		}
		var got []string
		for _, tl := range raw.([]any) {
			fn := tl.(map[string]any)["function"].(map[string]any)
			got = append(got, fn["name"].(string))
		}
		if !reflect.DeepEqual(got, names) {
			t.Errorf("node %s: request tools %v, want %v", name, got, names)
		}
	}
}

// fakeClaudeArgv writes a stand-in `claude` that records its argv, one
// argument per line, and answers with a result envelope.
func fakeClaudeArgv(t *testing.T) (cli, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	cli = filepath.Join(dir, "claude")
	argvFile = filepath.Join(dir, "argv")
	body := "#!/bin/sh\ncat > /dev/null\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + argvFile + "'\n" +
		"printf '{\"result\":\"ok\",\"is_error\":false,\"num_turns\":1}\\n'\n"
	if err := os.WriteFile(cli, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return cli, argvFile
}

func flagValue(argv []string, flag string) (string, bool) {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

// The claude CLI gets a node's allowlist as its own tool flags: --tools
// offers only those built-ins ("" offers none), --disallowed-tools denies
// the rest, and --strict-mcp-config keeps the user's MCP servers out; the
// chb sidecar comes only with chb_db_write. A node without tools: is allowed
// every built-in and the sidecar's chb_db_write, and gets none of these
// flags.
func TestCLIBackend_HonoursTheToolsAllowlist(t *testing.T) {
	builtins := []string{"Read", "Write", "Edit", "Glob", "Grep", "Bash", "WebFetch"}
	mcp := filepath.Join(t.TempDir(), "chb-mcp")
	if err := os.WriteFile(mcp, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]*[]string{
		"absent":   nil,
		"none":     {},
		"read":     {"read_file", "glob", "grep"},
		"db write": {"chb_db_write"},
		"shell":    {"shell"},
		"bash":     {"bash"},
	}
	for name, tools := range cases {
		t.Run(name, func(t *testing.T) {
			cli, argvFile := fakeClaudeArgv(t)
			b := &CLIBackend{CLIPath: cli, MCPServerPath: mcp, ScratchDir: t.TempDir(), AllowedTools: builtins}
			reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
			if tools != nil {
				reg.Restrict(*tools)
			}
			if _, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(argvFile)
			if err != nil {
				t.Fatal(err)
			}
			argv := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")

			if tools == nil {
				allowed, _ := flagValue(argv, "--allowed-tools")
				if want := strings.Join(append(append([]string{}, builtins...), "mcp__chb__chb_db_write"), ","); allowed != want {
					t.Errorf("--allowed-tools %q, want %q", allowed, want)
				}
				for _, f := range []string{"--tools", "--disallowed-tools", "--strict-mcp-config"} {
					if slices.Contains(argv, f) {
						t.Errorf("unrestricted node passed %s: %q", f, argv)
					}
				}
				return
			}
			var offered, denied []string
			for _, bi := range builtins {
				if slices.ContainsFunc(*tools, func(n string) bool { return workflow.CanonicalTool(n) == cliToolRegistryName[bi] }) {
					offered = append(offered, bi)
				} else {
					denied = append(denied, bi)
				}
			}
			got, ok := flagValue(argv, "--tools")
			if !ok || got != strings.Join(offered, ",") {
				t.Errorf("--tools %q (passed %v), want %q", got, ok, strings.Join(offered, ","))
			}
			// The shell tool, by either name, is the CLI's Bash.
			keepsShell := slices.ContainsFunc(*tools, func(n string) bool { return workflow.CanonicalTool(n) == "shell" })
			if slices.Contains(strings.Split(got, ","), "Bash") != keepsShell {
				t.Errorf("tools: %v keeps the shell tool = %v, and --tools is %q", *tools, keepsShell, got)
			}
			if got, _ := flagValue(argv, "--disallowed-tools"); got != strings.Join(denied, ",") {
				t.Errorf("--disallowed-tools %q, want %q", got, strings.Join(denied, ","))
			}
			if !slices.Contains(argv, "--strict-mcp-config") {
				t.Error("no --strict-mcp-config")
			}
			dbWrite := slices.Contains(*tools, "chb_db_write")
			if slices.Contains(argv, "--mcp-config") != dbWrite {
				t.Errorf("--mcp-config passed = %v, want %v", !dbWrite, dbWrite)
			}
			wantAllowed := append([]string{}, offered...)
			if dbWrite {
				wantAllowed = append(wantAllowed, "mcp__chb__chb_db_write")
			}
			got, ok = flagValue(argv, "--allowed-tools")
			if len(wantAllowed) == 0 {
				if ok {
					t.Errorf("--allowed-tools %q, want none", got)
				}
			} else if got != strings.Join(wantAllowed, ",") {
				t.Errorf("--allowed-tools %q, want %q", got, strings.Join(wantAllowed, ","))
			}
		})
	}
}
