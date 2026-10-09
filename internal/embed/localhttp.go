package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/endpointslot"
)

// LocalHTTPProvider talks to an Ollama-compatible HTTP endpoint:
//
//	POST <base>/api/embeddings
//	{ "model": "<model>", "prompt": "<text>" }
//	→
//	{ "embedding": [...] }
//
// One round-trip per input — Ollama doesn't batch as of writing —
// but BatchEmbed parallelises the calls with a small worker pool to
// keep wall-clock latency reasonable for ≥100 inputs.
type LocalHTTPProvider struct {
	baseURL string
	model   string
	client  *http.Client
	dim     atomic.Int64 // discovered on first call; 0 until then
}

// NewLocalHTTPProvider configures a LocalHTTPProvider. Empty baseURL
// is rejected — callers must point us at a running endpoint.
func NewLocalHTTPProvider(baseURL, model string) (*LocalHTTPProvider, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("LocalHTTP provider requires HIVE_LOCAL_EMBED_URL")
	}
	if model == "" {
		model = defaultLocalModel
	}
	return &LocalHTTPProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// Name returns the canonical model identifier.
func (p *LocalHTTPProvider) Name() string { return "localhttp/" + p.model }

// Dim returns the discovered dimension (0 until first call). Tests
// that need a known dim must call Embed once first.
func (p *LocalHTTPProvider) Dim() int { return int(p.dim.Load()) }

type localReq struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type localResp struct {
	Embedding []float32 `json:"embedding"`
	Error     string    `json:"error,omitempty"`
}

// Embed performs a single-text round-trip. Records the dimension on
// first success so callers + Searcher can sanity-check downstream.
func (p *LocalHTTPProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	req, err := p.newRequest(ctx, text)
	if err != nil {
		return nil, err
	}
	respBody, err := p.post(ctx, req)
	if err != nil {
		return nil, err
	}
	embedding, err := decodeLocalResp(respBody)
	if err != nil {
		return nil, err
	}
	p.dim.CompareAndSwap(0, int64(len(embedding)))
	return embedding, nil
}

// newRequest builds the POST /api/embeddings request for text.
func (p *LocalHTTPProvider) newRequest(ctx context.Context, text string) (*http.Request, error) {
	body, err := json.Marshal(localReq{Model: p.model, Prompt: text})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		p.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// post sends req while holding the endpoint's slot and returns the body of
// a 2xx response; any other status is an error carrying the body verbatim.
func (p *LocalHTTPProvider) post(ctx context.Context, req *http.Request) ([]byte, error) {
	// Wait for the server's slot, which chb's model calls to it share, so a
	// local server that answers one request at a time gets one.
	release, n, err := endpointslot.Acquire(ctx, p.baseURL)
	if err != nil {
		return nil, fmt.Errorf("localhttp: no free slot at the embedding endpoint (%d at a time, HIVE_MAX_PARALLEL_ENDPOINT): %w", n, err)
	}
	defer release()
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("localhttp %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// decodeLocalResp is the embedding a response carries. A response with an
// error, or with an empty embedding, is an error.
func decodeLocalResp(respBody []byte) ([]float32, error) {
	var parsed localResp
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("localhttp: %s", parsed.Error)
	}
	if len(parsed.Embedding) == 0 {
		return nil, fmt.Errorf("localhttp returned empty embedding")
	}
	return parsed.Embedding, nil
}

// batchPoolSize is BatchEmbed's worker pool.
const batchPoolSize = 4

// BatchEmbed runs Embed in parallel with a small worker pool. Pool size is
// hardcoded to 4 — local model servers don't tolerate many concurrent
// requests well, and 4 serves batches up to ~100. Each Embed waits for the
// endpoint's slot, so fewer than 4 are in flight when the slot bounds them:
// one at a time on this machine by default.
func (p *LocalHTTPProvider) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	if len(texts) == 0 {
		return out, nil
	}
	// Cancellable context so the first worker to error can both stop its
	// siblings' in-flight Embed calls and unblock the producer below.
	// Without this, an early worker error abandons `jobs`; once the
	// buffered channel fills, the producer's send blocks forever and the
	// caller deadlocks (close(jobs) / <-done are never reached).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &embedBatch{
		jobs: make(chan embedJob, batchPoolSize),
		errs: make(chan error, len(texts)),
		done: make(chan struct{}, batchPoolSize),
		out:  out,
	}
	for w := 0; w < batchPoolSize; w++ {
		go b.work(ctx, cancel, p)
	}
	b.feed(ctx, texts)
	b.wait()
	if err := b.firstError(); err != nil {
		return nil, err
	}
	return out, nil
}

// embedJob is one text for a worker to embed, and its index.
type embedJob struct {
	i    int
	text string
}

// embedBatch is one BatchEmbed call's pool: the jobs fed to it, the errors
// and stops its workers report, and the vectors they fill in.
type embedBatch struct {
	jobs chan embedJob
	errs chan error
	done chan struct{}
	out  [][]float32
}

// work embeds jobs until the channel closes or an Embed fails. A failure
// is reported and cancels ctx, which stops the siblings' in-flight calls
// and the producer.
func (b *embedBatch) work(ctx context.Context, cancel context.CancelFunc, p *LocalHTTPProvider) {
	defer func() { b.done <- struct{}{} }()
	for j := range b.jobs {
		v, err := p.Embed(ctx, j.text)
		if err != nil {
			b.errs <- err
			cancel()
			return
		}
		b.out[j.i] = v
	}
}

// feed sends one job per text, then closes the jobs channel. Once ctx is
// cancelled a worker failed, so it stops feeding; workers still ranging
// drain the closed channel and exit cleanly.
func (b *embedBatch) feed(ctx context.Context, texts []string) {
	defer close(b.jobs)
	for i, t := range texts {
		select {
		case b.jobs <- embedJob{i: i, text: t}:
		case <-ctx.Done():
			return
		}
	}
}

// wait blocks until every worker has stopped, then closes errs.
func (b *embedBatch) wait() {
	for w := 0; w < batchPoolSize; w++ {
		<-b.done
	}
	close(b.errs)
}

// firstError is the first error a worker reported, nil when none did.
func (b *embedBatch) firstError() error {
	for err := range b.errs {
		if err != nil {
			return err
		}
	}
	return nil
}
