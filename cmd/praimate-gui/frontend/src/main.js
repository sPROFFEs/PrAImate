import './styles.css'

async function boot() {
  const target = document.getElementById('app')
  if (window.PraimateNative) {
    if(!Array.prototype.at || !globalThis.crypto?.getRandomValues || typeof ReadableStream!=='function' || !globalThis.CSS?.supports?.('color','color-mix(in srgb, black, white)')) {
      throw new Error('Update Android System WebView to open PrAImate. This device is missing required browser features.')
    }
    const { installAndroidPlatform } = await import('./lib/androidTransport.js')
    await installAndroidPlatform()
    await import('./mobile.css')
  }
  let login
  async function mount(token) {
    if (token) { const { installRemoteBridge } = await import('./lib/remoteBridge.js'); await installRemoteBridge(token) }
    const [{default:App},connection,mobileShell] = await Promise.all([
      import('./App.svelte'),
      token ? import('./lib/RemoteConnection.svelte') : null,
      window.praimateMobile ? import('./lib/MobileShell.svelte') : null,
    ])
    document.getElementById('boot-status')?.remove()
    login?.$destroy()
    if (connection) { const host=document.createElement('div'); document.body.appendChild(host); new connection.default({target:host}) }
    if (mobileShell) { const host=document.createElement('div');document.body.appendChild(host);new mobileShell.default({target:host}) }
    return new App({ target })
  }
  if (!window.go?.main?.App) {
    try {
      const response = await fetch('/v1/discovery')
      if(window.praimateMobile&&!response.ok){response.body?.cancel().catch(()=>{});throw new Error(`PrAImate Server is unavailable (HTTP ${response.status}). Retry or choose another server.`)}
      const discovery = await response.json()
      if (discovery.product === 'PrAImate Server') {
        const { default: Login } = await import('./lib/RemoteLogin.svelte')
        document.getElementById('boot-status')?.remove()
        login = new Login({ target, props: { connect: mount } })
        return login
      }
      if(window.praimateMobile)throw new Error('The selected address is not a compatible PrAImate Server.')
    } catch (error) { if(window.praimateMobile)throw error; /* Standalone Vite development keeps its existing backend errors. */ }
  }
  return mount()
}
export default boot().catch(error => {
  const target=document.getElementById('app'),card=document.createElement('section')
  card.className='boot-recovery'
  const title=document.createElement('h1');title.textContent='PrAImate needs to reconnect'
  const message=document.createElement('p');message.textContent=error?.message || 'The application could not finish loading.'
  const hint=document.createElement('p');hint.textContent='Your conversations and running tasks remain on the server.'
  const retry=document.createElement('button');retry.className='btn primary';retry.textContent='Retry';retry.onclick=()=>location.reload()
  card.append(title,message,hint,retry)
  if(window.PraimateNative){const servers=document.createElement('button');servers.className='btn';servers.textContent='Choose server';servers.onclick=()=>window.PraimateNative.postMessage(JSON.stringify({id:'boot-servers',action:'servers.configure',args:{}}));card.append(servers)}
  target.replaceChildren(card)
})
