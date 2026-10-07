import assert from 'node:assert/strict'
import test from 'node:test'
import { createTerminalRenderers } from './terminalRenderers.js'

function fixture() {
  const data = new Map(), exits = new Map(), writes = [], inputs = []
  const registry = createTerminalRenderers({
    onData: (id, fn) => { data.set(id, fn); return () => data.delete(id) },
    onExit: (id, fn) => { exits.set(id, fn); return () => exits.delete(id) },
    resize: () => {}, write: (id, value) => inputs.push([id, value]),
    makeHost: () => ({ remove() {} }),
  })
  let created = 0, snapshots = 0
  const factory = () => {
    created++
    const terminal = { cols: 80, rows: 24, open() {}, focus() {}, dispose() { this.disposed = true }, resize(cols, rows) { this.cols = cols; this.rows = rows }, onData(fn) { this.input = fn }, write(value, callback) { writes.push(value); callback?.() } }
    return { xterm: terminal, fit: { fit() {} } }
  }
  const snapshot = async () => { snapshots++; return { data: btoa('initial'), endOffset: 7, cols: 80, rows: 24 } }
  return { registry, data, exits, writes, inputs, factory, snapshot, host: { appendChild() {} }, counts: () => [created, snapshots] }
}

test('reattaching keeps the same emulator and consumes output while hidden', async () => {
  const f = fixture(), first = f.registry.acquire('term', f.factory, f.snapshot)
  await first.mount(f.host, () => {})
  first.detach()
  f.data.get('term')(new TextEncoder().encode('hidden'), { startOffset: 7, endOffset: 13, cols: 100, rows: 30 })
  first.xterm.input('ignored while hidden')
  const second = f.registry.acquire('term', f.factory, f.snapshot)
  await second.mount(f.host, () => {})
  assert.equal(first.xterm, second.xterm)
  assert.deepEqual(f.counts(), [1, 1])
  assert.equal(second.xterm.cols, 100)
  assert.equal(second.xterm.rows, 30)
  assert.equal(f.writes.length, 2)
  assert.equal(f.inputs.length, 0)
  second.xterm.input('hello')
  assert.deepEqual(f.inputs, [['term', 'hello']])
  second.dispose()
  assert.equal(f.data.size, 0)
})

test('snapshot overlap is deduplicated and exited hidden terminals release memory', async () => {
  const f = fixture()
  let release
  const entry = f.registry.acquire('term', f.factory, () => new Promise(resolve => { release = resolve }))
  const mounted = entry.mount(f.host, () => {})
  f.data.get('term')(new TextEncoder().encode('initial'), { startOffset: 0, endOffset: 7 })
  f.data.get('term')(new TextEncoder().encode('new'), { startOffset: 7, endOffset: 10 })
  release({ data: btoa('initial'), endOffset: 7 })
  await mounted
  assert.equal(f.writes.length, 2)
  entry.detach()
  f.exits.get('term')()
  assert.equal(entry.xterm.disposed, true)
  assert.equal(f.data.size, 0)
  assert.equal(f.exits.size, 0)
})

test('screen geometry changes only after the preceding output has been parsed', async () => {
  const f = fixture(), entry = f.registry.acquire('term', f.factory, f.snapshot)
  await entry.mount(f.host, () => {})
  entry.detach()
  const callbacks = [], frames = []
  entry.xterm.write = (data, callback) => { frames.push([entry.xterm.cols, entry.xterm.rows]); callbacks.push(callback) }
  f.data.get('term')(new TextEncoder().encode('a'), { startOffset: 7, endOffset: 8, cols: 100, rows: 30 })
  f.data.get('term')(new TextEncoder().encode('b'), { startOffset: 8, endOffset: 9, cols: 120, rows: 40 })
  assert.deepEqual(frames, [[100, 30]])
  callbacks.shift()()
  assert.deepEqual(frames, [[100, 30], [120, 40]])
  callbacks.shift()()
  entry.dispose()
})

test('a missing snapshot does not prevent live output or later reattachment', async () => {
  const f = fixture()
  const entry = f.registry.acquire('term', f.factory, async () => { throw new Error('snapshot unavailable') })
  await entry.mount(f.host, () => {})
  f.data.get('term')(new TextEncoder().encode('live'), { startOffset: 0, endOffset: 4 })
  entry.detach()
  await entry.mount(f.host, () => {})
  assert.equal(new TextDecoder().decode(f.writes[0]), 'live')
  assert.equal(f.writes.length, 1)
  entry.dispose()
})
