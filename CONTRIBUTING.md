# Contributing to HIVE

Thank you for your interest in contributing! **HIVE**
is a superorganism: each forager is one analytical lens, the comb is
their shared belief surface, and the colony knows more than any one
forager. The system is built on three formal frameworks
(CDE / WASP / MSS), and `docs/specs/` is its contract: a change that
alters behaviour updates the spec in the same commit.

## Getting started

```bash
git clone https://github.com/Chubby-Honey-Bee/hive.git
cd hive

# Build both binaries
go build -o chb       ./cmd/chb
go build -o chb-mcp   ./cmd/chb-mcp

# Sanity-check
./chb --version
./chb list
```

## Repository layout

```
hive/
  cmd/chb/           `chb`'s main, a thin one over internal/cli
  cmd/chb-mcp/       `chb-mcp`'s main, a thin one over internal/mcp
  assets.go          the carried foragers/, agents/ and workflows/
  internal/cli/      `chb` — the CLI (53 subcommands; `chb --help`)
  internal/mcp/      `chb-mcp` — stdio JSON-RPC 2.0 MCP server (20 tools)
  internal/          the other Go packages: db, mss, cde, hive, workflow,
                     runner, citations, foragers, comb, dreamer, embed,
                     review, gate, artifact, bench, design, harness,
                     calibration, models, schema, endpointslot, useragent
  agents/            Specialist agent prompt templates (researcher,
                     analyst, evaluator, coder, verifier, …)
  foragers/          13 lenses, the editor and the queen — markdown
                     frontmatter + persona body. Drop a file to add one.
  workflows/         Graph-based workflow definitions (YAML)
  lean4/             Lean 4 proofs — 95 theorems and 9 instances across
                     Fitch, MSS, CDE, WASP, Dialectic; all five libraries
                     compile with no `sorry` (2026-10-06; see
                     docs/foundations.md § Lean 4 bridge)
  workspace/         Per-project SQLite databases (gitignored)
  fixtures/          CLI behaviour fixtures (`chb replay-behavior`), the
                     agent-harness, bench and design suites
  scripts/           build-binaries.sh, build-image.sh, chb-mcp-docker.sh
  docs/              Reference docs linked from the README
  docs/specs/        The specs, one per subsystem (the contract)
```

| Package | Responsibility |
|---|---|
| `internal/db` | SQLite schema (CREATE-only, no migrations), repos for findings/conflicts/gaps/comb/embeddings; scan detector |
| `internal/mss` | MSS partition / independence / no-laundering audits |
| `internal/cde` | Coleman Dimensional Encoding helpers (coordinate keys, probes, axis suggester) |
| `internal/foragers` | Forager registry — frontmatter parser, presets, theme, palette export, swarm YAML generator |
| `internal/comb` | Region builder, vantage resolver, ∇ convergence sensor, event bus |
| `internal/dreamer` | Five-pass comb ripening (prune → reprove → contradict → hypothesize → settle) |
| `internal/runner` | Workflow dispatch, backends, cost meter, accept gates, tier resolution |
| `internal/workflow` | Graph engine: nodes, edges, join semantics, accept predicates, `SafeEval` |
| `internal/gate` | Merge, conflict detection, the gate pipeline |
| `internal/citations` | DOI extractor + Unpaywall verifier |
| `internal/review` | Self-review aggregator + render + implement-workflow generator |
| `internal/embed` | Pure-Go embeddings (brute-force cosine over `comb_embeddings`) |
| `internal/harness` | `chb agent-harness` — the shipped prompts run through a provider and held to their contracts: the swarm, template, proof, hive, bench and design cases |

The runtime ships as one container image with both binaries, `chb-mcp` as its entrypoint and `chb` beside it. They share the same SQLite store, and neither listens on a port: `chb-mcp` speaks over stdio, and the runs its tools start are `chb` processes.

## Local quality gates

Two speeds. While iterating:

