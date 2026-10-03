import { describe, expect } from "bun:test"
import fs from "fs/promises"
import path from "path"
import { Effect } from "effect"
import { LayerNode } from "@opencode-ai/core/effect/layer-node"
import { Global } from "@opencode-ai/core/global"
import { Memory } from "@/session/memory"
import { testEffect } from "../lib/effect"

const it = testEffect(LayerNode.compile(Memory.node))

describe("session.memory", () => {
  it.effect("dir resolves the global scope under the test home", () =>
    Effect.gen(function* () {
      const memory = yield* Memory.Service
      expect(yield* memory.dir("global")).toBe(path.join(Global.Path.home, Memory.GLOBAL_DIR))
    }),
  )

  it.effect("ensure seeds both default markdown files", () =>
    Effect.gen(function* () {
      const memory = yield* Memory.Service
      const dir = yield* memory.ensure("global")
      expect(yield* Effect.promise(() => fs.readdir(dir))).toEqual(
        expect.arrayContaining(["user_profile.md", "project_context.md"]),
      )
    }),
  )

  it.instance("blocks reads every markdown file from both scopes", () =>
    Effect.gen(function* () {
      const memory = yield* Memory.Service
      const blocks = yield* memory.blocks()
      const global = blocks.filter((block) => block.scope === "global")
      expect(global.map((block) => block.file)).toEqual(["project_context.md", "user_profile.md"])
      expect(blocks.filter((block) => block.scope === "project").length).toBeGreaterThan(0)
      expect(blocks.every((block) => block.content.trim().length > 0)).toBe(true)
    }),
  )

  it.effect("save replaces an existing key instead of duplicating it", () =>
    Effect.gen(function* () {
      const memory = yield* Memory.Service
      yield* memory.save({ scope: "global", file_name: "user_profile.md", content: "Styling: prefers Tailwind" })
      yield* memory.save({ scope: "global", file_name: "user_profile.md", content: "Styling: prefers UnoCSS" })
      const dir = yield* memory.dir("global")
      const content = yield* Effect.promise(() => fs.readFile(path.join(dir, "user_profile.md"), "utf8"))
      const bullets = content.split("\n").filter((line) => line.startsWith("- "))
      expect(bullets).toEqual(["- Styling: prefers UnoCSS"])
    }),
  )

  it.effect("forget removes matching bullets and reports the count", () =>
    Effect.gen(function* () {
      const memory = yield* Memory.Service
      yield* memory.save({ scope: "global", file_name: "project_context.md", content: "Build: uses Bun" })
      yield* memory.save({ scope: "global", file_name: "project_context.md", content: "Testing: uses bun test" })
      const removed = yield* memory.forget({ scope: "global", key_or_phrase: "Build:" })
      expect(removed).toBe(1)
      const dir = yield* memory.dir("global")
      const content = yield* Effect.promise(() => fs.readFile(path.join(dir, "project_context.md"), "utf8"))
      expect(content).toContain("Testing: uses bun test")
      expect(content).not.toContain("- Build: uses Bun")
    }),
  )

  it.effect("forget reports zero when nothing matches", () =>
    Effect.gen(function* () {
      const memory = yield* Memory.Service
      expect(yield* memory.forget({ scope: "global", key_or_phrase: "no-such-key-anywhere" })).toBe(0)
    }),
  )

  it.instance("project scope writes into the worktree", () =>
    Effect.gen(function* () {
      const memory = yield* Memory.Service
      const dir = yield* memory.dir("project")
      expect(dir.endsWith(Memory.PROJECT_DIR)).toBe(true)
      yield* memory.save({ scope: "project", file_name: "notes", content: "Convention: no semicolons" })
      const content = yield* Effect.promise(() => fs.readFile(path.join(dir, "notes.md"), "utf8"))
      expect(content).toContain("- Convention: no semicolons")
      expect(yield* memory.forget({ scope: "project", key_or_phrase: "semicolons" })).toBe(1)
    }),
  )
})