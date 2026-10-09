# CDE / MSS Knowledge Store Specification

## Purpose

Typed, auditable storage for agent findings. CDE (Coleman Dimensional Encoding) gives each finding up to eight coordinate dimensions for bounded retrieval; MSS labels (definition / guarantee / assumption / unknown) carry each claim's epistemic standing and are enforced on write and audited before synthesis. The framework reference is chronomancy.io. The labels mean one thing everywhere: in `docs/foundations.md`, the persona prompts, the workflows and the harness tasks. A `definition` is a choice made, true by stipulation: a scope, a threshold, a price set, what a term means here. It needs no source, and a fact about the world is never one. An `assumption` is a bet on something not verified, including a fact read from a source the writer did not verify; it cites that source in `source_urls`. A `guarantee` follows from the findings named in `depends_on_ids` and is only as sure as they are. An `unknown` is an honest gap and never supports a guarantee.

## Requirements

### Requirement: Schema and version

Opening a store SHALL apply the whole schema as `CREATE … IF NOT EXISTS` statements — at `SchemaVersion` 1 there is no migration to run — so re-applying it to a current database changes nothing. An attempt SQLite answers with `SQLITE_BUSY` (concurrent first opens racing to switch a new file to WAL) is retried whole, for up to 30 seconds. The schema version `SchemaVersion` (1) is stamped in `PRAGMA user_version`, and a database stamped higher SHALL be refused, naming both versions. A database stamped lower — 0 for a new file or one written before the stamp — is stamped current as it stands: `CREATE … IF NOT EXISTS` adds missing tables and indexes but never a column or a constraint to an existing table. `SchemaVersion` is bumped with any change a previous release could not read; from v0.1.0 on, a bump ships a migration beside it. A database written before v0.1.0 SHALL be deleted and then created again with `chb db-init`. `chb db-init` does not recreate an existing file: it keeps the old tables and rows and stamps the file current, and later writes fail on the missing columns. `chb db-repair --table signals` SHALL rebuild the signals table and its indexes with the statements the schema creates them with (`SignalsSchema`), so the rebuilt table's `sqlite_master` entries are a new store's. It forgets a table `DROP TABLE` cannot drop through `writable_schema` only when SQLite reports the file damaged, by result code: `SQLITE_CORRUPT` or `SQLITE_NOTADB`.

#### Scenario: A newer database
- **WHEN** a chb built with schema version 1 opens a database stamped 2
- **THEN** opening fails and no table is created

#### Scenario: A rebuilt signals table
- **WHEN** `chb db-repair --table signals` rebuilds a signals table `DROP TABLE` reports corrupt
- **THEN** the table and its indexes have the `sqlite_master` entries a new store's have

### Requirement: Findings and supporting records

A finding SHALL carry `wave`, `agent`, `mss_label`, `finding`, optional `evidence`, `source_urls` and `depends_on_ids`, coordinates `d1`–`d8`, `convergence_count` (1 on write) and `convergence_level`, and `updated_at` (NULL until `UpdateFinding` changes it). The store SHALL also hold `gaps` (question, priority `critical`\|`important`\|`minor`, coordinates), `conflicts` (two finding ids), `followups`, `sources` (URL, title, contribution, validation status) and `evaluations` (per-wave scores 1–5 for coverage, depth, sources, actionability and MSS integrity, and a verdict `COMPLETE`\|`NEEDS_MORE_WORK`\|`NEEDS_MINOR_FOLLOWUP`). A source URL is stored once: writing a known URL keeps the stored record and only fills a missing title and a missing wave. `chb validate-sources` records its verdict on a URL's row, creating the row when there is none, and with `--wave` fills a missing wave with the wave it scanned. Only `chb_db_write`, the runner's tool and the MCP tool alike, reports the source's id; `chb db-write source` reports none. A conflict found by detection (`chb detect-conflicts`, `chb swarm-merge`, the dreamer's `contradict`, and the gate, which runs detection over its wave each time it runs) is recorded once per finding pair and description, resolved or not, under the later of its two findings' waves whether or not detection was given a wave; `db-write conflict` records what it is given. A conflict SHALL be closed only by `ConflictsRepo.Resolve`, which `chb db-write resolve_conflict` and the `resolve_conflict` kind of `chb_db_write` call, and by the gate's numeric auto-resolve (`--auto-resolve-numeric`); nothing else writes a conflict's `resolution`. A gap SHALL be closed only by `GapsRepo.ResolveGap`, which `chb db-write resolve_gap` and the `resolve_gap` kind of `chb_db_write` call. It records the resolving wave, agent and finding and refuses an unknown gap, an already-resolved gap, a missing agent and a finding that does not exist; nothing else writes a gap's `resolved_by_wave`.

#### Scenario: Detection re-runs after a resolution
- **WHEN** a detected conflict is resolved and detection runs again over the same findings
- **THEN** no new conflict row is written and the resolved one stays resolved

#### Scenario: Resolving a gap twice
- **WHEN** `chb db-write resolve_gap` names a gap already resolved
- **THEN** the write is refused and names the wave that resolved it

#### Scenario: A conflict found without a wave
- **WHEN** the dreamer's `contradict` pass, which runs without a wave, records a conflict between two wave-1 findings
- **THEN** the conflict row carries wave 1, where the wave 1 gate counts it

