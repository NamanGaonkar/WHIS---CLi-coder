<p align="center">
  <img src="logo.jpg" alt="Whis Logo" width="160" height="160" style="border-radius: 20px;">
</p>

# WHIS

<p align="center">
  <a href="https://github.com/NamanGaonkar/WHIS---CLi-coder/releases/latest"><img alt="Version" src="https://img.shields.io/badge/version-0.2.60-amber?style=flat-square" /></a>
  <img alt="License" src="https://img.shields.io/badge/license-MIT-orange?style=flat-square" />
  <img alt="Platform" src="https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-black?style=flat-square" />
  <a href="https://github.com/NamanGaonkar/whis-desktop"><img alt="Whis Desktop" src="https://img.shields.io/badge/companion-Whis%20Desktop%20v4.1.8-orange?style=flat-square" /></a>
</p>

```
██╗    ██╗ ██╗  ██╗ ██╗ ███████╗
██║    ██║ ██║  ██║ ██║ ██╔════╝
██║ █╗ ██║ ███████║ ██║ ███████╗
██║███╗██║ ██╔══██║ ██║ ╚════██║
╚███╔███╔╝ ██║  ██║ ██║ ███████║
 ╚══╝╚══╝  ╚═╝  ╚═╝ ╚═╝ ╚══════╝
```

**WHIS** — a fast, terminal-native AI coding agent, built by Naman Gaonkar.

WHIS lives in your terminal, reads and edits your code, runs commands, and gets things done
with token-surgical precision. Strictly BYOK: your keys, your providers, direct connections.
No hosted gateway, no middleman, ever.

---

## Unified Workspace: CLI + Desktop

> **Unified Chat & Session Continuity:** Whis CLI is natively integrated with [**Whis Desktop**](https://github.com/NamanGaonkar/whis-desktop). Both share the same local database (`~/.local/share/whis/opencode.db`) and state directory. Any chat or coding session started in the terminal CLI immediately appears in Whis Desktop, and any session created in Desktop can be continued in the terminal seamlessly. All BYOK credentials, project history, and context are shared automatically.

---

## Install

### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/NamanGaonkar/WHIS---CLi-coder/main/install.ps1 | iex
```

### Linux / macOS (curl)

```bash
curl -fsSL https://raw.githubusercontent.com/NamanGaonkar/WHIS---CLi-coder/main/install.sh | sh
```

Then run:

```bash
whis
```

That's it. The amber banner opens and you're talking to WHIS.

## Connect a provider

WHIS needs your own API key — pick a provider, paste the key, go:

```bash
whis auth
```

Supported providers (direct endpoints, no relays):

| Provider | Endpoint |
|---|---|
| Ollama (local) | `http://localhost:11434` |
| Ollama Cloud | `https://ollama.com/v1` |
| DeepSeek | `https://api.deepseek.com` |
| OpenRouter | `https://openrouter.ai/api/v1` |
| OpenAI | `https://api.openai.com/v1` |
| Anthropic | `https://api.anthropic.com/v1` |
| Google Gemini | `https://generativelanguage.googleapis.com` |
| xAI (Grok) | `https://api.x.ai/v1` |
| Mistral | `https://api.mistral.ai/v1` |
| Moonshot (Kimi) | `https://api.moonshot.ai/v1` |
| Qwen (Alibaba) | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` |
| Z.ai (GLM) | `https://api.z.ai/api/paas/v4` |
| MiniMax | `https://api.minimax.io/v1` |
| Groq | `https://api.groq.com/openai/v1` |

Keys are stored locally in `~/.local/share/whis/auth.json` and sent only to the provider
you choose.

## Daily driving

```bash
whis                                # launch the TUI
whis run "fix the failing tests"    # one-shot prompt
whis run -m google/gemini-3.8-flash "explain this repo"
whis models                         # list every model in the catalog
whis --continue                     # resume the last session
```

Inside the TUI: `/models` to switch models, `/themes` for themes, `/help` for everything.

## Configuration

- Global: `~/.config/whis/whis.json`
- Per-project: `.whis/whis.json` in the repo root

Example — set a default model:

```json
{
  "model": "deepseek/deepseek-chat",
  "small_model": "deepseek/deepseek-chat"
}
```

## Building from source

Requires [bun](https://bun.sh):

```bash
bun install
bun run check      # typecheck
cd packages/whis && bun run script/build.ts --single --skip-embed-web-ui
# binary lands in packages/whis/dist/whis-<os>-<arch>/bin/whis
```

## Releases

Releases are automated: push a tag (`v0.2.33` style) and GitHub Actions builds binaries
for Windows, Linux and macOS (x64 + arm64), publishes checksums, and attaches everything
to the release. The version you see bottom-right in the TUI is the tag it was built from.

## License

MIT
