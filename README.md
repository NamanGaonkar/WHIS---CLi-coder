<div align="center">

<img src="assets/whis-mark.svg" alt="WHIS — token-surgical coding agent" width="560"/>

<img src="assets/whis-term.svg" alt="whis terminal session" width="620"/>

[![Go](https://img.shields.io/badge/Go-1.23%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-7C3AED.svg)](LICENSE)

**Hyper-lean, token-surgical AI coding CLI with an overkill cybernetic TUI.**
BYO key. Local via Ollama or remote: DeepSeek · Anthropic · OpenAI · OpenRouter.

`whis` → type → ship. No setup wizard in your way.

</div>

---

## Install

**Go (recommended):**

```sh
go install github.com/whis-cli/whis/cmd/whis@latest
```

**curl (macOS / Linux / WSL):**

```sh
curl -fsSL https://raw.githubusercontent.com/whis-cli/whis/main/install.sh | sh
```

**npm:**

```sh
npm i -g whis-cli
```

**Windows (PowerShell):**

```powershell
go install github.com/whis-cli/whis/cmd/whis@latest
```

Or grab a static binary from [Releases](https://github.com/whis-cli/whis/releases)
(`whis-{windows,linux,darwin}-{amd64,arm64}`) — no runtime deps, no CGO.

## How it works

Start `whis` in any folder. You land on the splash with an input — that's it.
No config gate, no boot errors, no forced wizard.

**Type `/` and the command menu appears:**

```
 ❯ model       switch provider or model
   provider    manage API keys & providers
   sessions    resume a session from this folder
   task        run an isolated subagent task
   undo        roll back last change
   init        (re)generate WHIS.md
   help        all commands & keys
```

**model** → pick a **provider** first (Ollama local, Ollama Cloud, DeepSeek,
Anthropic, OpenAI, OpenRouter). If the provider is running locally, its
available models are listed automatically — you just arrow down and hit enter.
Cloud providers ask for a key inline the first time (masked input, saved to
`~/.whis/config.json`). Custom model IDs are supported where the provider
allows them.

**provider** → add or replace the API key for any provider at any time.

**sessions** → every session is tagged with the folder you opened it in, so
you only see history relevant to the project you're standing in. Resume one
and the full conversation is restored.

Your provider/model choice persists across restarts but is never required to
boot — WHIS starts identical whether you've configured anything or not.

## Usage

```sh
whis                    # full TUI in the current project
whis -m <model>         # skip the picker, bind a model up front
whis -p "fix the failing tests in pkg/x" -y   # headless one-shot, auto-approve
whis -r 20260926-153012 # resume a saved session (whis -l to list)
whis -undo              # roll back to the last whis snapshot
whis -init              # generate WHIS.md project guide
```

## The Token-Surgical Engine

| Tool | What it does |
|---|---|
| `locate_symbol` | extracts a function/type body via the symbol index — never whole files |
| `read_range` | numbered line slices; reads >120 lines are blocked unless forced |
| `apply_patch` | SEARCH/REPLACE edits with a **Levenshtein fuzzy applicator** (tolerates nearby-line drift) |
| `run_command` | sandboxed shell in workspace root, output capped at 40 lines |
| `search_codebase` | ripgrep-backed regex → `file:line` anchors |
| `list_tree` | pruned tree (skips `.git`, `node_modules`, `.venv`, …) |

**Prompt cache discipline:** the system prompt, WHIS.md guide and tool
manifests stay byte-identical across turns; Anthropic blocks carry
`cache_control: {"type":"ephemeral"}` and DeepSeek gets a maximally stable
prefix — expect >80% cache-hit rates on multi-turn sessions (watch the badge).

**Micro-compaction:** when estimated context crosses 60% of the model window,
stale tool logs are squashed into 2-line diagnostic vectors automatically.

## Agent loop & safety

Prompt → Plan → Tool invocation → Diff review → Terminal verification.

- Every shell command and file write opens an approval modal (`y/n/esc`)
- `--auto-approve` / `-y` for unattended runs
- `/undo` rolls back instantly (git shadow commits when in a repo, file
  snapshots in `~/.whis/undo` otherwise) — zero LLM tokens burned
- `/task "…"` runs a context-isolated subagent; only its final summary
  reaches the main session
- Sessions auto-snapshot to `~/.whis/sessions/` for pause/resume

## WHIS.md

`whis -init` (or `/init` in-session) inspects project markers
(`go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`, …) and writes a
project guide with build/test commands that WHIS pins into its cached system
prompt every session.

## Development

```sh
git clone https://github.com/whis-cli/whis && cd whis
make build   # → ./whis
make test vet
make release # cross-compiled artifacts in dist/
```

Go 1.23+, zero CGO — `GOOS/GOARCH` cross-compiles anywhere.

## License

MIT © WHIS contributors
