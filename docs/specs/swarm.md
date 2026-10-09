# Swarm of Foragers Specification

## Purpose

A swarm is a generated workflow that runs N analytical lenses — foragers — on one question in parallel and hands their verdicts to a synthesizer, the Queen. Each forager is a markdown file: frontmatter declares what it owns and who it is bonded to, and the body is its persona. The filesystem is the registry; adding a forager takes no code change.

## Requirements

### Requirement: Forager files

A forager SHALL be one `foragers/<name>.md` whose YAML frontmatter opens the file between `---` markers, followed by the persona body. Only `name` is required. It should be lowercase, and it is unique in the directory ignoring case, the same key lookup uses. The registry reads `name`, `title` (defaults to the capitalised name), `description` (its first line is the lens shown in the prompt), `default`, `tags`, `coverage`, `archetype` (`lens` \| `dreamer` \| `synthesizer`, default `lens`), `bonds` / `depends_on`, `jungian`, `ifs_role`, `sigil` (default `•`), `accent` (default `#9098A8`, a bare RGB gains its `#`), `render_layer` and `deliberation_eligible`. A forager is deliberation-eligible unless it is a synthesizer, `render_layer: true`, or `deliberation_eligible: false`.

The registry SHALL also read `output_schema`, the JSON Schema subset a workflow node takes (workflow.md § Output schema), in the order the frontmatter lists its properties, and `length_caps`. Each `length_caps.<field>.max_items` becomes that property's `maxItems`. `max_chars`, `max_chars_each` and `must_name_one_of` stay adherence checks for `chb agent-harness`: a grammar holding a string to a length would clip it mid-sentence. A `length_caps` field the schema does not declare, a cap key outside those four, a `max_items` that is not a whole number, a `max_items` on a property that is not an array, or a schema outside the subset, is a contract error. With no `output_schema`, `length_caps` is not read. A file with a contract error still loads, so naming it is not an unknown forager, and generation refuses a swarm or a synthesizer holding one, naming it and the key. The registry also reads `behavioral_floor.counter_bias_clause`, and `behavioral_floor.counter_bias_when_absent`, the forager whose absence from the swarm the clause is conditioned on (§ Workflow generation). A `behavioral_floor` that is not a mapping gives neither. Every other key — the other template-v1 keys below — is read by `chb validate-personas`, not by the registry.

#### Scenario: A forager with only a name
- **WHEN** a file declares `name: economist` and a body
- **THEN** it loads, `chb list` shows it, and `--foragers default,economist` dispatches it

#### Scenario: A cap on a field the schema lacks
- **WHEN** a persona's `length_caps` holds `key_points: {max_items: 3}` and its `output_schema` declares no `key_points`
- **THEN** it loads, and `chb ask --foragers <it>` fails naming the persona and `length_caps.key_points`

### Requirement: Discovery

