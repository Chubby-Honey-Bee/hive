package runner

// Gemini REST backend.
//
// Uses generativelanguage.googleapis.com directly so we don't need the
// google.golang.org/genai dependency. The API surface we touch is:
//
//   POST /v1beta/models/<model>:generateContent   (key in x-goog-api-key)
//
// with a request body of:
//
//   {
//     "contents":          [<part>...],              // user/model turns
//     "tools":             [{functionDeclarations}],  // optional
//     "systemInstruction": {role:system, parts:[...]},// optional
//   }
//
// Each `contents` item is {role, parts:[{text}|{functionCall}|{functionResponse}]}.
// We loop until the response has no functionCall parts (or MaxTurns hits).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// GeminiBackend implements LLMBackend via the Gemini REST API.
type GeminiBackend struct {
	APIKey  string
	BaseURL string // e.g. https://generativelanguage.googleapis.com/v1beta
	Client  *http.Client
	// Timeout, when >0, bounds every HTTP call in place of RunRequest.TTL
	// (HIVE_HTTP_TIMEOUT).
	Timeout time.Duration
}

// geminiBaseURL is the endpoint the Gemini backend sends to: GEMINI_BASE_URL
// without a trailing slash, else Google's own.
func geminiBaseURL() string {
	if base := strings.TrimRight(os.Getenv("GEMINI_BASE_URL"), "/"); base != "" {
		return base
	}
	return "https://generativelanguage.googleapis.com/v1beta"
}

// NewGeminiBackend constructs a GeminiBackend. Like the OpenAI-compatible
// backend, its client has no timeout of its own: each call is bounded by the
// request's TTL or HIVE_HTTP_TIMEOUT.
func NewGeminiBackend(cfg Config) (*GeminiBackend, error) {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		// Some users set GOOGLE_API_KEY instead — accept both.
		key = os.Getenv("GOOGLE_API_KEY")
	}
	if key == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY (or GOOGLE_API_KEY) not set")
	}
	timeout, err := httpTimeoutFromEnv()
	if err != nil {
		return nil, err
	}
	return &GeminiBackend{
		APIKey:  key,
		BaseURL: geminiBaseURL(),
		Client:  &http.Client{},
		Timeout: timeout,
	}, nil
}

// ResolveGeminiModel maps friendly aliases to Gemini model ids. Free-form
// pass-through preserved.
func ResolveGeminiModel(alias string) string {
	switch strings.ToLower(strings.TrimSpace(alias)) {
	case "", "sonnet":
		return "gemini-2.5-pro"
	case "opus":
		// Gemini doesn't expose a stronger SKU than 2.5-pro at this
		// time. We map opus → 2.5-pro so workflows authored for
		// Anthropic still pick the strongest available tier.
		return "gemini-2.5-pro"
	case "haiku":
		return "gemini-2.5-flash"
	}
	return alias
}

// Run drives req's prompt through generateContent, running each function
// call the model makes through the request's registry, until a reply calls
// none or the turns run out.
func (b *GeminiBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if req.Registry == nil {
		return nil, fmt.Errorf("gemini backend requires a tool registry")
	}
	model := ResolveGeminiModel(req.Model)
	maxTurns := requestTurnCap(req.MaxTurns)

	contents := []map[string]any{
		{
			"role":  "user",
			"parts": []map[string]any{{"text": req.Prompt}},
		},
	}
	tools := geminiToolsFromRegistry(req.Registry)

	res := &RunResult{}
	// Every Invoke below records into the registry, which is built fresh per
	// dispatch; the log is copied onto the result at every exit, the error
	// ones included, so a node served by this backend keeps its
	// tool_invocations provenance.
	defer func() { res.Invocations = req.Registry.Invocations() }()
	for turn := 0; turn < maxTurns; turn++ {
		var done bool
		var err error
		contents, done, err = b.turn(ctx, req, model, contents, tools, res)
		if done {
			return res, err
		}
	}
	return res, b.wrapUp(ctx, req, model, contents, tools, res)
}

