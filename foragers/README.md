# HIVE — forager extension guide

Each file in this directory defines one **forager**: a single analytical
lens the hive can run on a question, OR a non-LLM operator on the
comb. Foragers run in parallel where their bonds permit, in series where
they don't, and synthesize through the Queen into a structured
Verdict. `chb ask` runs the `balanced` preset (nine foragers) by
default; `--foragers default` selects the ten `default: true` foragers,
`minimal` the seven axis owners, `all` the whole hive.

## Roster

The repo ships **14 foragers** plus `queen.md`, the synthesizer — 10 default, 4 optional. Seven lenses declare `coverage:` (WASP / CDE / MSS axis ownership) and form the **minimal** preset; the queen declares CDE-Encode. **No shipped forager owns WASP-I**: Editor, which is render-layer only, owns no axis.

| File | Default? | Coverage (WASP / CDE / MSS) | Archetype | Lens / Role |
|---|---|---|---|---|
| `optimist.md`        | ✓ |  | lens    | future-positive — opportunity, upside, best-case |
| `skeptic.md`         | ✓ | **E / — / Unk** | lens | future-negative — risk, failure modes, hidden costs; surfaces what no one can ground |
| `pragmatist.md`      | ✓ | **k_execution / Execute / —** | lens | present-execution — constraints, real tradeoffs; the execution-constraint half of WASP-k |
| `historian.md`       | ✓ |  | lens    | past-context — precedent, why prior attempts succeeded or failed |
| `empiricist.md`      | ✓ | **F / Detect / Def** | lens | evidence-grounding — runs the numbers, demands data; fixes the metric and threshold they are read against |
| `framer.md`  | ✓ |  | lens    | proposes the missing CDE axis when bounded probes degrade into scans |
| `surveyor.md`        | ✓ |  | lens    | computes geodesics through perspective-space; disagreement as distance |
| `timekeeper.md`    | ✓ | **T / — / —** | lens | reads the Time Wheel; identifies belief drift across ticks |
| `scholar.md`     | ✓ | **— / Decompose / Gua** | lens | demands references + logical flow + historical precedence; raises the cited record |
| `dreamer.md`         | ✓ |  | dreamer | the hive's house bee: ripens the comb; runs prune→reprove→contradict→hypothesize→settle |
| `forecaster.md`        |   |  | lens    | second-order effects, downstream ripple |
| `steward.md`         |   | **— / Execute / Asm** | lens | ethics, externalities, who bears the cost; labels what the hive assumes without grounding |
| `architect.md`       |   | **k / — / —** | lens | structure, systems, composition |
| `editor.md`          |   | — (render-layer; in no deliberation preset) | lens | keeps the comb's narratives legible; named explicitly, it runs as an ordinary lens and its verdict lands at `forager:editor`. It does not rewrite region narratives, which stay heuristic |

