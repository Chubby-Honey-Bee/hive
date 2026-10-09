# Foundations — CDE, MSS, WASP, and the gate

Three interlocking frameworks underpin the hive. Each maps to a code-enforced contract.

- **CDE** — Coleman Dimensional Encoding. Where findings live.
- **MSS** — Minimally Sufficient Statistics. What kind of claim each finding is.
- **WASP** — Workload-Aware Sufficient Placement. How probes stay bounded.

The **gate** is what links them — it runs before any synthesis and rejects work that violates the integrity rules.

## Enforcement map

What each framework is held to in code:

- **WASP** (Workload-Aware Sufficient Placement) — the four
  guarantees: Sufficiency (every true answer is found), Exactness
  (filter agrees with query), Bounded Work (effort scales with
  answer, not dataset), Minimality (every dimension is necessary).
  Enforced in code by: CDE-indexed `findings`, the per-coordinate
  composite index (`idx_cde_d1d2d3d4d5`), the Comb's bounded probes
  for `at`/`diff`, and the Time Wheel ticks that make temporal queries
  bounded. Bounded Work is observed at runtime by the scan detector
  ([`cde-mss.md` § Scan detector](specs/cde-mss.md#requirement-scan-detector));
  its probe-set half, |T(q)| ≤ ∏ numBins, is proved in Lean
  (`lean4/WASP/BoundedWork.lean`), and the candidate-set term rests on the
  index design, which Lean does not model.

- **CDE** (Coleman Dimensional Encoding) — the four phases: workload
  analysis → coordinate encoding → index construction → query
  translation. Enforced by: the `dimensions` table, the d1..d8 columns
  on findings and Comb vantages (d1..d4 on gaps and follow-ups), the
  Comb's region vantage-key encoding, the
  `chb gaps` perspective-as-dimension walk.

- **MSS** (Minimally Sufficient Statistics) — every claim is one of
  Definition / Guarantee / Assumption / Unknown. Enforced by: SQL
  CHECK on `findings.mss_label`, the `mss.PartitionAudit` /
  `IndependenceAudit` checks, the no-laundering rule (a guarantee
  cannot transitively depend on an unknown). The Tetralemma /
  Catuṣkoṭi alignment is direct: A / not-A / both / neither →
  Definition / Guarantee / Assumption / Unknown.

The full framework theory lives at [chronomancy.io](https://chronomancy.io). The formal Lean 4 proofs live in [`lean4/`](../lean4/). This doc is the working summary you need to understand the system.

---

## CDE — coordinate encoding

CDE encodes every finding as a **coordinate** in a project-defined dimensional space. The schema reserves eight slots (`d1..d8`), but most projects only populate the first five — `d6..d8` are intentional headroom for axes a workload discovers later (a new sub-domain, an observer dimension, a temporal phase) without a schema migration.

Queries become **bounded probes** through that space — work scales with the answer, not the dataset (the WASP move).

Each project defines its own dimensions — the roster in [`docs/specs/cde-mss.md`](specs/cde-mss.md#requirement-dimension-roster-convention) is a recommended default, not a constraint. Example from a verification project that chose its own (uses d1..d5; d6..d8 unused):

| Dim | Name | Example values |
|---|---|---|
| d1 | verification_domain | citation-verification, scoring-math, cross-doc-consistency |
| d2 | source_document | investor-report, methodology-whitepaper, analytics-app |
| d3 | claim_type | numeric, citation, methodology, financial, scientific |
| d4 | verdict | confirmed, plausible, unsupported, contradicted |
| d5 | priority | critical, high, medium, low |
| d6–d8 | *reserved* | available for new axes the **framer** forager surfaces |

Every finding lives at a coordinate. Composite indexes on `(d1, d2, d3, d4, d5)` make region lookups cheap.

> **First mention — the comb:** the hive's shared, coordinate-indexed belief surface (the `comb` in code + specs). Every forager reads from it and writes to it; it persists across runs. Full surface in [`docs/specs/comb.md`](specs/comb.md); for here, it's just the cache that makes the next paragraph cheap.

The comb caches a per-region digest so consumers ask *"what does the hive believe at d1=0;d2=3?"* without rescanning the findings table.

> **First mention — the framer forager:** one of the hive's thirteen lenses (in the `default` and `all` presets, not `balanced`), specialized in dimensional-encoding pathologies. Its job is to reason about when the encoding is missing an axis — the signal that the next dimension slot should be claimed. Slots are positional, so the suggester proposes `d<N+1>` for N registered dimensions, and no slot once all eight are taken. It reasons from the question, the comb's coverage, and the axis-suggester's evidence injected as `{cde.axis-candidates}`: which non-coordinate attributes distinguish findings within one coordinate cell. Separately, a runtime scan detector runs `EXPLAIN QUERY PLAN` over two hot read paths and records any that degrade into a full table scan in `scan_events` (`chb wasp-scan-report`). Nothing feeds that log to the framer; it is evidence for the operator.

Formal spec: [`docs/specs/cde-mss.md`](specs/cde-mss.md). Proofs: [`lean4/CDE/`](../lean4/CDE/).

---

## MSS — truth labels

Every finding carries exactly one truth label:

| Label | Meaning |
|---|---|
| **definition** | A choice made, true by stipulation: a price we set, a scope, a threshold, what a term means here. It needs no source and never carries a fact about the world (e.g., "$89 retail price") |
| **assumption** | A bet on something not verified. A fact read from a source you did not verify yourself is an assumption, with the source in `source_urls` (e.g., "$47 unit cost", from the supplier's quote) |
| **guarantee** | Follows from the findings named in `depends_on_ids`: definitions, assumptions or other guarantees. It is only as sure as they are (e.g., "gross margin before shipping is 47% at that price", from the two above) |
| **unknown** | An honest gap. It never supports a guarantee (e.g., "exact outbound shipping rates") |

The store checks the structure, not the meaning. A guarantee must name dependencies that reach no unknown, and an assumption or guarantee written without `source_urls` draws a warning. Nothing checks that a definition is a choice or that a source says what the finding says.

Four integrity rules — enforced at write time and audited by `chb db-read mss_audit`:

1. **Partition** — every finding gets exactly one label. Schema-level `CHECK` plus a runtime audit catches schema drift.
2. **Traceability** — every guarantee is derivable from definitions and assumptions (not from unknowns). A write is refused when any chain of dependencies reaches an unknown, through guarantees and assumptions alike. The audit's BFS re-walks the whole graph at the gate.
3. **Independence** — no assumption is derivable from other assumptions + definitions. Heuristic: same-coordinate assumptions with content-word Jaccard similarity ≥ 0.7 are surfaced as redundancy candidates (warning, not failure).
4. **No laundering** — no unknown is used as if it were a guarantee. Both direct and transitive paths are checked.

The MSS no-laundering audit is a deterministic graph traversal — a write-time check plus a transitive BFS over the dependency graph — so within the implementation it catches every laundering path reachable from the dependency edges. The Lean decidability instances in `lean4/MSS/Decidability.lean` are discharged for the *one-hop* invariants — direct no-laundering, no untraceable guarantees, valid dependency references — and for acyclicity, as real, kernel-checked `Decidable` instances (usable by `by decide`; acyclicity is decided by fuel-bounded reachability, proved complete by shortening any cycle to one with no repeated node). `lean4/MSS/Preservation.lean` proves that a write passing the write checks preserves all four, and that an update preserves direct no-laundering and acyclicity under the premises its comments name. The transitive BFS the audit actually runs is engineering-enforced and test-covered; Lean states no decidability for `NoTransitiveLaundering`, and acyclicity with direct no-laundering does not imply it (guarantee → assumption → unknown passes both), so the audit, not the one-hop checks, is what carries that property.

### Open-access citation policy

Findings cited as guarantees should point at DOIs whose fulltext is freely available. The audit lists guarantees backed only by paywalled or unverified DOIs under `oa_citation_downgrades` as a warning. It never changes a label, and the list does not affect the PASS/FAIL verdict. Run `chb verify-citations` to refresh the cache against Unpaywall.

Formal spec: [`docs/specs/cde-mss.md`](specs/cde-mss.md). Proofs: [`lean4/MSS/`](../lean4/MSS/).

---

## WASP — bounded probes

The acronym is the method:

| Letter | Means | What it asks of the design |
|---|---|---|
| **W** | **Workload-aware** | Analyze the actual workload before defining axes — don't pick dimensions in the abstract |
| **A** | (in *Workload-Aware*) | — |
| **S** | **Sufficient** | Every axis earns its place; the encoding can answer every query the workload poses |
| **P** | **Placement** | Findings are placed *at coordinates*, not scattered; queries can probe regions, not scan tables |

And the four properties the method aims to give you — each stated as a Lean 4 *proof obligation* in [`lean4/WASP/`](../lean4/WASP/):

| Property | Claim | Lean status |
|---|---|---|
| **Sufficiency** | Every true answer is found | proved: W1–W3; `no_false_negatives_multi` puts a matching record's bin vector in the multi-dimensional probe set |
| **Exactness** | Every false positive is filtered | proved: `filter_exact_correct` (W4); `limit_breaks_exactness` (W5) proves a LIMIT can drop a true answer |
| **Bounded work** | Query cost scales with the *answer* size, not the dataset size | proved for the probe set: `probe_set_bound_1d`, `probe_set_bound_multi` (|T(q)| ≤ ∏ numBins); the candidate-set term is the index design, not stated |
| **Minimality** | No dimension is wasted — unused axes are flagged for retirement | not yet stated |

The first three are machine-checked as stated in the table: what is proved is the binning and probe-set algebra, not the SQL engine's behaviour. The composite-index design (below) is what makes the proved probe-set bound the real cost. Minimality is not stated in Lean.

Worked example — probe vs. scan over the verification project's coordinates:

```sql
-- PROBE: bounded work. Uses the composite index on (d1, d2).
-- Cost: O(matching rows).
SELECT * FROM findings WHERE d1 = 0 AND d2 = 3;

-- SCAN: unbounded work. No index serves d6.
-- Cost: O(table size).
SELECT * FROM findings WHERE d6 = 1;
```

The second query isn't wrong, but `d6`–`d8` are reserved headroom with no index, so SQLite reads every row. Nothing records an ad-hoc query like this one. The runtime scan detector checks only two recurring reads, the merge and conflict groupings, and `chb wasp-scan-report` lists the scans it recorded. If a scan like this becomes a recurring read, the slot has earned an index. `chb cde suggest-axis` reports the other signal: a non-coordinate attribute (today `mss_label`) that splits findings within one coordinate cell, a candidate for the next free slot: `d<N+1>` for N registered dimensions, since slots are positional.

Applied to the hive:

- Agents are dispatched to specific coordinates in the question space.
- Findings are encoded at those coordinates.
- A probe on a leading prefix of the coordinates (`d1`, then `d1` and `d2`, and so on) reads a bounded region. Some reads do scan: the merge and conflict-detection groupings (Q5 and Q6 in the spec), and probes that filter only on columns no index leads with — `d6`–`d8`, `convergence_level`, or a coordinate without the ones before it, such as `d2` alone.
- Region probes terminate in `O(matching rows)`, not `O(table size)`, by the composite-index structure. Lean proves the probe-set half (`lean4/WASP/BoundedWork.lean`: |T(q)| is bounded by the query shape); that SQLite reads only matching rows for a probe is the index design, not a theorem.

Formal spec: [`docs/specs/cde-mss.md` § Query workload and index set](specs/cde-mss.md#requirement-query-workload-and-index-set). Proofs: [`lean4/WASP/`](../lean4/WASP/).

---

## The gate

Before synthesizing any wave's results, run the gate:

```bash
chb guard --wave N --eval '{"coverage":4,"depth":4,"sources":4,"actionability":4,"mss_integrity":4,"verdict":"COMPLETE"}'
```

The gate runs these checks, in this order. An error blocks synthesis; a warning blocks it too unless you pass `--force`:

1. **MSS audit** — the full audit over the whole database. Transitive laundering, untraceable guarantees, dependency cycles and partition violations are errors.
2. **Agent completion** — an agent still running is an error, and so is a wave with no agents and no findings. A failed agent warns.
3. **Conflict detection** — the gate runs conflict detection over the wave and records what it finds. With `--auto-resolve-numeric` it resolves the numeric ones. Unresolved conflicts warn.
4. **Source check** — the wave's sources are every URL its findings cite in `source_urls` and every source registered for the wave. A URL that `chb validate-sources` marked dead is an error. A URL it never validated warns. The gate reads what `validate-sources` recorded. It does not run it.
5. **Wave MSS check** — a guarantee in the wave that rests directly on an unknown is an error. Overclaiming warns: five or more findings, with over 80% of them `definition`, or over 80% `guarantee`. That mix suggests facts labelled as choices, or conclusions with no premises behind them. A wave of mostly assumptions or unknowns draws no warning. That is how honest research reads: a fact from a source you did not verify is an assumption, and a gap is an unknown.
6. **Evaluation** — scores (1–5) on coverage, depth, sources, actionability, MSS integrity, plus a verdict. A `NEEDS_MORE_WORK` verdict, or an evaluation that cannot be recorded, is an error. Without `--eval`, as in `chb db-write gate_wave`, the gate reads the wave's latest evaluation and warns when there is none.

When the gate opens, it writes the wave's `wave_gates` row, recording whether the MSS audit, the source check, the conflict check and agent completion passed. It also records a closed `wave-N` tick on the Time Wheel.

If the gate opens, synthesize. There is no export step: read the wave with `chb db-read` or the MCP tools `chb_findings` and `chb_summary`, or render the Comb as Markdown with `chb comb synthesize`.

Useful flags:

```bash
chb guard --wave 1 --auto-resolve-numeric --eval '...'   # auto-resolve numeric-only conflicts
chb guard --wave 1 --force --eval '...'                  # open with warnings (e.g., MSS skew)
```

The gate replaces four pre-synthesis commands (`check-agents`, `detect-conflicts`, `db-write evaluation`, `db-write gate_wave`). Run `chb validate-sources --wave N` before it. It validates the same sources the gate checks, and the gate only reads its results. Each URL gets the verdict of its own request. URLs that differ only in host case or a fragment send the same request, so it makes that request once and records the verdict under each. A URL with no wave on record takes the wave it scanned. Registering such a URL for a wave with `chb db-write source` also gives it that wave. The individual commands still work for workflows that need one check alone.

---

## Lifecycle of a finding

What happens to one finding from the moment a forager emits it to the synthesis that reads it:

1. **Emission.** A forager or agent writes the marker `<!-- FINDING: {"d1":0, "d2":3, "mss_label":"guarantee", "finding":"…", "depends_on_ids":[12,17]} -->` into its output.
2. **Ingestion.** `chb ingest --wave N --agent <name> <output>` parses the marker, attaches `(wave, agent)`, and writes the row through the same path as `chb db-write finding` (`FindingsRepo.AddFinding`).
3. **Write-time validation.** The DB layer checks synchronously: the label is one of the four, every coordinate is within its registered dimension, and a guarantee has non-empty deps that all exist, none of which reaches `unknown` through any chain of dependencies. Violations are refused — the row is not written. A new row cannot close a cycle; `UpdateFinding` validates an edited guarantee's own dependencies, not the findings that depend on it.
4. **Coordinate landing.** On success the row gets an `id` and lands at its coordinate. The composite index over `(d1..d5)` makes it discoverable in the next region probe.
5. **Comb digest.** Nothing is rewritten on the write. `chb comb query` reports the region stale once a finding post-dates its digest, `chb comb refresh` rebuilds digests, and the dreamer's passes read the findings table directly.
6. **Pre-synthesis gate.** When the wave closes, `chb guard --wave N` runs the six gate checks, `mss_audit` among them. The audit re-walks the whole dependency graph. It catches laundering that a later edit left behind, such as a dependency relabelled `unknown` after the guarantee was written, which no write-time check sees. PASS opens the gate; FAIL blocks synthesis with a structured violation list.
7. **Synthesis.** There is no export step. The synthesis — a coordinator, or the queen in a generated swarm — reads the wave through `chb db-read`, the MCP tools `chb_findings` and `chb_summary`, or the Comb tokens in its prompt.

Two findings can co-exist at the same coordinate; the comb stores both and surfaces the conflict in step 6 if their labels disagree.

---

## Cost-dial verification (end-to-end)

The cost dial has four positions: `premium | standard | cheap | free`.

| Mode | planner | synthesist | worker | verifier |
|---|---|---|---|---|
| premium | opus | sonnet | haiku | codex |
| standard (default) | sonnet | haiku | haiku | sonnet |
| cheap | haiku | haiku | haiku | haiku |
| free | gpt-5.5 | gpt-5.2 | gpt-5-mini | gpt-5-mini |

Each cell maps to a literal model id via the tier table in [`internal/models/default-models.yaml`](../internal/models/default-models.yaml). Override at `~/.config/hive/models.yaml`. Inspect with:

```bash
chb models list           # every model: family, $/1M tokens, Copilot multiplier
chb models tiers          # the dial: each role × each mode
chb models show opus      # pricing + tier membership for one model
chb models path           # where the user override file lives
```

Tier resolution is unit-tested: `internal/runner/tiers_test.go` resolves every role in every mode, and `internal/models/config_test.go` checks the embedded tier table. `chb validate` checks only that `chb run-totals` reports a run's cost and tokens. Nothing runs each tier through a workflow and checks the dollars.

---

## Lean 4 bridge

The [`lean4/`](../lean4/) tree states the framework's load-bearing properties as Lean 4 theorems. **95 theorems and 9 instances across 5 libraries, with no `sorry` and no theorem concluding `True`** (2026-10-06, counted in the source; `lean4/Checks.lean` checks the last two in CI whenever `lean4/` changes).

| Module | Covers | Proof status |
|---|---|---|
| **Dialectic** | Gate, convergence, preservation | ✅ 0 `sorry`. D6 `dialectic_preserves_mss`: a sequence of write-checked insertions preserves direct no-laundering, traceability, valid dependencies and acyclicity — not `WellFormed`: an assumption over an unknown, then a guarantee over that assumption, passes every write check and launders transitively (the counterexample is in the file). D7 `synthesis_requires_gate` is a tautology about a Bool and constrains nothing |
| **Fitch** | Natural deduction for guarantee derivations | ✅ fully discharged (0 `sorry`) |
| **MSS** | Invariants, **decidability**, paths, preservation, Fitch bridge | ✅ 0 `sorry`. Direct no-laundering, no-untraceable, deps-valid **and acyclicity** are real `Decidable` instances (`Decidability.lean`, usable by `by decide`); acyclicity is decided by fuel-bounded reachability over the explicit paths of `Paths.lean`, complete because a cycle shortens to one with no repeated node. `Preservation.lean` proves the write checks preserve all four (`write_preserves_no_laundering` needs `DepsValid`, and says why), and the update checks preserve direct no-laundering (given no guarantee depends on a finding relabeled `unknown`, which `UpdateFinding` refuses) and acyclicity (given no new dep reaches the finding). acyclicity and direct no-laundering do not imply transitive no-laundering, so no theorem claims it (guarantee → assumption → unknown is acyclic, has no direct laundering, and launders transitively); `NoTransitiveLaundering` has no decidability instance and is checked by the Go audit's BFS |
| **CDE** | Encoding, indexing, probe correctness | ✅ 0 `sorry`. C3–C5: a fully specified finding is in its own coordinate's bucket, every finding of a complete encoding is in some bucket, and two buckets holding the same finding match on every active dimension |
| **WASP** | Null safety, sufficiency, exactness, bounded work | ✅ 0 `sorry`. W3 `no_false_negatives_multi` over the multi-dimensional probe set (`probeSet` in `Basic.lean`); `probe_set_bound_multi` bounds \|T(q)\| by ∏ numBins; X3 `wasp_needs_cde_nonnull`: an incomplete encoding leaves some finding in no bucket at all; W5, W6 and W8 proved as stated |

The honest summary: **every statement in the tree is kernel-checked.** What that covers: the one-hop MSS invariants and acyclicity are decidable, the write and update checks preserve them under the stated premises, the CDE index is a partition on fully specified coordinates, and WASP's probe sets are sufficient and bounded by the query shape. What it does not cover: the transitive no-laundering walk (no decidability instance is stated; the Go BFS enforces it), the SQL engine's behaviour (bounded work is proved for the probe set; the candidate-set term is the index design), Minimality (not stated), and the faithfulness of the Lean model to the Go code, which is read from the code, not checked: `AddFinding` and `UpdateFinding` check what `WriteCheckPasses` and `UpdateCheckPasses` state — dependencies exist for every label — and `UpdateFinding` refuses the relabel to `unknown` that M8's premise excludes, walking dependents through every label where M8 needs only the direct ones, and runs the cycle check on every label's dependency change, which is M9's premise. A CI job (`.github/workflows/lean.yml`) runs `lake build` and then `lean4/Checks.lean`, which fails on any `sorry` and on any theorem concluding `True`.

Build it yourself: `cd lean4 && lake build && lake env lean Checks.lean` (installs Lean v4.16.0 via `elan` on first run; no mathlib dependency).

Workflow:

```bash
chb lean4-extract --wave 1 > lean4/instance/Wave1.lean      # extract DB state as Lean terms
./lean4/bridge/verify-state.sh workspace/hive.db 1      # full verification pipeline
```

The bridge connects live DB state to the proof layer. `chb validate` §12 checks that `chb lean4-extract` runs and emits an extraction. It does not compile that output against the Lean side, so compatibility with the proofs is not checked. The extraction states the three one-hop invariants and acyclicity as `by decide` obligations.

Everything else (hive quorums, comb ripening, hive signals) is intended to inherit its safety from these proofs, to the extent the Go code matches the Lean model; that match is asserted in the files' comments, not checked.