#### Scenario: Re-citing a source
- **WHEN** `chb_db_write`, in the runner or over MCP, writes a source whose URL is already on record, without a title
- **THEN** the stored title is kept and the write reports that record's id

#### Scenario: Registering a source validated without a wave
- **WHEN** `chb db-write source` registers for wave W a URL that `chb validate-sources` recorded without a wave
- **THEN** the row takes wave W and keeps its verdict, and the wave W gate counts it

### Requirement: The write path

`Store.WriteRecord` SHALL be the one write path for the kinds `chb_db_write` takes: `finding`, `gap`, `source`, `resolve_gap` and `resolve_conflict`. `chb_db_write`, in the runner and over MCP, and `chb db-write` for those five kinds pass through it, so every surface decodes, checks and writes such a record the same way. It refuses, writing nothing, a field the kind does not read, with an error that names the field and lists the kind's fields, a numeric field (`wave`, a coordinate, an id, `primary_source`) that is not a whole number within `int`'s range: text such as `"2"` as not numeric, a fraction such as `1.5` as not whole; and a text field (`agent`, `mss_label`, `finding`, `evidence`, a gap's `description` and `priority`, a source's `url`, `title` and `contribution`, a `resolution`) that is not a string, such as `5`, which would otherwise be stored as no value. A field set to `null` is not set. A gap's priority is stored as one of `critical`, `important` and `minor`: `high` and no priority are `important`, `medium` and `low` are `minor`, and any other value is refused by the table's CHECK. A finding with no `mss_label` is an `assumption`. It writes through the repositories: `FindingsRepo.AddFinding`, the insert `GapsRepo.AddGap` makes, `SourcesRepo.AddSource`, `GapsRepo.ResolveGap` and `ConflictsRepo.Resolve`. It returns the row it wrote or closed and the line `chb_db_write` replies with; `chb db-write` prints its own: `Finding id=N` for a finding, nothing for a gap or a source, and the reply line for a resolve. Every other `chb db-write` kind that takes a JSON payload SHALL refuse a key it does not read, with an error that names the key and lists the keys the kind takes, and write nothing. It SHALL hold its integer fields to whole numbers and its text fields to strings by the same check and with the same error (`db.CheckFieldTypes`), so `"wave": 1.5` is refused by every kind, not stored as 1. `chb ingest` and `chb ingest-findings` hold each finding, gap and follow-up they write to the same check; a marker that fails it fails to write. Keys and fields match exactly, case included.

#### Scenario: A key the kind does not read
- **WHEN** `chb db-write finding`, or the `finding` kind of `chb_db_write`, is given `agent_id` where it takes `agent`
- **THEN** the write is refused with an error that names `agent_id` and lists the kind's fields, and no row is written

#### Scenario: A wave that is not a whole number
- **WHEN** `chb db-write finding` or `chb db-write followup` is given `"wave": 1.5` or `"wave": "2"`
- **THEN** it exits non-zero with the error `chb_db_write` gives a finding with that wave, and no row is written

#### Scenario: A text field that is not a string
- **WHEN** `chb db-write finding`, or the `finding` kind of `chb_db_write`, is given `"agent": 5`
- **THEN** the write is refused with one error that names `agent`, and no row is written

#### Scenario: A high-priority gap
- **WHEN** `chb db-write gap`, or the `gap` kind of `chb_db_write`, is given priority `high`
- **THEN** the gap is stored `important`


### Requirement: Dimension registry and coordinates

Dimensions SHALL be registered by name (`chb db-write dimension`) with `values_json`, a non-empty JSON array of strings; `null` and `[]` are refused, because an empty domain bounds every coordinate out. The registry's row order maps dimension N onto `dN`. At most eight may be registered. Re-registering a dimension updates its description and values in place and never moves its slot. `cde.ValidateCoords` SHALL refuse, on write, a NULL on any registered dimension, a value outside its domain, a registry entry whose domain does not parse, and a registry wider than eight. Registering a dimension warns how many stored rows it leaves NULL on the new slot; stored rows are not re-validated.

#### Scenario: A value outside the domain
- **WHEN** a finding sets `d2` to 7 and the second dimension has five values
- **THEN** the write is refused

### Requirement: MSS write rules

Every finding's label SHALL be one of the four (`CHECK`). A `guarantee` SHALL name non-empty `depends_on_ids`, every one an existing finding, with no `unknown` anywhere in its dependency chain — refused at write otherwise (`FindingsRepo.AddFinding`, `mss.ValidateGuaranteeDeps`). The write-time walk is the MSS audit's (`mss.WalkDeps`): it follows every dependency's own dependencies, whatever its label. A finding of any other label SHALL name only existing findings in `depends_on_ids`; a missing one is refused with the error the guarantee case gives (`mss.ValidateDepsExist`, on `AddFinding` and on `UpdateFinding`). `UpdateFinding` SHALL re-validate a finding that is or becomes a guarantee, refuse a `depends_on_ids` change of any label that names a missing finding or would close a dependency cycle (`mss.CheckCycle`, which names the cycle), and refuse to relabel a finding `unknown` while a guarantee rests on it, directly or through a chain of any labels (`mss.GuaranteesRestingOn`): that error names those guarantees and `chb db-write cascade_revert <id>`, which reverts them first. The cascade writes its reverts itself (`RevertFindingToUnknown`), outward from the trigger, and that check does not apply to them. A change beneath a stored guarantee that keeps the finding's label (say, an assumption it rests on gaining an `unknown` dependency) is not re-checked at write; the MSS audit catches it. `depends_on_ids` is accepted as a JSON array or a JSON-string array on every surface (`db.NormalizeDependsOnIDs`), `db-write update_finding` and `db-write promote_finding` included. `AddFinding` SHALL store an `assumption` or `guarantee` written without `source_urls` and print a warning naming its label on stderr; a `definition` or `unknown` draws none. Nothing checks a label's meaning: that a definition is a choice, or that a source says what the finding says.

