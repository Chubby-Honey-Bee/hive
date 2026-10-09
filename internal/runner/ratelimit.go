package runner

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/anthropics/anthropic-sdk-go"
)

// RateLimitedBackend wraps an LLMBackend with provider-aware retry on
// rate-limit / 429 / Retry-After signals, plus a small in-process
// token bucket so concurrent dispatchers don't punch through provider
// caps in the first place.
//
// Why this exists: Anthropic / OpenAI / Gemini all return HTTP 429 on
// per-key RPM exhaustion. The CLI backends (claude-cli, gemini-cli)
// surface that as a stderr message + non-zero exit. Without retry, a
// single rate-limited node aborts the workflow; with retry but no
// throttle, a 12-wide dispatch fan-out hammers the provider faster
// than tokens replenish. This wrapper does both:
//
//  1. Acquire from a per-provider token bucket BEFORE calling Run.
//     Soft cap is generous by default (60 RPM); tunable via
//     HIVE_RPM_<PROVIDER>.
//  2. Detect rate-limit errors AFTER Run returns. On detection,
//     read Retry-After if available and sleep + retry with
//     decorrelated-jitter exponential backoff (AWS SDK v2 algorithm).
//     Cap of 5 retries; total wall-clock cap of 5 min per node.
//
// The wrapper is provider-agnostic — it decides by the HTTP status an
// error carries, else by rate-limit words in its text ("rate limit",
// "quota exceeded", "Too Many Requests"), never by a number there
// (isRateLimitError), so it works equally for SDK and CLI backends.
// Callers wrap an existing backend via NewRateLimitedBackend.
type RateLimitedBackend struct {
	inner    LLMBackend
	provider string
	bucket   *tokenBucket
	maxRetry int
	maxWall  time.Duration
}

// NewRateLimitedBackend wraps `inner`. Defaults are tuned for typical
// per-key Anthropic + OpenAI tiers (60 RPM ≈ 1 RPS sustained); override
// via HIVE_RPM_ANTHROPIC, HIVE_RPM_OPENAI, HIVE_RPM_GEMINI,
// or the catch-all HIVE_RPM_DEFAULT.
func NewRateLimitedBackend(inner LLMBackend, provider string) *RateLimitedBackend {
	return &RateLimitedBackend{
		inner:    inner,
		provider: provider,
		bucket:   tokenBucketFor(provider),
		maxRetry: 5,
		maxWall:  5 * time.Minute,
	}
}

// Run is the LLMBackend interface method. Acquires a token, calls
// inner.Run, and retries on rate-limit errors with backoff.
//
// On a rate-limit error we refund the token before sleeping. The
// rationale: a 429 means the provider rejected the request without
// having served it, so charging the bucket would double-count
// (our local accounting + the provider's server-side counter).
// Refunding keeps our future budget honest — long backoffs from
// repeated 429s don't burn our long-run RPM allowance.
//
// Non-RL errors do NOT refund — the request reached the provider
// and consumed real quota even if it returned an error (e.g. 4xx
// validation failure still counts against most providers' RPM).
func (r *RateLimitedBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	rt := &rateLimitRetry{
		r:        r,
		req:      req,
		deadline: time.Now().Add(r.maxWall),
		// Tier-aware model fallback chain: if the request's Model is a
		// tier's premium slot, queue its fallback (and free) slot as next-up
		// so persistent rate-limits walk the chain instead of giving up. The
		// chain keeps only models of this backend's own provider family: a
		// tier's fallback is often another provider's model, which this
		// backend cannot serve.
		chain: tierFallbackChain(req.Model, providerFamily(r.provider)),
	}
	for attempt := 0; ; attempt++ {
		if res, done, err := rt.attempt(ctx, attempt); done {
			return res, err
		}
	}
}

// rateLimitRetry is one RateLimitedBackend.Run's state across its attempts.
type rateLimitRetry struct {
	r *RateLimitedBackend
	// req is the request as it stands, its model stepped down the chain.
	req      RunRequest
	deadline time.Time
	chain    []string
	chainIdx int
	// spent holds the attempts refused as rate limits that still came back
	// with calls answered, such as a claude CLI run whose later turn hit a
	// 529. Every return folds them in, so the node is charged for them.
	spent []*RunResult
}

