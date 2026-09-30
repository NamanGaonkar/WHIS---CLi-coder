import type { Argv } from "yargs"
import { UpgradeCommand } from "./upgrade"

// `whis update` — friendly alias for `whis upgrade`.
// Users (and muscle memory from other CLIs) type "update", so make it real
// instead of dumping the help screen.
export const UpdateCommand = {
  ...UpgradeCommand,
  command: "update [target]",
  describe: "update whis to the latest or a specific version (alias of upgrade)",
}