#### Scenario: An assumption without its source
- **WHEN** an `assumption` is written with no `source_urls`
- **THEN** the row is written and stderr warns that the assumption finding has no source_urls

#### Scenario: Direct laundering refused
- **WHEN** a guarantee's `depends_on_ids` includes an `unknown`
- **THEN** the write is refused and no row is written

#### Scenario: Laundering through an assumption refused
- **WHEN** a guarantee depends on an assumption that depends on an `unknown`
- **THEN** the write is refused, as the MSS audit would fail it

### Requirement: Outcomes and calibration scores

The store SHALL hold an append-only `outcomes` ledger. Each row records the resolution (`confirmed`, `refuted` or `partial`) of one subject: a `finding`, a `lens_verdict` or a `synthesis_verdict`. It records the `source` (`human`, `downstream_run` or `external`), the subject's coordinates `d1`–`d4`, the confidence stated at belief time when known (`stated_confidence`, 0–100), and the belief and resolution ticks. CHECK constraints enforce the subject identity: a `finding` names `finding_id` and `subject_label`, the finding's label when the outcome was recorded; a `lens_verdict` names `lens` and `run_id`; a `synthesis_verdict` names `run_id`. `calibration.Record` is the one write path: for a finding it copies the label and the coordinates from the finding. `chb outcome-record <json>` records with source `human` and `chb outcome-import <path.json>` with source `external`; each reads one object or an array in the same shape (`calibration.Input`: `subject_kind`, `finding_id`, `lens`, `run_id`, `belief_tick_id`, `d1`–`d4`, `resolution`, `stated_confidence`, `predicted_value`, `actual_value`, `rationale`, `evidence_urls`, `resolved_tick_id`) and refuses a key outside it. The MCP tool `chb_outcome_record` records one object with source `human` through the same shape and the same refusals (mcp.md). `downstream_run` outcomes come from one place: adjudicating a conflict (`calibration.Adjudicate`, which `chb db-write conflict_winner` calls) names the winner through `ConflictsRepo.SetWinner` and writes one `refuted` outcome on the losing finding, naming the conflict and the winner in its rationale; an adjudication SetWinner refuses writes nothing. Conflict detection, in the gate and in the dreamer's `contradict` pass, writes none: it records that two findings disagree, not which one is wrong. No outcome is derived from convergence or from a model's confidence. When sources disagree on a finding, its confirmation state (`calibration.ConfirmationState`) SHALL follow the outcome of the highest-ranking source, `external` over `human` over `downstream_run`, and the latest row within a source; the state is reviewed when that source is `human` or `external`. The precedence decides the state a consumer reads; the scores count every row, and the ledger keeps every row.

`calibration_scores` SHALL hold one row per `(predictor_kind, predictor_key, scope_key)`: `predictor_kind` is `lens`, `label`, `convergence` or `synthesizer`; `scope_key` is `''` or a coordinate prefix (`d1=x`, `d1=x;d2=y`); the counts `n_resolved`, `n_confirmed`, `n_refuted`, `n_partial`, `brier_sum` and `brier_n`; the derived `hit_rate`, `brier_score`, `weight` and `calibrated`; and `updated_tick_id`, the `calibrate` tick that last changed the row. `calibration_revisions` SHALL hold an append-only snapshot of each changed row under the tick that changed it. The `finding_confirmation` view counts a finding's outcomes per resolution. The ledger and the scores are tables of their own: the `findings` table carries no empirical state.

#### Scenario: A finding outcome without a label
- **WHEN** an outcome with `subject_kind` `finding` omits `subject_label`
- **THEN** the insert fails the CHECK and no row is written

#### Scenario: A database without the ledger
- **WHEN** a database without the ledger tables is opened
- **THEN** it gains `outcomes`, `calibration_scores`, `calibration_revisions` and `finding_confirmation`, and nothing else in it changes

#### Scenario: An adjudicated conflict
- **WHEN** `chb db-write conflict_winner` names finding A the winner of its conflict with B
- **THEN** one `refuted` outcome with source `downstream_run` is written on B, with B's label and coordinates, and no finding's label changes

#### Scenario: Detection alone
- **WHEN** `chb detect-conflicts` or the dreamer's `contradict` pass records a new conflict
- **THEN** no outcome is written

#### Scenario: Sources disagree
- **WHEN** finding F holds a `downstream_run` refutation and a later `external` confirmation
- **THEN** F's confirmation state is `confirmed` by `external`, and both rows stay in the ledger

### Requirement: Calibration never writes labels