```bash
make fast      # ~30 s: go vet, gofmt drift, unit tests, 21 replay fixtures
```

Before pushing, the same set CI's `gate` job runs (`.github/workflows/ci.yml`):

```bash
make gate      # go vet, gofmt drift, race tests, chb validate, replay (21), mcp-smoke (15 checks over 20 tools), preflight × 11
```

Both build `chb` and `chb-mcp` into `~/bin` first (override with `CHB_BIN=` /
`MCP_BIN=`). The binaries carry `foragers/`, `agents/` and `workflows/`; run
from the repository root they read the checkout's copies instead, which is
what the gate is checking.

Both are provider-free: on a machine with no API key and no `claude` CLI the
harnesses pin a placeholder key so preflight's provider check is deterministic
(`internal/cli/harness_env.go`). `make lean` compiles the proof tree when `lake` is
installed; CI runs it whenever lean4/ changes.

To exercise the prompts themselves — every forager persona, the agent
templates, the proof workflow — against a real model:

```bash
make harness   # chb agent-harness: local `claude` CLI, contract-checked, report in workspace/agent-harness/
```

Its default provider is the local `claude` CLI, which uses that CLI's own
login (no API key), and it asserts contracts rather than text.
`--provider` runs it on another provider instead (`openai` covers Ollama and
other OpenAI-compatible servers through `OPENAI_BASE_URL`), and `--lens-model`
and `--queen-model` pin the models. Template cases drive `claude -p`, so they
run only on the default `claude-cli` provider. Its checks come in two classes,
and the split is deliberate:

- **Contract** checks fail the case. They are deterministic given correct code:
  the artifact verifies, ∇ rows are recorded for every converging bonded pair,
  the run completes with no failed node, the MSS audit passes, the verdict
  carries its required keys and sits in its enum, a graded case's answer
  matches the measured one, and the run left its private tree unchanged.
- **Adherence** checks (⚠) report a rate instead: forbidden phrases, length
  caps, bare-JSON emission. Whether a model at a given tier honours a style rule
  varies between runs of a perfectly good system, so one slip is evidence about
  the tier, not about the code. Gating on it makes the suite flaky, and a flaky
  suite is one nobody reads — which costs you the day a real regression turns it
  red. Read the rate in `REPORT.md` and watch it drift.

Every swarm case runs in a private copy of `foragers/` and `agents/` under its
workspace, and the in-process file tools refuse paths outside it. The harness
hashes every file in the copy before and after the run and fails the case if
anything changed, naming the paths. A graded or bench case also reports
whether any tracked file in the checkout changed while it ran. That one
compares the checkout before and after, so an edit you make during the run
counts as well; it reports for that reason rather than failing, and `--strict`
gates it.

`--strict` makes adherence and environment checks fail the case too: use it
when tuning personas or as a release gate. Add a case to `fixtures/agent-harness/suite.yaml` when you add a
persona or template.

One swarm case is **graded**: it asks the swarm a question this repository answers
for itself — whether the `minimal` preset leaves a `resonates` pair
half-dispatched — and the harness recomputes that answer from the shipped
personas on every run before comparing it with the queen's verdict. So the
suite measures an answer, not only a shape, and the expected verdict follows
the roster: close the gap and the grade flips with it, with no fixture to
update. To add one, write the computation in `internal/harness/grade.go`
and name it in the case's `grade:` field. That case checks one verdict, whose
computed answer today is "support", so a model that always says "support"
passes it. Every swarm case runs in a private copy of `foragers/` and
`agents/`, and a change inside that copy fails the case. A graded case also
reports whether the run changed any tracked file in the repository, since its
foragers hold Read, Grep and Bash.

The `hive` kind grades a live hive run on the database it leaves, not on its
text. Its one case, `hive-seeded-gap`, seeds a critical gap and runs
`workflows/hive.yaml` against it. It passes when a hive agent's finding
resolves the gap, that finding states the page size a new database gets from
the SQLite build `chb` links, the next plan does not dispatch for the gap again,
and the MSS audit passes. It is marked `slow` and costs about $1.25 a run, so
it runs only with `--slow` (`make harness HARNESS_ARGS=--slow`).