// wrapUp asks for the answer after the loop's turns ran out with the model
// still calling functions (wrapUpReply). A wrap-up that gives none ends the
// loop out of turns (wrapUpFailed).
func (b *GeminiBackend) wrapUp(ctx context.Context, req RunRequest, model string, contents, tools []map[string]any, res *RunResult) error {
	maxTurns := requestTurnCap(req.MaxTurns)
	answer, stop, err := b.wrapUpReply(ctx, req, model, withGeminiWrapUpText(contents, maxTurns), tools, res)
	if err != nil {
		res.StopReason = "max_turns"
		return wrapUpFailed("gemini", maxTurns, err)
	}
	res.FinalText, res.StopReason, res.WrappedUp = answer, stop, true
	return nil
}

// wrapUpReply makes the wrap-up call of contents, which end with the message
// that no tool turn is left, with function calling set to NONE. It returns
// the answer and its finish reason, held to the checks any final reply is
// (incompleteReply); the error says why it gives none.
func (b *GeminiBackend) wrapUpReply(ctx context.Context, req RunRequest, model string, contents, tools []map[string]any, res *RunResult) (answer, stop string, err error) {
	body := geminiRequestBody(contents, tools, req)
	body["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "NONE"}}
	cand, err := b.call(ctx, req, model, body, res)
	if err != nil {
		return "", "", err
	}
	fnCalls, text := geminiReplyParts(cand.Content.Parts)
	if len(fnCalls) > 0 {
		return "", "", errWrapUpCalledTool
	}
	return text, cand.FinishReason, incompleteReply("gemini", cand.FinishReason, "MAX_TOKENS", text, req.MaxTokens)
}

// withGeminiWrapUpText is contents with wrapUpPrompt added to their last
// turn: after a function call, the user turn that holds the responses.
func withGeminiWrapUpText(contents []map[string]any, maxTurns int) []map[string]any {
	out := append([]map[string]any(nil), contents...)
	last := maps.Clone(out[len(out)-1])
	parts, _ := last["parts"].([]map[string]any)
	last["parts"] = append(append([]map[string]any(nil), parts...), map[string]any{"text": wrapUpPrompt(maxTurns)})
	out[len(out)-1] = last
	return out
}

// requestTurnCap is a request's turn cap, maxTurns, or 30 when it sets none.
func requestTurnCap(maxTurns int) int {
	if maxTurns <= 0 {
		return 30
	}
	return maxTurns
}

// turn makes one call of the conversation contents and charges it to res.
// It returns the conversation with the model's turn and the responses of the
// functions it called. done is set when the conversation ends, with the
// error it ends with: a reply that calls no function is the final one.
func (b *GeminiBackend) turn(ctx context.Context, req RunRequest, model string, contents, tools []map[string]any, res *RunResult) ([]map[string]any, bool, error) {
	cand, err := b.call(ctx, req, model, geminiRequestBody(contents, tools, req), res)
	if err != nil {
		return contents, true, err
	}
	fnCalls, text := geminiReplyParts(cand.Content.Parts)
	if len(fnCalls) == 0 {
		res.FinalText = text
		res.StopReason = cand.FinishReason
		return contents, true, incompleteReply("gemini", cand.FinishReason, "MAX_TOKENS", res.FinalText, req.MaxTokens)
	}
	contents = append(contents, geminiModelTurn(cand.Content.Parts))
	userParts, err := runGeminiCalls(ctx, req, fnCalls, res)
	if err != nil {
		return contents, true, err
	}
	return append(contents, map[string]any{"role": "user", "parts": userParts}), false, nil
}

// geminiRequestBody is one generateContent request: the conversation
// contents, the system prompt and the tools when there are any, and the
// request's generation config when it sets one.
func geminiRequestBody(contents, tools []map[string]any, req RunRequest) map[string]any {
	body := map[string]any{
		"contents": contents,
	}
	if req.System != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": req.System}},
		}
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if genCfg := geminiGenerationConfig(req); genCfg != nil {
		body["generationConfig"] = genCfg
	}
	return body
}