Recording an outcome SHALL NOT change any finding's `mss_label` (CALIB-1): nothing in `internal/calibration` writes the `findings` table. A `refuted` outcome on a finding from `human` or `external` SHALL call `store.CascadeRevert` on it (CALIB-2), so every finding that depends on it and is not `unknown` already reverts to `unknown` through the existing cascade, with a critical gap and an alarm each; the refuted finding keeps its label, because the cascade does not revert its trigger. A `downstream_run` refutation SHALL NOT call it: the adjudication that writes it (`calibration.Adjudicate`, § Outcomes and calibration scores) arms the alarm cascade itself (hive.md). A refuted lens or synthesis verdict names no finding and cascades nothing, and a `partial` outcome cascades nothing. Labels change only through the MSS write rules, the cascade and the dreamer's `fix_mss` settle pass. HIVE quorum changes no label (hive.md).

#### Scenario: A confirmation
- **WHEN** a `confirmed` outcome is recorded on a guarantee
- **THEN** every finding's `mss_label` and `depends_on_ids` are unchanged

#### Scenario: A human refutation
- **WHEN** a `human` `refuted` outcome is recorded on finding F, guarantee G depends on F and guarantee H depends on G
- **THEN** G and H revert to `unknown` with a critical gap each, and F's label is unchanged

#### Scenario: A downstream refutation
- **WHEN** a `downstream_run` `refuted` outcome is recorded on finding F, and guarantee G depends on F
- **THEN** the row is written, G keeps its label and no gap is opened

### Requirement: Calibration recompute

`chb calibrate [--rebuild] [--scope <prefix>] [--json]` SHALL score every predictor in every scope from the whole ledger (`calibration.Recompute`). It makes no model call and writes no finding. Without `--rebuild`, a run with no outcome recorded since the last `calibrate` tick changes nothing and opens no tick. Otherwise it opens a `calibrate` tick whose notes carry the highest outcome id it read, upserts the rows that changed, deletes the rows whose key has no outcome left, keeps an unchanged row's `updated_tick_id`, snapshots the changed rows into `calibration_revisions` under the tick, and closes it. The scores are a function of the ledger, the fired `resonates` bonds and the forager names, so two runs over the same rows write a byte-identical table and add no revision the second time. `--scope` filters what is printed, not what is computed. The `calibrate` workflow node runs the same recompute in a run (workflow.md § The calibrate node). On a run with nothing new, the result still carries the standing guarantee-floor drift of the current scores and the lowest calibrated lens; a hit-rate drop is reported once, by the recompute that saw it.

The formula is normative; its constants are in `internal/calibration/params.go` (`NFloor` 10, `KShrink` 10, `WMin` 0.5, `WMax` 2.0, `PriorA` = `PriorB` = 1.0, `GuaranteeConfirmFloor` 0.95, `DriftDelta` 0.20). With confirmed *c*, partial *p*, refuted *r* and *n = c + p + r*:
- `hit_rate = (c + 0.5p) / n`, 0 when *n* = 0;
- `p̂ = (c + 0.5p + PriorA) / (n + PriorA + PriorB)`;
- `w_raw = clamp(p̂ / p̄, WMin, WMax)`;
- `w = 1 + (w_raw − 1) · n / (n + KShrink)`;
- `calibrated = n ≥ NFloor`;
- `brier_score = mean((stated_confidence / 100 − y)²)` over the outcomes that stated a confidence, with *y* 1 for confirmed, 0.5 for partial and 0 for refuted; NULL when none did. It does not enter `w`.

The reference rate `p̄` is the label's target for `label` (`guarantee` 1.0, `definition` 0.95, `assumption` 0.6, `unknown` 0.5), and otherwise the posterior mean `p̂` of a pool in the same scope: every lens outcome for `lens`, every synthesis verdict for `synthesizer`, and the synthesis verdicts of runs with no ∇ for `convergence`. So a sole lens weighs 1, the Queen weighs 1 against herself, and a `convergence` weight above 1 means the ∇-positive runs' verdicts held more often than the others'. Attribution: `label` scores `subject_label`; `lens` scores `lens_verdict` rows under `lens`, plus `finding` rows whose finding's `agent` names a forager in the tree `chb ask` reads; `synthesizer` scores every `synthesis_verdict` under `queen`; `convergence` scores under `nabla` the synthesis verdicts of runs with a fired `resonates` row in `forager_bonds`. Scopes are `''` and every `d1=x` and `d1=x;d2=y` prefix holding at least `NFloor` outcomes of any kind; an outcome without `d1` counts toward `''` only. A score with `calibrated = 0` SHALL be treated as weight 1.0 by every consumer.

Drift is reported for calibrated rows only: a `label`/`guarantee` row with `hit_rate` under `GuaranteeConfirmFloor`, and a row whose `hit_rate` fell by `DriftDelta` or more since its previous revision, that revision calibrated too. The ∇ report lists each scored `convergence` row with the ∇ and no-∇ counts and hit rates and says whether ∇ predicts there (calibrated, weight above 1) or does not. `lowest_calibrated_lens` is the calibrated global lens with the smallest weight. `chb db-read calibration [--kind K] [--scope S] [--json]` lists the scores and marks a row under the floor `uncalibrated (n<10)`. Every surface that shows a weight says that calibration is correlational, not proof of skill.

#### Scenario: Determinism
- **WHEN** `chb calibrate --rebuild` runs twice over the same outcomes
- **THEN** `calibration_scores` is byte-identical, `updated_tick_id` included, and `calibration_revisions` gains no row the second time