The binary carries the shipped `foragers/` (the module's root package, `hive.Foragers`), and `Resolve` SHALL pick the tree `Load` reads in this order, a definition: `HIVE_FORAGERS_DIR` when set, whatever it holds; else the first directory holding a forager file (a `*.md` other than `README.md`) among `foragers/` under the working directory, then `foragers/`, `../foragers` and `../share/chb/foragers` beside the binary; else the carried copy. `chb validate-personas --dir` names a directory outright. The carried copy SHALL equal the repository's `foragers/` file for file (`assets_test.go`), so it cannot drift. `chb` says on stderr, once per process, when it reads the carried copy.

`Load` SHALL read every `*.md` at the root of the tree in file-name order and return the foragers sorted by name. `README.md` is skipped by name; a file with no opening `---`, unparseable frontmatter, no `name`, an unknown archetype or a malformed bond is skipped silently, and naming it is then an unknown forager. Two files declaring one name, compared ignoring case, resolve to the first file by name, and every preset and name lookup reaches that file. A missing directory yields no foragers and no error. A forager's `File` is its file name within the tree.

#### Scenario: A duplicate name
- **WHEN** `a-optimist.md` and `b-optimist.md` both declare `name: optimist`
- **THEN** the registry holds one optimist, the one from `a-optimist.md`

#### Scenario: No folder on disk
- **WHEN** `chb list` runs in a directory with no `foragers/`, none beside the binary and `HIVE_FORAGERS_DIR` unset
- **THEN** it lists the carried foragers, and stderr says once that it read the copy the binary carries

#### Scenario: A folder on disk overrides
- **WHEN** `foragers/` under the working directory holds `alpha.md`
- **THEN** the registry holds alpha and none of the carried foragers

#### Scenario: The variable wins
- **WHEN** `HIVE_FORAGERS_DIR` names a directory holding `gamma.md` while `foragers/` under the working directory holds `alpha.md`
- **THEN** the registry holds gamma; and when it names an empty directory, the registry holds nothing and `chb list` fails naming the directory, not the carried copy

### Requirement: Presets

`--foragers` SHALL accept forager names and four pseudo-names, resolved against the shipped roster of 15 files, 13 of them deliberation-eligible (Editor is render-layer, Queen a synthesizer):

| Pseudo-name | Selects | Today |
|---|---|---|
| `balanced` | `minimal` plus Optimist and Historian | 9 |
| `default` | every deliberation-eligible forager with `default: true` | 10 |
| `minimal` | every deliberation-eligible forager declaring any `coverage:` axis | 7 |
| `all` | every deliberation-eligible forager | 13 |

Names and pseudo-names combine (`--foragers balanced,framer`), and an unknown name SHALL be refused with the full roster and the four pseudo-names listed. `chb ask` defaults to `balanced`; `chb replicate` to `default`. Naming a render-layer forager adds it as a lens. Naming a synthesizer SHALL be refused: the generator already emits the Queen node, so it would run twice.

#### Scenario: Naming the Queen
- **WHEN** `chb ask --foragers balanced,queen`
- **THEN** generation fails, naming Queen as the synthesizer every swarm already runs

### Requirement: Axis ownership

`coverage:` SHALL declare the axes a forager owns, for the `minimal` preset and the roster audit: `wasp` (`k` structural workload, `k_execution` its execution half, `E`, `I`, `T`, `F`), `cde` (`detect`, `decompose`, `encode`, `execute`) and `mss` (`def`, `gua`, `asm`, `unk`). The vocabulary is a convention: nothing validates the value, and any non-empty scalar `wasp`, `cde` or `mss` under `coverage:` puts a deliberation-eligible forager in `minimal`. Any other key under `coverage:` is ignored. A list value such as `wasp: [k, E]` makes the frontmatter unparseable, so the file is skipped. Overlap is allowed — Pragmatist and Steward both declare CDE-execute, and their § 4 and § 6 body prose, which mirrors their `non_overlap_with` keys, divides them at dispatch.

The shipped roster owns: Architect `wasp: k`; Skeptic `wasp: E`, `mss: unk`; Pragmatist `wasp: k_execution`, `cde: execute`; Timekeeper `wasp: T`; Empiricist `wasp: F`, `cde: detect`, `mss: def`; Scholar `cde: decompose`, `mss: gua`; Steward `cde: execute`, `mss: asm`; Queen `cde: encode`, which no preset dispatches as a lens. **No shipped forager declares WASP-I** — Editor, which sits in the render layer, owns no axis — so no preset is axis-complete. `internal/foragers/shipped_roster_test.go` pins the gap and fails if an owner appears.

#### Scenario: WASP-I gains an owner
- **WHEN** a deliberation-eligible forager declares `wasp: I`
- **THEN** the roster test fails, so this spec and `foragers/README.md` are updated with it

### Requirement: Bonds

`bonds:` SHALL declare typed dependencies — `cites`, `contradicts` or `resonates`, with an optional weight (default 1.0); `depends_on: [name]` is shorthand for `cites`. Generation SHALL render `cites` as an edge from the upstream forager plus its one-line digest (`{comb.forager:<name>}`) in the dependent's prompt, `contradicts` as the same edge under an inversion preamble, and `resonates` as no edge at all — instead a top-level `resonates:` list the runner registers with the ∇ convergence sensor (comb.md) and the engine reads to compute the Queen's `{nabla.fired}`. The list holds every forager's `resonates` pairs.

`ValidateSwarm` SHALL refuse, before generation: a `cites` or `contradicts` bond on a forager outside the swarm (a `resonates` bond may name an absent forager, since it only registers a sensor pair), a cycle in the cites/contradicts graph, a bond of any kind onto a dreamer in the swarm or declared by one — dreamers run after Queen, so their verdicts cannot reach a lens prompt; a dreamer takes no upstream digest; and a dreamer's verdict is always `abstain`, so a `resonates` pair with one could never fire — and a synthesizer in the swarm. So no declared bond is dropped without a message.

A preset is **bond-complete** when every `resonates` pair has both halves present. No shipped preset is: `minimal` leaves 6 pairs half-present and `balanced` 2 (Architect⇄Framer, Editor⇄Timekeeper). Bond-completeness and axis-completeness are independent. The shipped roster SHALL declare no `resonates` bond with a dreamer: a dreamer's verdict is always `abstain`, which never counts toward a ∇ (comb.md), so the pair could never fire. `internal/foragers/shipped_bonds_test.go` pins this.

#### Scenario: A lens citing a dreamer
- **WHEN** a lens declares `bonds: [{to: dreamer, kind: cites}]` and both are in the swarm
- **THEN** generation fails, naming the persona prose as the place for the contrast

#### Scenario: A lens resonating with a dreamer
- **WHEN** a lens declares `bonds: [{to: dreamer, kind: resonates}]` and both are in the swarm
- **THEN** generation fails, naming the dreamer, and no prompt tells the lens it can converge with it

### Requirement: Template-v1 personas

`chb validate-personas` SHALL hold ten personas — the balanced-9 and Queen — to template-v1, and any other forager the `balanced` preset selects. It skips every other persona, naming those it skipped. It checks the file the registry loaded for each name, which is the file the swarm dispatches. One of the ten that the registry did not load, because its file is missing or its frontmatter does not parse, is a violation. For each in scope it checks the frontmatter keys `axis`, `non_overlap_with`, `model_tier_floor`, `behavioral_floor`, `forbidden_phrases`, `output_schema`, `length_caps` and `abstain_triggers`; that `output_schema` and `length_caps` parse as the registry reads them (§ Forager files); that a synthesizer's schema declares a string `report`, where its Markdown synthesis goes; the body markers `## § 1` through `## § 8` present and in order (output contract, decision rubric, worked example, anti-patterns, tie-breaker, bonds in prose, emission guard, lens lore); and the locked guard string verbatim inside § 7, not merely somewhere in the body. Every persona in scope returns one JSON object, the synthesizer included, so every one carries the same guard:

> Respond with the JSON object specified in §1 Output contract and nothing else. No prose preamble. No trailing commentary. No markdown code fences unless §1 explicitly specifies them. If you would otherwise abstain, emit the abstain JSON per §1, do not emit prose.

Each §3 worked example answers one shared canary question — *"Should `chb guard --wave N` block synthesis when the active preset contains any orphan ∇-resonance pair, or merely warn?"* — through that persona's own axis. Any failure is a violation and exits non-zero; `--strict` also fails on warnings. The swarm sends `output_schema`, with `max_items` compiled in, on the persona's node, and the runner checks each reply against it (workflow.md § Output schema). The rest of the contract is checked by no one at dispatch: `chb agent-harness` measures whether a live model honoured `forbidden_phrases` and the `length_caps` character caps, reporting a rate rather than failing (`--strict` makes it fail), and skips a `forbidden_phrases` that is not a list.

A persona body is inlined into its node's prompt, where the engine and the runner fill every `{…}` token they know. So no shipped persona body SHALL hold one; it names a token in plain words instead. A test over `foragers/*.md` holds this, and `chb validate-personas` does not check it.

#### Scenario: Drifting guard text
- **WHEN** a persona's §7 guard differs by one word, even with the verbatim guard quoted elsewhere in its body
- **THEN** `chb validate-personas` reports a violation and exits non-zero

#### Scenario: A contract that does not parse
- **WHEN** Architect's `length_caps` reads `key_points: {max_items: seven}`
- **THEN** `chb validate-personas` reports one violation naming `length_caps.key_points.max_items`

#### Scenario: A scoped persona that does not load
- **WHEN** `queen.md` is deleted, or its frontmatter does not parse
- **THEN** `chb validate-personas` reports a violation naming Queen and exits non-zero

### Requirement: Persona profiles

`--persona-profile` SHALL choose how much of each persona a swarm's prompts carry. `full`, the default, renders each persona body whole, with its Jungian and IFS lines, and Queen and `swarm-evaluate` read every forager's full verdict. `lean` renders the text before § 1 and body sections 1, 2, 4, 5 and 7: the output contract, the decision rubric, the anti-patterns, the tie-breakers and the emission guard. It leaves out the worked example, the bonds in prose (the prompt states the bonds) and the lens lore, and drops the Jungian and IFS lines. Queen and `swarm-evaluate` read `{swarm.ledger}` in place of the verdict tokens (comb.md § Template tokens), and are told the axes no lens in the swarm owns. Queen still reads `{tally}`, `{nabla.fired}` and `{diversity}`, so her `accept:` holds her verdict to the plurality under either profile, and the ledger, which names no model, needs no diversity line of its own. The profile renders every lens the same way whatever model a comma-separated `--model` gives it (§ Workflow generation).

`--persona-sections`, such as `1,2,3,4,5,7`, SHALL replace the profile's sections for every forager and Queen, for ablation. The profile's other choices stay. A section left out that a kept section refers to (`§ 3` or `§3`, not a dotted `§2.10`) is written as its marker line and `(left out of this prompt)`, so no reference points at text the prompt does not hold. Queen's § 1 refers to § 3, so a lean Queen holds that line; the report's sections are listed by her node's prompt. A persona with no `## § N` markers renders whole, and generation warns, naming it.

The lean profile is a choice made for small context windows (runner.md § Context window guard). On the captured balanced run with the coverage pass, it cuts each forager's prompt by about half and the swarm's prompts by about 40%; `TestReplayBalancedEval_LeanProfile` logs every node's size under both. Whether a swarm answers as well under lean is not measured. A Bench-1 ablation (bench.md, the harness's `--persona-profile`) must show lean non-inferior to full before lean is anyone's default.