// geminiGenerationConfig is the request's sampling, seed and output cap as a
// generationConfig, nil when it sets none of them.
func geminiGenerationConfig(req RunRequest) map[string]any {
	if !setsGeneration(req) {
		return nil
	}
	genCfg := geminiSampling(req)
	if req.Seed != nil {
		genCfg["seed"] = *req.Seed
	}
	if req.MaxTokens > 0 {
		// Gemini API field name is camelCase maxOutputTokens.
		genCfg["maxOutputTokens"] = req.MaxTokens
	}
	return genCfg
}

// setsGeneration reports whether req sets a sampling option, a seed or an
// output cap.
func setsGeneration(req RunRequest) bool {
	return req.Temperature != nil || req.TopP != nil || req.Seed != nil || req.MaxTokens > 0
}

// geminiSampling is the request's temperature and topP as generationConfig
// fields. Greedy decoding, as --deterministic always has, sets topP 1, unless
// the request sets its own, and a topK of 1, which at any other temperature
// would override it.
func geminiSampling(req RunRequest) map[string]any {
	genCfg := map[string]any{}
	if req.Temperature != nil {
		genCfg["temperature"] = *req.Temperature
	}
	if greedyDecoding(req) {
		genCfg["topP"], genCfg["topK"] = 1.0, 1
	}
	if req.TopP != nil {
		genCfg["topP"] = *req.TopP
	}
	return genCfg
}

// greedyDecoding reports whether req asks for greedy decoding: a temperature
// of 0.
func greedyDecoding(req RunRequest) bool {
	return req.Temperature != nil && *req.Temperature == 0
}

// call sends body to model's generateContent, within an endpoint slot and
// the call's bound, charges its usage to res, and returns its first
// candidate.
func (b *GeminiBackend) call(ctx context.Context, req RunRequest, model string, body map[string]any, res *RunResult) (*geminiCandidate, error) {
	release, err := acquireEndpointSlot(ctx, "gemini", b.BaseURL)
	if err != nil {
		return nil, err
	}
	callCtx, cancel, wrap := callDeadline(ctx, "gemini", req.TTL, b.Timeout)
	// Released by a defer, so a panic in the call frees the slot as it
	// unwinds.
	raw, err := func() (*geminiResponse, error) {
		defer release()
		return b.callGenerateContent(callCtx, model, body)
	}()
	cancel()
	if err != nil {
		return nil, wrap(err)
	}
	chargeGeminiCall(res, raw)
	if len(raw.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: no candidates in response")
	}
	return &raw.Candidates[0], nil
}

// chargeGeminiCall adds an answered call's usage to res, and counts it cut
// off when its first candidate stopped at MAX_TOKENS. promptTokenCount
// includes the cached tokens. A thinking model's thought tokens are billed
// as output, and candidatesTokenCount leaves them out.
func chargeGeminiCall(res *RunResult, raw *geminiResponse) {
	res.Turns++
	res.InputTokens += raw.UsageMetadata.PromptTokenCount
	res.CachedInputTokens += raw.UsageMetadata.CachedContentTokenCount
	res.OutputTokens += raw.UsageMetadata.CandidatesTokenCount + raw.UsageMetadata.ThoughtsTokenCount
	if len(raw.Candidates) > 0 && raw.Candidates[0].FinishReason == "MAX_TOKENS" {
		res.CutOffCalls++
	}
}

// geminiReplyParts walks a candidate's parts once: its function calls, and
// its text.
func geminiReplyParts(parts []geminiPart) ([]geminiFunctionCall, string) {
	var textChunks []string
	var fnCalls []geminiFunctionCall
	for _, p := range parts {
		if p.Text != "" {
			textChunks = append(textChunks, p.Text)
		}
		if p.FunctionCall != nil {
			fnCalls = append(fnCalls, *p.FunctionCall)
		}
	}
	return fnCalls, strings.Join(textChunks, "")
}

// geminiModelTurn is the model's turn to echo back into the conversation,
// every part exactly as received, so each part keeps its thoughtSignature:
// Gemini 3 refuses a function-call turn whose first functionCall part lacks
// the signature it came with (HTTP 400).
func geminiModelTurn(parts []geminiPart) map[string]any {
	assistantParts := make([]json.RawMessage, 0, len(parts))
	for _, p := range parts {
		assistantParts = append(assistantParts, p.raw)
	}
	return map[string]any{"role": "model", "parts": assistantParts}
}

