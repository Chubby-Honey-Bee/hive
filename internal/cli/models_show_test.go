package cli

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// TestModelsShow_CachedPrice: `chb models show` prints a model's cached
// input price when its entry gives one, and says a cached token costs the
// input price when it gives none.
func TestModelsShow_CachedPrice(t *testing.T) {
	cfg := models.Load()
	ids := make([]string, 0, len(cfg.Models))
	for id := range cfg.Models {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var withCached, without string
	for _, id := range ids {
		switch {
		case cfg.Models[id].CachedInputPerMTokUSD != nil && withCached == "":
			withCached = id
		case cfg.Models[id].CachedInputPerMTokUSD == nil && without == "":
			without = id
		}
	}
	if withCached == "" || without == "" {
		t.Fatalf("precondition: the models config needs a model with a cached price (%q) and one without (%q)", withCached, without)
	}
	for _, c := range []struct{ id, want string }{
		{withCached, fmt.Sprintf("  cached_input_per_mtok_usd: $%g\n", *cfg.Models[withCached].CachedInputPerMTokUSD)},
		{without, "  cached_input_per_mtok_usd: none (a cached token costs the input price)\n"},
	} {
		if out := modelsShow(t, c.id); !strings.Contains(out, c.want) {
			t.Errorf("models show %s lacks %q:\n%s", c.id, c.want, out)
		}
	}
}

// TestModelsShow_CacheWritePrices: `chb models show` prints a model's
// 5-minute and 1-hour cache-write prices when its entry gives them, and
// says what such a write costs when it gives none.
func TestModelsShow_CacheWritePrices(t *testing.T) {
	cfg := models.Load()
	ids := make([]string, 0, len(cfg.Models))
	for id := range cfg.Models {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var with, without string
	for _, id := range ids {
		m := cfg.Models[id]
		switch {
		case m.CacheWritePerMTokUSD != nil && m.CacheWrite1hPerMTokUSD != nil && with == "":
			with = id
		case m.CacheWritePerMTokUSD == nil && m.CacheWrite1hPerMTokUSD == nil && without == "":
			without = id
		}
	}
	if with == "" || without == "" {
		t.Fatalf("precondition: the models config needs a model with cache-write prices (%q) and one without (%q)", with, without)
	}
	for _, c := range []struct {
		id   string
		want []string
	}{
		{with, []string{
			fmt.Sprintf("  cache_write_per_mtok_usd: $%g\n", *cfg.Models[with].CacheWritePerMTokUSD),
			fmt.Sprintf("  cache_write_1h_per_mtok_usd: $%g\n", *cfg.Models[with].CacheWrite1hPerMTokUSD),
		}},
		{without, []string{
			"  cache_write_per_mtok_usd: none (a token written to the cache costs the input price)\n",
			"  cache_write_1h_per_mtok_usd: none (a 1-hour cache write costs the 5-minute write price)\n",
		}},
	} {
		out := modelsShow(t, c.id)
		for _, want := range c.want {
			if !strings.Contains(out, want) {
				t.Errorf("models show %s lacks %q:\n%s", c.id, want, out)
			}
		}
	}
}

// modelsShow runs `chb models show id` and returns what it printed.
func modelsShow(t *testing.T, id string) string {
	t.Helper()
	cmd := newModelsShowCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{id})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("models show %s: %v", id, err)
	}
	return out.String()
}
