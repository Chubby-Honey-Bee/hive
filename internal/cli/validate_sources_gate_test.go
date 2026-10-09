package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const completeEval = `{"coverage":4,"depth":4,"sources":4,"actionability":4,"mss_integrity":4,"verdict":"COMPLETE"}`

// sourceStatus is the test server's answer for a request: /ok* and /doc?id=1
// are live, except that a path ending in a slash names no file, as on a
// static file server. Everything else is gone. The expected gate outcome is
// computed from it, per cited URL.
func sourceStatus(r *http.Request) int {
	if strings.HasSuffix(r.URL.Path, "/") {
		return http.StatusNotFound
	}
	if strings.HasPrefix(r.URL.Path, "/ok") || (r.URL.Path == "/doc" && r.URL.Query().Get("id") == "1") {
		return http.StatusOK
	}
	return http.StatusNotFound
}

// verdictFor is the verdict validate-sources should record for an http URL
// served by sourceServer, computed from sourceStatus.
func verdictFor(t *testing.T, raw string) string {
	t.Helper()
	p, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if sourceStatus(&http.Request{URL: p}) == http.StatusNotFound {
		return "dead"
	}
	return "live"
}

func sourceServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(sourceStatus(r))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ingestFindings writes one FINDING marker per source_urls value, each at its
// own coordinate so conflict detection has nothing to pair, and ingests them
// into the wave through `chb ingest`.
func ingestFindings(t *testing.T, wave int, sourceURLs ...any) {
	t.Helper()
	var b strings.Builder
	for i, s := range sourceURLs {
		m, _ := json.Marshal(map[string]any{
			"d1": i, "mss_label": "definition", "finding": fmt.Sprintf("fact %c", 'a'+i), "source_urls": s,
		})
		fmt.Fprintf(&b, "<!-- FINDING: %s -->\n", m)
	}
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, newIngestCmd(), path, "--wave", fmt.Sprint(wave), "--agent", "a"); err != nil {
		t.Fatalf("ingest: %v", err)
	}
}

