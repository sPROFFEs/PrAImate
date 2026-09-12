import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { api as apiBridge } from '../lib/api.js'

const app = await readFile(new URL('../App.svelte', import.meta.url), 'utf8')
const agents = await readFile(new URL('./Agents.svelte', import.meta.url), 'utf8')
const chats = await readFile(new URL('./Chats.svelte', import.meta.url), 'utf8')
const clis = await readFile(new URL('./CLIs.svelte', import.meta.url), 'utf8')
const skills = await readFile(new URL('./Skills.svelte', import.meta.url), 'utf8')
const studio = await readFile(new URL('./Studio.svelte', import.meta.url), 'utf8')
const editor = await readFile(new URL('./Editor.svelte', import.meta.url), 'utf8')
const code = await readFile(new URL('./Code.svelte', import.meta.url), 'utf8')
const detached = await readFile(new URL('./DetachedSession.svelte', import.meta.url), 'utf8')
const workflowRunner = await readFile(new URL('../lib/WorkflowRunner.svelte', import.meta.url), 'utf8')

test('Studio owns studio-session navigation and Chats excludes its rows', () => {
  assert.match(app, /id: 'studio', label: 'Studio'/)
  assert.match(studio, /Settings\?\.surface === 'studio'/)
  assert.match(studio, /\+ New Studio/)
  assert.match(studio, /api\.openEditorWindow/)
  assert.match(studio, /openChatId\.set\(chat\.ID\)/)
  assert.doesNotMatch(chats, /studioChats/)
  assert.match(chats, /surface !== 'studio'/)
})

