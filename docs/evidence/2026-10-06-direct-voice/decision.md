# Decision: KEEP — `--direct-voice` stays off (2026-10-06)

One of the rule's four conditions held. Class-balanced accuracy rose
(0.923 against 0.885), but the paired result was two wins to one loss
(sign test p = 0.50, far from the registered p ≤ 0.10), the correct rate
given convergence `high` fell (35/37 against 28/28), and the candidate
fabricated one more abstain (4/4 against 3/4). The rule in `rule.md` was
committed at 17f1566 before the first model call and is applied here
unchanged.

## The runs

- Commit 17f1566, `chb` built from `./cmd/chb` on it; Ollama 0.35.0;
  provider `openai` at `http://localhost:11434/v1`; `qwen3.6:35b-a3b-q4_K_M`
  in every role; reasoning none; sampling seed 7; `suite.yaml` beside this
  file (`bench-twins-selection`, `minimal`, seeds 1–4, swarm arm only).
- Baseline `C3-baseline`: 15:14:39Z to 17:29:28Z (2 h 14 m 49 s), 448
  model calls, 56/56 runs completed, every tree check clean.
- Candidate `C3-direct-voice`: 17:30:42Z to 19:54:16Z (2 h 23 m 34 s), 504
  model calls, 56/56 runs completed. One warning, not a failure: the
  harness saw an untracked file appear in the checkout during the case,
  `rows-C3-baseline.tsv`, written by a dry run of `decide.py` on the
  baseline alone; no run reads it.
- Every item's `item_hash` is equal across the arms; 56 items joined.
- The candidate was launched by hand after the baseline because the
  launcher script's chain had a bug (`progress.log`); same binary, same
  commit, 74 s apart. The total, 4 h 39 m, is inside the registered 4.7 h.