#### Scenario: A small sample
- **WHEN** a lens has 9 resolved outcomes
- **THEN** its score has `calibrated = 0`

#### Scenario: Guarantee drift
- **WHEN** guarantees in scope `d1=2` have 12 outcomes and a hit rate of 10/12
- **THEN** `chb calibrate` reports one `guarantee_floor` drift for `label`/`guarantee` in `d1=2`; at 12/12 it reports none

#### Scenario: A label that changed after the outcome
- **WHEN** a finding recorded as `guarantee` reverts to `unknown` through the cascade before the next recompute
- **THEN** its outcome still counts toward `label`/`guarantee`

#### Scenario: Nothing new
- **WHEN** `chb calibrate` runs with no outcome recorded since the last `calibrate` tick
- **THEN** it says so, opens no tick and changes no score

#### Scenario: A relabel to unknown under a guarantee
- **WHEN** guarantee 7 rests on definition 3, directly or through an assumption, and 3 is relabelled `unknown`
- **THEN** the write is refused with an error naming 7 and `chb db-write cascade_revert 3`, and 3 keeps its label

#### Scenario: The cascade still reverts
- **WHEN** `chb db-write cascade_revert 3` runs on that chain
- **THEN** 7 and every finding resting on it are `unknown` with no dependencies, reverted outward from 3, and 3 may then be relabelled `unknown`

#### Scenario: An assumption with a missing dependency
- **WHEN** an `assumption` is written with `depends_on_ids` naming a finding that does not exist
- **THEN** the write is refused with the error a guarantee gets, and no row is written

#### Scenario: A cycle through assumptions
- **WHEN** assumption 2 rests on assumption 1, and 1 is given `depends_on_ids` naming 2
- **THEN** the update is refused naming the cycle `1 → 2 → 1`, and 1 given a dependency that does not reach it is accepted

### Requirement: Calibration consumers

Every consumer SHALL read a score through `calibration.Scores` and treat an uncalibrated row as absent, so with fewer than `NFloor` outcomes no consumer applies a non-neutral value. The consumers are: the Queen's `{calibration.lenses}` token (comb.md § Template tokens), which weights consensus by each lens's global `lens` score; `calibrated_confidence` on Comb reads (comb.md § Calibrated confidence), from the dominant label's score in the most specific of the region's scopes that has a calibrated one; and the gate's opt-in precondition, `chb guard --require-outcome-review`. Off by default, the precondition SHALL block the gate, as an error `--force` does not waive, for each `guarantee` in the wave whose governing `label`/`guarantee` score — the most specific of its scopes `d1=x;d2=y`, `d1=x`, `''` with a calibrated one — has a hit rate under `GuaranteeConfirmFloor`, unless the guarantee's confirmation state is reviewed: resolved by a `human` or `external` outcome (§ Outcomes and calibration scores). A `downstream_run` outcome alone is no review. The refusal names the guarantee, the scope, the hit rate and n, and `chb outcome-record`. A domain with no calibrated guarantee score asks for nothing. The scores are listed by `chb db-read calibration` and the MCP tool `chb_calibration_read` (mcp.md), each with the correlational note.

#### Scenario: Nine outcomes
- **WHEN** every predictor has at most nine outcomes
- **THEN** the Queen's prompt is unchanged, no Comb read reports `calibrated_confidence`, and `--require-outcome-review` blocks nothing

#### Scenario: An unreviewed guarantee in a weak domain
- **WHEN** guarantees in `d1=2` hold at 10 of 12, wave 2 holds a guarantee at `d1=2` with no outcome, and `chb guard --wave 2 --require-outcome-review` runs
- **THEN** the gate is blocked naming the guarantee and scope `d1=2`; it stays blocked after an adjudication refutes the guarantee, and opens once a human records an outcome on it

#### Scenario: The flag off
- **WHEN** the same wave is gated without `--require-outcome-review`
- **THEN** the precondition runs no check

### Requirement: Cross-project calibration

`chb calibration-export [--out PATH]` SHALL emit this workspace's calibration counts as one JSON object of format `hive-calibration-counts/1` (`calibration.Export`): for every key `(predictor_kind, predictor_key, scope_key)` the ledger folds to, the scopes under the floor included, its confirmed, partial and refuted counts, its Brier sum and count; every scope's outcome total; and the provenance (the database, the time, the outcome count and the highest outcome id). It emits no hit rate, weight or calibrated flag, opens no tick and writes nothing. `chb calibration-merge <files…> [--json]` SHALL sum the files' counts and scope totals key by key, rebuild the pools each reference rate is drawn from (the lens pool is the lens keys of the scope, the synthesis pool the queen's row, the no-∇ pool the queen's row less the ∇ row), decide each scope's eligibility over the summed total and apply the formula (`calibration.Merge`), so the result is what a recompute over the joined ledgers writes, the tick aside: integer counts sum exactly, and a Brier sum, which enters no weight, is a float summed in input order. The merge is printed and stored nowhere: `calibration_scores` stays a function of this workspace's own ledger. It SHALL refuse a file of another format, a count row whose kind is not one of the four, a key a file lists twice, and a scope whose ∇ count exceeds its synthesis count. A scope's `d1`, `d2` mean what each workspace's dimensions mean; merging across workspaces that number their dimensions differently is the operator's call.

