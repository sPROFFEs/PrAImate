import test from 'node:test'
import assert from 'node:assert/strict'
import { commandForMCPForm, envForMCPForm, headersForMCPForm } from './mcpForm.js'

test('MCP edit form reconstructs command arguments without losing spaces', () => {
  assert.equal(
    commandForMCPForm({
      command: '/opt/MCP tools/python',
      args: ['/opt/MCP tools/server.py', '--port', '9000'],
    }),
    "'/opt/MCP tools/python' '/opt/MCP tools/server.py' --port 9000",
  )
})

test('MCP edit form renders environment variables deterministically', () => {
  assert.equal(envForMCPForm({ env: { TOKEN: 'secret', HOST: 'local' } }), 'HOST=local\nTOKEN=secret')
})

test('MCP headers retain legacy bearer auth and literal custom auth values on edit', () => {
  assert.equal(headersForMCPForm({auth:{token:'fixture'}}), 'Authorization: Bearer fixture')
  assert.equal(headersForMCPForm({auth:{headers:JSON.stringify({'X-Key':'fixture','Authorization':'Basic fixture'})}}), 'Authorization: Basic fixture\nX-Key: fixture')
  assert.equal(headersForMCPForm({auth:{headers:'{"__proto__":"fixture"}'}}), '__proto__: fixture')
})
