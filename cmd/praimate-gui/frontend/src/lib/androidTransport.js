// Native messages are accepted only by the APK's packaged main frame. Tokens
// remain in Android Keystore; Java injects Authorization into server requests.
export class AndroidTransport {
  constructor(port, {maxConcurrent = 8, maxQueued = 64} = {}) {
    this.port = port
    this.pending = new Map()
    this.sequence = 0
    this.session = Array.from(crypto.getRandomValues(new Uint8Array(8)),value=>value.toString(16).padStart(2,'0')).join('')
    this.maxConcurrent = maxConcurrent
    this.maxQueued = maxQueued
    this.active = 0
    this.queue = []
    port.onmessage = event => this.receive(JSON.parse(event.data))
  }
  send(message) { this.port.postMessage(JSON.stringify(message)) }
  request(action, args = {}, {timeoutMs = ['files.upload','files.download','monitor'].includes(action) ? 0 : 15000} = {}) {
    const id = `${this.session}-native-${++this.sequence}`
    return new Promise((resolve, reject) => {
      const timer = timeoutMs ? setTimeout(() => {
        this.finish(id)
        reject(new Error('Android did not respond. Reopen the connection or choose another server.'))
      }, timeoutMs) : null
      this.pending.set(id, {resolve, reject, cleanup:() => clearTimeout(timer)})
      try { this.send({id, action, args}) }
      catch(error) { this.finish(id); reject(error) }
    })
  }
  finish(id) {
    const state = this.pending.get(id)
    if (!state) return
    this.pending.delete(id)
    state.cleanup?.()
    if (state.started) this.active--
    this.queue = this.queue.filter(item => item !== id)
    this.drain()
  }
  drain() {
    while (this.active < this.maxConcurrent && this.queue.length) {
      const id = this.queue.shift(), state = this.pending.get(id)
      if (!state) continue
      state.started = true
      this.active++
      try { this.send({id,action:'http',args:state.args}) }
      catch(error) { state.reject(error); this.finish(id) }
    }
  }
  receive(message) {
    const state = this.pending.get(message.id)
    if (!state) return
    if (message.type === 'headers') {
      state.controller = null
      state.awaitingAck = false
      const body = new ReadableStream({
        start: controller => { state.controller = controller },
        pull: () => { if (state.awaitingAck) { state.awaitingAck=false; this.send({action:'ack',id:message.id}) } },
        cancel: () => { this.send({action:'cancel',id:message.id}); this.finish(message.id) },
      })
      state.resolve(new Response([204,205,304].includes(message.status) ? null : body, {status:message.status,headers:message.headers}))
    } else if (message.type === 'data') {
      const bytes=Uint8Array.from(atob(message.data),char=>char.charCodeAt(0))
      state.awaitingAck=true
      state.controller.enqueue(bytes)
      if (state.controller.desiredSize>0) {state.awaitingAck=false;this.send({action:'ack',id:message.id})}
    } else if (message.type === 'done' || message.type === 'result') {
      this.finish(message.id)
      if (message.type === 'done') state.controller?.close()
      else state.resolve(message.result)
    } else if (message.type === 'error') {
      this.finish(message.id)
      const error=new TypeError(message.message || 'Native connection failed')
      state.controller?.error(error);state.reject(error)
    }
  }
  fetch = (url, options = {}) => {
    const path = new URL(url, location.origin)
    if (path.origin !== location.origin || !path.pathname.startsWith('/v1/')) return Promise.reject(new Error('Only PrAImate API requests may use the native connection'))
    if (options.body && typeof options.body !== 'string') return Promise.reject(new TypeError('Use native file upload for binary requests'))
    if (this.queue.length >= this.maxQueued) return Promise.reject(new Error('Too many waiting requests. Wait for the current operation.'))
    const id=`${this.session}-http-${++this.sequence}`
    return new Promise((resolve,reject)=>{
      const abort=()=>{
        const state=this.pending.get(id),error=new DOMException('Request cancelled','AbortError')
        if(state?.started)this.send({action:'cancel',id})
        this.finish(id);state?.controller?.error(error);reject(error)
      }
      if(options.signal?.aborted){abort();return}
      options.signal?.addEventListener('abort',abort,{once:true})
      this.pending.set(id,{resolve,reject,cleanup:()=>options.signal?.removeEventListener('abort',abort),args:{path:path.pathname+path.search,method:options.method||'GET',headers:Object.fromEntries(new Headers(options.headers)),body:options.body||''}})
      this.queue.push(id)
      this.drain()
    })
  }
}

export async function installAndroidPlatform(port = window.PraimateNative) {
  const transport=new AndroidTransport(port)
  const status=await transport.request('status')
  const platform={
    ...status, transport,
    credentials: {
      get:async()=> (await transport.request('status')).paired ? 'android-keystore-session' : '',
      set:token=>token==='android-keystore-session'?Promise.resolve():transport.request('credentials.set',{token}),
      clear:()=>transport.request('credentials.clear'),
    },
    upload:(multiple,accept)=>transport.request('files.upload',{multiple,accept}),
    download:descriptor=>transport.request('files.download',descriptor),
    configure:()=>transport.request('servers.configure'),
    signInAccess:()=>transport.request('access.login'),
    checkUpdates:()=>transport.request('updates.check'),
    close:()=>transport.request('window.close'),
    back:()=>transport.request('navigation.back'),
    monitor:enabled=>transport.request('monitor',{enabled}),
    rememberRoute:route=>transport.request('navigation.remember',{route}),
    copy:text=>transport.request('clipboard.write',{text}),
  }
  window.praimateMobile=platform
  const originalFetch=window.fetch.bind(window)
  window.fetch=(url,options)=>{
    const target=new URL(url,location.origin)
    return target.origin===location.origin&&target.pathname.startsWith('/v1/')?transport.fetch(url,options):originalFetch(url,options)
  }
  // Native activities give project/session windows a normal Android back stack.
  window.open=(url)=>{
    let closed=false
    const navigate=value=>{
      const target=new URL(value,location.origin)
      if(target.origin!==location.origin)throw new Error('Open external links from the document instead')
      if(!closed)transport.request('navigate',{route:target.hash}).catch(error=>window.alert(error.message))
    }
    const target={close(){closed=true},get closed(){return closed},set location(value){navigate(value)}}
    if(url&&url!=='about:blank')navigate(url)
    return target
  }
  let lastResume = 0
  const resume = () => {
    if (Date.now() - lastResume < 500) return
    lastResume = Date.now()
    window.praimateRemote?.client.reconnect()
    transport.request('status').then(status => { Object.assign(platform,status);window.dispatchEvent(new CustomEvent('praimate:mobile-status',{detail:status})) }).catch(()=>{})
    if (window.praimateRemote) window.dispatchEvent(new Event('praimate:mobile-resume'))
  }
  window.addEventListener('visibilitychange',()=>{if(!document.hidden)resume()})
  window.addEventListener('praimate:native-resume',resume)
  document.documentElement.classList.add('praimate-mobile')
  return platform
}
