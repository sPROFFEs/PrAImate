import test from 'node:test'
import assert from 'node:assert/strict'
import { api } from './api.js'
import { readFile } from 'node:fs/promises'

test('library commands preserve review and revision and do not conflate publication with trust', async () => {
  const calls = []
  globalThis.window = { go: { main: { App: {
    SkillLibraryV2: body => { calls.push(JSON.parse(body)); return Promise.resolve({}) },
    ExportSkillPackageV2: (...args) => { calls.push(args); return Promise.resolve('skill.zip') },
  } } } }
  try {
    const request = { action: 'publish', revision: 9, key: 'draft', review: 'sha256:review' }
    await api.skillLibraryV2(request)
    await api.exportSkillPackageV2('local/test', 'sha256:version')
    assert.deepEqual(calls, [request, ['local/test', 'sha256:version']])
    const source = await readFile(new URL('./SkillLibrary.svelte', import.meta.url), 'utf8')
    assert.match(source, /draft_revision: draft.draft_revision/)
    assert.match(source, /Publish \(approval still required\)/)
    assert.match(source, /Review selected content/)
    assert.match(source, /Revoke approval/)
    assert.doesNotMatch(source, /\{@html/)
  } finally { delete globalThis.window }
})

test('versioned skill APIs forward exact selection and identity', async () => {
  const calls = []
  globalThis.window = { go: { main: { App: new Proxy({}, { get: (_, method) => (...args) => { calls.push([method, ...args]); return Promise.resolve({}) } }) } } }
  try {
    const body = '{"config":null,"lock":null}'
    await api.chatSkillsV2('chat-1')
    await api.previewChatSkillsV2('chat-1', body)
    await api.setChatSkillsV2('chat-1', body)
    await api.previewAgentSkillsV2('schema: praimate.agent/v2')
    await api.skillsV2RolloutState()
    await api.setSkillsV2RolloutState(true)
    await api.pickSkillSourceV2('zip')
    assert.deepEqual(calls, [['ChatSkillsV2','chat-1'], ['PreviewChatSkillsV2','chat-1',body], ['SetChatSkillsV2','chat-1',body], ['PreviewAgentSkillsV2','schema: praimate.agent/v2'], ['SkillsV2RolloutState'], ['SetSkillsV2RolloutState',true], ['PickSkillSourceV2','zip']])
  } finally { delete globalThis.window }
})

test('skill selector saves one backend-built selection and escapes returned text', async () => {
  const source = await readFile(new URL('./SkillBindingsEditor.svelte', import.meta.url), 'utf8')
  assert.match(source, /Save skills/)
  assert.match(source, /api\.saveChatSkillChoicesV2\(chatID, choices\)/)
  assert.doesNotMatch(source, /Build lock/)
  assert.doesNotMatch(source, /Advanced JSON/)
  assert.doesNotMatch(source, /\{@html/)
})

test('chat UI distinguishes prepared runtime state from delivered evidence', async () => {
  const source = await readFile(new URL('../pages/Chats.svelte', import.meta.url), 'utf8')
  assert.match(source, /skill_runtime/)
  assert.match(source, /pending delivery/)
  assert.match(source, /controlled block/)
})

test('document studio exposes only observed controlled skill delivery', async () => {
  const source = await readFile(new URL('../pages/Editor.svelte', import.meta.url), 'utf8')
  assert.match(source, /skill_runtime/)
  assert.match(source, /Controlled payload evidence/)
  assert.match(source, /native private context is not inspected/)
})

test('installed-version picker exposes only approved choices and one save action', async () => {
  const calls = []
  globalThis.window = { go: { main: { App: {
    InstalledSkillVersionsV2: () => { calls.push('list'); return Promise.resolve([]) },
    BuildInstalledSkillSelectionV2: body => { calls.push(JSON.parse(body)); return Promise.resolve({}) },
  } } } }
  try {
    const choices = [{ ref: 'local/review', digest: 'sha256:exact', activation: 'pinned' }]
    await api.installedSkillVersionsV2()
    await api.buildInstalledSkillSelectionV2(choices)
    assert.deepEqual(calls, ['list', choices])
    const source = await readFile(new URL('./SkillBindingsEditor.svelte', import.meta.url), 'utf8')
    assert.match(source, /api\.saveChatSkillChoicesV2\(chatID, choices\)/)
    assert.match(source, /disabled=\{busy \|\| !version\.approved\}/)
    assert.match(source, /Execution mode/)
  } finally { delete globalThis.window }
})

test('creation selector keeps inherited defaults unless the user saves an explicit set', async () => {
  const source = await readFile(new URL('./SkillChoiceDraft.svelte', import.meta.url), 'utf8')
  assert.match(source, /Use agent and app defaults/)
  assert.match(source, /Use selection/)
  assert.match(source, /choices = null/)
  assert.match(source, /disabled=\{!v\.approved\}/)
  assert.match(source, /pinnedCount > 3/)
  assert.match(source, /Context budget limit:/)
  assert.doesNotMatch(source, /checksum/i)
})

test('skill bindings editor enforces maximum 3 pinned skills budget', async () => {
  const source = await readFile(new URL('./SkillBindingsEditor.svelte', import.meta.url), 'utf8')
  assert.match(source, /pinnedCount = versions\.filter/)
  assert.match(source, /exceedsBudget = pinnedCount > 3/)
  assert.match(source, /Context budget limit:/)
})