// runGeminiCalls runs each function call through the request's registry and
// returns the function response for each, counting the calls in res. A call
// the registry fails to run is an error.
func runGeminiCalls(ctx context.Context, req RunRequest, fnCalls []geminiFunctionCall, res *RunResult) ([]map[string]any, error) {
	userParts := make([]map[string]any, 0, len(fnCalls))
	for _, fc := range fnCalls {
		res.ToolUses++
		part, err := runGeminiCall(ctx, req, fc)
		if err != nil {
			return nil, err
		}
		userParts = append(userParts, part)
	}
	return userParts, nil
}

// runGeminiCall runs one function call through the request's registry and
// returns its function response: the tool's output, marked an error when
// the tool failed. The output is held to toolResultCap, since this backend
// is given no context window (toolResultBound), and a cut goes to the
// request's run log.
func runGeminiCall(ctx context.Context, req RunRequest, fc geminiFunctionCall) (map[string]any, error) {
	input := fc.Args
	if input == nil {
		input = map[string]any{}
	}
	out, isErr, invErr := req.Registry.Invoke(ctx, fc.Name, input)
	if invErr != nil {
		return nil, fmt.Errorf("tool %s: %w", fc.Name, invErr)
	}
	result, kept := toolResultBound{}.hold(out, req.Registry.restOf(fc.Name, input))
	logToolCut(req.Logf, fc.Name, kept, len(out), false)
	respObj := map[string]any{"output": result}
	if isErr {
		respObj["error"] = true
	}
	return map[string]any{
		"functionResponse": map[string]any{
			"name":     fc.Name,
			"response": respObj,
		},
	}, nil
}

func geminiToolsFromRegistry(r *ToolRegistry) []map[string]any {
	tools := r.NeutralSchemas()
	if len(tools) == 0 {
		return nil
	}
	decls := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.JSONSchema,
		})
	}
	return []map[string]any{{"functionDeclarations": decls}}
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiPart struct {
	Text         string              `json:"text,omitempty"`
	FunctionCall *geminiFunctionCall `json:"functionCall,omitempty"`
	// raw is the part as the API sent it, its thoughtSignature and any
	// field not decoded above included, to echo it back unchanged.
	raw json.RawMessage
}

func (p *geminiPart) UnmarshalJSON(b []byte) error {
	type fields geminiPart
	if err := json.Unmarshal(b, (*fields)(p)); err != nil {
		return err
	}
	p.raw = append(json.RawMessage(nil), b...)
	return nil
}

// geminiCandidate is one candidate reply in a generateContent response.
type geminiCandidate struct {
	Content struct {
		Role  string       `json:"role"`
		Parts []geminiPart `json:"parts"`
	} `json:"content"`
	FinishReason string `json:"finishReason"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount        int64 `json:"promptTokenCount"`
		CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
		CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
		TotalTokenCount         int64 `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (b *GeminiBackend) callGenerateContent(ctx context.Context, model string, body map[string]any) (*geminiResponse, error) {
	req, err := b.generateContentRequest(ctx, model, body)
	if err != nil {
		return nil, err
	}
	var out geminiResponse
	if err := doProviderRequest(b.Client, req, "gemini", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// generateContentRequest is the POST of body to model's generateContent.
// The key travels in a header, never in the URL: Go's transport errors
// quote the full URL, so a `?key=` query string put the secret into error
// strings, run logs and any proxy's access log.
func (b *GeminiBackend) generateContentRequest(ctx context.Context, model string, body map[string]any) (*http.Request, error) {
	js, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini body: %w", err)
	}
	endpoint := fmt.Sprintf("%s/models/%s:generateContent", b.BaseURL, url.PathEscape(model))
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(js))
	if err != nil {
		return nil, fmt.Errorf("gemini http req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", b.APIKey)
	return req, nil
}