- Per-run calibration: `cal-<arm>.jsonl` from `_tools/calibration.go`
  (`workflow.RunCalibration` on each run's `hive.db`); `rows-<arm>.tsv`
  is the join `_tools/decide.py` writes; its stdout is reproduced below.

## Per arm

| metric | C3-baseline | C3-direct-voice |
|---|---|---|
| accuracy | 47/56 (0.839) | 48/56 (0.857) |
| class-balanced accuracy | 0.885 | 0.923 |
| by class | support 26/26 · oppose 20/26 · abstain 1/4 | support 26/26 · oppose 22/26 · abstain 0/4 |
| completed | 56/56 (1.000) | 56/56 (1.000) |
| fabricated abstains | 3/4 | 4/4 |
| direct vote right | - | 53/56 (0.946) |
| correlated lens errors (REPORT.md) | 0.20 (0.17 if independent) | 0.18 (0.15 if independent) |
| runs with a ∇ pair fired | 54/56 (0.964) | 54/56 (0.964) |
| mean wall s | 144 | 154 |
| mean tokens in / out | 32632 / 5084 | 33974 / 5471 |

## Paired by item

wins 2: F1-s2-b, F1-s3-b
losses 1: AB-s1-b
both wrong 7: AB-s2-b, AB-s3-b, AB-s4-b, F3-s2-b, F3-s3-b, F3-s4-b, F8-s3-a
one-sided exact sign test p = 0.5000 (n = 3)

| item | want | C3-baseline got (conv; tally) | C3-direct-voice got (conv; tally) | direct |
|---|---|---|---|---|
| AB-s1-b | abstain | abstain (medium; abstain 6, oppose 1) | oppose (medium; abstain 7, oppose 1) | abstain |
| F1-s2-b | oppose | abstain (low; oppose 3, support 3, abstain 1) | oppose (medium; oppose 4, support 3, abstain 1) | oppose |
| F1-s3-b | oppose | conditional (low; oppose 3, support 3, abstain 1) | oppose (medium; oppose 4, support 3, abstain 1) | oppose |

## Calibration

| cell | C3-baseline | C3-direct-voice |
|---|---|---|
| convergence high | 28/28 (1.000) | 35/37 (0.946) |
| convergence medium | 16/20 (0.800) | 11/16 (0.688) |
| convergence low | 3/8 (0.375) | 2/3 (0.667) |
| convergence none | 0/0 | 0/0 |
| margin >=4 | 30/32 (0.938) | 38/39 (0.974) |
| margin 1-3 | 17/20 (0.850) | 10/16 (0.625) |
| margin tie | 0/4 (0.000) | 0/1 (0.000) |
| margin no vote | 0/0 | 0/0 |
| dissent written | 0/3 (0.000) | 0/3 (0.000) |
| dissent not written | 47/53 (0.887) | 48/53 (0.906) |

## The rule applied

1. Class-balanced accuracy 0.923 vs 0.885: **held**.
2. Losses 1 ≤ wins 2 and p = 0.50 ≤ 0.10: **failed**. Under the
   non-inferiority reading the rule set aside (losses ≤ wins alone) this
   clause would hold; it does not decide.
3. Correct rate given `high` 35/37 (0.946) vs 28/28 (1.000): **failed**.
4. Fabricated abstains 4 vs 3: **failed**.

**KEEP.** No default changes.

## What the rows say

- The two wins are the mechanism the quick screen's exploratory arm saw:
  both were 3–3 lens ties with one abstention, the baseline Queen abstained
  on one and called the other `conditional`, and the direct vote (right
  both times) made them 4–3; Queen followed with `medium`.
- The loss repeats the quick screen's one loss on the same item: on
  AB-s1-b seven lenses abstained, the direct vote with them, one lens said
  `oppose`, and the candidate Queen answered `oppose` with a written
  dissent where the baseline Queen abstained. The direct vote was right;
  Queen's reading of the extra line was not.
- The tie-break does not carry when Queen does not follow the tally: on
  F3-s3-b and F3-s4-b the direct vote turned 3–3 into oppose 4–3 (right),
  and Queen answered `support` with a dissent in both arms.
- `high` grew from 28 runs to 37. The eighth vote lifts 7–1 and 5–2–1
  tallies to `high`; the two `high` errors are F3-s2-b (support 5, oppose
  2, abstain 1; the direct vote said oppose) and AB-s3-b (oppose 5, abstain
  3, fabricated). The baseline's `high` was right every time.
- The abstain class is where the direct voice costs: the direct vote
  itself abstained on three of the four abstain items, and the candidate
  Queen fabricated all four.
- The direct vote alone was right 53/56 (misses F3-s1-a, AB-s4-b, F8-s4-a),
  more than either swarm. That is the solo control's standing question
  (bench.md), measured again here, not a finding of this rule.
- `conditional` is in the verdict enum of the lenses and of Queen, and the
  grader counts it wrong on these closed questions: three baseline Queens
  answered it, no candidate Queen did, and one candidate lens did (F6-s2-a,
  where Queen still answered right).

## What this decides

KEEP under the registered rule, on 56 items, C3, `minimal`, `--no-eval`,
closed questions over a context pack, this machine, Ollama 0.35.0,
2026-10-06. It is not evidence of harm on accuracy: the paired count is
2–1 and class-balanced accuracy rose. It is evidence against adoption on
the two calibration guards, and the gain it found (two items) is inside
the noise floor the rule named. A third rule would need either a larger
item set (the gain is bounded by the baseline's nine errors) or a Queen
prompt that does not count the direct line toward convergence; this run
proposes neither.

## Claims

- Definition: the rule's four conditions, the superiority reading of the
  sign test, the calibration bands, and "incomplete counts wrong".
- Guarantee: every number above follows from `results-<arm>.jsonl` and
  `cal-<arm>.jsonl` through `_tools/decide.py`; the calibration values are
  what `workflow.RunCalibration` reads from each run's database, the code
  `chb ask` prints.
- Assumption: temperature 0 and seed 7 made each arm's answers a property
  of the configuration rather than of the hour it ran; not verified by a
  repeat.
- Unknown: whether the direct voice helps on open questions, under the
  coverage pass, with 4b or 8b lenses, or on another provider.
