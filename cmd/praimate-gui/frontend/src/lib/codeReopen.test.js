import test from 'node:test'
import assert from 'node:assert/strict'
import { codeReopenPayload } from './codeReopen.js'

test('reopening Code preserves its chat identity and native route', () => {
  const chat = {
    ID: 'native-chat', CLIAgent: 'praimate-cli', WorkspacePath: '/project', Title: 'Native terminal',
    Settings: { model: 'qwen', local: { endpoint: 'http://localhost:8000', model: 'qwen' } },
  }
  assert.deepEqual(codeReopenPayload(chat), {
    termId: '', chatId: 'native-chat', cli: 'praimate-cli', cwd: '/project', agentId: '',
    model: 'qwen', localEndpoint: 'http://localhost:8000', localModel: 'qwen', label: 'Native terminal',
  })
  assert.equal(codeReopenPayload(chat, 'live-pty').chatId, 'native-chat')
  assert.equal(codeReopenPayload(chat, 'live-pty').termId, 'live-pty')
})
