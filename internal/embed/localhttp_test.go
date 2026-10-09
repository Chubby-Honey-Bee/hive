package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewLocalHTTPProvider_RequiresBaseURL(t *testing.T) {
	if _, err := NewLocalHTTPProvider("", "model"); err == nil {
		t.Error("expected error for empty baseURL")
	}
}

func TestNewLocalHTTPProvider_DefaultsModel(t *testing.T) {
	p, err := NewLocalHTTPProvider("http://localhost:11434", "")
	if err != nil {
		t.Fatalf("NewLocalHTTPProvider: %v", err)
	}
	if !strings.Contains(p.Name(), "localhttp/") {
		t.Errorf("Name() = %q; want prefix localhttp/", p.Name())
	}
}

func TestLocalHTTPProvider_NameAndDim(t *testing.T) {
	p, err := NewLocalHTTPProvider("http://localhost:11434", "test-model")
	if err != nil {
		t.Fatalf("NewLocalHTTPProvider: %v", err)
	}
	if got := p.Name(); got != "localhttp/test-model" {
		t.Errorf("Name() = %q; want %q", got, "localhttp/test-model")
	}
	if got := p.Dim(); got != 0 {
		t.Errorf("Dim() pre-call = %d; want 0", got)
	}
}

func TestLocalHTTPProvider_Embed_HappyPath(t *testing.T) {
	want := []float32{0.1, 0.2, 0.3, 0.4}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var req localReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(localResp{Embedding: want})
	}))
	defer srv.Close()

	p, _ := NewLocalHTTPProvider(srv.URL, "m")
	got, err := p.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d; want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%g; want %g", i, got[i], want[i])
		}
	}
	if p.Dim() != 4 {
		t.Errorf("Dim() post-call = %d; want 4", p.Dim())
	}
}

func TestLocalHTTPProvider_Embed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p, _ := NewLocalHTTPProvider(srv.URL, "m")
	_, err := p.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	if !strings.Contains(err.Error(), "localhttp 500") {
		t.Errorf("error = %v; expected localhttp 500", err)
	}
}

func TestLocalHTTPProvider_Embed_BodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(localResp{Error: "model not loaded"})
	}))
	defer srv.Close()

	p, _ := NewLocalHTTPProvider(srv.URL, "m")
	_, err := p.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error from body.error")
	}
	if !strings.Contains(err.Error(), "model not loaded") {
		t.Errorf("error = %v; expected to contain 'model not loaded'", err)
	}
}

func TestLocalHTTPProvider_Embed_EmptyEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(localResp{Embedding: nil})
	}))
	defer srv.Close()

	p, _ := NewLocalHTTPProvider(srv.URL, "m")
	_, err := p.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for empty embedding")
	}
}

func TestLocalHTTPProvider_BatchEmbed_Empty(t *testing.T) {
	p, _ := NewLocalHTTPProvider("http://x", "m")
	got, err := p.BatchEmbed(context.Background(), nil)
	if err != nil {
		t.Fatalf("BatchEmbed empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d entries", len(got))
	}
}

func TestLocalHTTPProvider_BatchEmbed_Multiple(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(localResp{Embedding: []float32{1, 2}})
	}))
	defer srv.Close()

	p, _ := NewLocalHTTPProvider(srv.URL, "m")
	got, err := p.BatchEmbed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 results; got %d", len(got))
	}
	for i, v := range got {
		if len(v) != 2 {
			t.Errorf("[%d] len=%d; want 2", i, len(v))
		}
	}
}