#### Scenario: A lean swarm
- **WHEN** `chb ask "q" --persona-profile lean`
- **THEN** every forager's prompt holds its § 1, § 2, § 4, § 5 and § 7 and none of § 3, § 6 or § 8; `queen` and `swarm-evaluate` hold `{swarm.ledger}` and no `{verdict.forager:…}`; `queen` also holds `{tally}`, `{nabla.fired}` and `{diversity}`

#### Scenario: A reference to a section left out
- **WHEN** the lean profile renders Queen, whose § 1 says "see § 3 Worked example"
- **THEN** her prompt holds `## § 3 — Worked example (left out of this prompt)` and not the example

#### Scenario: A persona without sections
- **WHEN** `chb ask "q" --foragers all --persona-profile lean`
- **THEN** forecaster, framer and surveyor render whole, and ask warns naming each

### Requirement: Workflow generation

`GenerateWorkflow` SHALL emit a workflow YAML in which each lens forager is one `forager-<name>` node of `type: agent`, carrying the forager's title, lens, archetype tags, persona body as its profile renders it (§ Persona profiles), its bonded upstreams' digests, and the verdict contract. What a persona states conditionally is resolved when the workflow is generated, under either profile, so the model is never left to work out whether it holds:

- the `resonates` line names the partners in the swarm, with whom a ∇ can fire, apart from those outside it, with whom none can;
- the persona's `counter_bias_clause` follows its persona under `## § behavioral_floor — counter-bias clause`, the heading its prose refers to as §behavioral_floor. A clause with `counter_bias_when_absent: <name>` is written as applying, with the clause, when that forager is not in the swarm, and as not applying when it is. Skeptic's is conditioned on Optimist.

The verdict contract:

- `outputs: [verdict, key_points, evidence, uncertainties, recommendation]`
- `accept:` constraining `outputs.verdict` to `support|oppose|conditional|abstain`
- `on_reject:` allowing one repair
- when the persona declares one, `output_schema`: the persona's schema with every property but `verdict` and `recommendation` in the persona's order, then `verdict`, then `recommendation` (a choice). A llama.cpp grammar writes required properties first, in declared order, and optional ones after them (runner.md § Output schema on the wire). So a model under one writes the required reasons (`key_points`, `evidence`, `uncertainties`) before its decision, and a persona's optional fields (Skeptic's `unk_claims`, Scholar's `references`, and the like) after it. The prompt lists the required keys in that order

Every node that calls a model — scope, the foragers, `swarm-evaluate`, `swarm-followup` and `queen` — SHALL run as `agent: swarm`, apart from the direct voice, which runs with no system prompt as the bench's solo control does (§ The direct voice), and SHALL name its role, which a routing profile routes it by (runner.md § Routing profiles): `scope`, `lens` on each forager, `evaluate`, `followup` and `queen`. `agents/swarm.md` is a system prompt of about two hundred characters, answer as the persona the message names and return only the JSON object the node asks for, chosen over the report-writing personas in `agents/` because an instruction to write a Markdown report would contradict that contract. Each of those nodes SHALL carry a `tools:` allowlist (runner.md § Tool allowlist). Scope, `swarm-evaluate` and `queen` get `tools: []`. The foragers and `swarm-followup`, the lenses, get what `--lens-tools` names: `none` (the default) writes `tools: []`; `read` writes `tools: [read_file, glob, grep]`, for a question the lenses answer by reading the repository; `all` writes no `tools:` key, so the lens keeps every tool. A value outside the three is refused.

Every LLM node SHALL name a tier, not a model, unless a model is pinned. Foragers and `swarm-followup` get the forager tier: `--forager-tier`, else `synthesist`; `--model` pins `model:` instead. A comma-separated `--model` mixes models across the lenses: the lens foragers, in name order, take the models in turn, and `swarm-followup` takes the first. An empty entry in the list is refused. Queen and the `scope` and `swarm-evaluate` nodes get the synthesizer tier: `--synthesizer-tier`, else `planner`; `--synthesizer-model` pins `model:` instead. So `--budget-mode` dials the whole swarm (runner.md). `--forager-reasoning` writes `reasoning:` on the foragers and `swarm-followup`, and `--synthesizer-reasoning` on Queen, `scope` and `swarm-evaluate` (runner.md: the OpenAI-compatible backend sends it as `reasoning_effort`). Each is `none`, `low`, `medium` or `high`, and any other value is refused. Unset, no key is written and the server's default applies; a Qwen model on Ollama then thinks. The `human-gate` and `dreamer-<name>` nodes call no LLM and name neither. The `queen` node reads `foragers/queen.md` as its prompt when the caller supplies the Synthesizer forager — `chb ask` and `chb generate` do — else an inlined fallback intro. In both it SHALL read, under the full profile, each forager's full verdict through `{verdict.forager:<name>}`, and under lean the swarm ledger through `{swarm.ledger}` (§ Persona profiles); and under either, the engine's vote count through `{tally}` and the run's fired ∇ pairs through `{nabla.fired}` (comb.md § Template tokens), labelled as identical verdict strings rather than agreement in substance. Under either it reads `{diversity}`, whether one model produced a unanimous swarm, and is told to say so in Consensus when the line says low. The queen prompt SHALL carry `{calibration.lenses}` (comb.md § Template tokens) in both the persona and the fallback prompt, once, glued to the end of the diversity paragraph, so that its empty resolution leaves the prompt byte-identical to one without it; no other node carries it. A calibration weight SHALL change a lens's influence on the synthesis, never whether it runs. `swarm-evaluate` reads the verdicts or the ledger as Queen does. The persona's own prose names none of these tokens, since the engine would fill them there too. It returns one JSON object: `report` (the Markdown Verdict: Swarm Verdict, The Question, Consensus, Disagreements, Unique Insights, ∇ Convergences — the pairs `{nabla.fired}` lists and no others — Coverage & Gaps, Follow-Up Questions, Recommended Action), then `convergence`, `coverage`, `gaps`, `dissent_from_plurality`, `verdict` and `recommendation`. Its `output_schema` is Queen's, ordered as a lens's is, or the fallback prompt's same shape. Every one of its properties is required, so a grammar writes them all in that order, `report` first and the decision last. Its `accept:` constrains the verdict to the enum and holds it to the tally: `tally_plurality == '' or outputs.verdict == tally_plurality or (outputs.verdict == 'abstain' and tally_abstentions >= tally_votes) or len(outputs.dissent_from_plurality) >= 20`. The `tally_` values are the engine's count, written to state (workflow.md § Accept predicates). So a verdict that departs from the plurality in silence is rejected and repaired once. Abstain casts no vote. A tie, or a tally of abstains, leaves no plurality, and any verdict passes. Queen SHALL be able to abstain with no dissent when at least as many lenses abstained as voted. A lens votes when its verdict is not abstain. A lens with no verdict counts as neither. That is a definition, not a finding. A silent abstain overrides the lenses that took a position only when they are a majority of the lenses with a verdict. A majority is more than half, so a tie is not one. Any other departure needs a dissent: a `support`, `oppose` or `conditional` against the plurality, however many lenses abstained, or an abstain when more lenses voted than abstained. Two alternatives fall short. Comparing the abstentions with the plurality's votes alone lets her abstain in silence over a committed majority that splits, such as oppose 3, conditional 1, abstain 3. Letting her always abstain in silence lets her overrule any committed majority unseen. The rule chosen gives up one thing: when abstain is the largest group but short of half, such as abstain 3, support 2, oppose 1, conditional 1, she must write a dissent to abstain. Her prompt and `foragers/queen.md` ask for a dissent on every departure and do not state the exception. A dissent of 20 characters or more always passes. Stated, the exception would invite a silent abstain where lenses took a position. The predicate carries a reason. Her repair prompt gives it in plain words before her original prompt, and again as its last lines, after her failed reply: `The foragers' plurality was <p>; you answered <v>. Either answer <p>, or write in dissent_from_plurality, in a sentence of at least 20 characters, which forager's evidence outweighs it. The tally: <the tally line>.` The prompt leaves the predicate out, since it names state she never saw. The repair's `failure_reason` keeps it after the reason. The 20-character floor (`foragers.MinDissentChars`) is a choice: it refuses the placeholders small models write into a field they mean to leave empty, such as `None`, `N/A` or `Not applicable`, and it cannot tell a sentence that gives a reason from one that does not. `scope` returns `{assumptions, scoped_question}` and gets `on_reject` with one attempt; `swarm-evaluate` returns `{gaps, coverage}`; each `swarm-followup` item returns `{followup_findings}`. Each carries that shape as its `output_schema`.

