# Workflow Engine Specification

## Purpose

Graph-based YAML workflow execution. A workflow is a graph of agent, decision, fan-out, command and human-review nodes; edges order them, decision nodes branch and may loop back. Node outputs are gated by author-written predicates with optional bounded repair, and every decision, rationale, token count and cost is persisted for audit.

## Requirements

### Requirement: Workflow definition

A workflow YAML SHALL declare `nodes` (a map, so names are unique), `edges` (`from` / `to`, optional `condition`) and optional `inputs`. Per-node fields:

| Field | Type | Notes |
|---|---|---|
| `type` | string | `agent` \| `decision` \| `parallel_fan` \| `command` \| `human_review` \| `calibrate` |
| `agent` | string | persona loaded from `agents/<name>.md`. Optional: with none, or a persona file that does not exist, the node runs with an empty system prompt and the runner logs that it did |
| `model` / `tier` | string | a model alias or id, or a cost-tier role resolved through the budget mode (runner.md) |
| `provider` | string | per-node provider override (`anthropic` \| `gemini` \| `openai` \| `local` \| `claude-cli` \| `gemini-cli`) |
| `reasoning` | string | `none` \| `low` \| `medium` \| `high`, exactly. Sent as `reasoning_effort` on the OpenAI-compatible backend only (runner.md § Reasoning level). Any other value is refused by `Validate` and by every way of starting a run |
| `role` | string | on `agent` and `parallel_fan` nodes only: the role a routing profile routes the node by (runner.md § Routing profiles), one of `models.Roles` and not a repair role. Without a profile it changes nothing |
| `ttl` | string | on `agent` and `parallel_fan` nodes only: the bound on each of the node's calls, dispatch, fan item and repair, as a positive Go duration such as `5m` (runner.md § Backends). Absent, the runner's default |
| `prompt` | string | template with `{variable}` placeholders resolved from state, in one pass: text a value brings in is not read again, so a placeholder inside a value (a question, a context pack, a model's output) stays as written, whatever order state's keys come in; required on `agent` nodes, and on `parallel_fan` unless `prompt_template` is given. A list or a map is written as JSON without HTML escaping, so its items keep their boundaries; a number is written as JSON writes it, so 1234567 is never `1.234567e+06`; any other value is written with `%v` |
| `tools` | list of strings | the node's tool allowlist, on `agent` and `parallel_fan` nodes only. Each name is one of the runner's registry tools: `read_file`, `write_file`, `edit_file`, `glob`, `grep`, `shell`, `web_fetch`, `chb_db_write` (`workflow.ToolNames`), or `bash`, another name for `shell` (`workflow.ToolAliases`). Absent, the node keeps every tool; `[]` gives it none (runner.md § Tool allowlist) |
| `min_tool_calls` | int | on an `agent` node that has a tool: a call that made fewer tool calls is sent back to the node's `on_reject:` repair with the count, however well-formed its answer, and fails the node when it has none (runner.md § Minimum tool calls). A positive integer |
| `fan_limit`, `fan_overflow` | int, string | on a `parallel_fan` only, and both or neither: the fan dispatches its first `fan_limit` items, and the engine writes the items past the limit, as a list, to state under `fan_overflow`, when a node sets the fan's source and again when the fan completes (runner.md § parallel_fan) |
| `join` | string | `settled`, the one value: the node is ready once every predecessor has finished — completed, skipped, failed or rejected — and at least one completed (§ Edge conditions and readiness). Absent, a failed or rejected predecessor keeps the node pending |
| `outputs` | list of strings | the node's declared output keys. On a model node they do not filter: every key the node returns is merged into state on completion, declared or not. When the node returns only `final_text`, that text is also stored under the first declared key, unless the node has an `output_schema` (§ Output schema). When the runner executes a `command` node they do filter: only the declared keys reach state, and each must be present (runner.md § Command nodes). A caller completing a command node through `chb workflow complete` merges what it passes, as for any node |
| `output_schema` | object | on `agent` and `parallel_fan` nodes only: the JSON Schema its reply must hold (§ Output schema). The OpenAI-compatible backend sends it for the server to constrain decoding with (runner.md § Output schema on the wire) |
| `argv` | list of strings | a `command` node's program and arguments, required. Each element is templated as `prompt` renders values, in one pass, and stays one argument; no shell reads the list (runner.md § Command nodes) |
| `stdin` | string | a `command` node's standard input, templated as `argv` is |
| `outputs_from` | string | `stdout_json` on a `command` node that reads its outputs from stdout. Without it the node has no outputs and may declare none |
| `ok_exit` | list of ints | a `command` node's exit codes that complete it, each 0 to 255. Absent, 0 alone (runner.md § Command nodes) |
| `rebuild` | bool | a `calibrate` node's: recompute even when no outcome was recorded since the last `calibrate` tick (§ The calibrate node) |
| `scope` | string | a `calibrate` node's: the scope its outputs count, `""` the global one; absent, every scope (§ The calibrate node) |
| `accept` | list | boolean predicates evaluated after the agent returns. Each item is a predicate string, or `{predicate, reason}`, whose reason a rejection by it gives in plain words (§ Accept predicates) |
| `on_reject` | object | repair block: `{model \| tier, role, prompt_template, max_repair_iterations}`. The repair runs with no agent persona, so an `agent:` key here is read by nothing. `role` is a repair role (`models.RepairRoles`), which a routing profile routes the repair's model by |
| `max_retries` | int | default 0 |
| `condition`, `true_edge`, `false_edge` | string | required on `decision` nodes |
| `state_updates` | map | literals or `{var}`-templated strings merged into state when the node completes, on every node type: a decision when it takes a branch, a `human_review` node on approve or redirect (not on reject) |

A `command` node SHALL accept only `type`, `argv`, `stdin`, `outputs_from`, `outputs`, `accept`, `state_updates`, `max_retries` and `ok_exit`. Any other key, such as a `prompt`, a `model` or an `on_reject` block, would be read by nothing, so validation refuses it, and so does every way of starting a run (§ Graph validation). A `calibrate` node SHALL accept only `type`, `rebuild`, `scope`, `outputs`, `accept` and `state_updates`: `rebuild` a boolean, `scope` a string, and `outputs` naming only `calibration_drift_count` and `lowest_calibrated_lens`, the two it writes (`workflow.CalibrateOutputs`). Any other key is refused the same way.

A command node's `argv` and `stdin` SHALL be templated in one pass: each `{name}`, where `name` starts with a letter or `_` and holds letters, digits, `_`, `.` and `-`, is replaced by the rendering of state's `name`, and a value's own braces are never read as placeholders. Any other brace, such as a JSON object's `{"`, is left as written. A placeholder no state value fills is left as written and named in the dispatch node's `command_error`, and so is an `argv` element that is not a string. The runner refuses to run such a node (runner.md § Command nodes): run, the program would get a literal `{name}` or an empty argument instead of failing.

#### Scenario: A placeholder inside a value
- **WHEN** a command node's argv holds `{text}`, and state's `text` is `a {run} value`
- **THEN** the argument is `a {run} value`, and `{run}` is not named as missing whether or not state has `run`

#### Scenario: `gate` is no node type
- **WHEN** a workflow declares a node of type `gate`
- **THEN** validation fails with `invalid type "gate"`: a wave gate runs as a command node (`chb guard`)

#### Scenario: A required input is missing
- **WHEN** a run is started without a variable the workflow's `inputs` declares, by `InitWorkflow` (`chb workflow init`, `chb agent-run`)
- **THEN** it fails with `missing required input: <name>` and creates no run

#### Scenario: An unknown reasoning level
- **WHEN** a node sets `reasoning: extreme`, or `reasoning: true`
- **THEN** `Validate` reports `node "<name>": reasoning "<value>" is not one of none|low|medium|high`, and `InitWorkflow` refuses to start the run with that error

### Requirement: Workflow file

The binary carries the shipped `workflows/` (`hive.Workflows`, equal to the repository's `workflows/` file for file). `ResolveFile` SHALL turn the workflow a command names into a file on disk in this order, a definition: the path as given, when it exists; else `workflows/<path>` under the working directory; else, when the path is a plain file name or `workflows/<name>`, `workflows/<name>` beside the binary; else the carried `workflows/<name>`, written to a temporary file the command removes when it is done. A path into any other directory, and a name no copy holds, are not found, and the error names what was given. `chb agent-run`, `chb preflight`, `chb workflow validate` and `chb workflow init` resolve their argument this way, and say on stderr when they run or check the carried copy; `chb_research` resolves its plain name under `workflows/` the same way. The runner reads the resolved file where it reads any workflow, so a run from the carried copy logs the temporary path.

#### Scenario: A workflow from the carried copy
- **WHEN** `chb agent-run workflows/hive.yaml --dry-run` runs in a directory with no `workflows/` and none beside the binary
- **THEN** it runs the carried `hive.yaml`, byte for byte, and stderr says so

#### Scenario: A folder on disk overrides
- **WHEN** `workflows/hive.yaml` exists under the working directory
- **THEN** `chb preflight workflows/hive.yaml` and `chb preflight hive.yaml` check that file and not the carried copy

### Requirement: Output schema

A node's `output_schema:` SHALL be a JSON Schema subset: `type` (`object`, `array`, `string`, `integer`, `number`, `boolean`, `null`, or a list of them), `enum`, `const`, `required`, `properties`, `additionalProperties` (`true` or `false` only), `items` (one schema), `minItems`, `maxItems`, `minLength`, `maxLength`, `minimum` and `maximum`, and the annotations `title` and `description`. Any other keyword SHALL be refused, so no constraint an author writes goes unchecked. `Validate` and `InitWorkflow` SHALL refuse a workflow, naming the node and the keyword's path, whose schema uses a keyword outside the subset, is not `type: object` at its root, sits on a node that is not `agent` or `parallel_fan`, leaves out a key the node declares in `outputs:`, lists in `required` a name it does not declare, or sets bounds that contradict each other or its type. Each check runs whatever the schema declares, a schema with no `properties` included, at every level. Merge keys (`<<: *anchor`) resolve as they do in the decoded workflow: a key a mapping sets itself wins, then the first merged mapping to set it. So a schema a node gets through a merge key is parsed, dispatched and checked like one written inline. The schema keeps its properties in the order the YAML lists them, on the dispatch node (`DispatchNode.OutputSchema`) and wherever it is serialized; a Go map would sort them. A merged mapping's properties take the merge key's place. That order is a choice the author makes, and it reaches decoding in part (runner.md § Output schema on the wire). A llama.cpp grammar writes the required properties first, in declared order, then the optional ones. So to have a model write its reasons before its decision, list them first and require them. A server that re-encodes the schema through a map, as Ollama's native-chat path does, sorts them instead; the constraint probe says which a server does.

`CompleteNode` SHALL check an `agent` node's outputs against its schema before it merges them or evaluates `accept:`. Outputs holding only `final_text`, the runner's fallback for a reply with no JSON object, are one violation, `the reply holds no JSON object`, unless the schema declares `final_text`. So prose is never stored under a declared output. A violation is an `AcceptRejection` with predicate `output_schema`, whose value names each violation by its path from `$` (at most five, then a count), and so goes to the node's `on_reject:` repair or rejects the node (§ Accept predicates). The same check runs on every completion path: the runner and `chb workflow complete`. `CompleteNode` checks a `parallel_fan`'s outputs as one reply too: a caller outside the runner checked no item, so the outputs it sends must hold the schema. The runner checks each item's reply itself and completes the fan with `CompleteFanItems`, which does not check the joined outputs again, since they are not one reply (runner.md § parallel_fan). `maxLength` and `minLength` count characters. `enum` and `const` compare as JSON values.

#### Scenario: A keyword outside the subset
- **WHEN** a node's schema gives a property `pattern: x`
- **THEN** `Validate` reports it naming the node and `properties.<name>.pattern`, and `InitWorkflow` creates no run

#### Scenario: Prose on a schema'd node
- **WHEN** a schema'd agent node completes with only `final_text` "bash: chb: command not found"
- **THEN** it is rejected with `output_schema = the reply holds no JSON object`, and state gains nothing, not even its first declared output

#### Scenario: A verdict outside its enum
- **WHEN** a node whose schema allows `verdict` `support` or `oppose` returns `maybe`
- **THEN** the rejection names `$.verdict`, and the node's `accept:` predicates are not evaluated

#### Scenario: A schema with no properties
- **WHEN** a node declares `outputs: [verdict]` and `output_schema: {type: object}`, or a schema lists `required: [zzz]` and no properties
- **THEN** `Validate` and `InitWorkflow` refuse it, naming `verdict` or `zzz`

#### Scenario: A schema through a merge key
- **WHEN** a node takes its definition from `<<: *lens`, and the anchored mapping holds `output_schema`
- **THEN** the node is dispatched with that schema and its prose reply is rejected; had the schema used `pattern`, `Validate` would refuse it

#### Scenario: A fan completed from outside the runner
- **WHEN** `chb workflow complete` completes a schema'd `parallel_fan` with only `final_text`
- **THEN** it is rejected with `output_schema`, as an agent node's prose is

### Requirement: Graph validation

`Validate` SHALL refuse a workflow with no start node (a node with no incoming edge), a decision node missing `condition`, `true_edge` or `false_edge`, a decision whose `true_edge` or `false_edge` names no node, an agent node without `prompt`, a cycle with no decision node on it, a cycle made only of decision nodes, and a loop closed by anything but a decision. A loop's closing edge is its back edge: in a depth-first walk from the start nodes, in sorted order, an edge into a node still on the walk's stack. Its source must be a decision; that branch is the engine's retry loop. A cycle made only of decisions, a decision whose branch is itself among them, runs no node, so no output can change the branches it takes; the message names the cycle's nodes. A run that never went through `Validate` (`InitWorkflow` and `chb agent-run` do not run these graph checks) meets the engine's own bound instead (§ Decision loops). `Validate` reports each node's type and required fields in node-name order, so one definition's report reads the same on every run.

`Validate` SHALL also refuse the node fields `CheckNodeFields` rejects: a `tools:` that is not a list, names a tool outside `workflow.ToolNames`, or sits on a node that is not `agent` or `parallel_fan`; a `min_tool_calls:` that is not a positive integer, sits on a node that is not `agent`, or sits beside `tools: []`, which offers no tool to call; a `join:` other than `settled`; a `role:` that is not a role, is a repair role, or sits on a node that is not `agent` or `parallel_fan`, and an `on_reject:` block's `role:` that is not a repair role; a `ttl:` that is not a positive Go duration, or sits on a node that is not `agent` or `parallel_fan`; and a `fan_limit:` that is not a positive integer, lacks `fan_overflow:`, or sits on a node that is not a `parallel_fan`, or a `fan_overflow:` without `fan_limit:`. `InitWorkflow` runs the same check and starts no run on such a workflow. A runner test holds `workflow.ToolNames` equal to the registry's tool names.

`CheckNodeFields` SHALL also refuse a command node that has no non-empty `argv` of non-empty strings, carries a key outside the list in § Workflow definition, names an `outputs_from` other than `stdout_json`, has a `stdin` that is not a string, declares `outputs` without `outputs_from`, or has an `ok_exit` that is not a non-empty list of whole numbers from 0 to 255; and a calibrate node that carries a key outside its list, a `rebuild` that is not a boolean, a `scope` that is not a string, or an output it does not write. So `InitWorkflow` refuses them too, and a run started with `chb agent-run`, which does not call `Validate`, never runs one.

#### Scenario: A misspelled branch is refused
- **WHEN** a decision's `true_edge` is `finsh` and no node has that name
- **THEN** validation fails naming the decision and `finsh`. The graph drops a branch with no target, so the node the author meant would run as a start node

#### Scenario: A command node with a prompt is refused
- **WHEN** a `command` node carries `prompt:` or `model:`
- **THEN** validation fails with `Command node "<name>": unknown key "<key>"`, so the node can never run as though configured

#### Scenario: A command node without argv is refused
- **WHEN** a `command` node has no `argv`, or an empty one
- **THEN** validation fails with `Command node "<name>": missing 'argv'`

#### Scenario: A number in argv is refused
- **WHEN** a `command` node's argv is `[chb, guard, --wave, 1]`, whose last element YAML reads as a number
- **THEN** validation fails with `argv[3] is not a non-empty string`, and `chb agent-run` starts no run

#### Scenario: A calibrate node with a prompt is refused
- **WHEN** a `calibrate` node carries `prompt:` or `model:`, or `outputs: [verdict]`
- **THEN** validation fails with `Calibrate node "<name>": unknown key "<key>"`, or names the output it does not write, and `InitWorkflow` starts no run

#### Scenario: A retry loop validates
- **WHEN** a decision's `false_edge` leads back to an upstream node reachable from a start node
- **THEN** the workflow validates

#### Scenario: A loop no decision can exit is refused
- **WHEN** a cycle contains no decision node
- **THEN** validation fails naming the cycle's nodes

#### Scenario: A loop of decisions alone is refused
- **WHEN** a decision's `true_edge` names the decision itself, or two decisions' branches name each other
- **THEN** validation fails with `Cycle made only of decision nodes`, naming `d → d` or `d1 → d2 → d1`

#### Scenario: An unknown tool is refused
- **WHEN** an agent node declares `tools: [read_file, shell]`
- **THEN** `chb workflow validate` names `shell` and the known tools, and `chb agent-run` starts no run

#### Scenario: A misspelled role is refused
- **WHEN** an agent node declares `role: lense`, or `ttl: soon`
- **THEN** `chb workflow validate` names the node and the value, and `chb agent-run` starts no run; a node's `role: implement-repair` is refused too, since that role goes on its `on_reject:` block

#### Scenario: A loop closed by an agent is refused
- **WHEN** `research → check → retry → check`, where only `check` is a decision
- **THEN** validation fails naming the edge from `retry`, which nothing would re-run

### Requirement: The calibrate node

A `calibrate` node SHALL run `calibration.Recompute` (cde-mss.md § Calibration recompute) in the run: no prompt, no model, no tokens. The runner SHALL dispatch it explicitly (`executeCalibrateNode`), never through the default that auto-completes a node with empty outputs, and its event names its `rebuild` and `scope` and no provider, agent or model. `rebuild` is the recompute's; the lens names finding outcomes are credited to come from the forager tree under the project's `foragers/`, else the working directory's, else the copy the binary carries. It writes `calibration_drift_count`, the recompute's drift reports counted in `scope` when one is set (`""` the global scope) and in every scope when none is, and `lowest_calibrated_lens`, the calibrated lens with the smallest weight in that scope (the global scope when none is set), `""` when no lens is calibrated there. On a run with no outcome recorded since the last `calibrate` tick and no `rebuild`, no tick opens and the count is the standing guarantee-floor drift of the current scores; a hit-rate drop is reported once, by the recompute that saw it. It completes through `CompleteNode`, so `accept:` and `state_updates` apply, and a rejected predicate marks it rejected, terminally, as a command node is: the same ledger gives the same scores. Its rationale is the recompute's summary as JSON: the tick, what was new, the rows changed, the drift, the ∇ report and the outputs. Under `--dry-run` it completes with `{"dry_run": true}` and opens no tick. The manual path hands it out as any node, with `rebuild`, `scope` and `scope_set`, and no model or prompt. `chb preflight` lists every calibrate node with its rebuild and scope. `workflows/research-calibrated.yaml` is the shipped example: a calibrate node, a decision on its drift count, a report on drift and one researcher otherwise.

#### Scenario: The node runs
- **WHEN** a workflow with a `calibrate` node runs over a ledger whose guarantees in `d1=2` held at 10 of 12
- **THEN** the node completes with `calibration_drift_count` 2 (the `d1=2` and global rows) and `lowest_calibrated_lens` `""`, one `calibrate` tick is recorded, and the node spent no tokens

#### Scenario: Nothing new
- **WHEN** the same node runs again with no outcome recorded since
- **THEN** no tick opens and `calibration_drift_count` is still 2

#### Scenario: A scope
- **WHEN** the node names `scope: d1=9`
- **THEN** `calibration_drift_count` is 0, and with `scope: d1=2` it is 1

### Requirement: Fan wiring check

`chb preflight` SHALL fail a workflow with a `parallel_fan` that would not fan out as written (`workflow.FanSourceProblems`). Its `fan_source` must be a workflow input, or a key that a node upstream of the fan declares in `outputs` or `state_updates`. Upstream means over forward edges. A node that reaches the fan only through a loop's back edge does not count, because it runs after the fan's first pass. The prompt the fan dispatches (`prompt`, else `prompt_template`) must hold its `fan_placeholder`, `{item}` by default. A fan with no `fan_source` is not checked. When the workflow has no nodes, or its graph does not build, preflight skips the check with a warning rather than passing it. `Validate` does not run this check, and the engine still runs a fan whose source is missing as one ordinary call (runner.md). Every shipped workflow passes it. In every shipped workflow, each node that returns a fan's `fan_source` among several outputs also refuses, in `accept:`, a reply where that key is missing or null, and its `on_reject:` allows one repair. A node with one output needs no such check, because a prose reply is stored under that key.

#### Scenario: An evaluator drops its gaps
- **WHEN** the `evaluate` node of `research-deep.yaml` or `research-scouts.yaml` replies without `gaps`, or with `gaps` null
- **THEN** `accept:` refuses the reply and `on_reject:` asks for it once more, so the gap-filling fan never runs one call on the literal `{item}`

#### Scenario: A fan_source nothing upstream declares
- **WHEN** a fan reads `fan_source: wave1_tasks` and no input or upstream node declares `wave1_tasks`
- **THEN** preflight fails naming the fan and `wave1_tasks`

#### Scenario: A fan prompt without its placeholder
- **WHEN** a fan's `fan_source` is declared upstream but its prompt has no `{item}`
- **THEN** preflight fails, since every item would get the same prompt

#### Scenario: A graph that does not build
- **WHEN** an edge names a node that does not exist
- **THEN** preflight warns that it skipped the fan check, and the `yaml validates` check fails

### Requirement: Expression evaluation

`SafeEval` SHALL evaluate conditions and predicates without I/O, by parsing with Go's `go/parser` after normalising word operators. It supports `==`, `!=`, `<`, `<=`, `>`, `>=`, `&&`/`and`, `||`/`or`, `!`/`not`, dotted member access (`outputs.count`), the one builtin `len(x)`, string and numeric literals, and identifiers from state. `==` and `!=` compare `fmt.Sprintf("%v")` renderings; the ordering operators require numeric operands and return an error otherwise. There is no `in`.

#### Scenario: A non-numeric ordering comparison is an error
- **WHEN** a condition evaluates `score > 5` with `score` = `"high"`
- **THEN** evaluation returns an error rather than treating `"high"` as 0

### Requirement: State and completion

`InitWorkflow` SHALL seed a run's node rows in node-name order, the order `chb workflow status` lists them in, so one definition's nodes are listed the same way in every run. Each run SHALL keep one state object in `workflow_runs.state_json`. Completing a node SHALL, in one write transaction (`workflow.Store.FinishNodeInTx`, which re-reads state under the write lock), merge the node's outputs into state, apply its `state_updates`, evaluate its `accept:` predicates, and write the node row — so siblings completing at once never overwrite each other. A decision with `state_updates` and a `human_review` node answered by resume SHALL finish through the same transaction, which also takes `failed` for a reject. Parallel siblings writing the same output key keep only the last to commit. Node statuses are `pending`, `running`, `completed`, `failed`, `skipped`, `waiting_human`, `rejected`; a node is dispatchable while `pending`. At most `HIVE_MAX_PARALLEL_NODES` (default 4) nodes dispatch at once.

`GetNextNodes` SHALL claim each node it hands out, marking it `running` and counting its attempt, only from `pending`, in one conditional write (`WorkflowsRepo.MarkNodeRunning`), and SHALL leave out a node whose claim changed nothing. So two drivers of one run — two `agent-run --resume <id>`, or `agent-run` beside `chb workflow next` — that read the same pending node never both dispatch it, and its attempt counts one run.

#### Scenario: Two siblings complete concurrently
- **WHEN** two parallel nodes complete at the same moment with different output keys
- **THEN** state holds both nodes' outputs

#### Scenario: Two drivers of one run
- **WHEN** two drivers of a run read the same two pending nodes before either claims one
- **THEN** each node is handed to one driver, once, and is `running` at attempt 1

### Requirement: Edge conditions and readiness

A pending node SHALL be ready when every predecessor over a forward edge (every incoming edge but a back edge) is `completed` or `skipped`, and at least one incoming edge fires. A node with no predecessors is ready at once. An edge from a `skipped` predecessor never fires: a skipped decision chose no branch, and a skipped node produced nothing. Otherwise an edge with no `condition` fires; so does an edge from a decision node, whose branch was already chosen. A conditioned edge fires when `SafeEval` returns true. When no incoming edge fires, because each evaluates false or comes from a skipped predecessor, the node is marked `skipped`, and the skip cascades to descendants with no other live predecessor. So a node every one of whose predecessors was skipped is skipped, whatever order the cascade reached them in. A skip can leave another node with every incoming edge false, or let a decision evaluate. So after each round of skips the engine SHALL resolve pending decisions again, and SHALL repeat until no node is left with every incoming edge false, before it decides whether the run is finished. A node SHALL be skipped this way at most once per request. A loop reset in the same request can return it to `pending` and leave its edges false again (see Decision loops); it then stays `pending`, so the repetition ends. A decision node is readied by the same rule: when every incoming edge is false it is `skipped` without evaluating its condition (no `workflow_decisions` row), and both of its branches are skipped. An edge condition that cannot be evaluated fires the edge and logs the error to stderr. When nothing is ready, nothing is running and no node waits on a human, pending nodes can never run — a predecessor failed or was rejected, or a decision cannot evaluate — and the run SHALL be marked `failed`.

A node with `join: settled` SHALL count a predecessor that failed or was rejected as finished too, and SHALL wait until at least one predecessor completed; its edges then fire by the same rule. A settled join every one of whose predecessors was skipped SHALL be skipped like any other node, not left waiting for an input that completed. It is for a node that reads what its inputs returned and can say which returned nothing, as the swarm's evaluator and Queen do (swarm.md). When every node is final, a node that failed or was rejected SHALL fail the run unless the nodes after it took its place: it has at least one successor over a forward edge, and every one declares `join: settled` and completed. Otherwise the run completes as usual.

#### Scenario: A settled join past a rejected input
- **WHEN** `a → j`, `b → j` and `j → k`, `j` declares `join: settled`, `a` completes and `b` is rejected
- **THEN** `j` is handed out; once `j` and `k` complete, the run is `completed`

#### Scenario: A settled join with no input completed
- **WHEN** both inputs of a `join: settled` node fail or are rejected
- **THEN** the node stays pending and the run is marked `failed`

#### Scenario: A settled join with every input skipped
- **WHEN** `start → d`, a decision whose true branch is `a` and false branch is `z`, with `a → b`, `a → j` and `b → j`, `j` declares `join: settled`, and `d` takes `z`
- **THEN** `a`, `b` and `j` are skipped, whatever order the cascade reached them in, and once `z` completes the run is `completed`

#### Scenario: A settled join that fails
- **WHEN** a `join: settled` node runs past a rejected input and then fails itself
- **THEN** the run is marked `failed`

#### Scenario: Every conditioned edge is false
- **WHEN** all of a node's incoming edges carry conditions that evaluate false
- **THEN** the node is skipped, and so is each descendant with no other live predecessor

#### Scenario: Every predecessor skipped
- **WHEN** `d → m` from a decision and `a → m` with no condition, and both `d` and `a` are skipped
- **THEN** `m` is skipped, not run: as when `workflows/hive.yaml`'s scan reports `capped`, and `merge`, which follows both the skipped `check-has-actions` and the skipped `dispatch`, is skipped with the rest of the pass

#### Scenario: A skip leaves another node with every edge false
- **WHEN** `root → a` if `x == 1`, `a → X` if `x == 1`, `root → X` if `x == 2`, and `root` completes with `x` = 0
- **THEN** `a` and `X` are skipped and the run completes

#### Scenario: A decision whose incoming edge is false
- **WHEN** a decision's only incoming edge carries a condition that evaluates false
- **THEN** the decision and both of its branches are skipped, and no decision row is written

#### Scenario: A skip lets a decision resolve
- **WHEN** `root → a` if `x == 1`, `a → d` and `root → d`, where `d` is a decision, and `root` completes with `x` = 0
- **THEN** in the same request `a` is skipped, `d` evaluates its condition, and the branch it takes is returned

#### Scenario: A skip leaves a decision with every edge false
- **WHEN** `root → a` if `x == 1`, `a → d` if `x == 1`, `root → d` if `x == 2`, where `d` is a decision, and `root` completes with `x` = 0
- **THEN** `a`, `d` and both of `d`'s branches are skipped, and the run completes

#### Scenario: A failed node strands its successor
- **WHEN** `a → b` and `a` fails with no retries left
- **THEN** the next request for dispatchable nodes returns none and marks the run `failed`

### Requirement: Decision loops

When a decision takes a branch that is a back edge, the engine SHALL reset the loop's body — every node on a forward path from the branch's target to the decision, both included — to `pending`, with attempt count, error and completion time cleared and tokens, cost and rationale kept, and SHALL NOT skip the decision's other branch, which waits for a later pass. State carries over between passes, and a node's outputs overwrite its previous pass's. A taken branch that is not a back edge skips the other branch as usual. The reset SHALL also return to `pending` every `skipped` node the body reaches over forward edges through `skipped` nodes only, such as an inner decision's exit and its descendants that an earlier pass skipped, so they can run on a later pass. A `skipped` node reached only through a finished node outside the body, such as the untaken branch of a decision outside the body, SHALL stay `skipped`, since nothing on a later pass chooses it again. The loop is bounded by its condition and the runner's iteration cap, and a pass that runs no node by the rule below.

No node runs while the engine resolves a request's decisions, which it does in name order, so a decision resolves again within that resolution only when a loop that ran no node returned it to `pending`: a closing decision that also follows a completed node outside its loop can loop back after every node of the body was skipped. One such pass SHALL be allowed. A decision that comes round a third time SHALL end the request with an error naming it, and nothing is handed out. So a loop that runs no node, such as a cycle of decisions in a definition that never went through `Validate`, cannot hang a run.

#### Scenario: The hive workflow iterates
- **WHEN** `workflows/hive.yaml` runs with `max_iterations` 2 and `complete-iteration` reports `should_continue` true after the first pass and false after the second
- **THEN** `scan` through `complete-iteration` run twice, `dispatch` runs on a pass whose `research_actions` hold an action and is skipped on a pass whose `research_actions` are empty, the gate's nodes run on a pass whose scan requests the gate and are skipped on one that does not, then `report`, `final-synthesis` and the nodes after it run once and the run completes

#### Scenario: An inner decision leaves the loop on a later pass
- **WHEN** pass 1 takes an inner decision's branch that stays in the loop, and pass 2 takes its other branch, which leaves the loop
- **THEN** that branch runs on pass 2, the rest of the loop is skipped, and the run completes

#### Scenario: A side decision's untaken branch stays skipped
- **WHEN** the head feeds both the loop's body and a decision outside the body, which takes one branch on pass 1, and the loop runs again
- **THEN** pass 2 runs the body only, and the side decision's untaken branch stays `skipped`

#### Scenario: A decision whose branch is itself
- **WHEN** a run started without `Validate` reaches a decision whose taken branch is the decision itself, or a cycle of two decisions
- **THEN** `GetNextNodes` returns, within a second, an error naming the decisions, and hands out nothing

#### Scenario: One pass with no node run
- **WHEN** a head decision skips the loop's only node, and the closing decision, which also follows a completed node outside the loop, loops back once and its `state_updates` turn its condition false
- **THEN** the head resolves again, the closing decision leaves the loop, and the nodes past both are handed out in the same request

#### Scenario: A skipped head with a decision that still loops back
- **WHEN** on pass 2 the head's only incoming edge is false, and the closing decision, which also has a completed predecessor outside the loop, loops back again
- **THEN** the request returns, the head stays `pending`, and with nothing else live the run is marked `failed`

### Requirement: Decision persistence

Every decision evaluation SHALL be recorded in `workflow_decisions(id, run_id, node_name, condition, evaluated_value, branch_taken, eval_error, created_at)`, with `evaluated_value` `"true"`, `"false"` or `"<error>"`. A condition that fails to evaluate SHALL NOT abort the dispatch pass: the row records `eval_error`, the decision stays pending, the error is logged, and other ready work is still dispatched. If nothing else can run, the run is marked `failed` (see Edge conditions and readiness).

#### Scenario: A decision condition errors
- **WHEN** a decision's condition references a missing variable
- **THEN** a `workflow_decisions` row with `evaluated_value` `"<error>"` is written and the decision node stays pending

### Requirement: Accept predicates

After an agent node's outputs hold its `output_schema` (§ Output schema), are merged and `state_updates` applied, the engine SHALL evaluate each `accept:` predicate, with `outputs` bound to the node's outputs map, and return the first failure as `*workflow.AcceptRejection` (node name, predicate, evaluated value, evaluation error, outputs, reason). The runner hands a rejection to the node's `on_reject:` block if it has one; otherwise, or when repair is exhausted, it marks the node `rejected` — a terminal status — with the rationale `accept rejected: <predicate> = <value>`, and emits `node_rejected`. A repair that passes emits no `node_rejected`.

Before the predicates run, the engine SHALL write to state the values it computes for them, after the outputs and `state_updates`, so an output cannot replace them. They come from this run's forager verdicts, for a node whose prompt reads `{tally}` (comb.md § Template tokens): `tally_plurality`, the plurality, `""` when there is none; `tally_votes`, the votes cast, one for each lens whose verdict is not abstain; `tally_abstentions`, the lenses that abstained; and `tally_line`, the tally as `{tally}` renders it. They are computed in `CompleteNode`, so `chb agent-run`, every repair attempt and `chb workflow complete` get them alike. They never enter the node's outputs, which are stored as the node returned them.

An `accept:` item SHALL be a predicate string, or a map holding a `predicate` string and, optionally, a `reason` string. A reason is plain words for a model or an operator. When its predicate evaluates to false, the rejection carries the reason with its placeholders filled in one pass: `{outputs.<key>}` from the node's outputs, any other from state, computed values included. A placeholder no value fills stays as written. The rejection's text then leads with the reason and names the predicate after it. The `failure_reason` of each `workflow_repairs` row, the log line of a repair still rejected and `chb workflow complete` carry that text. A repair prompt's `{accept_failure}` is the reason alone, without the predicate (§ Bounded repair). The node's rationale, its `REJECTED` log line and `node_rejected` name the predicate only. A predicate given as a string, and one that fails to evaluate, reject with that text alone: `accept: predicate "<p>" on node "<node>" evaluated to <value>`, or `failed to evaluate (<error>)`. Validation (`CheckNodeFields`) SHALL refuse an `accept:` that is not a list, an item that is neither form, a map with no `predicate` string or a reason that is not a string, and any other key in the map. The engine skips such an item, so it would gate nothing.

#### Scenario: A plurality the model wrote
- **WHEN** a Queen returns `tally_plurality: oppose` while the engine counts `support`
- **THEN** her `accept:` reads `support`, and her stored outputs keep `oppose`

#### Scenario: A reason in plain words
- **WHEN** a node's item `{predicate: "outputs.n >= limit", reason: "n was {outputs.n} under {limit}"}` fails with `n` 3 and `limit` 5
- **THEN** the rejection reads `n was 3 under 5 (accept: predicate "outputs.n >= limit" on node "<node>" evaluated to false)`, and the repair prompt gives `n was 3 under 5` alone

#### Scenario: A misspelled reason key
- **WHEN** an `accept:` item is `{predicate: "a == 1", reasons: "r"}`
- **THEN** validation refuses the workflow, naming the node, the item and the key

#### Scenario: A rejected node ends its run
- **WHEN** a node is rejected with no repair left and nothing else can run, including when its successors are still pending
- **THEN** the run is marked failed rather than left running

### Requirement: Bounded repair

For a rejected node with `on_reject:`, the runner (`tryRepair`) SHALL make at most `max_repair_iterations` attempts (default 3; the implement generator sets 1, and the swarm generator sets 1 on `scope`, every forager, `swarm-evaluate` and `queen`). Each attempt records a `workflow_repairs` row with the model and provider it runs on and its trigger (runner.md § Accept rejection and repair), builds its prompt from `prompt_template` substituting `{accept_failure}` (the item's reason when it gives one, else the rejection's text), `{original_prompt}` and `{outputs}` only, in one pass, so a placeholder inside the original prompt stays as written, and calls the backend on the block's `model`, else its `tier`, else the model the node's dispatch was sent (runner.md § Accept rejection and repair), with the node's tools. A backend error marks the attempt failed and moves to the next one; an attempt whose outputs pass `accept:` completes the node; a completion error other than a rejection ends repair. On a node with `min_tool_calls`, an attempt is checked only once the dispatch's and the attempts' tool calls together reach the minimum; until then each attempt is rejected with the count (runner.md § Minimum tool calls). Every attempt's tokens and cost are added to the node's `workflow_node_states` row, and its tool invocations to the node's `tool_invocations` rows.

#### Scenario: Repair exhausted
- **WHEN** every attempt's outputs fail `accept:`
- **THEN** each attempt has a `workflow_repairs` row with `accept_passed=0` and the node is rejected

#### Scenario: A repair short of its tool calls
- **WHEN** a `min_tool_calls: 1` node's call and its one repair attempt each make no tool call
- **THEN** the attempt's row has `accept_passed=0` and a `failure_reason` naming `min_tool_calls`, and the node is rejected

### Requirement: Retry

A failed node with `max_retries: N` SHALL run at most N+1 times: each failure under the limit resets it to `pending` for re-dispatch, and the last marks it `failed`. The runner and `chb workflow fail` share one retry rule (`workflow.FailNode`). A failure on a node that was never marked running counts as a run. A dispatch the run's own cancellation stopped is not a failure: the runner returns the node to `pending` with the attempt its claim counted taken back (`workflow.ReleaseNode`), so it spends no retry (runner.md § Run outcome). Retry is independent of accept and repair.

#### Scenario: One retry
- **WHEN** a node with `max_retries: 1` fails twice
- **THEN** it ran twice and is `failed`

#### Scenario: Failed without being dispatched
- **WHEN** `chb workflow fail` fails a pending node with `max_retries: 1` twice, with no dispatch in between
- **THEN** it is `failed`

### Requirement: Per-node persistence

`workflow_node_states` SHALL carry, per node: `rationale` (for a completed node, the accepted attempt's text — a passing repair's, not the rejected dispatch's — and for a node failed under `min_tool_calls` with no `on_reject:`, the short call's text, each cut on a UTF-8 boundary so that with its trailing ellipsis it is at most 64,000 bytes), `tokens_in` and `tokens_out` (cumulative over every call that returned a result: the dispatch, whether accepted, rejected or failed, and each repair attempt), `cost_usd_x10000` (cumulative, priced from whatever usage the backend reports; 0 when it reports none or the model has no price), `provider`, `base_url` (the endpoint that served the node's latest call, NULL for the CLI backends; runner.md § Per-node persistence and cost), `metered_calls` and `unmetered_calls` (the model calls charged to the node, split by whether their cost is known: a priced model on a backend that reports token counts), `resolved_model` (the canonical id of the model the node's `model:` or `tier:` resolved to at its latest dispatch, or of the passing repair's model when a repair passed; NULL until one resolves a model), and `schema_enforcement` (for a node with an `output_schema` that completed after a call, `enforced at decode` or `post-hoc only`, runner.md § Constraint probe; NULL for a node without one; for one that completed with no call, as an empty fan or a dry run does; and for one completed through `chb workflow complete`, whose answer is checked but whose call chb did not send). So a completed node's `rationale` and `resolved_model` name the same attempt.

#### Scenario: Another model repairs the node
- **WHEN** a node on `model-a` fails `accept:` and its `on_reject: {model: model-b}` attempt passes
- **THEN** the node's `rationale` is model-b's text and its `resolved_model` is `model-b`, and the `workflow_repairs` row names `model-b`

#### Scenario: Repair spend is counted
- **WHEN** a node takes one dispatch and two repair attempts
- **THEN** its token and cost columns are the sum of all three calls, whether the last attempt passes `accept:` or not

### Requirement: Human review

A `human_review` node SHALL park as `waiting_human` and pause the run, and dispatch hands out nothing while it waits; successors stay `pending`. The runner parks the node when it dispatches it. `chb workflow next` parks it instead of handing it out. `chb workflow resume <run_id> approve|reject|redirect [--feedback …]` SHALL complete the node (approve, redirect) or fail it (reject), write `{human_decision}` and `{human_feedback}` into state, and return the run to `running`. The node row, the answer and, on approve or redirect, the node's `state_updates` are written in one transaction. Any other decision SHALL be refused before anything is written. `chb agent-run --resume <run_id>` continues the run.

#### Scenario: Redirect
- **WHEN** a paused run is resumed with `redirect --feedback "narrow to EU"`
- **THEN** the node completes, state `human_feedback` is `"narrow to EU"`, and the run is `running`

#### Scenario: A mistyped decision is refused
- **WHEN** `chb workflow resume` is given the decision `aprove`
- **THEN** it is refused, the node is still `waiting_human`, and the run is still `paused`

#### Scenario: Review in the manual loop
- **WHEN** `chb workflow next` reaches a ready `human_review` node
- **THEN** the node is `waiting_human`, the run is `paused`, and `chb workflow resume` can answer it

## Files

- `internal/workflow/node_fields.go` — `ToolNames`, `CheckNodeFields`, the `tools:` allowlist, `join: settled`, `role:` and `ttl:` (`NodeTTL`), the `accept:` items (`checkAccept`), and the readers of a node's fields
- `internal/workflow/run_tokens.go` — the run tokens (comb.md § Template tokens)
- `internal/workflow/tally.go` — the vote tally and the `tally_` values
- `internal/workflow/diversity.go` — the lens diversity check
- `internal/workflow/verdict.go` — a verdict's text
- `internal/workflow/resolve.go` — `ResolveFile`; `assets.go` at the module root — the carried `workflows/` (`hive.Workflows`)
- `internal/workflow/load.go` — `LoadYAML`, `LoadYAMLString`, `ListWorkflows`
- `internal/workflow/validate.go` — `Validate`, the command and calibrate node checks, and `FanSourceProblems`, the fan wiring check
- `internal/workflow/init.go` — `InitWorkflow`, the required inputs, `CheckReasoning`; `internal/models/config.go` — `ReasoningLevels` and `CheckReasoningLevel`, the one check of a level that a node, a routing profile's route, the swarm generator's reasoning options and `chb agent-harness`'s reasoning flags share
- `internal/workflow/graph.go` — `BuildGraph`, back edges, loop bodies, `findCycle`: the cycle no decision can exit, and the cycle of decisions alone
- `internal/workflow/next.go` — `GetNextNodes`, `GetNextNodesManual`: the rounds of skips, and the claim
- `internal/workflow/dispatch.go` — `DispatchNode`, and the fields each node type carries
- `internal/workflow/fan.go` — a fan's items from its `fan_source`, `fan_limit` and the fan overflow
- `internal/workflow/ready.go` — readiness and edge conditions
- `internal/workflow/skip.go` — the skip cascade (`skipBranch`)
- `internal/workflow/decision.go` — decisions: their conditions, their branches, loop resets, and the bound on a loop that runs no node
- `internal/workflow/finalize.go` — finalisation of a run with nothing ready
- `internal/workflow/complete.go` — `CompleteNode`, `CompleteFanItems`, `CompleteEmptyFan`, and the completion transaction; `ExtractJSONOutput`, which reads a node's outputs from its reply, and `AliasFinalTextToDeclaredOutput`, which takes its `final_text` fallback
- `internal/workflow/accept.go` — `AcceptItems`, `AcceptRejection` and the accept reasons
- `internal/workflow/retry.go` — `FailNode` and its retry, `ReleaseNode`
- `internal/workflow/human.go` — `PauseForHuman`, `AnswerHumanReview`, and `ResumeHumanReview`, behind `chb workflow resume`
- `internal/workflow/eval.go` — `SafeEval`
- `internal/workflow/template.go` — `ResolveTemplate`, and a node's prompt filled from state and the run tokens
- `internal/workflow/state.go` — the run state, `state_updates`, and the node rows the engine reads
- `internal/workflow/store.go` — `Store`, the methods of `*db.WorkflowsRepo` the engine keeps a run through
- `internal/workflow/output_schema.go` — `OutputSchemas`, `CheckOutput`, the completion check
- `internal/schema/schema.go` — the schema subset, `Clone`, `MoveLast` and `Strict`
- `internal/schema/parse.go` — parsing a schema, and the contradictions it refuses
- `internal/schema/validate.go` — `Validate`
- `internal/schema/json.go` — ordered serialization
- `internal/schema/mapping.go` — `MapEntries`, which reads a YAML mapping with its merge keys resolved
- `internal/runner/repair.go` — `tryRepair`, the `on_reject:` loop (in the runner because it dispatches through a backend)
- `internal/runner/command_node.go` — `command` nodes (runner.md § Command nodes)
- `internal/db/workflow_node_states.go` — `MarkNodeRunning` (the claim), `ReleaseNode`, `FinishNodeInTx`, `MarkNodeRejected`, `UpdateNodeMetrics`; `workflow_decisions.go` — `RecordDecision`; `workflow_repairs.go` — `RecordRepairAttempt`, `MarkRepairCompleted`
- `internal/db/schema.go` — `workflow_runs`, `workflow_node_states`, `workflow_decisions`, `workflow_repairs`
- `internal/cli/workflow.go` — `chb workflow` commands
- `internal/cli/preflight.go` — `chb preflight`; `internal/cli/preflight_workflow.go` — its checks of the workflow file, the fan wiring check among them
- `internal/cli/gen_implement_workflow.go` — the implement-workflow generator
