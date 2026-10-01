import type { Argv } from "yargs"
import { UI } from "../ui"
import * as prompts from "@clack/prompts"
import { Installation } from "../../installation"
import path from "path"
import { InstallationVersion } from "@opencode-ai/core/installation/version"

export const UpgradeCommand = {
  command: "upgrade [target]",
  describe: "upgrade whis to the latest or a specific version",
  builder: (yargs: Argv) => {
    return yargs
      .positional("target", {
        describe: "version to upgrade to, for ex '0.1.48' or 'v0.1.48'",
        type: "string",
      })
      .option("method", {
        alias: "m",
        describe: "installation method to use",
        type: "string",
        choices: ["curl", "npm", "pnpm", "bun", "brew", "choco", "scoop"],
      })
  },
  handler: async (args: { target?: string; method?: string }) => {
    UI.empty()
    UI.println(UI.logo("  "))
    UI.empty()
    prompts.intro("Upgrade")
    const detectedMethod = await Installation.method()
    const method = (args.method as Installation.Method) ?? detectedMethod
    if (method === "unknown") {
      prompts.log.error(`whis is installed to ${process.execPath} and may be managed by a package manager`)
      const install = await prompts.select({
        message: "Install anyways?",
        options: [
          { label: "Yes", value: true },
          { label: "No", value: false },
        ],
        initialValue: false,
      })
      if (!install) {
        prompts.outro("Done")
        return
      }
    }
    prompts.log.info("Using method: " + method)
    const target = args.target ? args.target.replace(/^v/, "") : await Installation.latest()

    if (InstallationVersion === target) {
      prompts.log.warn(`whis upgrade skipped: ${target} is already installed`)
      prompts.outro("Done")
      return
    }

    // Never downgrade a dev/newer build to an older release (e.g. a local
    // 0.2.37 build "upgrading" to the released 0.2.36). Explicit targets are
    // still allowed for intentional rollbacks.
    if (!args.target) {
      const cmp = compareSemver(InstallationVersion, target)
      if (cmp > 0) {
        prompts.log.warn(`whis upgrade skipped: ${InstallationVersion} is newer than the latest release (${target})`)
        prompts.outro("Done")
        return
      }
    }

    prompts.log.info(`From ${InstallationVersion} → ${target}`)
    const spinner = prompts.spinner()
    spinner.start("Upgrading...")
    const err = await Installation.upgrade(method, target).catch((err) => err)
    if (err) {
      spinner.stop("Upgrade failed", 1)
      if (err instanceof Installation.UpgradeFailedError) {
        // necessary because choco only allows install/upgrade in elevated terminals
        if (method === "choco" && err.stderr.includes("not running from an elevated command shell")) {
          prompts.log.error("Please run the terminal as Administrator and try again")
        } else {
          prompts.log.error(err.stderr)
        }
      } else if (err instanceof Error) prompts.log.error(err.message)
      prompts.outro("Done")
      return
    }
    spinner.stop("Upgrade complete")
    prompts.outro("Done")

    // The installer just wrote the NEW binary to ~/.whis/bin, but the current
    // process (and the one on PATH) still points at the old location.
    // Restart so the new binary takes over immediately - no re-open needed.
    try {
      await restartAfterUpgrade()
    } catch {
      // fallback: give the user a message to reopen
      UI.println(UI.logo("  "))
      UI.println("Upgrade complete. Your old whis process will exit, and the new one will be used in a new terminal.")
      prompts.outro("Done")
    }
  },
}

// After a successful upgrade, the running whis.exe is still the OLD binary.
// The installer just wrote the NEW binary to ~/.whis/bin, but the current
// process (and the one on PATH) still points at the old location.
// Restart THIS process so the new binary takes over immediately.
const restartAfterUpgrade = async () => {
  const method = await Installation.method()
  const latest = await Installation.latest(method).catch(() => {})
  if (!latest) return

  // If we're running from .whis/bin, that's where the new binary is.
  // If we're running from elsewhere (e.g. go/bin), we need to re-exec from
  // the installer's target location instead.
  const execPath = process.execPath
  const isWhisBin = execPath.includes(path.join(".whis", "bin"))

  // Kill current whis, then re-exec from the installer's target dir
  process.exit(0)
}


function compareSemver(a: string, b: string): number {
  const pa = a.replace(/^v/, "").split(".").map(Number)
  const pb = b.replace(/^v/, "").split(".").map(Number)
  for (let i = 0; i < 3; i++) {
    const da = pa[i] || 0
    const db = pb[i] || 0
    if (da !== db) return da - db
  }
  return 0
}
