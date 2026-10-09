package db

import (
	"database/sql"
	"fmt"
	"sort"
	"testing"
)

func TestAddSource_HappyPath(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Sources().AddSource("https://example.com", "Example", "agent-1", intPtr(1), "primary data", 1)
	if err != nil {
		t.Fatalf("AddSource happy path: %v", err)
	}
}

func TestAddSource_DuplicateURLIgnored(t *testing.T) {
	s := newTestStore(t)

	// A second insert with the same URL succeeds, keeps the first record and
	// returns its id.
	first, err := s.Sources().AddSource("https://dup.example.com", "Title", "agent", intPtr(1), "x", 0)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	again, err := s.Sources().AddSource("https://dup.example.com", "Title2", "agent2", intPtr(2), "y", 1)
	if err != nil {
		t.Fatalf("duplicate insert should not error: %v", err)
	}
	if again != first {
		t.Errorf("the duplicate returned id %d; want the record's, %d", again, first)
	}
	var title, agent string
	if err := s.ReadDB.QueryRow(`SELECT title, agent FROM sources WHERE url='https://dup.example.com'`).Scan(&title, &agent); err != nil {
		t.Fatal(err)
	}
	if title != "Title" || agent != "agent" {
		t.Errorf("duplicate overwrote the record: title=%q agent=%q", title, agent)
	}
}

// Registering a known URL fills a missing wave and keeps one on record. A
// row `chb validate-sources` wrote without --wave has none, so a source
// registered for a wave afterwards takes that wave, and the gate counts it
// as one of that wave's sources.
func TestAddSource_FillsMissingWave(t *testing.T) {
	s := newTestStore(t)
	const validated, registered = "https://a.test/validated-first", "https://a.test/registered-first"
	if _, err := s.WriteDB.Exec(`INSERT INTO sources (url, validation_status) VALUES (?, 'dead')`, validated); err != nil {
		t.Fatal(err)
	}
	first, later := 2, 3
	for _, u := range []string{validated, registered} {
		if _, err := s.Sources().AddSource(u, "", "a", &first, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Sources().AddSource(registered, "", "a", &later, "", 0); err != nil {
		t.Fatal(err)
	}

	for u, want := range map[string]int{validated: first, registered: first} {
		var wave sql.NullInt64
		if err := s.ReadDB.QueryRow(`SELECT wave FROM sources WHERE url=?`, u).Scan(&wave); err != nil {
			t.Fatal(err)
		}
		if !wave.Valid || int(wave.Int64) != want {
			t.Errorf("%s: wave = %v; want %d", u, wave, want)
		}
	}
	var status string
	if err := s.ReadDB.QueryRow(`SELECT validation_status FROM sources WHERE url=?`, validated).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dead" {
		t.Errorf("registration replaced the verdict: %q", status)
	}
}

func TestAddSource_TableDrivenVariants(t *testing.T) {
	cases := []struct {
		name          string
		url           string
		title         string
		agent         string
		wave          *int
		contribution  string
		primarySource int
		wantErr       bool
	}{
		{
			name: "minimal fields",
			url:  "https://a.example.com", title: "", agent: "", wave: nil,
			contribution: "", primarySource: 0, wantErr: false,
		},
		{
			name: "full fields",
			url:  "https://b.example.com", title: "Full title", agent: "researcher",
			wave: intPtr(3), contribution: "background context", primarySource: 1, wantErr: false,
		},
		{
			name: "empty url — SQLite accepts empty string (not NULL)",
			url:  "", title: "t", agent: "a", wave: intPtr(1),
			contribution: "c", primarySource: 0, wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			_, err := s.Sources().AddSource(tc.url, tc.title, tc.agent, tc.wave, tc.contribution, tc.primarySource)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// A wave's sources are the URLs its findings cite and the sources registered
// for it; nothing another wave cites or registers.
func TestWaveURLs_CitedAndRegistered(t *testing.T) {
	s := newTestStore(t)
	cite := map[int]string{
		1: `["https://a.test/one", "https://a.test/two."]`,
		2: "see [three](https://a.test/three) and https://a.test/one",
	}
	register := map[int]string{1: "https://a.test/reg1", 2: "https://a.test/reg2"}
	for w, src := range cite {
		src := src
		if _, err := s.Findings().AddFinding(&Finding{Wave: w, Agent: "a", MSSLabel: "definition", Finding: "x", SourceURLs: &src}); err != nil {
			t.Fatal(err)
		}
	}
	for w, u := range register {
		if _, err := s.Sources().AddSource(u, "", "a", &w, "", 0); err != nil {
			t.Fatal(err)
		}
	}

	want := map[int][]string{}
	for w := range cite {
		want[w] = append(ExtractSourceURLs(cite[w]), register[w])
	}
	one := 1
	got, err := s.Sources().WaveURLs(&one)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(sortedSet(want[1])) {
		t.Errorf("wave 1 sources = %v; want %v", got, sortedSet(want[1]))
	}
	all, err := s.Sources().WaveURLs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if union := sortedSet(append(want[1], want[2]...)); fmt.Sprint(all) != fmt.Sprint(union) {
		t.Errorf("all sources = %v; want %v", all, union)
	}
}

func sortedSet(xs []string) []string {
	set := map[string]bool{}
	for _, x := range xs {
		set[x] = true
	}
	out := make([]string, 0, len(set))
	for x := range set {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

// Citations are read the way agents write them: a JSON array, Markdown links
// or bare URLs, with trailing sentence punctuation trimmed.
func TestExtractSourceURLs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`["https://x.test/b", "see https://x.test/a."]`, []string{"https://x.test/a", "https://x.test/b"}},
		{"[doc](https://x.test/doc?id=2), https://x.test/c;", []string{"https://x.test/c", "https://x.test/doc?id=2"}},
		{"no links here", []string{}},
		{"", nil},
	} {
		if got := ExtractSourceURLs(tc.in); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("ExtractSourceURLs(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestAddSource_ClosedDB_ReturnsError(t *testing.T) {
	// Use a fresh closed DB to simulate an IO failure.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.Close() // close immediately so Exec fails

	repo := NewSourcesRepo(db, db)
	_, err = repo.AddSource("https://closed.example.com", "t", "a", intPtr(1), "c", 0)
	if err == nil {
		t.Fatal("expected error from closed DB, got nil")
	}
}
