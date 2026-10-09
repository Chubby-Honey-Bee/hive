# Agent Runner Specification

## Purpose

Drives workflow nodes to completion: resolves a backend and model per node, runs the agent with its tools, runs command nodes with no model, gates outputs through `accept:` with optional repair, persists rationale, tokens and cost, commits file edits, and hands outputs back to the workflow engine.

## Requirements

### Requirement: Backend selection

A run's backend SHALL resolve by this precedence, first match wins:

1. `--provider <name>` on `agent-run`, `review` and the other run commands
2. The provider of the run's routing profile (§ Routing profiles)
3. `HIVE_PROVIDER` (`anthropic` \| `gemini` \| `openai` \| `claude-cli` \| `gemini-cli` \| `local`; aliases `claude-code`, `copilot`, `azure-openai`, `google`)
4. The first SDK key set: `ANTHROPIC_API_KEY` → anthropic, `GEMINI_API_KEY` or `GOOGLE_API_KEY` → gemini, `OPENAI_API_KEY` → openai
5. The first CLI on `PATH`: `claude` → claude-cli, `gemini` → gemini-cli
6. claude-cli, which reports a clear error if the binary is missing

A node's own `provider:` SHALL override the run default for that node only. A `provider:` that names none of these kinds or aliases, such as `ollama`, SHALL refuse the run before any dispatch, naming `provider: local` for a server on this machine. `local` is the OpenAI-compatible backend at `HIVE_LOCAL_BASE_URL` (§ Backends), so a local server and OpenAI itself, at `OPENAI_BASE_URL`, can serve different nodes of one run. If `HIVE_PROVIDER_ALLOWLIST` refuses a node's provider, the run SHALL be refused before any dispatch. If a node's backend cannot be built, the node runs on the run default and the runner logs why, except under a routing profile, where the node fails instead (§ Routing profiles). `HIVE_PROVIDER_ALLOWLIST` gates provider kinds, not hosts (SECURITY.md).

The runner SHALL build the run default's backend and check every node's `provider:` against the known kinds and the allowlist before it creates the run. So an unknown provider, an allowlist refusal, or a run-default backend that cannot be built, leaves no `workflow_runs` row and no auto-commit branch.

Setting `OPENAI_BASE_URL` does not choose the OpenAI-compatible backend. A run that resolves elsewhere by the order above, because another key is set or `claude` is on `PATH`, stays there, and the model preflight logs that the variable is set but unused (§ Endpoint model preflight).

Outside `--dry-run`, `agent-run` SHALL refuse to start when the run resolves to the Anthropic SDK and neither `ANTHROPIC_API_KEY` nor `ANTHROPIC_AUTH_TOKEN` is set. The check uses the resolved kind, so `--provider` outranks `HIVE_PROVIDER`.

#### Scenario: A node override wins over --provider
- **WHEN** a run started with `--provider anthropic` reaches a node declaring `provider: openai`, with an OpenAI key set
- **THEN** that node runs on the OpenAI backend and its row records `openai`

#### Scenario: The allowlist refuses a node's provider
- **WHEN** `HIVE_PROVIDER_ALLOWLIST=claude-cli` and a node declares `provider: openai`
- **THEN** `agent-run` fails naming that node, no node is dispatched, and no `workflow_runs` row is written

#### Scenario: A backend that cannot be built
- **WHEN** `chb agent-run wf.yaml --provider openai --branch x` starts with `OPENAI_API_KEY` unset
- **THEN** it exits with an error, writes no `workflow_runs` row, and creates no branch `x`

#### Scenario: A provider no backend has
- **WHEN** a node declares `provider: ollama`
- **THEN** `agent-run` fails naming the node and `ollama`, and writes no `workflow_runs` row

#### Scenario: --provider beats HIVE_PROVIDER
- **WHEN** `HIVE_PROVIDER=anthropic`, `ANTHROPIC_API_KEY` is unset, and `agent-run` runs with `--provider claude-cli`
- **THEN** the run starts on the Claude CLI

### Requirement: Backends

The runner SHALL provide five backends:

| Backend | Transport | Auth | Aliases (`haiku` / `sonnet` / `opus`) |
|---|---|---|---|
| Anthropic SDK | `anthropic-sdk-go`, each turn a streamed Messages call, in-process tool loop | `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` | resolved through the models config (empty is `sonnet`): `claude-haiku-4-5-20251001` / `claude-sonnet-4-6` / `claude-opus-4-8`; a name that is no alias there is sent as written |
| Gemini SDK | REST `generateContent`, tools as `functionDeclarations` | `GEMINI_API_KEY` or `GOOGLE_API_KEY` | `gemini-2.5-flash` / `gemini-2.5-pro` / `gemini-2.5-pro` |
| OpenAI-compatible | REST `<OPENAI_BASE_URL>/chat/completions` (default `https://api.openai.com/v1`), tools as `tools[].function`; covers OpenAI, Copilot's endpoint, Azure OpenAI and OSS gateways | `OPENAI_API_KEY`, optional `OPENAI_ORG` | `gpt-5.1-mini` / `gpt-5.1` / `gpt-5.1-pro` |
| OpenAI-compatible, provider `local` | the same backend at `<HIVE_LOCAL_BASE_URL>/chat/completions` (default `http://localhost:11434/v1`, Ollama) | none: it sends `Authorization: Bearer local`, so no provider key reaches that server | as OpenAI-compatible |
| Claude CLI | the `claude` binary (`CLAUDE_CODE_CLI_PATH`), `--allowedTools Read,Write,Edit,Glob,Grep,Bash,WebFetch` plus `mcp__chb__chb_db_write` when a `chb-mcp` sidecar (`HIVE_MCP_PATH`, else `chb-mcp` on `PATH`) is found, narrowed by a node's `tools:` (§ Tool allowlist) | the user's Claude Code login | passed to the CLI |
| Gemini CLI | the `gemini` binary (`GEMINI_CLI_PATH`), prompt on stdin, `-p ""`, `--output-format json` when the CLI has it | the user's gemini-cli login | the Gemini SDK's |

Full model ids pass through unchanged. Where this spec says the OpenAI-compatible backend, it means both provider `openai` and provider `local`; each has its own endpoint, and what is said of `OPENAI_BASE_URL` holds for `HIVE_LOCAL_BASE_URL` on a `local` node, except that the local endpoint is always asked (§ Endpoint model preflight).

Every backend SHALL honour one contract for a call that ends without an answer, and for the tool results its loop sends back:

