# Bench Specification

## Purpose

The bench tells "cheaper and as good" from "cheaper and worse" before any model is swapped for a smaller one. It asks questions the forager roster answers. Each question comes in a twin pair: an edit to the roster flips the answer, so a model that always gives one verdict gets no pair right. Items run through `chb agent-harness` on any provider, as a swarm and as one call with no persona. `chb bench decide` applies a decision rule fixed before the first run.

## Requirements

### Requirement: The harness runs on any provider

`chb agent-harness` SHALL take `--provider` (`claude-cli`, the default, or `anthropic`, `openai`, `gemini`, `gemini-cli`, `local`) and export it as `HIVE_PROVIDER` to every case. It refuses any other value. It SHALL require the `claude` binary only when the provider is `claude-cli`. Template cases drive `claude -p`, so on any other provider they are skipped and the report says why. The proof case passes the provider to `chb proof`.

`--lens-model` pins every forager (`chb ask --model`) and the bench solo control. A comma-separated list is a mixed-family arm, and the solo control runs on its first model. For each bench run the harness rotates the list by k = (the item's seed + the repetition − 1) mod n, for n models, and passes it to `chb ask`. The lenses, in name order, take the rotated list's models in turn (swarm.md § Workflow generation). So lens slot j runs on model (j + k) mod n. Without the rotation the same personas would always run on the same family. With it, over n consecutive seeds, each lens runs on each model once. Twins share a seed, so both twins run on one assignment. A row records the list its run used. `--queen-model` pins Queen (`--synthesizer-model`). Unpinned, the tier system and `--budget-mode` choose. `--lens-reasoning` sets the reasoning level of every lens and the solo control (`chb ask --forager-reasoning`), and `--queen-reasoning` that of Queen (`--synthesizer-reasoning`): `none`, `low`, `medium` or `high`. Unset, no level is sent, and the server's default applies. A Qwen model on Ollama then thinks; Bench-0 measured that hitting the output cap with no answer. The harness SHALL refuse an empty entry in the `--lens-model` list, and a reasoning level outside the four, before any case runs. `--seed N` passes a sampling seed, which implies temperature 0; repetition r of a bench item uses seed N+r−1. `--reps` is the number of runs of each bench item on each arm. `--persona-profile` and `--persona-sections` pass to every `chb ask` the harness runs (swarm.md § Persona profiles), for the ablation that must show lean non-inferior to full; the persona profile and the lens models are chosen independently, so a mixed-family arm runs under either profile. The harness refuses a persona value ask would refuse, before any case runs. The solo control has no persona, so neither flag reaches it. `--direct-voice` and `--context-split` pass to every `chb ask` a swarm or bench case runs (swarm.md § The direct voice, § The context split); the solo control reads neither, since it is already the direct voice's call, and it is unchanged. `--config` names the configuration in bench results; the default joins the provider, the two models and the budget mode, then the two reasoning levels when either is set, then, when either persona flag is given, the persona profile (`full` when only sections are given) and `§` with the sections, then `direct-voice` and `context-split` when each is given.

`--profile <name>`, else `HIVE_PROFILE`, runs every case under that routing profile (runner.md § Routing profiles): the harness exports it as `HIVE_PROFILE`, so each `chb ask` and `agent-run` a case starts routes its roles by it, the bench solo control as a `lens`. Its provider becomes the harness's unless `--provider` is given. It SHALL be refused beside `--lens-model`, `--queen-model`, `--lens-reasoning` or `--queen-reasoning`, which it replaces, and an unknown or malformed profile is refused before any case runs. The configuration is then named `profile <name>`, then the persona profile and sections as above when either persona flag is given, each bench row records the profile's lens and queen models, and the report and `report.json` name the profile's lens and Queen models and reasoning in place of the flags it replaces. The routing profile and the persona profile are chosen independently: the routing profile sets where each role runs, the persona profile how much of each persona its prompt carries, so the lean-versus-full ablation runs under a routing profile as under the flags. A case whose workflow names no roles, the proof workflow among them, runs each node that names no model or provider of its own on the profile's default route (runner.md § Routing profiles).

The report records the provider, `OPENAI_BASE_URL` when the provider is `openai` or the local endpoint when it is `local`, the routing profile with its lens and queen models, the models, the reasoning levels, the persona profile, the seed, the repetitions and the configuration name. It lists every node of a swarm case with its model, status, tokens and wall time.

#### Scenario: A local model through Ollama
- **WHEN** `OPENAI_BASE_URL=http://localhost:11434/v1 chb agent-harness --provider openai --lens-model ministral-3:8b` runs on a machine without the `claude` binary
- **THEN** swarm and bench cases run against that endpoint and template cases are reported as skipped

#### Scenario: A mixed-family arm at seed 1
- **WHEN** `--lens-model qwen3.5:4b,ministral-3:8b --lens-reasoning none` runs an F1 twin pair at seed 1 on both arms
- **THEN** both swarm runs pass `--model ministral-3:8b,qwen3.5:4b` (k = 1), so architect runs on ministral-3:8b; the solo runs use qwen3.5:4b; and every lens and solo call sends `reasoning_effort: none`

#### Scenario: An empty entry in the list
- **WHEN** `chb agent-harness --lens-model "qwen3.5:4b,,ministral-3:8b"` starts
- **THEN** it exits with an error naming `--lens-model` before it reads the suite or runs a case

#### Scenario: A persona ablation under a routing profile
- **WHEN** `chb agent-harness --profile local-small --persona-profile lean` runs a bench case
- **THEN** every `chb ask` it starts runs under `HIVE_PROFILE=local-small` with `--persona-profile lean`, the configuration is `profile local-small/lean`, and the report names the routing profile, its lens and Queen models, and the persona profile

### Requirement: Each run works in a private tree

Every swarm case and every bench run SHALL run with its working directory set to a private copy of `foragers/` and `agents/`, and with `HIVE_FORAGERS_DIR` pointing at that copy. The harness records the sha256 of every file and the target of every link in the copy before the run, and compares after it. Any added, removed or changed path SHALL fail the case, and the check names the paths. The in-process file tools refuse any path outside their working directory. A `shell` call, or the claude CLI's own tools, can still write outside the copy, and this check does not see that. Bench cases still report, as a warning, any tracked file that changed in the checkout while they ran.

A bench run SHALL work in a fresh temporary directory outside the workspace: its copy, its database and its artifact. After the run is graded, the harness moves that directory to `<workspace>/<case>/<item>/<arm>-r<rep>`. The workspace holds the item's ID in its path and earlier runs' results, which name the expected answers. A lens that prints its working directory, lists its parent or reads its environment finds neither. A lens with a shell can still search the whole disk. Nothing sandboxes it.

#### Scenario: A lens looks around
- **WHEN** a lens runs `pwd; ls -a ..; env` during a bench run
- **THEN** the output names neither the workspace nor the item's ID, and after the run its directory is in the workspace under the item's ID

#### Scenario: A lens writes a file
- **WHEN** a lens calls `write_file` on `foragers/intruder.md` during a bench run
- **THEN** the run's row has `tree_ok: false`, its error names `+foragers/intruder.md`, the case fails, and the checkout's `foragers/` is untouched

### Requirement: Twin items

`bench.Generate` SHALL build one twin pair per family per generator seed from the live roster (`foragers.Load`). Variant a is the live roster with edits that keep its answer. Some of the things the answer names are dropped, but never all. Bonds or axes are added or changed in ways that do not count, such as a `cites` bond where the question asks about `resonates`, or `wasp: I` on an ineligible forager. Variant b is variant a with the edit that flips the answer. The generator checks that the two verdicts differ. The families:

| ID | Question | Tokens |
|---|---|---|
| F1 | Does `minimal` leave an orphaned `resonates` pair? | `a⇄b` |
| F2 | The same for `balanced`, whose members the question lists | `a⇄b` |
| F3 | The same for `all` | `a⇄b` |
| F4 | Does any deliberation-eligible forager declare `wasp: I`? | `@name` |
| F6 | Does a forager in `minimal` declare `contradicts` on another in `minimal`? | `a→b` |
| F8 | Does every forager in `minimal` declare an `mss` axis? | `@name` for each without one |
| AB | Does the roster entry for X list a `resonates` bond to Y? | none |

The AB family is the abstain family. Its variant b drops X's entry from the pack, so its answer is `abstain`. On the shipped roster today, F1, F2 and F3 answer support, and F4, F6 and F8 answer oppose. The tests recompute these values from the roster and never pin them.

The pack SHALL be the frontmatter fields the families read: `name`, `archetype`, `render_layer`, `deliberation_eligible`, `coverage` and `bonds`, as YAML sorted by name. The item's answer is computed from the same edited roster the pack is rendered from. Every question states the rules it needs: use only the roster, the eligibility rule, and the preset's definition. It ends: "Emit exactly one verdict: support, oppose or abstain. Emit abstain if the roster lacks an entry the question needs." Twins share their question and differ only in the pack. No question or pack contains a brace, which the workflow engine would substitute.

A seed drives a PCG generator salted by the family ID. A pair that repeats one already generated in the same call is redrawn from the same generator, up to 64 times. The item set is therefore a function of the family list and the seed list. An item's hash is taken over its question and pack. Runs pair on it, so two runs pair on the same item even if the roster changed between them.

Generated in separate calls, two seed lists can share items: the roster admits few distinct items per family. `bench.GenerateAfter` SHALL draw the items of a seed list after those of a prior list, in one call, and return only the later seeds' items. Generate draws each family's seeds in list order, and no two families ask the same question. So the prior items are the ones Generate returns for the prior seeds alone, and no later item repeats one. A bench case's `exclude_seeds` names its prior seeds. The shipped confirmation case excludes the selection seeds.

#### Scenario: Confirmation after selection
- **WHEN** the shipped suite's selection and confirmation cases generate their items from the same roster
- **THEN** no confirmation item repeats a selection item, and `chb bench decide` does not void the run for overlap

#### Scenario: A constant model
- **WHEN** a model answers `support` to every item
- **THEN** it passes no twin pair, because each pair expects two different verdicts

### Requirement: Grading

An answer's verdict SHALL be correct when, lowercased and trimmed, it equals the expected verdict. When a family asks for tokens, the grade also extracts from the answer's text every canonical token of that kind whose names are on the roster. Case is ignored. A pair is written in alphabetical order whichever order the text used. Only `⇄`, `→` and `@` count as connectors. Token F1 compares the named set with the expected set. Both empty scores 1. One empty and the other not scores 0. The AB family is graded on the verdict alone.

The grade counts every canonical token in the text, including one the text mentions only to rule it out. So each question that asks for tokens tells the model to write nothing else in that form. A model that writes `a⇄b` for a pair it rules out is graded as naming it.

The swarm's answer is Queen's: her verdict from the artifact, and for tokens her stored outputs decoded, her report first and then every other field. So a `⇄` her JSON escaped still counts. When her outputs hold no object, her text from the database is used. The solo control's answer is its node's.

### Requirement: The swarm, the solo control and the results

A bench case SHALL run each item on its arms (default both). The swarm arm is `chb ask` with the pack as `--context`, the case's preset (default `minimal`), and `--no-eval` unless the case sets `eval`. The solo arm is a one-node workflow with no agent persona and the same question and pack. It asks for the lens contract's `verdict`, `key_points` and `recommendation`, on the lens model or the lenses' tier. Its prompt and schema are `foragers.DirectPrompt` and `foragers.DirectSchemaJSON`, the ones the swarm's direct voice runs under, so under `--direct-voice` the swarm holds the solo call as one of its votes. It SHALL be held to a lens's terms, so the comparison measures the swarm and not a handicap: no tools (`tools: []`), its output schema enforced (reasons before the verdict), and one repair (`on_reject`).

#### Scenario: The solo control is a fair baseline
- **WHEN** the harness writes the solo workflow
- **THEN** its node has `tools: []`, an `output_schema` whose `verdict` is the lens enum, and `on_reject` with one repair, as a generated lens node has

The case SHALL write one row per run to `<workspace>/<case>/results.jsonl`. A row holds the configuration, provider, models, arm, item, item hash, family, seed, variant and repetition. It also holds the expected and given verdicts and tokens, token F1, and whether the run completed (it exited 0 and yielded a verdict) and left its tree unchanged. It holds the lenses Queen read in full, the outputs that held their contract, the wall time, the tokens, and every node's model, status, tokens and wall time. A swarm row also holds `lens_answers`: each lens forager node of the run, whatever its status, with the verdict it returned (empty when none) and the model its row records (empty when none). Under `--direct-voice` that includes `direct`, so the lens count, the full-verdict count, the diversity state and the correlated-error rate count it with the lenses; its output holds its contract when the solo control's would (`key_points`, `recommendation`, and a verdict in the enum), since it has no persona file. It holds `diversity`, the run's lens diversity by the artifact's check (swarm.md § Deterministic artifact), empty when the run left no run row. Both come from the run's node rows (`workflow.RunLensAnswers`), not from the artifact. So a run whose Queen failed, which writes no artifact and has no verdict, still counts toward the correlated-error rate and the diversity counts. Queen read a lens in full when its node completed in the run with outputs that hold a verdict: then `{verdict.forager:<name>}` gives her every field of it (comb.md § Template tokens). Queen joins settled (swarm.md § Workflow generation), so a lens that was rejected reaches her as a line saying so, and the count is below the lens count for that run. A lens whose call failed returned no text and wrote no vantage, so the artifact leaves it out of the lens count too. A lens holds its contract when its persona's required keys are present and its verdict is in the enum. Both counts cover runs that wrote an artifact. `finish_length` is the number of the run's model calls the provider stopped at the output cap: the sum of its nodes' `cutoff_calls`, which count every dispatch, fan item, tool turn, finalize call and repair attempt (runner.md § Incomplete replies fail). On the Claude CLI they count its final reply only: the CLI reports no other stop reason, so a turn it cut off inside its own loop is not seen. It leaves out the constraint probes, which answer no node. It is null when a node ran on the Gemini CLI, which reports no stop reason, and when the run left no run row. Node wall time has whole-second resolution. The report adds, per arm, accuracy, class-balanced accuracy, the pair score, token F1, abstain items answered support or oppose, and tokens. It also gives HIVE lift (swarm minus solo class-balanced accuracy), pairs passed per family, and each node's mean tokens and wall time. For the swarm arm it gives the correlated-error rate over all lens pairs, and how many runs it covers: those with two or more lens verdicts. When the lenses' rows record two or more models, it also gives the rate over pairs on different models, or says that no such pair both returned a verdict. It gives each recorded model's lens runs and how many of them returned a verdict, so a family that returned none shows. It counts the swarm runs by diversity state, a run with none as `not_recorded`, and how many low runs were wrong. Class-balanced accuracy is the mean of support accuracy and oppose accuracy. At four seeds a run has four abstain items, too few to weigh as a class, so the count answered support or oppose judges them instead.

A wrong answer is data. A bench case fails only when its items cannot be generated, its workflows cannot be written, the endpoint fails the model preflight before the first run, a run reaches no model or does not complete and the endpoint then fails the preflight, no run completed (§ An outage is not a result), a run changed its tree, or the results cannot be written. A case that sets `require_pairs` SHALL also fail unless every run completed and, on every arm, both twins of every pair were answered right. These are two checks. The completion check names each run that did not complete, with the last line of its error. The pair check names each completed run answered wrong, and only counts the runs that did not complete. So a failed setup, such as a refused request, a deadline or a failed Queen, does not read as a wrong answer.

### Requirement: An outage is not a result

Before the first run of a bench case, the harness SHALL ask the endpoint whether it serves every model the case's runs will send. It asks with the runner's model preflight (runner.md § Endpoint model preflight), over the workflows the runs dispatch. The swarm's is the one `chb ask --no-dispatch` writes, once for each lens list the case's items and repetitions rotate to. The solo control's is the one the harness writes. Under a routing profile both are routed as agent-run routes them, and a node the profile does not route runs on the profile's provider. So the check covers each model of a `--lens-model` list that a run sends, Queen, the solo control, the evaluator and follow-ups when the case sets `eval`, and each model the profile routes the case's nodes to. A profile's routes for roles the case has no node for are not asked. When the workflows cannot be written, the case fails under its own check, not the endpoint's. When the endpoint check fails, the case SHALL fail with the preflight's message. It runs no item and writes no `results.jsonl`. The case clears its workspace only once this check passes, so a rerun while the endpoint is down keeps an earlier run's results.

The preflight asks the endpoint at `OPENAI_BASE_URL` when a node runs on provider `openai` and `OPENAI_API_KEY` is set, and the local provider's endpoint when a node runs on `local`. It asks nothing else. On `claude-cli` and `gemini-cli` nothing is asked: the CLI owns its endpoint and its credentials, and nothing lists its models without a model call. Nor on `anthropic` or `gemini`. When nothing is asked there is no endpoint check, and the console says that nothing was asked.

When nothing listens at the endpoint, the preflight's message is led by the URL, the refused connection, and what to do: start the model server, for Ollama the app or `ollama serve`. The console and `REPORT.md` show a detail's first 160 characters only, so each message here puts the cause before any long error text.

A run that did not complete SHALL stop the case at that run when it reached no model, or when the endpoint then fails the preflight. A run reached no model when it left no `workflow_runs` row. agent-run makes every setup refusal before it writes that row: its model preflight, the backend it cannot build (a missing `OPENAI_API_KEY`, say) and the routing refusals among them. So a run that failed its own model preflight stops the case, even when the endpoint is back by the time the harness asks. After a run that did not complete and left a run row, the harness asks the endpoint again. When the preflight now fails, the endpoint is down or no longer serves a model, and every later run would fail the same way. That covers a refused connection and a model list the endpoint cannot give. The case fails, naming the run and why: the preflight's error, and the run's own error when it says something else. When the endpoint gave no answer at all, the message adds that this configuration may have taken the server down, out of memory say, which the server's log tells. The harness tells an outage by whether the run left a run row, and by whether the preflight returned an error. It parses no error text.

A case that stopped, and a case in which no run completed, SHALL write its rows under a first line `{"not_measured": why}`. `bench.ReadRows`, which `chb bench decide` reads with, refuses such a file and says why. This is a choice. The rows are kept to read: the runs before an outage, or a model cut off on every run, can tell the owner something. But they are not a result of the configuration. A glob over the workspaces still hands the file to decide, which then refuses it. A missing file would leave that configuration out without a word.

A run that did not complete, left a run row, and after which the endpoint still passes the preflight is data, recorded as any run is: a model that answers badly, loops, is cut off at the cap, or whose request the server refuses. So is a wrong answer. An outage that starts and ends inside one run is not seen. A lens call refused while the endpoint was briefly down fails that lens, and the run is recorded as one that did not complete, or as one that completed without that lens. A node's error is stored as text, so the harness does not classify it. On a provider with nothing to ask, only a run that reached no model stops the case.

A bench case in which no run completed SHALL fail, whatever the cause. The check counts the runs by why: the error of the run's first failed or rejected node, else the last line of the run's error.

#### Scenario: The endpoint is down from the start
- **WHEN** `chb agent-harness --provider openai` runs a bench case with `OPENAI_BASE_URL` on a port nothing listens on
- **THEN** the case fails with the preflight's message, whose first 160 characters name the endpoint and the refused connection; no item runs; and there is no `results.jsonl`

#### Scenario: A rerun while the endpoint is down
- **WHEN** a bench case runs again into the workspace of an earlier run, with the endpoint down
- **THEN** it fails before its first run, and the earlier `results.jsonl` is unchanged

#### Scenario: The endpoint goes down mid-case
- **WHEN** the server stops answering after it has answered N runs of a case, before run N+1 starts or inside it
- **THEN** the case stops at run N+1, naming it and the refused connection within the first 160 characters; no later run starts; and `results.jsonl` holds the N+1 rows under a `not_measured` line, which `chb bench decide` refuses

#### Scenario: A run refused at its setup
- **WHEN** a bench case runs on provider `openai` with `OPENAI_API_KEY` empty
- **THEN** nothing is asked before the first run, run 1 reaches no model, and the case stops there, naming the run and the missing key

#### Scenario: The workflows cannot be written
- **WHEN** a bench case names a forager preset that does not exist
- **THEN** it fails under its own check before its first run, with no endpoint check

#### Scenario: A model the endpoint does not serve
- **WHEN** the endpoint lists every model but Queen's, or the second model of the `--lens-model` list, or a lens model only a later run's rotation sends, or the solo control's, or the model a routing profile routes Queen to, or the evaluator's when the case sets `eval`, or the model of a node the profile does not route
- **THEN** the case fails before its first run, naming that model or that node, and no served model

#### Scenario: A wrong answer and a refused Queen
- **WHEN** a model answers every item wrongly, and the server refuses every Queen call while it still lists its models
- **THEN** every run is recorded; the solo runs completed and are graded wrong; the swarm runs did not complete; and the case does not stop

#### Scenario: No run completed
- **WHEN** the server refuses every Queen call of a swarm-only case without `require_pairs`
- **THEN** the case fails, counting every run under Queen's error, and `results.jsonl` holds every row under a `not_measured` line

### Requirement: The correlated-error rate

`bench.CorrelatedErrors` SHALL measure how often the lenses err together. A lens errs on a run when its verdict is not the item's expected verdict. A lens with no verdict is left out. Every pair of lenses in one run, both with a verdict, is compared. It counts when at least one of them erred, and counts as together when both did. The rate is together ÷ counted. It is null when no pair counted. This is a choice. It is the share of lens pairs holding an error in which the error is shared. The result also gives the pairs compared, the runs with two or more lens verdicts, and each recorded model's lens runs and verdicts.

The same pairs are also scored as if each lens erred independently. A lens's error rate p is its forager's on its model over the rows. The independent rate is Σ pᵢpⱼ ÷ Σ (pᵢ + pⱼ − pᵢpⱼ). It assumes a lens's chance of erring is the same on every item. So errors caused by the item show up as correlation: a hard item, or one model's lean toward one verdict. That is the correlation a swarm cannot vote its way out of. Both figures are given again over the cross-model pairs: both lenses have a recorded model, and the two differ. A lens with no recorded model is in no cross-model pair, since its model is unknown. In a mixed-family arm, those figures measure whether the second family errs apart from the first. The harness rotates the model list across seeds (§ The harness runs on any provider), so no persona is tied to one family. With fewer seeds and repetitions than models, some personas still run on one family only, and the cross-model figure mixes the family with those personas. Nothing here claims that the second family errs apart. The rate is reported, and the decision rule does not use it.

#### Scenario: Lenses that copy one another
- **WHEN** every lens of every run returns the same verdict, right on some runs and wrong on others
- **THEN** every counted pair erred together, and the rate is 1

#### Scenario: One model that always says support
- **WHEN** every lens of a one-model swarm answers support to an F1 twin pair
- **THEN** on the oppose twin all C(L,2) pairs of its L lenses err together, on the support twin none counts, and both runs' diversity is `low`

#### Scenario: A Queen who fails
- **WHEN** the server refuses every Queen call of a `require_pairs` case whose lenses all answer right on one model
- **THEN** no run completes or writes an artifact; each row, written under a `not_measured` line, still holds every lens's answer and diversity `low`, and the rate covers every run; the completion check fails and names each run with the last line of its error, and the pair check names no wrong answer

### Requirement: The graded canary

The default harness suite (`fixtures/agent-harness/suite.yaml`) SHALL grade the swarm's answer on a twin pair, not on one verdict. Its canary, `swarm-graded-orphan-twins`, is a bench case: family F1 at seed 1, on the swarm arm, with the `minimal` preset. The question and pack come from the live roster, and the twin carries the edit that flips the answer. It sets `require_pairs`, so it fails unless both twins are answered right. Each pair expects two different verdicts, so a model that always gives one verdict fails it. It is not slow, so `make harness` runs it. It answers from the pack with no tools. It does not test a lens reading the repository with tools; no graded case does.

A swarm case SHALL take no `grade:`. The harness refuses one before the case runs: a model that always gives the expected verdict would pass. `grade:` names a computed answer only for a hive case.

### Requirement: The profile canary

The default suite SHALL hold a second twinned case, `profile-canary`, for re-running after a model or Ollama update: a bench case on the swarm arm with the `minimal` preset, one twin pair from each of families F1, F4, F6 and F8 at seed 1, and `require_pairs`. So it fails unless every twin is answered right, and no one family's bias passes it. It is slow, so `make harness` leaves it out. `make canary PROFILE=<name>` runs it under that routing profile (`agent-harness --profile <name> --only profile-canary --slow`), `local-fast` by default. Its cost note is an estimate from Bench-0's per-call time, not a measurement of the case. It answers from the pack with no tools, as the default canary does.

#### Scenario: A model update that breaks one family
- **WHEN** `make canary PROFILE=local-fast` runs after an update under which the lenses answer the F6 twins alike
- **THEN** the F6 pair fails, and so does the case


#### Scenario: A constant model runs the default harness
- **WHEN** the canary runs against a server whose every lens answers support
- **THEN** it passes 0 of its pairs and the case fails, and a server that answers each twin right passes every pair

### Requirement: What each case costs

A harness case MAY carry a `cost:` note: what the case was measured to cost, in money and time, and when. For a swarm or bench case, the harness also counts the model calls from the live roster. That is one call per node that calls a model, with no repair, retry or tool turn. A swarm makes one call per lens and one for Queen, and one more under `--direct-voice`. The coverage pass adds the evaluator and up to three follow-ups (`foragers.DefaultFollowups`). A bench case makes that many per swarm run and one per solo run, for each item, arm and repetition. The harness SHALL print the count and the note on the line that skips a case, whether it is slow or a template case on a provider other than `claude-cli`, and on the line after the one that starts it. The report's case table SHALL show them in its Cost column. `make harness` runs the cases not marked slow. Every shipped case carries a note. A note is a measurement from the date it names, not a guarantee. A count follows the roster.

#### Scenario: Skipping a slow case
- **WHEN** `make harness` reaches `swarm-balanced-eval` without `--slow`
- **THEN** its skip line gives its model calls, the balanced lenses plus two to five, and its cost note

### Requirement: The decision rule

`chb bench decide [--rule RULE] <results.jsonl>...` SHALL apply the rule in `internal/bench.Decide`. It refuses a file marked `not_measured` (§ An outage is not a result). `--rule` defaults to `fixtures/bench/rule.yaml`. The rule file overrides the defaults in `DefaultRule`. It names the reference and the negative control, and may list the configurations a machine class can choose, with their memory and whether their schema is enforced. That default file holds the Bench-1 rule. Rows split by seed into selection and confirmation. Rows on neither list are ignored and counted.

The guards run on a configuration's swarm rows:

| Guard | Passes when |
|---|---|
| completion | the Wilson 95% lower bound of completed runs is at least 0.85 |
| no output cut off | every row records finish reasons and none stopped at the cap |
| schema-valid outputs | at least 1.0 of outputs hold their contract if the configuration is enforced, else at least 0.95 |
| queen read full verdicts | at least 0.90 of lenses |
| support accuracy, oppose accuracy | each at least 0.70 |
| abstain fabrication | the share of abstain items answered support or oppose is at most the reference's plus 0.10 |
| tree unchanged | every swarm run left its tree unchanged |

A guard with nothing to measure is unmeasured, and an unmeasured guard never passes.

Non-inferiority SHALL be tested against the reference on the twin pairs whose twins expect support and oppose, paired by item hash, with repetitions averaged first. AB pairs, whose second twin expects abstain, are left out; the fabrication guard judges abstain items. It is tested on accuracy and on token F1. For m pairs, x is each pair's support-twin difference and y its oppose-twin difference, candidate minus reference. The difference is (mean x + mean y)/2. Each pair holds one twin of each class, so this is the class-balanced difference on those items.

The pair is the unit, so errors a model makes on both twins of a pair do not count as two independent observations: SE² = Var(x/2 + y/2)/m. Two floors keep a sample whose differences happen to be equal from collapsing the bound onto the mean. Each twin's variance is floored at δ(1−δ). That is its variance when the reference never errs and the candidate errs on that twin at rate δ. The covariance is the sample correlation of x and y times the two floored standard deviations, or 0 when either sample variance is 0. The pair's variance is floored at δ(1−2δ)/2, the least variance a mean of two −1/0/1 differences can have when its mean is −δ. The floors are a choice, and they apply to token F1 too. δ must lie in (0, 0.5). t is the 1−α quantile of Student's t on m−1 degrees of freedom. The test passes when the difference minus t·SE is above −δ. It fails when the difference plus t·SE is below −δ. Otherwise it is inconclusive. It needs two pairs, or the margin is unmeasured.

The rule decides in this order:

1. **Validity.** The reference must pass every guard. The negative control must be rejected, which means it fails a check of its answers: the support or oppose floor, the abstain fabrication guard, or the accuracy margin. A process guard (completion, cut-off outputs, schema, full verdicts, tree) does not count, because failing one says nothing about whether the items tell a bad model from the reference. Confirmation items must not repeat selection items; the reason names the `exclude_seeds` that prevent it. Otherwise the run is void. When the reference's guards pass but some are unmeasured, the run is inconclusive and nothing is chosen.
2. **Outcome per configuration.** A failed guard gives `reject-guard`. A failed margin gives `reject-margin`. An unmeasured guard, an open margin or a missing solo control gives `inconclusive`. A swarm that does not beat its own solo control (lift ≤ 0) gives `one-call`. Otherwise the outcome is `passed`. The reference is a candidate only when the rule lists it among the configurations.
3. **Choice per machine class.** Among passed configurations that fit the class's memory, the rule picks the lowest median swarm wall time. Within `wall_tie` of it, it picks the least memory.
4. **Confirmation.** The choice and the reference are re-evaluated on the confirmation seeds. A pass gives `accept`. A failure gives `inconclusive`, and the next configuration is tried. Missing confirmation runs give `pending`.

"inconclusive" is a normal outcome: it says the runs cannot tell.

#### Scenario: The negative control passes
- **WHEN** the negative control fails no class floor, no fabrication guard and no accuracy margin
- **THEN** the verdict is `void` and nothing is chosen, even if it failed a process guard such as schema-valid outputs

#### Scenario: Finish reasons are not recorded
- **WHEN** the rows carry `finish_length: null`, as a run on the Gemini CLI does
- **THEN** the reference's guard is unmeasured, the verdict is `inconclusive`, and nothing is chosen

#### Scenario: A lens cut off at the cap
- **WHEN** a swarm run's server answers one lens call with `finish_reason: length`
- **THEN** that run's row carries `finish_length: 1`, and the configuration's "no output cut off" guard fails; a configuration whose rows all carry 0 passes it

### Requirement: What the bench does not claim

The items are structural questions about this repository. Passing them does not show quality on open questions. That is a bet, not a measurement. Each pack describes a roster that may differ from the personas' own bodies. A lens that answers from its persona rather than the pack is graded wrong, by design.

The margin's level is not exact, and it is stated per case. Bench-1 has 24 support/oppose pairs. Against a reference that never errs, a configuration exactly δ worse passes the accuracy margin with probability 0.045 when its errors on a pair's two twins are independent. It is 0.035 when they never fall on both twins, 0.020 when they always do, and 0.012 when only oppose twins err. `TestMargin_LevelAtTheMargin` computes these exactly and fails above α. `TestMargin_SimulatedLevelAndPower` simulates a reference that errs 5% or 10% of the time. There the rate is 0.046 to 0.054, and 0.067 when the candidate's errors hit both twins together. That test fails above 1.5α. Other error models are not computed. The floors and the t quantile cost power. In the same simulation, a configuration 5 points worse than a reference that never errs passes 71% of the time. One equal to a reference, both with 10% independent errors, passes 75%. Expect inconclusive results. A false accept must pass on the selection seeds and again on the confirmation seeds. If the two splits are independent draws, that roughly squares the rates above. That is a bet, not a measurement.

Whether `claude -p` writes into its working directory has not been checked. If it does, claude-cli swarm cases fail the tree check and name the path.

## Files

- `internal/bench/roster.go`, `families.go` — the pack, the families, `Generate`, `GenerateAfter`
- `internal/bench/grade.go` — token extraction, `Grade`, `Row`, `Summarize`, and the `not_measured` line (`WriteNotMeasured`, `ReadRows`)
- `internal/bench/decide.go` — `Rule`, `Decide`
- `internal/bench/grade.go` — also `LensAnswer`, `CorrelatedErrors`
- `internal/cli/agent_harness.go` — the harness flags, which build `harness.Options`; `internal/harness/harness.go` — `Run`, the providers and swarm case, the flag check (`checkModelFlags`), the cost line (`caseCost`, `nominalCalls`)
- `internal/harness/contract.go` — the verdict contract in a persona's frontmatter (`loadPersonaContract`) and the checks a verdict is held to (`checkVerdictContract`), which decide whether a lens's output held its contract
- `internal/harness/tree.go` — private trees, the tree check, node stats
- `internal/harness/bench.go` — the bench case, its run directories, the solo control, `finishLength`, the list's rotation (`benchLensModels`), `require_pairs`, and the endpoint check (`benchWorkflows`, `checkEndpoints`)
- `internal/harness/sweep.go` — what the bench and design cases share: the routing their runs resolve under (`harnessRouting`), the model preflight before the first run (`preflightEndpoint`), the sweep that stops a case at an outage (`harnessSweep`, `harnessOutage`, `finishSweep`), and `results.jsonl` written as a measurement or under `not_measured` (`writeHarnessResults`, `checkHarnessResults`)
- `internal/harness/report.go` — `REPORT.md` and `report.json` (`writeReport`): the run's inputs, the case table with its Cost column, and each case's verdicts, nodes, bench or design report and checks
- `internal/cli/bench.go` — `chb bench decide`
- `fixtures/bench/suite.yaml`, `fixtures/bench/rule.yaml` — the Bench-1 items and rule
- `fixtures/agent-harness/suite.yaml` — the default suite, its canary, the profile canary and its cost notes
- `internal/harness/harness.go` — also `--profile` (`useProfile`); `Makefile` — `make canary`
- `internal/bench/*_test.go`, `internal/harness/bench_test.go` — the fake-server policies, the six decision fixtures, the margin's level, the shipped suite reaching accept, a lens cut off at the cap failing the finish guard, the canary, the mixed-family arm, the failed Queen and the rotation; the solo control and the direct voice are one prompt
- `internal/harness/sweep_test.go` — an endpoint down from the start, on a rerun, and going down after N runs or inside run N+1; a run refused at its setup; workflows that cannot be written; a model it does not serve; wrong answers and a refused Queen recorded; and no completed run
- `internal/harness/harness_test.go` — the call counts, the cost notes, the printed cost lines, the flag check and the refused swarm grade; `--direct-voice` and `--context-split` passed through, named in the configuration and counted
