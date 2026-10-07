import { term, onTermData, onTermExit, decodeBase64Bytes } from './terminal.js'

// A live PTY owns one emulator, including its alternate screen, palette,
// cursor and escape parser. Replaying a bounded tail into a new emulator cannot
// reconstruct that state. Keep consuming output when Code is not visible.
export function createTerminalRenderers(dependencies = {}) {
  const io = { onData: onTermData, onExit: onTermExit, resize: term.resize, write: term.write, makeHost: () => {
    const node = document.createElement('div')
    node.style.cssText = 'width:100%;height:100%;overflow:hidden'
    return node
  }, ...dependencies }
  const entries = new Map()

  function acquire(id, create, snapshot) {
    if (entries.has(id)) return entries.get(id)
    const { xterm, fit } = create()
    const container = io.makeHost()
    let mounted = false, opened = false, ready = false, disposed = false, cursor = 0, loading = null, observer = null, notifyExit = null
    const queued = [], pendingWrites = []
    let activeWrite = null, pendingFit = false
    const entry = { xterm, fit, exited: false, mount, detach, dispose, sync }

    function dimensions(meta) {
      if (meta?.cols > 0 && meta?.rows > 0 && (xterm.cols !== meta.cols || xterm.rows !== meta.rows)) xterm.resize(meta.cols, meta.rows)
    }
    function write(data, meta) {
      if (disposed) return Promise.resolve()
      return new Promise(resolve => { pendingWrites.push({ data, meta, resolve }); pump() })
    }
    function pump() {
      if (disposed || activeWrite) return
      const item = pendingWrites.shift()
      if (!item) { if (pendingFit) { pendingFit = false; sync() }; return }
      activeWrite = item
      dimensions(item.meta)
      xterm.write(item.data, () => { activeWrite = null; item.resolve(); pump() })
    }
    function consume(data, meta) {
      if (disposed) return
      if (meta) {
        const start = Number(meta.startOffset || 0), end = Number(meta.endOffset || start + data.length)
        if (end <= cursor) return
        data = data.slice(Math.max(0, cursor - start))
        cursor = end
      }
      return write(data, meta)
    }
    const unsubscribeData = io.onData(id, (data, meta) => {
      if (!ready) queued.push({ data, meta })
      else consume(data, meta)
    })
    const unsubscribeExit = io.onExit(id, () => {
      entry.exited = true
      xterm.write('\r\n\x1b[2m[process exited — start a new session to continue]\x1b[0m\r\n')
      notifyExit?.()
      if (!mounted) dispose()
    })
    xterm.onData(data => { if (mounted && !entry.exited) io.write(id, data) })

    async function mount(host, onExit) {
      mounted = true
      notifyExit = onExit
      host.appendChild(container)
      if (!opened) { xterm.open(container); opened = true }
      sync()
      observer?.disconnect()
      if (typeof ResizeObserver !== 'undefined') { observer = new ResizeObserver(sync); observer.observe(host) }
      if (!loading) loading = (async () => {
        try {
          let snap
          try { snap = await snapshot() } catch { /* A snapshot may be unavailable; live output still works. */ }
          if (disposed) return
          cursor = Number(snap?.endOffset || 0)
          if (snap?.data) await write(decodeBase64Bytes(snap.data), snap)
        } finally {
          for (const item of queued) await consume(item.data, item.meta)
          queued.length = 0
          ready = true
          // Snapshot geometry may differ from the pane. Fit after restoration
          // so the CLI receives a real size change and can redraw accordingly.
          sync()
        }
      })()
      await loading
      if (!disposed && mounted) xterm.focus()
    }
    function sync() {
      if (disposed || !mounted) return
      if (activeWrite || pendingWrites.length) { pendingFit = true; return }
      try { fit.fit(); if (xterm.cols > 0 && xterm.rows > 0) io.resize(id, xterm.cols, xterm.rows) } catch { /* pane is being hidden */ }
    }
    function detach() {
      mounted = false
      notifyExit = null
      observer?.disconnect()
      observer = null
      container.remove()
      if (entry.exited) dispose()
    }
    function dispose() {
      if (disposed) return
      disposed = true
      activeWrite?.resolve()
      for (const item of pendingWrites) item.resolve()
      pendingWrites.length = 0
      observer?.disconnect()
      unsubscribeData()
      unsubscribeExit()
      container.remove()
      xterm.dispose()
      entries.delete(id)
    }
    entries.set(id, entry)
    return entry
  }

  return { acquire, dispose: id => entries.get(id)?.dispose(), prune: ids => {
    const live = new Set(ids)
    for (const [id, entry] of entries) if (!live.has(id)) entry.dispose()
  } }
}

export const terminalRenderers = createTerminalRenderers()
