import test from 'node:test'
import assert from 'node:assert/strict'
import { remoteStudio } from './remoteStudio.js'

test('remote Studio preserves launch settings and needs no native editor installation', async () => {
  const calls = [], tab = {location:'about:blank', close(){}}
  const studio = remoteStudio({async call(method,args) {calls.push({method,args});return {workspace:'/srv/project',chatId:'chat-1'}}}, () => tab, 'https://server.example', 'fixture-version')
  assert.equal(studio.status().state, 'installed')
  assert.equal(studio.status().remote, true)
  assert.equal(await studio.openProject('/srv/project','codex','example-model','test-agent','full'), 'chat-1')
  assert.deepEqual(calls, [{method:'projects.prepare',args:['/srv/project','test-agent','codex','example-model','full','','']}])
  const route = new URLSearchParams(new URL(tab.location).hash.slice(1))
  assert.equal(route.get('workspace'), '/srv/project')
  assert.equal(route.get('chat'), 'chat-1')
})

test('opening an existing Studio chat reuses it and never prepares another chat', async () => {
  const methods = [], tab = {close(){}}
  const studio = remoteStudio({async call(method) {methods.push(method);return '/srv/project'}}, () => tab, 'https://server.example', 'fixture')
  await studio.openEditor('/srv/project','','codex','model','existing-chat','https://llm.example','', 'local-model')
  assert.deepEqual(methods, ['projects.validate'])
  assert.equal(new URLSearchParams(new URL(tab.location).hash.slice(1)).get('chat'), 'existing-chat')
})

test('blocked popups cannot create orphan conversations', async () => {
  let calls = 0
  const studio = remoteStudio({async call(){calls++}}, () => null, 'https://server.example', 'fixture')
  await assert.rejects(studio.openProject('/srv/project','codex','','','full'), /pop-up/)
  assert.equal(calls, 0)
})

test('changing a Studio folder updates document scope only after a successful save', async () => {
  const calls = [], changed = []
  let fail = false
  const studio = remoteStudio({async call(method,args) {
    calls.push({method,args})
    if (method === 'projects.validate') return '/srv/next'
    if (fail) throw new Error('Save failed')
  }}, () => null, 'https://server.example', 'fixture', path => changed.push(path))
  const settings = ['chat-1','Project','codex','example-model','full','','',['mcp-1'],'/srv/next/']
  await studio.saveConfig(...settings)
  assert.deepEqual(changed, ['/srv/next'])
  assert.equal(calls[1].method, 'api.SaveStudioConfig')
  assert.equal(calls[1].args[8], '/srv/next')
  assert.deepEqual(calls[1].args[7], ['mcp-1'])
  fail = true
  await assert.rejects(studio.saveConfig(...settings), /Save failed/)
  assert.deepEqual(changed, ['/srv/next'])
})
