# Comb Specification

## Purpose

The Comb is the hive's persistent, queryable belief surface: what the system currently believes about every addressable vantage — a coordinate region or a forager — with a revision history anchored to Time Wheel ticks, so the hive can recall what it believed at any moment. It also carries the event bus, the ∇ convergence sensor, the dreamer's ripening loop over the store, and the embedding sidecar for semantic similarity.

## Requirements

### Requirement: Vantages

`comb_state` SHALL hold one row per vantage, of kind `region` or `forager`:
- a `region` vantage is a coordinate-prefix digest keyed `""` (global), `d1=0`, `d1=0;d2=3`, …, built deterministically by `comb.BuildAllRegions` / `comb refresh`;
- a `forager` vantage is keyed `forager:<name>` and written by `comb.BuildForagerVantage` from the forager's verdict, before its workflow node is marked completed. The write is best-effort: a failure is logged and the node still completes. It is one row per forager per database, overwritten by whichever run wrote last; its history is in `comb_revisions`.

A row carries narrative, confidence (0–100), contested, dominant label (`definition`\|`guarantee`\|`assumption`\|`unknown` or NULL), evidence and open-question counts, digest method (`heuristic` for regions, `forager` for foragers), raw JSON and last-revised time. Region narratives are at most 512 bytes; a forager's narrative is its recommendation as given, or `verdict=<v> (no recommendation supplied)`.

#### Scenario: A later run overwrites a forager vantage
- **WHEN** two runs of the same forager complete in sequence
- **THEN** `forager:<name>` holds the second run's verdict and `comb_revisions` holds both

### Requirement: Confidence and contested

A region's confidence SHALL be `clamp(100 × (1 − conflict_rate) × coverage_factor, 0, 100)`, rounded down, with `conflict_rate` = unresolved conflicts ÷ findings and `coverage_factor` = 1 − critical gaps ÷ (findings + critical gaps + 1); a region with no findings SHALL have confidence 0, since there is no belief to be confident in. A region SHALL be contested when, and only when, an unresolved conflict touches it: one of the conflict's two findings sits in the region. A conflict is the store's record of findings that contradict each other. Detection (`gate.DetectConflicts`) records numeric divergence, negation, and a guarantee beside an assumption, between two findings in one cell. The label mix alone never makes a region contested. Honest research is mostly assumptions, and a region of them is not in dispute. The dominant label tie-breaks toward the stronger label. A change to any of these counts makes the region stale until a refresh (§ Staleness). A forager vantage's confidence SHALL follow its verdict — support 80, oppose 20, conditional 50, abstain 30 — and it is contested when the verdict is `conditional` or empty.

#### Scenario: An unresolved conflict
- **WHEN** a region has 10 findings and 1 unresolved conflict and no critical gaps
- **THEN** its confidence is 90 and it is contested

#### Scenario: A region of assumptions
- **WHEN** a region has 5 findings, all `assumption`, no unresolved conflict and no critical gaps
- **THEN** it is not contested and its confidence is 100

#### Scenario: An exact value
- **WHEN** a region has 5 findings and 4 unresolved conflicts and no critical gaps
- **THEN** its confidence is 20

### Requirement: Refresh, query, synthesis and status

`chb comb refresh [--region <key>]` SHALL rebuild region digests, appending a `comb_revisions` row with source `comb.refresh` for each and publishing a vantage-written event. A failed revision append is an error, not a silent skip; the upsert and the append are not atomic. `chb comb query` reports a region's digest with its staleness (§ Staleness) — `fresh`, `stale` or `missing` — without refreshing. `chb comb synthesize [--out PATH]` renders the region digests as markdown, each with its staleness (§ Staleness), and only those: forager vantages are neither rendered nor counted. `chb comb status` shows the last region refresh (forager writes do not move it), contested, stale (§ Staleness) and per-kind counts, and the capped cells and capped findings (§ Capped cells).

### Requirement: Staleness

A region SHALL be stale when a refresh would write it a different digest: a different narrative, confidence, contested flag, dominant label, evidence count or open-question count. A refresh is `comb.BuildDigest`, which `chb comb refresh` writes through `comb.WriteRegionVantage`. A forager vantage SHALL NOT be stale: it is one run's verdict, which no refresh rewrites.

So a region goes stale when a change moves its digest (§ Confidence and contested). These always do:
- a finding added to it or removed;
- a conflict that touches it opened or resolved, by `ConflictsRepo.Resolve` or by the gate's numeric auto-resolve, which sets a resolution alone;
- a gap or followup in it opened, resolved or answered.

A label change does when it moves the dominant label, as one by `UpdateFinding`, the cascade or the dreamer's `settle` can. A change that leaves the digest as it was leaves the region fresh: a finding replaced by one of the same label, a finding's text edited, a label change under a dominant label that holds, a cap (§ Capped cells), or two changes that cancel, such as one conflict opened and another resolved. The coordinates follow from the key, and the digest method is the builder's constant, so neither is compared. That is a choice.

`prune` and `reprove`, and `settle` without `--apply`, write only a signal or an alarm, which no digest reads, so they leave every region as fresh or as stale as it was.

