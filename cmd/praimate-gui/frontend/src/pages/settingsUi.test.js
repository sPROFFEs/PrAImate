import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const settingsSource = await readFile(new URL('./Settings.svelte', import.meta.url), 'utf8')
const mcpSource = await readFile(new URL('./MCP.svelte', import.meta.url), 'utf8')

test('destructive data dialog uses an opaque surface and supported-CLI wording', () => {
  assert.match(settingsSource, /\.danger-panel\s*\{[^}]*background:\s*var\(--bg-panel\)/s)
  assert.match(settingsSource, /class="modal-backdrop destructive-backdrop"/)
  assert.doesNotMatch(settingsSource, /from Codex, OpenCode, and DeepSeek config/)
})

test('MCP state and toggle action are presented separately', () => {
  assert.match(mcpSource, /s\.enabled \? 'Enabled' : 'Disabled'/)
  assert.match(mcpSource, /s\.enabled \? 'Disable' : 'Enable'/)
  assert.match(mcpSource, /class:primary=\{!s\.enabled\}/)
})

test('Git backup test and configure actions provide persistent toast feedback', () => {
  assert.match(settingsSource, /title: 'Testing Git remote'[\s\S]*tone: 'busy'[\s\S]*dismissible: false/)
  assert.match(settingsSource, /title: 'Remote connection successful'/)
  assert.match(settingsSource, /title: 'Remote connection failed'[\s\S]*tone: 'err'[\s\S]*duration: 0/)
  assert.match(settingsSource, /title: 'Configuring Git backup'[\s\S]*tone: 'busy'[\s\S]*dismissible: false/)
  assert.match(settingsSource, /applyBackupResult\(res\)[\s\S]*await tick\(\)[\s\S]*res\.action === 'diverged'/)
  assert.match(settingsSource, /title: 'Backup connected — action required'/)
  assert.match(settingsSource, /title: 'Backup configuration failed'[\s\S]*tone: 'err'[\s\S]*duration: 0/)
})
