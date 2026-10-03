import { afterAll, describe, expect, test } from "bun:test"
import fs from "fs"
import os from "os"
import path from "path"
import { compareSemver, removeStaleFiles, replaceBinary } from "@/cli/cmd/upgrade"

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

// The installer must never report success while leaving a stale binary on PATH,
// and it must never leave the user with no binary at all. These exercise the
// real implementations rather than restating their logic.
const roots: string[] = []

function makeDir(name: string) {
  const dir = path.join(os.tmpdir(), `whis-${name}-${process.pid}-${roots.length}`)
  fs.rmSync(dir, { recursive: true, force: true })
  fs.mkdirSync(dir, { recursive: true })
  roots.push(dir)
  return dir
}

describe("replaceBinary", () => {
  test("replaces an existing binary and reports success", () => {
    const dir = makeDir("replace")
    const source = path.join(dir, "source.bin")
    const target = path.join(dir, "target.bin")
    fs.writeFileSync(source, "new-build")
    fs.writeFileSync(target, "old-build")

    expect(replaceBinary(source, target)).toBe(true)
    expect(fs.readFileSync(target, "utf8")).toBe("new-build")
  })

  test("creates the target when it does not exist yet", () => {
    const dir = makeDir("fresh")
    const source = path.join(dir, "source.bin")
    const target = path.join(dir, "nested", "target.bin")
    fs.writeFileSync(source, "new-build")

    expect(replaceBinary(source, target)).toBe(true)
    expect(fs.readFileSync(target, "utf8")).toBe("new-build")
  })

  test("leaves the old binary in place when the new one cannot be written", () => {
    const dir = makeDir("rollback")
    const target = path.join(dir, "target.bin")
    fs.writeFileSync(target, "old-build")

    // A missing source makes the copy fail. replaceBinary must not leave the
    // user with nothing at the target path.
    const ok = replaceBinary(path.join(dir, "does-not-exist.bin"), target)

    expect(ok).toBe(false)
    expect(fs.existsSync(target)).toBe(true)
    expect(fs.readFileSync(target, "utf8")).toBe("old-build")
  })
})

describe("removeStaleFiles", () => {
  test("sweeps .old and .new once a live binary is present", () => {
    const dir = makeDir("sweep")
    fs.writeFileSync(path.join(dir, "whis.exe"), "live")
    fs.writeFileSync(path.join(dir, "whis.exe.old"), "leftover")
    fs.writeFileSync(path.join(dir, "whis.exe.new"), "staged")

    removeStaleFiles(dir, "whis.exe")

    expect(fs.existsSync(path.join(dir, "whis.exe.old"))).toBe(false)
    expect(fs.existsSync(path.join(dir, "whis.exe.new"))).toBe(false)
    expect(fs.existsSync(path.join(dir, "whis.exe"))).toBe(true)
  })

  test("restores an orphaned .old instead of deleting the only binary", () => {
    const dir = makeDir("orphan")
    fs.writeFileSync(path.join(dir, "whis.exe.old"), "only-surviving-copy")

    removeStaleFiles(dir, "whis.exe")

    expect(fs.existsSync(path.join(dir, "whis.exe"))).toBe(true)
    expect(fs.readFileSync(path.join(dir, "whis.exe"), "utf8")).toBe("only-surviving-copy")
    expect(fs.existsSync(path.join(dir, "whis.exe.old"))).toBe(false)
  })

  test("is a no-op on an empty directory", () => {
    const dir = makeDir("empty")
    expect(() => removeStaleFiles(dir, "whis.exe")).not.toThrow()
  })
})

afterAll(() => {
  for (const dir of roots) fs.rmSync(dir, { recursive: true, force: true })
})