import test from 'node:test'
import assert from 'node:assert/strict'
import { applyLiveChatEvent, restoreLiveChat } from './chatLiveState.js'

const delta = (turnId, sequence, text) => ({ turnId, sequence, type: 'text', text })

test('live events racing snapshot recovery are applied once in arrival order', () => {
  const started = { turnId: 2, sequence: 1, type: 'started' }
  const snapshot = {
    turnId: 2,
    events: [started, { turnId: 2, sequence: 2, type: 'reasoning', text: 'working' }, delta(2, 4, 'Hello world')],
  }
  const view = restoreLiveChat(snapshot, [delta(2, 4, ' world'), delta(2, 5, '!')])
  assert.deepEqual(view.stream, { text: 'Hello world!', activity: [{ type: 'reasoning', text: 'working', ok: true }] })
  assert.equal(applyLiveChatEvent(view, delta(2, 5, '!')), view)
})

test('a newer turn replaces the old preview and ignores stale events', () => {
  const view = restoreLiveChat({ turnId: 2, events: [delta(2, 2, 'old')] }, [delta(3, 1, 'new'), delta(2, 3, ' stale')])
  assert.equal(view.turnId, 3)
  assert.equal(view.stream.text, 'new')
  assert.equal(restoreLiveChat({ turnId: 0 }, []), null)
})
