package mcp

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// Spawning tools (chb_agent_run, chb_swarm, chb_self_review,
// chb_self_implement, chb_research) each start a run that calls a paid
// model. A host in a retry loop — or a prompt-injected one — could
// otherwise launch them without bound. MCP's
// tools guidance makes rate limiting a server responsibility ("Servers MUST
// rate limit tool invocations"), and here it is also a spend control.
//
// Read-only tools (summary, findings, run_totals, …) are not limited: they
// cost nothing and a host polling them is the documented usage.
type spawnLimiter struct {
	mu     sync.Mutex
	starts []time.Time
	max    int
	window time.Duration
}

func newSpawnLimiter() *spawnLimiter {
	return &spawnLimiter{
		max:    envIntDefault("HIVE_MCP_MAX_SPAWNS_PER_MIN", 6),
		window: time.Minute,
	}
}

// allow records a spawn attempt and reports whether it may proceed, with the
// reason when it may not.
func (l *spawnLimiter) allow(now time.Time) (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.max <= 0 {
		return true, "" // explicitly disabled
	}
	l.forgetBefore(now.Add(-l.window))
	if len(l.starts) >= l.max {
		return false, l.refusal(now)
	}
	l.starts = append(l.starts, now)
	return true, ""
}

// forgetBefore drops the starts at or before cutoff. Callers hold mu.
func (l *spawnLimiter) forgetBefore(cutoff time.Time) {
	kept := l.starts[:0]
	for _, t := range l.starts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.starts = kept
}

// refusal says why a spawn at now is refused, and when one may start
// again. Callers hold mu.
func (l *spawnLimiter) refusal(now time.Time) string {
	oldest := l.starts[0]
	return fmt.Sprintf(
		"rate limit: %d runs already started in the last minute (retry in %ds, or raise HIVE_MCP_MAX_SPAWNS_PER_MIN). "+
			"Each spawned run calls a paid model.",
		l.max, int(l.window-now.Sub(oldest))/int(time.Second)+1)
}

func envIntDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// limiter lazily builds the spawn rate limiter so a zero-value mcpServer
// (as constructed in tests) still works.
func (s *mcpServer) limiter() *spawnLimiter {
	s.limiterOnce.Do(func() {
		if s.spawnLimit == nil {
			s.spawnLimit = newSpawnLimiter()
		}
	})
	return s.spawnLimit
}
