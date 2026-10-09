package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/useragent"
)

// validate-sources sends the one chb User-Agent, on HEAD and on the GET
// retry alike.
func TestCheckURLSendsTheChbUserAgent(t *testing.T) {
	prev := useragent.Version
	useragent.Version = "0.9.1"
	t.Cleanup(func() { useragent.Version = prev })

	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.Header.Get("User-Agent"))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	if status, _ := checkURL(srv.Client(), srv.URL); status != "live" {
		t.Fatalf("status = %q, want live", status)
	}
	ua := "chb/0.9.1 (+https://github.com/Chubby-Honey-Bee/hive) validate-sources"
	want := []string{"HEAD " + ua, "GET " + ua}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("requests = %q, want %q", got, want)
	}
}
