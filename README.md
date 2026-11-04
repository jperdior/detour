# detour

Keep coding agents from reading large files with an expensive model.

A hook blocks whole-file reads above a line threshold and tells the agent to ask a question instead. `detour read` sends the question and the files to a cheap model (DeepSeek by default, or anything OpenAI-compatible, including local Ollama) and returns a compact answer with line numbers. The expensive model only sees the answer.

The idea comes from Spotify's [Portal + shunt](https://engineering.atspotify.com/2026/9/portal-by-spotify-cut-my-claude-code-token-usage-by-90) (about 90% fewer tokens on bulk reads in their tests). detour is a self-hosted, provider-agnostic take on it. Other public ports that measured carefully found small or no net savings, so **this project measures first**: the A/B benchmark is the core of v0.

> Status: experimental. Claude Code only, bulk-read only. No server or UI yet (see the roadmap).

## How it works

| Layer | What it does |
|---|---|
| **Hook** (`detour hook claude`) | Claude Code `PreToolUse` on `Read` and `Bash`. Denies whole reads of files over `DETOUR_MIN_LINES` (default 350), including `cat`/`less`/`head`/`tail` with large counts. Targeted reads (`offset` + `limit` ≤ threshold) pass. Fails open on any error. |
| **Delegate** (`detour read`) | Wraps files in `<file>` tags with numbered lines, sends them with the question to the mode's model, prints the answer. Refuses files that look like secrets (`.env`, `*.pem`, keys...). |
| **Skill** (`detour-read`) | Tells the agent when to delegate and when not to (editing, subtle debugging, small files). |
| **Ledger** | One JSON line per blocked read, targeted read and delegation, with tokens, cost and latency. `detour stats` summarises it. |
| **Benchmark** (`detour bench`) | Runs real Claude Code sessions on fixed scenarios with and without the plugin and reports net cost, quality and latency. |

## Install

Requires Go 1.23+ and Claude Code.

```bash
go install github.com/jperdior/detour/cmd/detour@latest   # puts detour on your PATH
export DEEPSEEK_API_KEY=sk-...

claude plugin marketplace add jperdior/detour
claude plugin install detour@detour
```

Try it on any file:

```bash
detour read -q "Which functions handle retries, and what backoff do they use?" internal/client.go
```

Works with a Claude subscription or an API key: the hook and plugin don't care how Claude Code is authenticated. The delegate model is billed separately by its own provider.

## Benchmark

```bash
git clone https://github.com/jperdior/detour && cd detour
export DEEPSEEK_API_KEY=sk-...
make bench REPS=3 MODEL=sonnet           # or: ./bin/detour bench run -reps 3 -only run-hooks
```

Each session runs `claude -p` in a pinned checkout of the scenario's repo, with the user's own settings, plugins and MCP servers disabled (`--setting-sources project,local --strict-mcp-config`). The two variants differ only in `--plugin-dir plugin/`. Reps are interleaved so drift affects both variants alike.

Per session it records, from Claude Code's own JSON result, `total_cost_usd`, input/cache/output tokens, turns and duration. From the ledger it adds blocks, delegations and delegate cost. It also checks the answer against the scenario's `expect` regexes, so cheaper but wrong doesn't count as a win.

```
net cost = Claude cost + delegate model cost
```

Results go to `bench/results/<timestamp>/` (`results.jsonl`, `ledger.jsonl`, `raw/`, `report.md`).

On a subscription, `total_cost_usd` is the list-price equivalent: you don't pay it, but it's the right number to compare variants, and it tracks how fast you use your plan's limits.

### Scenarios

`bench/scenarios/*.yaml`, currently against [spf13/cobra](https://github.com/spf13/cobra) v1.10.2:

| Scenario | What it tests |
|---|---|
| `run-hooks` | Answer in `command.go` (2072 lines) |
| `completion-directives` | Answer in `completions.go` (1020 lines) |
| `unknown-command` | Cross-file: `args.go` (small) + `command.go` (large) |
| `small-file-control` | Control: only `args.go` (131 lines); detour should change nothing |

Add your own: `name`, `repo`, `ref`, `prompt`, and `expect` (case-insensitive regexes the answer must match). Scenarios that make the agent read large files whole are where delegation can pay off. If the agent can answer with `grep`, it usually will, and detour won't help.

## Configuration

`detour config > ~/.config/detour/config.yaml` and edit. It has three sections:

- `providers`: OpenAI-compatible endpoints (DeepSeek, OpenRouter, Ollama preset).
- `models`: provider, API model id, and price per million tokens (keep these current, they drive the cost numbers).
- `modes`: declarative workers: model, temperature, limits and instructions. `bulk-reader` is the default.

| Env var | Default | |
|---|---|---|
| `DEEPSEEK_API_KEY` | | Key for the default model |
| `DETOUR_MODEL` | mode's model | e.g. `qwen2.5-coder` to stay local |
| `DETOUR_MIN_LINES` | `350` | Hook threshold |
| `DETOUR_CONFIG` | `~/.config/detour/config.yaml` | |
| `DETOUR_LEDGER` | `~/.local/state/detour/ledger.jsonl` | |

## Known limits

- Compound shell commands (`cat big.go | grep x`, redirects) pass through, as in shunt. A smarter shell parser is on the roadmap.
- The ledger's token figures for blocked reads are estimates (bytes/4). The benchmark uses real billed tokens.
- Delegation adds latency (typically seconds to tens of seconds) and the cheap model can miss subtle details. Don't route debugging or safety-critical reading through it.
- Sending code to a third-party model is a privacy decision. Use a local model if that matters.

## Roadmap

1. ~~Hook + delegate + ledger + A/B benchmark for Claude Code~~ (this release)
2. Better shell coverage (`sed -n`, `awk`, `rg` without limits, pipelines) and per-session read budgets
3. `code-writer` mode (generate tests/stubs from a reference file, write to disk)
4. `detour serve`: mode server in Docker, shared by a team, with a web UI for modes and a net-cost dashboard
5. Adapters for Codex CLI, Gemini CLI, Copilot CLI, Cursor, Devin, Cline, opencode (most expose a `PreToolUse`-style hook), MCP as a fallback

## Prior art

- Spotify, [Portal by Spotify cut my Claude Code token usage by 90%](https://engineering.atspotify.com/2026/9/portal-by-spotify-cut-my-claude-code-token-usage-by-90) and [spotify/portal-ai-plugins](https://github.com/spotify/portal-ai-plugins)
- [shunt-anywhere](https://github.com/SalehB1/shunt-anywhere), [cofload](https://github.com/jx-grxf/cofload), [shuntkit](https://github.com/melonkernel/shuntkit), [claude-shunt](https://github.com/jarvisrchen/claude-shunt), [haiku-shunt](https://github.com/nbrosse/haiku-shunt)

## License

MIT
