# Naming policy

Four names live in this project. They are not interchangeable; each owns
exactly one layer. When you write code, docs, or config, pick the name
for the layer you're touching.

| Layer | Name | What it covers | Mutable? |
|---|---|---|---|
| **Command** | `chb` | What a user *types*. The CLI binary, every CLI invocation in docs, the MCP server binary `chb-mcp`, HIVE's own tool names (`chb_*`) and the local image tag (`chb:local`). | Yes — this is the public command surface. |
| **Brand** | **HIVE** — the **H**ive-**I**nspired **V**irtual **E**ntity | Prose and titles. The name of the *system*: the foragers, the comb, the guard and autonomous mode. Never an invocation. `chb` is HIVE made typeable. | Prose; never code. |
| **Runtime / storage** | `hive` | The `HIVE_*` env vars, `hive.db`, and the `~/.config/hive/` override directory (`models.yaml`; `$XDG_CONFIG_HOME/hive/` when that is set). | No: renaming one breaks the environment, the databases and the model overrides of every workspace. |
| **Repo / module** | `Chubby-Honey-Bee/hive` | The GitHub slug, the Go module path `github.com/Chubby-Honey-Bee/hive`, clone URLs, badges, and the `ghcr.io/chubby-honey-bee/hive` image. | No: changing it is a breaking change for `go get`. |

## Rules

1. **Anything a user types is `chb`.** CLI examples in docs are `chb ask`,
   `chb comb refresh`, `chb validate`, and so is every command a help
   string, an error message or a Go comment names, since users and agents
   read those too. Tool names that agents call follow the same rule.
   HIVE's own tools are `chb_*`: every `chb-mcp` tool, and `chb_db_write`
   in the in-process registry the SDK backends use. That registry's
   generic tools (`read_file`, `shell` and the rest) keep their plain names.
2. **"HIVE" is prose, not a command.** Write "HIVE believes…",
   "the hive's verdict" — but to *run* it you type `chb`. Never rewrite
   brand prose into `chb` (e.g. "the hive believes" stays; only
   code-formatted invocations change). The expansion is written
   **Hive-Inspired Virtual Entity** — hyphenated, "Entity", never
   "Engine". This table row is the canonical source for that string.
   Outside the runtime names of rule 3, lowercase `hive` in code names
   *autonomous mode*: `chb hive`, `workflows/hive.yaml`, `internal/hive`
   and the `hive_state` table. In prose, call that loop "autonomous mode"
   and keep HIVE for the whole system; never write HIVE for the loop. A
   *swarm* is one run's foragers, what one `chb ask` sends out. The
   *colony* is the system that outlives the run.
3. **The runtime layer is `hive`.** HIVE is the product, and its runtime
   carries its name: the `HIVE_*` variables the binaries read, the
   `hive.db` database they keep, and the `~/.config/hive/` directory that
   holds a user's model overrides. `HIVE_` is that name in an environment
   variable's upper case, not the brand in code. These names are
   load-bearing and invisible to a user who only types `chb`; renaming one
   breaks every workspace on disk, and every user's model overrides, for no
   user-facing gain. The prefix is fixed, not the words after it, which
   name what the variable holds in the current vocabulary
   (`HIVE_FORAGERS_DIR`). `CHB_*` is a second prefix with a narrow job:
   `CHB_BIN`, `CHB_IMAGE`, `CHB_WORKSPACE` and `CHB_DOCKER` configure the
   Makefile and the shell scripts, and `CHB_TEST_FAKE_STAGES` is one test
   hook. `~/.chb/workspace` is only the docker wrapper's default
   workspace. The `chb` and `chb-mcp` binaries read `HIVE_*` and no
   `CHB_*` variable.
4. **The module path tracks the repo.** Go resolves a module by its path,
   so `github.com/<org>/<repo>` must match where the code actually lives
   or `go get` and `go install` fail. So the module path is
   `github.com/Chubby-Honey-Bee/hive`, the repository's address. The path
   does not name the `chb` binary — see rule 5.
