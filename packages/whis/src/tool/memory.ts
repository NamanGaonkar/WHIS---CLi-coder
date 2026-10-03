import { Effect, Schema } from "effect"
import * as Tool from "./tool"
import DESCRIPTION_SAVE from "./memory-save.txt"
import DESCRIPTION_FORGET from "./memory-forget.txt"
import { Memory } from "../session/memory"

export const SaveParameters = Schema.Struct({
  target: Schema.Literals(["global", "project"]).annotate({
    description:
      'Which memory block to write. "global" is ~/.whis/memory/ (user preferences, styling, tooling). "project" is <repo>/.whis/memory/ (architecture decisions, conventions, known quirks).',
  }),
  file_name: Schema.String.annotate({
    description:
      'Markdown file to write inside the target folder, e.g. "user_profile.md" or "project_context.md". New names are created automatically.',
  }),
  content: Schema.String.annotate({
    description:
      "The note to remember. Lead with a short key followed by a colon so re-saving the same key replaces it (e.g. 'Styling: prefers Tailwind CSS with Lucide icons').",
  }),
})

export const ForgetParameters = Schema.Struct({
  target: Schema.Literals(["global", "project"]).annotate({
    description: 'Which memory block to clean. "global" is ~/.whis/memory/, "project" is <repo>/.whis/memory/.',
  }),
  key_or_phrase: Schema.String.annotate({
    description:
      "Text to match against stored bullets. Every bullet containing this phrase is removed from the target scope.",
  }),
})

type SaveMetadata = {
  target: "global" | "project"
  file: string
  path: string
}

export const SaveMemoryTool = Tool.define<typeof SaveParameters, SaveMetadata, Memory.Service>(
  "save_memory",
  Effect.gen(function* () {
    const memory = yield* Memory.Service
    return {
      description: DESCRIPTION_SAVE,
      parameters: SaveParameters,
      execute: (params: Schema.Schema.Type<typeof SaveParameters>, ctx: Tool.Context) =>
        Effect.gen(function* () {
          yield* ctx.ask({
            permission: "save_memory",
            patterns: [params.target, params.file_name],
            always: ["*"],
            metadata: { target: params.target, file_name: params.file_name },
          })
          const file = yield* memory
            .save({ scope: params.target, file_name: params.file_name, content: params.content })
            .pipe(Effect.orDie)
          yield* ctx.metadata({ title: `remembered ${params.file_name}` })
          return {
            title: `Remembered in ${params.target} memory`,
            output: `Saved to ${file}`,
            metadata: { target: params.target, file: params.file_name, path: file },
          }
        }).pipe(Effect.orDie),
    } satisfies Tool.DefWithoutID<typeof SaveParameters, SaveMetadata>
  }),
)

type ForgetMetadata = {
  target: "global" | "project"
  removed: number
}

export const ForgetMemoryTool = Tool.define<typeof ForgetParameters, ForgetMetadata, Memory.Service>(
  "forget_memory",
  Effect.gen(function* () {
    const memory = yield* Memory.Service
    return {
      description: DESCRIPTION_FORGET,
      parameters: ForgetParameters,
      execute: (params: Schema.Schema.Type<typeof ForgetParameters>, ctx: Tool.Context) =>
        Effect.gen(function* () {
          yield* ctx.ask({
            permission: "forget_memory",
            patterns: [params.target, params.key_or_phrase],
            always: ["*"],
            metadata: { target: params.target, key_or_phrase: params.key_or_phrase },
          })
          const removed = yield* memory
            .forget({ scope: params.target, key_or_phrase: params.key_or_phrase })
            .pipe(Effect.orDie)
          yield* ctx.metadata({ title: `forgot ${removed}` })
          return {
            title: removed === 0 ? "No matching memory" : `Forgot ${removed} ${params.target} ${removed === 1 ? "entry" : "entries"}`,
            output:
              removed === 0
                ? `No ${params.target} memory entry contained "${params.key_or_phrase}".`
                : `Removed ${removed} ${params.target} memory ${removed === 1 ? "entry" : "entries"} matching "${params.key_or_phrase}".`,
            metadata: { target: params.target, removed },
          }
        }).pipe(Effect.orDie),
    } satisfies Tool.DefWithoutID<typeof ForgetParameters, ForgetMetadata>
  }),
)