**The minimal preset** (`--foragers minimal`) selects the 7 deliberation-eligible foragers with a non-empty Coverage column. It covers 4 of 5 WASP axes (k, E, T, F — not I), 3 of 4 CDE phases (Detect, Decompose, Execute — Encode is Queen's), and all 4 MSS labels (Def, Gua, Asm, Unk); `internal/foragers/shipped_roster_test.go` pins this. Use it for design-fork decisions where the cost-latency of the full roster is wrong. See [`docs/specs/swarm.md` § Axis ownership](../docs/specs/swarm.md#requirement-axis-ownership) for the rationale.

## Archetype contract

There are three archetypes:

- `lens` (default) — takes `{question, context}` and emits a verdict
  JSON `{forager, verdict, key_points, evidence, uncertainties,
  recommendation}`. The runner dispatches via the configured LLM
  backend. Every shipped forager except Dreamer and Queen uses this
  archetype.
- `dreamer` — takes the live comb (no question), runs the five
  consolidation passes and emits signals; a region whose digest a
  pass moves reads stale. The
  runner detects `archetype: dreamer` and routes through
  `internal/dreamer.Run` instead of an LLM backend. Cost: $0; pure
  deterministic SQLite work.
- `synthesizer` — Queen (`queen.md`). It reads every lens's verdict
  and writes the swarm's Verdict: one JSON object whose `report` field
  holds the Markdown synthesis. The generator always emits it as the `queen` node, so no
  preset includes it and naming it in `--foragers` is refused.

Any other archetype, or a bond kind other than the three below, makes
the registry skip the file without an error. Run `chb list` after
adding a forager to confirm it loaded.

## Bonds — typed dependencies

Foragers declare relationships to each other through `bonds:`:

```yaml
bonds:
  - to: optimist
    kind: cites          # I read their verdict via {comb.forager:optimist} before answering
  - to: empiricist
    kind: contradicts    # my role is to invert; runner wraps my prompt
  - to: skeptic
    kind: resonates      # convergence with them fires ∇ (a nabla signal + forager_bonds row)
    weight: 1.5          # optional; default 1.0
```

Three bond kinds:

| Kind | Effect |
|---|---|
| `cites`        | Workflow edge upstream → downstream; the dependent forager's prompt has `{comb.forager:<upstream>}` substituted at dispatch. |
| `contradicts`  | Same edge, plus: the dependent forager's prompt is wrapped with an inversion preamble — *"construct the strongest possible inversion of this position."* |
| `resonates`    | No edge constraint. Instead, the runner registers the pair with the **∇ convergence sensor** when the swarm starts. When both foragers' latest verdicts in one swarm run match (neither empty nor `abstain`), the sensor emits a `nabla` signal and writes a `forager_bonds` row, and the Queen notes the convergence in her verdict. ∇ promotes nothing, and convergence never makes a guarantee. A dreamer always abstains, so a bond with one could never fire: swarm generation refuses any bond to or from a dreamer in the swarm. |

Shorthand: `depends_on: [name1, name2]` is sugar for `bonds: [{to: name1, kind: cites}, {to: name2, kind: cites}]`.

Bond-graph cycles (over `cites` + `contradicts`) are rejected at
swarm generation time with the cycle members named. Resonates is
deliberately excluded from cycle detection — it doesn't constrain
dispatch ordering, only registers a runtime correlation.

## Adding your own forager

1. Pick a name — short, lowercase, hyphen-free (e.g. `economist`).
2. Create `foragers/<name>.md`:

```markdown
---
name: economist
title: The Economist
description: Cost-benefit thinking, opportunity cost, who pays vs. who benefits.
default: false              # set true only for hyper-general foragers
archetype: lens             # lens | dreamer | synthesizer
jungian: sage               # optional decorator (magician/sage/shadow/trickster/anima/animus/hero/self)
ifs_role: manager           # optional decorator (manager/firefighter/exile/self)
tags: [economics, costs]
bonds:
  - to: pragmatist
    kind: cites
---

You are **The Economist**, one of the hive's foragers.

## Your lens
<one paragraph defining what *only this forager* sees that the others would miss>

## Working register
<terse, specific, opinionated, willing to disagree with siblings>

## Your output contract
Return JSON exactly:
  {
    "forager": "economist",
    "verdict": "support" | "oppose" | "conditional" | "abstain",
    "key_points": ["…"],
    "evidence": ["…"],
    "uncertainties": ["…"],
    "recommendation": "<one sentence>"
  }
```

3. Run `chb list` — your forager appears with its theme; `chb palette --out foragers/palette.json` refreshes the exported palette.
4. Use it: `chb ask "<question>" --foragers balanced,economist`, or `--foragers all`.

The example above is the short form. The balanced nine and Queen follow **template v1** — `coverage:` / `axis:` declarations, behavioural floors, output schema, length caps — documented key by key in [`docs/forager-template.md`](../docs/forager-template.md); the registry's optional keys are:

| Field | Type | Notes |
|---|---|---|
| `default` | bool | in the `default` preset when true |
| `archetype` | enum | `lens` (default), `dreamer`, or `synthesizer` (queen) |
| `coverage` | map | `wasp` / `cde` / `mss` axis ownership; any non-empty entry plus deliberation eligibility puts the forager in `minimal` |
| `axis` | string | canonical single-axis declaration (template v1) |
| `render_layer` / `deliberation_eligible` | bool | render-only foragers (Editor) are excluded from every preset |
| `sigil` / `accent` | string | one glyph and a `#RRGGBB` for CLI + HTML; the registry defaults to `•` and `#9098A8` when omitted |
| `jungian` / `ifs_role` | string | optional decorators (magician/sage/shadow/trickster/anima/animus/hero/self; manager/firefighter/exile/self) |
| `tags` | list | free-form |
| `bonds` / `depends_on` | list | typed dependencies (above); `depends_on` is sugar for `cites` bonds |

`foragers/palette.json` is the canonical externalised theme; non-Go consumers read it instead of parsing every persona.

## Design principles

- **One lens, narrowly defined.** The forager's value comes from being
  the *only* one looking at its angle. Generic "be helpful" foragers
  add noise; the system would ask the Framer what dimension
  they're missing.
- **Permission to disagree.** Foragers must contradict each other and
  the user's framing. The queen integrates after.
- **Negative-evidence requirement.** Every forager's `uncertainties`
  field is mandatory — what they *don't* know. No confident prose
  hiding gaps.
- **Structured contract.** The JSON shape is identical across every
  lens forager so the queen can compare verdicts mechanically.
- **Bonds are first-class.** Don't write Contrarian without bonding
  it `contradicts` to Optimist — the substitution is what makes the
  contradiction actually responsive instead of generic.

## How they're run

`chb ask "<question>"` is **fully automated by default**:

1. Loads every forager from this directory; runs `Forager.NormalizeArchetype`
   to validate frontmatter.
2. Calls `foragers.ValidateSwarm(swarm)` — rejects bond cycles +
   missing-name references.
3. Generates a workflow YAML where each lens forager becomes one agent
   node, bonds become real workflow edges, dreamer foragers become
   `archetype: dreamer` nodes that route to `internal/dreamer.Run`.
4. Dispatches in-process via the existing `agent-run` command,
   inheriting the runner's mechanisms: `accept:` gates and repair,
   persistence, the cost meter, provider routing.
5. As each lens forager completes, the runner writes its verdict to
   the comb at vantage `forager:<name>`. Downstream bonded foragers
   read it via `{comb.forager:<name>}` substitution before they
   dispatch.
6. When a dreamer is in the swarm (e.g. `--foragers default` or
   `all`; the `balanced` default has none), it runs after the queen
   and ripens the comb (flags near-duplicate assumptions, alarms on
   guarantees with broken foundations, hypothesizes follow-ups for
   long-open gaps). It runs recommend-only; `chb ripen --apply`
   demotes.

After the run, `ask` prints how sure the swarm was before what it
said: the Queen's convergence, the tally with its margin and whether
she wrote a dissent, the ∇ pairs that fired, then the verdict. Those
three lines are all that reaches stdout; the run's narration and
agent-run's JSON trailer go to stderr, and `--json` prints one object
(`verdict`, `recommendation`, `report`, `calibration`, `run`) instead.
Use `--no-dispatch` to print the `chb preflight` and `chb agent-run`
commands instead of running them. The agent-run command is the one
`ask` would run: it passes an empty `--branch`, so no auto-commit
branch is created, and `--db <path>` when you set `--db`.
Use `--human` to insert a checkpoint after the queen.

## Open-access citation policy (applies to every forager)

When a forager cites scholarly work in its verdict — empiricist
demanding evidence, historian invoking precedent, architect quoting
a systems paper, steward referencing an ethics paper — **prefer DOIs
whose fulltext is freely available** (OA journals, preprint servers,
PMC, Zenodo, government reports).

Nothing re-labels a verdict whose only sources are paywalled — hedge it
yourself. `chb verify-citations` checks DOIs against Unpaywall when run
(nothing runs it automatically), and the MSS audit then lists
paywalled-only guarantees as a warning without changing them. Cite DOIs in the URL form (`https://doi.org/10.NNNN/...`) so
the Unpaywall verifier can confirm OA status. Only cite a DOI you have
actually seen resolve — never construct a `doi.org` URL to satisfy the
OA preference or to pass the verifier. If a key reference is
only paywalled and no green-OA copy exists, flag a gap requesting a
replacement and label your claim `assumption`, not `guarantee`.