The rule computes the digest with the code the refresh uses (`regionTallies`), and compares it with the stored row. `comb.WriteRegionVantage` is the one writer of a region's digest, and it converts the digest as the rule does, so a region is fresh right after a refresh writes it. The rule orders no times, so a change in the second of a refresh counts like any other. It reads the rows and the tables in one read transaction, so its answer holds for one database state. A surface reads the row it shows in a query of its own. So a refresh that commits between the two reads can show, for that one read, a new digest marked stale or an old one marked fresh.

The rule compares with the digest this build would write. So a row written by a build whose digest code differed reads stale until a refresh, with no data changed. Two builds with different digest code that share a workspace each call the other's refresh stale.

This is the Comb's one staleness rule (`comb.StaleVantages`, and `comb.Classify` for one region). `chb comb query`, `chb comb synthesize`, `chb comb status` and its `--json`, and `{comb.<key>}` SHALL each report staleness by it. So for one database state they agree on every vantage they show.

A surface that lists vantages SHALL read their staleness in at most six queries, whatever the number of rows. The work grows with the findings, unresolved conflicts, open gaps and unanswered followups, times the number of distinct sets of axes the regions pin. A database with no `comb_state` table holds no vantages. One with the table also holds the findings, conflicts, gaps and followups the rule reads, with every column it reads, since the schema creates them together. Every query of `comb_state` names its columns, so a table that carries a column the schema does not declare reads and writes as one without it.

#### Scenario: A finding after the digest
- **WHEN** a finding is written in a region after its last refresh, even in the same second
- **THEN** `comb query` reports the region `stale`, and `chb comb synthesize` and `chb comb status` count or show it stale

#### Scenario: A finding written during the refresh
- **WHEN** a finding is written in a region after the refresh counted the region's findings and before it stamped the digest
- **THEN** every surface that shows the region reports it stale, because a refresh would count one more finding

#### Scenario: A conflict opened after the digest
- **WHEN** a conflict between two findings in `d1=1` is recorded after the last refresh
- **THEN** every surface reports `d1=1` stale, and each region holding either finding

#### Scenario: A gap resolved after the digest
- **WHEN** a critical gap at `d1` 2, `d2` 0 is resolved after the last refresh
- **THEN** every surface reports `d1=2` and `d1=2;d2=0` stale

#### Scenario: A label reverted by the cascade
- **WHEN** a guarantee at `d1` 3, `d2` 1 rests on an assumption at `d1` 3, `d2` 0, and the cascade reverts it to `unknown` after the last refresh
- **THEN** every surface reports `d1=3` and `d1=3;d2=1` stale, and `d1=3;d2=0`, which holds neither the guarantee nor the gap the cascade records, fresh

#### Scenario: A cap
- **WHEN** the hive caps a finding after the last refresh
- **THEN** no surface reports a region stale for it

#### Scenario: A conflict the gate auto-resolves
- **WHEN** the gate auto-resolves a numeric conflict between findings in `d1=1` after the last refresh
- **THEN** every surface reports `d1=1` stale, and a refresh writes it uncontested

#### Scenario: One region refreshed
- **WHEN** `chb comb refresh --region d1=9` writes a region that holds no finding
- **THEN** every surface reports it fresh

#### Scenario: A finding replaced by its like
- **WHEN** a finding is deleted after the last refresh, and one with the same label is written in its cell an hour later
- **THEN** its regions are fresh, because a refresh would write the same digests

#### Scenario: A forager vantage
- **WHEN** a forager vantage's evidence count differs from the number of findings anywhere
- **THEN** `chb comb status` reports it fresh

### Requirement: Capped cells

The Comb SHALL show the hive's caps (hive.md § The plan is advisory unless applied). A cap is a row in `capped_findings`. `chb comb status` SHALL read the caps when it renders, through `comb.CappedCoords`, and nothing SHALL store them. So a cap shows without a refresh, and the Comb adds no table or column for it.

A cell is a coordinate on `d1`–`d4`, each axis a value or absent. Cells compare as the hive's seal compares them: an absent axis matches only an absent axis, and `d5`–`d8` are not part of a cell. A capped cell is a cell that holds a capped finding. The hive seals it from recruitment.

A database with no `capped_findings` table holds no caps, because the build that writes caps creates the table when it opens a database.

A cap SHALL change no digest. A region's confidence, contested flag, dominant label, counts, narrative and revisions stay as its findings, conflicts, gaps and followups make them. Capping changes no label, since agreement among agents is not a derivation. So a cap does not make a region stale: a refresh would write the same digest (§ Staleness).

`comb query`, `comb synthesize`, `comb at`, `comb diff`, `comb history`, the artifact and `{comb.<key>}` do not show caps. `capped_findings.capped_at` records when each cap was written.

A row's digest is as of its refresh, and its caps are as of the read. The hive loop does not refresh the Comb. So after a hive run, a capped cell whose findings all came after the last refresh has no row until `chb comb refresh` builds it, and the regions that hold those findings are stale. A region that holds more capped findings than its evidence count is always stale, because each capped finding in it is a finding in it, so a refresh would count more findings than its evidence count.

The surface: `chb comb status` SHALL print `capped_cells=<n> capped_findings=<m>` after `stale=`, and `--json` SHALL carry `capped_cells` and `capped_findings`. `capped_cells` counts the distinct capped cells, those with an absent axis included (`comb.CappedCells`). `capped_findings` counts the caps. Both are right before any refresh.

