package mcp

import (
	"strings"
	"testing"
	"time"
)

// Spawning tools each start a run that calls a paid model, so the server
// rate limits them (MCP: "Servers MUST rate limit tool invocations").
// Read-only tools are untouched.
func TestSpawnLimiter(t *testing.T) {
	l := &spawnLimiter{max: 3, window: time.Minute}
	base := time.Now()
	for i := 0; i < 3; i++ {
		if ok, why := l.allow(base); !ok {
			t.Fatalf("spawn %d refused inside the budget: %s", i+1, why)
		}
	}
	ok, why := l.allow(base)
	if ok {
		t.Fatal("the fourth spawn in one minute should be refused")
	}
	if !strings.Contains(why, "retry in") {
		t.Errorf("a refusal should say when to retry, got %q", why)
	}

	// The window slides: a minute later the budget is clear again.
	if ok, why := l.allow(base.Add(61 * time.Second)); !ok {
		t.Errorf("the window did not slide: %s", why)
	}

	// max <= 0 disables it outright.
	off := &spawnLimiter{max: 0, window: time.Minute}
	for i := 0; i < 50; i++ {
		if ok, _ := off.allow(base); !ok {
			t.Fatal("max=0 must disable the limiter")
		}
	}
}
