package citations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/useragent"
)

// Unpaywall sees the one chb User-Agent, with the command as its role.
func TestVerifySendsTheChbUserAgent(t *testing.T) {
	prev := useragent.Version
	useragent.Version = "0.9.1"
	t.Cleanup(func() { useragent.Version = prev })

	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"is_oa":true}`))
	}))
	defer srv.Close()

	v := &HTTPVerifier{Client: srv.Client(), Email: "a@example.org", Endpoint: srv.URL}
	if _, err := v.Verify(context.Background(), "10.1234/abc"); err != nil {
		t.Fatal(err)
	}
	want := "chb/0.9.1 (+https://github.com/Chubby-Honey-Bee/hive) verify-citations"
	if gotUA != want {
		t.Errorf("User-Agent = %q, want %q", gotUA, want)
	}
}

// A verifier with no contact address sends no request: Unpaywall requires
// one, and HIVE ships none. Its result names the variable that sets one.
func TestHTTPVerifier_NoContactSendsNothing(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	t.Setenv(EmailEnv, "")
	v := NewHTTPVerifier()
	v.Client, v.Endpoint = srv.Client(), srv.URL
	res, err := v.Verify(context.Background(), "10.1111/aaa")
	if err != nil || hits.Load() != 0 || !strings.Contains(res.Error, EmailEnv) {
		t.Errorf("err %v, %d requests, result error %q; want no request and a result naming %s", err, hits.Load(), res.Error, EmailEnv)
	}
}
