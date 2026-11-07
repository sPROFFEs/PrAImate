import test from 'node:test'
import assert from 'node:assert/strict'
import { createTerminalComposer, terminalTextPayload } from './mobileTerminalInput.js'

test('terminal text preserves Unicode and frames a paste before pressing Enter', () => {
  assert.equal(terminalTextPayload('hola ñ 🐒'), 'hola ñ 🐒\r')
  assert.equal(terminalTextPayload('one\ntwo\r\nthree', true), '\x1b[200~one\rtwo\rthree\x1b[201~\r')
})

test('a successful send clears only the submitted draft and never duplicates a pending send', async () => {
  const writes=[];let finish
  const composer=createTerminalComposer({write:(id,data)=>{writes.push([id,data]);return new Promise(resolve=>finish=resolve)}})
  composer.select('first');composer.edit('hello')
  const pending=composer.send()
  assert.equal(composer.snapshot().busy,true)
  assert.equal(await composer.send(),false)
  finish(true);assert.equal(await pending,true)
  assert.deepEqual(writes,[['first','hello\r']])
  assert.equal(composer.snapshot().draft,'')
})

test('failed or unconfirmed delivery retains the draft and does not retry automatically', async () => {
  let writes=0
  const composer=createTerminalComposer({write:async()=>{writes++;return false}})
  composer.select('first');composer.edit('keep this')
  assert.equal(await composer.send(),false)
  assert.equal(composer.snapshot().draft,'keep this')
  assert.equal(composer.snapshot().busy,false)
  assert.match(composer.snapshot().error,/confirm|accept/i)
  assert.equal(writes,1)
})

test('switching terminals during delivery preserves the other terminal draft', async () => {
  let finish
  const composer=createTerminalComposer({write:()=>new Promise(resolve=>finish=resolve)})
  composer.select('first');composer.edit('first message')
  const pending=composer.send()
  composer.select('second');composer.edit('second message')
  finish(true);await pending
  assert.equal(composer.snapshot().terminalID,'second')
  assert.equal(composer.snapshot().draft,'second message')
  composer.select('first');assert.equal(composer.snapshot().draft,'')
})

test('terminal control errors retain their actionable code after failed delivery', async () => {
  const composer=createTerminalComposer({write:async()=>{composer.warn('first','Another connection controls this terminal','terminal_busy');return false}})
  composer.select('first');composer.edit('hello')
  assert.equal(await composer.send(),false)
  assert.equal(composer.snapshot().code,'terminal_busy')
  assert.match(composer.snapshot().error,/Another connection/)
})