test('Studio editor settings hydrate session metadata before opening', () => {
  assert.match(editor, /async function openConfig\(\)[\s\S]*const currentChat = chat \|\| await loadChat\(\)/)
  assert.match(editor, /async function loadChat\(\)[\s\S]*api\.listChats\(\)[\s\S]*chat = \(chats \|\| \[\]\)\.find/)
  assert.doesNotMatch(editor, /function openConfig\(\) \{\s*if \(!chat\) return/)
  assert.match(editor, /const chatReady = loadChat\(\)[\s\S]*await loadTree\(\)[\s\S]*await chatReady/)
  assert.match(editor, /api\.studioListCLIs\(\)/)
  assert.match(editor, /api\.studioLocalLLMModels\(\)/)
  assert.match(editor, /api\.studioMCPServers\(\)/)
  assert.match(editor, /api\.saveStudioConfig\(/)
  assert.doesNotMatch(editor, /api\.listMCPServers\(\)/)
  assert.match(editor, /type="button" class="btn sm" title="Edit session CLI and model" on:click\|stopPropagation=\{openConfig\}/)
  assert.equal((editor.match(/<SkillBindingsEditor chatID=\{chatId\}/g) || []).length, 1)
  assert.match(editor, /const requestedCLI = cfg\.cli[\s\S]*cfg\.modelLoading = true[\s\S]*cfg = cfg[\s\S]*api\.studioListCLIModels\(requestedCLI\)[\s\S]*cfg\.suggestions = suggestions[\s\S]*cfg\.modelLoading = false[\s\S]*cfg = cfg/)
})

test('local LLM toggles do not present one default endpoint as the global route', () => {
  for (const source of [agents, chats, code, studio, editor]) {
    assert.doesNotMatch(source, /Use (?:the |a configured )?local LLM[^<]*<span[^>]*>\{localOpt\.endpoint\}/)
  }
  assert.doesNotMatch(workflowRunner, /Use (?:the |a configured )?local LLM[^<]*<span[^>]*>\{runLocalOpt\.endpoint\}/)
})

test('editing a chat loads local LLM options without requiring New chat first', () => {
  assert.match(chats, /function ensureLocalOptions\(\)[\s\S]*api\.localLLMModels\(\)/)
  assert.match(chats, /function openConfig\(chat\)[\s\S]*ensureLocalOptions\(\)/)
  assert.match(chats, /onMount\(async \(\) => \{[\s\S]*ensureLocalOptions\(\)[\s\S]*await load\(\)/)
  assert.match(chats, /\{:else if localOpt\?\.configured\}[\s\S]*localRoutingUnavailableMessage\(cfg\.cli\)/)
  assert.match(chats, /async function cfgCliChanged\(\)[\s\S]*cfg\.localEndpoint && !supportsLocalRouting\(cfg\.cli\)[\s\S]*cfg\.localEndpoint = ''[\s\S]*cfg\.localModel = ''/)
})

test('agent surface launch uses a modal and app-wide completion toast', () => {
  assert.match(agents, /class="modal-backdrop"/)
  assert.match(agents, /aria-labelledby="agent-launch-title"/)
  assert.match(agents, /class="modal-content launch-modal"[\s\S]*\{#if error\}<div class="banner error-banner"/)
  assert.match(studio, /class="modal-content studio-modal"[\s\S]*\{#if error\}<div class="banner error-banner"/)
  assert.match(agents, /showToast\(\{ title: 'Chat ready'/)
  assert.match(agents, /showToast\(\{ title: 'Terminal ready'/)
  assert.match(agents, /showToast\(\{ title: 'Studio opened'/)
})

test('external agents and bundled skills share the reviewed package importer', () => {
  assert.match(agents, /api\.reviewAgentImportDialog\(\)/)
  assert.match(agents, /agentImportReview\.skills/)
  assert.match(agents, /api\.importReviewedAgentPack\(agentImportReview\.path, agentImportReview\.review_digest\)/)
  assert.doesNotMatch(agents, /Install FORGE kit/)
  assert.doesNotMatch(agents, /inspectForgeKit/)
})

test('skills page exposes one shared system and installs shipped built-ins on enable', () => {
  assert.match(skills, /api\.skillsV2RolloutState\(\)/)
  assert.match(skills, /api\.setSkillsV2RolloutState\(true\)/)
  assert.match(skills, /built-in versions shipped with this PrAImate binary/)
  assert.doesNotMatch(skills, /Catalogue|Your skills|Copy built-in and legacy/)
})

test('agent pack import reviews and approves exact bundled digests once', () => {
  assert.match(agents, /api\.reviewAgentImportDialog\(\)/)
  assert.match(agents, /agentImportReview\.review_digest/)
  assert.match(agents, /api\.importReviewedAgentPack\(agentImportReview\.path, agentImportReview\.review_digest\)/)
  assert.match(agents, /approve its listed skill digests for automatic use/)
  assert.doesNotMatch(chats, /SkillsPicker/)
  assert.doesNotMatch(studio, /SkillsPicker/)
  assert.doesNotMatch(code, /SkillsPicker/)
})

test('agent terminal launch freezes skills before starting and keeps session state after closing the modal', async () => {
  globalThis.window = {
    go: { main: { App: { StartTerminal: (...args) => Promise.resolve(args) } } },
  }
  try {
    const args = await apiBridge.startTerminal('agent', 'praimate-code', '', '/tmp/project', '', '', '')
    assert.equal(args.length, 9)
    assert.deepEqual(args[8], [])
  } finally {
    delete globalThis.window
  }
  assert.match(agents, /api\.startCodeSessionWithSkills\(/)
  assert.match(agents, /dlg\.skillChoices/)
  assert.doesNotMatch(agents, /api\.recordCodeSession\(agent \? agent\.id : '', cli,/)
  assert.match(agents, /const sessionName = dlg\.name\.trim\(\)/)
  assert.doesNotMatch(agents, /dlg = null[\s\S]{0,300}dlg\.name/)
})

test('new and resumed code terminals use the unified skill selection', () => {
  assert.match(code, /api\.startCodeSessionWithSkills\(/)
  assert.match(code, /bind:choices=\{initialSkillChoices\}/)
  assert.match(code, /api\.startTerminalForChat\(sessionChatId, true\)/)
  assert.match(code, /showSkillPreparationToast\(refs, 'Terminal'\)/)
})

test('CLI installation remains modal through PATH refresh and detection', () => {
  assert.match(clis, /class="modal-content install-modal"/)
  assert.match(clis, /await api\.refreshPATH\(\)/)
  assert.match(clis, /await load\(\)/)
  assert.match(clis, /Installation complete\. PATH was refreshed and the executable was detected\./)
  assert.match(clis, /detected\.binary/)
})

test('chat and terminal sessions can detach without stopping their backend work', () => {
  assert.match(chats, /api\.detachSession\('chat', chat\.ID/)
  assert.match(chats, /detachedChats\.has\(req\.chatId\)/)
  assert.match(code, /api\.detachSession\('terminal', id/)
  assert.match(code, /teardown\(false\)/)
  assert.match(detached, /Move this terminal|Connected to PrAImate|praimate:detached-disconnected/)
  assert.match(detached, /if \(!disconnectTimer\) disconnectTimer = setTimeout/)
  assert.match(detached, /async function stopTerminal[\s\S]*window\.runtime\?\.Quit\?\.\(\)/)
})

test('Chats exposes delivery evidence without claiming model compliance', () => {
  assert.match(chats, /PrAImate payload evidence/)
  assert.match(chats, /runtime\.delivered\.map\(\(skill\) => skill\.ref\)/)
  assert.match(chats, /does not prove the model followed the skill/)
})

test('detached mode is resolved before database unlock and heavy pages are lazy-loaded', () => {
  assert.match(app, /detachedMode = await api\.detachedMode\(\)/)
  assert.ok(app.indexOf('detachedMode = await api.detachedMode()') < app.indexOf('databaseLock = await api.databaseLockStatus()'))
  assert.match(app, /load: \(\) => import\('\.\/pages\/Code\.svelte'\)/)
  assert.doesNotMatch(app, /import Code from '\.\/pages\/Code\.svelte'/)
  assert.match(app, /Close secondary windows first/)
})