// A dead URL a finding cites blocks its wave once `chb validate-sources` has
// found it dead: the gate looks each cited URL up, whatever wave its sources
// row carries.
func TestGate_DeadCitedSourceBlocksAfterValidateSources(t *testing.T) {
	s := useTestStore(t)
	srv := sourceServer(t)
	u := func(p string) string { return srv.URL + p }

	cited := []string{u("/gone"), u("/ok"), u("/gone-too"), u("/gone/"), u("/doc?id=1"), u("/doc?id=2")}
	ingestFindings(t, 1,
		[]string{cited[0], cited[1]},   // a JSON array
		"see "+cited[2]+".",            // prose, trailing period
		cited[3],                       // another spelling of /gone
		"["+cited[4]+"]("+cited[4]+")", // a Markdown link to a live query
		cited[5],                       // the same path, another query
	)

	wantDead := 0
	for _, c := range cited {
		if sourceStatus(httptest.NewRequest(http.MethodGet, c, nil)) == http.StatusNotFound {
			wantDead++
		}
	}

	if _, err := execute(t, newValidateSourcesCmd(), "--wave", "1"); err != nil {
		t.Fatalf("validate-sources: %v", err)
	}
	for _, c := range cited {
		var wave sql.NullInt64
		var status string
		if err := s.ReadDB.QueryRow(`SELECT wave, validation_status FROM sources WHERE url=?`, c).Scan(&wave, &status); err != nil {
			t.Fatalf("no verdict recorded for %s: %v", c, err)
		}
		want := "live"
		if sourceStatus(httptest.NewRequest(http.MethodGet, c, nil)) == http.StatusNotFound {
			want = "dead"
		}
		if status != want || !wave.Valid || wave.Int64 != 1 {
			t.Errorf("%s: recorded %s in wave %v; want %s in wave 1", c, status, wave, want)
		}
	}

	out, err := execute(t, newGuardCmd(), "--wave", "1", "--eval", completeEval)
	if err == nil {
		t.Fatalf("the gate opened over %d dead cited source(s):\n%s", wantDead, out)
	}
	if want := fmt.Sprintf("%d dead source(s) in wave 1", wantDead); !strings.Contains(out, want) {
		t.Errorf("guard printed:\n%s\nwant %q", out, want)
	}
	var gates int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM wave_gates WHERE wave=1`).Scan(&gates); err != nil {
		t.Fatal(err)
	}
	if gates != 0 {
		t.Errorf("%d wave_gates row(s) for a blocked wave", gates)
	}
}

// Once validate-sources has found every cited source live, the gate opens
// without --force: no source reads as never validated.
func TestGate_LiveCitedSourcesOpenAfterValidateSources(t *testing.T) {
	s := useTestStore(t)
	srv := sourceServer(t)
	ingestFindings(t, 1, srv.URL+"/ok", "["+srv.URL+"/ok/second"+"]("+srv.URL+"/ok/second)")
	wave := 1
	if _, err := s.Sources().AddSource(srv.URL+"/ok/registered", "t", "a", &wave, "", 0); err != nil {
		t.Fatal(err)
	}

	if _, err := execute(t, newValidateSourcesCmd(), "--wave", "1"); err != nil {
		t.Fatalf("validate-sources: %v", err)
	}
	out, err := execute(t, newGuardCmd(), "--wave", "1", "--eval", completeEval)
	if err != nil {
		t.Fatalf("the gate stayed shut over live sources: %v\n%s", err, out)
	}
	var passed int
	if err := s.ReadDB.QueryRow(`SELECT source_check_passed FROM wave_gates WHERE wave=1`).Scan(&passed); err != nil {
		t.Fatal(err)
	}
	if passed != 1 {
		t.Errorf("source_check_passed = %d; want 1", passed)
	}
}

// A URL a finding cites that nobody validated warns, which blocks without
// --force, whether or not someone registered it with `db-write source`.
func TestGate_UnvalidatedCitedSourceWarns(t *testing.T) {
	useTestStore(t)
	cited := []string{"https://example.test/a", "https://example.test/b"}
	ingestFindings(t, 1, strings.Join(cited, ", "))

	out, err := execute(t, newGuardCmd(), "--wave", "1", "--eval", completeEval)
	if err == nil {
		t.Fatalf("the gate opened over unvalidated sources:\n%s", out)
	}
	if want := fmt.Sprintf("%d source(s) in wave 1 were never validated", len(cited)); !strings.Contains(out, want) {
		t.Errorf("guard printed:\n%s\nwant %q", out, want)
	}
}

// A dead URL cited in two waves blocks both, after a validate-sources run over
// all waves. The row keeps one wave at most, so only a lookup by URL finds it
// for each wave that cites it.
func TestGate_DeadSourceCitedInTwoWavesBlocksBoth(t *testing.T) {
	useTestStore(t)
	srv := sourceServer(t)
	dead := srv.URL + "/gone"
	waves := []int{1, 2}
	for _, w := range waves {
		ingestFindings(t, w, dead)
	}

	if _, err := execute(t, newValidateSourcesCmd()); err != nil {
		t.Fatalf("validate-sources: %v", err)
	}
	for _, w := range waves {
		out, err := execute(t, newGuardCmd(), "--wave", fmt.Sprint(w), "--eval", completeEval)
		if err == nil {
			t.Errorf("wave %d opened over a dead cited source:\n%s", w, out)
			continue
		}
		if want := fmt.Sprintf("1 dead source(s) in wave %d", w); !strings.Contains(out, want) {
			t.Errorf("wave %d: guard printed:\n%s\nwant %q", w, out, want)
		}
	}
}

// Each URL gets the verdict of the request it sends, even beside a spelling
// that differs only in its scheme or a trailing slash: an unreachable
// spelling with no scheme does not stand for the dead http:// URL the wave
// cites, and a live /ok does not stand for a dead /ok/, so neither opens the
// gate over a dead source.
func TestValidateSources_EachURLGetsItsOwnRequestsVerdict(t *testing.T) {
	s := useTestStore(t)
	srv := sourceServer(t)
	u := func(p string) string { return srv.URL + p }
	cited := []string{u("/gone"), u("/gone#part"), u("/ok"), u("/ok#part"), u("/ok/")}
	ingestFindings(t, 1, cited)
	schemeless := "//" + strings.TrimPrefix(srv.URL, "http://") + "/gone"
	if _, err := execute(t, newDBWriteCmd(), "source", fmt.Sprintf(`{"url":%q,"agent":"a","wave":1}`, schemeless)); err != nil {
		t.Fatalf("db-write source: %v", err)
	}

	if _, err := execute(t, newValidateSourcesCmd(), "--wave", "1"); err != nil {
		t.Fatalf("validate-sources: %v", err)
	}
	statusOf := func(v string) string {
		var status string
		if err := s.ReadDB.QueryRow(`SELECT validation_status FROM sources WHERE url=?`, v).Scan(&status); err != nil {
			t.Fatalf("no verdict recorded for %s: %v", v, err)
		}
		return status
	}
	wantDead := 0
	for _, c := range cited {
		want := verdictFor(t, c)
		if want == "dead" {
			wantDead++
		}
		if got := statusOf(c); got != want {
			t.Errorf("%s: recorded %s; want %s", c, got, want)
		}
	}
	// No scheme, so no request can be made for it.
	if got := statusOf(schemeless); got != "unreachable" {
		t.Errorf("%s: recorded %s; want unreachable", schemeless, got)
	}

	out, err := execute(t, newGuardCmd(), "--wave", "1", "--eval", completeEval)
	if err == nil {
		t.Fatalf("the gate opened over %d dead cited source(s):\n%s", wantDead, out)
	}
	if want := fmt.Sprintf("%d dead source(s) in wave 1", wantDead); !strings.Contains(out, want) {
		t.Errorf("guard printed:\n%s\nwant %q", out, want)
	}
}

// URLs share a request only when they differ in host case or the fragment,
// and only an http or https URL with a host joins a group.
func TestGroupByRequest(t *testing.T) {
	in := []string{
		"http://Example.test/a#x", "http://example.test/a", // one request
		"https://example.test/a", "http://example.test/a/", "http://example.test/a?q=1",
		"//example.test/a", "example.test/a", "mailto:a@example.test", "http:///a",
	}
	want := [][]string{{in[0], in[1]}}
	for _, u := range in[2:] {
		want = append(want, []string{u})
	}
	for _, g := range want {
		sort.Strings(g)
	}
	sort.Slice(want, func(i, j int) bool { return want[i][0] < want[j][0] })

	if got := groupByRequest(in); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("groupByRequest = %v\nwant %v", got, want)
	}
}

// A source registered for wave 2 after a validate-sources run over all waves
// found it dead counts as one of wave 2's sources, and blocks it: that run
// records no wave, and registering a known URL fills in the wave its row
// lacks.
func TestGate_SourceRegisteredAfterValidationBlocksItsWave(t *testing.T) {
	useTestStore(t)
	srv := sourceServer(t)
	registered, cited := srv.URL+"/gone", srv.URL+"/ok"
	ingestFindings(t, 1, registered)
	if _, err := execute(t, newValidateSourcesCmd()); err != nil {
		t.Fatalf("validate-sources: %v", err)
	}

	ingestFindings(t, 2, cited)
	if _, err := execute(t, newDBWriteCmd(), "source", fmt.Sprintf(`{"url":%q,"agent":"a","wave":2}`, registered)); err != nil {
		t.Fatalf("db-write source: %v", err)
	}
	if _, err := execute(t, newValidateSourcesCmd(), "--wave", "2"); err != nil {
		t.Fatalf("validate-sources --wave 2: %v", err)
	}

	wantDead := 0
	for _, v := range []string{registered, cited} {
		if verdictFor(t, v) == "dead" {
			wantDead++
		}
	}
	out, err := execute(t, newGuardCmd(), "--wave", "2", "--eval", completeEval)
	if err == nil {
		t.Fatalf("wave 2 opened over %d dead source(s):\n%s", wantDead, out)
	}
	if want := fmt.Sprintf("%d dead source(s) in wave 2", wantDead); !strings.Contains(out, want) {
		t.Errorf("guard printed:\n%s\nwant %q", out, want)
	}
}
