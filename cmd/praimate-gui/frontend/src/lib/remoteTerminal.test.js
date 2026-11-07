import test from 'node:test'
import assert from 'node:assert/strict'
import { remoteTerminal } from './remoteTerminal.js'

test('large pastes and later keystrokes reach the terminal in order', async () => {
  const calls = [], warnings = []
  let unblock
  let started
  const firstCall = new Promise(resolve => { started = resolve })
  const gate = new Promise(resolve => { unblock = resolve })
  const transport = remoteTerminal({ identity: {role:'control'}, async call(method, args) {
    if (!calls.length) { calls.push(args[1]); started(); await gate } else calls.push(args[1])
  } }, error => warnings.push(error))
  const original = Buffer.alloc(100000, 65).toString('base64')
  const paste = transport.write('terminal', original)
  const enter = transport.write('terminal', 'Cg==')
  await firstCall
  assert.equal(calls.length, 1)
  unblock()
  assert.deepEqual(await Promise.all([paste, enter]), [true, true])
  assert.equal(calls.slice(0, -1).join(''), original)
  assert.equal(calls.at(-1), 'Cg==')
  assert.ok(calls.every(chunk => chunk.length <= 60000 && chunk.length % 4 === 0))
  assert.equal(warnings.length, 0)
})

test('a failed input is reported once and is never replayed', async () => {
  let attempts = 0
  const warnings = []
  const transport = remoteTerminal({ identity:{role:'control'}, async call() { attempts++; throw new Error('Connection lost') } }, error => warnings.push(error))
  const first = transport.write('terminal', 'YQ==')
  const queuedEnter = transport.write('terminal', 'Cg==')
  assert.deepEqual(await Promise.all([first, queuedEnter]), [false, false])
  assert.equal(attempts, 1)
  assert.equal(warnings[0].terminal, 'terminal')
})

test('terminal ownership failures keep their code for explicit take-control UI',async()=>{
  const warnings=[]
  const transport=remoteTerminal({identity:{role:'admin'},call:async()=>{throw Object.assign(new Error('Another connection controls this terminal'),{code:'terminal_busy'})}},warning=>warnings.push(warning))
  assert.equal(await transport.write('terminal','YQ=='),false)
  assert.equal(warnings[0].code,'terminal_busy')
  assert.equal(warnings[0].terminal,'terminal')
})

test('resize bursts send the latest dimensions without parallel requests', async () => {
  const sizes = []
  let unblock
  const gate = new Promise(resolve => { unblock = resolve })
  const transport = remoteTerminal({ identity:{role:'control'}, async call(method,args) { sizes.push(args); if (sizes.length === 1) await gate } }, () => {})
  const first = transport.resize('terminal', 80, 24)
  await transport.resize('terminal', 90, 30)
  await transport.resize('terminal', 120, 40)
  unblock()
  await first
  assert.deepEqual(sizes, [['terminal',80,24], ['terminal',120,40]])
})
