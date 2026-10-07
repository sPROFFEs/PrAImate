import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import vm from 'node:vm'
import test from 'node:test'

async function handler(page, start, end, state) {
  const source = await readFile(new URL(`./${page}.svelte`, import.meta.url), 'utf8')
  const offset = source.indexOf(start)
  const code = source.slice(offset, source.indexOf(end, offset + start.length))
  const context = vm.createContext(state)
  vm.runInContext(code, context)
  return context
}
function deferred() { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }

test('Code ignores a late catalogue after switching CLI', async () => {
  const first = deferred(), second = deferred()
  const context = await handler('Code', '  async function loadModels()', '  // Refresh model suggestions', {
    cli: 'codex', modelLoadSeq: 0, modelSuggestions: [], modelLoading: false,
    api: { listCLIModels: cli => cli === 'codex' ? first.promise : second.promise },
  })
  const stale = context.loadModels()
  context.cli = 'praimate-code'
  const current = context.loadModels()
  second.resolve(['provider/model'])
  await current
  first.resolve(['codex-model'])
  await stale
  assert.deepEqual(Array.from(context.modelSuggestions), ['provider/model'])
  assert.equal(context.modelLoading, false)
})

for (const [page, name, start, end, field] of [
  ['Chats', 'cfgCliChanged', '  async function cfgCliChanged()', '  async function saveConfig()', 'cfg'],
  ['Editor', 'cfgCliChanged', '  async function cfgCliChanged()', '  async function saveConfig()', 'cfg'],
  ['Agents', 'dlgCliChanged', '  async function dlgCliChanged()', '  async function dlgPickFolder()', 'dlg'],
]) test(`${page} ignores a catalogue when its settings dialog is replaced or closed`, async () => {
  const request = deferred()
  const state = { [field]: { cli: 'codex', tools: '', suggestions: [] }, api: {
    listCLIModels: () => request.promise, studioListCLIModels: () => request.promise,
    executionCapabilities: async () => ({}),
  }, supportsLocalRouting: () => false, isLocalRoutable: () => false, normalizeToolsForCli: (_, value) => value }
  const context = await handler(page, start, end, state)
  const pending = context[name]()
  const replacement = { cli: 'codex', suggestions: ['new-dialog-model'] }
  context[field] = replacement
  request.resolve(['old-dialog-model'])
  await pending
  assert.deepEqual(replacement.suggestions, ['new-dialog-model'])
  const closing = context[name]()
  context[field] = null
  await closing
})