#### Scenario: Counts, not weights
- **WHEN** `chb calibration-export` runs
- **THEN** the file holds counts, totals and provenance for every key, including a scope with nine outcomes, and no `weight`, `hit_rate` or `calibrated`

#### Scenario: The union
- **WHEN** workspaces A and B each hold eight outcomes in `d1=1` and their exports are merged
- **THEN** `label`/`guarantee` in `d1=1` is scored over sixteen, as a recompute over both ledgers would score it, and merging B then A gives the same rows

### Requirement: MSS audit

`Store.MSSAudit` SHALL report, over the whole database: label distribution; `untraceable_guarantees` (no dependencies); `laundering_violations` found by walking each guarantee's full dependency chain, so `guarantee → guarantee → unknown` is caught; `dependency_cycles` (DFS); `partition_violations` (`mss.PartitionAudit`); `redundancy_candidates`; and `oa_citation_downgrades`. Integrity is `FAIL` when there is any laundering, untraceable guarantee, cycle or partition violation, and `PASS` otherwise. Redundancy candidates and citation downgrades are warnings only:
- redundancy candidates are same-coordinate assumption pairs with content-word Jaccard ≥ 0.7 (`mss.IndependenceAudit`); the dreamer's `prune` pass adds embedding-cosine ≥ 0.85 pairs once `chb comb embed --findings` has run, changing only the warnings;
- `oa_citation_downgrades` lists guarantees whose cited DOIs are all paywalled or unverified in `citation_oa_cache`, as `chb verify-citations` last cached them. It downgrades nothing and nothing runs it automatically.

Semantic strength of a proof chain is not checked: a guarantee resting on a guarantee is accepted without re-proving the inner one; the dreamer's `reprove` pass flags guarantees whose dependencies changed after them.

#### Scenario: Transitive laundering
- **WHEN** guarantee A depends on guarantee B, which depends on an unknown
- **THEN** the audit reports laundering and integrity is `FAIL`

#### Scenario: Redundancy does not fail integrity
- **WHEN** the only finding is a pair of near-identical assumptions
- **THEN** they appear as a redundancy candidate and integrity is `PASS`

### Requirement: The gate's wave MSS check

`chb guard` SHALL run a wave-scoped MSS check beside the full audit (`gate.RunGatePipeline`). A guarantee in the wave whose `depends_on_ids` name an `unknown` directly is an error. The check SHALL warn on overclaiming: a wave of five or more findings in which `definition` makes up more than 80 %, or `guarantee` makes up more than 80 %. That mix suggests facts labelled as choices, or conclusions with no premises. It SHALL NOT warn on a wave of mostly `assumption` or mostly `unknown` findings. Under these label meanings honest research reads that way. The 5 and the 80 % are choices. A warning keeps the gate shut unless `--force` is passed.

#### Scenario: A wave of sourced facts
- **WHEN** a wave holds nine assumptions and one unknown
- **THEN** the gate raises no skew warning

#### Scenario: A wave of definitions
- **WHEN** a wave holds nine definitions and one assumption
- **THEN** the gate warns that 9 of its 10 findings are definitions

### Requirement: Convergence

`convergence_count` SHALL be 1 on write and set only by `chb swarm-merge` (`gate.MergeFindings`), to the number of effective agents in the finding's near-duplicate cluster. A cluster is the findings in one coordinate cell whose word Jaccard similarity with the cluster's first finding is above 0.7. Its effective agents are its distinct agents, capped by the number of distinct source domains they cite, and at least 1. `convergence_level` SHALL be computed from the same number: `high` at 3 or more, `medium` at 2, `low` otherwise.

#### Scenario: Three agents, one source
- **WHEN** three agents write the same claim at one cell, all citing the same source
- **THEN** swarm-merge stores `convergence_count` 1 and `convergence_level` `low` on each

#### Scenario: Three unrelated claims in one cell
- **WHEN** three agents write three unrelated claims at one cell, citing three different domains
- **THEN** each claim is its own cluster, and each stores `convergence_count` 1 and `convergence_level` `low`

### Requirement: Probes are bounded

`FindingsRepo.Probe` — behind `chb db-read probe` — SHALL filter by any of `d1`–`d8`, `wave`, `mss_label` and `convergence_level`, order by `convergence_count` then `created_at` descending, and return at most 500 rows: 500 when no limit is given, and an error, not a clamp, for a larger limit. A filter a surface cannot read SHALL be refused, not dropped. The `chb db-read` label lists (`assumptions`, `definitions`, `guarantees`, `unknowns`) take no limit: each returns at most 500 rows, and prints a notice on stderr when exactly 500 come back.

#### Scenario: An over-limit probe
- **WHEN** a probe asks for `limit` 1000
- **THEN** it is refused with an error naming the 500 ceiling

### Requirement: Query workload and index set

The indexes on `findings` SHALL be exactly those the recurring reads need. Each SHALL serve its read as an indexed SEARCH (`TestWorkloadReadsAreIndexed`), except Q10, which is an ordered index walk bounded by `LIMIT`. Q5 and Q6 are full scans.

