import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'
import { compile } from 'svelte/compiler'
import { applyChatStreamEvent, emptyChatStream } from './chatActivity.js'
import { normalizeToolsForCli, toolLevelsForCli } from './chatTools.js'

test('reasoning, steps, retries and paired tools survive the shared chat reducer', () => {
  const original = emptyChatStream()
  let state = original
  for (const event of [
    { type: 'step_start' }, { type: 'reasoning', text: 'Inspect ' }, { type: 'reasoning', text: 'files' },
    { type: 'tool_start', id: 'a', tool: 'read' }, { type: 'tool_start', id: 'b', tool: 'write' },
    { type: 'tool_end', id: 'b', ok: false }, { type: 'retry', detail: 'Retry in 2s' },
    { type: 'step_finish', ok: true }, { type: 'text', text: 'Answer' },
  ]) state = applyChatStreamEvent(state, event)
  assert.equal(state.text, 'Answer')
  assert.equal(state.activity[1].text, 'Inspect files')
  assert.equal(state.activity.find(item => item.id === 'a').done, false)
  assert.equal(state.activity.find(item => item.id === 'b').ok, false)
  assert.ok(state.activity.some(item => item.type === 'retry'))
  assert.deepEqual(original, emptyChatStream())
})

test('saved and live activity renders thoughts, tool failures and escaped details', async () => {
  const source = await readFile(new URL('./ChatActivity.svelte', import.meta.url), 'utf8')
  let { js } = compile(source, { generate: 'ssr' })
  const code = js.code.replaceAll('"svelte/internal"', JSON.stringify(import.meta.resolve('svelte/internal')))
    .replace(/^(import.* from )['"]\.\/chatActivity\.js['"]/m, (_, prefix) => prefix + JSON.stringify(new URL('./chatActivity.js', import.meta.url).href))
  const { default: Activity } = await import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'))
  for (const collapsible of [true, false]) {
    const { html } = Activity.render({ collapsible, events: [
      { type: 'reasoning', text: 'Thought <script>alert(1)</script>' },
      { type: 'tool', tool: 'write', ok: false, detail: 'Denied' },
      { type: 'error', detail: 'Quota exceeded' },
    ] })
    assert.match(html, /thought/)
    assert.match(html, /Quota exceeded/)
    assert.match(html, /Denied/)
    assert.doesNotMatch(html, /<script>/)
    assert.equal(html.includes('<details'), collapsible)
  }
})

test('detached and embedded tool choices use the same CLI contract', () => {
  assert.deepEqual(toolLevelsForCli('opencode').map(level => level.id), ['plan', '', 'full'])
  assert.equal(normalizeToolsForCli('opencode', 'ask'), '')
  assert.equal(normalizeToolsForCli('claude', 'ask'), 'ask')
  assert.equal(normalizeToolsForCli('copilot', 'ask'), '')
  assert.equal(normalizeToolsForCli('antigravity', 'plan'), 'plan')
})
