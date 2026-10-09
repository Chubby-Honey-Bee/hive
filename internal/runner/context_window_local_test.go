package runner

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A node on provider local is held to the window its own server reports,
// not the window of the server at OPENAI_BASE_URL: the same model name,
// sent the same prompt, is refused at a local Ollama that runs it at 512
// tokens and answered at one that runs it at a window the prompt fits.
// Each endpoint loads the model to report its window, and the run log names
// each window at its endpoint.
func TestContextGuard_LocalProviderHasItsOwnWindow(t *testing.T) {
	onlyTheServersWindow(t)
	const model, small = "tiny:1b", 512
	prompt := longPrompt(4000)
	est := estimateOf(loneUserPrompt(prompt), 1)
	if est < small {
		t.Fatalf("the test prompt is estimated at %d tokens, inside the %d window", est, small)
	}
	large := est + 4*minReplyRoom
	reply := func(map[string]any) string { return chatReply(`{"answer":"x"}`, "stop", len(prompt)/5, 1) }
	localFake := &fakeOllama{window: small, modelID: model, reply: reply}
	cloudFake := &fakeOllama{window: large, modelID: model, reply: reply}
	localSrv, cloudSrv := httptest.NewServer(localFake), httptest.NewServer(cloudFake)
	defer localSrv.Close()
	defer cloudSrv.Close()
	t.Setenv("HIVE_LOCAL_BASE_URL", localSrv.URL+"/v1")
	node := func(name, provider string) string {
		return fmt.Sprintf("  %s:\n    type: agent\n    provider: %s\n    model: %s\n    tools: []\n    prompt: %q\n    outputs: [answer]\n", name, provider, model, prompt)
	}
	yaml := "name: guard\nnodes:\n" + node("on-local", "local") + node("on-openai", "openai") + "edges: []\n"
	_, store, log, _ := localRun(t, cloudSrv, yaml, Config{})

	local := readLocalNodeRow(t, store, "on-local")
	if want := fmt.Sprintf("is %d tokens (%s)", small, windowFromOllama); local.status != "failed" || !strings.Contains(local.errMsg, want) {
		t.Errorf("on-local is %s with %q, want failed naming %q", local.status, local.errMsg, want)
	}
	if n := len(localFake.sent()); n != 0 {
		t.Errorf("%d chat requests reached the local server, want none", n)
	}
	if cloud := readLocalNodeRow(t, store, "on-openai"); cloud.status != "completed" {
		t.Errorf("on-openai is %s (%q), want completed inside its %d-token window", cloud.status, cloud.errMsg, large)
	}
	if n := len(cloudFake.sent()); n != 1 {
		t.Errorf("%d chat requests reached the OPENAI_BASE_URL server, want 1", n)
	}
	for _, c := range []struct {
		srv    *httptest.Server
		fake   *fakeOllama
		window int64
	}{{localSrv, localFake, small}, {cloudSrv, cloudFake, large}} {
		if want := fmt.Sprintf("context window: %s at %s/v1 holds %d tokens (%s)", model, c.srv.URL, c.window, windowFromOllama); !strings.Contains(log, want) {
			t.Errorf("the run log lacks %q:\n%s", want, log)
		}
		if n := c.fake.loads.Load(); n != 1 {
			t.Errorf("%s was asked to load %s %d times, want once, to read its own window", c.srv.URL, model, n)
		}
	}
}

// The guard's calibration is the run's, not a backend's. A node that names
// its provider, as every node a routing profile routes does, builds its own
// backend, and so does each repair; a count the server gave the first such
// node still raises the estimate of the next on that model and endpoint. The
// first node's prompt is counted denser than its estimate, so the second
// node's cap is the window less its estimate raised by that count.
func TestContextGuard_CalibrationCarriesAcrossNodes(t *testing.T) {
	const name = "local-fast"
	p, err := ResolveProfile(name)
	if err != nil {
		t.Fatal(err)
	}
	const window = 16384
	first, second := longPrompt(6000), longPrompt(9000)
	est1, est2 := loneUserPrompt(first), loneUserPrompt(second)
	counted := int(math.Ceil(est1 * 1.2))
	scale := float64(counted) / est1 / calibrationMargin
	for _, c := range []struct {
		name, model, profile, provider, keys string
	}{
		{"under a routing profile", p.Routes["evaluate"].Model, name, "", "role: evaluate"},
		{"on nodes that name provider local", "m:8b", "", "local", "provider: local\n    model: m:8b\n    tools: []"},
	} {
		t.Run(c.name, func(t *testing.T) {
			onlyTheServersWindow(t)
			f := &fakeOllama{window: window, modelID: c.model, reply: func(map[string]any) string {
				return chatReply(`{"answer":"x"}`, "stop", counted, 5)
			}}
			f.loaded.Store(true)
			srv := httptest.NewServer(f)
			defer srv.Close()
			t.Setenv("HIVE_LOCAL_BASE_URL", srv.URL+"/v1")
			t.Setenv("OPENAI_BASE_URL", "")
			t.Setenv("HIVE_PROFILE", "")
			t.Setenv("HIVE_PROVIDER", "")
			t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
			t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
			node := func(name, prompt string) string {
				return fmt.Sprintf("  %s:\n    type: agent\n    %s\n    prompt: %q\n    outputs: [answer]\n", name, c.keys, prompt)
			}
			dir := t.TempDir()
			wf := filepath.Join(dir, "wf.yaml")
			if err := os.WriteFile(wf, []byte("name: guard\nnodes:\n"+node("one", first)+node("two", second)+"edges:\n  - {from: one, to: two}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			cfg := Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 10, MaxOutputTokens: window, Profile: c.profile, Provider: c.provider, Log: &log}
			if _, err := Run(context.Background(), newTempStore(t), cfg); err != nil {
				t.Fatalf("run: %v\n%s", err, log.String())
			}
			chats := f.sent()
			if len(chats) != 2 {
				t.Fatalf("%d chat requests, want 2\n%s", len(chats), log.String())
			}
			wants := []int64{window - estimateOf(est1, 1), window - estimateOf(est2, scale)}
			for i, want := range wants {
				if chats[i]["model"] != c.model {
					t.Errorf("call %d went to %v, want %s", i+1, chats[i]["model"], c.model)
				}
				if got, _ := chats[i]["max_tokens"].(float64); int64(got) != want {
					t.Errorf("call %d: max_tokens %v, want %d", i+1, chats[i]["max_tokens"], want)
				}
			}
		})
	}
}

// A count raises later estimates for its model at its own endpoint only:
// the same model name at another endpoint may be served by another server.
func TestCalibration_PerEndpoint(t *testing.T) {
	const at, other, model = "http://127.0.0.1:1/v1", "http://127.0.0.1:2/v1", "m:8b"
	c := &calibration{}
	size := promptSize{bytes: minCalibrationBytes, tokens: 1000}
	const counted = 1200
	c.observe(at, model, size, counted, true)
	want := float64(counted) / size.tokens / calibrationMargin
	if s, _, _ := c.scale(at, model); s != want {
		t.Errorf("scale at %s = %v, want %v", at, s, want)
	}
	for _, k := range [][2]string{{other, model}, {at, "other:1b"}} {
		if s, _, basis := c.scale(k[0], k[1]); s != 1 || basis != "by text class" {
			t.Errorf("scale for %s at %s = %v (%s), want 1 by text class", k[1], k[0], s, basis)
		}
	}
	var none *calibration
	none.observe(at, model, size, counted, true)
	if s, _, _ := none.scale(at, model); s != 1 {
		t.Errorf("a nil calibration scales by %v, want 1: it keeps nothing", s)
	}
}