// attempt makes attempt number attempt and, when it was refused as a rate
// limit, waits out the backoff (backOff). done is set when the run ends,
// with its result and error.
func (rt *rateLimitRetry) attempt(ctx context.Context, attempt int) (*RunResult, bool, error) {
	// Token bucket gate. Blocks until a token is available or
	// ctx cancels. The bucket itself never expires tokens; it
	// just paces dispatch so the provider's per-minute counter
	// stays under cap.
	if err := rt.r.bucket.acquire(ctx); err != nil {
		return foldSpent(nil, rt.spent), true, fmt.Errorf("rate-limit gate: %w", err)
	}
	result, err := rt.r.inner.Run(ctx, rt.req)
	if err == nil || !isRateLimitError(err) {
		return foldSpent(result, rt.spent), true, err
	}
	rt.refused(result)
	return rt.backOff(ctx, attempt, err)
}

// refused notes result, an attempt refused as a rate limit: kept to be
// charged when it answered calls, and its token refunded before sleeping.
// We didn't actually get served, so charging the bucket would shrink our
// future budget unnecessarily.
func (rt *rateLimitRetry) refused(result *RunResult) {
	if callsMade(result) > 0 {
		rt.spent = append(rt.spent, result)
	}
	rt.r.bucket.refund()
}

// backOff ends the run, done, when attempt, refused with err, was the last
// the retries or the deadline allow; else it steps down the tier chain and
// waits out the backoff.
func (rt *rateLimitRetry) backOff(ctx context.Context, attempt int, err error) (*RunResult, bool, error) {
	if attempt >= rt.r.maxRetry {
		return foldSpent(nil, rt.spent), true, fmt.Errorf("rate-limit retry exhausted after %d attempts (last model=%s): %w", attempt+1, rt.req.Model, err)
	}
	rt.stepDown(attempt)
	wait := computeBackoff(attempt, err, time.Until(rt.deadline))
	if wait <= 0 {
		return foldSpent(nil, rt.spent), true, fmt.Errorf("rate-limit retry deadline exceeded: %w", err)
	}
	return rt.sleep(ctx, wait)
}

// stepDown sends the next attempt to the next model in the chain, if any,
// after every second retry, so each model gets one backoff before the
// switch. The step shares the one retry budget and still waits the backoff.
func (rt *rateLimitRetry) stepDown(attempt int) {
	if attempt%2 == 1 && rt.chainIdx+1 < len(rt.chain) {
		rt.chainIdx++
		rt.req.Model = rt.chain[rt.chainIdx]
	}
}

// sleep waits wait before the next attempt; the run ends, done, when ctx
// ends first.
func (rt *rateLimitRetry) sleep(ctx context.Context, wait time.Duration) (*RunResult, bool, error) {
	select {
	case <-ctx.Done():
		return foldSpent(nil, rt.spent), true, ctx.Err()
	case <-time.After(wait):
		return nil, false, nil
	}
}

// foldSpent returns last, the last attempt's result, with spent, earlier
// attempts refused as rate limits after calls answered, folded in. Each
// attempt is priced as its own call (ItemResults), and the token counts,
// calls, cut-off calls, tool calls and context notes add up over them. The
// text and the rest are last's, or empty when last is nil.
func foldSpent(last *RunResult, spent []*RunResult) *RunResult {
	if len(spent) == 0 {
		return last
	}
	out := &RunResult{}
	parts := spent
	if last != nil {
		*out = *last
		parts = append(parts, last)
	}
	out.ItemResults = parts
	out.ByModel = nil
	out.InputTokens, out.CachedInputTokens, out.CacheWriteTokens, out.CacheWrite1hTokens, out.OutputTokens = 0, 0, 0, 0, 0
	out.Turns, out.CutOffCalls, out.ToolUses = 0, 0, 0
	out.Invocations, out.ContextNotes = nil, nil
	for _, p := range parts {
		out.InputTokens += p.InputTokens
		out.CachedInputTokens += p.CachedInputTokens
		out.CacheWriteTokens += p.CacheWriteTokens
		out.CacheWrite1hTokens += p.CacheWrite1hTokens
		out.OutputTokens += p.OutputTokens
		out.Turns += callsMade(p)
		out.CutOffCalls += p.CutOffCalls
		out.ToolUses += p.ToolUses
		out.Invocations = append(out.Invocations, p.Invocations...)
		out.ContextNotes = append(out.ContextNotes, p.ContextNotes...)
	}
	return out
}

// tierFallbackChain returns the [premium, fallback, free] chain of the
// models config's tier whose premium slot `model` is, the first in name
// order when several are, keeping only the steps whose models config family
// is `family`. Returns a single-entry slice [model] otherwise. Used by
// RateLimitedBackend to walk down progressively cheaper alternatives on
// persistent 429s.
func tierFallbackChain(model, family string) []string {
	if model == "" {
		return []string{model}
	}
	cfg := models.Load()
	for _, name := range slices.Sorted(maps.Keys(cfg.Tiers)) {
		if t := cfg.Tiers[name]; t.Premium == model {
			return fallbackChain(cfg, t, family)
		}
	}
	return []string{model}
}

