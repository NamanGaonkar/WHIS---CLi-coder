import type { Argv } from "yargs"
import { UI } from "../ui"
import * as prompts from "@clack/prompts"
import { Installation } from "../../installation"
import fs from "fs"
import { execFileSync } from "child_process"
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
      await restartAfterUpgrade(target)
    } catch {
      // fallback: give the user a message to reopen
      UI.println(UI.logo("  "))
      UI.println("Upgrade complete. Your old whis process will exit, and the new one will be used in a new terminal.")
      prompts.outro("Done")
    }
  },
}

// Replace `target` with the freshly installed `source`.
//
// Windows locks the image file of a RUNNING process, so a plain copy over a
// live whis.exe fails. Windows does allow RENAMING a running executable, so we
// move the stale binary aside (freeing the path), write the new one, and leave
// the old file for the OS to release on exit. This is what makes an upgrade
// actually stick instead of silently leaving the old binary on PATH.
function replaceBinary(source: string, target: string) {
  try {
    fs.copyFileSync(source, target)
    return true
  } catch {}

  const stale = `${target}.old`
  try {
    fs.rmSync(stale, { force: true })
  } catch {}
  try {
    fs.renameSync(target, stale)
  } catch {
    // Locked by something we cannot rename (AV, permissions). Stage the new
    // binary beside it so the next install picks it up.
    try {
      fs.copyFileSync(source, `${target}.new`)
    } catch {}
    return false
  }

  try {
    fs.copyFileSync(source, target)
  } catch {
    return false
  }
  // The renamed original is still mapped by the live process, so it usually
  // cannot be deleted yet. Sweep it on the next run instead of relying on a
  // detached retry loop.
  try {
    fs.rmSync(stale, { force: true })
  } catch {}
  return true
}

// Delete whis.exe.old / .new leftovers from earlier upgrades. These can only
// be removed once the process that had them mapped has exited.
function removeStaleFiles(dir: string, exeName: string) {
  try {
    for (const suffix of [".old", ".new"]) {
      const leftover = path.join(dir, exeName + suffix)
      if (fs.existsSync(leftover)) fs.rmSync(leftover, { force: true })
    }
  } catch {
    // best-effort
  }
}

// After a successful upgrade, the running whis.exe is still the old binary.
// The installer wrote the NEW binary to ~/.whis/bin, so two things must happen
// for the upgrade to stick:
//  1. Refresh every stale `whis` shadow on PATH (e.g. ~/go/bin) so a new
//     terminal resolves `whis` to the new binary.
//  2. Hand the terminal over to the freshly installed binary.
const restartAfterUpgrade = async (target: string) => {
  const home = process.env.HOME || process.env.USERPROFILE || ""
  const binDir = path.join(home, ".whis", "bin")
  const exeName = process.platform === "win32" ? "whis.exe" : "whis"
  const newBin = path.join(binDir, exeName)

  // Running from the canonical install dir already - nothing to refresh.
  if (path.resolve(process.execPath) === path.resolve(newBin)) {
    process.exit(0)
  }

  // 1. Refresh stale shadows on PATH so `whis` resolves to the new build.
  removeStaleFiles(binDir, exeName)
  for (const dir of (process.env.PATH || "").split(path.delimiter)) {
    if (!dir) continue
    const candidate = path.join(dir, exeName)
    try {
      if (!fs.existsSync(candidate)) continue
      if (path.resolve(candidate) === path.resolve(newBin)) continue
      if (path.resolve(candidate) === path.resolve(process.execPath)) {
        // This is the running binary. On Windows we cannot overwrite it while
        // it executes, so leave it: install.ps1 already updated this folder via
        // its own rename-aside step.
        continue
      }
      replaceBinary(newBin, candidate)
    } catch {
      // ignore individual shadow failures
    }
  }

  // 2. Verify the canonical install really reports the version we asked for
  //    before we claim success. A silently-failed swap must never be reported
  //    as done.
  try {
    const reported = execFileSync(newBin, ["--version"], { encoding: "utf8", timeout: 20_000 }).trim()
    if (!reported) {
      prompts.log.warn("Could not read the new binary version. Close whis and run `whis --version` to confirm.")
    } else if (reported.replace(/^v/, "") !== target.replace(/^v/, "")) {
      prompts.log.warn(
        `Installed binary reports ${reported} but ${target} was requested. Close whis and re-run the upgrade.`,
      )
    } else {
      prompts.log.info(`Verified: whis --version reports ${reported}`)
    }
  } catch {
    prompts.log.warn("Could not verify the new binary. Close whis and run `whis --version` to confirm.")
  }

  // 3. Hand the terminal over to the new binary.
  try {
    const { spawn } = await import("child_process")
    spawn(newBin, [], { stdio: "inherit", detached: true, env: { ...process.env } })
  } catch {
    // best-effort: the user can reopen whis; the new binary is already in place
  }
  process.exit(0)
}

export function compareSemver(a: string, b: string): number {
  const pa = a.replace(/^v/, "").split(".").map(Number)
  const pb = b.replace(/^v/, "").split(".").map(Number)
  for (let i = 0; i < 3; i++) {
    const da = pa[i] || 0
    const db = pb[i] || 0
    if (da !== db) return da - db
  }
  return 0
}