`swarm-evaluate` and `queen` SHALL declare `join: settled` (workflow.md § Edge conditions and readiness). A lens that failed, or was rejected after its repair, does not stop synthesis: they run once the other lenses finish, read `(no verdict: forager-<name> is <status> in this run)` for it, and the tally names it. Queen also runs past a follow-up fan that failed. The run completes when they do. An evaluator rejected after its repair leaves the fan, and so Queen, waiting, and the run fails. A lens that `cites` or `contradicts` a lens that failed never runs, so the run still fails then; only surveyor declares such bonds today, in the `all` preset.

Lens nodes fan in to one node and there are no cycles. Four options extend the shape:

| Option | Shape |
|---|---|
| Evaluate (`chb ask`, on unless `--no-eval`) | the lenses fan into `swarm-evaluate`, which scores coverage 1–5 and names gaps, most important first. It returns no verdict: its edges test its gaps, so COMPLETE is `len(gaps) == 0`, computed. With gaps, `swarm-followup` runs, a `parallel_fan` over `gaps` with `fan_limit` set to `--followups` (default 3): one fresh lens for each of the first K gaps. The engine writes the rest to `gaps_unfollowed` when the evaluator returns its gaps, so the list is there however the fan ends. Then `queen` reads `{gaps}`, the joined follow-up findings as `{followup_findings}`, where every one of the K items keeps a section, a failed one saying why it has no finding, and `{gaps_unfollowed}` by name. She is told to name in Coverage & Gaps every gap no follow-up answered. The evaluator's `state_updates` set `followup_findings` to a line saying no follow-up lens ran or finished, which the fan replaces when it completes. So with no gaps, when the fan is skipped and `queen` runs straight after the evaluator, or when the fan fails, her prompt holds no placeholder and says why there are no findings. Synthesis happens once. `swarm-evaluate`'s schema requires `gaps` to be a list of strings, its `accept:` requires a `gaps` key that is not null (an empty list passes), and its `on_reject:` allows one repair. So a reply without gaps, or with gaps that are not a list, is repaired rather than sending the follow-up fan the literal `{gap}` |
| Scope (`--scope`) | a `scope` node sharpens the question into one decidable sentence plus named assumptions, and every forager reads `{scoped_question}`. Its edge is added only to foragers with no `cites`/`contradicts` bond, so it never releases a forager ahead of an upstream |
| Human gate (`--human`) | a `human_review` node, `human-gate`, after `queen`, answered with `chb workflow resume <run_id> approve\|reject\|redirect`. Every dreamer node waits on it, so a reject halts the ripening pass |
| Dreamers | each dreamer-archetype forager in the swarm becomes a `dreamer-<name>` node after `queen`, or after `human-gate` when the gate is on (comb.md) |

#### Scenario: A persona's schema on its node
- **WHEN** `chb ask` generates a swarm holding Skeptic, whose schema lists `forager, verdict, key_points, evidence, uncertainties, unk_claims, recommendation`
- **THEN** `forager-skeptic` carries `output_schema` with properties `forager, key_points, evidence, uncertainties, unk_claims, verdict, recommendation`, `key_points` capped at `maxItems` 7 from its `length_caps`, and `on_reject` with one attempt; a grammar writes the optional `unk_claims` after `recommendation`

#### Scenario: A swarm with no model pinned
- **WHEN** `chb ask "q"` generates with neither `--model` nor `--forager-tier`
- **THEN** every forager node carries `tier: synthesist` and the queen node `tier: planner`

#### Scenario: An evaluator reply without gaps
- **WHEN** `swarm-evaluate` returns a coverage score and no `gaps` key
- **THEN** its `output_schema`, which requires `gaps` as a list, rejects the reply before its `accept:` would, and the runner makes one repair attempt, so `swarm-followup` does not run on the literal `{gap}`

#### Scenario: More gaps than follow-ups
- **WHEN** `swarm-evaluate` names 8 gaps in a default `chb ask` swarm
- **THEN** `swarm-followup` makes 3 calls, on the first 3 gaps, and Queen's prompt lists the other 5 as the gaps no follow-up lens was sent to

#### Scenario: A lens rejected after its repair
- **WHEN** one forager of a default `chb ask` swarm answers outside the verdict enum on its dispatch and its repair
- **THEN** `swarm-evaluate` and `queen` still run, each reads that the forager was rejected, the tally names it, and the run completes

#### Scenario: A follow-up call fails
- **WHEN** one of the 3 follow-up calls fails and the other 2 answer
- **THEN** Queen's `{followup_findings}` holds all 3 items in order, the failed one as `(no finding: the call failed: <error>)`

#### Scenario: No gaps
- **WHEN** `swarm-evaluate` returns an empty gaps list
- **THEN** `swarm-followup` is skipped, Queen runs next, and her prompt holds no literal `{followup_findings}` or `{gaps_unfollowed}`

