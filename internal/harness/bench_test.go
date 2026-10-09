package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Chubby-Honey-Bee/hive/internal/bench"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

// fakeModel is an OpenAI-compatible chat endpoint that answers every bench
// prompt by a scripted policy. It finds the item a lens or solo prompt is
// about by its question and pack; Queen's prompt carries no pack, so she
// takes the majority of the lens verdicts she is shown and repeats their
// key points.
type fakeModel struct {
	items  []bench.Item
	policy func(*bench.Item) (string, []string)
	// intrude makes the first lens call of every run write a file into the
	// run's tree before it answers.
	intrude bool
	// probe makes the first lens call of every run run this bash command
	// before it answers; probed keeps what the command printed.
	probe string
	// served are the models GET /models lists, as an Ollama or LM Studio
	// server lists the models it holds.
	served []string
	// lensTools is the case's lens_tools, which `chb ask --lens-tools`
	// gets: a lens is offered no tool unless it names one, so a model that
	// intrudes or probes needs "all".
	lensTools string
	// cutLens names a forager whose first call with each prompt is cut off
	// at the output cap (finish_reason length); cut counts those calls per
	// item ID.
	cutLens string
	// queenStatus, when set, answers every Queen call with this HTTP
	// status and queenRefusal, as a server that refuses her request does.
	queenStatus int
	// downAfter, when set, takes the server down once it has answered that
	// many runs' last calls (Queen's or the solo control's): it closes
	// listener, so a new connection is refused, and drops every request that
	// still arrives on an open one.
	downAfter int
	// downOnChat defers that to the next chat call: the next run passes
	// its model preflight, then loses the endpoint.
	downOnChat bool
	listener   net.Listener

	mu      sync.Mutex
	finals  int
	down    bool
	unknown int
	probed  []string
	cutSeen map[string]bool
	cut     map[string]int
	// efforts counts the calls by the role of their prompt (lens, queen or
	// solo) and the reasoning_effort they sent: "lens:none", or "lens:"
	// when they sent none.
	efforts map[string]int
}

const queenRefusal = "the fake server refuses this Queen request"

func (f *fakeModel) sent(role, effort string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.efforts == nil {
		f.efforts = map[string]int{}
	}
	f.efforts[role+":"+effort]++
}

func (f *fakeModel) effortCounts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.efforts)
}

// cutOff reports whether the fake cuts off this call of forager: its first
// call with prompt, when forager is cutLens. It counts the cut against the
// prompt's item.
func (f *fakeModel) cutOff(forager, prompt string) bool {
	if f.cutLens == "" || forager != f.cutLens {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cutSeen[prompt] {
		return false
	}
	if f.cutSeen == nil {
		f.cutSeen, f.cut = map[string]bool{}, map[string]int{}
	}
	f.cutSeen[prompt] = true
	if it := f.find(prompt); it != nil {
		f.cut[it.ID]++
	}
	return true
}

func (f *fakeModel) unmatched() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unknown
}

var (
	foragerKey   = regexp.MustCompile(`\{"forager":"(\w+)"`)
	shownVerdict = regexp.MustCompile(`verdict: (support|oppose|conditional|abstain)\b`)
	shownPoint   = regexp.MustCompile(`(?m)^\s*- (.+)$`)
)

// isDown reports whether the server has gone down (downAfter). With
// downOnChat, a chat call after the downAfter-th run takes it down.
func (f *fakeModel) isDown(chat bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if chat && f.downOnChat && f.downAfter > 0 && f.finals >= f.downAfter && !f.down {
		f.down = true
		_ = f.listener.Close()
	}
	return f.down
}

// answeredRun counts a run's last call, and takes the server down after
// the downAfter-th unless downOnChat defers it.
func (f *fakeModel) answeredRun() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finals++
	if f.downAfter > 0 && f.finals == f.downAfter && !f.downOnChat {
		f.down = true
		_ = f.listener.Close()
	}
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.isDown(strings.HasSuffix(r.URL.Path, "/chat/completions")) {
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		data := []map[string]string{}
		f.mu.Lock()
		served := slices.Clone(f.served)
		f.mu.Unlock()
		for _, id := range served {
			data = append(data, map[string]string{"id": id, "object": "model"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
		return
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		ResponseFormat struct {
			JSONSchema struct {
				Name string `json:"name"`
			} `json:"json_schema"`
		} `json:"response_format"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// The constraint probe chb sends before a run. This fake does not
	// constrain decoding, so it answers the probe's prose prompt in prose.
	if body.ResponseFormat.JSONSchema.Name == "hive_constraint_probe" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "It is sunny."}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		})
		return
	}
	var prompt string
	var toolResults []string
	for _, m := range body.Messages {
		switch m.Role {
		case "user":
			prompt += m.Content
		case "tool":
			toolResults = append(toolResults, m.Content)
		}
	}
	answeredTool := len(toolResults) > 0
	msg := map[string]any{"role": "assistant"}
	finish := "stop"
	last := false // the run's last call: Queen's or the solo control's
	switch {
	case foragerKey.MatchString(prompt) && f.cutOff(foragerKey.FindStringSubmatch(prompt)[1], prompt):
		msg["content"] = `{"forager":"` + f.cutLens + `","verdict":"sup`
		finish = "length"
	case foragerKey.MatchString(prompt):
		f.sent("lens", body.ReasoningEffort)
		if f.intrude && !answeredTool {
			msg["content"] = ""
			msg["tool_calls"] = []map[string]any{{"id": "call_1", "type": "function",
				"function": map[string]any{"name": "write_file", "arguments": `{"path":"foragers/intruder.md","content":"edited"}`}}}
			break
		}
		if f.probe != "" && !answeredTool {
			msg["content"] = ""
			msg["tool_calls"] = []map[string]any{{"id": "call_1", "type": "function",
				"function": map[string]any{"name": "bash", "arguments": mustJSON(map[string]string{"command": f.probe})}}}
			break
		}
		if f.probe != "" {
			f.mu.Lock()
			f.probed = append(f.probed, toolResults...)
			f.mu.Unlock()
		}
		v, tokens := f.answer(prompt)
		msg["content"] = mustJSON(map[string]any{
			"forager": foragerKey.FindStringSubmatch(prompt)[1], "verdict": v, "key_points": []string{keyPoint(tokens)},
			"evidence": []string{"the roster in the context"}, "uncertainties": []string{"none"}, "recommendation": "Answer from the roster.",
		})
	case strings.Contains(prompt, "Forager verdicts"):
		f.sent("queen", body.ReasoningEffort)
		if f.queenStatus != 0 {
			http.Error(w, `{"error":{"message":"`+queenRefusal+`"}}`, f.queenStatus)
			return
		}
		shown := prompt[strings.Index(prompt, "Forager verdicts"):]
		count := map[string]int{}
		for _, m := range shownVerdict.FindAllStringSubmatch(shown, -1) {
			count[m[1]]++
		}
		best := ""
		for v, n := range count {
			if best == "" || n > count[best] || n == count[best] && v < best {
				best = v
			}
		}
		// Each point opens its own line, so a token follows a newline, which
		// her JSON reply escapes as \n: graded from the raw reply, the token
		// would read as n<name>.
		var lines []string
		for _, m := range shownPoint.FindAllStringSubmatch(shown, -1) {
			lines = append(lines, m[1])
		}
		msg["content"] = mustJSON(map[string]any{"report": "## Swarm Verdict: bench\n\n" + strings.Join(lines, "\n"),
			"verdict": best, "convergence": "high", "recommendation": "Follow the lenses.", "coverage": 3, "gaps": []string{},
			"dissent_from_plurality": "The fake Queen takes the most common verdict she is shown."})
		last = true
	default:
		f.sent("solo", body.ReasoningEffort)
		v, tokens := f.answer(prompt)
		msg["content"] = mustJSON(map[string]any{"verdict": v, "key_points": []string{keyPoint(tokens)}, "recommendation": "Answer from the roster."})
		last = true
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"finish_reason": finish, "message": msg}},
		"usage":   map[string]any{"prompt_tokens": len(prompt) / 4, "completion_tokens": 10},
	})
	if last {
		f.answeredRun()
	}
}

