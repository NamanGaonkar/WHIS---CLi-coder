import path from "node:path"
import os from "node:os"
import { existsSync, readFileSync } from "node:fs"
import { ConfigMCPV1 } from "./v1/config/mcp"

// WHIS: import MCP servers configured for OTHER tools so users who already
// set something up (VS Code Copilot MCP, Cursor, Claude Desktop) get it
// working in WHIS with zero configuration. Callers must treat the result as
// the LOWEST-precedence layer: any explicit whis config entry (global or
// project) overrides a same-named import.
export function detectImportedMcp(): Record<string, ConfigMCPV1.Info> {
  const home = os.homedir()
  const sources: string[] = [
    path.join(home, "AppData", "Roaming", "Code", "User", "mcp.json"), // VS Code
    path.join(home, ".vscode", "mcp.json"), // VS Code (alt)
    path.join(home, ".cursor", "mcp.json"), // Cursor
    path.join(home, ".whis", "mcp.json"), // WHIS legacy file (Claude Desktop shape)
    path.join(home, ".claude", "claude_desktop_config.json"), // Claude Desktop
  ]
  const out: Record<string, ConfigMCPV1.Info> = {}
  for (const file of sources) {
    if (!existsSync(file)) continue
    try {
      const parsed = JSON.parse(readFileSync(file, "utf8").replace(/^\uFEFF/, ""))
      // VS Code uses { servers: {...} }; Claude Desktop / whis use { mcpServers: {...} }.
      const servers =
        (parsed.servers && typeof parsed.servers === "object" ? parsed.servers : undefined) ??
        (parsed.mcpServers && typeof parsed.mcpServers === "object" ? parsed.mcpServers : undefined)
      if (!servers) continue
      for (const [name, value] of Object.entries(servers as Record<string, any>)) {
        if (!value || typeof value !== "object" || out[name]) continue
        if (value.disabled === true) continue
        if (typeof value.command === "string") {
          const args = Array.isArray(value.args) ? value.args.map(String) : []
          out[name] = {
            type: "local",
            command: [value.command, ...args],
            ...(value.cwd ? { cwd: String(value.cwd) } : {}),
            ...(value.env && typeof value.env === "object"
              ? { environment: Object.fromEntries(Object.entries(value.env).map(([k, v]) => [k, String(v)])) }
              : {}),
          }
        } else if (typeof value.url === "string") {
          out[name] = { type: "remote", url: value.url }
        }
      }
    } catch {
      // Malformed foreign config - skip silently.
    }
  }
  return out
}
