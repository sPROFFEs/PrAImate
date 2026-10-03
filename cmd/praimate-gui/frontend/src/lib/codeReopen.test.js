import test from 'node:test'
import assert from 'node:assert/strict'
import { codeReopenPayload, assistantCodePayload } from './codeReopen.js'

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

test('assistant attaches only to the terminal it actually started', () => {
  assert.equal(assistantCodePayload({page:'code'}), null)
  assert.equal(assistantCodePayload({page:'code',chat_id:'chat-1'}), null)
  assert.deepEqual(assistantCodePayload({page:'code',chat_id:'chat-1',term_id:'pty-1',cli:'codex',workspace:'/project'}),
    {termId:'pty-1',chatId:'chat-1',cli:'codex',cwd:'/project',model:'',label:'codex'})
})