Comb text SHALL name a cap as a capped cell or a capped finding, and SHALL NOT use `capped` alone. A bare `capped` is the `phase` of a hive at its iteration cap (hive.md).

#### Scenario: A cap after a refresh
- **WHEN** the Comb is refreshed, and `chb hive next --apply` then caps the only finding, at `d1` 0, `d2` 1, `d3` 2, `d4` 3
- **THEN** `chb comb status` prints `capped_cells=1 capped_findings=1`, and every field of every row is as it was before the cap

#### Scenario: An absent axis
- **WHEN** the only capped finding sits at `d1` 0 and `d3` 5, with `d2` and `d4` absent, and an uncapped finding sits at `d1` 0, `d2` 2, `d3` 5
- **THEN** `chb comb status` prints `capped_cells=1 capped_findings=1`

#### Scenario: A cap on findings newer than the refresh
- **WHEN** the Comb is refreshed with one finding, at `d1` 2, `d2` 0, `d3` 0, `d4` 0; two findings are then written at `d1` 2, `d2` 3, `d3` 3, `d4` 3; and both are capped
- **THEN** `chb comb status` prints `capped_cells=1 capped_findings=2`, every surface reports the global region and `d1=2` stale, and after `chb comb refresh` no region is stale

#### Scenario: The seal and the Comb agree
- **WHEN** the only open gap sits at some coordinate, and a high-convergence finding shares its `d1`
- **THEN** a waggle dance targets the gap exactly when adding the gap's coordinate to the capped coordinates would add a capped cell (`comb.CappedCells`); no waggle dance targets a gap whose cell is capped

### Requirement: Calibrated confidence

`chb comb query`, `chb comb at` (`--tick` and `--time`) and the `{comb.<key>}` line SHALL report `calibrated_confidence` beside `confidence` when the row's dominant label has a calibrated `label` score in one of the region's scopes, most specific first: `d1=x;d2=y`, `d1=x`, then the global scope (`calibration.ScopesFor`, `Scores.LabelScore`). It is `clamp(confidence × hit_rate / target, 0, 100)`, rounded to the nearest whole percent, with the label's target from cde-mss.md § Calibration recompute (`calibration.CalibratedConfidence`). It is computed on read and never stored; `confidence` is unchanged. A label with fewer than `NFloor` outcomes in every scope reports nothing: `comb query` and `comb at` omit the key, and the `{comb.<key>}` line ends with ` [calibrated confidence N%]` only when there is one. For `comb at --tick N` the score is the `calibration_revisions` snapshot current at tick N (the latest under a tick with an id at or below N, `CalibrationRepo.ScoresAtTick`); for `--time T`, the latest under a calibrate tick that started at or before T (`ScoresAtTime`). A forager vantage has no dominant label and never carries it.

#### Scenario: An uncalibrated label
- **WHEN** a region's dominant label has nine outcomes in every one of its scopes
- **THEN** `comb query` reports no `calibrated_confidence`

#### Scenario: A calibrated label
- **WHEN** a region's dominant label is `guarantee`, guarantees in its `d1` scope have 12 outcomes of which 10 held, and the region's confidence is 100
- **THEN** every surface reports `calibrated_confidence` 83

#### Scenario: Calibration at a past tick
- **WHEN** `comb at --tick 4` reads a region, and the label's score changed at tick 6
- **THEN** `calibrated_confidence` uses the score current at tick 4

### Requirement: Template tokens

