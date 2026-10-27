import { Effect } from "effect"
import { effectCmd } from "../effect-cmd"
import { withNetworkOptions, resolveNetworkOptions } from "../network"
import { Flag } from "@opencode-ai/core/flag/flag"

export const ServeCommand = effectCmd({
  command: "serve",
  builder: (yargs) => withNetworkOptions(yargs),
  describe: "starts a headless whis server",
  // Server loads instances per-request via x-opencode-directory header — no
  // need for an ambient project InstanceContext at startup.
  instance: false,
  handler: Effect.fn("Cli.serve")(function* (args) {
    const { Server } = yield* Effect.promise(() => import("../../server/server"))
    if (!Flag.OPENCODE_SERVER_PASSWORD) {
      console.log("Warning: OPENCODE_SERVER_PASSWORD is not set; server is unsecured.")
    }
    const opts = yield* resolveNetworkOptions(args)
    const server = yield* Effect.promise(() => Server.listen(opts))
    console.log(`whis server listening on http://${server.hostname}:${server.port}`)

    if (args.service) {
      const stateDir =
        process.env.XDG_STATE_HOME ||
        (process.env.HOME ? `${process.env.HOME}/.local/state` : `${process.env.USERPROFILE}/.local/state`)
      const whisState = `${stateDir}/whis`
      const fs = yield* Effect.promise(() => import("fs/promises"))
      yield* Effect.promise(() => fs.mkdir(whisState, { recursive: true }))
      const reg = {
        version: process.env.OPENCODE_VERSION ?? "0.2.60",
        url: `http://${server.hostname}:${server.port}`,
        pid: process.pid,
      }
      yield* Effect.promise(() => fs.writeFile(`${whisState}/service.json`, JSON.stringify(reg, null, 2)))
    }

    yield* Effect.never
  }),
})
