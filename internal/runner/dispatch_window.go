package runner

// windowKey names a model at the endpoint of one OpenAI-compatible provider
// kind, so one model name served at OPENAI_BASE_URL and at
// HIVE_LOCAL_BASE_URL has a window at each.
type windowKey struct {
	kind  BackendKind
	model string
}

// contextWindow is a model's context window in tokens, 0 when unknown, and
// where it came from.
type contextWindow struct {
	tokens int64
	source string
}

// contextWindowFor is the context window a call on model is held to when
// the OpenAI-compatible backend serves it, as provider openai or local
// (resolveContextWindow), resolved once per model per endpoint per run and
// logged then. The kind of server behind each endpoint is asked once per
// run. It is 0 for any other backend: a hosted API refuses an overflow with
// an error, and the CLIs build their own prompts.
func (rc *runtimeContext) contextWindowFor(kind BackendKind, model string) (int64, string) {
	if !openAICompatible(kind) {
		return 0, ""
	}
	rc.windowMu.Lock()
	defer rc.windowMu.Unlock()
	key := windowKey{kind, model}
	if w, ok := rc.windows[key]; ok {
		return w.tokens, w.source
	}
	b := rc.windowDiscovery(kind)
	w := contextWindow{}
	w.tokens, w.source = resolveContextWindow(rc.ctx, b, rc.servers[kind], model)
	if rc.windows == nil {
		rc.windows = map[windowKey]contextWindow{}
	}
	rc.windows[key] = w
	rc.logContextWindow(kind, model, w)
	return w.tokens, w.source
}

// windowDiscovery is the backend that asks kind's endpoint about its
// models, nil when it cannot be built, built and asked for the kind of
// server there once a run. Callers hold windowMu.
func (rc *runtimeContext) windowDiscovery(kind BackendKind) *OpenAIBackend {
	if rc.discovery == nil {
		rc.discovery, rc.servers = map[BackendKind]*OpenAIBackend{}, map[BackendKind]localServer{}
	}
	b, asked := rc.discovery[kind]
	if asked {
		return b
	}
	if nb, err := newOpenAICompatible(kind, rc.cfg); err == nil {
		b = nb
		rc.servers[kind] = b.findServer(rc.ctx)
		rc.cfg.calibration.serverAt(b.BaseURL, rc.servers[kind])
	}
	rc.discovery[kind] = b
	return b
}

// logContextWindow logs the context window a model at kind's endpoint was
// found to hold, or that it is unknown, and what that means for its calls.
func (rc *runtimeContext) logContextWindow(kind BackendKind, model string, w contextWindow) {
	endpoint := endpointFor(kind)
	switch {
	case w.tokens > 0:
		rc.logf("context window: %s at %s holds %d tokens (%s); a call whose prompt would not fit is refused", model, endpoint, w.tokens, w.source)
	case isLocalNetworkURL(endpoint):
		rc.logf("context window: %s at %s is unknown, so no call to it is held to one, and a reply is failed as cut only when the server counts under one token per %d bytes of prompt. A local server may drop the start of a prompt that does not fit, so set context_window for %s in the models config", model, endpoint, cutBytesPerToken, model)
	default:
		rc.logf("context window: %s at %s is unknown, so no call to it is held to one", model, endpoint)
	}
}
