# Design-bench tasks

The executability benchmark's tasks (docs/specs/bench-design.md). Each directory holds `task.yaml`, `statement.md`, `tree/` (a Go module), `hidden/` (the tests no model sees), `reference/` (one correct solution) and `note.md` (what a correct design decides). `go.mod` here fences the fixtures off the repository's module. `internal/design.TestShippedTasks` checks that every hidden suite passes on its reference, fails on the unmodified tree, and that twins share a tree and cross-fail.

| Task | Family | Twin | What the statement fixes |
|---|---|---|---|
| `slug-ascii` | contract | `slug-unicode` | `Slugify`: ASCII only, runs of other bytes to one hyphen, a 32-byte cut at a word boundary |
| `slug-unicode` | contract | `slug-ascii` | `Slugify`: letters and digits of any script, an apostrophe rule, no cut |
| `cache-lru` | concurrency | `cache-ttl` | `NewLRU(capacity)`, least-recently-used eviction, concurrency-safe |
| `cache-ttl` | concurrency | `cache-lru` | `NewTTL(ttl)`, an injected `Now`, expiry, `Purge`, concurrency-safe |
| `kv-sections` | parser | `kv-quoted` | `Parse`: `[section]` prefixes, `#`/`;` comments, last duplicate wins, `*ParseError` |
| `kv-quoted` | parser | `kv-sections` | `Parse`: quoted values with four escapes, trailing comments, duplicates refused, no sections |
| `cli-wc` | cli | `cli-head` | `run`: `-l -w -c` counts, a total line, missing files continue with exit 1 |
| `cli-head` | cli | `cli-wc` | `run`: `-n N`/`-c N`, `==> name <==` headers, missing files continue with exit 1 |
| `intervals` | bugfix | — | `Merge`: do not mutate the input, merge touching and contiguous ranges, normalise reversed ones |
| `table` | refactor | — | extract `Widths` and `RenderRow`; `Render` byte-identical to the original |
| `ratelimit` | concurrency | — | a token bucket with an injected clock, `AllowN`, `Tokens`, exact under concurrency |
| `retry` | contract | — | `Do`: `Permanent`, `Exhausted`, `ErrNoAttempts`, waits through `Sleep` |
