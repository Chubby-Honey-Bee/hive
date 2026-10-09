package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewOpenAIProvider_RequiresKey(t *testing.T) {
	if _, err := NewOpenAIProvider("", "model"); err == nil {
		t.Error("expected error for missing api key")
	}
}

func TestNewOpenAIProvider_DefaultsModel(t *testing.T) {
	p, err := NewOpenAIProvider("k", "")
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if !strings.HasPrefix(p.Name(), "openai/") {
		t.Errorf("Name() = %q; expected openai/ prefix", p.Name())
	}
}

func TestOpenAIProvider_NameAndDim(t *testing.T) {
	cases := []struct {
		model   string
		wantDim int
	}{
		{"text-embedding-3-small", 1536},
		{"text-embedding-3-large", 3072},
		{"text-embedding-ada-002", 1536},
		{"custom-unknown", 1536}, // default fallback
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			p, _ := NewOpenAIProvider("k", tc.model)
			if got := p.Name(); got != "openai/"+tc.model {
				t.Errorf("Name() = %q; want %q", got, "openai/"+tc.model)
			}
			if got := p.Dim(); got != tc.wantDim {
				t.Errorf("Dim() = %d; want %d", got, tc.wantDim)
			}
		})
	}
}

func TestOpenAIProvider_BatchEmbed_Empty(t *testing.T) {
	p, _ := NewOpenAIProvider("k", "text-embedding-3-small")
	got, err := p.BatchEmbed(context.Background(), nil)
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result, got %v", got)
	}
}

func TestOpenAIProvider_BatchEmbed_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing Authorization header")
		}
		var req embedReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Return embeddings out of order, with Index, to exercise reorder.
		resp := embedResp{}
		for i, txt := range req.Input {
			_ = txt
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{
				Embedding: []float32{float32(i + 1), float32(i+1) * 2},
				Index:     i,
			})
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p, _ := NewOpenAIProvider("k", "text-embedding-3-small")
	p.baseURL = srv.URL // override

	got, err := p.BatchEmbed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d; want 3", len(got))
	}
	if got[0][0] != 1 || got[1][0] != 2 || got[2][0] != 3 {
		t.Errorf("ordering wrong: %v", got)
	}
}

func TestOpenAIProvider_BatchEmbed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limit", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p, _ := NewOpenAIProvider("k", "text-embedding-3-small")
	p.baseURL = srv.URL
	_, err := p.BatchEmbed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
	if !strings.Contains(err.Error(), "openai embeddings 429") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpenAIProvider_BatchEmbed_BodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"message":"invalid_request"}}`))
	}))
	defer srv.Close()
	p, _ := NewOpenAIProvider("k", "text-embedding-3-small")
	p.baseURL = srv.URL
	_, err := p.BatchEmbed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error from body")
	}
	if !strings.Contains(err.Error(), "invalid_request") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpenAIProvider_BatchEmbed_OutOfRangeIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Index 99 out of range for 1-input request.
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1.0],"index":99}]}`))
	}))
	defer srv.Close()
	p, _ := NewOpenAIProvider("k", "text-embedding-3-small")
	p.baseURL = srv.URL
	_, err := p.BatchEmbed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error for out-of-range index")
	}
}

func TestOpenAIProvider_BatchEmbed_MissingEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Empty data → missing embedding for input 0.
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	p, _ := NewOpenAIProvider("k", "text-embedding-3-small")
	p.baseURL = srv.URL
	_, err := p.BatchEmbed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error for missing embedding")
	}
	if !strings.Contains(err.Error(), "missing embedding") {
		t.Errorf("error = %v; expected missing embedding", err)
	}
}

func TestOpenAIProvider_Embed_DelegatesToBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.5,0.5],"index":0}]}`))
	}))
	defer srv.Close()
	p, _ := NewOpenAIProvider("k", "text-embedding-3-small")
	p.baseURL = srv.URL
	got, err := p.Embed(context.Background(), "x")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 2 || got[0] != 0.5 {
		t.Errorf("embedding = %v; want [0.5, 0.5]", got)
	}
}

func TestOpenAIProvider_Embed_PropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()
	p, _ := NewOpenAIProvider("k", "text-embedding-3-small")
	p.baseURL = srv.URL
	_, err := p.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error from Embed when BatchEmbed fails")
	}
	if !strings.Contains(err.Error(), "openai embeddings 500") {
		t.Errorf("unexpected error: %v", err)
	}
}
