import { RGBA, TextAttributes } from "@opentui/core"
import { For, type JSX } from "solid-js"
import { tint, useTheme } from "../context/theme"
import { logo } from "../logo"

// WHIS banner: the exact WHIS art from the Go TUI, one molten gradient color
// per row (deep ember -> bright flame -> pale gold), matching whis startup.
const bannerColors = ["#7A3410", "#B45A16", "#E07B1F", "#FF9F1C", "#FFB84D", "#FFD166"]

export function Logo() {
  const { theme } = useTheme()

  const renderLine = (line: string, fg: RGBA, bold: boolean): JSX.Element[] => {
    const shadow = tint(theme.background, fg, 0.25)
    const attrs = bold ? TextAttributes.BOLD : undefined
    return Array.from(line).map((char) => {
      if (char === "_") {
        return (
          <text fg={fg} bg={shadow} attributes={attrs} selectable={false}>
            {" "}
          </text>
        )
      }
      if (char === "^") {
        return (
          <text fg={fg} bg={shadow} attributes={attrs} selectable={false}>
            ▀
          </text>
        )
      }
      if (char === "~") {
        return (
          <text fg={shadow} attributes={attrs} selectable={false}>
            ▀
          </text>
        )
      }
      if (char === ",") {
        return (
          <text fg={shadow} attributes={attrs} selectable={false}>
            ▄
          </text>
        )
      }
      return (
        <text fg={fg} attributes={attrs} selectable={false}>
          {char}
        </text>
      )
    })
  }

  return (
    <box>
      <For each={logo.left}>
        {(line, index) => {
          // Molten gradient: dim the left (WH) half, keep the right (IS) half
          // bright and bold, both tinted with the row's banner color.
          const rowColor = RGBA.fromHex(bannerColors[index() % bannerColors.length] as `#${string}`)
          const leftColor = tint(rowColor, theme.textMuted, 0.55)
          const rightColor = tint(rowColor, theme.primary, 0.35)
          return (
            <box flexDirection="row" gap={1}>
              <box flexDirection="row">{renderLine(line, leftColor, false)}</box>
              <box flexDirection="row">{renderLine(logo.right[index()], rightColor, true)}</box>
            </box>
          )
        }}
      </For>
    </box>
  )
}
