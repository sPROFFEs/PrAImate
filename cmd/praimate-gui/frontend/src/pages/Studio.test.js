import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compile } from 'svelte/compiler'
import { confirmModal } from '../lib/stores.js'

test('duplicate open requests create only one installation confirmation', async () => {
  const source = await readFile(new URL('./Studio.svelte', import.meta.url), 'utf8')
  const script = source.match(/<script>([\s\S]*?)<\/script>/)[1]
    .replace('const session =', 'export const session =')
    .replace('async function openProject(', 'export async function openProject(')
  // Run the real script with Svelte's reactive updates. An empty fragment
  // isolates the confirmation flow from the browser and native editor.
  const { js } = compile(`<script>${script}</script>`, { generate: 'dom', accessors: true })
  const code = js.code.replace(/(["'])(svelte(?:\/[^"']+)?|\.\.\/lib\/[^"']+)\1/g, (_, quote, name) =>
    JSON.stringify(name.startsWith('svelte') ? import.meta.resolve(name) : new URL(name, import.meta.url).href))
  const { default: Studio } = await import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'))
  const previous = globalThis.window
  globalThis.window = { go: { main: { App: {
    StudioGetStatus: async () => ({ state: 'not_installed' }),
    StudioListRecentProjects: async () => [],
  } } } }
  const confirmations = []
  const unsubscribe = confirmModal.subscribe(value => { if (value) confirmations.push(value) })
  const component = new Studio({ target: {} })
  let requests = []
  try {
    await component.session.refresh()
    requests = [component.openProject('/tmp/one'), component.openProject('/tmp/two')]
    assert.equal(confirmations.length, 1)
  } finally {
    for (const confirmation of confirmations) confirmation.resolve(false)
    await Promise.all(requests)
    component.$destroy()
    unsubscribe()
    globalThis.window = previous
  }
})
