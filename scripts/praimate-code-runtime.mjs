import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

const buildPath = resolve(process.argv[2], 'packages/opencode/script/build.ts')
const source = readFileSync(buildPath, 'utf8').replace(/\r\n/g, '\n')
const marker = 'praimate:runtime-module-order'
if (!source.includes(marker)) {
  const anchor = '    splitting: true,'
  if (!source.includes(anchor)) throw new Error('PrAImate runtime patch anchor missing; review the upstream Bun build')
  // Split Bun bundles capture an uninitialized cyclic FileSystem dependency.
  // Every prompt then fails before reaching its model.
  // Keep lazy imports, but let one bundle preserve module initialization order.
  writeFileSync(buildPath, source.replace(anchor,
    '    // ' + marker + ' — preserve cyclic service initialization in standalone builds.\n' +
    '    splitting: false,'))
}