| # | Read | Source | Served by |
|---|---|---|---|
| Q2 | `WHERE wave=?` | the gate, probes, summary | `idx_findings_wave` |
| Q3 | `WHERE id IN (...)` | dreamer `reprove` / `settle`; the gate's partition audit (`json_each`) | primary key |
| Q4 | `WHERE mss_label=?` | the summary, `chb db-read` label lists | `idx_findings_mss` |
| Q5 | group by `(d1..d5)` | `internal/gate/merge.go` | **full scan** — reads every row and groups in Go |
| Q6 | group by coordinate, compare | `internal/gate/conflict.go` | **full scan**, likewise |
| Q7 | `WHERE d1=?` (and deeper prefixes) | coordinate probes | `idx_cde_d1d2d3d4d5`, by leading column |
| Q8 | `WHERE depends_on_ids IS NOT NULL` | MSS audit, cascade revert | `idx_findings_depends_on` (partial) |
| Q9 | `WHERE agent IN (...)` | `chb gaps` | `idx_findings_agent` |
| Q10 | `ORDER BY convergence_count DESC, created_at DESC` | unfiltered probes | **ordered walk** of `idx_findings_convergence_created`, stopped by `LIMIT` |

There is no Q1: no code filters findings by wave and agent together. No index serves a filter on `d6`–`d8`, on `convergence_level`, or on coordinates that do not start at `d1` (a `d2`-only or `d3`-only probe). Such filters are accepted and answered by the Q10 walk, which reads the whole index when fewer than `LIMIT` rows match. Q5 and Q6 scanning is open work.

#### Scenario: A d1-only probe
- **WHEN** the planner is asked for `SELECT * FROM findings WHERE d1 = 0`
- **THEN** it searches `idx_cde_d1d2d3d4d5` rather than scanning

### Requirement: Dimension roster convention

The recommended roster SHALL be documentation only; nothing enforces it, and a project may register any dimensions. Recommended: `d1` domain tier, `d2` subsystem within d1, `d3` severity/confidence (ordered, 0 = critical … 4 = informational), `d4` effort (ordered, 0 = trivial … 4 = architectural), `d5` claim type, `d6`–`d8` reserved. A range query on `d3` or `d4` is meaningful only in a project that adopted the ordering; read its `dimensions` row first. The reserved slots are unregistered rather than nullable: once registered, every later finding must carry them. They carry no index and do not yet earn their place under WASP minimality — a one-time audit of one large workspace (5,525 findings, not reproducible from this repository) found 12 rows using them.

#### Scenario: A range query in a non-roster project
- **WHEN** a project registered its third dimension as an unordered enumeration
- **THEN** `d3 <= 1` selects labels by position, which carries no meaning — the query is the caller's error, not the store's

### Requirement: Scan detector

`db.ClassifyScan(conn, query, table, args...)` SHALL run `EXPLAIN QUERY PLAN` with the read's own arguments and report a full scan when the plan shows `SCAN <table>`. `Store.RecordScanIfFullScan(query, table, args...)` records a scanning shape in `scan_events`, one row per whitespace-normalised shape with a hit count; the instrumented reads are Q5 and Q6. It is best-effort: a classification error records nothing and never affects the read. `chb wasp-scan-report` lists shapes most-hit first. It is operator evidence; nothing feeds it to a forager, and there is no scan signal.

#### Scenario: A bounded read is not recorded
- **WHEN** an instrumented read resolves to an indexed SEARCH
- **THEN** no `scan_events` row is written

#### Scenario: A parameterised read is classified
- **WHEN** a read with `?` placeholders, given its arguments, plans as `SCAN findings`
- **THEN** its shape is recorded in `scan_events`

### Requirement: Citation verification

`chb verify-citations` SHALL extract DOIs from each finding's `source_urls` (a JSON array or a separated list; `10.<4-9 digits>/<rest>` anywhere in the string, where the rest keeps `(`, `)` and `;`; trailing sentence punctuation and a trailing `)` with no matching `(` trimmed; deduplicated in first-seen order), look each up in Unpaywall, and cache the answer in `citation_oa_cache` keyed by DOI: open access, paywalled, or unverified with the error. It is the only writer of `citation_oa_cache`, and makes one request per DOI not already cached. (`chb validate-sources` also calls out, to check source URLs.) Flags: `--stale` (cache TTL, default 90 days), `--offline` (never call out; a cache miss is unverified), `--budget` (wall-clock ceiling, default 2 minutes, 0 disables — DOIs not reached are unverified, not paywalled), `--limit` and `--json`. Once the budget is spent, every remaining finding is still reported, its DOIs from the cache or as unverified. A lookup the budget cuts short is reported unverified and not cached, so a re-run fetches it. Requests carry the contact address `HIVE_UNPAYWALL_EMAIL` names, as Unpaywall requires; HIVE ships no default. With none set, the run reads the cache only, as `--offline` does, and says on stderr which variable to set. Only Unpaywall is implemented; the cache's `source` column and the result type leave room for another.

Nothing runs it automatically, it downgrades no finding and opens no gap: the MSS audit reports `oa_citation_downgrades` from whatever the cache holds, as a warning for a person to act on.

#### Scenario: The budget runs out
- **WHEN** `--budget 10s` expires with DOIs left to check
- **THEN** those DOIs are reported unverified, the run says the budget ran out, and a re-run resumes without re-fetching what is cached

#### Scenario: A DOI with parentheses
- **WHEN** `source_urls` holds `https://doi.org/10.1016/S0140-6736(20)30183-5`
- **THEN** the extracted DOI is `10.1016/s0140-6736(20)30183-5`