5. **The mains are `cmd/chb/` and `cmd/chb-mcp/`; the code behind them
   is `internal/cli/` and `internal/mcp/`.** Go names a binary after its
   main's directory, so
   `go install github.com/Chubby-Honey-Bee/hive/cmd/chb@latest` yields
   `chb`, and the builds this repo ships (`.goreleaser.yaml`, `Makefile`,
   `Dockerfile`, CI) build those two. Each main is thin: `cmd/chb` calls
   `cli.Main`, the CLI in `internal/cli` (package `cli`), and
   `cmd/chb-mcp` calls `mcp.Main`, the MCP server in `internal/mcp`
   (package `mcp`). Under `internal/`, neither is importable from outside
   the module. The binaries carry the shipped `foragers/`, `agents/` and
   `workflows/` (`assets.go`), so an installed `chb` runs from any
   directory; a folder on disk, or `HIVE_FORAGERS_DIR`, is read before
   the carried copy.
6. **The local image tag is `chb:local`.** The published image belongs to
   the repo layer: `ghcr.io/chubby-honey-bee/hive` (registry paths are
   lowercase even though the GitHub org is not).
7. **Schema vocabulary changes only with a schema bump.** A CHECK-enum
   value — in `signals.signal_type`, `time_wheel.kind`,
   `comb_state.vantage_kind`, `forager_bonds.bond_kind` and the rest — is
   written into each database's schema when the database is created, and
   the schema is never migrated. A database created before a rename keeps
   refusing the new value. So rename one only with a `SchemaVersion` bump
   (`internal/db/schema.go`). The bump makes an older chb refuse the newer
   database. It does not repair an older database: chb stamps that file
   current and leaves its existing tables as they were. A database created
   before the bump must be deleted and created again with `chb db-init`.
   Persona frontmatter keys (`sigil`, `accent`, `bonds` and the rest) are
   read from forager files that users write; rename one only with a new
   persona template version. This is why
   `tremble_dance`, `chronomantic_drift`, the `swarm` tick kind and the
   `sigil` key keep their names.

## Why the seams are acceptable

Three reasonable questions, one answer each.

*Why is the system HIVE but the command `chb`?*
Because they answer different questions. HIVE names the *idea* — what the
system is and how it reasons — and reads well in prose. `chb` is what your
fingers do: three characters, from the owning organisation
(**Ch**ubby **H**oney **B**ee), short enough to type all day. A system name
that is pleasant to write about is rarely the one you want to type, and
collapsing them would cost one of the two.

*Why is the binary `chb` but the DB `hive.db`?*
Because the database belongs to the product and the binary to the
command. HIVE is the product, and its runtime carries its name: `hive.db`,
the `HIVE_*` variables and `~/.config/hive/`, where users keep their model
overrides. `chb` is what people type, and the question above says why it
is short. The runtime names are a contract with *machines*, every
workspace's database and environment, while `chb` is the contract with
*people*.

*Why is the module path `Chubby-Honey-Bee/hive` when the command is `chb`?*
Because a module path is not a name, it is an address. Rule 4 covers it:
an address that points at the wrong host is not a naming preference, it
is a broken install.

## Where the bee names come from

Each bee name resolves to a code contract, not decoration. Where the code
departs from the biology, the entry says so.