// fallbackChain is t's [premium, fallback, free] chain, each step past
// premium kept when it joins the chain (joinsChain).
func fallbackChain(cfg *models.Config, t models.TierSlots, family string) []string {
	out := []string{t.Premium}
	for _, step := range []string{t.Fallback, t.Free} {
		if joinsChain(cfg, out, step, family) {
			out = append(out, step)
		}
	}
	return out
}

// joinsChain reports whether step joins chain, a fallback chain so far: it
// is set, not in the chain yet, and a model cfg lists in family, which is
// not "".
func joinsChain(cfg *models.Config, chain []string, step, family string) bool {
	return step != "" && !slices.Contains(chain, step) && family != "" && cfg.Models[cfg.Resolve(step)].Family == family
}

// providerFamily names the models config family a backend kind serves, or ""
// for a kind it does not know.
func providerFamily(provider string) string {
	switch canonicalKind(provider) {
	case BackendAnthropic, BackendClaudeCLI:
		return "anthropic"
	case BackendOpenAI:
		return "openai"
	case BackendGemini, BackendGeminiCLI:
		return "google"
	}
	return ""
}

// httpStatusError is the error the OpenAI and Gemini backends return for an
// HTTP error status. StatusCode lets isRateLimitError see a 429 or 503 however
// the body is worded, and retryAfter carries the response's Retry-After header.
type httpStatusError struct {
	provider   string
	status     int
	retryAfter time.Duration
	body       string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s HTTP %d: %s", e.provider, e.status, e.body)
}

func (e *httpStatusError) StatusCode() int { return e.status }

// newHTTPStatusError builds the error for a REST backend's error response.
func newHTTPStatusError(provider string, resp *http.Response, body []byte) *httpStatusError {
	return &httpStatusError{
		provider:   provider,
		status:     resp.StatusCode,
		retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		body:       truncate(string(body), 600),
	}
}

// statusError is err carrying status, the HTTP status a CLI's report of a
// failed call names, as the Gemini CLI's JSON error does in its code. Like
// httpStatusError's, StatusCode lets isRateLimitError decide by the status.
type statusError struct {
	err    error
	status int
}

func (e *statusError) Error() string   { return e.err.Error() }
func (e *statusError) Unwrap() error   { return e.err }
func (e *statusError) StatusCode() int { return e.status }

// withStatus is err carrying status (statusError); err as it is when status
// is 0, none.
func withStatus(err error, status int) error {
	if status == 0 {
		return err
	}
	return &statusError{err: err, status: status}
}

// parseRetryAfter reads a Retry-After header value: delay-seconds or an
// HTTP-date. Returns 0 when it is absent, malformed or already past.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return secondsDelay(secs)
	}
	return dateDelay(v)
}

// secondsDelay is a delay of secs seconds; 0 when secs is not positive.
func secondsDelay(secs float64) time.Duration {
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// dateDelay is the time until v, an HTTP-date; 0 when v is not one, or is
// past.
func dateDelay(v string) time.Duration {
	at, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	return max(time.Until(at), 0)
}

// isRateLimitError reports whether err is an LLM provider's rate-limit
// signal: it carries a status a throttled call gets (throttleStatus), as
// the REST backends' errors, the Anthropic SDK's and a Gemini CLI error
// that names a code do, or its text holds a rate-limit word
// (rateLimitMarkers), the only sign the claude CLI gives. A number in the
// text never decides: a turn cap, an output cap or a port that holds 429 or
// 529 is not a rate limit. Nor, by its type, is a reply that is not an
// answer, or a call the context-window guard refused (notThrottled).
func isRateLimitError(err error) bool {
	if err == nil || notThrottled(err) {
		return false
	}
	return throttleStatus(errorStatusCode(err)) || rateLimitMessage(err.Error())
}

// notThrottled reports whether err shows, by its type, that the provider
// was not throttling. A reply that is not an answer came back from the
// provider, so it was not throttled, whatever its text says. Nor was a call
// the context-window guard refused, or a reply it rejected.
func notThrottled(err error) bool {
	var incomplete *incompleteReplyError
	var window *contextWindowError
	return errors.As(err, &incomplete) || errors.As(err, &window)
}

// rateLimitMarkers are the words a rate-limit error's text holds, in lower
// case, on one surface or another. None is a number: a status is read from
// the error that carries it (errorStatusCode), never from its text.
var rateLimitMarkers = []string{
	"rate limit",
	"rate_limit", // anthropic's rate_limit_error
	"ratelimit",
	"too many requests",
	"quota exceeded",
	"resource exhausted",
	"resource_exhausted", // google's status for a 429
	"throttl",            // throttled / throttling
	"overloaded",         // anthropic's overloaded_error, its 529
}

// rateLimitMessage reports whether msg, an error's text, holds one of
// rateLimitMarkers, in any case.
func rateLimitMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return slices.ContainsFunc(rateLimitMarkers, func(n string) bool { return strings.Contains(msg, n) })
}

