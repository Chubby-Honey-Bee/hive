<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-white.svg">
    <img src="docs/assets/logo.svg" alt="The Chubby Honey Bee logo: a smiling bee inside a honeycomb cell" width="200">
  </picture>
</p>

<h1 align="center">HIVE</h1>

<p align="center">
  A colony of AI agents that looks at a question from many sides,<br>
  remembers what it learns, and tells you how sure it is.
</p>

<div align="center">

[![CI](https://github.com/Chubby-Honey-Bee/hive/actions/workflows/ci.yml/badge.svg)](https://github.com/Chubby-Honey-Bee/hive/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Install in VS Code](https://img.shields.io/badge/VS_Code-Install_MCP_server-0098FF?logo=visualstudiocode&logoColor=white)](vscode:mcp/install?%7B%22name%22%3A%22chb%22%2C%22type%22%3A%22stdio%22%2C%22command%22%3A%22docker%22%2C%22args%22%3A%5B%22run%22%2C%22--rm%22%2C%22-i%22%2C%22--init%22%2C%22--pull%3Dmissing%22%2C%22-v%22%2C%22chb-workspace%3A%2Fapp%2Fworkspace%22%2C%22-e%22%2C%22HIVE_DB_PATH%3D%2Fapp%2Fworkspace%2Fhive.db%22%2C%22-e%22%2C%22ANTHROPIC_API_KEY%22%2C%22-e%22%2C%22OPENAI_API_KEY%22%2C%22-e%22%2C%22GEMINI_API_KEY%22%2C%22-e%22%2C%22GH_TOKEN%22%2C%22ghcr.io%2Fchubby-honey-bee%2Fhive%3Alatest%22%5D%7D)

</div>

## What HIVE is

HIVE, the Hive-Inspired Virtual Entity, answers hard questions the way a bee colony finds food. You ask one question. A group of *foragers* works on it at the same time, each from its own angle: risk, evidence, history, cost, ethics and more. They can't see each other's work, so they don't drift toward one easy answer. The *queen* reads what they found, writes a single verdict, and tells you how much the foragers agreed.

What the foragers learn is kept in a shared memory called the *comb*, so the next question can build on the last one. Every finding is labeled as a definition, an assumption, a guarantee or an unknown, and a *guard* turns away any "guarantee" that rests on something unknown.

You use HIVE through one command, `chb`, with a model on your own computer or in the cloud.

## Install

Download the archive for your system from the [releases page](https://github.com/Chubby-Honey-Bee/hive/releases). Each one holds two programs, `chb` and `chb-mcp`, built for Linux, macOS and Windows on amd64 and arm64.

Or install them with Go:

```bash
go install github.com/Chubby-Honey-Bee/hive/cmd/chb@latest
go install github.com/Chubby-Honey-Bee/hive/cmd/chb-mcp@latest
```

Or build them from source:

```bash
git clone https://github.com/Chubby-Honey-Bee/hive.git && cd hive
go build ./cmd/chb && go build ./cmd/chb-mcp
```

The programs carry everything they need, so you can run them from any folder.

## Ask your first question

HIVE can run entirely on your own computer with [Ollama](https://ollama.com). Pull the model, then ask:

```bash
ollama pull qwen3.6:35b-a3b-q4_K_M
chb ask "Should a command-line tool print its version and exit 0 on --version?" --profile local-fast
```

On a computer with 8 GB of memory, pull `qwen3.5:4b` and use `--profile local-8gb` instead.

To use a cloud model, set one API key and leave out the profile:

```bash
export ANTHROPIC_API_KEY=...   # or OPENAI_API_KEY, or GEMINI_API_KEY
chb ask "Should a command-line tool print its version and exit 0 on --version?"
```

The answer comes back in three lines:

```text
Quorum: convergence high; tally support 8, conditional 1; plurality support, margin 7; no dissent written
∇ fired: architect↔pragmatist (support); empiricist↔pragmatist (support); empiricist↔scholar (support); …
Verdict: support — Implement `--version` to print `Name vX.Y.Z` to stdout and exit 0, following the dominant POSIX/GNU convention …
```

The first line says how strongly the foragers agreed. The second names the pairs of related foragers who reached the same view on their own. The third is the queen's verdict. Add `--json` to get the whole answer, her full report included, as one JSON object.

## How it works

| Part | What it does |
|---|---|
| **Foragers** | Thirteen lenses, each a short markdown file in [`foragers/`](foragers/). `chb ask` uses a balanced nine of them by default, and `chb list` shows them all. You can write your own. |
| **The queen** | Reads the foragers' findings and writes one verdict, with how much they agreed. |
| **The comb** | The colony's memory. Findings are filed by topic and kept between runs, and you can ask what the hive believed at any past moment. |
| **The guard** | Checks findings before the verdict. A "guarantee" that depends on an unknown is refused when it is written, and again before the queen reads it. |

The guard's one-step rules and its check for circular reasoning are proved in Lean 4, in [`lean4/`](lean4/). The full walk through a finding's dependencies is enforced in code and covered by tests.

HIVE can also research on its own. Give it an open question, and the colony works in passes until the research settles or reaches the limit you set:

```bash
chb hive init --project demo
chb db-write gap '{"description":"What exit status should a command-line tool use for invalid usage?","priority":"critical"}'
chb agent-run workflows/hive.yaml --inputs '{"project":"demo","max_iterations":3}' --profile local-fast
```

Its findings go into the comb, and its summary into `workspace/demo/final-synthesis.md`. `chb hive report --project demo` shows where the colony stands.

## Everyday commands

```bash
chb list                               # the foragers, with the balanced nine starred
chb ask "<question>"                   # ask the balanced nine
chb ask "<question>" --foragers all    # ask all thirteen
chb ask "<question>" --json            # the whole answer as JSON
chb review                             # audit a repository through five lenses
chb proof --profile local-fast         # check that HIVE works on your machine
```

The [CLI reference](docs/cli-reference.md) lists every command and MCP tool.

## Use HIVE from an AI assistant

`chb-mcp` is an [MCP](https://modelcontextprotocol.io) server, so assistants such as Claude Desktop, Claude Code, Cursor and VS Code can use HIVE's 20 tools directly. It runs in Docker, so there's nothing else to install.

In VS Code, the **Install in VS Code** button at the top of this page sets it up in one click. For other hosts, add this to the host's MCP config:

```json
{
  "mcpServers": {
    "chb": {
      "type": "stdio",
      "command": "docker",
      "args": [
        "run", "--rm", "-i", "--init", "--pull=missing",
        "-v", "chb-workspace:/app/workspace",
        "-e", "HIVE_DB_PATH=/app/workspace/hive.db",
        "-e", "ANTHROPIC_API_KEY",
        "-e", "OPENAI_API_KEY",
        "-e", "GEMINI_API_KEY",
        "-e", "GH_TOKEN",
        "ghcr.io/chubby-honey-bee/hive:latest"
      ]
    }
  }
}
```

| Host | Config file | Top-level key |
|---|---|---|
| Cursor | `~/.cursor/mcp.json` | `mcpServers` |
| Claude Desktop (macOS) | `~/Library/Application Support/Claude/claude_desktop_config.json` | `mcpServers` |
| Claude Desktop (Windows) | `%APPDATA%\Claude\claude_desktop_config.json` | `mcpServers` |
| Claude Code | `.mcp.json` in your project, or `claude mcp add-json chb '<the chb object>'` | `mcpServers` |
| GitHub Copilot (VS Code) | `.vscode/mcp.json`, or your user `mcp.json` | `servers` |

Set your API key in the environment your assistant starts from. Docker passes it into the container by name, so the config needs no `env` block. That matters: some hosts don't expand `${…}` in an `env` block, and the provider then receives the literal text as a broken key.

## What we measured

We test HIVE against a single call to the same model, and we publish what we find, including where it falls short.

- **On closed questions, the swarm is not more accurate.** With qwen3.6:35b in every role, it scored 0.82, against 0.95 for one call of the same model.
- **What it adds is honest confidence.** Every answer it called high-convergence was right, 26 of 26. Its mistakes came with low agreement, so the quorum line tells you when to look closer.
- **Smaller models are not reliable judges of themselves.** At 4b and 8b, the foragers tend to make the same mistakes together, so their agreement is not a trustworthy signal.
- **Research did not make better plans for small coding tasks.** On twelve small Go tasks, HIVE's designs were not carried out better than one call's, and they cost 1.68 times the tokens.

The full reasoning is in [`docs/strategy.md`](docs/strategy.md), and the data is in [`docs/evidence/`](docs/evidence/).

## Configuration

- **Which provider runs.** `--provider`, then `HIVE_PROVIDER`, then the first API key HIVE finds (Anthropic, Gemini, OpenAI), then the `claude` or `gemini` command on your `PATH`.
- **Staying on your machine.** `--profile local-fast`, `local-small` or `local-8gb` sends every call to the server at `HIVE_LOCAL_BASE_URL` (Ollama's `http://localhost:11434/v1` by default) and refuses any run that would leave your computer.
- **Choosing models.** `--budget-mode premium`, `standard`, `cheap` or `free` picks a tier of models. Change the tiers in `~/.config/hive/models.yaml`, and see the current setup with `chb models tiers`.
- **Where data lives.** `HIVE_DB_PATH` names the database, `workspace/hive.db` by default.
- **Repeatable records.** `chb ask --deterministic --seed 42 --artifact run.json` saves a run with a hash, and `chb verify-artifact run.json` checks it later. The hash shows when output drifts; models don't promise identical text twice.
- **Citation checks.** `chb verify-citations` looks up open-access copies through Unpaywall, which asks for a contact address. Set `HIVE_UNPAYWALL_EMAIL` to yours first.

## Learn more

- [CLI reference](docs/cli-reference.md): every command and MCP tool
- [Foundations](docs/foundations.md): the theory behind the labels, the comb and the guard
- [Writing a forager](foragers/README.md): the forager format and how to add your own
- [Strategy](docs/strategy.md): why HIVE is built this way, and what was measured
- [Specs](docs/specs/): the detailed contract for each part
- [Contributing](CONTRIBUTING.md): the repository layout and how to send changes

## Contributing

Issues and pull requests are welcome. [`CONTRIBUTING.md`](CONTRIBUTING.md) explains how to build, test and propose a change. To report a security problem privately, see [`SECURITY.md`](SECURITY.md).

## License

[MIT](LICENSE). © 2026 Chubby Honey Bee Inc.

HIVE builds on the CDE, WASP and MSS framework from [chronomancy.io](https://chronomancy.io), and on the way real bee colonies decide things together.
