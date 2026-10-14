import test from 'node:test'
import assert from 'node:assert/strict'
import { onTurn, onWorkflowStream, onChatStream, onApproval, onRequirementsProgress } from './api.js'

test('closing one panel preserves other subscribers, including global approvals', () => {
  const previous = globalThis.window
  const listeners = new Map()
  globalThis.window = { runtime: {
    EventsOn(name, callback) {
      const items = listeners.get(name) || new Set()
      listeners.set(name, items)
      items.add(callback)
      return () => items.delete(callback)
    },
    EventsOff(name) { listeners.delete(name) },
  } }
  try {
    for (const subscribe of [onTurn, onWorkflowStream, onChatStream, onApproval, onRequirementsProgress]) {
      const delivered = []
      const closeGlobal = subscribe(value => delivered.push(['global', value]))
      const closePanel = subscribe(value => delivered.push(['panel', value]))
      closePanel()
      closePanel()
      for (const items of listeners.values()) for (const callback of items) callback('event')
      assert.deepEqual(delivered, [['global', 'event']])
      closeGlobal()
    }
  } finally { globalThis.window = previous }
})

test('subscriptions are safe outside the desktop runtime', () => {
  for (const subscribe of [onTurn, onWorkflowStream, onChatStream, onApproval, onRequirementsProgress]) {
    assert.doesNotThrow(() => subscribe(() => {})())
  }
})
