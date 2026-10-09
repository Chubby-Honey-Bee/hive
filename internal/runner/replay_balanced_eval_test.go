package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// replayFixture holds the replies of a captured balanced swarm with the
// coverage pass; testdata/balanced-eval-replay.json says where they came
// from.
type replayFixture struct {
	Question      string            `json:"question"`
	Foragers      map[string]string `json:"foragers"`
	SwarmEvaluate string            `json:"swarm_evaluate"`
	QueenReport   string            `json:"queen_report"`
	QueenTail     map[string]any    `json:"queen_tail"`
}

// replayRequest is one call the fake endpoint served.
type replayRequest struct {
	role   string // forager:<name>, evaluate, followup, queen or unknown
	body   []byte
	system string
	prompt string
	tools  int
}

// replayServer is an OpenAI-compatible endpoint that answers each swarm
// role with its captured reply. badForager, when set, names a forager that
// answers with a verdict outside the enum on every call; failGap names a
// gap whose follow-up call gets HTTP 500; profile is the persona profile
// the swarm is generated with, full when empty.
type replayServer struct {
	fx         *replayFixture
	badForager string
	failGap    string
	profile    string

	mu       sync.Mutex
	requests []replayRequest
}

// tallyMarker is the line of the Queen's prompt the tally follows.
const tallyMarker = "casts no vote, so it is never the plurality:"

// badVerdict is the reply a badForager gives: a verdict outside the enum.
func badVerdict(name string) string { return fmt.Sprintf(`{"forager":%q,"verdict":"maybe"}`, name) }

var (
	replayForagerKey = regexp.MustCompile(`\{"forager":"(\w+)"`)
	replayPlurality  = regexp.MustCompile(`Plurality: (support|oppose|conditional|abstain)\b`)
	// leftoverToken matches a placeholder left in a prompt: {name},
	// {a.b} or {a.b:c}, with no quote, space or angle bracket inside.
	leftoverToken = regexp.MustCompile(`\{[a-z_][a-z0-9_]*(?:[.:][a-z0-9_-]+)*\}`)
)

func followupReply(gap string) string { return "follow-up finding for: " + gap }

