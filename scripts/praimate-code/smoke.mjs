// Test the compiled prompt path, not just --version. No accounts or real models.
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'

const [binary, target] = process.argv.slice(2)
const platform = process.platform === 'win32' ? 'windows' : process.platform
const arch = process.arch === 'x64' ? 'amd64' : process.arch
if (target !== `${platform}-${arch}`) {
  console.log(`Model smoke test skipped for cross target ${target}`)
  process.exit(0)
}
const root = await mkdtemp(resolve(tmpdir(), 'praimate-code-smoke-'))
const project = resolve(root, 'project')
await mkdir(project)
let requests = 0
const server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch(req) {
  if (new URL(req.url).pathname !== '/v1/chat/completions') return new Response('fixture route not found', { status: 404 })
  requests++
  const chunk = JSON.stringify({ id: 'fixture', object: 'chat.completion.chunk', created: 1, model: 'fixture', choices: [{ index: 0, delta: { content: 'praimate-smoke-ok' }, finish_reason: 'stop' }] })
  return new Response(`data: ${chunk}\n\ndata: [DONE]\n\n`, { headers: { 'Content-Type': 'text/event-stream' } })
} })
const env = Object.fromEntries(['PATH', 'SystemRoot', 'WINDIR', 'PATHEXT', 'COMSPEC', 'TEMP', 'TMP'].flatMap(key => process.env[key] ? [[key, process.env[key]]] : []))
Object.assign(env, {
  HOME: root, USERPROFILE: root, APPDATA: resolve(root, 'config'), LOCALAPPDATA: resolve(root, 'data'), PWD: project,
  XDG_CONFIG_HOME: resolve(root, 'config'), XDG_DATA_HOME: resolve(root, 'data'), XDG_CACHE_HOME: resolve(root, 'cache'), XDG_STATE_HOME: resolve(root, 'state'),
  OPENCODE_TEST_HOME: root, OPENCODE_CONFIG_DIR: resolve(root, 'config/opencode'),
  OPENCODE_DISABLE_AUTOUPDATE: '1', OPENCODE_DISABLE_MODELS_FETCH: '1', OPENCODE_DISABLE_DEFAULT_PLUGINS: '1',
  OPENCODE_DISABLE_EXTERNAL_SKILLS: '1', OPENCODE_DISABLE_CLAUDE_CODE: '1', OPENCODE_DISABLE_LSP_DOWNLOAD: '1', PRAIMATE_HOST_TLS: '{}',
  OPENCODE_CONFIG_CONTENT: JSON.stringify({ enabled_providers: ['fixture'], model: 'fixture/fixture', small_model: 'fixture/fixture', snapshot: false,
    provider: { fixture: { npm: '@ai-sdk/openai-compatible', options: { baseURL: `${server.url.origin}/v1`, apiKey: 'fixture-key' }, models: { fixture: { name: 'Fixture', limit: { context: 8192, output: 1024 } } } } } }),
})
let child, timer
try {
  child = Bun.spawn([resolve(binary), 'run', '--format', 'json', '--model', 'fixture/fixture', 'Reply with the fixture response.'], { cwd: project, env, stdin: 'ignore', stdout: 'pipe', stderr: 'pipe' })
  timer = setTimeout(() => child.kill(), 45000)
  const [code, stdout, stderr] = await Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()])
  const answered = stdout.split('\n').some(line => {
    try { const event = JSON.parse(line); return event.type === 'text' && event.part?.text === 'praimate-smoke-ok' } catch { return false }
  })
  if (code !== 0 || !requests || !answered) throw new Error(`Compiled model smoke test failed (exit ${code}, requests ${requests}): ${stdout.slice(-1500)} ${stderr.slice(-1500)}`)
  console.log('Compiled model smoke test passed (isolated HTTP fixture)')
} finally {
  clearTimeout(timer)
  if (child && child.exitCode === null) { child.kill(); await child.exited }
  server.stop(true)
  await rm(root, { recursive: true, force: true })
}
