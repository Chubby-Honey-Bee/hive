package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/endpointslot"
)

// OpenAIProvider implements Provider against the OpenAI Embeddings
// API (text-embedding-3-small / -large). Reuses OPENAI_BASE_URL when
// set so the same code targets OpenAI proper, Azure OpenAI, GitHub
// Copilot's openai-compatible endpoint, or vLLM gateways.
type OpenAIProvider struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// dimByModel records the vector dimensionality for the OpenAI
// embedding models we support out of the box. Custom models can be
// passed as `--model`; we trust the API's response shape but fall
// back to the small-model dim for the dim() report.
var dimByModel = map[string]int{
	"text-embedding-3-small": 1536,
	"text-embedding-3-large": 3072,
	"text-embedding-ada-002": 1536,
}

// NewOpenAIProvider returns an OpenAIProvider configured to talk to
// OpenAI (or an OpenAI-compatible endpoint via OPENAI_BASE_URL).
// Returns an error if apiKey is empty.
func NewOpenAIProvider(apiKey, model string) (*OpenAIProvider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("OpenAI provider requires OPENAI_API_KEY")
	}
	if model == "" {
		model = defaultOpenAIModel
	}
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(base, "/"),
		model:   model,
		client:  &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// Name returns the canonical model identifier.
func (p *OpenAIProvider) Name() string { return "openai/" + p.model }

// Dim returns the vector size; defaults to 1536 if the model is not
// in our table (caller can override via the API response).
func (p *OpenAIProvider) Dim() int {
	if d, ok := dimByModel[p.model]; ok {
		return d
	}
	return 1536
}

// Embed delegates to BatchEmbed for the single-input case so all
// transport + retry logic lives in one place.
func (p *OpenAIProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	out, err := p.BatchEmbed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(out) != 1 {
		return nil, fmt.Errorf("OpenAI returned %d vectors for 1 input", len(out))
	}
	return out[0], nil
}

// BatchEmbed posts the inputs as a single request to /embeddings.
// OpenAI accepts arrays up to ~2048 inputs per call; for larger
// batches the caller (Searcher) chunks before invoking us.
type embedReq struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type embedResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// BatchEmbed sends a single POST /embeddings call. Returns errors
// verbatim on non-2xx responses.
func (p *OpenAIProvider) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	req, err := p.newRequest(ctx, texts)
	if err != nil {
		return nil, err
	}
	respBody, err := p.post(ctx, req)
	if err != nil {
		return nil, err
	}
	return vectorsInInputOrder(respBody, len(texts))
}

// newRequest builds the POST /embeddings request for texts.
func (p *OpenAIProvider) newRequest(ctx context.Context, texts []string) (*http.Request, error) {
	body, err := json.Marshal(embedReq{Input: texts, Model: p.model})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// post sends req while holding the endpoint's slot and returns the body of
// a 2xx response; any other status is an error carrying the body verbatim.
func (p *OpenAIProvider) post(ctx context.Context, req *http.Request) ([]byte, error) {
	// Wait for the server's slot, which chb's model calls to it share: with
	// OPENAI_BASE_URL on this machine, a local server gets one call at a time.
	release, n, err := endpointslot.Acquire(ctx, p.baseURL)
	if err != nil {
		return nil, fmt.Errorf("openai embeddings: no free slot at the endpoint (%d at a time, HIVE_MAX_PARALLEL_ENDPOINT): %w", n, err)
	}
	defer release()
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post embeddings: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("openai embeddings %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// vectorsInInputOrder reads an /embeddings response to n inputs: one vector
// per input, in input order.
func vectorsInInputOrder(respBody []byte, n int) ([][]float32, error) {
	parsed, err := decodeEmbedResp(respBody)
	if err != nil {
		return nil, err
	}
	out, err := parsed.byIndex(n)
	if err != nil {
		return nil, err
	}
	if err := missingVector(out); err != nil {
		return nil, err
	}
	return out, nil
}

// decodeEmbedResp parses an /embeddings response; one that carries an error
// is that error.
func decodeEmbedResp(respBody []byte) (*embedResp, error) {
	var parsed embedResp
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("openai error: %s", parsed.Error.Message)
	}
	return &parsed, nil
}

// byIndex places each returned vector at its index among n inputs, since
// OpenAI's docs don't guarantee return order.
func (r *embedResp) byIndex(n int) ([][]float32, error) {
	out := make([][]float32, n)
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= n {
			return nil, fmt.Errorf("response index %d out of range", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}

// missingVector names the first input in out with no vector, nil when
// every input has one.
func missingVector(out [][]float32) error {
	for i, v := range out {
		if v == nil {
			return fmt.Errorf("missing embedding for input %d", i)
		}
	}
	return nil
}