func (s *replayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The replay lists no models, as an endpoint without GET /models does,
	// so the model preflight warns and lets the run through.
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		http.NotFound(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools          []any `json:"tools"`
		ResponseFormat struct {
			JSONSchema struct {
				Name string `json:"name"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	_ = json.Unmarshal(raw, &body)
	// The constraint probe chb sends before the first node. The replay does
	// not constrain decoding, so it answers the probe's prose prompt in
	// prose, and the probe is not one of the swarm's requests.
	if body.ResponseFormat.JSONSchema.Name == "hive_constraint_probe" {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "It is sunny."}}},
		})
		return
	}
	req := replayRequest{body: raw, tools: len(body.Tools)}
	for _, m := range body.Messages {
		switch m.Role {
		case "system":
			req.system += m.Content
		case "user":
			req.prompt += m.Content
		}
	}
	var text string
	status := http.StatusOK
	switch p := req.prompt; {
	case strings.Contains(p, "You are the swarm's coverage evaluator"):
		req.role, text = "evaluate", s.fx.SwarmEvaluate
	case strings.Contains(p, "Gap to fill:"):
		gap := p[strings.Index(p, "Gap to fill:")+len("Gap to fill:"):]
		gap = strings.TrimSpace(gap[:strings.Index(gap, "Return JSON exactly")])
		js, _ := json.Marshal(map[string]string{"followup_findings": followupReply(gap)})
		req.role, text = "followup", string(js)
		if gap == s.failGap {
			status = http.StatusInternalServerError
		}
	case strings.Contains(p, "Vote tally, counted by the runner"):
		out := map[string]any{"report": s.fx.QueenReport, "dissent_from_plurality": ""}
		for k, v := range s.fx.QueenTail {
			out[k] = v
		}
		// The tally is the line after its marker; the Queen persona quotes
		// an example tally of its own earlier in the prompt.
		tally := p[strings.Index(p, tallyMarker)+len(tallyMarker):]
		tally = strings.TrimSpace(strings.SplitN(strings.TrimPrefix(tally, "\n"), "\n", 2)[0])
		if m := replayPlurality.FindStringSubmatch(tally); m == nil || m[1] != out["verdict"] {
			out["dissent_from_plurality"] = "The captured synthesis weighed the lenses differently."
		}
		js, _ := json.Marshal(out)
		req.role, text = "queen", string(js)
	case replayForagerKey.MatchString(p):
		name := replayForagerKey.FindStringSubmatch(p)[1]
		req.role, text = "forager:"+name, s.fx.Foragers[name]
		if name == s.badForager {
			text = badVerdict(name)
		}
	default:
		req.role, text = "unknown", `{"verdict":"abstain"}`
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if status != http.StatusOK {
		http.Error(w, `{"error":{"message":"replay: this follow-up fails"}}`, status)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}},
		"usage":   map[string]any{"prompt_tokens": len(raw) / 4, "completion_tokens": len(text) / 4},
	})
}

func (s *replayServer) byRole() map[string][]replayRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]replayRequest{}
	for _, r := range s.requests {
		out[r.role] = append(out[r.role], r)
	}
	return out
}

// runReplay generates the default `chb ask` swarm — balanced, with the
// coverage pass and the Queen persona — optionally rewrites its YAML, and
// runs it on the OpenAI-compatible backend against the replay server.
func runReplay(t *testing.T, fx *replayFixture, rewrite func(string) string) (*replayServer, string) {
	t.Helper()
	return runReplayOn(t, &replayServer{fx: fx}, rewrite)
}

// runReplayOn is runReplay against a server the caller configured.
func runReplayOn(t *testing.T, srv *replayServer, rewrite func(string) string) (*replayServer, string) {
	t.Helper()
	fx := srv.fx
	all, err := foragers.Load(filepath.Join("..", "..", "foragers"))
	if err != nil {
		t.Fatal(err)
	}
	swarm, err := foragers.Filter(all, []string{"balanced"})
	if err != nil {
		t.Fatal(err)
	}
	synth, _ := foragers.ByName(all, "queen")
	yamlText, err := foragers.GenerateWorkflow(swarm, foragers.WorkflowOptions{Name: "replay", Evaluate: true, Synthesizer: synth, PersonaProfile: srv.profile})
	if err != nil {
		t.Fatal(err)
	}
	if rewrite != nil {
		yamlText = rewrite(yamlText)
	}

	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	t.Setenv("OPENAI_API_KEY", "replay")
	t.Setenv("OPENAI_BASE_URL", hs.URL)
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"swarm.md", "analyst.md"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "agents", a))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "agents", a), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wf := filepath.Join(dir, "swarm.yaml")
	if err := os.WriteFile(wf, []byte(yamlText), 0o644); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	if _, err := Run(context.Background(), newTempStore(t), Config{
		WorkflowYAML: wf, ProjectDir: dir, Provider: "openai", Log: &log,
		Inputs: map[string]any{"question": fx.Question, "context": ""},
	}); err != nil {
		t.Fatalf("Run: %v\n%s", err, log.String())
	}
	return srv, yamlText
}

func loadReplayFixture(t *testing.T) *replayFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "balanced-eval-replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx replayFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return &fx
}

// lineAfter returns the line that follows the first line holding marker.
func lineAfter(t *testing.T, prompt, marker string) string {
	t.Helper()
	lines := strings.Split(prompt, "\n")
	for i, l := range lines {
		if strings.Contains(l, marker) && i+1 < len(lines) {
			return strings.TrimSpace(lines[i+1])
		}
	}
	t.Fatalf("no line after %q in the prompt", marker)
	return ""
}

// The captured balanced run with the coverage pass, replayed with no model:
// the evaluator's gaps reach at most Followups fresh lenses and Queen reads
// the rest by name; no placeholder is left in any request; Queen reads the
// full verdict of every forager whose reply opened with prose; each forager
// request drops the analyst persona and the tool schemas; and the tally and
// the ∇ pairs Queen reads are the ones the verdicts give.
func TestReplayBalancedEval(t *testing.T) {
	fx := loadReplayFixture(t)
	srv, yamlText := runReplay(t, fx, nil)
	got := srv.byRole()

	if u := got["unknown"]; len(u) > 0 {
		t.Fatalf("%d requests matched no swarm role; first prompt:\n%s", len(u), u[0].prompt)
	}
	for _, r := range srv.requests {
		if strings.Contains(r.prompt, "The previous attempt failed acceptance criteria") {
			t.Errorf("a %s reply was repaired; the replay should pass on the first attempt", r.role)
		}
	}

	// Follow-ups: the first Followups gaps, one call each.
	followups := foragers.WorkflowOptions{}.WithDefaults().Followups
	gapsAny, _ := workflow.ExtractJSONOutput(fx.SwarmEvaluate)["gaps"].([]any)
	var gaps []string
	for _, g := range gapsAny {
		gaps = append(gaps, g.(string))
	}
	followed, unfollowed := gaps[:min(len(gaps), followups)], gaps[min(len(gaps), followups):]
	if len(got["followup"]) != len(followed) {
		t.Fatalf("%d follow-up calls for %d gaps, want %d", len(got["followup"]), len(gaps), len(followed))
	}
	for _, g := range followed {
		n := 0
		for _, r := range got["followup"] {
			if strings.Contains(r.prompt, g) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("gap %q reached %d follow-up calls, want 1", g, n)
		}
	}
	if len(got["queen"]) != 1 {
		t.Fatalf("%d Queen calls, want 1", len(got["queen"]))
	}
	queen := got["queen"][0].prompt
	var named []string
	if err := json.Unmarshal([]byte(lineAfter(t, queen, unfollowedMarker)), &named); err != nil {
		t.Fatalf("the gaps no lens was sent to are not a JSON list: %v", err)
	}
	if strings.Join(named, "\n") != strings.Join(unfollowed, "\n") {
		t.Errorf("Queen is told no lens was sent to %q, want %q", named, unfollowed)
	}
	for _, g := range followed {
		if !strings.Contains(queen, followupReply(g)) {
			t.Errorf("Queen's prompt lacks the follow-up finding for %q", g)
		}
	}

	// No placeholder survives into any request.
	for _, r := range srv.requests {
		for _, lit := range []string{"{gap}", "{followup_findings}"} {
			if strings.Contains(string(r.body), lit) {
				t.Errorf("a %s request carries the literal %s", r.role, lit)
			}
		}
		if m := leftoverToken.FindString(r.prompt); m != "" {
			t.Errorf("a %s request carries the literal %s", r.role, m)
		}
	}

	// Full verdicts for foragers whose reply opened with prose.
	preamble := 0
	for name, reply := range fx.Foragers {
		s := strings.TrimSpace(reply)
		if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "```") {
			continue
		}
		preamble++
		for _, item := range verdictItems(workflow.ExtractJSONOutput(reply)) {
			if !strings.Contains(queen, item) {
				t.Errorf("Queen's prompt lacks %q from %s, whose reply opened with prose", item, name)
			}
			if !strings.Contains(got["evaluate"][0].prompt, item) {
				t.Errorf("the evaluator's prompt lacks %q from %s", item, name)
			}
		}
	}
	if preamble == 0 {
		t.Fatal("the fixture has no forager reply that opens with prose")
	}

	// The tally and the ∇ pairs, recomputed from the served verdicts.
	verdicts := map[string]string{}
	for name, reply := range fx.Foragers {
		verdicts[name], _ = workflow.ExtractJSONOutput(reply)["verdict"].(string)
	}
	counts, plurality := tallyOf(verdicts)
	tally := lineAfter(t, queen, tallyMarker)
	for v, n := range counts {
		if !strings.Contains(tally, fmt.Sprintf("%s %d (", v, n)) {
			t.Errorf("tally %q lacks %s %d", tally, v, n)
		}
	}
	if plurality != "" && !strings.HasSuffix(tally, "Plurality: "+plurality) {
		t.Errorf("tally %q, want plurality %s", tally, plurality)
	}

	// The Queen persona is inlined into her prompt, so a token in its prose
	// would be filled too: every lens has a node here, so no line may say
	// one has none, and the tally is written once, where the template asks.
	if strings.Contains(queen, "(no verdict:") {
		t.Errorf("Queen's prompt says a lens has no verdict, though every lens completed:\n%s", queen)
	}
	if n := strings.Count(queen, tally); n != 1 {
		t.Errorf("the tally is written %d times in Queen's prompt, want once", n)
	}
	defn, _ := workflow.LoadYAMLString(yamlText)
	want := firedPairs(parseResonates(defn), verdicts)
	nabla := lineAfter(t, queen, "two lenses can both say conditional for different reasons.")
	if (len(want) == 0 && nabla != "none") || (len(want) > 0 && nabla != strings.Join(want, "; ")) {
		t.Errorf("∇ line %q, want %q", nabla, strings.Join(want, "; "))
	}

	// Every model call runs as the swarm persona with no tools.
	persona, err := os.ReadFile(filepath.Join("..", "..", "agents", "swarm.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range srv.requests {
		if r.system != string(persona) || r.tools != 0 {
			t.Errorf("a %s request has system %d chars and %d tools, want agents/swarm.md and none", r.role, len(r.system), r.tools)
		}
	}

	// Each forager request is shorter than the same run's with the analyst
	// persona and every tool schema by at least the analyst persona plus the
	// tool schemas less the swarm persona. A call that offers no tools
	// carries the node's output_schema as response_format (runner.md §
	// Output schema on the wire), which a call with tools does not; the cut
	// is measured without it.
	before, _ := runReplay(t, fx, func(y string) string {
		y = strings.ReplaceAll(y, "agent: swarm", "agent: analyst")
		return strings.ReplaceAll(y, "\n    tools: []", "")
	})
	analyst, err := os.ReadFile(filepath.Join("..", "..", "agents", "analyst.md"))
	if err != nil {
		t.Fatal(err)
	}
	schemas, _ := json.Marshal(openaiToolsFromRegistry(NewToolRegistry(t.TempDir(), nil, NewFSRecorder())))
	escapedPersona, _ := json.Marshal(string(persona))
	chars := utf8.RuneCount
	minCut := chars(analyst) + chars(schemas) - chars(escapedPersona)
	beforeByRole := before.byRole()
	for name := range fx.Foragers {
		a, b := got["forager:"+name], beforeByRole["forager:"+name]
		if len(a) != 1 || len(b) != 1 {
			t.Fatalf("forager %s: %d requests now and %d before, want 1 each", name, len(a), len(b))
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(a[0].body, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["response_format"]; !ok {
			t.Errorf("forager %s: a request with no tools carries no response_format", name)
		}
		delete(fields, "response_format")
		after, _ := json.Marshal(fields)
		cut := chars(b[0].body) - chars(after)
		if cut < minCut {
			t.Errorf("forager %s request is %d chars shorter, want at least %d", name, cut, minCut)
		}
		t.Logf("forager %-10s request %6d → %6d chars without its response_format (%d shorter; analyst.md %d + tool schemas %d − swarm.md %d)",
			name, chars(b[0].body), chars(after), cut, chars(analyst), chars(schemas), chars(persona))
	}
}

// With no gaps the edges skip the follow-up fan: Queen runs straight after
// the evaluator, no follow-up call is made, and her prompt holds no
// placeholder, because the evaluator set the follow-up findings and the
// uninvestigated gaps to empty.
func TestReplayBalancedEval_NoGaps(t *testing.T) {
	fx := loadReplayFixture(t)
	fx.SwarmEvaluate = `{"coverage":5,"gaps":[]}`
	srv, _ := runReplay(t, fx, nil)
	got := srv.byRole()
	if n := len(got["followup"]); n != 0 {
		t.Errorf("%d follow-up calls with no gaps, want 0", n)
	}
	if len(got["queen"]) != 1 {
		t.Fatalf("%d Queen calls, want 1", len(got["queen"]))
	}
	queen := got["queen"][0].prompt
	if m := leftoverToken.FindString(queen); m != "" {
		t.Errorf("Queen's prompt carries the literal %s", m)
	}
	var named []string
	if err := json.Unmarshal([]byte(lineAfter(t, queen, unfollowedMarker)), &named); err != nil || len(named) != 0 {
		t.Errorf("uninvestigated gaps %v (%v), want an empty list", named, err)
	}
}

// unfollowedMarker is the line of the Queen's prompt the gaps past the
// follow-up limit follow.
const unfollowedMarker = "which no follow-up lens was sent to:"

// One lens that answers outside the verdict enum twice is rejected, and one
// follow-up call fails. The evaluator and Queen join settled, so both still
// run: each reads a line saying the lens was rejected, the tally names it,
// the follow-up findings keep a section for the failed gap that says why it
// has no finding, and the run completes.
func TestReplayBalancedEval_RejectedLensAndFailedFollowup(t *testing.T) {
	fx := loadReplayFixture(t)
	names := make([]string, 0, len(fx.Foragers))
	for name := range fx.Foragers {
		names = append(names, name)
	}
	sort.Strings(names)
	bad := names[0]
	gapsAny, _ := workflow.ExtractJSONOutput(fx.SwarmEvaluate)["gaps"].([]any)
	k := foragers.WorkflowOptions{}.WithDefaults().Followups
	if len(gapsAny) < 2 || k < 2 {
		t.Fatalf("the fixture has %d gaps for %d follow-ups, want at least 2 of each", len(gapsAny), k)
	}
	failed := gapsAny[1].(string)

	srv, _ := runReplayOn(t, &replayServer{fx: fx, badForager: bad, failGap: failed}, nil)
	got := srv.byRole()

	if n := len(got["forager:"+bad]); n != 2 {
		t.Errorf("%s was called %d times, want the dispatch and one repair", bad, n)
	}
	if len(got["evaluate"]) != 1 || len(got["queen"]) != 1 {
		t.Fatalf("%d evaluator and %d Queen calls, want one each", len(got["evaluate"]), len(got["queen"]))
	}
	rejectedLine := fmt.Sprintf("(no verdict: forager-%s is rejected in this run)", bad)
	for _, role := range []string{"evaluate", "queen"} {
		if !strings.Contains(got[role][0].prompt, rejectedLine) {
			t.Errorf("the %s prompt lacks %q", role, rejectedLine)
		}
	}
	queen := got["queen"][0].prompt
	if tally := lineAfter(t, queen, tallyMarker); !strings.Contains(tally, fmt.Sprintf("no verdict from %s (rejected)", bad)) {
		t.Errorf("tally %q does not name the rejected lens", tally)
	}

	followed := gapsAny[:min(len(gapsAny), k)]
	for i, g := range followed {
		gap := g.(string)
		head := fmt.Sprintf("## Item %d\n\n", i+1)
		at := strings.Index(queen, head)
		if at < 0 {
			t.Errorf("Queen's prompt has no section for follow-up item %d", i+1)
			continue
		}
		body := queen[at+len(head):]
		switch {
		case gap == failed && !strings.HasPrefix(body, "(no finding: the call failed:"):
			t.Errorf("item %d, whose call failed, reads %.80q, want the reason it has no finding", i+1, body)
		case gap != failed && !strings.Contains(body[:strings.Index(body+"## Item", "## Item")], followupReply(gap)):
			t.Errorf("item %d lacks its finding for %q", i+1, gap)
		}
	}
}