#### Scenario: The Queen is told the count
- **WHEN** `chb generate` renders a swarm with or without the Queen persona, under the full profile
- **THEN** the queen node's prompt holds `{tally}`, `{nabla.fired}` and `{diversity}`, and every lens's `{verdict.forager:<name>}`

#### Scenario: No calibration data
- **WHEN** no lens in the swarm has a calibrated score
- **THEN** the Queen's resolved prompt is byte-identical to the prompt without `{calibration.lenses}`, with or without her persona

#### Scenario: Two model families across the lenses
- **WHEN** `chb ask --foragers minimal --model qwen3.5:4b,ministral-3:8b` generates its swarm
- **THEN** architect, pragmatist, skeptic and timekeeper run on qwen3.5:4b, empiricist, scholar and steward on ministral-3:8b, and `swarm-followup` on qwen3.5:4b

#### Scenario: One model agrees with itself
- **WHEN** every lens of a swarm runs on qwen3.5:4b and returns support
- **THEN** the Queen's diversity line opens `low:` and names the model and the verdict, and the artifact holds `diversity: "low"`

#### Scenario: Thinking off for a Qwen swarm
- **WHEN** `chb ask --model qwen3.5:4b,ministral-3:8b --forager-reasoning none --synthesizer-reasoning none` generates its swarm
- **THEN** every forager, `swarm-followup`, Queen, `scope` and `swarm-evaluate` node carries `reasoning: none`

#### Scenario: Skeptic without Optimist
- **WHEN** `chb ask "q" --foragers minimal`, which leaves Optimist out
- **THEN** `forager-skeptic`'s prompt says Optimist is not in this swarm and holds Skeptic's counter-bias clause, and names optimist among its `resonates` partners outside the swarm; under `balanced` it says the clause does not apply

#### Scenario: A Queen who departs from the tally in silence
- **WHEN** the tally's plurality is `support` and Queen returns `oppose` with an empty `dissent_from_plurality`, or with `N/A`
- **THEN** her `accept:` rejects the reply and the runner makes one repair attempt

#### Scenario: A Queen who abstains with her lenses
- **WHEN** six lenses abstain and one opposes, and Queen returns `abstain` with an empty `dissent_from_plurality`
- **THEN** her `accept:` passes and no repair runs

#### Scenario: A Queen who abstains while most lenses voted
- **WHEN** three lenses support, two oppose and two abstain, and Queen returns `abstain` with an empty dissent
- **THEN** her `accept:` rejects the reply

#### Scenario: A Queen who abstains over a split majority
- **WHEN** three lenses oppose, one says conditional and three abstain, and Queen returns `abstain` with an empty dissent
- **THEN** her `accept:` rejects the reply: four lenses voted and three abstained

#### Scenario: A lens with no verdict is no abstention
- **WHEN** two lenses support, one abstains and two were rejected, and Queen returns `abstain` with an empty dissent
- **THEN** her `accept:` rejects the reply

#### Scenario: A Queen who commits while most lenses abstain
- **WHEN** six lenses abstain and one opposes, and Queen returns `support` with an empty dissent
- **THEN** her `accept:` rejects the reply

#### Scenario: The Queen's repair in plain words
- **WHEN** three lenses support, two oppose and two abstain, and Queen returns `conditional` with an empty dissent
- **THEN** her repair prompt says `The foragers' plurality was support; you answered conditional.`, says to answer support or name the forager whose evidence outweighs it, and gives the tally with each lens's name
- **AND** it says so before her original prompt and again after her failed reply, and does not show the predicate

#### Scenario: Rejecting at the human gate
- **WHEN** `chb ask "q" --human` runs a swarm with a dreamer and the gate is answered `reject`
- **THEN** the dreamer node never runs

### Requirement: The direct voice

`--direct-voice` on `chb ask` SHALL add one lens node, `forager-direct`, with no persona: the model's own answer to the question and the whole context. Its prompt is `foragers.DirectPrompt`, the bench solo control's (bench.md § The swarm, the solo control and the results): the question, the context, and the lens contract's `verdict`, `key_points` and `recommendation`, under `foragers.DirectSchemaJSON`, with `tools: []`, one repair and the verdict enum in its `accept:`. It names no `agent:`, so it runs with no system prompt, as the solo control does. It names `role: lens`; it runs on the first model of `--model`, else the forager tier, at `--forager-reasoning`. It reads `{question}` as asked, under `--scope` too, and `{context}` whole, under `--context-split` too: it is the solo call inside the swarm, so the measurement compares like with like. Its node name carries the `forager-` prefix, so the run tokens count it as they count a lens: one vote in `{tally}`, one line in `{swarm.ledger}`, one `LensAnswer` in a bench row. It declares no bond, no `resonates:` pair names it, and the ∇ sensor registers only those pairs, so it fires no ∇. Its edge goes to Queen, or to `swarm-evaluate` with the coverage pass; no edge enters it. Queen reads it under the full profile as `The direct answer (direct), the model with no persona, over the question and the whole context: {verdict.forager:direct}`, after the lens verdicts, and under lean her prompt says that the ledger's lens named `direct` is the direct answer. The name is plain rather than bee-accurate: no roster file or preset uses the word. Generation SHALL refuse a swarm holding a forager named `direct`, or a synthesizer so named, under the flag, since the node names would collide; without the flag such a forager is an ordinary lens.

The flag defaults to off: § What the measurement found.

#### Scenario: The direct voice votes
- **WHEN** `chb ask "q" --direct-voice --foragers minimal` runs and six lenses say support, one says oppose and the direct voice says oppose
- **THEN** Queen's `{tally}` reads `8 verdicts: support 6 (…); oppose 2 (direct, …). Plurality: support`, her prompt lists the direct answer's full verdict after the seven lenses, and `{nabla.fired}` names no pair with `direct`

#### Scenario: A forager named direct
- **WHEN** `foragers/direct.md` exists and `chb ask "q" --foragers balanced,direct --direct-voice` runs
- **THEN** generation fails naming `direct` and `forager-direct`; without `--direct-voice` the swarm runs with that forager as a lens

### Requirement: The context split

`--context-split` on `chb ask` SHALL give each lens forager its own part of the context in place of all of it. `foragers.SplitContext` cuts the context into as many parts as the swarm has lens foragers and deals its blocks round-robin: part i holds blocks i, i+n, i+2n, … in their original order. The blocks are the context's paragraphs, separated by blank lines; when it has one paragraph, they are the items of its shallowest list, the lines starting `- ` at the least indentation, which is the roster entry boundary the bench's pack uses, the text before the first item riding with the first; a context with neither is one block. Every non-blank line lands in exactly one part, and a part past the last block is empty. The lenses take the parts in name order, the order the generator gives them models. `foragers.ContextParts` returns each lens's part under `foragers.ContextKey(name)`, `context_<name>`; `chb ask` passes those beside `question` and `context` in `--inputs`, the generated workflow declares them under `inputs:`, and each lens's prompt reads `Context (part i of n: each lens reads a different part of the context):` and `{context_<name>}` in place of `{context}`. Scope, the direct voice and Queen's inputs keep `{context}` whole. The follow-up lenses read no context either way.

