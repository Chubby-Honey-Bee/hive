# Changelog

All notable changes to HIVE are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

Nothing yet.

## [0.1.0] — 2026-10-09

The first release: two binaries built from `cmd/`, `chb` (the command line)
and `chb-mcp` (a stdio MCP server). Both carry `foragers/`, `agents/` and
`workflows/`, so they run from any directory; a folder of the same name
beside the binary or under the working directory is read instead.

### Added
- **The forager swarm.** `chb ask` runs thirteen lenses, the balanced nine by default, each with a fixed vantage and typed bonds to the others (`cites`, `contradicts`, `resonates`). A ∇ convergence sensor watches every `resonates` pair while the run is going. The Queen's answer leads with its calibration: a `Quorum` line (convergence, tally, plurality, dissent), the ∇ pairs that fired, then the verdict. `--json` prints one object; `--deterministic --seed N --artifact` writes a canonical artifact that `chb verify-artifact` re-checks; `--lens-tools` lets the lenses read a repository; `--persona-profile lean` fits small context windows.
- **Labels that cannot be laundered.** Every finding is a definition, an assumption, a guarantee or an unknown. A guarantee resting on an unknown is refused at the write path and again by the gate's audit; a refuted finding reverts what depends on it (`chb db-write cascade_revert`). The one-hop invariants and acyclicity are decidable and preserved by the write checks in Lean 4 (`lean4/`, 95 theorems and 9 instances, no `sorry`); `chb lean4-extract` and `lean4/bridge/verify-state.sh` check a live database against them.
- **The comb.** A per-coordinate belief digest with a confidence that falls with conflicts and critical gaps and is 0 with no findings, Time Wheel revisions (`chb comb query`, `at`, `diff`, `history`, `wheel`), exact staleness, a quorum cap that stops a region from promoting on agreement alone, embeddings and `chb recall`, and the dreamer's ripening loop (`chb ripen`).
- **Autonomous mode.** `chb agent-run workflows/hive.yaml` loops scan, dispatch, evaluate and gate over a project's database, with `chb hive init`, `status`, `next`, `complete` and `report`; a second project gets its own workspace database. The hive works gaps and findings, so a workspace is seeded with `chb db-write gap`, `chb ingest-findings` or `chb ingest` first.
- **The calibration loop.** An append-only `outcomes` ledger (`chb outcome-record`, `outcome-import`, the MCP tool `chb_outcome_record`), `chb calibrate` rolling it into per-lens, per-label and per-synthesizer scores, those scores reaching the Queen's prompt, the comb (`calibrated_confidence`) and the gate (`--require-outcome-review`), a `calibrate` workflow node, and `chb calibration-export` / `calibration-merge` for counts across projects. Calibration never writes a label.
- **The workflow engine and runner.** Graph workflows of `agent`, `parallel_fan`, `decision`, `human_review`, `command` and `calibrate` nodes with accept gates and repair, `tier` and `role` routing, `--budget-mode`, and `--profile local-fast` (also `local-small`, `local-8gb`) that routes every role, and every node that names no model or provider of its own, to a model served on this machine and refuses a run that would leave it. Providers: Anthropic, Gemini and OpenAI-compatible SDKs, the `claude` and `gemini` CLIs, and `local`. Per-node tokens and cost, and a `shell` tool that runs natively on Windows.
- **Measurement harnesses.** `chb agent-harness` with `bench` (graded twin items under a pre-registered rule, decided by `chb bench decide`) and `design` (the executability benchmark, reported by `chb design report`), beside `chb validate`, `chb replay-behavior` and `chb mcp-smoke`, which `make gate` and CI run.
- **The MCP server.** `chb-mcp` exposes 20 tools over stdio; the image `ghcr.io/chubby-honey-bee/hive` carries both binaries, `chb-mcp` as its entrypoint.
- **Review and implement.** `chb review` audits a repository through five lenses and renders `REVIEW.md`; `chb implement` applies its findings.

### Measured
- **The swarm is not the more accurate way to answer a closed question**, and its quorum is calibrated. Bench-1 (2026-10-01; four seeds, 56 items, `qwen3.6:35b-a3b-q4_K_M` in every role on Ollama): the swarm 0.82 against one call of the same model 0.95; every verdict the Queen called high convergence was right, 26 of 26; lenses too small to err independently give a quorum that is not calibrated. `docs/strategy.md` holds the reading.
- **The direct voice is not adopted** (`chb ask --direct-voice`, off): 48/56 against 47/56 over four seeds, with a worse high-convergence record and more fabricated abstains. `docs/evidence/2026-10-06-direct-voice/`.
- **The executability benchmark's first run is `not-worth-it` at 1.68× the tokens**: over twelve small Go tasks, the research-first design and plan were not carried out better by a plain executor than one call's. `docs/evidence/2026-10-06-bench-design/`.

Commit hashes named under `docs/evidence/` belong to the development history, which this repository does not carry.

[Unreleased]: https://github.com/Chubby-Honey-Bee/hive/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Chubby-Honey-Bee/hive/releases/tag/v0.1.0
