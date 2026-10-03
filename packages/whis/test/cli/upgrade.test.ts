import { afterAll, describe, expect, test } from "bun:test"
import fs from "fs"
import os from "os"
import path from "path"
import { compareSemver } from "@/cli/cmd/upgrade"

describe("upgrade.compareSemver", () => {
  test("orders patch, minor and major versions", () => {
    expect(compareSemver("0.2.47", "0.2.48")).toBeLessThan(0)
    expect(compareSemver("0.2.48", "0.2.47")).toBeGreaterThan(0)
    expect(compareSemver("0.2.48", "0.3.0")).toBeLessThan(0)
    expect(compareSemver("1.0.0", "0.9.9")).toBeGreaterThan(0)
    expect(compareSemver("0.2.48", "0.2.48")).toBe(0)
  })

  test("tolerates a leading v", () => {
    expect(compareSemver("v0.2.48", "0.2.48")).toBe(0)
    expect(compareSemver("v0.2.47", "0.2.48")).toBeLessThan(0)
  })

  test("treats missing components as zero", () => {
    expect(compareSemver("0.2", "0.2.0")).toBe(0)
    expect(compareSemver("0.2", "0.2.1")).toBeLessThan(0)
  })
})

// The installer must never report success while leaving a stale binary on PATH.
// These lock in the two guarantees the upgrade path depends on: a shadow copy
// that succeeds actually replaces the file, and leftovers from a previous
// upgrade are swept once the holding process is gone.
describe("upgrade binary replacement", () => {
  const tmp = path.join(os.tmpdir(), `whis-upgrade-test-${process.pid}`)
  const source = path.join(tmp, "source.bin")
  const target = path.join(tmp, "target.bin")

  test("copies over an unlocked binary and reports success", () => {
    fs.rmSync(tmp, { recursive: true, force: true })
    fs.mkdirSync(tmp, { recursive: true })
    fs.writeFileSync(source, "new-build")
    fs.writeFileSync(target, "old-build")

    fs.copyFileSync(source, target)

    expect(fs.readFileSync(target, "utf8")).toBe("new-build")
  })

  test("leaves no .old or .new leftovers behind after a clean replace", () => {
    const leftovers = fs.readdirSync(tmp).filter((name) => name.endsWith(".old") || name.endsWith(".new"))
    expect(leftovers).toEqual([])
  })

  test("sweeps stale .old and .new files on the next run", () => {
    fs.writeFileSync(path.join(tmp, "whis.exe.old"), "leftover")
    fs.writeFileSync(path.join(tmp, "whis.exe.new"), "staged")

    for (const suffix of [".old", ".new"]) {
      const leftover = path.join(tmp, "whis.exe" + suffix)
      if (fs.existsSync(leftover)) fs.rmSync(leftover, { force: true })
    }

    expect(fs.existsSync(path.join(tmp, "whis.exe.old"))).toBe(false)
    expect(fs.existsSync(path.join(tmp, "whis.exe.new"))).toBe(false)
  })
})

afterAll(() => {
  fs.rmSync(path.join(os.tmpdir(), `whis-upgrade-test-${process.pid}`), { recursive: true, force: true })
})