// Shared by the server web shell and future native mobile wrapper. Credentials
// stay in the client; model/provider authentication stays on the server.
function randomID() {
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  return Array.from(bytes, byte => byte.toString(16).padStart(2,'0')).join('')
}
export async function assertAccessResponse(response) {
  const html=response.headers?.get('content-type')?.toLowerCase().includes('text/html')
  if(response.type==='opaqueredirect'||(response.status>=300&&response.status<400)||(html&&response.headers?.get('cf-ray'))){
    await response.body?.cancel().catch(()=>{})
    const error=new Error('Cloudflare Access requires sign in. Use Cloudflare Access sign-in on Android, or reload this page in your browser. Server tasks are retained; interrupted actions are not repeated.')
    error.code='access_required';throw error
  }
}
export class RemoteClient {
  constructor({ baseURL, token, fetchImpl = globalThis.fetch, onEvent = () => {}, onConnection = () => {} }) {
    this.baseURL = baseURL.replace(/\/$/, '')
    this.token = token
    // Native Window.fetch validates its receiver. Keeping it as an unbound
    // instance property makes browser logins fail with "Illegal invocation".
    this.fetch = fetchImpl.bind(globalThis)
    this.onEvent = onEvent
    this.onConnection = onConnection
    this.methods = new Map()
    this.cursor = ''
    this.stopped = false
    this.clientId = randomID()
  }
  async request(path, options = {}) {
    const response = await this.fetch(this.baseURL + path, {
      ...options, redirect:'manual', headers: { Authorization: `Bearer ${this.token}`, 'X-Praimate-Client':this.clientId, ...(options.body ? { 'Content-Type': 'application/json' } : {}), ...options.headers },
    })
    await assertAccessResponse(response)
    const payload = await response.json().catch(() => null)
    if (!response.ok) {
      const error = new Error(payload?.error?.message || `Server returned HTTP ${response.status}`)
      error.status = response.status; error.code = payload?.error?.code
      throw error
    }
    if (payload === null) throw new Error('Server response was interrupted; inspect the operation before retrying')
    return payload
  }
  async connect() {
    const status = await this.request('/v1/status')
    if (status.protocol !== 2) throw new Error('This client requires server protocol 2')
    const methods = await this.request('/v1/methods')
    this.methods = new Map(methods.map(method => [method.name, method]))
    this.identity = status.identity
    this.clockOffset = new Date(status.serverTime).getTime() - Date.now()
    return status
  }
  async call(method, args = []) {
    const descriptor = this.methods.get(method)
    if (!descriptor) throw new Error('This operation is unavailable or your device lacks permission')
    const requestId = descriptor.mutation ? `${Math.trunc(Date.now() + (this.clockOffset || 0))}_${randomID()}` : undefined
    let response
    try {
      response = await this.request('/v1/call', { method: 'POST', body: JSON.stringify({ method, params: args, ...(requestId ? { requestId } : {}) }) })
    } catch (error) {
      // A connection can fail after the server accepts a mutation. Query its
      // durable ID instead of submitting it for a second execution.
      if (!requestId || error.status || error.code==='access_required') throw error
      response = { operation: await this.request(`/v1/operations/${requestId}`) }
    }
    if (!response.operation) return response.result
    let operation = response.operation
    while (operation.status === 'running') {
      await new Promise(resolve => setTimeout(resolve, 500))
      try { operation = await this.request(`/v1/operations/${requestId}`) }
      catch (error) { if (error.status || error.code==='access_required') throw error; await new Promise(resolve => setTimeout(resolve, 1500)) }
    }
    if (operation.status !== 'completed') throw new Error(operation.error || 'Operation needs attention')
    if (operation.resultTruncated) throw new Error(operation.error)
    return operation.result
  }
  async events() {
    while (!this.stopped) {
      this.controller = new AbortController()
      try {
        const response = await this.fetch(`${this.baseURL}/v1/events${this.cursor ? `?cursor=${encodeURIComponent(this.cursor)}` : ''}`, {
          headers: { Authorization: `Bearer ${this.token}`, 'X-Praimate-Client':this.clientId }, signal: this.controller.signal, redirect:'manual',
        })
        await assertAccessResponse(response)
        if (!response.ok) { const error = new Error('Event connection rejected'); error.status = response.status; throw error }
        this.onConnection(true)
        const reader = response.body.getReader(), decoder = new TextDecoder()
        let pending = ''
        while (!this.stopped) {
          const { value, done } = await reader.read()
          if (done) break
          pending += decoder.decode(value, { stream: true })
          let end
          while ((end = pending.indexOf('\n\n')) >= 0) {
            const frame = pending.slice(0, end); pending = pending.slice(end + 2)
            const text = frame.split('\n').filter(line => line.startsWith('data: ')).map(line => line.slice(6)).join('\n')
            if (!text) continue
            const event = JSON.parse(text)
            if (event.id) this.cursor = event.id
            if (event.name !== 'heartbeat') this.onEvent(event)
          }
          if (pending.length > 1 << 20) throw new Error('Event frame exceeds limit')
        }
      } catch (error) {
        if (error.code==='access_required') { this.onConnection(false,error.message);return }
        if (error.status === 401 || error.status === 403) { this.onConnection(false, 'Device access expired or was revoked'); return }
      }
      this.onConnection(false)
      if (!this.stopped) await new Promise(resolve => setTimeout(resolve, 1500))
    }
  }
  stop() { this.stopped = true; this.controller?.abort() }
  reconnect() { if (!this.stopped) this.controller?.abort() }
}
