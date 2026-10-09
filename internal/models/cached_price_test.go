package models

import "testing"

// A model's cached_input_per_mtok_usd prices its cached input tokens, by
// alias too; a model with none prices them at its input price, a price of 0
// included; a model with no entry reads 0.
func TestPriceCachedInPer1MTokensX10000(t *testing.T) {
	base, err := parseConfig(defaultYAML)
	if err != nil {
		t.Fatal(err)
	}
	user, err := parseConfig([]byte(`
models:
  cached-model: {family: google, input_per_mtok_usd: 2.00, cached_input_per_mtok_usd: 0.20, output_per_mtok_usd: 8.00}
  free-cache: {family: google, input_per_mtok_usd: 2.00, cached_input_per_mtok_usd: 0, output_per_mtok_usd: 8.00}
  plain-model: {family: google, input_per_mtok_usd: 3.00, output_per_mtok_usd: 9.00}
aliases:
  cm: cached-model
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := mergeConfigs(base, user)
	x10000 := func(usd float64) int64 { return int64(usd * 10_000) }
	cases := []struct {
		name string
		want int64
	}{
		{"cached-model", x10000(*user.Models["cached-model"].CachedInputPerMTokUSD)},
		{"cm", x10000(*user.Models["cached-model"].CachedInputPerMTokUSD)},
		{"free-cache", 0},
		{"plain-model", x10000(user.Models["plain-model"].InputPerMTokUSD)},
		{"no-such-model", 0},
	}
	for _, c := range cases {
		if got := cfg.PriceCachedInPer1MTokensX10000(c.name); got != c.want {
			t.Errorf("PriceCachedInPer1MTokensX10000(%q) = %d, want %d", c.name, got, c.want)
		}
	}
	if user.Models["free-cache"].CachedInputPerMTokUSD == nil {
		t.Error("cached_input_per_mtok_usd: 0 parsed as absent, so a free cache would be priced at the input price")
	}
}
