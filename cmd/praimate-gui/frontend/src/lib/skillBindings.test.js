import test from 'node:test'
import assert from 'node:assert/strict'
import { api } from './api.js'
import { readFile } from 'node:fs/promises'

test('GitHub folder imports use the normalized source and pinned revision through review and install', async () => {
  const component = await readFile(new URL('./SkillLibrary.svelte', import.meta.url), 'utf8')
  const script = component.match(/<script>([\s\S]*?)<\/script>/)[1].replace(/^\s*import .*$/gm, '')
  const root = 'https://github.com/owner/repo', subpath = 'skills/example', revision = 'a'.repeat(40)
  const calls = []
  const response = { source: root, subpath, git_ref: revision, review: 'sha256:review', packages: [{ index: 0, ref: 'imported/example', manifest: { Name: 'example' }, files: [{ Path: 'SKILL.md' }] }] }
  const harness = new Function('api', 'onMount', 'showConfirm', `let filteredSummaries; ${script}
    return { inspect, previewSelection, install, sourceRequest, reviewState:() => review,
      configure() { kind = 'github'; source = '${root}/tree/main/${subpath}'; gitRef = ''; subpath = ''; }
    };`)(
    { skillLibraryV2: async request => { calls.push(request); return request.action === 'list' ? { view: { Revision: 1 }, summaries: [] } : response } },
    () => {}, () => {}
  )
  harness.configure()
  await harness.inspect()
  assert.equal(calls[0].source, `${root}/tree/main/${subpath}`)
  assert.deepEqual(harness.sourceRequest(), { kind: 'github', source: root, git_ref: revision, subpath })
  assert.equal(harness.reviewState().review, 'sha256:review')
  await harness.install()
  assert.equal(calls.filter(call => call.action === 'inspect').length, 1)
  for (const request of calls.filter(call => call.selections)) {
    assert.equal(request.source, root)
    assert.equal(request.git_ref, revision)
    assert.equal(request.subpath, subpath)
    assert.deepEqual(request.selections, [{ index: 0, ref: 'imported/example', shared: [] }])
  }
  assert.equal(calls.find(call => call.action === 'install').review, 'sha256:review')
  assert.equal(calls.find(call => call.action === 'install').approved, true)
})

test('import bulk controls select all aliases without a second per-skill approval', async () => {
  const component = await readFile(new URL('./SkillLibrary.svelte', import.meta.url), 'utf8')
  const script = component.match(/<script>([\s\S]*?)<\/script>/)[1].replace(/^\s*import .*$/gm, '')
  const calls = []
  const response = { review: 'sha256:bulk', packages: [0, 1, 2].map(i => ({ index: i, ref: 'imported/example', manifest: { Name: 'example' }, files: [{ Path: 'SKILL.md' }] })) }
  const harness = new Function('api', 'onMount', 'showConfirm', `let filteredSummaries; ${script}
    return { inspect, selectCandidates, previewSelection, install,
      choices:() => formatChoices(), reviewState:() => review,
      configure() { kind = 'zip'; source = '/fixture.zip'; }
    };`)(
    { skillLibraryV2: async request => { calls.push(request); return request.action === 'list' ? { view: { Revision: 1 }, summaries: [] } : response } },
    () => {}, () => {}
  )
  harness.configure()
  await harness.inspect()
  assert.equal(harness.reviewState(), null)
  harness.selectCandidates(false)
  assert.deepEqual(harness.choices(), [])
  harness.selectCandidates(true)
  assert.deepEqual(harness.choices().map(choice => choice.ref), ['imported/example', 'imported/example-2', 'imported/example-3'])
  await harness.previewSelection()
  await harness.install()
  const installed = calls.find(call => call.action === 'install')
  assert.equal(installed.selections.length, 3)
  assert.equal(installed.approved, true)
  assert.equal(installed.review, 'sha256:bulk')
  assert.equal(calls.some(call => call.action === 'approve'), false)
})

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