Prompt resolution SHALL substitute:
- `{comb.<key>}` via `comb.Resolve`: a region key walks from most to least specific; `forager:<name>` is a direct lookup. The line ends ` [stale]` when the vantage it resolves to is stale by § Staleness;
- `{comb.region}` with the node's `comb_vantage:` field;
- `{verdict.forager:<name>}` with that forager's verdict as its node returned it in this run, which the queen and the coverage evaluator read. It is the outputs stored when the node completed, which the accept path parsed with `ExtractJSONOutput`, so a reply that opened with prose is read in full. Every field is written, uncut: the verdict, key points, evidence, uncertainties, the typed claims `def_claims`, `gua_claims`, `asm_claims` and `unk_claims`, any other field by name, then the recommendation. A forager with no node in the run, or whose node did not complete, resolves to a line that says so: `(no verdict: this run has no node for forager <name>)`, or `(no verdict: forager-<name> is <status> in this run)`. It does not read the forager's Comb vantage, which another run can overwrite. A forager is named by its node's `forager_name`, else a `forager-` prefix; dreamer nodes are not counted. Any other `{verdict.<key>}` is a malformed key and resolves to empty with a stderr warning;
- `{tally}` with the vote over the same verdicts, on one line: each verdict with its count and foragers, most votes first, the foragers with no verdict and their status, and the plurality. Abstain is listed and casts no vote, as it fires no ∇, so the plurality is the one other verdict with strictly the most votes; otherwise the line says `none, a tie between …`, `none, every verdict abstains`, or `none`. When a node that reads `{tally}` completes, the engine counts the same outputs again and writes to state, before the node's `accept:` runs, the plurality as `tally_plurality`, `""` when there is none; the votes cast as `tally_votes`, one for each lens whose verdict is not abstain; the lenses that abstained as `tally_abstentions`, a lens with no verdict counting in neither; and the line as `tally_line`. The forager nodes are final by then, because the node waited for them, so it is the count the prompt showed. It never enters the node's outputs (workflow.md § Accept predicates);
- `{nabla.fired}` with this run's fired ∇ pairs over the same verdicts. The pairs are the workflow's `resonates:` list, and a pair fires when both of its foragers returned identical verdict strings, neither empty nor `abstain` (`comb.ConvergedPairs`, the sensor's rule). The token resolves to one line: the fired pairs written `a↔b (<verdict>)`, each pair in canonical order and the list sorted; or `none`; or `none — this swarm declares no resonates pairs`. Identical strings are not agreement in substance, and the Queen's prompt says so. It does not read `forager_bonds`;
- `{diversity}` with whether one model produced a unanimous swarm, over the lenses of this run that returned a verdict and the model each node row records (`resolved_model`). The check has four states (`workflow.LensDiversity.State`). Not checked: fewer than two lenses returned a verdict. Not low: they returned two or more verdicts, or ran on two or more recorded models. Unknown: they agree, a lens has no recorded model, and the recorded ones number one or none. Low: two or more returned one verdict string, all on one recorded model. Models compare by name, so two sizes of one family count as two models; this is a choice. The line opens with its state: `low:` gives how many lenses, the verdict and the model, and says the agreement is one model's view; `not low:` names the different verdicts, or, when the verdicts agree, the models; `unknown:` names the lenses with no recorded model; `not checked:` says fewer than two lenses returned a verdict. The artifact's `diversity` is the same check (swarm.md § Deterministic artifact);
- `{calibration.lenses}` with the track records of the run's lens foragers (`workflow.LensNames`: the nodes a `forager_name` or a `forager-` prefix names, dreamers left out) from their global `lens` calibration scores (cde-mss.md § Calibration recompute; `calibration.LensTrackRecord`). When no lens in the swarm is calibrated it resolves to the empty string, so the resolved prompt is byte-identical to one without the token. Otherwise it resolves to a paragraph that opens with the instruction to weight each lens's agreement in Consensus by its weight and the correlational note, then one line per lens in name order: `<lens>: weight W (hit rate H, n=N)` for a calibrated lens, `<lens>: uncalibrated (n=N < 10)` for one under the floor and `<lens>: uncalibrated (no outcomes)` for one with no score. A weight changes a lens's influence on the synthesis, never whether it runs. The runner resolves it at dispatch, as it resolves `{comb.…}`, so the manual path hands it out as written;
- `{swarm.ledger}` with the swarm ledger, which the lean Queen and coverage evaluator read in place of every full verdict (swarm.md § Persona profiles). It reads the same outcomes and holds: each lens's verdict — recommendation, key points and uncertainties, or the line saying why it has no verdict; the claims the lenses labelled in `def_claims`, `gua_claims`, `asm_claims` and `unk_claims`, grouped under definition, guarantee, assumption and unknown, each naming its lens; the `resonates` pairs with a forager that has no node in this run, which cannot fire; and, for each lens whose verdict is not the tally's plurality (every lens with a verdict when there is none), the rest of its verdict, evidence first. The evidence and other fields of a lens that returned the plurality are left out. That is a choice: Queen may depart from the plurality only by naming the evidence that outweighs it, and that evidence comes from the lenses that did not return it. Whether a Queen that reads less writes as good a report is not measured. The ledger names no lens's model: Queen reads `{tally}`, `{nabla.fired}` and `{diversity}` beside it under either profile, so the lean Queen gets the same diversity line as the full one.

Resolution SHALL never fail dispatch. A missing vantage resolves to empty silently; a malformed key or a database error resolves to empty with a stderr warning; `{comb.region}` on a node with no `comb_vantage` resolves to empty. When this run's node states cannot be read, `{verdict.forager:<name>}`, `{tally}`, `{nabla.fired}`, `{swarm.ledger}` and `{diversity}` resolve to `unavailable (<reason>)`, never to `none`, and `tally_plurality` is empty, so an `accept:` holds the verdict to no count. The counts are then 0, and `tally_line` reads `unavailable (<reason>)`. A token with no resolver stays in the prompt as written.

The engine resolves `{verdict.…}`, `{tally}`, `{nabla.fired}`, `{swarm.ledger}` and `{diversity}` when it hands a node out (`workflow.GetNextNodes`), in the one pass that fills the state placeholders; a state key wins over a run token of the same name. Text a value brings in is not read again, so a token inside a question, a context pack or a lens's own text stays as written. So `chb agent-run` and the manual path, `chb workflow next`, hand out the same prompt, and `chb workflow complete` holds the verdict to the same count. `{comb.…}` and `{cde.…}` are resolved by the runner when it dispatches, where the node's template holds them and nowhere else (`workflow.FillLeft`), so the manual path hands them out as written. Within a run, a node with a `cites:` bond on `<name>` SHALL find `{comb.forager:<name>}` populated once that forager's node has completed, provided its vantage write succeeded.

#### Scenario: A context pack that names tokens
- **WHEN** `chb ask --context-file` reads a file that holds `{tally}`, `{swarm.ledger}`, `{question}` and `{comb.forager:skeptic}`
- **THEN** every forager's prompt holds that text as written, in every run of the same question, and Queen's own `{tally}` and `{swarm.ledger}` are filled

#### Scenario: A missing forager
- **WHEN** a prompt names `{comb.forager:nobody}`
- **THEN** it resolves to empty without a warning, and the node dispatches

#### Scenario: No calibrated lens
- **WHEN** the Queen's prompt carries `{calibration.lenses}` and no lens in the swarm has ten resolved outcomes
- **THEN** her resolved prompt is byte-identical to the prompt with the token removed

#### Scenario: One calibrated lens
- **WHEN** skeptic has 20 lens outcomes, optimist 4 and steward none
- **THEN** the token lists skeptic with its weight, hit rate and n, optimist as `uncalibrated (n=4 < 10)` and steward as `uncalibrated (no outcomes)`, and never the dreamer

#### Scenario: A verdict token without a forager
- **WHEN** a prompt names `{verdict.d1=0}`
- **THEN** it resolves to empty with a stderr warning, and the node dispatches

#### Scenario: A verdict after a prose preamble
- **WHEN** a forager's reply opens with prose and ends with its JSON verdict
- **THEN** `{verdict.forager:<name>}` gives the Queen every field of that verdict, evidence and typed claims included

#### Scenario: Another run overwrites a vantage
- **WHEN** another run writes a different verdict to `forager:<name>` before this run's Queen dispatches
- **THEN** the Queen's `{verdict.forager:<name>}`, `{tally}` and `{nabla.fired}` still read this run's verdict

#### Scenario: A ∇ the sensor missed
- **WHEN** two bonded foragers in the run return `support` and the sensor never handled either event
- **THEN** `{nabla.fired}` names the pair with `(support)`

#### Scenario: A bond to an absent forager
- **WHEN** a `resonates` pair names a forager with no node in this run, and its vantage from an earlier run holds the same verdict as its partner
- **THEN** the pair does not fire

#### Scenario: The ledger's detail
- **WHEN** lenses a and b return `support` and c returns `oppose`, and a lean Queen is handed out
- **THEN** her ledger holds every lens's key points, uncertainties and typed claims, and c's evidence, and neither a's nor b's evidence

#### Scenario: A rejected lens
- **WHEN** a lens forager is rejected and the Queen, who joins settled, is handed out
- **THEN** its token reads `(no verdict: forager-<name> is rejected in this run)` and the tally lists it under `no verdict from <name> (rejected)`

#### Scenario: One model, one verdict
- **WHEN** every lens that returned a verdict ran on qwen3.5:4b and returned support, and a dissenting lens was rejected
- **THEN** `{diversity}` opens `low:` and names support and qwen3.5:4b; the rejected lens casts nothing

#### Scenario: Two models and a lens with no model recorded
- **WHEN** lenses on qwen3.5:4b and on ministral-3:8b, and one lens whose row records no model, all return support
- **THEN** `{diversity}` opens `not low:` and names both models, since two recorded models already rule out one model's view

#### Scenario: More abstains than any verdict
- **WHEN** three lenses abstain and one returns `support`
- **THEN** the tally lists all four and names `support` the plurality

#### Scenario: A read error
- **WHEN** reading this run's node states fails as the Queen is handed out
- **THEN** each of her run tokens reads `unavailable (<reason>)`, `tally_plurality` is empty, and she dispatches

#### Scenario: The manual path
- **WHEN** `chb workflow next` hands out the Queen of a generated swarm
- **THEN** her prompt holds each verdict, the tally and the ∇ line, as agent-run's does

### Requirement: Time Wheel and revision history

Every Comb write SHALL append a `comb_revisions` row anchored to an open Time Wheel tick: a forager write to its own run's tick, a region write to the most recently opened tick of any kind. A revision written with no such tick has a NULL `tick_id`. Ticks come from four producers: each workflow run opens and closes a `swarm` tick, and a run resumed with `chb agent-run --resume` reopens its own tick until it ends again; any gate opening, by `chb guard` or `chb db-write gate_wave`, records a closed `wave` tick (labelled `wave-N`, so gating a wave again records nothing new); `chb ripen` opens a new `ripen` tick around each loop, its label unique to that invocation; `chb calibrate`, and the `calibrate` workflow node (workflow.md § The calibrate node), open a new `calibrate` tick around each recompute that writes, its notes carrying the highest outcome id it read (cde-mss.md § Calibration recompute). Tick writes are best-effort and never stop the work they mark, except the `calibrate` tick, which the recompute requires. `chb comb at --vantage K --time T` and `--tick N` SHALL return the revision current then, by the `(vantage_key, revision_at)` and `(tick_id, vantage_key)` indexes. For `--tick N` that is the latest revision anchored to tick N, else the latest written before N opened. Ticks open in id order and a revision is anchored only to an open tick, so a revision anchored to a later tick never counts, whatever its time. One anchored to no tick, or to an earlier tick, which a concurrent run or a resumed one can still be writing to after N opens, counts when written at or before N's start time. Both times are to the second, so such a revision written in N's own second counts as before N, even when it came after. `chb comb diff --from --to` returns confidence delta, label flips and narrative diff; `chb comb history [--limit N]` lists the arc newest first; `chb comb wheel [--kind K]` lists ticks and refuses a kind outside `swarm`, `wave`, `session`, `day`, `manual`, `ripen`, `calibrate`.

#### Scenario: Belief at a tick
- **WHEN** a forager vantage was revised during tick 4 and again later
- **THEN** `comb at --tick 4` returns the tick-4 revision

#### Scenario: A revision in the tick's own second
- **WHEN** a vantage is revised in the second tick 4 opens, once before tick 4 opened and once under a tick opened after it
- **THEN** `comb at --tick 4` returns the revision written before tick 4 opened

#### Scenario: Concurrent runs
- **WHEN** runs A and B are both open and a forager in run A writes its verdict after B opened its tick
- **THEN** the revision is anchored to A's tick

#### Scenario: A resumed run
- **WHEN** a run paused at a `human_review` gate is resumed and a forager then writes its verdict
- **THEN** the revision is anchored to the run's own `swarm` tick

#### Scenario: Two ripens in one second
- **WHEN** `chb ripen` runs twice within the same second
- **THEN** `comb wheel --kind ripen` lists two ticks

### Requirement: Event bus

`comb.Default` SHALL be the process-wide event bus. Every forager and region write publishes `vantage_written` carrying the run id that produced it (0 outside a run), the bus's one kind of event. There are no tick events. Subscribers — the ∇ convergence sensor — receive on buffered channels, and a publish never blocks: an event for a full subscriber is dropped and not counted. The bus is not shared across processes: an event reaches only subscribers in the process that made the write.

#### Scenario: A stalled subscriber
- **WHEN** a subscriber stops reading while a run writes many vantages
- **THEN** the run is not slowed; that subscriber misses events

#### Scenario: A write in another process
- **WHEN** `chb comb refresh` writes region vantages in another process while a run's ∇ sensor is subscribed
- **THEN** the sensor receives no `vantage_written` event

### Requirement: ∇ convergence sensor

For each run whose workflow declares `resonates` pairs, `comb.QuorumSensor` SHALL watch forager vantage-written events, ignoring those stamped with another run's id, and fire when two `resonates`-bonded foragers' latest verdicts in the run match (neither empty nor `abstain`): it records a `forager_bonds` row with `fired=1` and writes a `nabla` signal — at most once per pair per run. The sensor drains its buffer when the run ends, and the run waits for the drain before it returns; a ∇ completed during the drain is still recorded. A run resumed with `chb agent-run --resume` starts its sensor from the verdicts its foragers already wrote in the run (their revisions on the run's tick) and from the bonds that already fired in it. The sensor changes no label. Convergence never makes a guarantee, for verdicts or findings: the hive's `quorum` caps a converged finding, and it stays an assumption (hive.md).

The Queen's ∇ line SHALL come from `{nabla.fired}`, computed at dispatch from this run's node outputs, not from the sensor's record. The two can differ. The sensor can miss a ∇ when the bus drops an event. It can also keep a ∇ that fired on a verdict a repair later replaced.

#### Scenario: Two concurrent runs
- **WHEN** two runs in one process each write a verdict for a bonded pair
- **THEN** each run's sensor considers only its own run's verdicts

#### Scenario: A repaired verdict
- **WHEN** the sensor fires on a pair's first-attempt verdicts and a repair changes one of them
- **THEN** `forager_bonds` keeps the fired row, and the Queen's line omits the pair

#### Scenario: Verdicts on both sides of a resume
- **WHEN** one forager of a bonded pair writes its verdict before the run pauses, and the other writes a matching verdict after it resumes
- **THEN** the pair fires once for the run

### Requirement: Dreamer archetype

A forager with `archetype: dreamer` SHALL become a workflow node `{type: agent, agent: <name>, archetype: dreamer}` with no model, which the runner dispatches through `dreamer.Run` instead of a model: outputs `passes`, `touched` and `halted`, plus a `forager:<name>` vantage — verdict `abstain`, since it takes no position on the question and must not count toward a ∇, with a recommendation naming the halt reason when the loop halted. The node runs recommend-only and treats a halt as completion, not failure. Preflight does not require `accept:` on it. The balanced preset has no dreamer; `default` and `all` do, and it runs after the queen. `chb ripen` runs the same loop directly, with `--apply`, `--dry-run`, `--max-passes` (default 5; below 1 is refused), `--passes`, `--gap-age-days` (default 7) and `--jaccard-min` (default 0.7). A 0 for `--gap-age-days` or `--jaccard-min` is used as given, not replaced by the default.

The five passes, in order, each one `ripen_log` row:

| Pass | Behaviour |
|---|---|
| `prune` | same-coordinate assumption pairs at Jaccard ≥ `--jaccard-min`, plus embedding-cosine ≥ 0.85 pairs when finding embeddings exist, under the model of the most recently written finding embedding (highest id): emits a `stop_signal` for each pair that has none yet. Recommend-only with or without `--apply`; it never merges findings |
| `reprove` | guarantees with a dependency that an `alarm` or `stop_signal` signal (sourced from a finding or audit) or an `updated_at` post-dates — measured from the guarantee's creation or its last reprove `alarm`, whichever is later — and guarantees resting on one flagged in the same pass: emits an `alarm` (payload `source` `dreamer.reprove`). It visits guarantees dependencies first, so a dependency's `alarm` is never written after its dependent's |
| `contradict` | `gate.DetectConflicts` across every wave; records each conflict not already recorded (cde-mss.md) |
| `hypothesize` | unresolved `critical` / `important` gaps older than `--gap-age-days` (compared in UTC) become followups, deduplicated by question and d1–d4 |
| `settle` | guarantees whose dependency chain reaches an `unknown`, walked through every label as the MSS audit's laundering check walks it, get an `alarm` unless one stands (a reprove `alarm` does not count); with `--apply` they are demoted to `assumption` with dependencies cleared, including those an earlier run flagged. The walk stops at other guarantees, so it acts on the last guarantee before the `unknown` on each chain; demoting that one cuts the chain, and the guarantees above it keep their label. A demotion that moves a region's dominant label makes it stale by § Staleness |

Each pass SHALL observe a 5-minute deadline before each row it acts on (`contradict` only before it starts) and act on at most 200 rows — rows it skips as already handled do not count, so later runs reach the rest (`contradict` records every new conflict). A pass cut by its deadline reports the rows it already acted on as touched, and what it wrote for them stands. Timestamps have one-second resolution: a dependency change in the same second as a guarantee's last reprove `alarm` is not seen. Before every pass the QMP gate SHALL halt on a partition violation or dependency cycle; on laundering it first runs `settle` as a repair, re-audits, and halts with reason `laundering` only if laundering remains or `--passes` excluded `settle`. A halt is `dreamer.ErrHalted`, recorded as a `halted` `ripen_log` row and a QMP signal (neither on `--dry-run`); `chb ripen` exits non-zero on it. Passes report a cost column, which the shipped passes leave at 0. There is no daemon: cadence comes from outside (a scheduler, a loop, a swarm with a dreamer).

#### Scenario: Laundering ahead of prune
- **WHEN** the comb holds a guarantee resting on an unknown and `chb ripen --apply` starts
- **THEN** `settle` runs first and demotes it, the audit passes, and the loop continues with `prune`

#### Scenario: Laundering behind an assumption
- **WHEN** a guarantee rests on an assumption that rests on an unknown, and `chb ripen --apply` runs
- **THEN** `settle` demotes the guarantee, the audit passes, and the loop does not halt

#### Scenario: Apply after a recommend-only run
- **WHEN** a swarm's dreamer node flagged a laundered guarantee and `chb ripen --apply` runs later
- **THEN** settle demotes the guarantee without a second alarm, and the loop does not halt

#### Scenario: A second ripen over an unchanged comb
- **WHEN** `chb ripen` runs twice without halting, no pass in the first run reached its 200-row cap, and nothing else writes a finding, signal or gap in between
- **THEN** the second run writes no signal, conflict or followup (a halted run records its halt each time)

#### Scenario: A cancelled pass
- **WHEN** a pass's deadline expires part-way through its rows
- **THEN** the pass fails with the context error and records no further rows; the rows it already acted on count as touched, and what it wrote for them stands

### Requirement: Embeddings

`comb_embeddings` SHALL store one packed little-endian float32 vector per `(vantage_key, model)` with its dimension and source text, for kinds `region`, `forager`, `finding` (`finding:<id>`) and `question` (`question:<run_id>`, from a run's `inputs_json`; embeddings only). A write replaces its row with a new id, so the most recently written model is the highest id. Queries filter by model, so vector spaces never mix. Providers implement `Name`, `Dim`, `Embed` and `BatchEmbed`: OpenAI (`OPENAI_BASE_URL`, default `text-embedding-3-small`, 1536 dimensions), a local Ollama-compatible HTTP provider (`HIVE_LOCAL_EMBED_URL`, default `nomic-embed-text`, at most four requests at once, dimension learned from the first response) and a stub (FNV-1a, 64 dimensions, `stub/v1`). Each request of either HTTP provider SHALL first wait for its server's endpoint slot, the one chb's model calls to that server wait for (runner.md § Backends). With `HIVE_MAX_PARALLEL_ENDPOINT` unset or 1, a server on this machine gets one request at a time from chb, embedding or model call, across the chb processes on this machine. At N, chb sends it up to N at once, embeddings and model calls together, and a server that answers one at a time queues the rest. A server elsewhere has no bound unless the variable sets one. The OpenAI provider sends one request per batch. The HTTP timeout (120 s local, 60 s OpenAI) starts once the request has its slot. No chb command embeds and calls a model in one process today: `chb comb embed` and `chb recall` call no model. So what the slot changes for them is that a local provider's batch to this machine goes one request at a time by default, not four. The slots are shared across processes through lock files (runner.md § Backends): `chb comb embed` in one shell and `chb ask` in another take turns at a single-slot server, and neither's wait counts against its HTTP timeout. A process with no lock files, such as a Windows build, has slots of its own, and the server gets what it sends beside the others. Selection is `--provider`, else `HIVE_EMBED_PROVIDER` (both `openai`, `localhttp` or its alias `ollama`, or `stub`; any other name is refused), else `HIVE_LOCAL_EMBED_URL` → localhttp, `OPENAI_API_KEY` → openai, else the stub with a stderr warning. The local URL comes first because setting it asks for local embeddings and nothing else, while a local OpenAI-compatible server's model calls also need `OPENAI_API_KEY` set. `HIVE_EMBED_MODEL` overrides the provider's default model. The OpenAI provider's default is OpenAI's model, so behind an `OPENAI_BASE_URL` on this machine it needs `HIVE_EMBED_MODEL` to name a model that server has: Ollama 0.34.4 answers `text-embedding-3-small` with HTTP 404, `model "text-embedding-3-small" not found`. Search is brute-force cosine in memory (`Searcher.NearestK`), with an optional kind filter and anchor exclusion; a searcher reloads when the model's row count changes and lives for one command. Cosine returns 0 on mismatched dimensions or a zero vector. Everything is pure Go on `modernc.org/sqlite`.

`chb comb embed` SHALL re-embed every selected vantage each run (`--vantage`, `--kind region|forager`, `--provider`, `--model`, `--dry-run`); `--findings [--wave N]` and `--questions` embed only rows missing an embedding for the model. `chb comb embed-status` reports counts per model and kind; `chb comb similar --vantage K [--top N] [--kind region|forager|finding|question]` ranks neighbours on the most recent model unless `--model` names one. An unknown `--kind` is refused. `chb recall "<q>"` embeds the question and ranks the questions prior runs were asked, returning each match with its own run's queen output; with no question embeddings it says so. The queen's output is her JSON object: her stored outputs when they hold a `verdict`, else the object her text holds (`ExtractJSONOutput`, which reads one in a code fence or after a preamble). Under `--json` a match carries its `verdict`, `recommendation` and `report` as fields, not her raw text. A queen whose object holds no verdict wrote prose, and her whole text is the match's `report`. The text output puts one truncated line under each match: `<verdict> — <recommendation>`, else her prose.

#### Scenario: Recall without question embeddings
- **WHEN** `chb recall` runs in a workspace where `chb comb embed --questions` never ran
- **THEN** it reports nothing to recall and names that command

#### Scenario: Recall of a queen's object
- **WHEN** the matched run's queen answered `{"report": "## Swarm Verdict …", "verdict": "conditional", "recommendation": "Ship once the seam test passes."}`, stored or in a fenced block of her text
- **THEN** `--json` gives that match `verdict`, `recommendation` and `report` equal to the object's fields, and the text line reads `queen: conditional — Ship once the seam test passes.`

#### Scenario: Embedding on a single-slot local server
- **WHEN** `chb comb embed --findings` embeds 12 findings through `HIVE_LOCAL_EMBED_URL=http://127.0.0.1:11434`, with `HIVE_MAX_PARALLEL_ENDPOINT` unset
- **THEN** the server holds one request at a time; with the variable at 2 it holds two, and at 8 it holds four, the provider's own limit

#### Scenario: Embedding while a model call runs
- **WHEN** a model call to `http://localhost:11434/v1` holds that server's one slot, and the same process embeds through `http://127.0.0.1:11434`
- **THEN** the embedding request is not sent until the model call has its answer

#### Scenario: Embedding beside a run in another shell
- **WHEN** `chb comb embed --findings` in one shell embeds through `http://127.0.0.1:11434` while `chb ask` in another calls `http://localhost:11434/v1`, with no variable set
- **THEN** the server holds one request at a time from the two processes, and each request's timeout starts once it has the slot

#### Scenario: A local embedding URL beside an OpenAI key
- **WHEN** `HIVE_LOCAL_EMBED_URL=http://127.0.0.1:11434` and `OPENAI_API_KEY` are both set, and no provider is named
- **THEN** the local provider embeds, at that URL, with `nomic-embed-text` unless `HIVE_EMBED_MODEL` names another

## Files

- `internal/comb/regions.go`, `builder.go` — region keys, digests, `BuildAllRegions`; `internal/comb/tally.go` — `regionTallies` (the refresh's and the staleness rule's reads); `internal/comb/vantage.go` — `WriteRegionVantage`, `BuildForagerVantage`
- `internal/comb/staleness.go` — `Classify`, `StaleVantages`: the staleness rule
- `internal/comb/caps.go` — `CappedCoords`, `CappedCells`: the caps the Comb shows
- `internal/comb/template.go` — `Resolve`
- `internal/comb/eventbus.go`, `quorum.go` — the bus, the ∇ sensor and `ConvergedPairs`
- `internal/workflow/run_tokens.go` — the run tokens' resolution, the lens foragers they read (`LensNames`), `{verdict.forager:<name>}` and `{nabla.fired}`
- `internal/workflow/tally.go` — `{tally}`: the vote tally, and `tally_plurality` and the other `tally_` values
- `internal/workflow/diversity.go` — `{diversity}`: `LensDiversity`, `DiversityOf`, `RunLensAnswers`, `RunDiversity`
- `internal/workflow/verdict.go` — a verdict's fields as `{verdict.forager:<name>}` and the ledger write them
- `internal/workflow/ledger.go` — `{swarm.ledger}`
- `internal/db/comb.go`, `comb_revisions.go`, `time_wheel.go`, `forager_bonds.go`, `ripen.go`, `comb_embeddings.go` — the tables
- `internal/dreamer/` — `passes.go`, `loop.go` (`Run`, `qmpGate`), `qmp.go`, the five pass files, `prune_semantic.go`, `regions.go`
- `internal/runner/dispatch_dreamer.go` — dreamer nodes
- `internal/embed/` — `provider.go`, `openai.go`, `localhttp.go`, `stub.go`, `cosine.go`, `searcher.go`
- `internal/endpointslot/endpointslot.go`, `lock_flock.go` — the endpoint slots the HTTP providers wait for, shared across processes by lock files
- `internal/cli/comb.go`, `comb_history.go`, `comb_embed.go`, `ripen.go`, `recall.go` — CLI