// errorStatusCode is the HTTP status err carries, 0 when it carries none:
// the OpenAI and Gemini backends' httpStatusError and a CLI's statusError
// have a StatusCode method, and the Anthropic SDK's API error a StatusCode
// field.
func errorStatusCode(err error) int {
	var httpErr interface{ StatusCode() int }
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode()
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// statusOverloaded is the status the Anthropic API answers a call with when
// it is overloaded.
const statusOverloaded = 529

// throttleStatus reports whether code is a status a throttled call gets:
// 429, 503, or 529.
func throttleStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable || code == statusOverloaded
}

// backoffBase / backoffCap are the decorrelated-jitter bounds. Package
// variables (not constants) so the retry tests can run the real algorithm
// at millisecond scale instead of sleeping for seconds.
var (
	backoffBase = 1 * time.Second
	backoffCap  = 30 * time.Second
)

// computeBackoff returns the next sleep interval. If the error carries
// a Retry-After hint (set by the SDK on 429), honour it within the
// remaining deadline. Otherwise use decorrelated jitter:
//
//	delay = min(cap, random(base, prev * 3))
//
// — the AWS SDK algorithm. base = 1s, cap = 30s. Returns 0 when the
// remaining deadline is gone.
func computeBackoff(attempt int, err error, remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return 0
	}
	// Retry-After hint takes precedence.
	if hint := extractRetryAfter(err); hint > 0 {
		return hintedBackoff(hint, remaining)
	}
	return min(jitteredBackoff(attempt), remaining)
}

// hintedBackoff is hint, a Retry-After delay, when it is within remaining,
// the deadline left; 0 when it is not.
func hintedBackoff(hint, remaining time.Duration) time.Duration {
	if hint > remaining {
		return 0
	}
	return hint
}

// jitteredBackoff is the decorrelated-jitter delay after retry attempt:
// base plus a random part of base times 3^attempt, each bounded by the cap.
func jitteredBackoff(attempt int) time.Duration {
	prev := min(time.Duration(math.Pow(3, float64(attempt)))*backoffBase, backoffCap)
	return min(backoffBase+time.Duration(rand.Int64N(int64(prev))), backoffCap)
}

// extractRetryAfter pulls a Retry-After duration from an error: the response
// header the OpenAI and Gemini backends and the Anthropic SDK keep on their
// errors, else a hint in the message ("Retry-After: 12s" or "retry after 12
// seconds"). Returns 0 when not found.
func extractRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}
	if d := headerRetryAfter(err); d > 0 {
		return d
	}
	return messageRetryAfter(strings.ToLower(err.Error()))
}

// headerRetryAfter is the Retry-After header err keeps, from an
// httpStatusError or the Anthropic SDK's API error; 0 when it keeps none.
func headerRetryAfter(err error) time.Duration {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr.retryAfter > 0 {
		return statusErr.retryAfter
	}
	return anthropicRetryAfter(err)
}

// anthropicRetryAfter is the Retry-After header of the response the
// Anthropic SDK's API error in err keeps; 0 when there is none.
func anthropicRetryAfter(err error) time.Duration {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		return parseRetryAfter(apiErr.Response.Header.Get("Retry-After"))
	}
	return 0
}

// retryAfterMarkers open a Retry-After hint in an error's text, in lower
// case.
var retryAfterMarkers = []string{"retry-after:", "retry after ", "retry_after:", "retry-after "}

// messageRetryAfter is the delay a hint in msg, an error's text in lower
// case, gives: the first marker followed by a number gives it. 0 when no
// marker is.
func messageRetryAfter(msg string) time.Duration {
	for _, marker := range retryAfterMarkers {
		if i := strings.Index(msg, marker); i >= 0 {
			if d, ok := retryAfterAt(strings.TrimSpace(msg[i+len(marker):])); ok {
				return d
			}
		}
	}
	return 0
}

