# Agent harness report

- generated: 2026-10-06T19:54:16Z
- chb: chb version dev
- git: 17f1566
- suite: `docs/evidence/2026-10-06-direct-voice/suite.yaml` (sha256 c06046789855fc5b)
- provider: `openai` at `http://localhost:11434/v1`; swarm tier `--budget-mode cheap`; lens model `qwen3.6:35b-a3b-q4_K_M`; queen model `qwen3.6:35b-a3b-q4_K_M`; lens reasoning `none`; queen reasoning `none`; persona profile `full`; seed 7; bench reps 1; template model `haiku`
- config: `C3-direct-voice`

What is deterministic here is the contract, not the prose: the same suite, preset, provider and tier are run every time, and each case passes only if every declared contract holds. The artifact sha256 is recorded so two runs can be compared, but model text is expected to differ between runs; a changed hash is a signal to read the verdict table, not a failure.

A **contract** check fails the case — it is deterministic given correct code. An **adherence** check (⚠) reports instead: whether a model at this tier honours a style rule varies between runs of a correct system, so the rate below is the signal, not any single run. `--strict` makes adherence fail the case too.

**Adherence this run: 0/0 checks clean.**

| Case | Kind | Result | Duration | Cost | Artifact sha256 |
|---|---|---|---|---|---|
| bench-twins-selection | bench | PASS | 2h23m34s | 504 model calls |  |

## bench-twins-selection

56 twin items in 28 pairs; one row per run in `<scratch>/dv-rule/ws-C3-direct-voice/bench-twins-selection/results.jsonl`, for `chb bench decide`.

| Arm | Runs | Completed | Accuracy | Class-balanced | Pairs passed | Token F1 | Abstain fabricated | Tokens in | Tokens out | Wall s |
|---|---|---|---|---|---|---|---|---|---|---|
| swarm | 56 | 56 | 0.86 | 0.92 | 20/28 = 0.71 | 0.52 | 4/4 | 1902559 | 306354 | 8612 |

- swarm by expected verdict: support 26/26 · oppose 22/26 · abstain 0/4
- correlated lens errors, over the 56 of 56 swarm runs with two or more lens verdicts: 130 of 724 pairs with an error erred together = 0.18 (0.15 if each lens erred independently at its own rate), of 1568 compared
- lens runs that returned a verdict, by model: qwen3.6:35b-a3b-q4_K_M 448/448
- swarm runs by lens diversity (low: one model, one verdict): low 4 · not_low 52 · unknown 0 · not_checked 0, of 56; 0 of the low runs wrong

| Family | swarm pairs |
|---|---|
| AB | 0/4 |
| F1 | 4/4 |
| F2 | 4/4 |
| F3 | 1/4 |
| F4 | 4/4 |
| F6 | 4/4 |
| F8 | 3/4 |

| Arm | Node | Runs | Mean tokens in | Mean tokens out | Mean wall s |
|---|---|---|---|---|---|
| swarm | forager-architect | 56 | 3225 | 528 | 64.1 |
| swarm | forager-direct | 56 | 863 | 460 | 66.8 |
| swarm | forager-empiricist | 56 | 3854 | 644 | 65.1 |
| swarm | forager-pragmatist | 56 | 3198 | 411 | 68.0 |
| swarm | forager-scholar | 56 | 4093 | 644 | 60.6 |
| swarm | forager-skeptic | 56 | 3453 | 603 | 62.6 |
| swarm | forager-steward | 56 | 3560 | 557 | 66.3 |
| swarm | forager-timekeeper | 56 | 3308 | 449 | 76.9 |
| swarm | queen | 56 | 8423 | 1175 | 36.2 |

- ✓ twin items generate from the live roster — 56 items in 28 twin pairs
- ✓ the workflows the runs dispatch are written (chb ask --no-dispatch, the solo control, --profile)
- ✓ the endpoint serves every model the case sends (model preflight) — http://localhost:11434/v1 serves qwen3.6:35b-a3b-q4_K_M
- ✓ results written to results.jsonl
- ✓ every run left its private tree unchanged
- ⚠ no tracked file changed during the case (a concurrent edit counts too) — ?? docs/evidence/2026-10-06-direct-voice/rows-C3-baseline.tsv
- ✓ a run completed (56 of 56)

logs: `<scratch>/dv-rule/ws-C3-direct-voice/bench-twins-selection`
