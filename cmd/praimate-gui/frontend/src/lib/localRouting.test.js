import test from 'node:test'
import assert from 'node:assert/strict'
import { localRoutingUnavailableMessage, supportsLocalRouting } from './localRouting.js'

test('routes OpenClaude and OpenCode-compatible CLIs, but not Claude Code', () => {
  assert.equal(supportsLocalRouting('openclaude'), true)
  assert.equal(supportsLocalRouting('opencode'), true)
  assert.equal(supportsLocalRouting('praimate-code'), true)
  assert.equal(supportsLocalRouting('praimate-cli'), true)
  assert.equal(supportsLocalRouting('claude'), false)
  assert.equal(supportsLocalRouting('codex'), false)
})

test('directs Claude local users to OpenClaude', () => {
  assert.match(localRoutingUnavailableMessage('claude'), /OpenClaude/)
})
