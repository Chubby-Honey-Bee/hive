# MSS-labeled assumptions

HIVE is built on **CDE / WASP / MSS** from
[chronomancy.io](https://chronomancy.io). MSS demands every claim
carry a label: `definition` (a choice we made), `guarantee`
(provable from definitions + assumptions), `assumption` (a bet that
could be wrong), or `unknown` (an honest gap).

This document catalogs the **assumptions** the system rests on —
the bets we've made that could be wrong, listed honestly so a
reader can see where the cracks would appear if the bet doesn't hold.

## A1 — Nine foragers is the right default breadth

**Bet:** Most users want the `balanced` nine — the seven `minimal`
axis-owners plus Optimist and Historian — on every `chb ask`, rather
than a narrower set.

**What changes if wrong:** Cost per run is higher than necessary, and
someone reaching for the default may be surprised by nine parallel LLM
calls. Mitigation: `--foragers minimal` narrows to the seven axis-owners.
Note that `--foragers default` *widens* to ten (every `default: true`
forager) — it is not the narrow set its name suggests, and there is no
lens-only preset.

**How to verify later:** Once we have telemetry on real usage, compare
runs that kept the default against runs that narrowed it. If most users
narrow, make `minimal` the default.

**Verified 2026-09-16:** `minimal` = 7, `balanced` = 9, `default` = 10,
`all` = 13. `chb ask` and `chb generate` both default to `balanced`.

## A2 — The Copilot multiplier table is correct (snapshot 2026-05-06)

**Bet:** The 27 model-multiplier pairs carried in
[`internal/models/default-models.yaml`](../internal/models/default-models.yaml)
as `copilot_multiplier`, and read through
[`internal/runner/pricing.go::copilotMultiplier`](../internal/runner/pricing.go),
match what GitHub Copilot actually bills. Four of them, for
`claude-opus-5-5`, `claude-opus-5`, `claude-sonnet-5-5` and
`claude-sonnet-5`, are the 1.0 chb gives a model it does not list; they are
not checked against Copilot.

**What changes if wrong:** The credit meter under
`HIVE_BILLING=copilot` reports an inaccurate cost. Decisions made
on the basis of "I have N premium credits left" become unreliable.

**How to verify later:** Compare displayed `credits=X.XX` against the
operator's actual Copilot usage report at month-end. Multiplier drift
gets fixed by editing `default-models.yaml`. Users can fix it without
waiting for a new release in their models override,
`~/.config/hive/models.yaml` (`chb models path` prints where it
lives). An entry there replaces the whole default entry for that model,
so copy the full entry — family, both prices, the cache prices
(`cached_input_per_mtok_usd`, `cache_write_per_mtok_usd`,
`cache_write_1h_per_mtok_usd`) it has, `copilot_multiplier` and
`max_output_tokens` — and change the multiplier. Without the cached price,
a cached token costs the input price; without `cache_write_per_mtok_usd`,
so does a token written to the cache, and without
`cache_write_1h_per_mtok_usd`, a 1-hour write costs the 5-minute price. An
entry with only
`copilot_multiplier` zeroes the model's dollar pricing.

## A3 — `git diff --name-only` is sufficient to derive affected packages for the verifier hook

**Bet:** When the implement-loop's verifier hook runs `go test` on
edited packages, the diff-based package extraction catches every
package whose tests should re-run.

**What changes if wrong:** An agent edits a file the verifier
classifies as not-Go (e.g. a `testdata/` fixture) and the test
re-run misses a regression that would have failed. The agent's
`tests_pass=true` claim slides through the gate.

**How to verify later:** Add a periodic mutation audit on the
implement-loop runs — pick 10 random PRs, mutate one assertion in
the touched code, confirm the verifier flips
`tests_pass`. If <8/10 catch the mutation, the package extractor
needs broadening.

## A4 — Brute-force cosine over `comb_embeddings` is sub-100ms at our scale

**Bet:** Real workspaces (largest observed ~5,500 findings, typical
a few hundred) won't generate enough embeddings to make brute-force
linear search noticeable.

**What changes if wrong:** Hive recall + dreamer prune feels
slow on large workspaces. Mitigation: an HNSW index (`internal/embed/hnsw.go` and
`hnsw_persist.go`) exists in the development history, which the public
repository does not carry, so restoring it means recovering those files
and rewiring the searcher, not a revert.

**How to verify later:** Time `chb recall "<q>"` on the largest
workspace under load. If p95 latency exceeds 100ms — the bet — it is
lost; restore HNSW.

## A5 — `Lean 4` formal-verification side stays compatible

**Bet:** The `chb lean4-extract` command produces Lean 4 terms
that the proof scripts at [`lean4/bridge/`](../lean4/bridge/) can
consume without Lean-side edits.

**What changes if wrong:** `chb lean4-extract` emits terms the Lean
tree will not accept. Partial mitigation: validate §12 runs the
extractor on every push, so a *schema or extractor* break is loud.
Nothing typechecks the emitted instance file against the Lean tree in
CI — `lean.yml` compiles `lean4/` but not a generated instance — so a
Lean-side incompatibility (a renamed constructor, a changed field) is
caught only by running `lean4/bridge/verify-state.sh` by hand.

**How to verify later:** Run `lean4/bridge/verify-state.sh` on a real
workspace after any change under `lean4/MSS/` or to `lean4-extract`. If it
fails to typecheck an instance the Go audit passed, the bet is lost.

## A6 — Sigil glyphs render, through font fallback where needed

**Bet:** A terminal or browser shows each of the fifteen sigils in
`foragers/palette.json` as a glyph, not as `□`: ⌬ 🜔 ✎ Δ ⊹ 𓂀 ☽ ☉ ♃ ♛ ⊨ ♂ ♀
☿ ♄. No one monospace font covers them all, so the bet is on the
system's font fallback, not on the terminal font.

What was checked, on one Mac with fontconfig (`fc-list ':charset=<hex>'`):
Menlo has ✎ Δ ☽ ☉ ♃ ♛ ♂ ♀ ☿ ♄. It lacks ⌬ ⊨ ⊹ 🜔 𓂀. Apple Symbols has
⌬ ⊨ ⊹ 🜔, and Noto Sans Egyptian Hieroglyphs has 𓂀, so on that Mac all
fifteen render, five of them through fallback. Cascadia Code, JetBrains
Mono, SF Mono and the Linux `monospace` fonts were not checked.

**What changes if wrong:** A user's terminal or browser renders one or
more sigils as `□` (tofu / missing-glyph). Mitigation: the sigil is
decorative; the forager's name still renders. A future improvement
could detect tofu and fall back to the default `•`.

**How to verify later:** For each code point (U+232C ⌬, U+1F714 🜔,
U+270E ✎, U+0394 Δ, U+22B9 ⊹, U+13080 𓂀, U+263D ☽, U+2609 ☉, U+2643 ♃,
U+265B ♛, U+22A8 ⊨, U+2642 ♂, U+2640 ♀, U+263F ☿, U+2644 ♄),
`fc-list ':charset=<hex>' family` lists the installed fonts that have it.
Then run `chb list` in each terminal font you care about. Any `□` in the
sigil column loses the bet for that setup.

## A7 — Rate limits can be recognised from error text

**Bet:** Every provider's rate-limit error, through its SDK or CLI, either
carries HTTP 429, 503 or 529 (as a status on the error, or as the `code` of
the gemini CLI's JSON error), or holds one of the words `RateLimitedBackend`
matches (`rate limit`, `rate_limit`, `ratelimit`, `too many requests`,
`quota exceeded`, `resource exhausted`, `resource_exhausted`, `throttl`,
`overloaded`) — and no other error does. A number in an error's text never
decides: the claude CLI reports its errors as text only, so for it the words
are the whole signal.

**What changes if wrong:** A missed rate limit fails the node instead of
backing off; a false match retries a real error for up to five minutes
before failing, and can walk the tier fallback chain for nothing.

**How to verify later:** Collect the error text of every failed node for a
month (`workflow_node_states` + run logs). Any rate-limit error the patterns
missed, or any non-rate-limit error they matched, loses the bet.

## A8 — Half a parallel fan is enough

**Bet:** A `parallel_fan` node whose items at least half succeed carries
enough of the wave that treating it as successful is better than failing it.

**What changes if wrong:** A research wave silently loses up to half its
angles, and synthesis reads the survivors as the whole.

**How to verify later:** For runs with partial fans, compare the synthesis
against a rerun of the failed items. If the missing items change the
verdict in more than one run in ten, raise the threshold.

## A9 — CLI usage priced at API rates is a useful cost signal

**Bet:** Pricing the tokens the `claude` CLI, and a `gemini` CLI with
`--output-format json`, report at the model's API rates gives a cost meter,
and a `--max-cost-usd` cap, that track what a run consumes, although a
subscription, or a gemini-cli login on a Google account's quota, is not
billed per token.

**What changes if wrong:** The cap stops runs early or late relative to the
operator's real limits (subscription quota), and cost comparisons between
CLI and SDK runs mislead.

**How to verify later:** For a week of CLI-backed runs, compare the meter's
per-run cost ordering with the subscription usage the operator sees. If
they rank runs differently, the meter needs a CLI-specific measure.

## A10 — Five minutes is long enough for a dreamer pass

**Bet:** On real workspaces every ripen pass finishes inside its 5-minute
deadline, so the deadline only ever stops a runaway pass.

**What changes if wrong:** A legitimate pass on a large comb fails part-way,
every time, and the ripening it does is never completed.

**How to verify later:** Read `ripen_log` durations on the largest
workspace. A pass over four minutes means the deadline is within reach of
normal work.

## A11 — One-second timestamps are fine for "changed since we last looked"

**Bet:** The dreamer's `reprove` pass measures a dependency's change against
the guarantee's creation or its own last reprove `alarm`, both stored at
SQLite's one-second resolution. The bet is that a dependency never changes
in the same second as the flag that answered its previous change.

**What changes if wrong:** That one change is not flagged, and the guarantee
keeps resting on a dependency that moved under it until the next change.

**How to verify later:** Compare `signals.created_at` against the
`findings.updated_at` of the dependencies they name in a busy workspace. Two
events inside one second means the resolution is within reach of normal work,
and the comparison needs an id or a finer clock.

## A12 — A loop's back edge is the one a depth-first walk finds

**Bet:** Reading a workflow from its start nodes, in sorted order, identifies
the same closing edge for every loop a person would draw the same way — one
entry per loop. Validation refuses a loop closed by anything but a decision,
so a graph this reading gets wrong is refused rather than run oddly.

**What changes if wrong:** A workflow with two entries into one loop is
refused with a message about an edge the author did not think of as the
loop's return.

**How to verify later:** Write a workflow that enters one loop from two
places. If the refusal reads as a puzzle rather than a fix, the engine needs
an explicit loop declaration instead of an inferred one.

## What's **not** an assumption

The following are `definition`s (choices we made — could be different
but are what they are):

- **The 4 MSS labels** — definition/guarantee/assumption/unknown
- **The 4 tier roles** — planner / synthesist / worker / verifier
- **The balanced nine** — the seven `minimal` axis-owners (architect,
  empiricist, pragmatist, scholar, skeptic, steward, timekeeper) plus
  optimist and historian, as `chb ask`'s default
- **The 20 MCP tools** — chosen surface area for the MCP server

The following are `guarantee`s (derivable from definitions +
assumptions; the Lean 4 layer proves a subset):

- **WASP boundedness, for index-served probes** — a probe on the d1–d5
  prefix, wave or label reads O(matching rows), from the index set in
  `docs/specs/cde-mss.md`. Not every probe: d6–d8 filters and the
  merge/conflict passes scan, as that spec records. Lean proves the
  probe-set half (`lean4/WASP/BoundedWork.lean`, `probe_set_bound_multi`:
  |T(q)| ≤ ∏ numBins); that the index design keeps the candidate set to
  the probe set, so SQLite reads only the matching rows, is the design
  assumption, which Lean does not model
- **MSS no-laundering** — guarantees never trace back to unknowns

The following are honest `unknown`s:

- Whether the 13 deliberation lenses are *complete* (does some lens we
  haven't named exist that would catch a class of failures?)
- Whether the verifier hook covers every "agent lies about tests"
  pathway, or just the most common one
- Whether the dreamer's five-pass loop converges on every comb shape,
  or only on the shapes we've tested

## How to add to this list

When you make a bet — a choice that *could* be wrong — write it here
with a `What changes if wrong:` and a `How to verify later:` line.
That's how the hive stays honest about its own foundations.
