import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compile } from 'svelte/compiler'
import { tick } from 'svelte'

const source = await readFile(new URL('./AgentStudio.svelte', import.meta.url), 'utf8')

test('Agent Studio error banners can be dismissed in both creation and editor views', () => {
  assert.ok((source.match(/aria-label="Cerrar error"/g) || []).length >= 2)
  assert.match(source, /function dismissError\(\)\s*\{\s*error = ''/)
})

test('new agent entry preserves guided, manual, and import creation paths', () => {
  assert.match(source, /Guided creation/)
  assert.match(source, /Manual creation/)
  assert.match(source, /Import agent/)
  assert.match(source, /api\.previewGuidedAgent\(guidedRequest\(\)\)/)
  assert.match(source, /api\.createGuidedAgent\(guidedRequest\(\)\)/)
})

test('runtime manifests are edited separately from agent YAML and knowledge files', () => {
  assert.match(source, /const RUNTIME = '__runtime__'/)
  assert.match(source, /api\.saveAgentRuntimeJSON\(agentId, body\)/)
  assert.match(source, /runtimeConfigured\s*\?\s*await api\.agentRuntimeJSON\(agentId\)\s*:\s*await api\.enableAgentRuntime\(agentId\)/)
  assert.match(source, /Advanced capabilities/)
})

test('managed runs expose lifecycle state, working memory, and artifacts', () => {
  assert.match(source, /api\.listManagedRuns\(agentId\)/)
  assert.match(source, /api\.managedRunDetails\(runId\)/)
  assert.match(source, /api\.managedArtifactText\(selectedRun\.id, name\)/)
  assert.match(source, /Managed runs/)
  assert.match(source, /Working memory/)
  assert.match(source, /Artifacts/)
})

test('guided creation offers only executable presets', () => {
  assert.match(source, /id:'autonomous'/)
  assert.doesNotMatch(source, /id:'team'/)
  assert.match(source, /Resume checkpoint/)
  assert.match(source, /Managed tool approval/)
})

test('a successful requirements action clears a previous Agent Studio error', () => {
  assert.match(
    source,
    /notice = `Requirements script \$\{requirementsScript\} attached\.`\s*\n\s*dismissError\(\)/,
  )
})

test('an empty local model catalogue does not trigger an automatic request loop', async () => {
  // Execute the actual RAG controls with Svelte reactivity, without mounting
  // the editor or starting a CLI. An empty fragment needs no browser DOM.
  const snippet = source.slice(source.indexOf('  let ragBackend'), source.indexOf('  // --- optional environment'))
    .replace('let ragBackend', 'export let ragBackend')
  const { js } = compile(`<script>export let api; let disposed = false; ${snippet}</script>`, { generate: 'dom' })
  const code = js.code.replace(/(["'])(svelte(?:\/[^"']+)?)\1/g, (_, quote, specifier) => JSON.stringify(import.meta.resolve(specifier)))
  const { default: Controls } = await import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'))
  const requests = []
  const controls = new Controls({ target: {}, props: { ragBackend: 'local', api: {
    getLocalLLM: async () => ({ endpoint: 'http://fixture.invalid' }),
    testLocalLLM: () => new Promise(resolve => requests.push(resolve)),
  } } })
  try {
    await tick()
    assert.equal(requests.length, 1)
    requests[0]([])
    await Promise.resolve()
    await tick()
    await tick()
    assert.equal(requests.length, 1, 'empty response must not request the catalogue again')
    controls.$set({ ragBackend: 'native' })
    await tick()
    controls.$set({ ragBackend: 'local' })
    await tick()
    await tick()
    assert.equal(requests.length, 2, 'choosing the local backend again should refresh it')
  } finally { controls.$destroy() }
})
