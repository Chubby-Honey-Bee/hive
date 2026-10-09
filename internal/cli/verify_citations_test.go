package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/citations"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// unpaywallStub stands in for http.DefaultTransport, which the verifier's
// client uses. With block set it holds every request until its context ends,
// as a lookup cut off by --budget would be; otherwise it answers open access.
type unpaywallStub struct {
	mu    sync.Mutex
	block bool
	calls []string
}

func (s *unpaywallStub) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.calls = append(s.calls, strings.TrimPrefix(req.URL.Path, "/v2/"))
	block := s.block
	s.mu.Unlock()
	if block {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"is_oa": true, "title": "t"}`)),
		Request:    req,
	}, nil
}

type verifyReport struct {
	FindingsChecked int  `json:"findings_checked"`
	BudgetExhausted bool `json:"budget_exhausted"`
	Checks          []struct {
		DOIs       []string `json:"dois"`
		OACount    int      `json:"oa_count"`
		Unverified int      `json:"unverified_count"`
	} `json:"checks"`
}

// captureStdout runs fn and returns what it wrote to os.Stdout.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	out := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()
	runErr := fn()
	os.Stdout = prev
	w.Close()
	return <-out, runErr
}

func runVerifyCitations(t *testing.T, args ...string) verifyReport {
	t.Helper()
	cmd := newVerifyCitationsCmd()
	cmd.SetArgs(append(args, "--json"))
	out, err := captureStdout(t, func() error { return cmd.ExecuteContext(context.Background()) })
	if err != nil {
		t.Fatalf("verify-citations %v: %v", args, err)
	}
	var rep verifyReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("verify-citations %v printed %q: %v", args, out, err)
	}
	return rep
}

func seedCitingFindings(t *testing.T, sources []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cite.db")
	s, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	for _, src := range sources {
		src := src
		if _, err := s.Findings().AddFinding(&db.Finding{
			Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "cites " + src, SourceURLs: &src,
		}); err != nil {
			t.Fatal(err)
		}
	}
	prev := dbPath
	dbPath = path
	t.Cleanup(func() { dbPath = prev })
}

// budgetCut is long enough that the command's setup (opening the database,
// reading the findings) always finishes inside it, so the budget runs out
// while the first lookup is held by the stub, never before that lookup
// starts, even on a loaded or briefly paused machine.
const budgetCut = "3s"

func stubUnpaywall(t *testing.T) *unpaywallStub {
	t.Helper()
	t.Setenv(citations.EmailEnv, "verify@example.org")
	stub := &unpaywallStub{}
	prev := http.DefaultTransport
	http.DefaultTransport = stub
	t.Cleanup(func() { http.DefaultTransport = prev })
	return stub
}

// When --budget runs out, every finding is still reported, the DOI cut off
// mid-lookup is not cached, and a re-run fetches exactly what is not cached.
func TestVerifyCitations_BudgetRunsOut(t *testing.T) {
	sources := []string{
		"https://doi.org/10.1111/aaa",
		"10.1111/bbb, 10.1111/ccc",
		"https://doi.org/10.1111/ddd",
	}
	var allDOIs []string
	for _, s := range sources {
		for _, part := range strings.Split(s, ", ") {
			allDOIs = append(allDOIs, strings.TrimPrefix(part, "https://doi.org/"))
		}
	}
	seedCitingFindings(t, sources)
	stub := stubUnpaywall(t)

	stub.block = true
	rep := runVerifyCitations(t, "--budget", budgetCut)
	if !rep.BudgetExhausted {
		t.Error("run 1: budget_exhausted is false; the budget cut a lookup short")
	}
	if rep.FindingsChecked != len(sources) {
		t.Errorf("run 1: %d findings reported, want all %d", rep.FindingsChecked, len(sources))
	}
	unverified := 0
	for _, c := range rep.Checks {
		unverified += c.Unverified
	}
	if unverified != len(allDOIs) {
		t.Errorf("run 1: %d DOIs unverified, want all %d", unverified, len(allDOIs))
	}
	if len(stub.calls) != 1 || stub.calls[0] != allDOIs[0] {
		t.Errorf("run 1 called for %v; want only %s, the lookup the budget cut short", stub.calls, allDOIs[0])
	}

	// Nothing was answered, so the re-run fetches every DOI, the cut-off one
	// included.
	stub.block, stub.calls = false, nil
	rep = runVerifyCitations(t, "--budget", "0")
	if strings.Join(stub.calls, " ") != strings.Join(allDOIs, " ") {
		t.Errorf("run 2 called for %v; want %v", stub.calls, allDOIs)
	}
	if rep.BudgetExhausted {
		t.Error("run 2: budget_exhausted with the budget disabled")
	}

	// Everything is cached now: a third run calls out for nothing.
	stub.calls = nil
	rep = runVerifyCitations(t, "--budget", "0")
	if len(stub.calls) != 0 {
		t.Errorf("run 3 called for %v; everything was cached", stub.calls)
	}
	oa := 0
	for _, c := range rep.Checks {
		oa += c.OACount
	}
	if oa != len(allDOIs) {
		t.Errorf("run 3: %d DOIs open access from the cache, want %d", oa, len(allDOIs))
	}
}

// A single DOI the budget cuts short still reports that the budget ran out.
func TestVerifyCitations_BudgetCutsTheLastLookup(t *testing.T) {
	seedCitingFindings(t, []string{"https://doi.org/10.1111/only"})
	stub := stubUnpaywall(t)
	stub.block = true
	rep := runVerifyCitations(t, "--budget", budgetCut)
	if !rep.BudgetExhausted {
		t.Error("budget_exhausted is false; the budget cut the only lookup short")
	}
	if len(rep.Checks) != 1 || rep.Checks[0].Unverified != 1 {
		t.Errorf("checks = %+v; want the one DOI unverified", rep.Checks)
	}
}

// With no contact address set, verify-citations sends Unpaywall nothing:
// every DOI is unverified and nothing is cached, so a later run with an
// address set fetches them all.
func TestVerifyCitations_NoContactReadsTheCacheOnly(t *testing.T) {
	seedCitingFindings(t, []string{"https://doi.org/10.1111/aaa", "10.1111/bbb"})
	stub := stubUnpaywall(t)
	t.Setenv(citations.EmailEnv, "")
	rep := runVerifyCitations(t)
	unverified := 0
	for _, c := range rep.Checks {
		unverified += c.Unverified
	}
	if len(stub.calls) != 0 || unverified != 2 {
		t.Fatalf("with no contact address: calls %v, %d unverified; want no call and both unverified", stub.calls, unverified)
	}
	t.Setenv(citations.EmailEnv, "verify@example.org")
	runVerifyCitations(t)
	if len(stub.calls) != 2 {
		t.Errorf("with an address set the run called for %v; want both DOIs, none of them cached", stub.calls)
	}
}

// A run without a contact address says why it reads the cache only, naming
// the variable to set; under --offline, or with an address, it says nothing.
func TestVerifyCitations_CacheOnlyNamesTheVariable(t *testing.T) {
	var buf bytes.Buffer
	if !(&verifyCitationsOptions{}).cacheOnly(&citations.HTTPVerifier{}, &buf) || !strings.Contains(buf.String(), citations.EmailEnv) {
		t.Errorf("no address: note %q; want cache only and a note naming %s", buf.String(), citations.EmailEnv)
	}
	buf.Reset()
	if !(&verifyCitationsOptions{offline: true}).cacheOnly(&citations.HTTPVerifier{}, &buf) || buf.Len() != 0 {
		t.Errorf("--offline: note %q; want cache only and no note", buf.String())
	}
	if (&verifyCitationsOptions{}).cacheOnly(&citations.HTTPVerifier{Email: "a@example.org"}, &buf) || buf.Len() != 0 {
		t.Errorf("with an address: note %q; want lookups and no note", buf.String())
	}
}