- **Out of turns.** The Anthropic SDK, Gemini and OpenAI-compatible backends run the tool loop in chb. A loop that uses all its turns (30, or the request's cap) while the model still calls tools SHALL make one wrap-up call: the conversation so far, ending with a message that no tool call is left and asking for the final answer in the form the task asks for, with no tool call possible (the tools dropped on the OpenAI-compatible backend, `tool_choice` `none` on the Anthropic SDK, function calling `NONE` on Gemini) and the request's schema set when it has one. Its reply is the node's answer, held to every check an answer is: the output schema, `accept:` and `min_tool_calls`, which counts the loop's tool calls. The run log says a wrap-up call gave the answer. A wrap-up reply that calls a tool, a wrap-up call that fails, and a reply that is no answer (§ Incomplete replies fail) fail the node with an incomplete reply: the result's stop reason is `max_turns`, the error names that stop reason, the turn cap and why the wrap-up gave no answer, and every call's usage, the wrap-up's included, comes back with it. It is never read as a rate limit, whatever the cap (§ Rate limiting). The CLIs run their own loops: the Claude CLI reports `error_max_turns`, a plain error that comes back with its tokens, and the Gemini CLI lists `Maximum session turns exceeded` among its warnings (§ Incomplete replies fail).
- **Spend on failure.** A call that fails after the provider answered comes back with the usage it spent, so it is charged (§ Per-node persistence and cost). The SDK backends return every answered turn's usage with the error that ends the loop. A CLI that exits non-zero is read for the reply it printed: the Claude CLI's result, and the Gemini CLI's `--output-format json` reply, from stdout, else from stderr, where gemini-cli prints an error it throws. A reply that reports tokens comes back with the error. The Claude CLI's error is what a result marked `is_error` reports, else the exit status and stderr; the Gemini CLI's is the exit status and stderr, then the error its reply carries. A reply that reports no tokens, or no reply, gives no result, and no call is counted.
- **Cancellation.** A call whose context ends, cancelled or past its node's budget, fails with an error that wraps the context's error, so `errors.Is` finds `context.Canceled` or `context.DeadlineExceeded`. A CLI call kills the CLI's process group (below) and fails with `claude CLI timed out or cancelled: …` or `gemini CLI timed out or cancelled: …`, not with the signal the CLI died of, and what the CLI printed is not read.
- **Tool results.** Every tool result is held to one bound before it joins the conversation: the room the next call leaves it under the model's context window, when the backend knows the window (§ Context window guard); else 50,000 bytes, what is kept and its note together. Only the OpenAI-compatible backend is given a window, and only when one is known, so the Anthropic SDK and Gemini backends hold every result to 50,000 bytes. A longer result is cut at a character boundary and ends with the same note on every backend: `[cut: kept <k> of <n> bytes; <why>.`, then how to get the rest when the tool has a way (§ Context window guard), then `]`. `<why>` is `the rest would not fit the model's context window with room for the reply`, or `the rest is past the 50000-byte cap on a tool result`. A tool's error text is held the same way. A cut is logged, `tool <name>: result cut to <k> of <n> bytes,` then the room the context window leaves it or the 50000-byte cap, and `tool_invocations` holds the tool's whole output, cut at 20,000 bytes.
- **Provider naming.** An error that names a provider names the one the run resolved for the backend, never another. The OpenAI-compatible backend names `local` on provider `local`. On provider `openai` it names the provider as the run named it: the first of `--provider` (a node's `provider:`, for that node) and `HIVE_PROVIDER` that names a provider, in lower case, when that is `openai`, `copilot` or `azure-openai`; else `openai`, as when `OPENAI_API_KEY` chose it. Every error of that backend that names a provider does so: a cut-off or empty reply, a loop out of turns, an HTTP error status, a transport error, an empty choices list, a slot or TTL error and the model listing. The node's row records the provider's kind, `openai`, whichever name the errors use.

The Gemini CLI backend SHALL send `--output-format json` when the CLI has that flag. It asks by running `<cli> --help` and looking for `--output-format` in what it prints, stdout and stderr. gemini-cli answers `--help` while it parses its flags, before it signs in or calls a model (`parseArguments` in packages/cli/src/config/config.ts), so the probe costs nothing. Calls at once on one CLI path share one probe. A `--help` that answers holds for that path for the rest of the process. One that fails is not kept: the next call asks again. A `--help` that has not answered in 30 seconds is killed and counts as failed, and its output is closed a second later even when a child it started still holds it open. A call whose context ends first stops waiting, and the probe goes on for the others. gemini-cli has the flag from 0.6.0: v0.6.0's config.ts declares it and v0.5.5's does not. With the flag, the final text is the reply's `response`, and its token counts come from `stats.models` (§ Per-node persistence and cost). The reply is the first line of stdout that opens a JSON object with `response`, `stats` or `error`, so a line the CLI prints ahead of it is skipped. A CLI whose `--help` lists no such flag, or fails, is sent no `--output-format`: the prompt goes on stdin with `-p ""`, and its stdout is the final text.

The Gemini CLI backend SHALL send `-p` an empty value. From gemini-cli 0.12.0, `-p` takes exactly one value (`nargs: 1` in config.ts), and yargs-parser fails a bare `-p`, or one followed by another flag, with `Not enough arguments following: p` (`eatNargs`). The CLI puts stdin ahead of `-p`'s value, so the prompt on stdin is the whole input. Earlier versions read `-p ""` as an empty prompt too. This is read from the source. No gemini-cli runs in the tests; they use a fake that applies the rule.

The OpenAI-compatible and Gemini backends SHALL bound each HTTP call by the request's TTL: the node's `ttl:` (workflow.md § Workflow definition) when it has one, else 5 minutes for a node or a repair attempt and 15 for a `parallel_fan` item. `HIVE_HTTP_TIMEOUT`, a positive Go duration such as `15m`, replaces the TTL for every call when it is set. Any other value refuses the backend. Their clients have no timeout of their own. A call past its bound fails with `no reply within <d> (the call's TTL)` or `(HIVE_HTTP_TIMEOUT)`. The node's own budget (30 minutes, or 15 per fan batch, or its `ttl:` per call or batch when that is longer) still bounds every call.

A call to one of these endpoints SHALL first wait in chb for a free slot, and its bound starts only once it has one. At most `HIVE_MAX_PARALLEL_ENDPOINT` calls to one server are in flight at once, across every backend, node, fan item and embedding request in the process (comb.md § Embeddings), and across the chb processes on this machine (below), read and clamped to 1–64 as `HIVE_MAX_PARALLEL_NODES` is. Unset, the bound is 1 for a server on this machine (`localhost`, a loopback address, or the unspecified address `0.0.0.0` or `::`, which a call reaches this machine on), where Ollama and LM Studio may answer one request at a time, and there is none for any other server. The variable is one number for every server. Endpoints that reach one server share its slots: a server is its host and port, the port defaulting by scheme, and every name for this machine is one host. So `http://localhost:11434/v1` on the OpenAI-compatible backend and `http://127.0.0.1:11434` on the Anthropic SDK backend share one Ollama's slot; Ollama serves both APIs. So on a single-slot local server, a call's bound measures its own reply, not the calls chb sent ahead of it. A call that does not stream cannot tell waiting from generating, so time the request waits in the server's own queue still counts: behind a client that is not chb, behind a chb process with no lock files, or behind chb's calls when the variable allows more than the server's slots. A call that gets no slot before its node's budget ends fails with `no free slot at <endpoint>`. Either error starts with the call's provider, `local` on provider `local`, so a local call's error does not name `openai`. The `llm backend:` log line names the bound on the run default's endpoint, for each of these backends. The Anthropic SDK backend waits for a slot too, per turn, at `ANTHROPIC_BASE_URL` (else `https://api.anthropic.com`), so a server on this machine behind that variable also gets one call at a time. It streams, and bounds each wait for a stream event instead of the whole call (§ Per-turn output cap), so `HIVE_HTTP_TIMEOUT` does not apply to it. Neither variable applies to the CLI backends, whose calls chb does not make.

The slots SHALL be shared by the chb processes on this machine. So `chb ask` in one shell and `chb comb embed` in another take turns at a single-slot server, as do two runs, or a run chb-mcp starts beside one of yours. A slot is a lock file. Each call holds one of its server's files `<h>-0.lock` to `<h>-<N−1>.lock`, locked with `flock(2)`, and writes its pid there. The files live in `HIVE_ENDPOINT_SLOT_DIR`, else in `<user cache dir>/chb/slots`: `~/Library/Caches/chb/slots` on macOS, `$XDG_CACHE_HOME/chb/slots` or `~/.cache/chb/slots` on Linux. A leading `~` in the variable is the home directory, since an MCP client's env block does not expand it. A relative path is refused: processes started in different directories would read it as different directories. `<h>` is 16 hex digits of the SHA-256 of the server's key. The key itself is not used, because an endpoint that does not parse is its own key and may hold a credential. A process whose bound is N uses the first N files. So when processes run with different bounds, the server gets at most the largest of them at once. A call waits for its process's own slot first, so a process tries the files for at most N calls. The directory is per user, so two users' processes do not share slots.

A call waiting for another process's slot SHALL hold `<h>-next.lock` while it waits, one waiter per process at a time. So a slot a process frees goes to the waiter that queued, not back to the process that freed it: that process's next call, ready the same instant, cannot get next.lock. The waiter tries the files every 25 ms. A call that cannot get next.lock still tries the files, and takes one only once it has found it free on 4 polls in a row. A queued waiter that is running takes a free file within one poll. So a file free that long has no waiter that can take it: the waiter's process was stopped with Ctrl-Z, its bound is smaller and does not reach that file, or the machine has not run it for that long. A stopped waiter then costs another process's call about 75 ms, not the time until it resumes. A process stopped with Ctrl-Z while it holds a slot keeps it until it resumes or exits.

A call that has waited 5 s for other processes SHALL log it, once per server: `endpoint slots: <server>: waiting for a slot other chb processes hold (pid <p>, …); the call's timeout starts once it has one`, with the pids the holders wrote. A waiting call stops when its context ends, cancelled or past its node's budget. It then fails with `no free slot at <endpoint>`, as a wait within the process does, and the error names the holders: `waiting for a slot other chb processes hold (pid <p>)`. A holder removes its file before it lets go, so no lock files stay behind. A lock taken on a file no longer at its path does not count. The kernel releases a process's locks when it exits, however it exits, so a killed or crashed run frees its slots; the file it leaves is taken as it is.

The first call to a bounded server SHALL log once, on stderr, which applies: `endpoint slots: <server>: N call(s) in flight at a time, shared with other chb processes (lock files <path>-*.lock)`, or `… from this process; other chb processes are not counted (no lock files: <reason>)`. `<server>` is the server's key, or `an endpoint with no host`. The slots are the process's own when this build has no `flock` (Windows, Solaris, illumos, AIX, WebAssembly), when there is no user cache directory, when `HIVE_ENDPOINT_SLOT_DIR` is relative, or when the directory cannot be made or a file there cannot be locked. They are the process's own too when a second open of a locked file there, in the same process, can lock it as well. The slots rest on flock's locks belonging to an open file, as they do on a local disk. Where they belong to the process, as when Linux emulates flock with POSIX locks on NFS, two calls in one process could hold one slot file. A lock file that fails later is tried once more after the directory is made again, as when a cache cleaner removed it. If it still fails, the call goes ahead counted in this process only, and that is logged: `endpoint slots: <server>: a lock file failed (<reason>); until one works again, calls are counted in this process only`. Each later call tries the files again. The first that locks one logs `endpoint slots: <server>: the lock files work again; calls are shared with other chb processes`.

The in-process tool registry the SDK backends use SHALL register `read_file`, `write_file`, `edit_file`, `glob`, `grep`, `shell`, `web_fetch` and `chb_db_write` (the name the MCP server gives the same tool), confined to the project directory; `shell`'s deny-list is a typo guard, not a sandbox. A node's `tools:` narrows the set its calls are offered (§ Tool allowlist).

`shell` SHALL run a command through the shell resolved for the machine. The resolution is a definition, the same on every OS: the first of `bash -c`, `pwsh -NoProfile -NonInteractive -Command`, `powershell` with the same arguments, and `cmd /d /s /c` that is on `PATH`; none found, `bash` by name, so the call fails where it runs. The lookup is `exec.LookPath`, once per registry, when the node's call starts. bash comes first on Windows too: every shipped prompt's example commands are bash-flavoured, and a machine with Git for Windows or WSL has a `bash` that runs them unchanged; a WSL `bash` with no distribution fails, and the result names it. `cmd` is given its command line whole, `<cmd> /d /s /c "<command>"`, since cmd reads quotes by rules of its own and Go's quoting of each argument would leave backslashes before the command's own; this is read from the cmd and Go documentation and is run only by the Windows CI job. The tool's description names the shell and its arguments, so a model knows which syntax to write before it calls. On Windows the result's first line is `[shell: <name>]`, the shell that ran, bash included; elsewhere the result is the command's output alone. The working directory, the 300 s timeout, the combined output, the deny-list, the environment (below), the audit row in `tool_invocations` and the cut note's hint (§ Context window guard) are the same under every shell. Not checked: whether a command written for bash, such as the hive prompt's single-quoted JSON, means the same under pwsh or cmd; it is sent as written.

`bash` SHALL be another name for `shell` (`workflow.ToolAliases`, `workflow.CanonicalTool`): one registry entry, offered to the model as `shell`. A call that names `bash`, on any backend, runs `shell` as resolved and returns the same result, and its `tool_invocations` row names the tool `shell`. A node's `tools:` may name either, and `bash` in the list keeps `shell`; on the Claude CLI either keeps the built-in `Bash`. A refusal names the tool as the model called it.

`web_fetch` SHALL return the response as text. Its first line is the HTTP status. A body whose `Content-Type` names HTML, or that is sniffed as HTML when the header is absent, is reduced to its readable text: scripts, styles and markup out; headings as lines prefixed with one `#` a level; paragraphs, list items (`- `) and table rows on lines of their own; links as `[text](href)`, the href resolved against the page's URL; entities decoded; runs of whitespace folded to one space outside `<pre>`. Any other body is returned as it is. It reads up to 200 KiB of the body. It takes an `offset`, the byte of the text to start at, and `read_file` takes an `offset`, the first line to return, counted from 1; a result the context window guard cut names the offset that gets the rest (§ Context window guard). The reduction trades this: a table keeps its cells on a row, separated by ` | `, and loses its grid; images and their alt text are dropped; a model asked about a page's markup cannot see it. On the sqlite.org file-format page it kept 52% of the bytes and 42% of the tokens by text class.

The OpenAI-compatible backend SHALL refuse a tool call whose arguments are not a JSON object. Empty or `null` arguments are a call with none. It does not run the tool on `{}`: it sends the model `ERROR: the tool was not run: its arguments are not a JSON object …` as that call's result, naming the arguments, and records the call with its raw arguments as an error in `tool_invocations`. The Gemini and Anthropic APIs hand back arguments already decoded.

Every agent subprocess — the Claude CLI, the Gemini CLI, and the in-process `shell` tool — and every command node's program SHALL run with the parent's environment plus two changes (`agentEnv`): the running `chb`'s directory first on `PATH`, so an agent told to run `chb …` reaches the binary driving it even when none is installed; and `HIVE_DB_PATH` set to the run's database when the run has one, replacing any inherited value, so what an agent writes with `chb db-write` lands in the database the run reads. The run's database is `Config.DBPath`, else the inherited `HIVE_DB_PATH`, made absolute once before any node runs: agents and command nodes run in the project directory, where a relative path would name another file than the one the run opened. The in-process `shell` tool SHALL also run without the provider credentials `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `OPENAI_API_KEY`, `GEMINI_API_KEY` and `GOOGLE_API_KEY`. Its output is stored in `tool_invocations` and sent back to the provider, so a command that printed a key would write it into both. The CLI backends keep those variables, since each CLI may authenticate with its key. A process a model drives, the Claude CLI, the Gemini CLI and the in-process `shell` tool but not a command node's program, SHALL also carry `HIVE_AGENT_DB` naming the run's database when the run has one (`modelEnv`), so a command that only the workflow's own steps may run against that database can refuse it there: `chb hive next`, `complete` and `reset` do (hive.md § The hive workflow runs its relays without a model).

Every agent subprocess SHALL run in a process group of its own, as a command node's program does (§ Command nodes). When its call's context ends, by the call's timeout or a cancelled run, the whole group is killed, and the wait for the program's output ends 5 seconds after the program exits or is killed, even while a child it left behind, such as one a command started with `&`, still holds the output open. So a `shell` command that exits and leaves such a child returns 5 seconds later with what was printed until then, and a CLI that does so fails its call.

#### Scenario: Malformed tool arguments
- **WHEN** a local model calls `read_file` with arguments `{"path": "README`
- **THEN** the tool does not run, the next request carries a tool message starting `ERROR: the tool was not run`, and `tool_invocations` holds the call with `is_error` 1

#### Scenario: A gateway id on the Anthropic SDK
- **WHEN** a node names `model: anthropic.claude-opus-4-1-20250805-v1:0` and runs on the Anthropic SDK
- **THEN** the request's `model` is `anthropic.claude-opus-4-1-20250805-v1:0`

#### Scenario: A slow local reply
- **WHEN** a local endpoint takes 6 minutes to answer a node, with `HIVE_HTTP_TIMEOUT=15m`
- **THEN** the node completes; without the variable it fails with `no reply within 5m0s (the call's TTL)`; with `ttl: 10m` on the node and no variable, it completes

#### Scenario: Two endpoints in one run
- **WHEN** one node sets `provider: local` and another `provider: openai`, with `HIVE_LOCAL_BASE_URL` and `OPENAI_BASE_URL` naming two servers
- **THEN** each server is sent only its node's model, each node row records its own provider and endpoint, and the local server never sees `OPENAI_API_KEY`

#### Scenario: Three nodes on a single-slot local server
- **WHEN** three nodes of one wave each send `http://localhost:11434/v1` a call that takes 3 minutes, with no variable set
- **THEN** chb sends the calls one at a time, and each completes inside its 5-minute TTL

#### Scenario: More slots than the server has
- **WHEN** the same three calls run with `HIVE_MAX_PARALLEL_ENDPOINT=3`
- **THEN** the server queues them, and the third fails with `no reply within 5m0s (the call's TTL)`

#### Scenario: One local server behind two backends
- **WHEN** some nodes run on the OpenAI-compatible backend at `http://localhost:11434/v1` and others on the Anthropic SDK backend at `ANTHROPIC_BASE_URL=http://127.0.0.1:11434`, with no variable set
- **THEN** one call is in flight at a time across both backends

#### Scenario: Two runs on one local server
- **WHEN** `chb ask` runs in two shells against `http://localhost:11434/v1`, with no variable set
- **THEN** one call is in flight at a time across both processes, and each call's TTL starts once it has the slot

#### Scenario: A run that keeps the server busy
- **WHEN** one process sends its calls back to back, and another process's call starts waiting while one of them runs
- **THEN** the waiting call gets the slot next

#### Scenario: A waiter stopped with Ctrl-Z
- **WHEN** a process waiting for a server's one slot is stopped with SIGSTOP, and the process holding the slot then lets go
- **THEN** a call in a third process takes the slot on the 4th poll in a row that finds it free

#### Scenario: Two bounds on one server
- **WHEN** a process with `HIVE_MAX_PARALLEL_ENDPOINT=1` waits for `<h>-0.lock`, which another process holds, and a process with the variable at 2 calls the same server
- **THEN** that call takes `<h>-1.lock` on the 4th poll in a row that finds it free

#### Scenario: A wait behind another run
- **WHEN** a call has waited 5 s for the one slot, which process 4242 holds
- **THEN** stderr shows `endpoint slots: localhost:11434: waiting for a slot other chb processes hold (pid 4242); the call's timeout starts once it has one`, once; if its node's budget ends first, it fails with `no free slot at …: waiting for a slot other chb processes hold (pid 4242): context deadline exceeded`

#### Scenario: The lock directory removed mid-run
- **WHEN** a cache cleaner removes the lock directory between two calls
- **THEN** the second call makes it again and holds a lock file, and nothing is logged

#### Scenario: A run killed while it holds the slot
- **WHEN** a process holding a server's one slot is killed with SIGKILL
- **THEN** its lock file stays, unlocked, and the next call to that server takes it

#### Scenario: A wait cancelled while another process holds the slot
- **WHEN** a call waits for a slot another process holds, and its context is cancelled
- **THEN** it fails at once with the cancellation, while the other process still holds the slot, and the next call finds the queue free

#### Scenario: No lock files
- **WHEN** `HIVE_ENDPOINT_SLOT_DIR` names a path under a regular file
- **THEN** the first call logs `… from this process; other chb processes are not counted (no lock files: mkdir …: not a directory)`, and the process's own calls are still bound

#### Scenario: A CLI run reports usage
- **WHEN** a node runs on the Claude CLI, or on a Gemini CLI whose `--help` lists `--output-format`
- **THEN** its token counts come from the CLI's `--output-format json` reply; a Gemini CLI without the flag reports none, so its tokens read 0 and its calls are unmetered (§ Per-node persistence and cost)

#### Scenario: Many calls on one Gemini CLI
- **WHEN** six calls run at once on one Gemini CLI path
- **THEN** its `--help` runs once, and every call is sent `--output-format json`

#### Scenario: A `--help` that fails once
- **WHEN** a Gemini CLI's `--help` fails on the first call and lists `--output-format` on the second
- **THEN** the first call is sent no `--output-format` and is unmetered, saying `--help` failed; the second and later calls are sent it, and `--help` runs twice in all

#### Scenario: A call that ends while `--help` runs
- **WHEN** a call's context ends while the CLI's `--help` has not answered
- **THEN** the call returns at once with an error; the probe goes on, and a later call uses its answer without running `--help` again

#### Scenario: An agent writes to the run's database
- **WHEN** `chb agent-run --db W/hive.db` dispatches a node whose agent runs `chb db-write finding …`
- **THEN** the agent resolves `chb` to the running binary and the finding lands in `W/hive.db`

#### Scenario: A relative database under --dir
- **WHEN** `chb agent-run --dir X` runs from another directory with the default database `workspace/hive.db`, or with `HIVE_DB_PATH` relative
- **THEN** an agent node's `shell` tool and a command node's program both see `HIVE_DB_PATH` as that path made absolute from the directory `chb` ran in, the database the run opened

#### Scenario: shell prints the environment
- **WHEN** an SDK-backend agent runs `env` through the `shell` tool while `ANTHROPIC_API_KEY` is set in the parent
- **THEN** the output, and the `tool_invocations` row that stores it, holds no `ANTHROPIC_API_KEY`

#### Scenario: A Windows machine with Git Bash on PATH
- **WHEN** an SDK-backend agent calls `shell` with `echo hi` on Windows, where Git for Windows put `bash` on `PATH`
- **THEN** the command runs through `bash -c`, the tool's description says so, and the result is `[shell: bash]`, then `hi`

#### Scenario: A Windows machine without bash, with PowerShell 7
- **WHEN** the same call runs on Windows where no `bash` is on `PATH` and `pwsh` is
- **THEN** it runs through `pwsh -NoProfile -NonInteractive -Command`, and the result is `[shell: pwsh]`, then `hi`

#### Scenario: A Windows machine with cmd alone
- **WHEN** the same call runs on Windows where none of `bash`, `pwsh` and `powershell` is on `PATH`
- **THEN** it runs through `cmd /d /s /c`, given the command line whole, and the result's first line is `[shell: cmd]`

#### Scenario: A Unix machine
- **WHEN** the same call runs on macOS or Linux
- **THEN** it runs through `bash -c`, and the result is `hi` alone

#### Scenario: bash by name
- **WHEN** a model calls `bash` with that command, on Windows or not
- **THEN** the same shell runs, the result is the same, and the `tool_invocations` row names `shell`

#### Scenario: A child that holds the output
- **WHEN** an agent's `shell` call runs `sleep 30 & wait` with a second left on its context, or a Claude CLI or Gemini CLI call's program starts `sleep 30 &` and waits for it
- **THEN** the call returns once its context ends, with the child killed, not after 30 seconds; `sleep 30 & echo started` returns 5 seconds after the shell exits, with `started`

#### Scenario: An Anthropic SDK loop out of turns
- **WHEN** an Anthropic SDK call with a turn cap of 429 is answered with a tool call on every call, the wrap-up's included
- **THEN** after 430 calls it fails with an incomplete reply naming stop reason `max_turns` and the cap, carrying every call's tokens, and the rate-limit retry does not send it again

#### Scenario: A Gemini CLI that fails after its reply
- **WHEN** a Gemini CLI prints an `--output-format json` reply whose `stats.models` report tokens, then exits 1
- **THEN** the node fails naming the exit status and the CLI's stderr, and its row carries the reply's tokens and calls

#### Scenario: A cancelled call on each backend
- **WHEN** a run is cancelled while a call is in flight on any backend
- **THEN** the call fails with an error that wraps `context.Canceled`; a Gemini CLI call's error says `gemini CLI timed out or cancelled`, not `signal: killed`

#### Scenario: A large tool result on each backend
- **WHEN** a model on the Anthropic SDK, on Gemini, or on the OpenAI-compatible backend with no window known, calls `read_file` on a file whose numbered lines run past 50,000 bytes
- **THEN** each backend sends the model the same result: the longest prefix that fits 50,000 bytes with its note, then `[cut: kept <k> of <n> bytes; the rest is past the 50000-byte cap on a tool result. For the rest, call read_file again with offset <line>.]`; each logs the cut, and the call's `tool_invocations` row holds the tool's own output, not the result sent, cut at 20,000 bytes

#### Scenario: A cut-off reply from a local endpoint
- **WHEN** a `provider: local` node's reply is cut off at the output cap
- **THEN** its error starts `local: reply cut off at the output cap`, and none of that backend's errors names `openai`; on a run started with `--provider copilot`, the same reply's error starts `copilot:`

### Requirement: Agent personas

A node's system prompt is its agent's persona, `<agent>.md`. The runner SHALL read it in this order, a definition: with `--agents-dir` given (`Config.AgentsDir` set), `<dir>/<agents-dir>/<agent>.md` and nothing else; with it unset, `<dir>/agents/<agent>.md`, else the shipped `agents/<agent>.md` the binary carries (`hive.Agents`, equal to the repository's `agents/` file for file). A persona found nowhere is not an error: the node runs with an empty system prompt and the log says so. `chb agent-run`, `chb-mcp`'s `chb_research` and `chb replicate` leave `AgentsDir` unset.

#### Scenario: No folder on disk
- **WHEN** `chb agent-run workflows/research-deep.yaml` runs with `--dir` naming a directory with no `agents/`
- **THEN** each node's persona is the carried one of the same name

#### Scenario: A folder on disk overrides
- **WHEN** `<dir>/agents/researcher.md` exists
- **THEN** the researcher nodes run with that file, whatever the carried copy holds

#### Scenario: An explicit directory has no fallback
- **WHEN** `--agents-dir personas` is given and `<dir>/personas/researcher.md` does not exist
- **THEN** the researcher node runs with an empty system prompt, logged, and the carried copy is not read

### Requirement: Tool allowlist

A node's `tools:` (workflow.md § Workflow definition) SHALL narrow the tool registry its calls run with to the tools it names, on the dispatch, every `parallel_fan` item and every repair attempt. A node without `tools:` keeps every tool. How each backend honours the list:

| Backend | With `tools: [...]` | With `tools: []` |
|---|---|---|
| Anthropic SDK, OpenAI-compatible, Gemini SDK | sends only those tools' schemas | sends no tools field at all, which Ollama needs for a model without the tools capability |
| Claude CLI | `--tools` names the built-ins kept, `--disallowed-tools` the rest, `--allowed-tools` the kept ones, and `--strict-mcp-config` keeps the user's own MCP servers out. The chb sidecar and `mcp__chb__chb_db_write` come only when `chb_db_write` is kept | `--tools ""`, every built-in disallowed, no `--allowed-tools`, no sidecar |
| Gemini CLI | not honoured: the CLI runs its own tool set and has no per-call flag to narrow it. The runner warns once per process | same |

A model that calls a tool the list removed is refused as calling an unknown tool. Every SDK backend hands the refusal back to the model as an error tool result, and the call goes on: the Anthropic SDK names the tool; the OpenAI-compatible and Gemini SDK backends, through `ToolRegistry.Invoke`, name it and the tools the node has, or say it has none. The refusal is recorded as an error tool invocation. So a lens that calls `bash` under `--lens-tools read` is told so and can answer, rather than failing its node.

#### Scenario: A removed tool is called
- **WHEN** a `tools: [read_file]` node on the OpenAI-compatible backend calls `bash`
- **THEN** its next request ends with a tool message `ERROR: unknown tool: bash; the tools available here are read_file`, and the node goes on to its answer

#### Scenario: A lens with no tools on Ollama
- **WHEN** a `tools: []` node runs on the OpenAI-compatible backend
- **THEN** the request carries no `tools` field

#### Scenario: Read-only tools on the Claude CLI
- **WHEN** a `tools: [read_file, glob, grep]` node runs on the Claude CLI
- **THEN** it is passed `--tools Read,Glob,Grep`, `--disallowed-tools Write,Edit,Bash,WebFetch` and `--strict-mcp-config`, and no `--mcp-config`

### Requirement: Minimum tool calls

An agent node with `min_tool_calls: N` (workflow.md § Workflow definition) SHALL be sent back when its call made fewer than N tool calls, before its answer is checked or merged. A well-formed answer does not save it: the node's prompt asks for work only tools can do, so a call without them reports actions it never took. The shortfall is an `AcceptRejection` with predicate `min_tool_calls` and the count made as its value, and a reason in plain words for the model: the count made, the minimum, that the actions it reports were not taken, and to do them now with its tools and then answer. A node with `on_reject:` hands it to `tryRepair` (§ Accept rejection and repair): `{accept_failure}` is that reason alone, `{outputs}` the answer's parsed outputs, and the repair runs with the node's tools. The node's calls count together: the dispatch's tool invocations and each attempt's. An attempt that brings the count to the minimum goes on to the schema and `accept:` checks and can complete the node; one that leaves it short is rejected the same way, before its answer is checked and with no verdict written to the Comb, and the next attempt is asked again. Each attempt's tool invocations join the node's `tool_invocations` rows, so a node completed by its repair shows the calls that did its work. When repair is exhausted the node is `rejected`, with rationale `accept rejected: min_tool_calls = <count>`. A node with no `on_reject:` fails instead, with an error naming the count, and the failure goes through its `max_retries`, so the prompt can run again; the reply it answered with is kept as the node's `rationale`, since no repair row holds it. Under `min_tool_calls: 1` the short call made no tool call, so a retry or a repair repeats nothing it wrote; under a higher minimum it may have written, and a retry or a repair can write again. The count is every tool invocation a call made. The Claude and Gemini CLIs run their own tool loops and report none of their calls (`RunResult.ToolCallsUnreported`), so on those backends the minimum is not checked, on the dispatch or on a repair, and the runner logs that it was not.

#### Scenario: A repair that does the work
- **WHEN** a `min_tool_calls: 1` node with one repair on the OpenAI-compatible backend answers its schema well-formed with no tool call, and its repair makes one tool call and answers
- **THEN** the node completes with the repair's answer, its `tool_invocations` rows hold the repair's call, the repair's `repair_prompt` carries the reason naming 0 calls made and at least 1 needed with the dispatch's answer, and its `failure_reason` leads with the reason and names `min_tool_calls`

#### Scenario: A repair that still does nothing
- **WHEN** the same node's repair answers again with no tool call
- **THEN** the node is `rejected` after two calls with rationale `accept rejected: min_tool_calls = 0`, the run fails, its answer is in no state, the dispatch's answer is in the `repair_prompt` and the repair's in `repair_outputs_json`

#### Scenario: The dispatch's calls count for its repair
- **WHEN** a `min_tool_calls: 1` node's call makes one tool call and fails `accept:`, and its repair answers with no tool call
- **THEN** the repair completes the node, since the node's calls together made one

#### Scenario: No repair block
- **WHEN** a `min_tool_calls: 1` node without `on_reject:` or `max_retries` answers well-formed with no tool call
- **THEN** the node is `failed` after one call with an error naming 0 calls, its `rationale` is the reply as the model wrote it, and no `workflow_repairs` row exists

#### Scenario: A retry that does the work
- **WHEN** a `min_tool_calls: 1`, `max_retries: 1` node without `on_reject:` makes no tool call on its first call and one on its second
- **THEN** the node completes with the second call's answer

#### Scenario: A CLI backend
- **WHEN** a `min_tool_calls: 1` node with `on_reject:` runs on a backend that reports no tool calls
- **THEN** the minimum is not checked, the runner logs so, and the node completes on its answer with no repair

### Requirement: Model tiers and budget mode

A node SHALL name either `model:` or `tier:`. A tier resolves through the budget mode (`--budget-mode`, else `HIVE_BUDGET_MODE`, else `standard`; `agent-run`, `ask` and `replicate` refuse a name that is no mode; `agent-run --budget-mode` exports `HIVE_BUDGET_MODE`, so the commands a run starts, the hive's `chb hive next` among them, see the mode): `premium` → the tier's premium model, `standard` → its standard model, `cheap` → its cheap model, `free` → its free model. An empty slot degrades — `cheap` to standard then premium, the others to premium — and an unknown tier resolves to no model, leaving the backend's default. Tiers come from the models config (`internal/models/default-models.yaml`, overridden by the user's models config at `HIVE_MODELS_PATH`, else `$XDG_CONFIG_HOME/hive/models.yaml`, else `~/.config/hive/models.yaml`, whose every tier replaces the one of its name whole or is added; `models.Load`). A run's dispatch and `chb models tiers` SHALL resolve a tier through that one config and one rule (`models.Config.ResolveTier`), so `chb models tiers` shows the slot a run sends for each tier and mode. The shipped tiers name Claude models, which the OpenAI and Gemini backends pass through as written and cannot serve. So on the OpenAI-compatible backend, the Gemini SDK and the Gemini CLI, a tier slot that names a model the models config lists in the `anthropic` family SHALL be sent as the models config's alias that names it (`haiku`, `sonnet` or `opus`; the first in name order if several do). A slot of any other family, a model the config does not list, such as a local model a user's tier maps to, or a Claude model no alias names, is sent as written. That rule is a definition, and it holds for an `on_reject` block's `tier:` too. The runner SHALL then resolve the model for the provider that serves the node (its `provider:` override, else the run default) with that backend's alias table (§ Backends). The Claude CLI gets the name as written, since it resolves aliases itself. Each dispatch SHALL record the canonical id of the resolved model in `workflow_node_states.resolved_model`, so it names the latest dispatch; it is also the model whose cap applies (§ Per-turn output cap) and the one costed. For the Claude CLI, the record names the models config's id for the alias. The models config SHALL look a model up by its whole id. It strips a `provider:` prefix only when the prefix is `anthropic`, `openai`, `gemini`, `google` or `ollama`, in any case; any other colon is part of the id.

#### Scenario: A model id with a colon
- **WHEN** `models.yaml` lists `qwen3.5:4b` with a `max_output_tokens` and prices, and a node names `model: qwen3.5:4b`
- **THEN** the node's cap and cost come from that entry, not from a lookup of `4b`

#### Scenario: Cheap mode with no cheap slot
- **WHEN** a node names a tier whose `cheap` slot is empty and the budget mode is `cheap`
- **THEN** the node runs on the tier's standard model, or its premium model if that is empty too

#### Scenario: Tiers in the user's models config
- **WHEN** the user's models config maps the planner tier to models of its own and adds a tier, and a `tier:` node of each tier runs at each budget mode on an injected backend
- **THEN** each node is sent the model `chb models tiers` shows for its tier and mode, the planner's from the user's config

#### Scenario: A tier on an OpenAI or Gemini run
- **WHEN** a `tier: planner` node runs under `--provider openai` at the standard budget mode, where the shipped planner slot is `claude-sonnet-4-6`
- **THEN** it is sent `gpt-5.1`, the OpenAI model for `sonnet`, not `claude-sonnet-4-6`; under `--provider gemini` it is sent `gemini-2.5-pro`; and a tiers config that maps the planner slot to `qwen3.6:35b-a3b` sends that

#### Scenario: A sonnet node on an OpenAI run
- **WHEN** a `model: sonnet` node with no `provider:` runs under `--provider openai`
- **THEN** the backend is sent `gpt-5.1`, the cap is `gpt-5.1`'s 16384, the pin reads `gpt-5.1`, and the cost uses `gpt-5.1`'s prices

### Requirement: Routing profiles

A routing profile SHALL map roles to routes. Profiles live under `profiles:` in the models config (`internal/models/default-models.yaml`, overridden by `~/.config/hive/models.yaml`, where a profile replaces the shipped one of its name whole). A run takes one through `--profile` on `chb ask`, `agent-run`, `preflight`, `review`, `implement`, `proof` and `agent-harness`, else `HIVE_PROFILE`. The runner reads the variable itself when the caller names no profile, so the runs chb-mcp's `chb_research` and `chb replicate` start in-process take it too. `--profile` exports `HIVE_PROFILE`, so the commands a run starts, the hive's `chb hive next` among them, run under it too. The roles are `scope`, `lens`, `evaluate`, `followup`, `queen`, `hive-research`, `hive-synthesis`, `review-lens`, `review-synthesis`, `implement-plan` and `implement-fix`, and the repair role `implement-repair` (`models.Roles`, `models.RepairRoles`). A route names a `model`, and may name a `provider`, a `reasoning` level, `tools` and a `ttl`. A profile may also set a `default:` route of the same shape, for the model nodes no role route serves (below). The profile's `provider:` serves a route that names none, and is the run default unless `--provider` is given (§ Backend selection). `because:` records the evidence for a route that leaves this machine. `quality:` says how far the profile has been measured.

`ResolveProfile` SHALL refuse a profile, naming each role and problem, when a role is not one of the roles, a route names no model, no provider or one that is no provider kind, a reasoning level outside `none|low|medium|high`, a tool outside `workflow.ToolNames` or a `ttl` that is not a positive Go duration. It SHALL also refuse a route or profile key that is none of its own (`models.RouteKeys`, `models.ProfileKeys`), so a misspelled `reasoning:` is refused, not dropped. It SHALL check the default route as it checks a role's, naming it `default route`, and refuse a default route that leaves this machine, with `because:` or without: a call leaves the machine only on a role's cloud route. A route is local when its provider is `local` and its model is not an Ollama cloud tag: a tag of `cloud`, or one ending in `-cloud`, which a local Ollama forwards to ollama.com. That every cloud model is tagged so is a bet on Ollama's naming. Every other route is a cloud route, `provider: openai` included, whatever `OPENAI_BASE_URL` names. A cloud route SHALL carry `because:`, the measured evidence that no local model is sufficient for the role. The refusal of an `openai` route without it says that a server on this machine is `provider: local`. That is a definition: the owner's rule is local first, and a cloud model for a role only on evidence. chb checks that `because:` is there, not what it says. A repair runs with its node's provider, reasoning, tools and TTL (§ Accept rejection and repair), so the `implement-repair` route SHALL set a model and no reasoning, tools or ttl, and its provider SHALL be `implement-fix`'s.

`agent-run` SHALL apply the profile to the workflow before anything else (`ApplyProfile`). Each node whose `role:` the profile routes gets the route's `model`, with its `tier:` removed, and its `provider`, and the `reasoning`, `tools` and `ttl` the route sets. What a route leaves out stays as the node declares it. An `on_reject:` block on a routed node that names a routed role gets that route's model. A block that names no role loses its `model` and `tier`, so its repair runs on the routed model. A node that dispatch sends to a backend, an `agent` or a `parallel_fan`, that no role route serves, because it names no role or one the profile does not route, and that names no `model:` and no `provider:` of its own, such as a node that names only a `tier:`, takes the profile's `default:` route the same way, its repair included (`takesDefault`). Any other node runs as written: one that names a model or a provider of its own, and every node no role routes under a profile with no default route. The workflow keeps its key order, so an `output_schema` keeps its declared order. The routed workflow is the one the run row stores. A result the engine's node checks refuse, such as `tools: []` on a node with `min_tool_calls`, is refused naming the profile.

Before any endpoint is asked anything, every run that calls a model SHALL print the routing in its log (`Routing`, `RoutingReport`), under a profile or not. A run on an injected backend (a test's) prints it only under a profile. First the profile and its `quality:` note, when there is one. Then one line per role and target: `role <role> → <model> on <provider> at <endpoint>, reasoning <level> [<nodes>]`. A node with no role reads `no role`, and a repair whose block names its own model is listed as `<node> on_reject`. Then each line whose calls leave this machine, with its route's `because:`, or `off this machine: nothing`. Then what reaches the network without calling a model: the model nodes that keep `web_fetch` or `shell` (which runs any command, curl included; `bash` in `tools:` names it), and the command nodes, whose programs chb does not confine. Calls leave the machine when their endpoint is not loopback, when a CLI backend serves them, since the CLI chooses its own host, or when the model is an Ollama cloud tag, whatever endpoint serves it. A resume dispatches the workflow its run row stores, so the routing printed and checked is that one's.

A profile SHALL refuse the run, before the run row is created, when a call would leave the machine on a line whose role has no cloud route in the profile. The refusal names each such line. Such a line is a node the profile does not route, left on a cloud provider or a CLI; a role, or the default route, routed local whose `HIVE_LOCAL_BASE_URL` is on another host; or an Ollama cloud tag on a node the profile does not route. A cloud route lets its own role leave and no other. So one role's evidence never carries another role's calls off the machine.

Under a profile a node runs where it is routed or not at all. The runner SHALL refuse the run, before the run row, when a node's provider cannot serve it: its backend cannot be built, or it is the Anthropic SDK with neither `ANTHROPIC_API_KEY` nor `ANTHROPIC_AUTH_TOKEN` set. The refusal names the provider and its nodes. A node whose backend stops building during the run fails; it does not run on the run default. A resume under a profile SHALL be refused before anything is asked when its run row stores another workflow than the file as the profile routes it, such as a run started without a profile.

`chb preflight` prints the same lines for every workflow that calls a model, with ⚠ for each that leaves. With `--profile` it reads the workflow as the profile routes it and fails the same case. It then asks no endpoint anything. Not governed by any profile: `chb recall` and `chb comb embed` compute embeddings through `HIVE_EMBED_PROVIDER`, outside any run; the network tools and command nodes above reach the network under any profile.

Three profiles ship, built from Bench-0 (Ollama 0.34.4, thinking off, schema enforced at decode): `local-fast` puts qwen3.6:35b-a3b-q4_K_M in every role; `local-small` puts qwen3.5:4b on `lens` and `followup` and qwen3.6:35b-a3b-q4_K_M on every other role; `local-8gb` puts qwen3.5:4b in every role. Each is provider `local`, reasoning `none` and ttl `5m`, with `tools: []` on `scope`, `evaluate`, `queen` and `hive-synthesis`. Each sets a `default:` route on qwen3.6:35b-a3b-q4_K_M (`local-fast`, `local-small`) or qwen3.5:4b (`local-8gb`), reasoning `none` and ttl `5m`, which leaves a node its own tools. So every shipped workflow routes under each: the ones whose nodes name only a tier, `research-deep` that `chb_research` runs among them, run on the default route. Each says in `quality:` what was measured and when: Bench-1 (2026-10-01, four seeds, closed roster questions) for `local-fast` and `local-small`, and the 2026-09-29 quick screen for `local-8gb`, which Bench-1 did not run. Each line gives the swarm's score beside one call of the same model, and whether the quorum is calibrated. Bench-0 measured the shape and speed of single calls, not answer quality and no tool loop. The shipped config's comment shows how to add a cloud fallback for one role with `because:`.

#### Scenario: A profile on chb ask
- **WHEN** `chb ask --profile local-small --scope` runs against a server at `HIVE_LOCAL_BASE_URL`
- **THEN** the scope, evaluator and Queen calls carry qwen3.6:35b-a3b-q4_K_M and every lens and follow-up call qwen3.5:4b, each with `reasoning_effort: none` and no tools, and the log says nothing leaves the machine

#### Scenario: A node the profile does not route
- **WHEN** `agent-run --profile local-fast` runs a workflow with a node on `provider: anthropic` that names no role
- **THEN** it is refused naming that node and `https://api.anthropic.com is not this machine`, and no run row is written

#### Scenario: A workflow whose nodes name no role
- **WHEN** `chb preflight workflows/proof.yaml --profile local-fast` runs, whose nodes and repair name only `tier: worker`
- **THEN** every node and the repair are routed to qwen3.6:35b-a3b-q4_K_M on `local`, reasoning `none`, and nothing is refused

#### Scenario: A default route off this machine
- **WHEN** a user's profile sets `default: {model: claude-opus-4-8, provider: anthropic, because: "..."}`
- **THEN** every command that takes the profile refuses it, naming `default route`: it stays on this machine

#### Scenario: A cloud role without evidence
- **WHEN** a user's profile routes `queen` to `provider: anthropic` with no `because:`
- **THEN** every command that takes the profile refuses it, naming `role queen`

#### Scenario: A local endpoint on another host
- **WHEN** `HIVE_LOCAL_BASE_URL=http://10.1.2.3:11434/v1` and a run takes `--profile local-fast`
- **THEN** it is refused: that endpoint is not this machine

#### Scenario: A cloud route carries only its own role
- **WHEN** a profile routes `queen` to `provider: anthropic` with `because:`, every other role to `local`, and `HIVE_LOCAL_BASE_URL` names another host
- **THEN** the run is refused naming the lens, evaluate and follow-up lines, and not the Queen's

#### Scenario: A routed node whose backend cannot be built
- **WHEN** `HIVE_HTTP_TIMEOUT=bogus` and `agent-run --profile local-fast --provider anthropic` runs a workflow with a lens node
- **THEN** it is refused naming `provider local` and the lens node, no run row is written, and nothing is sent to the Anthropic API

#### Scenario: HIVE_PROFILE in chb-mcp
- **WHEN** chb-mcp runs with `HIVE_PROFILE=local-fast` and `chb_research` starts the hive
- **THEN** the hive's model nodes are routed by local-fast, and the run log prints the routing

#### Scenario: A misspelled route key
- **WHEN** a user's profile routes `lens: {model: qwen3.5:4b, reasonig: none}`
- **THEN** every command that takes the profile refuses it, naming `role lens` and `reasonig`

### Requirement: Per-turn output cap

Every backend call SHALL carry a per-turn output cap resolved as: `--max-output-tokens` on `chb ask` / `chb agent-run`, else `HIVE_MAX_OUTPUT_TOKENS`, else the resolved model's `max_output_tokens` in the models config, else 8192. The SDK backends send it (`max_tokens`; `max_completion_tokens` and `max_tokens`; `generationConfig.maxOutputTokens`). The OpenAI-compatible backend sends less when the model's context window leaves less room (§ Context window guard). The Anthropic SDK backend streams each turn, because `anthropic-sdk-go` refuses a non-streaming call whose `max_tokens` is above 21,333. Its request TTL (5 minutes, or 15 for a `parallel_fan` item) SHALL bound the wait for each stream event, not the whole turn, so a turn that writes its full cap is bounded only by the node's timeout; a stream that sends nothing for the TTL fails the turn. The CLI backends do not pass it — the `claude` CLI has no `--max-tokens` — and either CLI backend warns once per process that the cap has no effect there. A turn cap is separate: a request carrying one reaches the `claude` CLI as `--max-turns`, and the Anthropic SDK backend caps at 30 turns by default. Nothing in dispatch sets a turn cap today, so each backend's own default stands.

Models config caps: `claude-opus-5-5`, `claude-opus-5`, `claude-opus-4-8`, `claude-opus-4-6`, `claude-opus-4-5-20250514` 32768; `claude-sonnet-5-5`, `claude-sonnet-5`, `claude-sonnet-4-6`, `claude-sonnet-4-5` 65536; `claude-haiku-4-5` (and dated) 8192; `gemini-2.5-pro`, `gemini-2.5-flash` 65536; `gemini-2.5-flash-lite` 16384; `gpt-5.1-pro` 32768; every other listed OpenAI model 16384.

#### Scenario: A flag beats the model default
- **WHEN** `chb ask … --max-output-tokens 4096` runs a forager on `claude-sonnet-4-6`
- **THEN** each call is capped at 4096 tokens

#### Scenario: A sonnet node on the Anthropic SDK at its full cap
- **WHEN** a `model: sonnet` node runs on the Anthropic SDK with no flag or variable setting the cap
- **THEN** each turn is a streamed call with `max_tokens` 65536

### Requirement: Reasoning level

A node's `reasoning:` (workflow.md § Workflow definition) SHALL be sent as `reasoning_effort` by the OpenAI-compatible backend, on the dispatch, on each repair attempt and on the constraint probe, to a model that can take it. An unset level sends no field. The other backends send nothing for it: the runner has no mapping from these levels to Anthropic's or Gemini's thinking settings, and the CLIs take no such flag. For each dispatch of such a node on another provider, the run log says `node <name>: reasoning <level> has no effect on provider <provider>`.

Which model can take the field is asked of the endpoint, once per endpoint and model in a run: `POST /api/show` on the base URL without `/v1`, which Ollama answers with the model's `capabilities`. Only a base URL ending in `/v1` is asked. The answer is kept for the run for any HTTP answer below 500; a request that got no answer, or a 5xx, is asked again on the next call. Calls that race to a model not yet asked wait for one answer. Each run asks again, so a long-lived process (`chb-mcp`, `chb replicate`) sees a model pulled again between runs. Then:
- `capabilities` lists `thinking`: the level is sent as set.
- `capabilities` lists no `thinking`: no level is sent. Ollama turns `none` into thinking off and the other levels into thinking on (openai/openai.go, server/routes.go), so it answers `low`, `medium` or `high` on such a model with HTTP 400 `"<model>" does not support thinking` (measured on 0.34.4 for `ministral-3:8b` and `devstral-small-2:24b`). Such a model does not think whatever is sent.
- The endpoint does not answer in that shape (another server, an older Ollama, a model it does not hold): the level is sent as set, `none` included. `none` is the one level that turns a thinking model's default thinking off. On Ollama 0.34.4, Bench-0 found Qwen-family models enforce a schema at decode only with thinking off. So a node that sets `none` gets it wherever the server honours it. A server that refuses the field fails the call with its own error, and a node that wants no field sets no level.

The first call, per endpoint and model in a run, whose level is not sent logs `reasoning: <endpoint> reports no thinking capability for "<model>" …`. With no level sent, Ollama turns thinking on for a model that has the capability, such as qwen3.5 and qwen3.6. What another server does with a level it is sent is not checked. Whether LM Studio honours `reasoning_effort`, and whether vLLM or OpenAI accept `none`, are not known.

What each backend does with a model's reasoning:
- **OpenAI-compatible.** It reads a reply's reasoning from `reasoning`, the field Ollama returns it in, and from `reasoning_content`, the field other OpenAI-compatible servers use. On a tool-call turn it SHALL send the reasoning back on the assistant message it echoes, in the field the server returned it in, so a model that needs its reasoning across tool calls, as gpt-oss, GLM and North Mini Code do, keeps it. The reasoning of a final reply is not sent back, including on the finalize call, and is not stored. A final reply with no text but reasoning in either field fails, naming the reasoning's size (§ Incomplete replies fail). The constraint probe reads it the same way.
- **Anthropic SDK.** It sets no thinking option, so the API returns no thinking blocks, and it echoes text and tool calls only.
- **Gemini.** It sets no thinking option. On a function-call turn it SHALL send back every part of the model's reply exactly as it came, in order, each with its `thoughtSignature` and any field chb does not read. The reply of the last turn is not sent back, since the call ends there. Google's thought-signatures page (ai.google.dev/gemini-api/docs/generate-content/thought-signatures, last updated 2026-09-04, read 2026-09-28) says a thinking model returns a signature on the first `functionCall` part of each step, and that a signature must go back in the part it came in. It says Gemini 3 refuses a request with HTTP 400 when the first `functionCall` part of a step in the current turn lacks its signature (`Function call <name> in the <n>. content block is missing a thought_signature`), and that Gemini 2.5 does not check. The tests check this against a fake that applies that rule. It has not been checked against the live API, which needs a paid call. A call's output tokens SHALL be its `candidatesTokenCount` plus its `thoughtsTokenCount`. Google's API reference (ai.google.dev/api/generate-content, UsageMetadata, last updated 2026-09-23) gives `totalTokenCount` as prompt + thoughts + candidates, so the candidates leave the thoughts out. Its Thinking guide (ai.google.dev/gemini-api/docs/thinking, last updated 2026-09-25) prices a response as its output tokens plus its thinking tokens, and lists `gemini-2.5-pro` and `gemini-2.5-flash` as thinking by default. So a thinking model's thoughts are priced as output.
- **Claude CLI and Gemini CLI.** Each keeps its own history.

#### Scenario: Reasoning on two providers
- **WHEN** a node with `reasoning: low` runs on the OpenAI-compatible backend with a model the endpoint says can think, and again with `provider: anthropic`
- **THEN** the first request carries `"reasoning_effort": "low"`; the second carries none, and the run log names the level and the provider

#### Scenario: Reasoning across a tool call
- **WHEN** a local model returns a tool call with reasoning in `reasoning`, or in `reasoning_content`
- **THEN** the next request's assistant message carries that reasoning in the same field, and not in the other

#### Scenario: Gemini thought signatures across a tool loop
- **WHEN** a Gemini 3 model answers with two parallel function calls, the first carrying a `thoughtSignature`, then with text and one more signed call, then with its answer
- **THEN** each later request carries each earlier model turn's parts exactly as they came, signatures included, and the call completes instead of failing with HTTP 400

#### Scenario: Gemini thought tokens
- **WHEN** a Gemini reply's `usageMetadata` counts 40 candidate tokens and 1,500 thought tokens
- **THEN** the call's output tokens are 1,540, and its cost prices all of them as output

#### Scenario: A model that cannot think
- **WHEN** a lens on `ministral-3:8b` sets `reasoning: low`, and Ollama's `/api/show` lists no `thinking` for it
- **THEN** no call to it carries `reasoning_effort`, `/api/show` is asked once, and the run log says so once; a second run in the same process asks, and says so, once more

#### Scenario: A server that does not report capabilities
- **WHEN** a node sets `reasoning: none` on an endpoint whose `/api/show` answers 404
- **THEN** its calls carry `"reasoning_effort": "none"`; with `reasoning: low` they carry `"low"`

### Requirement: Output extraction

The runner SHALL read a node's outputs from its final text with `workflow.ExtractJSONOutput`, in order: the whole text after stripping a leading ```` ``` ```` fence; the widest span from the first `{` to the last `}`; the last balanced `{...}` object. When none parses, the outputs are `{"final_text": <text>}`. For a node with an `output_schema`, those outputs are then checked against it, on every backend (workflow.md § Output schema): a reply with no JSON object is rejected, not stored under a declared output. For a node with a forager name, the forager verdict SHALL be written to the Comb before the node is marked completed, so a downstream `cites` forager that sees `completed` also sees the verdict. A reply that breaks the node's `output_schema` SHALL NOT be written, on the dispatch or on a repair attempt: the engine rejects it, and a verdict in the Comb is read by `cites` foragers and can fire the ∇ quorum sensor, which fires each pair once. The log says the write was skipped. A repair attempt that holds the schema writes its own.

#### Scenario: Prose with braces, then a JSON tail
- **WHEN** a reply's text contains `{braces}` in its prose and ends with a JSON object
- **THEN** the outputs are that final object

#### Scenario: A forager verdict outside its schema
- **WHEN** a forager's reply gives `verdict: maybe`, which its schema's enum refuses, and its repair answers `support`
- **THEN** neither `comb_state` nor `comb_revisions` ever holds `maybe`, and `comb_state` holds the repair's reply

### Requirement: Output schema on the wire

A request carries its node's `output_schema` (workflow.md § Output schema) to the backend, on the dispatch, on each `parallel_fan` item and on each repair attempt. The OpenAI-compatible backend SHALL send it as `response_format: {"type": "json_schema", "json_schema": {"name", "schema", "strict"}}` on a call that offers no tools. `name` is the node's name with any character outside `a-z A-Z 0-9 _ -` replaced by `_`, at most 64. `schema` lists its properties in declared order. `strict` is true only when the schema qualifies for OpenAI's strict mode: every object in it sets `additionalProperties: false` and requires every property it declares. OpenAI refuses `strict: true` for any other schema; Ollama and LM Studio ignore the flag.

A call that offers tools SHALL carry no `response_format`, since a llama.cpp server (Ollama and LM Studio run one) cannot constrain decoding to a grammar and parse tool calls in one call. When such a tool loop ends with a final text that breaks the schema, the backend SHALL make one finalize call: the conversation so far, the answer as an assistant message, and a user message naming the violations and asking for the JSON object alone, with no tools and the schema set. A complete reply replaces the answer. A finalize call refused as a rate limit (§ Rate limiting) returns its error, as any other turn's does, so the rate-limit retry runs the node's call again, tool loop included. When the finalize call fails any other way, or its reply is cut off, empty or calls a tool, the answer stands and the log says `the finalize call gave no answer`, and why. The answer then fails its schema check, so `on_reject:` repairs it as it would with no finalize call; such a failure never fails the node. Its usage is charged either way. A final text that already holds the schema stands, with no extra call. That is a choice: a finalize call after every tool loop would record `enforced at decode` (§ Constraint probe) more often, for one more call per node, and adds nothing to a shape the Go check already holds. A node with `tools: []` (§ Tool allowlist) offers none, so its first call carries the schema; every swarm node has that unless `--lens-tools` gives the lenses tools. A node that keeps any tool gets the schema only through the finalize call, so its first answer that holds the schema records `post-hoc only`, even on a server that enforces schemas. The Gemini, Anthropic SDK and CLI backends send no schema. On every backend the answer is checked against the schema after the call.

The schema's declared order reaches decoding only in part. llama.cpp's json-schema-to-grammar writes an object's required properties first, in declared order, and then its optional ones, in declared order. Ollama 0.30.10 hands the schema to llama-server unchanged on its `/completion` path. On its native-chat path, which serves a GGUF model with no Ollama parser, it decodes the schema into a Go map and encodes it again, so the properties reach the grammar sorted. The probe measures which one a server does (§ Constraint probe).

#### Scenario: A tool loop's answer that breaks the schema
- **WHEN** a schema'd node's first call, which offers tools, answers in prose
- **THEN** a second call carries the conversation, the prose answer and the schema as `response_format`, and no tools, and its reply is the node's answer

#### Scenario: Property order on the wire
- **WHEN** a schema lists `zeta`, `alpha`, `mid` and a call offers no tools
- **THEN** the request body holds the properties in that order

#### Scenario: A finalize call cut off at the cap
- **WHEN** a schema'd node with `on_reject:` answers in prose and its finalize reply ends with `finish_reason: length`
- **THEN** the node does not fail: the prose is rejected with `output_schema`, the log names the finalize failure, and the repair runs

#### Scenario: A finalize call refused as a rate limit
- **WHEN** a schema'd node's tool loop answers in prose and its finalize call gets HTTP 429, then answers on the retry
- **THEN** the rate-limit retry runs the node's call again, and the retry's finalize reply is the node's answer

### Requirement: Constraint probe

At `OPENAI_BASE_URL` when it is set, and at `HIVE_LOCAL_BASE_URL` whenever a node runs on the local provider, the runner SHALL send one probe per distinct (endpoint, model, reasoning) that a node with an `output_schema`, or that node's `on_reject:` repair, sends on the OpenAI-compatible backend. The probes run after the endpoint model preflight, so every model probed is one the endpoint serves. They run after the run row is created and its `workflow run ID:` line logged, and before the first dispatch. So a caller waiting for the run ID gets it first: an MCP spawner waits about 2.5 s, and on a local endpoint a cold model load and up to 1024 tokens can take minutes per probe. Each probe is logged as it is sent, `constraint probe: asking <model> (reasoning <level>) for <nodes>`. Models resolve as the model preflight resolves them (§ Endpoint model preflight).

The probe asks for two sentences of prose. It sends a schema that allows only `{"probe": "hive-constraint-ok", "order": "second"}`, with `strict: false`, the node's reasoning level as a node's call sends it (§ Reasoning level), and an output cap of 64 tokens when no thinking comes first, that is when it sends `none` or the endpoint reports the model cannot think, else 1024, room for a thinking model to finish thinking. Both keys are required and declared out of alphabetical order, so a grammar fixes their order and the reply shows it. The runner reads the reply as `enforced` when it is exactly that object, in either key order; `not enforced` when it is anything else whose first character is not `{`, or a complete object that is not that one; and `no answer` when the call failed, the reply was empty (a thinking model's reasoning alone included) or an object was cut off at the cap. An `enforced` reply also names its key order: `declared order` when `probe` comes first, `keys sorted` when `order` does. Each result is logged, `constraint probe: <model> (reasoning <level>): <outcome>[, <order>] — <detail> [<nodes>]`. A probe's tokens and cost are charged to the run and count toward `--max-cost-usd`, as one metered or unmetered call. No node row carries them; the run's `workflow_runs` row does (`probe_tokens_in`, `probe_tokens_out`, `probe_cost_usd_x10000`, `probe_metered_calls`, `probe_unmetered_calls`), and every run total adds them to its nodes' (§ Per-node persistence and cost). A probe never refuses the run. A dry run sends none. `chb preflight` sends the same probes and says it measures the server: ✓ for `enforced` in `declared order`, ⚠ otherwise, and that a node offered tools carries its schema only on a finalize call.

Each node with an `output_schema` that completes after a call SHALL record `schema_enforcement`: `enforced at decode` when its accepted text (a passing repair's, when one passed; every joined item's, for a fan) came from a call that sent the schema, to an (endpoint, model, reasoning) the probe showed `enforced`; else `post-hoc only`. The run log names the reason: the backend sends no schema, the answer came from a call that offered tools, no probe ran, or what the probe found, with its key order. A node that completes with no call, an empty fan or a dry run, records none: no answer was checked. A node completed from outside the runner, through `chb workflow complete`, records none either: `CompleteNode` checks its answer against the schema, but chb sent no call, so it cannot say how the schema held. Either way the answer held the schema: the record says how it was guaranteed, not whether it is right. `GetWorkflowNodeStates` carries it, and the artifact names it per forager and for the synthesizer (swarm.md § Deterministic artifact).

The record is an inference from the probe, not an observation of the node's own call. The probe is a bet that only constrained decoding turns a prose prompt into its object. A server that showed the model the schema without enforcing it, and a model that followed it, would read as `enforced`. It sends `strict: false`, the weakest form a node's schema is sent in, on the bet that a server enforcing that also enforces `strict: true`. On Ollama 0.30.10 the record can be wrong both ways. For a thinking-capable model that has a built-in Ollama parser, or whose thinking is on, 0.30.10 withholds the format until the reply has streamed thinking and begun its answer (server/routes.go:2749, 2806). So such a model with a built-in parser and thinking off is never constrained, and neither is a reply that streams no thinking, yet a node can record `enforced at decode` from a probe that did stream thinking. Ollama 0.34.4 has no such gate: its chat route passes the format to the model runner on every call (server/routes.go:2842), and a probe on 0.34.4 found `qwen3.5:0.8b` at `reasoning: none` enforced. On both versions, with `reasoning` unset, Ollama turns thinking on for a thinking-capable model (routes.go:2600 on 0.30.10, 2711 on 0.34.4). Then the probe's 1024-token cap can end in reasoning alone: `no answer`, and `post-hoc only`, though the node's own calls, capped higher, may have been constrained. The Go check holds the shape either way; only the label can be wrong.

#### Scenario: A server that constrains decoding
- **WHEN** Ollama answers the probe for `ministral-3:8b` with `{"probe":"hive-constraint-ok","order":"second"}`, and a forager node on it gets its answer from the finalize call
- **THEN** the node records `enforced at decode`, and the log says `declared order`

#### Scenario: A server that sorts the keys
- **WHEN** the probe comes back `{"order":"second","probe":"hive-constraint-ok"}`
- **THEN** it reads `enforced, keys sorted`, the node still records `enforced at decode`, and `chb preflight` warns that properties reach decoding in alphabetical order

#### Scenario: Probes on a slow local endpoint
- **WHEN** an MCP tool spawns a run whose probe takes a minute on a cold model
- **THEN** `workflow run ID:` is logged before the probe is sent, so the tool returns the run ID

#### Scenario: A thinking model that never answers the probe
- **WHEN** the probe for `qwen3.5:4b` at `reasoning: low` comes back with reasoning and no content
- **THEN** it reads `no answer`, the run goes on, and that model's schema'd nodes record `post-hoc only`

#### Scenario: Nodes sharing a combination
- **WHEN** two schema'd nodes send `m1` at `reasoning: none`, and a third sends `m1` with no level
- **THEN** two probes are sent

### Requirement: Incomplete replies fail

A backend SHALL return an error, not an answer, for a final reply the provider cut off at the output cap: `finish_reason` `length` on the OpenAI-compatible backend, `MAX_TOKENS` on Gemini, stop reason `max_tokens` on the Anthropic SDK and the Claude CLI. It SHALL do the same for a final reply with no text, or only whitespace, and no tool call. The Gemini CLI reports no stop reason, so only its empty output is caught, and, from gemini-cli 0.42.0, what its `--output-format json` reply says. That reply carries `warnings` when the CLI stopped or held back the agent: `Loop detected, stopping execution`, `Maximum session turns exceeded`, or a hook that stopped or blocked it. It carries an `error` of type `INVALID_STREAM` when the model's stream ended with no usable reply: empty, cut off at the output cap, or blocked. Both come with the reply's `response` and `stats`, and the CLI exits 0 (packages/cli/src/nonInteractiveCli.ts at v0.42.0 and v0.61.0, read 2026-09-28; v0.41.0 has neither). From gemini-cli 0.54.0, an `INVALID_STREAM` error whose message begins `Model response was truncated because it exceeded the token limit.` is a stream that ended at `MAX_TOKENS` with no text: packages/core/src/core/geminiChat.ts raises it, and the message is `MAX_TOKENS_EXCEEDED_SUGGESTION` in packages/core/src/utils/constants.ts (v0.54.0 and v0.61.0, read 2026-09-28; v0.53.0 has neither). A reply that stopped at `MAX_TOKENS` with text is not an error there, so the JSON reply shows nothing of it. Such a reply is an incomplete reply, and the error lists the warnings or gives the `error`'s message. Any warning fails it, since the CLI gives no other sign of which warnings left the answer whole. The Anthropic SDK, OpenAI-compatible and Gemini backends SHALL also return an error when a node's tool loop uses all its turns (30) with the model still calling tools and its wrap-up call gives no answer, with stop reason `max_turns` and the turn cap (§ Backends). The error names the stop reason, and for a cut-off reply the cap chb sent. The Claude CLI is sent no cap, so its cut-off error names none. On the OpenAI-compatible backend, an empty reply whose `reasoning` or `reasoning_content` holds text also says how many bytes of reasoning it held: a thinking model that never closed its reasoning. The call's usage comes back with the error, so its spend is charged (§ Per-node persistence and cost). The OpenAI-compatible, Gemini and Anthropic SDK backends SHALL also count each call the provider stopped at the cap, on any turn, including a tool loop's finalize call, and the node's row adds the count to `cutoff_calls` (§ Per-node persistence and cost). The Claude CLI reports the stop reason of its final reply only, so it counts that reply; a turn it cut off inside its own loop is not seen. The Gemini CLI reports no stop reason. It counts one call for a reply whose `INVALID_STREAM` error says the response was truncated at the token limit, and none for any other reply, since one cut off with text left shows no sign of it. That one is a lower bound. gemini-cli makes up to 4 attempts at a stream that ends in any `InvalidStreamError`, this one included (`MID_STREAM_RETRY_OPTIONS` and `isRetryableContentError` in packages/core/src/core/geminiChat.ts at v0.61.0, read 2026-09-28), and the reply does not say how many of them ended at the cap. So the bench still reads a Gemini CLI run's cut-off count as unknown (bench.md). Warnings count none: a loop or the session's turn cap is not the output cap, as a tool loop that uses all its turns is not on the other backends. The node then fails like any backend error, and `max_retries` applies. These errors are never read as rate limits (§ Rate limiting), whatever numbers their text holds, so they are not retried. A `parallel_fan` item with such an error is a failed item, and its text is not joined.

#### Scenario: A reply cut off at the cap
- **WHEN** a local endpoint answers a node with `finish_reason: length`
- **THEN** the node is `failed`, its error names `stop reason length` and the cap, and its row carries the call's tokens and `cutoff_calls` 1

#### Scenario: Cut-off calls a node survives
- **WHEN** two of a fan's four items are cut off at the cap, or a node's first repair attempt is and its second passes
- **THEN** the node completes, and its row's `cutoff_calls` is 2, or 1

#### Scenario: A model that never stops calling tools
- **WHEN** a local model answers every call of a node with a tool call, the wrap-up's included
- **THEN** after 31 calls the node is `failed`, its error names `max_turns` and the wrap-up, and its row carries the tokens of all 31 calls

#### Scenario: A model that answers on its wrap-up
- **WHEN** a local model answers every call that offers tools with a tool call, and the wrap-up call, which offers none, with an answer
- **THEN** the node completes with that answer after 31 calls, its row carries the tokens of all 31, and the run log says a wrap-up call gave the answer

#### Scenario: A cap that contains 429
- **WHEN** a reply is cut off under `--max-output-tokens 4290`
- **THEN** the node fails naming the cut-off, after one call, not as a rate limit

#### Scenario: A Gemini CLI reply with warnings
- **WHEN** a Gemini CLI's JSON reply carries `warnings: ["Loop detected, stopping execution"]`, or an `error` of type `INVALID_STREAM`, with its `response` and `stats`
- **THEN** the node fails naming the warning or the error, not as a rate limit, and its row carries the calls and tokens the stats report

#### Scenario: A Gemini CLI reply truncated at the token limit
- **WHEN** a Gemini CLI's JSON reply carries an `INVALID_STREAM` error whose message is `Model response was truncated because it exceeded the token limit. Try using /compress to free up context space.`
- **THEN** the node fails naming it, not as a rate limit, and its row's `cutoff_calls` is 1; a reply with warnings leaves it at 0

### Requirement: Context window guard

The OpenAI-compatible backend, on provider `openai` and on provider `local`, SHALL hold every call to the model's context window when one is known: the dispatch, each tool-loop turn, the finalize call, each `parallel_fan` item and each repair. A local server drops the start of a prompt longer than its window and answers anyway. Measured on Ollama 0.34.4: an 11,317-token prompt sent at `num_ctx` 4096 came back counted as 2,050 prompt tokens, with no error, and the model began a report.

The window SHALL be the smaller of two bounds, each applying when known: the model's `context_window` in the models config, and the window the server reports it runs the model with. The runner asks each endpoint, `OPENAI_BASE_URL` for provider `openai` and `HIVE_LOCAL_BASE_URL` for provider `local`, once per run which server it is, by the API it answers beside `/v1`:
- Ollama, when `GET /api/ps` answers with a `models` list. The window is the `context_length` it reports for the loaded model. A model it does not list yet is loaded first, within an endpoint slot, by `POST /api/generate` naming only the model; the node's own call would load it anyway. `/api/show` is not read: it reports the length the model was trained for, not the window it runs with. When Ollama reports no `context_length`, `OLLAMA_CONTEXT_LENGTH` in chb's environment stands in for it. It never overrides a reported window, since chb's environment is not the server's.
- LM Studio (0.4 and later), when `GET /api/v1/models` answers with a `models` list. The window is the `context_length` of the model's loaded instance, the smallest when it has several; the model is matched by its key, a variant or an instance id. A listed model with no instance loaded is loaded first by `POST /api/v1/models/load` naming only the model. An unlisted model has no window.
- llama.cpp, when `GET /props` reports `default_generation_settings.n_ctx`. The window is that `n_ctx`.

Any other server's window is known only from the models config. The window is resolved once per model per endpoint per run, so a model name served at both endpoints has a window at each, and the run log names it, the endpoint and where it came from, or says it is unknown. For an endpoint on this machine or a private network, the unknown line also says to set `context_window` in the models config. A models-config entry meters the model at the price it lists, $0 when it lists none (§ Per-node persistence and cost), and replaces the default entry for that model whole: an entry that adds `context_window` to a model the defaults list must repeat its prices and `max_output_tokens`.

Before a call, once it has its endpoint slot, the runner SHALL estimate the prompt's tokens. The text is every message's content and tool calls, and the tool schemas. The reasoning of an assistant turn after the last user message is estimated too, in `reasoning` or `reasoning_content`, whichever the backend echoes it back in (§ Reasoning level), since a template may render it: qwen3.5 renders it inside a tool loop and drops it once a user message follows. The estimate counts text by class: each ASCII digit, punctuation mark or symbol is a token, as is each run of whitespace but a lone space before a letter or a mark; a run of letters is a token per 4 ASCII letters and per 2 two-byte letters (Latin with diacritics, Greek, Cyrillic), rounded up, and a token a letter when it touches a digit; any other two-byte character is a token, and a wider one (CJK, Indic scripts, emoji) 1.5. The chat template adds 64 tokens, 8 a message, 512 when the request offers tools and 768 when it has no system message. Each number is a choice, set at or above what qwen3.5 and ministral-3 counted on fifteen kinds of text: English, German, French and Spanish prose, Go, YAML, JSON, minified JSON, CSV, hex hashes, a Markdown table, Russian, Japanese and Chinese. CSV counted a token a byte, and the estimate matched it; the others it over-estimated by up to 2.5 times (Chinese on qwen3.5). On HIVE's swarm prompts it over-estimates by about 1.4 times. The server's counts calibrate the estimate of that model at that endpoint, from prompts of at least 2,048 bytes. A count above 90% of the estimate raises the model's later estimates, so the densest count on it stays 10% under them. Once three counts on the model have passed the check after a call (below), a densest count under 90% lowers its later estimates the same way: to the class count times the densest count per estimated token, over 0.9. It does so only at an endpoint the runner found to be Ollama, LM Studio or llama.cpp (below). A count the check fails can raise the estimate but does not count toward the three: a server that cut a prompt counted only part of it. The counts are kept per run, per endpoint and model, not per backend: a node that sets `provider:`, as every node a routing profile routes does, and each repair build their own backend, and a count still carries to every later call on that model at that endpoint in the run. A model name served at both endpoints keeps a count at each.

A call SHALL be refused unsent when its estimate reaches the window, or leaves less room for the reply than 1,024 tokens, or than the call's cap when that is smaller. It SHALL be refused too when its class count reaches the window. That class count is raised when a count above 90% raised the estimate, and is never lowered. So a lowered estimate sets only the room for the reply and the cap, and the prompt is held to the window at its class count. The node fails naming the estimate, the bytes, how it was estimated, the window and where it came from. A refusal for the reply's room names the room too. A refusal for the class count names the class count and the lowered estimate. Otherwise, when the call asks for more output than the window leaves, its cap is lowered to the window less the estimate. While the estimate is at least the server's count, the reply cannot run past the window. A lowered estimate can be under it, and then the reply can (below). A reply cut off at a lowered cap fails as § Incomplete replies fail says, and the error names the lowering.

A tool result SHALL be held to the room the next call leaves it, before it joins the conversation. The room is half of what that call's prompt, with the result blank, leaves under the window less the reply floor at the estimate's scale, and under the window at the class count's bound. Both are the numbers above; the result adds no estimate of its own. A result within the room is sent as it is. A longer one is cut at a character boundary so that what is kept and a note fit the room, and ends with the note: how many bytes of how many were kept, that the rest would not fit the context window with room for the reply, and how to get the rest when the tool has a way. `web_fetch` is called again with the same `url` and the `offset` the note names; `read_file` with the `offset` the note names; `shell`, under either of its names, with a narrower command. Every tool's result is held, since `shell` and `read_file` return as much as `web_fetch`. Half, so that a loop's results never fill the room by themselves: each takes at most half of what the ones before it left, and a second page fetched in one loop is cut too, not refused. What this costs: a result that would fit the whole room but not half of it is cut, and a second call gets the rest. A cut is logged with the bytes kept and the bytes the tool returned; `tool_invocations` holds the tool's whole output, cut at 20,000 bytes. Without a window the result is held to 50,000 bytes, as on the backends given none (§ Backends). On a local model with a 65,536-token window (qwen3.6:35b-a3b-q4_K_M), two sqlite.org pages fetched whole, 110 and 140 KiB of HTML, take a hive run's prompt from 11,053 to 261,798 bytes, and its sixth call would be refused at an estimated 81,674 tokens, failing the node after its work was done.

After a call, before its endpoint slot is released, the runner SHALL read the server's count, so a call queued behind it on the same model at the same endpoint estimates with it. It fails the node when the server dropped, or may have dropped, part of the conversation:
- the prompt is counted at 1,024 tokens or more and at fewer than one token per 16 bytes of its text, window or no window. The sparsest text measured, Russian on qwen3.5, counted 9.66 bytes a token, so a count that sparse means part of the prompt is gone, or the window is not the one chb was told.
- the prompt and reply are counted past the window.
- the prompt is counted at half the window or more and above its class count (its estimate, unless the estimate was lowered). A server that drops part of a prompt keeps at least half its window, so such a count cannot show that nothing was dropped.
Past the first rule, a count under half the window shows nothing was dropped, however few tokens a byte. A final reply the server stopped with stop reason `length` short of the cap sent, with the prompt and reply counted at the window or one token under it, was cut by the window, not the cap. llama.cpp stops a reply there when context shift is off, its default. It fails as § Incomplete replies fail says, and the error says the window cut it and names the counts. When a reply runs into the window, by this rule or the second above, and the estimate was under the server's count, the error names the estimate, and says so when counts of earlier prompts lowered it. Each call's estimate, the server's count and the window go to the run log, with the class count the prompt was held to when the estimate was lowered, one line a call, numbered when one dispatch, fan item or repair makes several. A call the server stopped with stop reason `length`, at the cap or at the window, counts in `cutoff_calls` (§ Incomplete replies fail) even when this check then fails it. The guard's errors are never read as rate limits, whatever numbers they hold. The other backends hold no call to a window: the hosted APIs refuse an overflow with an error, and the CLIs build their own prompts. The Anthropic SDK and Gemini backends hold each tool result to 50,000 bytes instead (§ Backends).

Why a lowered estimate does not hold the prompt to the window. The counts are of earlier prompts, and a later prompt can be denser. On Bench-1's N arm (Ollama 0.34.4, qwen3.5:0.8b), the server counted the solo control at 0.46 to 0.51 of its estimate, the foragers at 0.75 to 0.77, the Queen's first call at 0.71 to 0.76 and its repairs at about 0.68. The solo prompt is short and has no system message, so 768 of its 1,600 to 1,800 estimated tokens are the allowance for the system prompt ministral-3 adds. CSV, as in a tool result or a context pack, is counted at its class count. If the lowered estimate held the prompt, three counts at 0.52 would let a CSV prompt of 5,000 tokens, by class and by count, into a 4,096-token window at an estimate of about 2,890. Ollama would cut it to about 2,050 tokens and answer. That count is under the estimate, so the check after the call would pass it.

Holding the prompt at its class count costs this: a prompt whose class count reaches the window is refused, though the lowered estimate leaves room for the reply. Those are the class counts from the window up to the window less the reply's floor, over the lowered scale. At the foragers' densest count, 0.774, and a call asking for 1,024 tokens or more: none in a 4,096-token window, where the rule for the reply's room refuses first; 8,192 to 8,334 in an 8,192-token window, about 2% of it; 16,384 to 17,860 in a 16,384-token window, about 9%. A call asking for fewer tokens has a lower floor, so its band is wider: at a cap of 256, class counts 4,096 to 4,465 in a 4,096-token window.

Which calls gain. The counts are kept for one run, and the estimate falls only after the third count of a prompt of 2,048 bytes or more. So only a run's later calls on the model gain, and only when the window, not the call, sets their cap. A solo `chb ask` makes one call, so its estimate never falls. On Bench-1's N arm, lowering would change no refusal in a window of 4,096, 8,192 or 16,384 tokens. In a 4,096-token window every balanced forager is refused either way: its class count, 4,038 to 5,493, leaves under 1,024 tokens for the reply, and so does the server's count, 3,090 to 4,152; `--persona-profile lean` is the lever there. The solo control is sent either way. What lowering gives is larger caps: in an 8,192-token window each forager after the third gets 585 to 809 more tokens, and in a 16,384-token window the Queen gets 1,500 to 2,065 more.

What this guarantees rests on the estimate and on how servers cut. A call the guard sends fits its window when its class count, raised as above, is at least the server's count; that it is, on text not measured, is an assumption. A lowered estimate is not part of this, but it removes a margin the rule for the reply's room gave: a call asking for 1,024 tokens or more was sent only when its class count left that much of the window, and once the estimate is lowered it is sent when its class count is under the window. So for a prompt whose class count is within 1,024 tokens of the window, the class count alone stands between it and a cut. When a prompt is denser than the counts that lowered its estimate, its cap can pass the room the window leaves, and a reply that runs into the window fails (above). The estimate is lowered only at Ollama, LM Studio or llama.cpp, since each runs such a call. Ollama 0.34.4 bounds a cap only at ten windows, and llama.cpp checks only the prompt against its window. That LM Studio does too is an assumption: it documents a policy for a reply that runs past the window (stop it, or drop part of the conversation), not a refusal. A server that refuses a call whose prompt and cap together pass the window, as vLLM does, would refuse such a call before counting it, so no count could correct the estimate. The rule for a reply stopped at the window allows one token because llama.cpp stops there; how close LM Studio stops is unknown. A call counted under half the window lost nothing, on the assumption that a server that cuts keeps at least half its window (llama.cpp keeps its first tokens and half the rest; Ollama's measured cut kept 2,050 of 4,096) and that no local server runs a window under 2,048 tokens. A cut in the upper half of the window is caught only when the count exceeds the class count. How LM Studio treats a prompt over its window is unknown.

#### Scenario: A prompt over a small model's window
- **WHEN** Ollama's `/api/ps` reports a 512-token window for a model, and a node's 4,000-byte prompt is estimated above it
- **THEN** the node fails naming the estimate and the 512-token window from `Ollama /api/ps`, and the server gets no chat request

#### Scenario: A tool result larger than the room is cut
- **WHEN** a node in an 8,192-token window calls `web_fetch` on a page whose text is 60 KB, and the next call's prompt without the result is 3,100 tokens by text class
- **THEN** the result sent is cut so that it and its note are at most 2,034 tokens by text class, half of 8,192 less the 1,024 for the reply less 3,100; the note says how many bytes of how many were kept and names the `offset` that gets the rest; the next call is sent, and the run log names the cut

#### Scenario: A second fetch in one loop still fits
- **WHEN** the model then fetches a second page as large
- **THEN** it is cut to half of the room left after the first, which is less, both results are in the third call's prompt, and the loop ends with an answer

#### Scenario: A small result is untouched
- **WHEN** the page's text is 400 bytes
- **THEN** the result is sent whole, with no note

#### Scenario: No room for the reply
- **WHEN** a prompt's estimate leaves 1,023 tokens of the window and the call asks for 8,192
- **THEN** the call is refused unsent, naming the room and the 1,024 a reply needs

#### Scenario: A local node is held to its own server's window
- **WHEN** a node on `provider: local` sends a prompt estimated above the window its Ollama at `HIVE_LOCAL_BASE_URL` reports, and `OPENAI_BASE_URL` names another server
- **THEN** the node fails naming that window from `Ollama /api/ps`, the local server gets no chat request, and the run log names the window at the local endpoint

#### Scenario: A prompt that fits
- **WHEN** the window is 4,096 tokens and a call's prompt is estimated at 1,500 tokens and asks for 8,192 output tokens
- **THEN** the call asks for 2,596

#### Scenario: A count carries across routed nodes
- **WHEN** a routing profile routes two nodes, one after the other, to one model at `HIVE_LOCAL_BASE_URL`, and the server counts the first node's prompt at 1.2 times its estimate
- **THEN** the second node's estimate is its class count times 1.2 / 0.9, though each node built its own backend

#### Scenario: Counts of sparse prompts lower the estimate
- **WHEN** a model at Ollama has a 4,096-token window, three prompts on it have been counted at 0.53 of their class counts, and a prompt of 3,500 tokens by text class asks for 8,192 output tokens
- **THEN** it is sent asking for 2,034 tokens: the window less 3,500 × 0.53 / 0.9, rounded up. Before the third count it was refused, leaving 596 tokens for the reply

#### Scenario: A lowered estimate does not hold the prompt
- **WHEN** after those three counts a prompt is 4,500 tokens by text class, which the counts put at 2,650
- **THEN** it is refused unsent, naming its class count, the window and the lowered estimate

#### Scenario: A prompt too long at the densest count
- **WHEN** after those three counts a prompt is 5,600 tokens by text class
- **THEN** it is refused unsent: at 3,298 tokens it leaves 798 for the reply

#### Scenario: A denser count raises the estimate again
- **WHEN** after those three counts a prompt is counted at 0.85 of its class count
- **THEN** later estimates are the class count times 0.85 / 0.9, and the 3,500-token prompt is refused again, leaving 790 tokens for the reply

#### Scenario: A failed count does not lower the estimate
- **WHEN** two counts at 0.53 have passed and a third is failed as a cut
- **THEN** the 3,500-token prompt is still refused, until a third count passes

#### Scenario: Counts do not lower the estimate at another server
- **WHEN** three prompts on a model at an endpoint that answers only the OpenAI-compatible API, with a 4,096-token window from the models config, have been counted at 0.53 of their class counts
- **THEN** the 3,500-token prompt is refused, leaving 596 tokens for the reply

#### Scenario: A count above the class count after lowering
- **WHEN** after three counts at 0.53 the 3,500-token prompt is counted at 3,675
- **THEN** the node fails naming the 3,500 estimated by text class, and later estimates are the class count times 3,675 / 3,500 / 0.9

#### Scenario: A reply stopped at the window
- **WHEN** after three counts at 0.53 at llama.cpp, the 3,500-token prompt is sent asking for 2,034 tokens, counted at 2,800, and its reply stops with stop reason `length` after 1,295 tokens, one token under the 4,096-token window
- **THEN** the node fails saying the window cut the reply, not the cap, naming the counts and the estimate of 2,062 under them, and `cutoff_calls` counts the call

#### Scenario: Sparse text is not a cut
- **WHEN** a 6,000-byte prompt is counted at 622 tokens in an 8,192-token window
- **THEN** the node completes

#### Scenario: A count that cannot show nothing was dropped
- **WHEN** an 8,192-token window's server counts a prompt at 4,096 tokens, above its estimate
- **THEN** the node fails saying the reply may rest on part of the prompt, and its row carries the call's tokens

#### Scenario: A gross cut with no window known
- **WHEN** no window is known and the server counts a 40,000-byte prompt at 2,499 tokens
- **THEN** the node fails saying the server dropped part of the prompt

#### Scenario: A repair past the window
- **WHEN** a node's prompt fits, and its repair prompt, which holds the failed outputs, does not
- **THEN** the repair is refused unsent and the node is rejected

#### Scenario: chb's environment does not override Ollama
- **WHEN** Ollama reports 65,536 tokens for a model and chb runs with `OLLAMA_CONTEXT_LENGTH=16384`
- **THEN** the window is 65,536, from `Ollama /api/ps`

#### Scenario: LM Studio
- **WHEN** the endpoint answers `GET /api/v1/models` and the model has no loaded instance
- **THEN** the runner loads it and holds calls to the `context_length` of the loaded instance

### Requirement: Accept rejection and repair

The runner SHALL hand a node's `*workflow.AcceptRejection`, from an `accept:` predicate, from its `output_schema` (workflow.md § Output schema) or from its `min_tool_calls` (§ Minimum tool calls), to `tryRepair` when the node has `on_reject:` (workflow.md § Bounded repair), and otherwise, or once repair is exhausted, mark the node `rejected` and emit `node_rejected`; a `min_tool_calls` shortfall on a node with no `on_reject:` fails the node instead. Each repair attempt SHALL run on the node's backend with its provider, the node's tools (§ Tool allowlist), the run's sampling fields (§ Sampling), the node's reasoning level and its output schema (§ Output schema on the wire). A repair model the block names is resolved for that provider as a dispatch's is, so `sonnet` on an OpenAI-compatible node is `gpt-5.1`, capped and costed as such. A block that names neither `model` nor `tier` sends the model the node's dispatch was sent, as written, such as `qwen3.5:4b`, not the canonical id recorded for pricing. Each attempt is a `workflow_repairs` row carrying its `model` (the canonical id), `provider` and `trigger`: `accept` when the previous attempt was rejected, by an `accept:` predicate, the schema or `min_tool_calls`, `backend_error` when the previous repair call failed. When an attempt passes, its model becomes the node's `resolved_model`. No repair events are streamed. The node's `node_completed` event names the accepted attempt's model, and it and `tokens_delta` carry the tokens and cost of the dispatch and every repair attempt together.

The runner SHALL hand `workflow.CompleteNode` the outputs it parsed from the model's text and nothing else, on the dispatch and on every repair attempt. A value the engine computes for `accept:`, such as `tally_plurality`, goes to state, not to the outputs (workflow.md § Accept predicates). So the stored outputs, the `node_completed` event and the artifact hold what the model said.

A repair prompt's `{accept_failure}` SHALL be the plain-words reason of the `accept:` item that failed, when the item gives one, and otherwise the rejection's text (workflow.md § Accept predicates). The reason leaves the predicate out. The attempt's `failure_reason` keeps the full text, the reason first and the predicate after it. After a backend error, the next attempt keeps the reason. The default `prompt_template` gives `{accept_failure}` first, then the original prompt and the failed outputs, and then again as `What failed: {accept_failure}`, just before `Fix only what failed.` So the last thing the model reads is what to fix. That holds for every node whose `on_reject:` sets no `prompt_template`. A rejected node's replies are kept only through its repairs. Under the default template the dispatch's parsed outputs are in the first attempt's `repair_prompt`, after `Outputs that failed:`, and each attempt's are in its row's `repair_outputs_json`. The node's own row keeps no outputs, and its `rationale` names the predicate. A node rejected with no `on_reject:` keeps no reply in the workflow tables. A node failed under `min_tool_calls` with no `on_reject:` keeps its reply as its `rationale` (§ Minimum tool calls).

#### Scenario: Rejected without a repair block
- **WHEN** a node without `on_reject:` fails `accept:`
- **THEN** it is marked `rejected` with rationale `accept rejected: <predicate> = <value>`

#### Scenario: A same-model repair of a local tag
- **WHEN** a node on `qwen3.5:4b` fails `accept:` and its `on_reject:` names no model
- **THEN** the repair request's `model` is `qwen3.5:4b`

#### Scenario: A same-model repair of a tier node
- **WHEN** a node that names `tier: worker` and no model fails `accept:` on the OpenAI-compatible backend, and its `on_reject:` names no model
- **THEN** the repair request's `model` is the one the dispatch was sent, not the backend's default for an unnamed model

#### Scenario: What a rejected Queen said
- **WHEN** a Queen is rejected after her one repair
- **THEN** her first reply is in the `repair_prompt` of her `workflow_repairs` row, after `Outputs that failed:`, and her repair's reply is in that row's `repair_outputs_json`

#### Scenario: A reason after a backend error
- **WHEN** a node's rejection gives a reason, its first repair call fails with a backend error, and it has a second attempt
- **THEN** the second attempt's prompt gives the reason before and after the failed outputs, without the predicate, and its `failure_reason` leads with the reason

#### Scenario: The completion event after a repair
- **WHEN** a node on `model-a` is repaired by `model-b`
- **THEN** its `node_completed` event names `model-b`, and its tokens are the dispatch's plus the repair's

### Requirement: Run outcome

`agent-run` SHALL dispatch until the engine returns nothing, then report the run's status: `completed` succeeds; `paused` succeeds and names the `chb workflow resume` and `--resume` commands; any other status — `failed`, or a run stopped with nothing left to run — is an error, so the command exits non-zero, except under `--dry-run`, which produces no outputs and so stops at the first decision or conditioned edge that reads one; it logs that and succeeds. Reaching `--max-iterations` (default 500) with nodes still to run is an error naming `--resume`; when the last iteration's wave left every node completed, failed, skipped or rejected, the run is finalized and reported by its status as above. `--max-cost-usd` and `--only-wave` stop the loop cleanly and leave the run resumable.

A cancelled run SHALL stop and stay resumable. When the run's context is cancelled — Ctrl-C or SIGTERM in `agent-run`, which captures them only while its run lasts, or `chb-mcp`'s shutdown for a `chb_research` run — the dispatch loop starts no further wave and returns the cancellation, so `agent-run` exits non-zero and the run stays `running`, not `failed`. A node the cancellation stops did not fail and spends no retry: an agent node or a fan whose call it cut, a command node whose program it killed, a node whose `on_reject:` repair it cut (no further repair attempt is recorded), a calibrate node it reaches before the recompute starts (the recompute takes no context, and once started runs to its end), and a dreamer node it stopped each go back to `pending` with the attempt their dispatch counted taken back (`workflow.ReleaseNode`), and `--resume` dispatches them again.

A panic SHALL fail only what panicked. A panic while a node runs fails that node, through the retry rule (workflow.md § Retry), with `panic: <value>` as its error, and the wave's other nodes run on; one in an item of a `parallel_fan` fails that item as a call that returned an error does, its section reading `(no finding: the call failed: panic: <value>)`. An endpoint slot the panicking call held is freed as the panic unwinds. A panic in the run's ∇ quorum sensor stops the sensor, logged, and the run goes on.

#### Scenario: A node fails
- **WHEN** a node fails with no retries left
- **THEN** its run is marked `failed` and `agent-run` exits non-zero

#### Scenario: The last wave lands on the last iteration
- **WHEN** `agent-run --max-iterations 1` runs a one-node workflow and the node completes
- **THEN** the run is marked `completed` and `agent-run` succeeds

#### Scenario: A human review pauses the run
- **WHEN** a run reaches a `human_review` node
- **THEN** the run is `paused`, `agent-run` succeeds, and its log names `chb workflow resume <id>` and `--resume <id>`

#### Scenario: A run cancelled mid-wave
- **WHEN** a run is cancelled while two of its agent nodes, each with `max_retries: 2`, wait on their calls
- **THEN** the run returns the cancellation and stays `running`, both nodes are `pending` at attempt 0, and `agent-run --resume` completes the run with each node dispatched once

#### Scenario: A node that panics
- **WHEN** one of two nodes in a wave panics in its backend call
- **THEN** it fails with `panic: …` as its error, the other node completes, and the process lives on

### Requirement: Rate limiting

Every backend SHALL be wrapped in `RateLimitedBackend`, unless `HIVE_DISABLE_RATE_LIMIT=1`, a test hook that returns the bare backend. A per-provider token bucket (60 requests a minute; `HIVE_RPM_<PROVIDER>` or `HIVE_RPM_DEFAULT` override it) gates each call. An error SHALL count as a rate limit when it carries HTTP status 429, 503 or 529, or when its text holds one of the words `rate limit`, `rate_limit`, `ratelimit`, `too many requests`, `quota exceeded`, `resource exhausted`, `resource_exhausted`, `throttl` or `overloaded`, in any case. A number in an error's text SHALL never count: a turn cap or an output cap of 429, or a timeout on a port like 54291, is not a rate limit. An incomplete reply's error (§ Incomplete replies fail) and the context window guard's never count, by their type, whatever their text. The OpenAI and Gemini backends' HTTP errors and the Anthropic SDK's API error carry the status, and so does a Gemini CLI error whose JSON gives a `code` from 400 to 599. The Claude CLI's errors are text only; the Anthropic API's error body, which Claude Code prints, names its type: `rate_limit_error` for a 429, `overloaded_error` for a 529. Such a call is retried with decorrelated-jitter backoff, at most 5 retries and 5 minutes, and the bucket token is refunded. A Retry-After header (seconds or an HTTP date), which those three backends keep on their errors, or a `retry-after` hint in the error text, sets the wait instead.

An attempt refused as a rate limit can still come back with calls answered: a Claude CLI run whose later turn got an API 529, or a tool loop refused on a later turn. The retry SHALL charge those calls. Whatever the retry returns, an answer, a non-rate-limit error, or the error when the retries or the time run out, SHALL carry each such attempt as its own call, beside the last attempt (`foldSpent`, as `RunResult.ItemResults`). The runner prices each on its own, as it does a `parallel_fan`'s items, and a fan item's attempts the same way (`spendParts`). The result's tokens, calls and cut-off calls are the attempts' sums, and its text is the last attempt's. An attempt that answered no call adds nothing. The bucket token is refunded all the same.

When the requested model is a tier's premium model, persistent rate limits step down to the tier's fallback and then its free model. When several tiers share that premium model, the chain is the first of them in name order, so it is the same on every run. The chain SHALL keep only models whose models config family is the backend's own: `anthropic` for the Anthropic SDK and the Claude CLI, `openai` for the OpenAI backend, `google` for Gemini and the Gemini CLI. A step comes on every second retry, inside the same 5-retry budget, and waits the backoff like any retry. The node's pin and cost still name the requested model. This detection is a bet (docs/assumptions.md A7).

#### Scenario: Retries exhausted
- **WHEN** a call keeps returning HTTP 429 through five retries
- **THEN** the node fails with `rate-limit retry exhausted`

#### Scenario: A refused attempt that spent tokens
- **WHEN** a Claude CLI run prints a result marked `is_error` with `API Error: 529 …` after 2 turns, exits 1, and the retry answers in 5 turns, for an agent node and for each item of a fan
- **THEN** each node completes, and its row counts 7 calls per call it made, both attempts' tokens, and each attempt's cost at the prices of the models it names; when the retries run out instead, the node fails and its row carries every attempt

#### Scenario: The step-down stays with the provider
- **WHEN** an Anthropic SDK call on `claude-opus-4-8` (the planner's premium; fallback `claude-opus-4-6`, free `gpt-5.5`) keeps returning HTTP 429
- **THEN** the first retry goes to `claude-opus-4-8`, the second through fifth go to `claude-opus-4-6`, and `gpt-5.5` is never sent

#### Scenario: A gateway 503
- **WHEN** an OpenAI-compatible endpoint answers HTTP 503 with a body that names no rate limit
- **THEN** the call is retried as a rate limit

#### Scenario: A number is not a rate limit
- **WHEN** a call fails with `dial tcp 127.0.0.1:54291: connect: connection refused`, or a Claude CLI run reports `Reached maximum number of turns (529)`
- **THEN** it is not retried as a rate limit

#### Scenario: A Gemini CLI error with a status
- **WHEN** a Gemini CLI exits 1 after printing `{"error":{"type":"ApiError","message":"quota exhausted","code":429}}` on stderr
- **THEN** the call is retried as a rate limit; with `"code":53`, an exit code, it is not, whatever numbers the message holds

### Requirement: parallel_fan

A `parallel_fan` node SHALL make one backend call per item, substituting the item for `fan_placeholder` (default `{item}`) where the node's template holds it; a placeholder inside a value, such as a context pack, stays as written. A `fan_placeholder` that is not a `{…}` token is replaced wherever it appears. At most `HIVE_MAX_PARALLEL_FAN` items are in flight, else 4, read and clamped to 1–64 as `HIVE_MAX_PARALLEL_NODES` is. `HIVE_MAX_PARALLEL_NODES` bounds the nodes of a wave, not a fan's items. The endpoint bound (§ Backends) applies to each item's calls as well. The node's budget is 15 minutes per batch of that many items, and at least 30. Its items come from the state value `fan_source` names. A list gives one item per element: a string element as it is, any other element JSON-encoded. A string is read as a JSON array of strings, then a numbered list, then one item per line; an empty JSON array gives no items. With `fan_limit: K` the node dispatches its first K items only, and the engine writes the items past K, in order, as a list to state under `fan_overflow`; an empty list when there are K or fewer, the empty fan included. It writes the list when a node's outputs or `state_updates` set the fan's source, and again when the fan completes, so the list is in state whether the fan completes, fails or is skipped. So no item is dropped silently. The node succeeds when at least half its items, rounded up, return text without an error that holds the node's `output_schema` when it has one. The failed items, those with an error, no text, or text that breaks the schema (`item N: output_schema: <violations>`), are named in the log. `on_reject:` does not repair an item. The runner completes the fan with `CompleteFanItems`, which does not check the joined outputs again: they are not one reply. A fan that runs as one call is one reply, so `CompleteNode` checks it as an agent node's, and a violation goes to `on_reject:`. Tokens, cost and tool invocations are summed across items. Every item keeps its `## Item N` section in the joined text, in order: its text, or `(no finding: the call failed: <error>)`, `(no finding: the reply was empty)` or `(no finding: the reply broke the node's output schema)`. A reply cut off at the output cap is a failed call (§ Incomplete replies fail), so its text is not an answer. The joined text is the value of the node's first declared output. When the list or string holds no items, the node SHALL complete with empty outputs and its `state_updates` applied, make no backend call and not evaluate `accept:`. When the node sets no `fan_source`, or state holds no list or string under it, the node SHALL run as one ordinary call on its prompt, with the placeholder left in place.

#### Scenario: A fan of 8 with 4 failures
- **WHEN** 4 of 8 items fail
- **THEN** the node succeeds with the 4 texts, the 4 failed items each keep a section that says why they have no finding, and the 4 failures are logged

#### Scenario: A fan of 3 with 2 failures
- **WHEN** 2 of 3 items fail
- **THEN** the node fails, because half of 3 rounds up to 2

#### Scenario: A fan's own bound
- **WHEN** `HIVE_MAX_PARALLEL_FAN=1` and a fan has 4 items
- **THEN** its items are sent one at a time; with only `HIVE_MAX_PARALLEL_NODES=1` set, 4 are in flight, subject to the endpoint bound

#### Scenario: A fan item that breaks the schema
- **WHEN** one of three schema'd items answers in prose
- **THEN** the node succeeds with the other two texts joined and the prose item's section reading `(no finding: the reply broke the node's output schema)`, and the log names the item and its violation

#### Scenario: A fan over an empty list
- **WHEN** the state value `fan_source` names is an empty list
- **THEN** the node completes with empty outputs, its `state_updates` apply, and no backend call is made

#### Scenario: A fan over more items than its limit
- **WHEN** a fan with `fan_limit: 3` and `fan_overflow: rest` reads 8 items
- **THEN** it makes 3 backend calls, on the first 3 items, and state `rest` lists the other 5 in order

#### Scenario: A fan_source no node produced
- **WHEN** no node has written the state value `fan_source` names
- **THEN** the node makes one backend call on its prompt and completes like an `agent` node

### Requirement: Test-claim verification

On a node with `verify_tests: true`, an output claiming `tests_pass: true` SHALL be checked before `accept:` runs: the runner runs `go test -count=1` from the project directory over the packages holding the `.go` files `git diff --name-only --relative` reports (`./<dir>` per directory, `.` for the project root; subpackages are not included, and a directory that no longer exists is skipped), and on failure sets `tests_pass` to false and adds `verifier_override` (the first 500 bytes of the output). Untracked new files are not in that diff and are not tested (docs/assumptions.md A3).

#### Scenario: A false claim
- **WHEN** an agent claims `tests_pass: true` and the affected packages fail
- **THEN** `accept:` sees `tests_pass` false

### Requirement: Command nodes

The runner SHALL execute a `command` node (workflow.md § Workflow definition) with no model and no shell. It runs the node's templated `argv` in the project directory, with the environment in § Backends and the templated `stdin` when the node has one. The name `chb` in `argv[0]` resolves to `Config.ChbPath` when it is set, else to the running binary, so a workflow drives the chb that runs it whatever is on `PATH`; any other name is looked up on `PATH`. `chb-mcp` runs workflows in-process for `chb_research` and sets `ChbPath` to the chb it drives (the one beside it, else `chb` on `PATH`), since its own binary is not chb. The program runs in a process group of its own, and a timeout or a cancelled run kills the whole group, so no program it started outlives the node. The node has 30 minutes.

A node whose templated `argv` or `stdin` holds a placeholder no state value fills, or whose `argv` holds an element that is not a string, SHALL fail before anything runs, with an error naming each such placeholder or element (workflow.md § Template substitution); it records no `tool_invocations` row. A program that cannot start, an exit code outside the node's `ok_exit` (0 alone when it names none), and a timeout each fail the node through the retry rule (workflow.md § Retry), with the error naming the exit code and the last 4,000 bytes of stderr, or of stdout when stderr is empty. An exit code `ok_exit` lists completes the node as 0 does, so a workflow can read what a program prints when it refuses, such as `chb guard --json` on a closed gate, and branch on it.

With `outputs_from: stdout_json`, stdout SHALL be one JSON object holding every declared output; only the declared keys reach state. Stdout that is not one JSON object, a missing declared key, and a failing `accept:` predicate each mark the node `rejected`, with a rationale that ends with the exit code and the last 1,000 bytes of stdout. That is terminal, since the same state gives the same output. Without `outputs_from` the node completes with no outputs. The node records one `tool_invocations` row per run: `tool` `command`, `input` the executed argv as a JSON list, `output` a JSON object `{exit_code, stdout, stderr}` with the last 4,000 bytes of each stream (1,000 when escaping would pass the row's 20,000-byte cap), and `is_error` for a failure. `exit_code` is -1 when the program never started or was killed. The node's `tokens_in`, `tokens_out` and `cost_usd_x10000` stay 0, its `provider` stays NULL, and its rationale is its stdout. Its `node_started` event carries its `type` and `argv` and names no provider, agent or model. Under `--dry-run` the node is not executed and completes with `{"dry_run":true}`. A command node's file changes are not auto-committed. `chb workflow next` hands a ready command node out with its templated `argv` and `stdin`, and with `command_error` when it could not run as given, for the caller to run.

A workflow no node of which calls a model (no `agent` or `parallel_fan` node, the dreamer aside) SHALL run with no model backend: none is built, and no API key is needed.

#### Scenario: A program that is not installed
- **WHEN** a command node's `argv[0]` names a program that is not on `PATH`
- **THEN** the node fails with the lookup error, its successors never run, and its `tool_invocations` row records exit code -1

#### Scenario: A non-zero exit
- **WHEN** a command node's program exits 3 after writing to stderr
- **THEN** the node fails with `command exited 3: <stderr tail>` and the row records exit code 3

#### Scenario: A failure reported on stdout
- **WHEN** a command node's program prints `--- FAIL: TestSomething` to stdout, nothing to stderr, and exits 1
- **THEN** the node's error is `command exited 1: --- FAIL: TestSomething`

#### Scenario: An exit code the node allows
- **WHEN** a node with `ok_exit: [0, 1]` runs `chb guard --json` on a gate that stays closed, which prints `{"opened": false, …}` and exits 1
- **THEN** the node completes, `opened` is false in state, and the row records exit code 1 without `is_error`; an exit of 2 would fail it

#### Scenario: A placeholder with no value
- **WHEN** a command node's argv holds `{finding_id}` and state has no `finding_id`
- **THEN** the node fails naming `{finding_id}`, and no program runs

#### Scenario: A missing output key
- **WHEN** a node declares `outputs: [should_continue]` with `outputs_from: stdout_json` and stdout lacks that key
- **THEN** the node is `rejected` with `command output rejected: stdout has no key "should_continue"`

#### Scenario: A relay spends nothing
- **WHEN** a command node completes
- **THEN** its row shows 0 tokens and 0 cost, and state holds only its declared outputs

#### Scenario: A cancelled run
- **WHEN** a run is cancelled while a command node's program has a child of its own running
- **THEN** both are killed

#### Scenario: A workflow of command nodes
- **WHEN** `agent-run` runs a workflow whose only nodes are command nodes, with `--provider openai` and no `OPENAI_API_KEY`
- **THEN** the run completes

### Requirement: Per-node persistence and cost

For every backend run that a model call answered — a dispatch that is accepted, rejected or fails, and each repair attempt, even one that also returns an error — the runner SHALL add its tokens and cost to the node's `workflow_node_states` row, record its provider and endpoint, and add its cost to the run's cumulative cost. The endpoint (`base_url`) is where chb sent the call: `OPENAI_BASE_URL` (else `https://api.openai.com/v1`), `GEMINI_BASE_URL` (else Google's), `ANTHROPIC_BASE_URL` (else `https://api.anthropic.com`), with any `user:password@` removed, here and in every log line that names it. The CLI backends choose their own hosts, so their rows record none, and neither does an injected test backend. The run log's `llm backend:` line names the run default's endpoint. An accepted node's rationale SHALL be the accepted attempt's text (the repair's, when a repair passed), cut on a UTF-8 boundary so that with its trailing ellipsis it is at most 64,000 bytes. Cost is priced from the models config, in 1/10000 USD (`usageCostUSDx10000`). A node's `tokens_in` counts every input token: uncached, read from the provider's prompt cache, and written to it. Uncached input tokens cost the model's input price. Cached input tokens, those read from the cache, cost its `cached_input_per_mtok_usd` when its entry gives one, and its input price when not; a cached price of 0 is a price. Input tokens written to the cache cost its `cache_write_per_mtok_usd`, and those written with a 1-hour lifetime its `cache_write_1h_per_mtok_usd`. Without the first, a write costs the input price; without the second, a 1-hour write costs the 5-minute price. Output tokens cost its output price, and thought tokens are output tokens. The Gemini API (`cachedContentTokenCount`, a part of `promptTokenCount`) and a Gemini CLI with `--output-format json` report cached tokens. The Claude CLI reports cache reads and cache writes (below). So does the Anthropic SDK backend, as the Messages API counts them: a turn's input is `input_tokens`, the uncached remainder, plus `cache_read_input_tokens` plus `cache_creation_input_tokens`, and `cache_creation.ephemeral_1h_input_tokens` is the part of the writes made with a 1-hour lifetime. It sets no `cache_control`, but a server it is pointed at may cache unasked, as Ollama 0.34.4 does. That backend streams each turn, and `message_delta`'s usage is cumulative. It takes a turn's input counts from `message_delta` when that event carries them, and from `message_start` when not, as the Anthropic Python SDK's stream accumulator does (src/anthropic/lib/streaming/_messages.py, read 2026-09-28). Measured on Ollama 0.34.4's `/v1/messages`, 2026-09-28: a 2,930-token prompt sent twice came back with `input_tokens` 2,010 in `message_start` both times, an estimate, and in `message_delta` with 2,930 uncached the first time, then 4 uncached and 2,926 read from the cache. The OpenAI-compatible backend reads `prompt_tokens`, which counts cached tokens too: the same text on Ollama 0.34.4's `/v1/chat/completions` counted 2,929 prompt tokens, of which 2,925 were cached. It reads no split, so its input is priced whole at the input price, as the other backends' is. The shipped config gives cached prices for `gemini-2.5-pro`, `gemini-2.5-flash` and `gemini-2.5-flash-lite`: Google's context-caching prices for text prompts up to 200k tokens (ai.google.dev/gemini-api/docs/pricing, last updated 2026-09-24, read 2026-09-28). It models neither the higher prices above 200k prompt tokens nor cache storage. For each Claude model it lists, it gives Anthropic's prices for input, output, cache hits, 5-minute cache writes and 1-hour cache writes (platform.claude.com/docs/en/about-claude/pricing, read 2026-09-28; the page shows no date). Those are 0.1, 1.25 and 2 times the input price, except a cache hit on `claude-opus-5-5`, at 0.05. Opus 4.5 through Opus 5 cost $5 per million input tokens and $25 per million output tokens, and `claude-opus-5-5` $4 and $20. The config lists `claude-opus-5` and `claude-sonnet-5`, which the Claude CLI's `opus` and `sonnet` aliases call in Claude Code 2.1.236, and `claude-opus-5-5` and `claude-sonnet-5-5`, the current models. It models neither the 1.1 times price of US-only inference, nor fast mode, nor web searches ($10 per 1,000), which a Claude CLI run can make. A backend that names the models it called is priced model by model (`RunResult.ByModel`); the others at the node's resolved model. A Claude CLI or Gemini CLI node is priced at API rates, a notional figure when its login is not billed per token (docs/assumptions.md A9). `PricingLastUpdated()` gives the config's verification date, and preflight warns past 60 days.

A Claude CLI reply to `--output-format json` SHALL be priced from its token counts, cache reads and writes included. Its `usage` counts the main loop's tokens, summed over its turns, as the Messages API does: `input_tokens` is the uncached remainder, beside `cache_read_input_tokens` and `cache_creation_input_tokens`, and `cache_creation.ephemeral_1h_input_tokens` is the part of the writes made with a 1-hour lifetime. Anthropic's prompt-caching page (platform.claude.com/docs/en/build-with-claude/prompt-caching, read 2026-09-28) defines `input_tokens` as the tokens neither read from nor used to create a cache, total input as the sum of the three, and `cache_creation_input_tokens` as the sum of `cache_creation`'s 5-minute and 1-hour parts. Its `modelUsage` holds one entry per model the run called, its own inner calls included, keyed by the model sent: `inputTokens`, `cacheReadInputTokens`, `cacheCreationInputTokens`, `outputTokens` and `canonicalModel`. Claude Code 2.1.236's bundled result schema gives this shape, and describes `canonicalModel` as the id it prices the entry by, which may differ from the key for a provider's id or an alias. Its builder adds each call's `input_tokens`, cache reads and cache writes under the key. When `modelUsage` names a model that spent tokens, each such model is priced at its own prices, with its three input counts as its input. An entry is priced by its key when the models config lists the key, and by its `canonicalModel` when not. On Bedrock or Vertex the key is the provider's id, such as `us.anthropic.claude-sonnet-4-5-20250929-v1:0`, which the config does not list. The key comes first because Claude Code 2.1.236 gives a model it does not know the canonical id of the older model whose name the id contains: `claude-opus-5-5` gets `claude-opus-5`. An entry neither of whose ids the config lists is unmetered, as any unpriced model is; so is a provider's id from a CLI whose reply carries no `canonicalModel`. A model id's `[1m]` suffix, Claude Code's mark for the 1M context, is dropped, since the price is the model's. `modelUsage` does not split a model's writes by lifetime. So the 1-hour writes `usage` reports go first to the model that wrote the most, up to its writes, and then to the next. That is a choice. It is exact when one model wrote to the cache, and when several did it is a bet that the main loop's model wrote the most. Without `modelUsage`, as from an older CLI, `usage` is priced at the node's model. The calls are the reply's `num_turns`. When the models priced are not just the node's model, the run log names them once per node, as below. Which lifetime a call writes is Claude Code's choice; chb prices what the reply reports. chb does not read the reply's `total_cost_usd`, Claude Code's own figure from its own price table. A reply with `is_error` set, such as `error_max_turns` or an API error, fails the node with an error naming its subtype, its `result` and its `errors`. The tokens it reports come back with the error and are charged. One that reports no tokens counts no call. Claude Code prints such a reply and then exits 1: its headless runner ends with exit status 1 when its last message is a result marked `is_error` (read from the 2.1.236 bundle). So a run that exits non-zero SHALL be read from the result it printed, when it printed one. A result not marked `is_error` from a run that exits non-zero fails the node with the exit status and stderr, and its tokens are charged too. A run that printed no result fails the same way and counts no call. An API error that is a rate limit, such as `API Error: 529 {…"overloaded_error"…}`, reads as one (§ Rate limiting), and the retry charges the tokens it spent. The node row's `resolved_model` names the model the node asked for, as § Model resolution says; the models the CLI called are priced each at its own price and named in that log line, and no row column holds them.

A Gemini CLI reply to `--output-format json` SHALL be priced from its `stats.models`, which holds one entry per model the CLI called in that run, its own inner calls included. The headless docs (google-gemini.github.io/gemini-cli/docs/cli/headless.html, last modified 2025-10-07, read 2026-09-28) show one prompt that used two models. Each entry that spent tokens is priced at its own model, not the node's. Its input tokens are `prompt` plus `tool`, the tool-use prompt tokens. Of those, `cached` are cached. Its output tokens are `candidates` plus `thoughts`. The docs' example totals are prompt + candidates + thoughts + tool, and gemini-cli's telemetry sets `input` to prompt minus cached (packages/core/src/telemetry/uiTelemetry.ts), so `prompt` includes `cached`. Each request a model answered (`totalRequests` minus `totalErrors`) counts as one call. An entry that answered no request and spent no token is left out, so a model whose every request failed does not make the call unmetered. The call's cost is unknown, and the call unmetered, when its `stats.models` is missing or empty, names no model that spent tokens, or reports no tokens for a model that answered a request. It then counts its answered requests, or one call when it counts none. A reply that is not that JSON fails the node and counts as one unmetered call. A reply that carries `error` or `warnings` fails the node (§ Incomplete replies fail), and its calls count from its `stats.models` by the same rules. An error gemini-cli throws, such as an API error or a failed sign-in, never reaches that reply: the CLI prints it on stderr as JSON with no stats and exits non-zero (packages/cli/src/utils/errors.ts, v0.6.0 and v0.61.0). That run fails like any CLI run that fails, and counts no call. A CLI that prints a reply whose stats report tokens and then exits non-zero fails the node too, and is charged those tokens and calls, as the Claude CLI is (§ Backends). The node's `tokens_in` and `tokens_out` are the sums, and `resolved_model` still names the node's model. When the models a node's calls used are not just the node's model, the run log names them once per node: `node <n>: its calls used <m1>, <m2>, each priced at its own prices; the node names "<m>"`.

A call is unmetered when its cost is unknown: a model it used has no models-config entry and the call left this machine, or its backend reports no token counts. Its cost then adds 0, even for the models it used that have a price. A call on this machine, on provider `local` at a loopback endpoint with a model that is not an Ollama cloud tag (§ Routing profiles), costs nothing: one to a model the config does not price is metered at $0, its constraint probe too, and nothing is logged about its price (`onThisMachine`). That is a definition: the meter counts what a provider bills per token, and a server on this machine bills none; its power and wear are not counted. A model the config prices costs its price wherever it runs. A call on provider `openai` is off this machine, whatever endpoint serves it, as a cloud tag on the local server is. A Gemini CLI without `--output-format json` is that backend: its plain-text output carries no usage, so each of its runs is one unmetered call, whatever model it names and whatever calls it made inside. A `parallel_fan`'s items are priced one by one, each as its own call, since one item's reply may report usage, or name models, that another's does not. So an item that is unmetered leaves its siblings' metered calls and cost counted. An unmetered call adds 0 because its cost is unknown, not because it was free. A model listed at a price of 0 is metered at $0. The first unmetered call for each reason is logged, naming it, with the note that `--max-cost-usd` does not count it. The reasons are `model "<m>" has no price in the models config`, naming the first such model, and `provider gemini-cli reports no token counts for model "<m>" (<why>)`. `<why>` names the CLI and says its `--help` lists no `--output-format` flag or failed, or says what its JSON reply lacked. A run that no call answered is charged nothing, counted as no call, and records no provider or endpoint: a CLI run that fails, an HTTP call refused or never sent on its first turn, and a `parallel_fan` none of whose items' calls was answered. `--max-cost-usd` SHALL end the dispatch loop cleanly once cumulative cost reaches the cap. Each iteration logs `iter=N provider=<p> tokens=in=…/out=… cost=<c>`, and the run ends with `done. … cost=<c>`, with `credits=N.NN` appended when Copilot credits are metered. Each node row SHALL count its model calls as `metered_calls` and `unmetered_calls`: a dispatch or repair attempt adds the calls that answered it: the turns it made, or 1 for a backend that counts no turns but returned text or tokens (`callsMade`). So a fan adds every item's calls and a tool loop every turn's. It SHALL add the calls the provider stopped at the output cap to `cutoff_calls` the same way (§ Incomplete replies fail). `<c>` is `$X.XX` when every call was metered, `unmetered` when none was, and `$X.XX + unmetered (N calls)` when the run mixed them, N counting the unmetered calls (`models.CostLabel`). The `run_completed` event carries `unmetered_calls`. Every surface that shows a cost reads the same way from the node rows: `chb run-totals` and the MCP `chb_run_totals` (`cost_usd`, with `metered_calls` and `unmetered_calls`), the MCP `chb_node_rationale`, and `chb proof-inspect`. A run total (`run-totals`, `chb_run_totals` and `proof-inspect`'s cost total) also adds the constraint probes the run's row carries (§ Constraint probe), which the run log's `done.` line counts too. `cost_usd_x10000` stays the priced calls' sum.

#### Scenario: The cost cap
- **WHEN** cumulative cost reaches `--max-cost-usd`
- **THEN** no further node is dispatched and the run ends without error

#### Scenario: A rejected node's spend counts
- **WHEN** a node's dispatch fails `accept:` with no repair left
- **THEN** its row carries that dispatch's tokens, cost and provider, and its cost counts toward `--max-cost-usd`

#### Scenario: A local model with no price
- **WHEN** every node runs on provider `local` at an endpoint on this machine, on a model the models config does not list, one of them a tool loop and one with an `output_schema` the run probes
- **THEN** the run's log lines say `cost=$0.00` and log nothing about a price, and so does `run-totals`; every call, the probe's included, is metered; each node row records the local endpoint as its `base_url`; a node on an Ollama cloud tag at the same endpoint is unmetered, and the log says it has no price

#### Scenario: A tool loop on an unpriced model off this machine
- **WHEN** a priced node makes one call and a node on a model the config does not price, at an `OPENAI_BASE_URL` server, makes four, three of them tool calls
- **THEN** the run ends with `cost=$X.XX + unmetered (4 calls)`, and the log says the model has no price

#### Scenario: A priced model on a Gemini CLI without `--output-format`
- **WHEN** a node on a Gemini CLI whose `--help` lists no `--output-format` names `gemini-2.5-flash`, which the models config prices, and a `parallel_fan` of three items does too
- **THEN** the CLI is never sent the flag; their rows count 1 and 3 unmetered calls and no metered one, and the run log, `run-totals`, `chb_run_totals` and `chb_node_rationale` say `unmetered`, not `$0.00`; the log names the CLI and the missing flag once; a node on the Gemini API with that model in the same run is priced from its usage, its cached tokens at the cached price

#### Scenario: A Gemini CLI with `--output-format json`
- **WHEN** the same nodes run on a Gemini CLI that has the flag, and each reply's `stats.models` lists `gemini-2.5-pro` with 2 answered requests and cached tokens, `gemini-2.5-flash` with 1 and tool tokens, and a third model whose one request failed
- **THEN** each call is priced at each model's own prices, the cached tokens at the cached price; the rows count 3 and 9 metered calls; their tokens are the stats' sums; and every surface shows dollars

#### Scenario: A Claude CLI run that reads and writes the prompt cache
- **WHEN** a node names `sonnet` on the Claude CLI, and each reply's `modelUsage` lists `claude-sonnet-5` and `claude-haiku-4-5-20251001`, each with uncached, cache-read and cache-written input tokens, and its `usage` counts part of the writes at the 1-hour lifetime
- **THEN** the node's `tokens_in` counts all three kinds of input for both models; each model's reads cost its cached price and its writes its write prices, the 1-hour ones going to `claude-sonnet-5`, which wrote more; the log says the node's calls used both models; and the run log and every run total show dollars above what the uncached input alone would cost; without `modelUsage`, `usage` is priced the same way at `sonnet`'s prices

#### Scenario: A Claude CLI reply marked is_error
- **WHEN** the Claude CLI prints a result with `is_error` set and subtype `error_max_turns`, after spending tokens, and exits 1
- **THEN** the node fails naming `error_max_turns` and the CLI's errors, and its row carries the tokens and cost the reply reports; a reply that reports no tokens counts no call

#### Scenario: A Claude CLI run on Bedrock
- **WHEN** a reply's `modelUsage` is keyed `us.anthropic.claude-sonnet-4-5-20250929-v1:0` with `canonicalModel` `claude-sonnet-4-5`
- **THEN** its tokens are priced at `claude-sonnet-4-5`'s prices, not reported unmetered; an entry keyed `claude-opus-5-5` with `canonicalModel` `claude-opus-5` is priced at `claude-opus-5-5`'s

#### Scenario: The Anthropic SDK backend on a server that caches
- **WHEN** a node on provider `anthropic` with `ANTHROPIC_BASE_URL` at Ollama sends the same prompt twice, and the second turn's `message_delta` reports 4 uncached input tokens and 2,926 read from the cache after an estimate in `message_start`
- **THEN** both calls' `tokens_in` is the prompt's full count, and the second's cache reads are its cached tokens

#### Scenario: A Gemini CLI reply that names an unpriced model
- **WHEN** a reply's `stats.models` lists a model with no models-config entry that spent tokens
- **THEN** the call is unmetered, its cost adds 0, and the log says `model "<m>" has no price in the models config` once

#### Scenario: A Gemini CLI reply whose stats report no tokens
- **WHEN** a reply's `stats.models` is empty, or lists a model that answered 2 requests with no tokens
- **THEN** the call is unmetered, counting 1 or 2 calls, never metered at $0, and the log says what the reply lacked

#### Scenario: One fan item without usage
- **WHEN** a `parallel_fan` of three items runs on a Gemini CLI with `--output-format json`, and item b's reply has no stats, names an unpriced model, or reports no tokens for a model that answered
- **THEN** the row counts items a's and c's calls as metered, with their cost, and item b's as unmetered; the run's cost, which `--max-cost-usd` reads, is a's and c's; the log gives b's reason once; and `run-totals` shows `$X.XX + unmetered (N calls)`

#### Scenario: A node no call answered
- **WHEN** a node on a priced model, or a `parallel_fan` of two items on one, gets HTTP 400 on every first call, or its Gemini CLI exits 1 on every item
- **THEN** its row counts no metered or unmetered call, no cost and no endpoint; the run's totals count no call; and no unmetered line is logged

#### Scenario: A constraint probe in the run totals
- **WHEN** a run sends one constraint probe and its one schema'd node makes one call
- **THEN** the node row counts one call, and `run-totals` and `chb_run_totals` count both calls and both calls' tokens

### Requirement: Endpoint model preflight

When `OPENAI_BASE_URL` is set, `agent-run`, and so `chb ask`, SHALL list `<base>/models` before it creates the run and before any model call, and SHALL refuse the workflow when a node served by the OpenAI-compatible backend, or that node's `on_reject:` repair, is sent a model the endpoint does not list. It SHALL make the same check of the local endpoint (`HIVE_LOCAL_BASE_URL`, default `http://localhost:11434/v1`) whenever a node runs on the local provider, whether or not `OPENAI_BASE_URL` is set, and report each endpoint on its own `model preflight:` line; a local server that does not answer refuses the run. The check runs after the allowlist checks (§ Backend selection), so an endpoint the allowlist refuses is never asked. A node is served by that backend when the run default is `openai` or the node sets `provider: openai`, and its repair runs on the same provider. Decision, dreamer and human-review nodes are skipped. A node's model resolves as at dispatch: `model:`, else `tier:` under the budget mode (§ Model tiers and budget mode), else `sonnet`, then the OpenAI alias table. So a Claude alias left in a workflow, or a shipped tier, is refused as the `gpt-5.1` name it becomes. A repair's model resolves as `tryRepair` resolves it: the block's `model:`, else its `tier:`, then the same alias table, else the node's own model. A block with `max_repair_iterations` of 0 or less makes no repair and is skipped. A name without a tag also matches `name:latest`, Ollama's default tag. The refusal names each missing model, the nodes sent it (`<node> on_reject` for a repair), and what the endpoint serves. When a missing model came from a tier, the refusal also names the tier and where to map it to a served model: `tiers:` in the models config.

An answer to the listing is a model list only when it is a 2xx with a `data` list; anything else is a failure to list, and the error shows the body. An endpoint that answers 404 is taken not to list models, unless its base URL lacks `/v1` and `<base>/v1/models` lists them. Ollama and LM Studio serve their API under `/v1`, so then the base URL is wrong, every call would miss, and the run is refused naming the URL to set. Otherwise the run logs that the check was skipped and goes on, adding that an Ollama or LM Studio base URL ends in `/v1` when it does not. Any other failure to list refuses the run, whose first call would fail the same way, with the same note.

When every model is served, the check SHALL also ask the endpoint about each model a node, or its repair, sends a reasoning level (§ Reasoning level), and name each node whose level will not be sent where that changes what the model does: `"<model>" (node <node>, reasoning <level>): the server reports no thinking capability` for a level other than `none` to a model that cannot think. The run log's `model preflight:` line carries these after the models served. None refuses the run: the backend omits the field, so no call gets Ollama's HTTP 400. The answers are kept for the run's calls, which do not ask again.

When `OPENAI_BASE_URL` is set but the run default is another provider and no node sets `provider: openai`, the run logs that the variable is set but unused, naming the run default, and, when that is `local`, the `HIVE_LOCAL_BASE_URL` its calls go to. When a node does run on it and no `--temperature`, `--top-p` or `--deterministic` is given, the run logs that the server chooses the sampling (§ Sampling). A dry run skips the check. Without an `OPENAI_API_KEY` the backend cannot be built, and nothing is checked. `chb preflight` makes the same check, with `--budget-mode` to resolve `tier:` nodes as `agent-run --budget-mode` would: ✓ when every model is served and every level is sent, ✗ when a model is not served or the listing fails, ⚠ on a 404, an unused `OPENAI_BASE_URL`, or a level that will not be sent. The listing is a bet on the server's `/models` answering in OpenAI's list shape, which Ollama and LM Studio document.

#### Scenario: A model the endpoint does not serve
- **WHEN** `OPENAI_BASE_URL` points at an endpoint that lists `qwen3.5:4b`, and a node is sent `ministral-3:8b`
- **THEN** `agent-run` fails naming `"ministral-3:8b"` and the node, makes no model call, and writes no `workflow_runs` row

#### Scenario: A repair model the endpoint does not serve
- **WHEN** a node on `qwen3.5:4b`, which the endpoint lists, has `on_reject: {tier: synthesist}`
- **THEN** `agent-run` fails naming the model the tier resolves to and `<node> on_reject`, and makes no model call

#### Scenario: A local base URL without /v1
- **WHEN** `OPENAI_BASE_URL=http://localhost:11434`, and the server answers `GET /models` with 404 and `GET /v1/models` with its list
- **THEN** `agent-run` fails saying to set `OPENAI_BASE_URL` to `http://localhost:11434/v1`, and makes no model call

#### Scenario: Thinking on a model without the capability
- **WHEN** a node on `ministral-3:8b` sets `reasoning: low`, and Ollama's `/api/show` lists no `thinking` for it
- **THEN** `agent-run` runs; its `model preflight:` line names `"ministral-3:8b" (node <node>, reasoning low)`, `chb preflight` shows ⚠, and the node's call carries no `reasoning_effort`; with `reasoning: none` nothing is named

#### Scenario: A refused provider is not asked
- **WHEN** `HIVE_PROVIDER_ALLOWLIST=anthropic` and a run starts with `--provider openai` and `OPENAI_BASE_URL` set
- **THEN** the run is refused, and the endpoint receives no request

### Requirement: Sampling

`--deterministic` SHALL set temperature 0 on every dispatch and repair attempt; `--seed <int64>` sets a seed on them and implies `--deterministic`. No seed is ever chosen silently. `--temperature` (0 to 2) and `--top-p` (above 0, at most 1), on `agent-run` and `chb ask`, SHALL be sent on every dispatch and repair attempt, and nothing is sent for one that is unset. Either one beside `--deterministic` or `--seed` is refused, since those fix temperature 0.

Each backend sends what is set. The OpenAI backend sends `temperature` and `top_p`, and at temperature 0 with no top-p it also sends `top_p` 1. The Gemini backend sends `temperature` and `topP` likewise, and at temperature 0 also `topK` 1. So `--deterministic` sends `temperature` 0, `top_p` 1 and `seed` on OpenAI, and `temperature` 0, `topP` 1, `topK` 1 and `seed` on Gemini, while `--temperature 0.7` sends no greedy `topK`. The Anthropic backend sends `temperature` and `top_p` as set, and warns once that the Messages API has no seed. The Messages API takes a temperature of 0 to 1, so a `--temperature` above 1 SHALL be refused before the run starts when any node runs on the Anthropic SDK backend (`PreflightSampling`). Anthropic documents that its Claude 4 and later models refuse `temperature` and `top_p` together, that Opus 4.7 and later refuse either one, and that Sonnet 5 refuses a value other than the default. The runner does not check those per model, so such a call, `--deterministic`'s temperature 0 included, fails with the API's HTTP 400. The CLI backends warn once that none of these flags has an effect.

With neither flag, no field is sent and the server chooses. Ollama 0.30.10's OpenAI endpoint then uses temperature 1.0 and top_p 1.0, not the values in the model's Modelfile (openai/openai.go). When `OPENAI_BASE_URL` is set and a node runs on it, the run log says so once.

`chb ask --artifact` records the run's settings: temperature (0 under `--deterministic`, else `--temperature`, null when unset), top-p (`--top-p`, left out when unset) and the seed. An unset value means none was sent and the server chose. The greedy `top_p` 1 and `topK` 1 a backend adds at temperature 0 are not recorded.

#### Scenario: Seed on a CLI backend
- **WHEN** `--seed 42` is passed to a run on the Claude CLI
- **THEN** the runner prints `[agent-run] --deterministic / --seed / --temperature / --top-p have no effect on CLI backends` once and proceeds

#### Scenario: Sampling on a local endpoint
- **WHEN** `chb ask "q" --temperature 0.7` runs on the OpenAI-compatible backend
- **THEN** each request carries `"temperature": 0.7` and no `top_p`; without the flag it carries neither

#### Scenario: A temperature the Anthropic API cannot take
- **WHEN** `agent-run --temperature 1.5` runs a workflow with a node on the Anthropic SDK backend
- **THEN** it exits with an error naming `--temperature 1.5` before any run is created; on the OpenAI-compatible backend the value is sent

#### Scenario: Sampling beside determinism
- **WHEN** `agent-run --deterministic --temperature 0.7` is run
- **THEN** it exits with an error before any run is created

### Requirement: Auto-commit and dry run

`--branch <name>` SHALL commit a run's file edits to that branch, refusing to start on a dirty working tree (it stages with `git add -A`) unless `--allow-dirty` is given. The refusal SHALL come before the run is created, so it leaves no run behind. The branch SHALL be created or checked out only after the run is created, so a run that cannot start leaves the checkout as it was. A project directory that is not inside a git working tree (`git rev-parse --is-inside-work-tree`) runs without auto-commit; a subdirectory of a repository is inside one. `--auto-pr` opens a rollup pull request at the end (the `gh` CLI and `GH_TOKEN`). `--dry-run` SHALL skip model calls and file mutations, bypass `accept:`, still walk decisions, and create no branch even with `--branch`; with `--auto-pr` it opens no pull request.

#### Scenario: Dry run with a branch
- **WHEN** `chb agent-run … --dry-run --branch x` runs
- **THEN** no branch named `x` is created and git is untouched

#### Scenario: A dirty tree
- **WHEN** `chb agent-run … --branch x` starts on a working tree with uncommitted changes
- **THEN** it exits with an error naming `--allow-dirty` and writes no `workflow_runs` row

#### Scenario: A run that cannot start
- **WHEN** `chb agent-run missing.yaml --branch x` runs on a clean working tree
- **THEN** it exits with an error, no branch named `x` exists, and the checkout stays on its branch

## Files

- `internal/runner/runner.go` — the dispatch loop and its stop on cancellation, cost cap, auto-commit setup, `loadAgentPersona`; `assets.go` at the module root — the carried `agents/` (`hive.Agents`)
- `internal/runner/dispatch.go` — per-node execution, the per-node recover in `dispatchWave`, `releaseNode`, model and provider resolution, accept-rejection handling, `recordSpend`, `spendParts` and `callsMade`, `contextWindowFor` (a window per model per endpoint)
- `internal/runner/backend.go` — `ResolveBackendKind`, `NewBackend`, the provider allowlist, `PreflightWorkflowProviders`, `PreflightSampling`, `HIVE_HTTP_TIMEOUT`, `acquireEndpointSlot`, `incompleteReply`
- `internal/endpointslot/endpointslot.go` — the endpoint slots (`Acquire`, `Limit`, `Key`, `IsLoopbackURL`), shared by the backends and the embedding providers, and across processes by the lock files (`lockFiles`, `slotDir`, `checkLocks`, `lockAcross`, `tryLock`, `lockOpen`, `unlock`)
- `internal/endpointslot/lock_flock.go`, `lock_none.go` — `flock`, and the builds without it
- `internal/runner/backend_sdk.go`, `claude.go` — Anthropic SDK backend (`streamTurn` reads a turn's usage), `ResolveModelAlias`; `internal/workflow/complete.go` — `ExtractJSONOutput`
- `internal/runner/backend_gemini.go`, `backend_openai.go`, `backend_cli.go`, `backend_cli_gemini.go` — the other backends; `backend_openai.go` holds `responseFormat`, the finalize call, `toolArguments` and the local provider (`NewLocalBackend`, `localBaseURL`); `backend_gemini.go` keeps each reply part as it came (`geminiPart.raw`) to send it back; `backend_cli.go` holds `cliUsage` and `strip1MContext`; `backend_cli_gemini.go` holds `geminiCLITakesJSON`, `geminiCLIJSONResult`, `geminiCLIFailed` and `geminiCLITruncated`
- `internal/runner/profile.go` — `ResolveProfile`, `ApplyProfile`, `Routing`, `RoutingReport`, `PreflightLocality`; `applyRunProfile` in `runner.go`
- `internal/models/config.go`, `default-models.yaml` — `Profile`, `Route`, `Roles`, `RepairRoles`, `HiveLadder`, `ReasoningLevels` and `CheckReasoningLevel`, the shipped profiles
- `internal/runner/endpoint_models.go` — `PreflightEndpointModels`, the reasoning levels it names as not sent
- `internal/runner/thinking.go` — the per-run thinking-capability cache, `reasoningToSend`, `ollamaCapabilities`
- `internal/runner/constraint_probe.go` — `ProbeConstraints`, `classifyProbe`, `schemaEnforcement`
- `internal/runner/context_window.go` — the context-window guard: the estimate (`textTokens`, `measurePrompt`), `fitContext`, the check after a call, the calibration, `findServer` and `resolveContextWindow` with the Ollama, LM Studio and llama.cpp windows; `toolResultBound` and `toolResultCap`, the bound every SDK backend holds a tool result to
- `internal/runner/ratelimit.go` — `RateLimitedBackend`, `foldSpent`, `isRateLimitError`, `statusError`, `tierFallbackChain`
- `internal/runner/dispatch_fanout.go` — `parallel_fan`
- `internal/runner/command_node.go` — `command` nodes; `command_node_unix.go`, `command_node_windows.go` — `killProcessGroupOnCancel`, which the CLI backends and `shell` use too
- `internal/runner/verify_tests.go` — test-claim verification
- `internal/runner/tiers.go` — `BudgetMode`, `ResolveBudgetMode` (`--budget-mode`, else `HIVE_BUDGET_MODE`, for `chb agent-run`, `ask`, `replicate`, `preflight` and `agent-harness`), `ParseBudgetMode`, `tierModel`: a tier's slot as a node's call sends it; `pricing.go` — `ComputeCostUSDx10000`, `usageCostUSDx10000`, `isMetered`, `formatCost`, `providerLabel`
- `internal/models/config.go`, `default-models.yaml` — the models config, `ResolveTier`, `PriceCachedInPer1MTokensX10000`, `PriceCacheWritePer1MTokensX10000`, `PriceCacheWrite1hPer1MTokensX10000`; `load.go` — `Load` and the user's override
- `internal/models/cost_label.go` — `CostLabel`, the cost every surface prints
- `internal/runner/repair.go` — `tryRepair`
- `internal/runner/tools.go` — the tool registry, `Restrict` for a node's allowlist, the unknown-tool refusal, `registerShell`
- `internal/runner/shell.go`, `shell_windows.go`, `shell_other.go` — `shellRunner`, `shells`, `resolveShell`, `hostShell`; `cmdCommandLine`, cmd's whole command line on Windows
- `internal/workflow/node_fields.go` — `ToolNames`, `ToolAliases`, `CanonicalTool`
- `internal/runner/dispatch_fanout.go` — `runFanOut`, the `## Item N` sections
- `internal/runner/agent_env.go` — `agentEnv`, the environment every agent subprocess and command node runs with, and `modelEnv`, which adds `HIVE_AGENT_DB` for what a model drives
- `internal/cli/agent_run.go` — `chb agent-run`, `samplingFlags`, `useProfile`
- `internal/cli/preflight.go` — `chb preflight`, including the endpoint model check, the constraint probe and the routing (`routedWorkflow`, `checkRouting`)
