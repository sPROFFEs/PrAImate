import assert from 'node:assert/strict'
import { test } from 'node:test'
import { mkdtempSync, readFileSync, writeFileSync, rmSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { execFileSync, spawnSync } from 'node:child_process'

test('standalone runtime patch preserves module order, handles CRLF and fails on drift', () => {
  const dir = mkdtempSync(resolve(tmpdir(), 'praimate-runtime-test-'))
  const root = resolve(import.meta.dirname, '../..')
  const patch = resolve(root, 'scripts/praimate-code-runtime.mjs')
  const target = resolve(dir, 'packages/opencode/script')
  mkdirSync(target, { recursive: true })
  const build = resolve(target, 'build.ts')
  try {
    const pristine = readFileSync(resolve(root, 'third_party/opencode/packages/opencode/script/build.ts'), 'utf8').replace(/\r\n/g, '\n')
    for (const newline of ['\n', '\r\n']) {
      writeFileSync(build, pristine.replace(/\n/g, newline))
      execFileSync(process.execPath, [patch, dir])
      const first = readFileSync(build, 'utf8')
      assert.match(first, /praimate:runtime-module-order/)
      assert.match(first, /splitting: false,/)
      assert.doesNotMatch(first, /splitting: true,/)
      execFileSync(process.execPath, [patch, dir])
      assert.equal(readFileSync(build, 'utf8'), first)
    }
    writeFileSync(build, 'upstream build changed')
    assert.notEqual(spawnSync(process.execPath, [patch, dir]).status, 0)
  } finally { rmSync(dir, { recursive: true, force: true }) }
})