What it costs on a closed question: a lens whose part lacks the entry the question needs must abstain by the question's own rule, so fewer lenses vote and the plurality rests on fewer voices, and a question about a whole preset cannot be answered from one part at all. Why it might still pay: the lenses' errors then come from different evidence, which is the independence the quorum needs (bench.md § The correlated-error rate measured 0.37–0.49 of lens pairs erring together on one context with 4b or 8b lenses), and Queen still reads every lens. The flag defaults to off: § What the measurement found.

#### Scenario: A roster pack dealt to seven lenses
- **WHEN** `chb ask "q" --context-split --foragers minimal --context-file pack.yaml` runs on the bench's pack, one `roster:` list of fifteen entries and no blank line
- **THEN** architect's prompt holds `roster:` and entries 1, 8 and 15, empiricist's entries 2 and 9, and so on; no entry is in two prompts; the direct voice and scope, when on, hold the whole pack

#### Scenario: Fewer blocks than lenses
- **WHEN** the context is two paragraphs and the swarm has nine lenses
- **THEN** the first two lenses in name order read one paragraph each and the other seven read an empty part

### Requirement: The verdict leads with its calibration

After the in-process dispatch, `chb ask` SHALL print the run's verdict to stdout, led by how sure the swarm was, so a reader sees the calibrated signal before what it says, and nothing else there: agent-run's JSON trailer, which `chb agent-run` prints on its own stdout, goes to stderr under `ask`, with the rest of the run's narration, so `chb ask "<q>" > verdict.txt` captures the three lines alone. The three lines are `workflow.Calibration.Lines`, computed from the run's rows and its workflow's `resonates:` pairs, never from a prompt: first `Quorum: convergence <high|medium|low>; tally <verdict n, …>; plurality <p>, margin <m>; <no dissent written|dissent written>`, then `∇ fired: <a↔b (verdict); …|none|none — this swarm declares no resonates pairs>`, then `Verdict: <verdict> — <recommendation>`. The tally lists each verdict with its count, most votes first then by name, then the lenses with no verdict and their status; the plurality is the tally's, and the margin is its votes less the runner-up's, abstain casting no vote; a tie reads `plurality none (tie)` and no vote `plurality none (no vote cast)`. A dissent is written when Queen's `dissent_from_plurality` is not blank. The ∇ line is the text `{nabla.fired}` gave Queen. When the queen node did not complete, the first line reads `convergence none (queen is <status> in this run)` and the last `Verdict: none (queen is <status> in this run)`; a run with no `queen` node says so. This changes no prompt. `--no-dispatch` prints no verdict, since nothing ran. The print is presentation: when the rows cannot be read, ask says `verdict: unavailable (<why>)` on stderr and its exit status is the run's, so a harness reading the exit status still sees a run that completed. The artifact records the same values (§ Deterministic artifact). A run that fails prints its error once.

`--json` SHALL print one JSON object and nothing else on stdout, for a script to parse: `run_id`; `question`; `verdict`, `recommendation` and `report`, the Queen's, each `""` when she did not complete; `calibration`, `workflow.Calibration` as the artifact records it; and `run`, the object agent-run's trailer holds (`run_id`, `iterations`, `nodes_run`, `commits`, `pr_url`, `input_tokens`, `output_tokens`). The trailer itself is then printed nowhere. Under `--json` a verdict that cannot be read is an error, since the object is what was asked for. `--json` with `--no-dispatch` is refused: there is no run to report.

#### Scenario: A verdict after a dispatched swarm
- **WHEN** `chb ask "q" --no-eval` runs, six lenses say support, one abstains, Queen says support with convergence high and no dissent, and the pair empiricist⇄skeptic converged
- **THEN** stdout is `Quorum: convergence high; tally support 6, abstain 1; plurality support, margin 6; no dissent written`, `∇ fired: empiricist↔skeptic (support)`, `Verdict: support — <her recommendation>`, in that order and nothing else, and the JSON trailer is on stderr

#### Scenario: One object under --json
- **WHEN** the same swarm runs with `--json`
- **THEN** stdout holds one JSON object whose `verdict` is `support`, whose `calibration.nabla_fired` is `["empiricist↔skeptic (support)"]` and whose `run.run_id` equals `run_id`, and no trailer is printed on either stream

#### Scenario: A Queen who was rejected
- **WHEN** Queen's reply is rejected after its repair
- **THEN** the first line opens `Quorum: convergence none (queen is rejected in this run)` and still gives the tally and the ∇ pairs, and the last reads `Verdict: none (queen is rejected in this run)`

### Requirement: What the measurement found

The two flags SHALL default to what the pre-registered measurements adopted, which is neither: the quick screen of 2026-10-01 for both, and the 56-item measurement of 2026-10-06 for the direct voice. The quick screen's rule, fixed before any code: the swarm arm of the bench's quick screen (7 twin pairs, 14 items, `minimal`, `--no-eval`, sampling seed 7), qwen3.6:35b-a3b-q4_K_M in every role, against Bench-1's recorded runs on the same items (bench.md); a change becomes the default only if on the design seed its class-balanced accuracy is not below the baseline's and it loses no more items than it wins, then again on the held-out seed 2 with its high-convergence correct rate not below the baseline's, and with no loss of accuracy with qwen3.5:4b lenses, and no more fabricated abstains or incomplete runs than the baseline. The baseline at seed 1 is 14/14, so the design seed could only screen for harm. Both fell there, so neither went to the held-out seed or the small lenses under the rule.

- **The direct voice**: 13/14 against 14/14; class-balanced 1.000 = 1.000; high convergence right 11/11 (baseline 8/8); the direct vote itself right 13/14, missing the item the solo call misses; correlated errors among the persona lenses unchanged at 0.11. The one loss is the abstain item: the lenses answered as in the baseline (six abstain, Skeptic oppose), the direct vote abstained, and Queen answered `oppose` with a written dissent where the baseline Queen abstained in silence. One net loss and one fabrication against zero fail the rule, and one item is within the noise floor the Queen-replay placebo measured (a content-free prompt change flipped 1 of 14 verdicts). Not adopted; a larger measurement, not a new rule, is the next step. An exploratory arm outside the rule ran the direct voice on the held-out seed 2 afterwards: 12/14 against the recorded 10/14, two wins on tally ties the baseline Queen abstained on, no loss, high convergence 8/8. It was read after the rule had decided, and it decides nothing; it is the case for the next, larger rule.
- **The context split**: 7/14 against 14/14; class-balanced 0.452; seven losses, no win (sign test p = 0.008): four abstains and three flipped verdicts. The correlated-error rate among the lenses rose from 0.11 to 0.54, since a lens whose part lacks the entries a whole-preset question needs abstains or guesses with the rest, and Queen's `high` was wrong once in once. Not adopted, by a wide margin. Whether a split pays on an open question whose context is many independent paragraphs is not measured.