// answer finds the prompt's item and answers by the policy.
func (f *fakeModel) answer(prompt string) (string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	match := f.find(prompt)
	if match == nil {
		f.unknown++
		return bench.Abstain, nil
	}
	return f.policy(match)
}

// find is the prompt's item: the longest pack it contains, since a twin's
// pack can extend the other's. The caller holds f.mu.
func (f *fakeModel) find(prompt string) *bench.Item {
	var match *bench.Item
	for i := range f.items {
		it := &f.items[i]
		if strings.Contains(prompt, it.Question) && strings.Contains(prompt, it.Context) && (match == nil || len(it.Context) > len(match.Context)) {
			match = it
		}
	}
	return match
}

func keyPoint(tokens []string) string {
	if len(tokens) == 0 {
		return "nothing to name"
	}
	return strings.Join(tokens, "; ")
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// buildChb compiles chb into dir. The harness runs it once per item and
// arm; a test binary built with -race would run each of those hundreds of
// runs several times slower, so chb is built without the race detector.
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

// benchHarness points a harness at a fake model and a one-case bench suite,
// running chb from the repository root.
func benchHarness(t *testing.T, chb string, model *fakeModel, arms []string, families []string, seeds []int64) (*agentHarness, string) {
	t.Helper()
	lensModel, queenModel := "fake-lens", "fake-queen"
	model.served = []string{lensModel, queenModel}
	srv := httptest.NewServer(model)
	t.Cleanup(srv.Close)
	model.listener = srv.Listener
	t.Chdir(filepath.Join("..", ".."))
	// The default database is under the working directory, the checkout
	// here; a test keeps its own.
	t.Setenv("HIVE_DB_PATH", filepath.Join(t.TempDir(), "hive.db"))
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake-key-for-the-test-server")
	t.Setenv("HIVE_RPM_OPENAI", "600000")
	t.Setenv("HIVE_FORAGERS_DIR", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")

	c := harnessCase{Name: "bench", Kind: "bench", Foragers: "minimal", Families: families, Seeds: seeds, Arms: arms, LensTools: model.lensTools}
	raw, err := yaml.Marshal(harnessSuite{Cases: []harnessCase{c}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	suite := filepath.Join(dir, "suite.yaml")
	if err := os.WriteFile(suite, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devnull.Close() })
	ws := filepath.Join(dir, "ws")
	return &agentHarness{Options: Options{
		Self: chb, Stdout: devnull, Suite: suite, Workspace: ws, BudgetMode: "cheap", Model: "haiku",
		KeepGoing: true, Provider: "openai", LensModel: lensModel, QueenModel: queenModel, Reps: 1, Config: "fake",
		Foragers: resolveForagers, LoadForagers: loadForagers,
	}}, filepath.Join(ws, "bench", "results.jsonl")
}

// resolveForagers and loadForagers stand in for chb's: the forager tree
// foragers.Resolve picks from the working directory, and its foragers.
func resolveForagers() foragers.Source { return foragers.Resolve("foragers") }

func loadForagers() ([]foragers.Forager, error) { return resolveForagers().Load() }

func liveRoster(t *testing.T) []foragers.Forager {
	t.Helper()
	all, err := foragers.Load(filepath.Join("..", "..", "foragers"))
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func liveItems(t *testing.T, families []string, seeds []int64) []bench.Item {
	t.Helper()
	items, err := bench.Generate(liveRoster(t), families, seeds)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func readResults(t *testing.T, path string) []bench.Row {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := bench.ReadRows(f)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func armRows(rows []bench.Row, arm string) []bench.Row {
	var out []bench.Row
	for _, r := range rows {
		if r.Arm == arm {
			out = append(out, r)
		}
	}
	return out
}

// The bench, end to end: the real harness drives chb ask and agent-run
// against an OpenAI-compatible server that answers by a scripted policy.
func TestBenchCase_AgainstAFakeModel(t *testing.T) {
	chb := buildChb(t, t.TempDir())
	t.Run("scripted policies score exactly", func(t *testing.T) { scriptedPolicies(t, chb) })
	t.Run("a coin passes a quarter of pairs", func(t *testing.T) { coinPolicy(t, chb) })
	t.Run("a tree write fails the case", func(t *testing.T) { treeWrite(t, chb) })
	t.Run("a lens cannot see the workspace", func(t *testing.T) { privateWorkingDir(t, chb) })
	t.Run("a lens cut off fails the finish guard", func(t *testing.T) { lensCutOff(t, chb) })
	t.Run("the shipped canary grades both twins", func(t *testing.T) { shippedCanary(t, chb) })
	t.Run("a mixed-family arm", func(t *testing.T) { mixedFamily(t, chb) })
	t.Run("a failed Queen", func(t *testing.T) { failedQueen(t, chb) })
}

// finishGuard is bench decide's "no output cut off" guard on rows, all of
// one configuration, taken as the rule's reference.
func finishGuard(t *testing.T, rows []bench.Row) bench.Check {
	t.Helper()
	rule := bench.DefaultRule()
	rule.Reference, rule.Negative = rows[0].Config, "negative control"
	for _, c := range bench.Decide(rows, rule).Configs {
		if c.Config != rule.Reference {
			continue
		}
		for _, g := range c.Selection.Guards {
			if g.Name == bench.GuardFinish {
				return g
			}
		}
	}
	t.Fatalf("bench decide gave %s no %q guard", rule.Reference, bench.GuardFinish)
	return bench.Check{}
}

// A lens call the server cuts off at the output cap reaches the bench: the
// run's row counts it in finish_length, and bench decide's "no output cut
// off" guard fails on those rows.
func lensCutOff(t *testing.T, chb string) {
	seeds := []int64{1}
	items := liveItems(t, []string{"F4"}, seeds)
	lens := foragers.Minimal(liveRoster(t))[0].Name
	model := &fakeModel{items: items, cutLens: lens, policy: func(it *bench.Item) (string, []string) { return it.Want.Verdict, it.Want.Tokens }}
	h, results := benchHarness(t, chb, model, []string{bench.ArmSwarm}, []string{"F4"}, seeds)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("harness: %v", err)
	}
	rows := readResults(t, results)
	if len(rows) != len(items) {
		t.Fatalf("%d rows, want %d", len(rows), len(items))
	}
	model.mu.Lock()
	cut := maps.Clone(model.cut)
	model.mu.Unlock()
	total := 0
	for _, r := range rows {
		want := cut[r.Item]
		if want == 0 {
			t.Errorf("%s: the server cut off no call of %s; the test needs one per run", r.Item, lens)
		}
		if r.FinishLength == nil {
			t.Errorf("%s: finish_length null, want %d, the calls the server cut off (%s)", r.Item, want, r.Error)
		} else if *r.FinishLength != want {
			t.Errorf("%s: finish_length %d, want %d, the calls the server cut off (%s)", r.Item, *r.FinishLength, want, r.Error)
		}
		total += want
	}
	if g := finishGuard(t, rows); g.Status != bench.Fail || int(g.Value) != total {
		t.Errorf("%s guard %s at %v, want %s at %d", bench.GuardFinish, g.Status, g.Value, bench.Fail, total)
	}
}

// The default harness's graded canary is the shipped suite's twinned case.
// Each of its pairs expects two different verdicts, so a model that always
// answers support passes no pair and fails the case; an oracle passes every
// pair and the case.
func shippedCanary(t *testing.T, chb string) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "agent-harness", "suite.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var suite harnessSuite
	if err := yaml.Unmarshal(raw, &suite); err != nil {
		t.Fatal(err)
	}
	var canary *harnessCase
	for i, c := range suite.Cases {
		if c.RequirePairs && !c.Slow {
			if canary != nil {
				t.Fatalf("two cases make harness runs require pairs: %s and %s", canary.Name, c.Name)
			}
			canary = &suite.Cases[i]
		}
		if c.Kind == "swarm" && c.Grade != "" {
			t.Errorf("swarm case %s carries a one-verdict grade", c.Name)
		}
	}
	if canary == nil || canary.Kind != "bench" || canary.Slow {
		t.Fatalf("canary %+v; want one bench case with require_pairs that make harness runs by default", canary)
	}
	items, err := benchItems(liveRoster(t), *canary)
	if err != nil {
		t.Fatal(err)
	}
	pairs := len(items) / 2
	for i := 0; i+1 < len(items); i += 2 {
		if items[i].Want.Verdict == items[i+1].Want.Verdict {
			t.Fatalf("pair %s expects %s on both twins", items[i].Pair, items[i].Want.Verdict)
		}
	}
	for _, p := range []struct {
		name   string
		passed int
		fn     func(*bench.Item) (string, []string)
	}{
		{"always-support", 0, func(*bench.Item) (string, []string) { return bench.Support, nil }},
		{"oracle", pairs, func(it *bench.Item) (string, []string) { return it.Want.Verdict, it.Want.Tokens }},
	} {
		t.Run(p.name, func(t *testing.T) {
			model := &fakeModel{items: items, policy: p.fn}
			h, _ := benchHarness(t, chb, model, nil, nil, nil)
			h.Suite, h.Only = filepath.Join("fixtures", "agent-harness", "suite.yaml"), canary.Name
			err := h.run(context.Background())
			if n := model.unmatched(); n > 0 {
				t.Fatalf("%d prompt(s) matched no item", n)
			}
			rows := readResults(t, filepath.Join(h.Workspace, canary.Name, "results.jsonl"))
			s := bench.Summarize(armRows(rows, bench.ArmSwarm))
			if s.Pairs != pairs || s.PairsPassed != p.passed || len(rows) != len(items) {
				t.Fatalf("%d rows, pairs passed %d/%d; want %d rows and %d/%d", len(rows), s.PairsPassed, s.Pairs, len(items), p.passed, pairs)
			}
			if (err == nil) != (p.passed == pairs) {
				t.Fatalf("harness error %v; the case must fail exactly when a pair fails", err)
			}
		})
	}
}

// A mixed-family arm: --lens-model a,b is rotated by the item's seed plus
// the repetition, and chb ask gives the minimal lenses, in name order, the
// rotated list's models in turn. Its unanimous runs are not low; one
// model's are. Every lens answers support, so on each oppose twin all
// C(L,2) lens pairs err together, and on each support twin none errs. The
// solo control runs on the first model. --lens-reasoning and
// --queen-reasoning reach every lens, Queen and solo call, and without
// them no call sends a level.
func mixedFamily(t *testing.T, chb string) {
	seeds, families := []int64{1}, []string{"F1"}
	items := liveItems(t, families, seeds)
	lens, _ := foragers.SplitByArchetype(foragers.Minimal(liveRoster(t)))
	var names []string
	for _, f := range lens {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	choose2 := func(n int) int { return n * (n - 1) / 2 }
	for _, c := range []struct {
		models    []string
		reasoning string
	}{{[]string{"fake-qwen", "fake-ministral"}, "none"}, {[]string{"fake-lens"}, ""}} {
		models := c.models
		t.Run(strings.Join(models, "+"), func(t *testing.T) {
			model := &fakeModel{items: items, policy: func(*bench.Item) (string, []string) { return bench.Support, nil }}
			h, results := benchHarness(t, chb, model, []string{bench.ArmSwarm, bench.ArmSolo}, families, seeds)
			h.LensModel, h.LensReasoning, h.QueenReasoning = strings.Join(models, ","), c.reasoning, c.reasoning
			model.served = append(slices.Clone(models), h.QueenModel)
			if err := h.run(context.Background()); err != nil {
				t.Fatalf("harness: %v", err)
			}
			rows := readResults(t, results)
			n := len(models)
			oppose, all, cross := 0, 0, 0
			for _, r := range rows {
				if r.Arm == bench.ArmSolo {
					if r.LensModel != models[0] {
						t.Errorf("%s solo: lens model %q, want the first, %s", r.Item, r.LensModel, models[0])
					}
					continue
				}
				k := int(r.Seed+int64(r.Rep-1)) % n
				var used []string
				perModel := map[string]int{}
				for i, name := range names {
					m := models[(i+k)%n]
					perModel[m]++
					if a := r.LensAnswers[name]; a.Model != m || a.Verdict != bench.Support {
						t.Errorf("%s: lens %s answered %+v, want support on %s", r.Item, name, a, m)
					}
				}
				for i := range models {
					used = append(used, models[(i+k)%n])
				}
				if r.LensModel != strings.Join(used, ",") {
					t.Errorf("%s: lens model %q, want the rotated list %v", r.Item, r.LensModel, used)
				}
				wantState := "not_low"
				if len(perModel) == 1 {
					wantState = "low"
				}
				if r.Diversity != wantState {
					t.Errorf("%s: diversity %q with lens models %v, want %s", r.Item, r.Diversity, perModel, wantState)
				}
				if r.Want == bench.Oppose {
					oppose++
					all += choose2(len(names))
					cross += choose2(len(names))
					for _, m := range perModel {
						cross -= choose2(m)
					}
				}
			}
			e := bench.CorrelatedErrors(armRows(rows, bench.ArmSwarm))
			if e.All.Pairs != all || e.All.Together != all || e.CrossModel.Pairs != cross || e.CrossModel.Together != cross || oppose == 0 {
				t.Errorf("lens errors %+v; want %d pairs all together, %d of them across models, over %d oppose twins", e, all, cross, oppose)
			}
			for role, calls := range model.effortCounts() {
				if !strings.HasSuffix(role, ":"+c.reasoning) || calls == 0 {
					t.Errorf("%d %s call(s); want every lens, Queen and solo call to send reasoning %q", calls, role, c.reasoning)
				}
			}
			raw, err := os.ReadFile(filepath.Join(h.Workspace, "REPORT.md"))
			report := string(raw)
			for _, line := range []string{"- correlated lens errors, over the", "- lens runs that returned a verdict, by model:", "- swarm runs by lens diversity"} {
				if err != nil || !strings.Contains(report, line) {
					t.Errorf("the report has no line %q: %v", line, err)
				}
			}
			if strings.Contains(report, "- correlated lens errors across models:") != (n > 1) {
				t.Errorf("the report's cross-model line is there %v, want %v with %d lens models", !(n > 1), n > 1, n)
			}
		})
	}
}

// A Queen the server refuses leaves no artifact and no verdict. The run's
// lens answers and diversity still come from its node rows, so it counts
// toward the correlated-error rate. The canary's failure names the run and
// the error that stopped it, apart from the answers graded wrong.
func failedQueen(t *testing.T, chb string) {
	seeds, families := []int64{1}, []string{"F1"}
	items := liveItems(t, families, seeds)
	lens, _ := foragers.SplitByArchetype(foragers.Minimal(liveRoster(t)))
	model := &fakeModel{items: items, queenStatus: http.StatusBadRequest,
		policy: func(it *bench.Item) (string, []string) { return it.Want.Verdict, it.Want.Tokens }}
	h, results := benchHarness(t, chb, model, []string{bench.ArmSwarm}, families, seeds)
	c := harnessCase{Name: "bench", Kind: "bench", Foragers: "minimal", Families: families, Seeds: seeds, Arms: []string{bench.ArmSwarm}, RequirePairs: true}
	raw, err := yaml.Marshal(harnessSuite{Cases: []harnessCase{c}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Suite, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.run(context.Background()); err == nil {
		t.Fatal("the canary passed with every Queen refused")
	}
	// No run completed, so the rows are marked as not a measurement.
	_, rows := markedRows(t, results)
	for _, r := range rows {
		queen := ""
		for _, n := range r.Nodes {
			if n.Node == "queen" {
				queen = n.Status
			}
		}
		if r.Completed || r.Got != "" || r.Error == "" || queen != "failed" {
			t.Errorf("%s: completed %v, got %q, error %q, queen %q; want no verdict, an error and a failed Queen", r.Item, r.Completed, r.Got, r.Error, queen)
		}
		if len(r.LensAnswers) != len(lens) {
			t.Errorf("%s: %d lens answers, want the %d minimal lenses", r.Item, len(r.LensAnswers), len(lens))
		}
		for name, a := range r.LensAnswers {
			if a.Verdict != r.Want || a.Model != h.LensModel {
				t.Errorf("%s: lens %s answered %+v, want %s on %s", r.Item, name, a, r.Want, h.LensModel)
			}
		}
		if r.Diversity != "low" {
			t.Errorf("%s: diversity %q; every lens gave one verdict on one model", r.Item, r.Diversity)
		}
	}
	if e := bench.CorrelatedErrors(rows); e.Runs != len(rows) || e.All.Compared != len(rows)*len(lens)*(len(lens)-1)/2 {
		t.Errorf("lens errors %+v; want all %d runs and every lens pair compared", e, len(rows))
	}

	var report struct {
		Results []harnessResult `json:"results"`
	}
	js, err := os.ReadFile(filepath.Join(h.Workspace, "report.json"))
	if err != nil || json.Unmarshal(js, &report) != nil || len(report.Results) != 1 {
		t.Fatalf("report.json: %v", err)
	}
	checks := map[string]harnessCheck{}
	for _, ck := range report.Results[0].Checks {
		checks[strings.SplitN(ck.Name, " (", 2)[0]] = ck
	}
	done, pairs := checks["every run completed"], checks["both twins of every pair answered right"]
	if done.OK || pairs.OK || done.Name != fmt.Sprintf("every run completed (0 of %d)", len(rows)) {
		t.Fatalf("checks %+v and %+v; want both failed, with 0 of %d runs completed", done, pairs, len(rows))
	}
	for _, r := range rows {
		if want := fmt.Sprintf("%s %s r%d: %s", r.Item, r.Arm, r.Rep, lastLine(r.Error)); !strings.Contains(done.Detail, want) {
			t.Errorf("completion detail %q does not name %q", done.Detail, want)
		}
	}
	if want := fmt.Sprintf("%d run(s) did not complete", len(rows)); pairs.Detail != want+", named above" {
		t.Errorf("pairs detail %q; want only %q, since no run gave a wrong answer", pairs.Detail, want)
	}
}

// A model that always gives one verdict passes no twin pair and an oracle
// passes every one, on both arms.
func scriptedPolicies(t *testing.T, chb string) {
	seeds := []int64{1}
	items := liveItems(t, nil, seeds)
	lenses := len(foragers.Minimal(liveRoster(t)))
	policies := []struct {
		name  string
		pairs float64
		fn    func(*bench.Item) (string, []string)
	}{
		{"always-support", 0, func(*bench.Item) (string, []string) { return bench.Support, nil }},
		{"always-oppose", 0, func(*bench.Item) (string, []string) { return bench.Oppose, nil }},
		{"oracle", 1, func(it *bench.Item) (string, []string) { return it.Want.Verdict, it.Want.Tokens }},
	}
	for _, p := range policies {
		t.Run(p.name, func(t *testing.T) {
			model := &fakeModel{items: items, policy: p.fn}
			h, results := benchHarness(t, chb, model, nil, nil, seeds)
			if err := h.run(context.Background()); err != nil {
				t.Fatalf("harness: %v", err)
			}
			if n := model.unmatched(); n > 0 {
				t.Fatalf("%d prompt(s) matched no item", n)
			}
			rows := readResults(t, results)
			if len(rows) != 2*len(items) {
				t.Fatalf("%d rows, want %d (every item on both arms)", len(rows), 2*len(items))
			}
			for _, arm := range []string{bench.ArmSwarm, bench.ArmSolo} {
				s := bench.Summarize(armRows(rows, arm))
				if s.Completed != s.Runs || s.Pairs != len(items)/2 || s.PairScore != p.pairs {
					t.Errorf("%s: completed %d/%d, pairs passed %d/%d; want all completed and pair score %.2f",
						arm, s.Completed, s.Runs, s.PairsPassed, s.Pairs, p.pairs)
				}
				if p.name == "oracle" && s.TokenF1 != 1 {
					t.Errorf("%s: the oracle named its tokens but token F1 is %.3f", arm, s.TokenF1)
				}
			}
			for _, r := range rows {
				if !r.TreeOK || r.TokensIn == 0 || len(r.Nodes) == 0 {
					t.Errorf("%s/%s: tree ok %v, tokens in %d, %d node(s); want an unchanged tree and per-node tokens",
						r.Item, r.Arm, r.TreeOK, r.TokensIn, len(r.Nodes))
				}
				if r.Arm == bench.ArmSwarm && (r.Lenses != lenses || r.FullVerdicts != r.Lenses || r.SchemaValid != r.Lenses) {
					t.Errorf("%s: lenses %d, full verdicts %d, schema-valid %d; want %d of each, the minimal preset (%s)", r.Item, r.Lenses, r.FullVerdicts, r.SchemaValid, lenses, r.Error)
				}
				if !r.Correct && p.pairs == 1 {
					t.Errorf("%s/%s: the oracle's answer was graded wrong: want %s %v, got %q %v (%s)", r.Item, r.Arm, r.Want, r.WantTokens, r.Got, r.GotTokens, r.Error)
				}
			}
		})
	}
}

// A coin between support and oppose passes a twin pair with probability
// 1/4, so over n pairs it passes a Binomial(n, 1/4) count of them.
func coinPolicy(t *testing.T, chb string) {
	var families []string
	for _, f := range bench.Families() {
		if f != "AB" {
			families = append(families, f)
		}
	}
	seeds := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	items := liveItems(t, families, seeds)
	rng := rand.New(rand.NewPCG(2026, 9))
	model := &fakeModel{items: items, policy: func(*bench.Item) (string, []string) {
		if rng.IntN(2) == 0 {
			return bench.Support, nil
		}
		return bench.Oppose, nil
	}}
	h, results := benchHarness(t, chb, model, []string{bench.ArmSolo}, families, seeds)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("harness: %v", err)
	}
	s := bench.Summarize(readResults(t, results))
	lo, hi := binomialBounds(s.Pairs, 0.25, 0.0005)
	if s.Pairs != len(items)/2 || s.PairsPassed < lo || s.PairsPassed > hi {
		t.Fatalf("coin passed %d/%d pairs; the 99.9%% binomial interval around 1/4 is [%d, %d]", s.PairsPassed, s.Pairs, lo, hi)
	}
}

// binomialBounds returns the largest lo with P(X < lo) at most tail and the
// smallest hi with P(X > hi) at most tail, for X ~ Binomial(n, p).
func binomialBounds(n int, p, tail float64) (lo, hi int) {
	pmf := func(k int) float64 {
		a, _ := math.Lgamma(float64(n + 1))
		b, _ := math.Lgamma(float64(k + 1))
		c, _ := math.Lgamma(float64(n - k + 1))
		return math.Exp(a - b - c + float64(k)*math.Log(p) + float64(n-k)*math.Log(1-p))
	}
	for cum := 0.0; lo <= n && cum+pmf(lo) <= tail; lo++ {
		cum += pmf(lo)
	}
	hi = n
	for cum := 0.0; hi >= 0 && cum+pmf(hi) <= tail; hi-- {
		cum += pmf(hi)
	}
	return lo, hi
}

// A run that writes into its tree fails the case, and the write never
// reaches the checkout.
func treeWrite(t *testing.T, chb string) {
	seeds := []int64{1}
	items := liveItems(t, []string{"F4"}, seeds)
	model := &fakeModel{items: items, intrude: true, lensTools: "all", policy: func(it *bench.Item) (string, []string) { return it.Want.Verdict, it.Want.Tokens }}
	h, results := benchHarness(t, chb, model, []string{bench.ArmSwarm}, []string{"F4"}, seeds)
	err := h.run(context.Background())
	if err == nil {
		t.Fatal("the harness passed a run that edited its tree")
	}
	for _, r := range readResults(t, results) {
		if r.TreeOK || !strings.Contains(r.Error, "+foragers/intruder.md") {
			t.Errorf("%s: tree ok %v, error %q; want the write to foragers/intruder.md named", r.Item, r.TreeOK, r.Error)
		}
	}
	if _, err := os.Stat(filepath.Join("foragers", "intruder.md")); err == nil {
		t.Fatal("the write reached the checkout's foragers/")
	}
	report, rerr := os.ReadFile(filepath.Join(h.Workspace, "REPORT.md"))
	if rerr != nil || !strings.Contains(string(report), "every run left its private tree unchanged") {
		t.Fatalf("the report does not name the tree check: %v", rerr)
	}
}

// The shipped suite and rule can reach accept. The confirmation case draws
// its items after the selection seeds', so none repeats a selection item;
// runs that answer them the way their policies say are decided on both
// splits, and the candidate is accepted, not voided for overlap.
func TestBenchSuite_ConfirmationItemsAreFresh(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "bench", "suite.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var suite harnessSuite
	if err := yaml.Unmarshal(raw, &suite); err != nil {
		t.Fatal(err)
	}
	rule, err := bench.LoadRule(filepath.Join("..", "..", "fixtures", "bench", "rule.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]harnessCase{}
	for _, c := range suite.Cases {
		cases[c.Name] = c
	}
	sel, conf := cases["bench-twins-selection"], cases["bench-twins-confirmation"]
	if !reflect.DeepEqual(sel.Seeds, rule.SelectionSeeds) || !reflect.DeepEqual(conf.Seeds, rule.ConfirmationSeeds) {
		t.Fatalf("suite seeds %v / %v; the rule's are %v / %v", sel.Seeds, conf.Seeds, rule.SelectionSeeds, rule.ConfirmationSeeds)
	}
	if len(sel.ExcludeSeeds) > 0 || !reflect.DeepEqual(conf.ExcludeSeeds, sel.Seeds) {
		t.Fatalf("exclude_seeds %v / %v; confirmation must exclude exactly the selection seeds %v, and selection nothing",
			sel.ExcludeSeeds, conf.ExcludeSeeds, sel.Seeds)
	}
	all := liveRoster(t)
	selItems, err := benchItems(all, sel)
	if err != nil {
		t.Fatal(err)
	}
	confItems, err := benchItems(all, conf)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, it := range selItems {
		seen[it.Hash()] = it.ID
	}
	for _, it := range confItems {
		if id := seen[it.Hash()]; id != "" {
			t.Errorf("confirmation item %s repeats selection item %s", it.ID, id)
		}
	}

	var candidate string
	for name, spec := range rule.Configs {
		if len(rule.Classes) > 0 && spec.MemoryGB <= rule.Classes[0].MemoryGB && (candidate == "" || name < candidate) {
			candidate = name
		}
	}
	if candidate == "" {
		t.Fatal("the rule lists no configuration its first machine class can hold")
	}
	oracle := func(it bench.Item) (string, string) { return it.Want.Verdict, strings.Join(it.Want.Tokens, " ") }
	always := func(v string) func(bench.Item) (string, string) {
		return func(bench.Item) (string, string) { return v, "" }
	}
	rows := func(config, arm string, items []bench.Item, answer func(bench.Item) (string, string)) []bench.Row {
		var out []bench.Row
		zero := 0
		for _, it := range items {
			r := bench.NewRow(it, arm, 1)
			r.Config = config
			v, text := answer(it)
			r.Got = v
			r.Correct, r.GotTokens, r.TokenF1 = bench.Grade(it, v, text)
			r.Completed, r.TreeOK, r.FinishLength, r.WallSeconds = true, true, &zero, 60
			if arm == bench.ArmSwarm {
				r.Lenses, r.FullVerdicts, r.Outputs, r.SchemaValid = 7, 7, 7, 7
			} else {
				r.Outputs, r.SchemaValid = 1, 1
			}
			out = append(out, r)
		}
		return out
	}
	var runs []bench.Row
	for _, part := range [][]bench.Row{
		rows(rule.Reference, bench.ArmSwarm, selItems, oracle),
		rows(rule.Negative, bench.ArmSwarm, selItems, always(bench.Support)),
		rows(candidate, bench.ArmSwarm, selItems, oracle),
		rows(candidate, bench.ArmSolo, selItems, always(bench.Support)),
		rows(rule.Reference, bench.ArmSwarm, confItems, oracle),
		rows(candidate, bench.ArmSwarm, confItems, oracle),
	} {
		runs = append(runs, part...)
	}
	d := bench.Decide(runs, rule)
	if d.Verdict != "decided" || len(d.Overlap) > 0 {
		t.Fatalf("verdict %s %v, %d overlapping items; want decided", d.Verdict, d.Reasons, len(d.Overlap))
	}
	if len(d.Choices) == 0 || d.Choices[0].Config != candidate || d.Choices[0].Outcome != bench.OutcomeAccept {
		t.Fatalf("choices %+v; want %s accepted", d.Choices, candidate)
	}
}

// A run works in a directory outside the workspace. A lens that prints its
// working directory, its parent and its environment finds neither the
// workspace, where earlier results name expected answers, nor the item's
// ID, which with the roster gives the answer away. Afterwards the run's
// directory is in the workspace under the item's ID.
func privateWorkingDir(t *testing.T, chb string) {
	seeds := []int64{1}
	items := liveItems(t, []string{"F4"}, seeds)
	model := &fakeModel{items: items, probe: "pwd; ls -a ..; env", lensTools: "all", policy: func(it *bench.Item) (string, []string) { return it.Want.Verdict, it.Want.Tokens }}
	h, _ := benchHarness(t, chb, model, []string{bench.ArmSwarm}, []string{"F4"}, seeds)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("harness: %v", err)
	}
	leaks := []string{h.Workspace}
	if real, err := filepath.EvalSymlinks(h.Workspace); err == nil {
		leaks = append(leaks, real)
	}
	for _, it := range items {
		leaks = append(leaks, it.ID)
	}
	model.mu.Lock()
	probed := append([]string(nil), model.probed...)
	model.mu.Unlock()
	if len(probed) == 0 {
		t.Fatal("no lens ran the probe")
	}
	for _, out := range probed {
		for _, leak := range leaks {
			if strings.Contains(out, leak) {
				t.Errorf("a lens saw %q:\n%s", leak, out)
			}
		}
	}
	for _, it := range items {
		if _, err := os.Stat(filepath.Join(h.Workspace, "bench", it.ID, "swarm-r1", "artifact.json")); err != nil {
			t.Errorf("the run of %s was not kept in the workspace: %v", it.ID, err)
		}
	}
}

// Queen returns one JSON object, and a model may escape a non-ASCII
// character in it, ⇄ as a \u escape. Graded on her raw text alone, such a
// token is missed. The swarm arm grades her text and her decoded outputs,
// and her text alone when the outputs hold no object.
func TestQueenAnswerText_GradesTheDecodedObject(t *testing.T) {
	names := []string{"architect", "pragmatist", "skeptic"}
	want := []string{"architect⇄pragmatist", "pragmatist⇄skeptic"}
	raw := strings.ReplaceAll(`{"report":"## Swarm Verdict\n\n### ∇ Convergences\n- architect⇄pragmatist","verdict":"support",`+
		`"recommendation":"keep skeptic⇄pragmatist","gaps":["none"],"coverage":4}`, "⇄", "\\"+"u21c4")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	outputs := mustJSON(decoded)
	if got := bench.ExtractTokens(bench.TokensPair, raw, names); len(got) != 0 {
		t.Fatalf("the raw text yields %v; the fixture must escape its tokens", got)
	}
	if got := bench.ExtractTokens(bench.TokensPair, queenAnswerText(raw, outputs), names); !reflect.DeepEqual(got, want) {
		t.Errorf("graded tokens %v, want %v", got, want)
	}
	preamble := "Before the object: architect⇄skeptic.\n"
	wantAll := append([]string{"architect⇄skeptic"}, want...)
	slices.Sort(wantAll)
	if got := bench.ExtractTokens(bench.TokensPair, queenAnswerText(preamble+raw, outputs), names); !reflect.DeepEqual(got, wantAll) {
		t.Errorf("with a prose preamble, graded tokens %v, want %v", got, wantAll)
	}
	prose := "## Swarm Verdict\n- architect⇄skeptic"
	if got := queenAnswerText(prose, mustJSON(map[string]any{"final_text": prose})); got != prose {
		t.Errorf("with only the final_text fallback the text is %q, want Queen's own text", got)
	}
}

// A mixed arm where one family never answers — a thinking model that
// hits the cap — still gets a cross-model line, saying why it has no
// figure, and a per-model line showing which family returned nothing.
// A one-model arm gets no cross-model line.
func TestBenchReport_SaysWhyThereIsNoCrossModelFigure(t *testing.T) {
	names := []string{"architect", "empiricist", "pragmatist", "scholar"}
	models := []string{"qwen3.5:4b", "ministral-3:8b"}
	for _, mixed := range []bool{true, false} {
		var rows []bench.Row
		answered := map[string][2]int{}
		for i, want := range []string{bench.Support, bench.Oppose, bench.Support} {
			r := bench.Row{Arm: bench.ArmSwarm, Item: fmt.Sprintf("F1-s%d-a", i), Want: want, LensAnswers: map[string]bench.LensAnswer{}, Diversity: "low"}
			for j, n := range names {
				m, v := models[1], bench.Support
				if mixed && j%2 == 0 {
					m, v = models[0], ""
				}
				r.LensAnswers[n] = bench.LensAnswer{Verdict: v, Model: m}
				c := answered[m]
				c[0]++
				if v != "" {
					c[1]++
				}
				answered[m] = c
			}
			rows = append(rows, r)
		}
		var md strings.Builder
		writeBenchReport(&md, benchSummary(rows, nil, []string{bench.ArmSwarm}, "results.jsonl"))
		report := md.String()
		if got := strings.Contains(report, "- correlated lens errors across models: none: no pair of lenses on different models both returned a verdict"); got != mixed {
			t.Errorf("mixed %v: the cross-model line saying why there is no figure is there %v:\n%s", mixed, got, report)
		}
		for m, c := range answered {
			if want := fmt.Sprintf("%s %d/%d", m, c[1], c[0]); !strings.Contains(report, want) {
				t.Errorf("mixed %v: the report does not give %s's lens runs and verdicts as %q:\n%s", mixed, m, want, report)
			}
		}
		if want := fmt.Sprintf("low %d · not_low 0 · unknown 0 · not_checked 0, of %d", len(rows), len(rows)); !strings.Contains(report, want) {
			t.Errorf("mixed %v: the report does not count the runs by diversity as %q:\n%s", mixed, want, report)
		}
	}
}

// The solo control is held to a lens's terms: no tools, an enforced output
// schema and one repair, as a generated lens node has. With every tool and
// no schema, a small model could call tools for 30 turns and fail, which
// would count against the one call and inflate HIVE's lift.
func TestSoloWorkflow_HeldToALensTerms(t *testing.T) {
	var solo struct {
		Nodes map[string]map[string]any `yaml:"nodes"`
	}
	if err := yaml.Unmarshal([]byte(soloWorkflow("qwen3.5:4b", "none")), &solo); err != nil {
		t.Fatal(err)
	}
	node := solo.Nodes["solo"]

	gen, err := foragers.GenerateWorkflow(foragers.Minimal(liveRoster(t)), foragers.WorkflowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var swarm struct {
		Nodes map[string]map[string]any `yaml:"nodes"`
	}
	if err := yaml.Unmarshal([]byte(gen), &swarm); err != nil {
		t.Fatal(err)
	}
	var lens map[string]any
	for name, n := range swarm.Nodes {
		if len(name) > 8 && name[:8] == "forager-" {
			lens = n
			break
		}
	}
	if lens == nil {
		t.Fatal("the generated swarm has no forager node")
	}

	if !reflect.DeepEqual(node["tools"], lens["tools"]) {
		t.Errorf("solo tools %v, want the lens's %v", node["tools"], lens["tools"])
	}
	if !reflect.DeepEqual(node["on_reject"], lens["on_reject"]) {
		t.Errorf("solo on_reject %v, want the lens's %v", node["on_reject"], lens["on_reject"])
	}
	verdict := func(n map[string]any) any {
		schema, _ := n["output_schema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		return props["verdict"]
	}
	if verdict(node) == nil || !reflect.DeepEqual(verdict(node), verdict(lens)) {
		t.Errorf("solo schema verdict %v, want the lens's %v", verdict(node), verdict(lens))
	}
}

// A mixed list is rotated by the item's seed plus the repetition: run r
// of seed s gives lens slot j the model k+j places on, k = (s+r−1) mod n.
// Over n consecutive seeds, each slot runs on each model once.
func TestBenchLensModels_Rotate(t *testing.T) {
	models := []string{"qwen3.5:4b", "ministral-3:8b", "devstral-small-2:24b"}
	n := len(models)
	h := &agentHarness{lensModels: models}
	for rep := 1; rep <= 3; rep++ {
		seen := make([]map[string]bool, n)
		for s := int64(1); s <= int64(n); s++ {
			got := h.benchLensModels(bench.Item{Seed: s}, rep)
			k := int(s+int64(rep-1)) % n
			for j := range got {
				if got[j] != models[(j+k)%n] {
					t.Fatalf("seed %d rep %d: %v, want slot %d on %s", s, rep, got, j, models[(j+k)%n])
				}
				if seen[j] == nil {
					seen[j] = map[string]bool{}
				}
				seen[j][got[j]] = true
			}
		}
		for j, m := range seen {
			if len(m) != n {
				t.Errorf("rep %d: slot %d ran on %d of %d models over %d seeds", rep, j, len(m), n, n)
			}
		}
	}
	one := &agentHarness{lensModels: models[:1]}
	if got := one.benchLensModels(bench.Item{Seed: 5}, 2); !slices.Equal(got, models[:1]) {
		t.Errorf("one model rotated to %v", got)
	}
}

// The solo control and the direct voice are one prompt and one schema, so
// the swarm's extra vote is the solo call the bench compares it with.
func TestSoloWorkflow_IsTheDirectVoice(t *testing.T) {
	var solo struct {
		Nodes map[string]map[string]any `yaml:"nodes"`
	}
	if err := yaml.Unmarshal([]byte(soloWorkflow("qwen3.5:4b", "none")), &solo); err != nil {
		t.Fatal(err)
	}
	gen, err := foragers.GenerateWorkflow(foragers.Minimal(liveRoster(t)), foragers.WorkflowOptions{DirectVoice: true, Model: "qwen3.5:4b", ForagerReasoning: "none"})
	if err != nil {
		t.Fatal(err)
	}
	var swarm struct {
		Nodes map[string]map[string]any `yaml:"nodes"`
	}
	if err := yaml.Unmarshal([]byte(gen), &swarm); err != nil {
		t.Fatal(err)
	}
	direct, ok := swarm.Nodes["forager-"+foragers.DirectVoiceName]
	if !ok {
		t.Fatal("the generated swarm has no direct voice")
	}
	node := solo.Nodes["solo"]
	for _, key := range []string{"prompt", "output_schema", "tools", "on_reject", "model", "reasoning", "role"} {
		a, b := node[key], direct[key]
		if s, ok := a.(string); ok {
			a = strings.TrimSpace(s)
		}
		if s, ok := b.(string); ok {
			b = strings.TrimSpace(s)
		}
		if !reflect.DeepEqual(a, b) {
			t.Errorf("solo %s %v, direct voice %v; want them equal", key, a, b)
		}
	}
}

// The profile canary is a slow bench case whose pairs each expect two
// verdicts, spanning more than one family, so a constant model fails it.
func TestProfileCanaryCase(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "agent-harness", "suite.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var suite harnessSuite
	if err := yaml.Unmarshal(raw, &suite); err != nil {
		t.Fatal(err)
	}
	var canary *harnessCase
	for i := range suite.Cases {
		if suite.Cases[i].Name == "profile-canary" {
			canary = &suite.Cases[i]
		}
	}
	if canary == nil || canary.Kind != "bench" || !canary.Slow || !canary.RequirePairs || len(canary.Families) < 2 {
		t.Fatalf("profile-canary = %+v; want a slow bench case over several families with require_pairs", canary)
	}
	items, err := benchItems(liveRoster(t), *canary)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(items); i += 2 {
		if items[i].Want.Verdict == items[i+1].Want.Verdict {
			t.Errorf("pair %s expects %s on both twins", items[i].Pair, items[i].Want.Verdict)
		}
	}
	if want := 2 * len(canary.Families) * len(canary.Seeds); len(items) != want {
		t.Errorf("%d items, want %d: one pair per family and seed", len(items), want)
	}
}

// finish_length is the sum of the run's nodes' cutoff_calls, and null when a
// node ran on the Gemini CLI, which reports no stop reason.
func TestFinishLength_SumsTheRunsNodes(t *testing.T) {
	cases := []struct {
		name  string
		nodes map[string][2]any // node → provider, cut-off calls
	}{
		{"none cut off", map[string][2]any{"a": {"openai", int64(0)}, "b": {"openai", int64(0)}}},
		{"two nodes cut off", map[string][2]any{"a": {"openai", int64(2)}, "b": {"anthropic", int64(1)}, "c": {"claude-cli", int64(0)}}},
		{"a gemini-cli node", map[string][2]any{"a": {"openai", int64(1)}, "b": {"gemini-cli", int64(0)}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := db.NewStore(filepath.Join(t.TempDir(), "finish.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Init(); err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var seeds []db.NodeSeed
			for name := range c.nodes {
				seeds = append(seeds, db.NodeSeed{Name: name, Type: "agent"})
			}
			runID, err := s.Workflows().CreateWorkflowRun("finish", 1, "name: finish", "{}", seeds)
			if err != nil {
				t.Fatal(err)
			}
			sum, blind := 0, false
			for name, n := range c.nodes {
				provider, cut := n[0].(string), n[1].(int64)
				if err := s.Workflows().UpdateNodeMetrics(runID, name, 1, 1, 0, provider, "", 0, 1); err != nil {
					t.Fatal(err)
				}
				if err := s.Workflows().AddNodeCutoffCalls(runID, name, cut); err != nil {
					t.Fatal(err)
				}
				sum += int(cut)
				blind = blind || provider == "gemini-cli"
			}
			got, err := finishLength(s, runID)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case blind && got != nil:
				t.Errorf("finish_length = %d, want null: a node ran on the Gemini CLI", *got)
			case !blind && (got == nil || *got != sum):
				t.Errorf("finish_length = %v, want %d", got, sum)
			}
		})
	}
}

// The harness reads finish_length from a real run's database: the solo
// workflow run in process against a fake endpoint, which cuts off the first
// `cut` calls it answers. The run's row counts exactly the calls the server
// cut off, and the bench's guard reads it.
func TestReadBenchAnswer_CountsTheCallsARunCutOff(t *testing.T) {
	const model = "fake-lens"
	for _, cut := range []int64{0, 1} {
		t.Run(fmt.Sprintf("cut=%d", cut), func(t *testing.T) {
			var calls, cutOff atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/models"):
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": model}}})
					return
				case !strings.HasSuffix(r.URL.Path, "/chat/completions"):
					http.NotFound(w, r)
					return
				}
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), "hive-constraint-ok") {
					// The constraint probe a schema'd node triggers; it is no
					// node call, so it is neither cut off nor counted.
					_ = json.NewEncoder(w).Encode(map[string]any{
						"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": `{"probe":"hive-constraint-ok"}`}}},
						"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
					})
					return
				}
				content, reason := `{"key_points":["a"],"verdict":"support","recommendation":"r"}`, "stop"
				if calls.Add(1) <= cut {
					content, reason = `{"verdict":"sup`, "length"
					cutOff.Add(1)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{map[string]any{"finish_reason": reason, "message": map[string]any{"content": content}}},
					"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
				})
			}))
			defer srv.Close()
			t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
			t.Setenv("OPENAI_API_KEY", "k")
			t.Setenv("HIVE_PROVIDER", "")
			t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
			t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")

			dir := t.TempDir()
			wf := filepath.Join(dir, "solo.yaml")
			if err := os.WriteFile(wf, []byte(soloWorkflow(model, "")), 0o644); err != nil {
				t.Fatal(err)
			}
			dbPath := filepath.Join(dir, "hive.db")
			s, err := db.NewStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Init(); err != nil {
				t.Fatal(err)
			}
			_, _ = runner.Run(context.Background(), s, runner.Config{
				WorkflowYAML: wf, ProjectName: "bench-solo", ProjectDir: dir, Provider: "openai", MaxIterations: 10,
				Inputs: map[string]any{"question": "q", "context": "c"}, Log: io.Discard,
			})
			s.Close()

			row := bench.Row{Arm: bench.ArmSolo}
			readBenchAnswer(&row, dbPath, "")
			if row.FinishLength == nil || int64(*row.FinishLength) != cutOff.Load() {
				t.Fatalf("finish_length = %v, want %d, the calls the server cut off (%s)", row.FinishLength, cutOff.Load(), row.Error)
			}
		})
	}
}
