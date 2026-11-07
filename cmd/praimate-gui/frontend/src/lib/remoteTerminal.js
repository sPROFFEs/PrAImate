// Preserve keyboard order over HTTP and coalesce resize storms. Input is never
// retried: after a lost response the PTY may already have consumed those bytes.
export function remoteTerminal(client, warn) {
  const inputs = new Map(), resizes = new Map()
  return {
    write(id, data) {
      const next = (inputs.get(id) || Promise.resolve()).then(async () => {
        // Base64 chunks must end on a four-character boundary.
        for (let offset = 0; offset < data.length; offset += 60000) {
          await client.call('api.WriteTerminal', [id, data.slice(offset, offset + 60000)])
        }
        return true
      })
      inputs.set(id, next)
      return next.catch(error => { warn({ message: error.message, terminal: id }); return false }).finally(() => {
        if (inputs.get(id) === next) inputs.delete(id)
      })
    },
    async resize(id, cols, rows) {
      if (client.identity.role === 'viewer') return true
      let queue = resizes.get(id)
      if (queue) { queue.next = [id, cols, rows]; return true }
      queue = { next: [id, cols, rows] }
      resizes.set(id, queue)
      try {
        while (queue.next) {
          const args = queue.next
          queue.next = null
          await client.call('api.ResizeTerminal', args)
        }
        return true
      } catch (error) { warn({ message: error.message, terminal: id }); return false }
      finally { resizes.delete(id) }
    },
  }
}
