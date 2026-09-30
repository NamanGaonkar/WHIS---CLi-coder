// WHIS provider-catalog patch rules.
//
// Applied in TWO places so every catalog source is normalized:
//   1. packages/whis/script/generate.ts — patches the snapshot embedded into
//      the binary at build time (OPENCODE_MODELS_DEV).
//   2. packages/core/src/models-dev.ts — patches whatever loads at runtime
//      (on-disk cache from an older WHIS, or a fresh models.dev fetch),
//      because models.dev upstream has no `ollama` entry and still lists
//      hosted gateways WHIS must never ship.
export function patchWhisCatalog(catalog: Record<string, any>): Record<string, any> {
  const WHIS_PROVIDERS: Record<string, { api?: string; env: string[]; name?: string }> = {
    ollama: { api: "http://localhost:11434/v1", env: [], name: "Ollama (local)" },
    "ollama-cloud": { api: "https://ollama.com/v1", env: ["WHIS_OLLAMA_KEY"] },
    deepseek: { api: "https://api.deepseek.com", env: ["WHIS_DEEPSEEK_KEY"] },
    anthropic: { env: ["WHIS_ANTHROPIC_KEY"] },
    openai: { env: ["WHIS_OPENAI_KEY"] },
    openrouter: { api: "https://openrouter.ai/api/v1", env: ["WHIS_OPENROUTER_KEY"] },
    google: { env: ["WHIS_GEMINI_KEY"] },
    xai: { env: ["WHIS_XAI_KEY"] },
    mistral: { env: ["WHIS_MISTRAL_KEY"] },
    moonshotai: { api: "https://api.moonshot.ai/v1", env: ["WHIS_MOONSHOT_KEY"] },
    alibaba: {
      api: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
      env: ["WHIS_QWEN_KEY"],
    },
    zai: { api: "https://api.z.ai/api/paas/v4", env: ["WHIS_ZAI_KEY"] },
    minimax: { api: "https://api.minimax.io/v1", env: ["WHIS_MINIMAX_KEY"] },
    groq: { env: ["WHIS_GROQ_KEY"] },
  }

  for (const [id, patch] of Object.entries(WHIS_PROVIDERS)) {
    let provider = catalog[id]
    if (!provider) {
      // models.dev has no entry for this WHIS provider (e.g. local Ollama).
      // Create it so the provider shows up in /models.
      if (id !== "ollama") continue
      catalog[id] = {
        id,
        env: patch.env,
        name: patch.name ?? id,
        api: patch.api,
        npm: "@ai-sdk/openai-compatible",
        models: {},
      }
      provider = catalog[id]
    }
    provider.env = patch.env
    if (patch.api) provider.api = patch.api
    if (patch.name) provider.name = patch.name
  }

  // Local Ollama seed models; the daemon's live /api/tags discovery fills in
  // the rest at runtime.
  if (catalog.ollama && (!catalog.ollama.models || Object.keys(catalog.ollama.models).length === 0)) {
    const local = (id: string, name: string, ctx: number) => ({
      id,
      name,
      attachment: false,
      reasoning: false,
      temperature: true,
      tool_call: true,
      release_date: "2025-01-01",
      limit: { context: ctx, output: 8192 },
    })
    catalog.ollama.models ??= {}
    catalog.ollama.models = {
      "qwen3-coder:30b": local("qwen3-coder:30b", "Qwen3 Coder 30B", 262144),
      "qwen2.5-coder:7b": local("qwen2.5-coder:7b", "Qwen2.5 Coder 7B", 32768),
      "deepseek-r1:8b": local("deepseek-r1:8b", "DeepSeek R1 8B", 131072),
      "llama3.3:70b": local("llama3.3:70b", "Llama 3.3 70B", 131072),
      "gpt-oss:20b": local("gpt-oss:20b", "GPT-OSS 20B", 131072),
    }
  }

  // Strict BYOK: hosted gateways never ship in the WHIS catalog.
  for (const id of ["opencode", "opencode-go", "llmgateway", "zenmux"]) {
    delete catalog[id]
  }

  // Scrub stray "opencode" references from third-party provider entries
  // (doc URLs, endpoint paths). WHIS users should never see the old name.
  for (const [key, value] of Object.entries(catalog)) {
    const json = JSON.stringify(value)
    if (!json.toLowerCase().includes("opencode")) continue
    const cleaned = JSON.parse(json.replaceAll(/opencode/gi, "whis"))
    delete catalog[key]
    catalog[cleaned.id] = cleaned
  }

  return catalog
}