These are 14-item screens: they can call a change harmful, as they did the split, and they can fail to see a 5-point difference. Each is a measurement from its date on this machine (Ollama 0.35.0), not a guarantee.

The direct voice was measured again on 2026-10-06 under its own pre-registered rule (`docs/evidence/2026-10-06-direct-voice/`: the rule, both arms' rows and reports, the per-run calibration rows, the decision): Bench-1's selection suite, 56 items over generator seeds 1–4, qwen3.6:35b-a3b-q4_K_M in every role at reasoning none and sampling seed 7, the swarm arm only, with the baseline rerun on the same build. The rule adopted only if the candidate's class-balanced accuracy was not below the baseline's, it won more items than it lost with a one-sided exact sign test at p ≤ 0.10, its correct rate given convergence `high` was not below the baseline's, and it fabricated no more abstains. Measured: 48/56 against 47/56, class-balanced 0.923 against 0.885 (held); two wins and one loss, p = 0.50 (failed); `high` right 35/37 against 28/28 (failed); fabricated abstains 4/4 against 3/4 (failed); 56/56 completed in both; 154 s against 144 s a run. The wins are the quick screen's exploratory case made good: F1-s2-b and F1-s3-b were 3–3 lens ties the baseline Queen abstained on or called `conditional`, and the direct vote broke each to 4–3 the right way. The loss repeats the quick screen's: on AB-s1-b seven lenses abstained, the direct vote with them, and Queen answered `oppose` with a written dissent. The eighth vote also lifts 7–1 and 5–2–1 tallies to `high`, so `high` grew from 28 runs to 37 and lost its perfect record. The direct vote alone was right 53/56, more than either swarm. Not adopted: one of four conditions held. Fifty-six items can see a four-item gain and did not see one; they hold the abstain class as four items, and they speak to C3, `minimal`, `--no-eval` and these closed questions on this machine, nothing wider.

#### Scenario: Reading the defaults
- **WHEN** `chb ask --help` prints `--direct-voice` and `--context-split`
- **THEN** each defaults to false, and this section says why

### Requirement: CLI surface

- `chb list` SHALL list every forager with its sigil and lens, starring the `balanced` preset. A synthesizer (Queen) is not a forager: it SHALL be listed on its own line after the foragers and left out of their count.
- `chb generate "<q>" [--out PATH]` SHALL render the YAML without dispatching.
- `chb generate` and `chb ask` SHALL take `--lens-tools none|read|all` (default `none`, § Workflow generation). `chb ask` also takes `--followups K` (default 3), the number of evaluator gaps that get a follow-up lens, and refuses a K below 1: `--no-eval` is how to skip the coverage pass.
- `chb generate` and `chb ask` SHALL take `--persona-profile full|lean` (default `full`) and `--persona-sections` (§ Persona profiles). Another profile, or a section list that is not whole numbers of 1 or more, is refused before anything is written.
- `chb ask` SHALL take `--direct-voice` (§ The direct voice) and `--context-split` (§ The context split), each off unless the measurement there says otherwise. `chb generate` does not take them.
- `chb ask --context-file PATH` SHALL give every forager that file's text as its context, after any `--context` text and a blank line. A file that is not UTF-8 text is refused. The text reaches agent-run in `--inputs`, so the command `--no-dispatch` prints carries it. It is a context pack: the lenses reason over facts gathered for them rather than reading the repository. It reaches each prompt as written: a placeholder or token it names, such as `{tally}` or `{comb.forager:skeptic}`, is not filled (comb.md § Template tokens). No size limit is set here. On a local endpoint the context-window guard holds each prompt it lands in to the model's window, where one is known (runner.md § Context window guard).
- `chb ask "<q>"` SHALL write the YAML (`--out`, default `$TMPDIR/swarm-<pid>.yaml`) and dispatch it in-process with no auto-commit branch: agent-run's own flags parse the arguments ask builds into the run's configuration, and the run uses the database ask opened, so `--db` holds for it. `--no-dispatch` prints the `chb preflight` and `chb agent-run` commands instead. Both carry `--db` when it was given. The agent-run command carries the arguments ask would dispatch, `--branch ''` included. Every argument is single-quoted, so sh, bash and zsh pass it verbatim. `--temperature` and `--top-p` are checked as agent-run checks them (runner.md § Sampling) and passed to it as given.
- `chb ask --profile <name>` (else `HIVE_PROFILE`) SHALL pass the profile to agent-run, which routes each role (runner.md § Routing profiles), and to the printed preflight and agent-run commands. It SHALL refuse the profile beside `--model`, `--synthesizer-model`, `--forager-tier`, `--synthesizer-tier`, `--forager-reasoning` or `--synthesizer-reasoning`, which the profile replaces, and beside `--lens-tools` when the profile's `lens` or `followup` route sets tools. An unknown profile is refused before the workflow is written. `--persona-profile` and `--persona-sections` SHALL be taken beside it: the routing profile sets where each role runs, and the persona profile what its prompt carries (§ Persona profiles), so a lean swarm runs on a small local model's window.

#### Scenario: Listing the shipped roster
- **WHEN** `chb list` runs against the shipped 15 files
- **THEN** it reports 14 foragers and prints Queen on her own line after them

#### Scenario: Printing instead of running
- **WHEN** `chb ask "q" --no-dispatch`
- **THEN** the workflow is written and the preflight and agent-run commands are printed, and no agent runs

#### Scenario: A question the shell would expand
- **WHEN** `chb --db x.db ask 'is $HOME worth it?' --no-dispatch`
- **THEN** the printed agent-run command, pasted into a shell, passes `--db x.db`, `--branch ''` and the question with `$HOME` unexpanded

#### Scenario: A path zsh would expand
- **WHEN** `chb ask "q" --no-dispatch --out =x.yaml`
- **THEN** both printed commands, pasted into zsh, pass `=x.yaml` verbatim

#### Scenario: A profile beside a model flag
- **WHEN** `chb ask "q" --profile local-fast --model m`
- **THEN** it exits with an error naming `--model` and the profile, and writes no workflow

#### Scenario: A routing profile with the lean persona profile
- **WHEN** `chb ask "q" --profile local-8gb --persona-profile lean --no-dispatch`
- **THEN** the workflow renders the lean personas, Queen reads `{swarm.ledger}`, and the printed commands carry `--profile local-8gb`

### Requirement: Deterministic artifact

`--artifact PATH` SHALL write a canonical JSON artifact of the run: `schema_version` "1.4", the question, `determinism` (enabled; temperature, null when unset; top_p, left out when unset; seed: the run's flags as runner.md § Sampling records them, without the greedy extras a backend adds at temperature 0), the run's foragers (sorted), their models, providers and `base_urls` (the endpoint each node row records, empty for the CLI backends; runner.md § Per-node persistence and cost), each forager's parsed verdict, the synthesis JSON (Queen's outputs, her `report` included), `synthesizer` (the synthesis node's model, provider and `base_url` from its row, and its `schema_enforcement` when the row records one; left out when the run has none), `schema_enforcement` (forager → what its node row records, runner.md § Constraint probe, for each forager whose row records one), `diversity` (the state of the check the Queen's `{diversity}` line reads, `workflow.RunDiversity`: `low`, `not_low`, `unknown` or `not_checked`; a lens with no recorded model keeps it `unknown` rather than `not_low`), `calibration` (`workflow.RunCalibration`, § The verdict leads with its calibration: `queen_status`, `convergence`, `dissent_written`, `tally`, `plurality`, `margin`, `votes`, `abstentions` and `nabla_fired`; the sorted encoding writes it before `synthesis`), the Comb snapshot (region vantages and this run's forager vantages, sorted by `vantage_key`, timestamps stripped) and `sha256`. A forager belongs to the artifact only when the run has a node for it, so another run's vantages in the same database stay out. The hash SHALL be SHA-256 over the encoding with the `sha256` field empty, then written back into it. The encoding sorts map keys at every level and is `encoding/json`'s, which renders a float in its shortest round-trip form. The seed is written exactly, at any int64 value. `base_urls`, `synthesizer` and `determinism.top_p`, which 1.1 added, and the two `schema_enforcement` fields, which 1.2 added, are left out when empty. So are `diversity`, which 1.3 added, and `calibration`, which 1.4 added; `BuildArtifact` always fills both. So an older file still decodes to its own bytes and verifies.

`--seed N` implies `--deterministic` and is recorded in the artifact. Byte-identical artifacts across two runs require a provider that honours the seed: OpenAI does, Gemini on most variants, and the Anthropic Messages API has no seed (temperature 0 only). Otherwise the hash is a drift detector, not a reproducibility proof. `chb verify-artifact <path>` SHALL require the file's bytes to equal the canonical encoding of what they decode to, and SHALL re-hash the bytes with the `sha256` value emptied. It exits 0 when both hold, printing the hash. Otherwise it prints the stored and recomputed hashes — not a content diff — and exits 1. A file that does not decode as an artifact fails with the decode error instead of the hashes, and also exits 1.

#### Scenario: A tampered artifact
- **WHEN** a byte of a written artifact is changed, whitespace or a key's case included, and the file still decodes
- **THEN** `chb verify-artifact` prints both hashes and exits 1

#### Scenario: A tampered artifact that no longer decodes
- **WHEN** a changed byte breaks the JSON, such as the leading `{`
- **THEN** `chb verify-artifact` prints the decode error and exits 1

### Requirement: Replication harness

`chb replicate "<q>" --n N --vary none|stochastic` SHALL run N swarms, each against its own `hive.db` in a temporary directory, so no Comb is shared. Like `agent-run`, each runs from the caller's working directory: personas resolve from its `agents/`, and agents write to the replica's database. It SHALL write a meta-diff JSON (`--out`, default `./replicate-<vary>-<unix>.json`) carrying the question, `n`, `vary`, `seed`, a `replicas` array (index, artifact path, hash, whether its hash verified), per-vantage entries with each replica's narrative, confidence and label plus a divergence block (`confidence_range`, `distinct_narratives`, `label_consensus`), and a summary. When every artifact hash matches, divergence is reported as zero without the per-vantage pass. `--vary none` requires `--seed` and makes each replica deterministic; `--vary stochastic` drops determinism to measure sampling sensitivity; `phrasing` and `model` SHALL be refused as not yet implemented.

#### Scenario: Identical replicas
- **WHEN** `--vary none --n 3 --seed 42` and all three artifacts hash the same
- **THEN** divergence is zero and no per-vantage diff is computed

### Requirement: The empirical record

The roster choices above SHALL be reported as what a five-wave self-evaluation measured, not as a guarantee this repository can reproduce: its artifacts were written under `workspace/`, which `.gitignore` keeps out of every clone. What it reported, at ~$16 total: a bonded pair shifted Architect's verdict from `oppose` to `conditional` and raised citation density (opus; untested at haiku tier); Skeptic-without-Optimist hedged where Skeptic-with-Optimist committed, which flipped the sign of the assumption that Optimist balances bias — it anchors decisiveness; and rewriting the personas to template-v1 raised haiku citation density from 0.57 to 1.00 and verdict-shape parity with opus from 3/7 to 6/7, at 0 % reject rate. Treat each as an assumption carried from a run you cannot re-check, and re-measure before leaning on it.

#### Scenario: Reading the record
- **WHEN** a reader follows the spec to `workspace/forager-self-eval/`
- **THEN** the directory is absent from the clone, as this requirement says

## Files

- `foragers/*.md` — the personas; `foragers/README.md` — the authoring guide
- `agents/swarm.md` — the system prompt every swarm node runs with
- `assets.go` — the carried `foragers/`, `agents/` and `workflows/` (`hive.Foragers`, `hive.Agents`, `hive.Workflows`); `assets_test.go` holds them equal to the folders
- `internal/foragers/registry.go` — `Forager`; `load.go` — `Load`, `LoadFS`; `presets.go` — `Default`, `Minimal`, `Balanced`, `Filter`, `SplitByArchetype`; `validate.go` — `ValidateSwarm`
- `internal/foragers/source.go` — `Source`, `Resolve`, `DirSource`
- `internal/cli/foragers.go` — `foragerDirs`, `foragersSource` (the once-only note), `loadForagers`
- `internal/foragers/contract.go` — `parseContract` (`output_schema`, `length_caps`), the order a persona's schema is sent in
- `internal/foragers/persona.go` — the persona profiles, `renderPersona`, the counter-bias clause, the axes no lens owns
- `internal/foragers/workflow.go`, `workflow_nodes.go`, `swarm.go.tmpl` — `GenerateWorkflow`, the nodes and edges it fills the YAML template with, and the template
- `internal/foragers/direct.go` — the direct voice's name, prompt and schema; `SplitContext`, `ContextParts`, `ContextKey`
- `internal/foragers/palette.go` — sigils and accents
- `internal/workflow/calibration.go` — `RunCalibration`, `Calibration.Lines`
- `internal/artifact/artifact.go` — `Artifact`; `build.go` — `BuildArtifact`; `encode.go` — `Encode`, `VerifyArtifact`
- `internal/cli/list.go`, `generate.go`, `ask.go` — `chb list`, `chb generate`, `chb ask`, `--context-file`, `--direct-voice`, `--context-split`, the verdict printed after dispatch
- `internal/cli/validate_personas.go` — `chb validate-personas`
- `internal/cli/verify_artifact.go`, `replicate.go` — `chb verify-artifact`, `chb replicate`
- `internal/foragers/shipped_roster_test.go`, `assumptions_ledger_test.go` — the roster and preset pins
- `internal/foragers/direct_test.go`, `internal/workflow/calibration_test.go`, `internal/artifact/calibration_test.go`, `internal/cli/ask_direct_split_test.go`, `internal/harness/harness_test.go` — the direct voice, the split, the leading lines, the flags and the harness pass-through
