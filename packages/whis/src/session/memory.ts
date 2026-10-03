import fs from "fs/promises"
import path from "path"
import { Context, Effect, Layer } from "effect"
import { LayerNode } from "@opencode-ai/core/effect/layer-node"
import { Global } from "@opencode-ai/core/global"
import { InstanceState } from "@/effect/instance-state"

/**
 * WHIS persistent memory.
 *
 * Two file-backed scopes, both plain markdown so they stay hand-editable:
 *   - global  ~/.whis/memory/          (user preferences, style, tooling)
 *   - project <worktree>/.whis/memory/ (architecture decisions, quirks)
 *
 * The whole directory is read into the system prompt on every turn, so the
 * agent can answer "what do I prefer?" in a brand new session with no
 * conversational history.
 */

export type Scope = "global" | "project"

export const GLOBAL_DIR = path.join(".whis", "memory")
export const PROJECT_DIR = path.join(".whis", "memory")

const SEEDS: Record<string, string> = {
  "user_profile.md": `# User profile

_Preferences that persist across every WHIS session. Edit freely - WHIS reads this
file on every prompt._

## Styling

## Preferred packages

## Coding style

## Tooling
`,
  "project_context.md": `# Project context

_Architecture decisions, conventions and known quirks for this repo. Edit freely -
WHIS reads this file on every prompt._

## Architecture decisions

## Installed dependencies

## Conventions

## Known quirks
`,
}

export interface Block {
  scope: Scope
  file: string
  content: string
}

export interface Interface {
  readonly dir: (scope: Scope) => Effect.Effect<string>
  readonly ensure: (scope: Scope) => Effect.Effect<string>
  readonly blocks: () => Effect.Effect<Block[]>
  readonly save: (input: { scope: Scope; file_name: string; content: string }) => Effect.Effect<string>
  readonly forget: (input: { scope: Scope; key_or_phrase: string }) => Effect.Effect<number>
}

export class Service extends Context.Service<Service, Interface>()("@opencode/Memory") {}

/**
 * Effect.promise rejections surface as defects, not typed errors, so every
 * filesystem read here swallows inside the callback and always succeeds.
 */
function read<T>(fn: () => Promise<T>, fallback: T): Effect.Effect<T> {
  return Effect.promise(async () => {
    try {
      return await fn()
    } catch {
      return fallback
    }
  })
}

function normalizeName(input: string) {
  const name = path.basename(input.trim()).replace(/[^\w.-]/g, "-")
  if (!name) throw new Error("file_name must not be empty")
  return name.endsWith(".md") ? name : `${name}.md`
}

/** Leading `- Styling: ...` bullets are keyed by the text before the colon. */
function keyOf(line: string) {
  const stripped = line.replace(/^\s*[-*]\s+/, "")
  const head = stripped.split(/[:：]/)[0]
  return (head ?? "").trim().toLowerCase()
}

function isBullet(line: string) {
  return line.trimStart().startsWith("- ")
}

const layer = Layer.effect(
  Service,
  Effect.gen(function* () {
    const dir = Effect.fn("Memory.dir")(function* (scope: Scope) {
      if (scope === "global") return path.join(Global.Path.home, GLOBAL_DIR)
      const ctx = yield* InstanceState.context
      return path.join(ctx.worktree, PROJECT_DIR)
    })

    const ensure = Effect.fn("Memory.ensure")(function* (scope: Scope) {
      const target = yield* dir(scope)
      yield* Effect.promise(() => fs.mkdir(target, { recursive: true }))
      for (const [name, content] of Object.entries(SEEDS)) {
        const file = path.join(target, name)
        const exists = yield* read(() => fs.access(file).then(() => true), false)
        if (!exists) yield* Effect.promise(() => fs.writeFile(file, content, "utf8"))
      }
      return target
    })

    const blocks = Effect.fn("Memory.blocks")(function* () {
      const out: Block[] = []
      for (const scope of ["global", "project"] as const) {
        const target = yield* dir(scope)
        const files = yield* read(() => fs.readdir(target), [] as string[])
        for (const name of files.toSorted()) {
          if (!name.endsWith(".md")) continue
          const content = yield* read(() => fs.readFile(path.join(target, name), "utf8"), "")
          if (!content.trim()) continue
          out.push({ scope, file: name, content: content.trim() })
        }
      }
      return out
    })

    const save = Effect.fn("Memory.save")(function* (input: { scope: Scope; file_name: string; content: string }) {
      const target = yield* ensure(input.scope)
      const name = normalizeName(input.file_name)
      const entry = input.content.trim()
      if (!entry) throw new Error("content must not be empty")

      const file = path.join(target, name)
      const existing = yield* read(() => fs.readFile(file, "utf8"), "")

      // Merge by leading key so re-saving the same preference replaces the
      // stale line instead of growing an ever-duplicating list.
      const incomingKey = keyOf(entry)
      const lines = existing.split(/\r?\n/)
      const kept =
        incomingKey.length > 2 ? lines.filter((line) => !(isBullet(line) && keyOf(line) === incomingKey)) : lines
      const next = [...kept, `- ${entry.replace(/^\s*[-*]\s+/, "")}`].join("\n").replace(/\n{3,}/g, "\n\n")
      yield* Effect.promise(() => fs.writeFile(file, next.trimEnd() + "\n", "utf8"))
      return file
    })

    const forget = Effect.fn("Memory.forget")(function* (input: { scope: Scope; key_or_phrase: string }) {
      const target = yield* ensure(input.scope)
      const needle = input.key_or_phrase.trim().toLowerCase()
      if (!needle) throw new Error("key_or_phrase must not be empty")

      let removed = 0
      const files = yield* read(() => fs.readdir(target), [] as string[])
      for (const name of files) {
        if (!name.endsWith(".md")) continue
        const file = path.join(target, name)
        const content = yield* read(() => fs.readFile(file, "utf8"), "")
        if (!content) continue
        const lines = content.split(/\r?\n/)
        const keptLines = lines.filter((line) => {
          if (!isBullet(line)) return true
          const hit = line.toLowerCase().includes(needle)
          if (hit) removed++
          return !hit
        })
        if (keptLines.length !== lines.length) {
          const next = keptLines.join("\n").replace(/\n{3,}/g, "\n\n")
          yield* Effect.promise(() => fs.writeFile(file, next.trimEnd() + "\n", "utf8"))
        }
      }
      return removed
    })

    return Service.of({ dir, ensure, blocks, save, forget })
  }),
)

export const node = LayerNode.make({
  service: Service,
  layer,
  deps: [],
})

export * as Memory from "./memory"