- **Colony and swarm.** HIVE is the colony, a superorganism: it outlives each run, and its comb holds what it has learned. A swarm is one run's foragers. A real swarm is a whole colony on the move, so this is a loose fit, kept because `chb_swarm`, `chb swarm-merge` and the `swarm` tick already use it.
- **Scouts** (bees that search out new sites before others are recruited to them) → the `hive-scout` agent that autonomous mode sends to open gaps and waggle-dance targets, and the micro-agents of the `scouts` workflow.
- **Guard bees** (they check each bee at the hive entrance and turn robbers away) → `chb guard`, which checks a wave — agents, conflicts, sources, the MSS audit — before it opens the wave's gate. [`docs/foundations.md`](foundations.md#the-gate) calls that entrance the gate.
- **Waggle dance** (a returning forager's report of a rich patch, which recruits others to it) → the `waggle_dance` signal: autonomous mode recruits scouts to open gaps beside a high-convergence patch.
- **Bonds** (`cites` / `contradicts` / `resonates`) are wiring declared in frontmatter before a run, not a dance. Their closest bee readings: `cites` is following a dancer, since the dependent reads the upstream forager's digest before it flies; `contradicts` is the head-butt of a stop signal, the same edge under a preamble that argues against the upstream find; `resonates` is two scouts dancing for the same site, with no edge, only a pair the ∇ sensor watches.
- **Quorum sensing** (a swarm choosing its nest: scouts commit once enough of them are at one site together) → the `quorum` signal. An assumption whose `convergence_count` has reached `convergence_threshold`, with high convergence and no open conflict, fires `quorum`. `chb hive next --apply` then caps it, and it stays an assumption.
- **∇** is not a bee name. It is the framework's convergence operator. The ∇ convergence sensor (`comb.QuorumSensor` in code) fires at most once per pair per run, when two `resonates`-bonded foragers reach the same verdict: it writes a `nabla` signal and a `forager_bonds` row. It promotes nothing, and neither does the `quorum` signal: convergence never makes a guarantee.
- **Ripening** (nectar is fanned, cured into honey, then capped) → `chb ripen`, the Dreamer's five passes. `prune` fans off the excess: it flags near-duplicate assumptions. `reprove` and `contradict` cure: they re-test what stands. `hypothesize` turns old gaps into questions for the next foragers, which is foraging, not a ripening stage. `settle` is hygienic behaviour, clearing out a cell that went bad: it flags, and with `--apply` demotes, a guarantee whose foundation went bad. It lifts no cap. No pass seals a claim, and none launders one.
- **Capping** (sealing a cell of ripe honey) → `chb hive next --apply` caps the finding the `quorum` signal names, once. The capped cell is sealed from recruitment: quorum never acts on it again, and no waggle dance sends a scout to its coordinate. A cap is not a guarantee. The finding stays an assumption: agreement among agents is not a derivation, and a guarantee still needs premises named in its `depends_on_ids`. Where HIVE departs from the biology: the seal stops short of gap fill. A critical or important gap open at a capped coordinate is still dispatched, because only a finding closes a gap and termination waits for it. The Comb shows capped cells: `chb comb status` counts them. The hive loop's `capped` phase is its iteration cap, not a capped cell.
- **Comb building** (bees build comb cell by cell, and rework cells when they must) → the comb. `comb_state` holds each vantage's current belief. `comb_revisions` holds every revision, the current one included: each write appends one. A rework adds a revision and erases none, so the history is append-only.
- **Queen mandibular pheromone** (a healthy queen emits it all the time, and the colony reacts when it stops) → the `qmp` signal, the MSS-integrity halt nothing overrides. HIVE borrows its authority, not its timing: `qmp` fires only when the integrity audit fails.
- **The Queen** → the synthesizer node and `foragers/queen.md`. The name borrows her centrality — every forager's return reaches her, except the Dreamer's, which ripens the comb after her — and the cohesion she gives the colony, not decision authority: a real queen decides nothing, and a swarm's choice is the scouts' quorum. The Queen writes the foragers' findings up as one verdict. She is not a forager.
- **The other signals** — `stop_signal`, `alarm`, `tremble_dance`, `shaking_signal` — carry bee names too. Their meanings, and where HIVE's use departs from the biology, are in the signal vocabulary of [`docs/specs/hive.md`](specs/hive.md).
- **The Time Wheel** is framework vocabulary, not a bee name. Its ticks read loosely as foraging trips. Three producers write them: every workflow run, `chb agent-run` included (a `swarm` tick, whatever the workflow), `chb ripen` (a `ripen` tick), and opening a wave's gate with `chb guard` or `chb db-write gate_wave` (a `wave` tick).
