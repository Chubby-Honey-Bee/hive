# The direct voice: does it become the default? (2026-10-06)

The rows and the reports of the run that decided whether `chb ask --direct-voice` (`docs/specs/swarm.md` § The direct voice) defaults on. Two arms over the Bench-1 selection case, 56 items each, on the swarm arm alone: **baseline** without the flag, **candidate** with it. `rule.md` holds the rule and the configuration, pre-registered and committed before the first model call; `decision.md` holds the result and the decision: KEEP, the flag stays off, because only one of the four conditions the rule set for adoption held.

The run was 2026-10-06, baseline 15:14:39Z to 17:29:28Z and candidate 17:30:42Z to 19:54:16Z, both on `chb` built from `./cmd/chb` at commit 17f1566, Ollama 0.35.0 at `http://localhost:11434/v1`, `qwen3.6:35b-a3b-q4_K_M` in every role, reasoning `none`, sampling seed 7. The commit hash here and in `decision.md` and `progress.log` is the build identity the harness recorded. It names the development history, which the public repository does not carry.

## Files

- `rule.md`: the pre-registered rule, the fixed configuration, the commands, the metrics and the adoption threshold.
- `decision.md`: the result of applying the rule to the rows, and the decision.
- `suite.yaml`: the case both arms ran, the Bench-1 selection case restricted to `arms: [swarm]`.
- `REPORT-C3-baseline.md`, `REPORT-C3-direct-voice.md`: each arm's harness report.
- `results-C3-baseline.jsonl`, `results-C3-direct-voice.jsonl`: the rows, one per item, as the harness wrote them.
- `cal-C3-baseline.jsonl`, `cal-C3-direct-voice.jsonl`: per-run calibration from `_tools/calibration.go`.
- `rows-C3-baseline.tsv`, `rows-C3-direct-voice.tsv`: the join `_tools/decide.py` writes and reads.
- `_tools/calibration.go`, `_tools/decide.py`: the two tools the decision ran.
- `progress.log`: the launcher's log, with its note on the candidate arm's hand launch.

Every local path in these files is replaced by `<scratch>`, `<repo>` or `<home>`.
