import path from "path"
import { fileURLToPath } from "url"
import { patchWhisCatalog } from "@opencode-ai/core/whis-catalog"

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const dir = path.resolve(__dirname, "..")

process.chdir(dir)

const modelsUrl = process.env.OPENCODE_MODELS_URL || "https://models.dev"
const raw = process.env.MODELS_DEV_API_JSON
  ? await Bun.file(process.env.MODELS_DEV_API_JSON).text()
  : await fetch(`${modelsUrl}/api.json`).then((x) => x.text())

// WHIS: patch the raw catalog into the strict-BYOK WHIS roster. The patch
// rules live in packages/core/src/whis-catalog.ts and are ALSO applied at
// runtime (models-dev.ts) so stale on-disk caches and fresh models.dev
// fetches get identical treatment (upstream models.dev has no `ollama`).
const catalog = patchWhisCatalog(JSON.parse(raw) as Record<string, any>)

export const modelsData = JSON.stringify(catalog)
console.log("Loaded WHIS provider catalog (strict BYOK, hosted gateways stripped)")