// retryAfterAt is the delay tail opens with: a number, digits and an
// optional fraction, of seconds, the default unit, or of milliseconds when
// "ms" follows, as SDKs sometimes emit. ok is false when tail opens with no
// number.
func retryAfterAt(tail string) (time.Duration, bool) {
	end := len(tail) - len(strings.TrimLeft(tail, "0123456789."))
	if end == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(tail[:end], 64)
	if err != nil {
		return 0, false
	}
	if strings.HasPrefix(strings.TrimSpace(tail[end:]), "ms") {
		return time.Duration(v * float64(time.Millisecond)), true
	}
	return time.Duration(v * float64(time.Second)), true
}

// ── Token bucket ────────────────────────────────────────────────────

// tokenBucket is a per-provider RPM throttle. Tokens replenish at
// `rate` per second; up to `burst` tokens accumulate. acquire() blocks
// until a token is available or ctx cancels.
type tokenBucket struct {
	mu       sync.Mutex
	rate     float64 // tokens per second
	burst    float64 // bucket cap
	tokens   float64 // current
	lastFill time.Time
}

func newTokenBucket(rate, burst float64) *tokenBucket {
	return &tokenBucket{
		rate:     rate,
		burst:    burst,
		tokens:   burst,
		lastFill: time.Now(),
	}
}

// refund returns one token to the bucket, capped at burst. Used by
// RateLimitedBackend.Run when the inner backend returns a rate-limit
// error: the request never landed at the provider's quota counter,
// so we shouldn't charge our local one either.
//
// Refunds clamp at burst to preserve the bucket's invariant —
// repeated refunds beyond burst silently no-op, which is what we
// want (a flood of 429s shouldn't somehow give us *more* future
// throughput than we started with).
func (b *tokenBucket) refund() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens = math.Min(b.burst, b.tokens+1.0)
}

// acquire blocks until 1 token is available. Caller passes a context
// for cancellation; nil ctx means wait indefinitely.
func (b *tokenBucket) acquire(ctx context.Context) error {
	for {
		ok, wait := b.take()
		if ok {
			return nil
		}
		if err := sleepOrDone(ctx, wait); err != nil {
			return err
		}
	}
}

// take takes a token when the bucket, refilled for the time since it last
// was, holds one; else it says how long until it will.
func (b *tokenBucket) take() (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens = math.Min(b.burst, b.tokens+elapsed*b.rate)
	b.lastFill = now
	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true, 0
	}
	// Compute wait time for the next token.
	shortBy := 1.0 - b.tokens
	return false, time.Duration(shortBy / b.rate * float64(time.Second))
}

// sleepOrDone sleeps for wait, or until ctx ends, with ctx's error; a nil
// ctx sleeps the whole time.
func sleepOrDone(ctx context.Context, wait time.Duration) error {
	if ctx == nil {
		time.Sleep(wait)
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// tokenBucketFor returns (or constructs) the per-provider bucket. Each
// provider has its own bucket so a slow Anthropic key doesn't throttle
// OpenAI dispatch. Defaults: 60 RPM (1 RPS) sustained, burst of 6.
//
// Override via env: HIVE_RPM_<PROVIDER> (case-insensitive,
// hyphens → underscores). e.g.:
//
//	HIVE_RPM_ANTHROPIC=120
//	HIVE_RPM_CLAUDE_CLI=60
//	HIVE_RPM_DEFAULT=30
func tokenBucketFor(provider string) *tokenBucket {
	bucketsMu.Lock()
	defer bucketsMu.Unlock()
	if b, ok := buckets[provider]; ok {
		return b
	}
	rate := rpmFromEnv(provider) / 60.0
	burst := math.Max(1, math.Min(rate*6, 30)) // 6 seconds of headroom; max 30
	b := newTokenBucket(rate, burst)
	buckets[provider] = b
	return b
}

// rpmFromEnv is provider's requests per minute: HIVE_RPM_<PROVIDER>,
// else HIVE_RPM_DEFAULT, the first that is a positive number, else 60.
func rpmFromEnv(provider string) float64 {
	envKey := "HIVE_RPM_" + strings.ToUpper(strings.ReplaceAll(provider, "-", "_"))
	for _, key := range []string{envKey, "HIVE_RPM_DEFAULT"} {
		if rpm, ok := positiveFloatEnv(key); ok {
			return rpm
		}
	}
	// Conservative default — well under every published Anthropic /
	// OpenAI / Gemini per-key floor as of 2026-05.
	return 60.0
}

// positiveFloatEnv is the environment variable key as a number; ok is false
// when it is unset, or not a positive number.
func positiveFloatEnv(key string) (float64, bool) {
	v, err := strconv.ParseFloat(os.Getenv(key), 64)
	return v, err == nil && v > 0
}

var (
	bucketsMu sync.Mutex
	buckets   = map[string]*tokenBucket{}
)
