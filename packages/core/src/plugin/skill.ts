/// <reference path="../markdown.d.ts" />

export * as SkillPlugin from "./skill"

import { define } from "./internal"
import { Effect } from "effect"
import { AbsolutePath } from "../schema"
import { SkillV2 } from "../skill"
import customizeWhisContent from "./skill/customize-whis.md" with { type: "text" }

export const CustomizeWhisContent = customizeWhisContent

export const Plugin = define({
  id: "skill",
  effect: Effect.fn(function* (ctx) {
    yield* ctx.skill.transform((draft) => {
      draft.source(
        SkillV2.EmbeddedSource.make({
          type: "embedded",
          skill: SkillV2.Info.make({
            name: "customize-whis",
            description:
              "Use ONLY when the user is editing or creating whis's own configuration: whis.json, whis.jsonc, files under .whis/, or files under ~/.config/whis/. Also use when creating or fixing whis agents, subagents, commands, skills, plugins, MCP servers, or permission rules. Do not use for the user's own application code, or for any project that is not configuring whis itself.",
            location: AbsolutePath.make("/builtin/customize-whis.md"),
            content: CustomizeWhisContent,
          }),
        }),
      )
    })
  }),
})
