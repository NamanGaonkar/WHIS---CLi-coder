// WHIS wordmark used for plain-text (non-TUI) output, e.g. session epilogue.
// The exact WHIS banner art from the Go TUI, with the molten amber gradient
// (deep ember -> bright flame -> pale gold), one color per row.
const banner = [
  "██╗    ██╗ ██╗  ██╗ ██╗ ███████╗",
  "██║    ██║ ██║  ██║ ██║ ██╔════╝",
  "██║ █╗ ██║ ███████║ ██║ ███████╗",
  "██║███╗██║ ██╔══██║ ██║ ╚════██║",
  "╚███╔███╔╝ ██║  ██║ ██║ ███████║",
  " ╚══╝╚══╝  ╚═╝  ╚═╝ ╚═╝ ╚══════╝",
]

const bannerColors = ["#7A3410", "#B45A16", "#E07B1F", "#FF9F1C", "#FFB84D", "#FFD166"]

// Convert #rrggbb to a truecolor SGR sequence (38;2;r;g;b needs decimals).
function sgr(hex: string) {
  const n = parseInt(hex.slice(1), 16)
  return `\x1b[38;2;${(n >> 16) & 255};${(n >> 8) & 255};${n & 255}m`
}

const reset = "\x1b[0m"
const bold = "\x1b[1m"
const dim = "\x1b[90m"

function wordmark(pad = "") {
  return banner.map(
    (line, index) => `${pad}${sgr(bannerColors[index % bannerColors.length])}${line}${reset}`,
  )
}

export function sessionEpilogue(input: { title: string; sessionID?: string }) {
  const weak = (text: string) => `${dim}${text.padEnd(10, " ")}${reset}`
  return [
    ...wordmark("  "),
    "",
    `  ${weak("Session")}${bold}${input.title}${reset}`,
    `  ${weak("Continue")}${bold}whis -s ${input.sessionID}${reset}`,
    "",
  ].join("\n")
}
