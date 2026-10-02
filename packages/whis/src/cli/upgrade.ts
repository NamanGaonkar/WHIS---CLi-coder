import { Config } from "@/config/config"
import { AppRuntime } from "@/effect/app-runtime"
import { Installation } from "@/installation"
import { InstallationVersion } from "@opencode-ai/core/installation/version"
import { GlobalBus } from "@/bus/global"

export async function upgrade() {
  const config = await AppRuntime.runPromise(Config.Service.use((cfg) => cfg.getGlobal()))

  // autoupdate: false = never auto-upgrade. Anything else means "auto-upgrade
  // when a newer release is available" (default in whisk.jsonc: "notify").
  if (config.autoupdate === false) return

  const method = await Installation.method()
  const latest = await Installation.latest(method).catch(() => {})
  if (!latest) return

  // Already at the latest release. Nothing to do. This check runs before any
  // popup emission so we never spam "update available" on a clean install.
  if (InstallationVersion === latest) return

  // WHIS: always show the update popup, for every user, on every newer release.
  // (`notify` = prompt the user in the TUI; we always show it so nobody misses
  // a release. Users who want nothing can set config.autoupdate = false.)
  GlobalBus.emit("event", {
    directory: "global",
    payload: {
      type: Installation.Event.UpdateAvailable.type,
      properties: { version: latest },
    },
  })

  // If auto-upgrade is actually a notification prompt (not "install silently"),
  // stop here. The user should accept/decline in the popup.
  if (config.autoupdate === "notify") return

  // Otherwise (default "notify" still auto-installs patch/minor? No: WHIS
  // auto-installs everything newer than the current version) go ahead and
  // upgrade.
  if (method === "unknown") return
  await Installation.upgrade(method, latest)
    .then(() =>
      GlobalBus.emit("event", {
        directory: "global",
        payload: {
          type: Installation.Event.Updated.type,
          properties: { version: latest },
        },
      }),
    )
    .catch(() => {})
}
