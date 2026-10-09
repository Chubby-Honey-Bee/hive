package runner

import "context"

// SDKBackend dispatches via the official anthropic-sdk-go client. It wraps
// the long-standing ClaudeRunner implementation unchanged — the interface
// extraction is purely a plumbing refactor at this layer.
type SDKBackend struct {
	APIKey string
}

// Run drives req's prompt through a ClaudeRunner built for it
// (claudeRunnerFor). The Messages API takes no seed, so a request's seed only
// warns.
func (b *SDKBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	cr := b.claudeRunnerFor(req)
	if req.Seed != nil {
		warnAnthropicSeedOnce()
	}
	return cr.Run(ctx, req.Prompt)
}

// claudeRunnerFor is the ClaudeRunner for req: its model, system prompt,
// tools, sampling and run log, and its turn cap, output cap and TTL in place
// of the runner's defaults when it sets them.
func (b *SDKBackend) claudeRunnerFor(req RunRequest) *ClaudeRunner {
	cr := NewClaudeRunner(b.APIKey, req.Model, req.System, req.Registry)
	if req.MaxTurns > 0 {
		cr.MaxTurns = req.MaxTurns
	}
	if req.MaxTokens > 0 {
		cr.MaxTokens = req.MaxTokens
	}
	if req.TTL > 0 {
		cr.PerCallTTL = req.TTL
	}
	cr.Temperature = req.Temperature
	cr.TopP = req.TopP
	cr.Logf = req.Logf
	return cr
}