### Requirement: Axis-candidate suggester

`cde.SuggestAxes(conn, minEvidence)` SHALL run one `GROUP BY (d1..d5, mss_label)` over `findings` and report, per non-coordinate attribute (`mss_label`), the cells holding at least `minEvidence` findings (default 2) with at least two distinct labels — evidence the attribute is doing an axis's work — and the next free positional slot: `d<registered+1>`, and none once eight are registered. Slots are positional, so that is the only slot a new registration can take. It is read-only and advisory. It surfaces through `chb cde suggest-axis [--min-evidence N]` and the `{cde.axis-candidates}` token injected into the framer's prompt, which renders a "none" line on a cold workspace and never fails dispatch.

#### Scenario: A cold workspace
- **WHEN** the framer's prompt resolves `{cde.axis-candidates}` over an empty findings table
- **THEN** it reads a "none" line and dispatch proceeds

### Requirement: Execution-graph export

`chb export-graph` SHALL emit `{metadata: {generated_at, db_path}, statistics: {total_findings, total_agents, total_waves, total_conflicts, total_evaluations, total_gates, gates_open, mss_counts}, nodes, edges}` for the database, with node types `wave`, `agent`, `finding`, `conflict`, `evaluation`, `gate`, `dimension` and edge types `sequence`, `dispatched`, `produced`, `depends_on`, `conflicts_with`, `gates`, each edge keyed `source` / `target`; `nodes` and `edges` are arrays even when empty. `--compact` cuts finding text longer than 120 bytes to at most 120 bytes, on a UTF-8 boundary, and appends `...`; `--mermaid` emits a Mermaid flowchart instead, whose classDefs colour each node type, a finding by its MSS label's solid colour and a gate by whether it passed; `--out` writes a file. A table that cannot be read, or a wave's finding count that cannot, SHALL fail the export with the read's error, which then writes nothing, to `--out` or to stdout.

#### Scenario: A read that fails
- **WHEN** a table export-graph reads, such as `wave_gates`, cannot be read, or a wave's finding count cannot
- **THEN** it exits non-zero with the read's error, and no file is written at `--out`

#### Scenario: A compacted finding
- **WHEN** `--compact` cuts a finding whose 120th byte falls inside a multi-byte rune
- **THEN** the cut backs off to the rune's start and the text stays valid UTF-8

## Files

- `internal/cli/export_graph.go` — `chb export-graph`, the execution graph as JSON or Mermaid
- `internal/db/schema.go` — tables, indexes, `SchemaVersion`, the busy retry, `SignalsSchema`; `internal/cli/db_repair.go` — `chb db-repair`
- `internal/db/findings.go` — `AddFinding`, `UpdateFinding`
- `internal/db/probe.go` — `Probe`, the probe limits
- `internal/db/dimensions.go`, `sources.go` — dimension and source writes
- `internal/db/mss_audit.go` — `MSSAudit`; `internal/mss/walk.go` — `WalkDeps`, the transitive walk the audit and the write-time check share
- `internal/db/field_shapes.go` — `NormalizeSourceURLs`, `NormalizeDependsOnIDs`
- `internal/db/record.go` — `WriteRecord`, the write path of `chb_db_write` and of `chb db-write`'s `finding`, `gap`, `source`, `resolve_gap` and `resolve_conflict`, and `CheckFieldTypes`, the field-type check every write surface runs; `internal/cli/db_write.go` — `decodeDBWritePayload`, which runs it for `chb db-write`'s other kinds; `internal/cli/ingest.go` — `ingestFinding`, `ingestGap`, `ingestFollowup`, which run it on each marker
- `internal/db/scan_detector.go` — `ClassifyScan`, `RecordScanIfFullScan`, `ScanEvents`
- `internal/db/outcomes.go`, `calibration.go` — the outcomes ledger, the scores and their revisions
- `internal/calibration/` — `params.go` (the constants), `formula.go`, `record.go` (`Record`, CALIB-1 and CALIB-2), `input.go` (`Input`, the shape every surface reads), `adjudicate.go` (`Adjudicate`), `state.go` (`ConfirmationState`), `recompute.go` (`Recompute`), `consume.go` (`Scores`, `ScopesFor`, `LabelScore`, `CalibratedConfidence`, `LensTrackRecord`, `ScoreView`), `export.go` (`Export`, `Merge`)
- `internal/cli/calibrate.go` — `chb calibrate`, `outcome-record`, `outcome-import`, `db-read calibration`; `internal/cli/calibration_merge.go` — `calibration-export`, `calibration-merge`; `internal/mcp/tools_calibration.go` — the MCP surface
- `internal/mss/` — label rules, `ValidateGuaranteeDeps`, `PartitionAudit`, `IndependenceAudit`
- `internal/cde/coords.go`, `axis_suggester.go` — `ValidateCoords`, `SuggestAxes`
- `internal/gate/merge.go`, `conflict.go` — convergence and conflict passes
- `internal/gate/gate.go` — the gate's wave MSS check (`pipeAuditMSS`) and the outcome-review precondition (`pipeOutcomeReview`)
- `internal/citations/` — `ExtractDOIs`, the Unpaywall verifier; `internal/db/citation_oa.go` — the cache
- `internal/cli/db.go`, `wasp_scan_report.go`, `cde_suggest_axis.go`, `verify_citations.go` — CLI