To compare models, run the bench suite instead
(`chb agent-harness --suite fixtures/bench/suite.yaml`; see
`docs/specs/bench.md`). It asks questions the roster answers, each paired
with an edit that flips the answer, so a model that always gives one verdict
scores 0 on every pair. Each item runs as a swarm and as one call with no
persona, in a temporary directory outside the workspace, so the run's
working directory and its parent hold neither the item's ID nor earlier
results. Every run lands in `results.jsonl`, and
`chb bench decide --rule fixtures/bench/rule.yaml` applies the pre-registered
rule across configurations. `go test` checks the bench against a fake OpenAI-compatible
server, with no model and no network. The live CLI smoke test in `go test`
stays opt-in (`HIVE_RUN_CLI_SMOKE=1`) because it makes a model call.

## Specs

`docs/specs/` is the contract, one file per subsystem:

- `swarm.md` — the forager swarm: persona discovery, the roster, `chb ask` and the Queen
- `hive.md` — autonomous mode: its signals, its dispatch plan and when it stops
- `comb.md` — the comb: vantages, revisions, the Time Wheel, embeddings and the Dreamer
- `runner.md` — the runner: backends and tiers, tools, accept gates and repair
- `workflow.md` — the workflow engine: the YAML format, graph validation, readiness
- `mcp.md` — the MCP server and its tools
- `cde-mss.md` — the knowledge store: CDE coordinates, MSS labels, the audit, calibration
- `bench.md` — the graded twin bench
- `bench-design.md` — the executability benchmark

A change that alters behaviour updates the spec in the same commit. A spec
states the present shape and carries no history.

## Adding a new forager

HIVE is extensible by file. To add a forager:

1. Drop a markdown file at `foragers/<name>.md`.
2. Set the YAML frontmatter (see existing foragers for examples) — the
   minimum is `name`, `title`, `description`. Optional but recommended:
   `archetype` (lens|dreamer|synthesizer), `default` (bool), `tags`, `bonds`,
   `sigil` (one Unicode glyph), `accent` (`#RRGGBB` hex).
3. Run `chb list` — your forager appears with its theme.
4. Run `chb palette --out foragers/palette.json` to refresh the
   exported palette.
5. Open a PR.

The runner picks up the file with no code change and no registration.

## Commit messages

Follow Conventional Commits where it fits:

```
feat(runner): tier-aware fallback chain for rate-limit retries
fix(comb): forager rows lose accent color on missing palette
test(citations): cover the offline DOI extraction path
chore(deps): bump golang.org/x/net for CVE-2026-0001
```

The body should explain *why*, not just *what*; the diff already shows
*what*.

## Testing philosophy

- **Behavior tests beat coverage padding.** A test that closes the DB
  and asserts "any error" passes through the line counter but pins
  nothing — write a test that asserts the *specific* failure shape.
- **Pin the rule, not the implementation.** When a test expects a
  particular convergence level or accept-gate verdict, comment the
  rule from the spec so a future reader understands the intent.
- **Race detector is mandatory.** `go test -race` catches goroutine
  leaks and unsynchronized writes that production runs would
  eventually surface as flakes.

## Releasing

A release is a push of `main` to the release repository, `Chubby-Honey-Bee/hive`, followed by a
`vX.Y.Z` tag there: `release.yml` builds the archives into a draft release and `publish-image.yml`
pushes the image. The checks before tagging, the commands and the two visibility settings are in
[docs/releasing.md](docs/releasing.md).

## Reporting bugs

Use the [bug report template](.github/ISSUE_TEMPLATE/bug.yml). For
security-sensitive issues, follow [SECURITY.md](SECURITY.md) instead
of filing a public issue.

## License

By contributing, you agree your code is licensed under the
[MIT License](LICENSE).
