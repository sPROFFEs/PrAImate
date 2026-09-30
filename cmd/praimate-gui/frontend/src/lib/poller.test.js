import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createPoller } from './poller.js'

test('coalesces refreshes while I/O is pending and stops without another timer', async () => {
  let calls = 0, release
  const timers = new Map()
  const poller = createPoller(async () => {
    calls++
    await new Promise((resolve) => { release = resolve })
  }, { schedule: (fn) => { timers.set(fn, fn); return fn }, cancel: (id) => timers.delete(id) })
  const first = poller.request()
  await Promise.resolve()
  assert.equal(calls, 1)
  const queued = poller.request()
  poller.request()
  assert.equal(queued, first)
  assert.equal(calls, 1)
  release()
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  poller.stop()
  release()
  await first
  assert.equal(timers.size, 0)
  await poller.request()
  assert.equal(calls, 2)
})

test('hidden windows do not fetch and idle delay is evaluated after each request', async () => {
  let calls = 0, hidden = false, scheduled, interval = 1000
  const poller = createPoller(async () => { calls++ }, {
    visible: () => !hidden, delay: () => interval,
    schedule: (fn, ms) => { scheduled = { fn, ms }; return fn }, cancel: () => {},
  })
  await poller.request()
  assert.equal(calls, 1)
  assert.equal(scheduled.ms, 1000)
  hidden = true
  scheduled.fn()
  assert.equal(calls, 1)
  hidden = false
  interval = 10000
  scheduled.fn()
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  assert.equal(scheduled.ms, 10000)
  poller.stop()
})
