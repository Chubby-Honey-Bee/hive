# Rule: does the direct voice become the default? (pre-registered 2026-10-06)

Written and committed before any model call. The question: should
`chb ask --direct-voice` (swarm.md § The direct voice) default on? The
quick screen of 2026-10-01 said no on one seed (13/14 vs 14/14, one
fabricated abstain) and an exploratory held-out seed said maybe (12/14 vs
10/14); its rows were lost with the scratch directory. This rule decides on 56
items and leaves its rows here.

## Configuration (fixed)

- Build: this checkout at the commit that holds this file, `chb` built from
  `./cmd/chb` into a scratch directory. Both arms run on the same binary.
- C3: `qwen3.6:35b-a3b-q4_K_M` in every role (lenses, Queen, the direct
  voice), `--lens-reasoning none --queen-reasoning none`, sampling seed 7
  (which implies temperature 0), `--reps 1`. Provider `openai` against
  Ollama 0.35.0 at `http://localhost:11434/v1`; nothing else uses it.
- Items: `suite.yaml` beside this file, which is the Bench-1 selection case
  of `fixtures/bench/suite.yaml` (`bench-twins-selection`: preset `minimal`,
  generator seeds 1–4, 7 families × 4 seeds × twins = 56 items, `--no-eval`)
  restricted to `arms: [swarm]`. The solo control reads neither flag, so it
  would decide nothing; the candidate's direct vote is recorded in its rows
  as the lens named `direct`.
- Arms: **baseline** = that run without `--direct-voice`; **candidate** =
  the same with `--direct-voice`. Both arms run fully here; the earlier
  Bench-1 rows are gone and the build has changed.
- Order: baseline first, then candidate, sequentially.

## Commands

From the repository root (the harness reads `foragers/<name>.md` from the
working directory), with
`OPENAI_BASE_URL=http://localhost:11434/v1 OPENAI_API_KEY=ollama`:

    chb agent-harness --suite docs/evidence/2026-10-06-direct-voice/suite.yaml \
      --only selection --provider openai \
      --lens-model qwen3.6:35b-a3b-q4_K_M --queen-model qwen3.6:35b-a3b-q4_K_M \
      --lens-reasoning none --queen-reasoning none --seed 7 \
      --config C3-baseline --workspace <scratch>/dv-rule/ws-C3-baseline

    chb agent-harness ... --direct-voice \
      --config C3-direct-voice --workspace <scratch>/dv-rule/ws-C3-direct-voice

Expected wall: about 145 s a baseline run and 154 s a candidate run
(2026-10-01, same models), so about 2.3 h and 2.4 h, 4.7 h in all. All four
seeds are registered rather than three: Ollama is dedicated to this run, the
cost is wall time and not money, three seeds would leave 42 items and three
abstain items, and the claim at issue (tie-breaking on the held-out seed 2)
needs the seeds the quick screen never ran, 3 and 4, as much as seed 2.

## Metrics (each arm, over the 56 items; an incomplete run counts as wrong)

- Class-balanced accuracy: the mean of the support-class and oppose-class
  correct rates over the 52 support/oppose items, as `bench.Summarize`
  computes it and REPORT.md prints it.
- Paired outcome by item: a **win** is an item the candidate answers right
  and the baseline wrong; a **loss** the reverse; the rest are ties. The
  one-sided exact sign test over the discordant items: with n = wins +
  losses, p = P(X ≥ wins | X ~ Binomial(n, ½)). With n = 0, p = 1.
- Calibration table, from each run's `hive.db` through
  `workflow.RunCalibration` (the same code `chb ask` prints), joined to
  `results.jsonl` by item: correct/n by Queen convergence (high, medium,
  low, none when Queen did not complete); by tally margin (≥ 4, 1–3, tie,
  no vote; the margin is the plurality's votes less the runner-up's,
  abstain casting no vote; the candidate's tally has eight voters, the
  baseline's seven); by dissent (written, not written).
- Fabricated abstains: the four `AB-s<seed>-b` items (want `abstain`)
  answered `support` or `oppose`.
- Completion: runs that exited 0 with a verdict, per arm. Reported, not a
  threshold on its own: an incomplete run is already a wrong answer above.
- Also reported, deciding nothing: accuracy, the direct vote's own correct
  rate in the candidate, the correlated-error rate among the persona lenses,
  ∇ pairs fired, mean wall and tokens.

## Adoption threshold

`--direct-voice` becomes the default only if all four hold:

1. The candidate's class-balanced accuracy is not below the baseline's.
2. Losses ≤ wins **and** the sign test gives p ≤ 0.10. This is a superiority
   test at one-sided α = 0.10, chosen because the default change costs one
   model call per swarm and a tie (no discordant items) should not pay it.
   The smallest passing counts are 4–0, 6–1, 7–2, 9–3, 10–4.
3. The candidate's correct rate given convergence `high` is not below the
   baseline's; a candidate with no `high` run fails this (nothing to show).
4. The candidate's fabricated abstains are not more than the baseline's.

Otherwise the flag stays off (KEEP). The numbers under the non-inferiority
reading of 2 (losses ≤ wins alone) are reported beside the decision so the
choice is visible, but they do not decide.

The join requires equal `item_hash` per item across arms; an item whose
hashes differ is excluded and named. If either arm stops short of 56 runs
(the harness stops a case when the endpoint goes away and marks the file
`not_measured`), the rule is not applied, the decision is KEEP, and the
partial rows are kept here and said to be partial.

## What 56 items can and cannot decide

They can call harm (a loss count the sign test would reject at the same
level) and they can see a large gain: the candidate fixing four or more of
the baseline's errors with no loss. The gain is bounded by the baseline's
own error count; on the quick screen's build the baseline erred on 0 of 14
(seed 1) and 4 of 14 (seed 2), so roughly 4–10 items are available to win.
They cannot separate a one- or two-item difference from the Queen-replay
noise floor (a content-free prompt change flipped 1 of 14 verdicts, about 4
of 56); they weigh the abstain class as four items, not as a class; and
they speak only to C3, the `minimal` preset, `--no-eval`, the seven twin
families as closed questions on a context pack, this machine and Ollama
0.35.0 on 2026-10-06. Nothing here measures open questions, the coverage
pass, 4b or 8b lenses, or another provider.

## Evidence left here

`progress.log` (arm start and end), `results-<arm>.jsonl` and
`REPORT-<arm>.md` copied from each workspace, `rows-<arm>.tsv` (one line a
run: item, want, got, convergence, tally, plurality, margin, dissent, ∇
fired, the direct vote, wall), `decision.md`. The run workspaces and their
databases stay in the scratch directory and are not committed. `_tools/` holds the
extractor (a Go program that calls `workflow.RunCalibration` on each run's
database) and the script that joins and decides; the leading underscore
keeps them out of `./...`.
