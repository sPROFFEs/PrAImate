<script>
  import { onMount, onDestroy } from 'svelte'
  import { api } from './api.js'
  let status=null, publicURL='', token='', busy='', error='', notice=''
  let polling=false, timer, disposed=false
  $: dirty=!!token || publicURL!==(status?.publicURL||'')
  const admin=!window.praimateRemote || window.praimateRemote.status?.identity?.role==='admin'
  async function refresh(){
    try { status=await api.cloudflareStatus() } catch(e) {error=String(e)}
  }
  async function save(){
    busy='Saving configuration';error='';notice=''
    try{await api.saveCloudflareConfig({publicURL,token});token='';await refresh();publicURL=status.publicURL;notice='Saved. Configure this hostname in Cloudflare using the origin below.'}catch(e){error=String(e)}finally{busy=''}
  }
  async function service(action){
    if(action==='install'&&!confirm('Download the official cloudflared binary and install a dedicated per-user connector service? Its token will also be stored in a private service credential file. The hostname and access policies are managed in Cloudflare.'))return
    if(['stop','uninstall','restart'].includes(action)&&!confirm(`${action==='uninstall'?'Remove':action==='restart'?'Restart':'Stop'} this connector? Devices connected through this tunnel will disconnect. Server tasks will keep running.`))return
    busy=action==='install'?'Downloading and installing connector…':`${action} connector…`;error='';notice=''
    try{await api.cloudflareService(action);if(action==='install'){busy='Starting connector…';await api.cloudflareService('start')}notice=action==='install'?'Connector installed. Waiting for its Cloudflare connection.':'Connector operation completed.'}catch(e){error=String(e)}finally{await refresh();busy=''}
  }
  onMount(async()=>{
    if(!admin)return
    await refresh();publicURL=status?.publicURL||'';if(!disposed)timer=setInterval(async()=>{if(busy||polling)return;polling=true;try{await refresh()}finally{polling=false}},5000)
  })
  onDestroy(()=>{disposed=true;clearInterval(timer)})
</script>

{#if admin}
<section aria-label="Cloudflare Tunnel" class="tunnel">
  <h2>Cloudflare Tunnel</h2>
  <p class="muted">Publish this server through your own tunnel. Optionally protect its hostname with Cloudflare Access; Android will open its email and verification-code sign-in before pairing.</p>
  <div class="card">
    <div class="state"><strong>{status?.ready?'Connected to Cloudflare':status?.running?'Connecting to Cloudflare…':status?.installed?'Connector stopped':'Connector not installed'}</strong><button class="btn sm" disabled={!!busy} on:click={refresh}>Refresh status</button></div>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    {#if status?.error}<p class="error">{status.error}</p>{/if}
    {#if notice}<p class="muted" role="status">{notice}</p>{/if}
    <label>Public hostname<input type="url" placeholder="https://praimate.example.com" bind:value={publicURL} disabled={!!busy} /></label>
    <label>Connector token<input type="password" autocomplete="new-password" spellcheck="false" placeholder={status?.tokenConfigured?'Token saved · leave blank to keep':'Paste only the eyJ… tunnel token'} bind:value={token} disabled={!!busy||status?.installed} /></label>
    <p class="muted">Create the tunnel and public hostname in Cloudflare. Paste its connector token, then save and install. Remove the connector service before replacing its token.</p>
    <div class="actions"><button class="btn" disabled={!!busy||!publicURL||(!token&&!status?.tokenConfigured)} on:click={save}>Save configuration</button>
    {#if !status?.installed}<button class="btn primary" disabled={!!busy||dirty||!status?.tokenConfigured||!status?.originURL} on:click={()=>service('install')}>Install and start connector</button>
    {:else}<button class="btn primary" disabled={!!busy} on:click={()=>service(status.running?'restart':'start')}>{status.running?'Restart':'Start'} connector</button><button class="btn" disabled={!!busy||!status.running} on:click={()=>service('stop')}>Stop</button><button class="btn" disabled={!!busy} on:click={()=>service('uninstall')}>Remove service</button>{/if}</div>
    {#if busy}<p role="status">{busy}</p>{/if}
    {#if status?.originURL}<div class="origin"><span class="muted">Cloudflare public hostname → service URL</span><code>{status.originURL}</code><span class="muted">Use this local origin on the same machine. Cloudflare Access is configured in your Cloudflare dashboard. PrAImate device pairing is still required.</span></div>{/if}
    <p class="muted">{status?.note}</p>
  </div>
</section>
{/if}
<style>
  .tunnel{margin-top:24px}h2{font-size:16px;margin-bottom:8px}.muted{font-size:12px;color:var(--text-dim);line-height:1.6;overflow-wrap:anywhere}.state,.actions{display:flex;flex-wrap:wrap;gap:10px;align-items:center}.state{justify-content:space-between;margin-bottom:14px}label{display:grid;gap:6px;margin:12px 0;font-size:12px}input{box-sizing:border-box;max-width:100%;width:100%;padding:9px 11px;border:1px solid var(--border);border-radius:8px;background:var(--bg);color:var(--text);font:inherit}.error{color:var(--err);overflow-wrap:anywhere}.origin{display:grid;gap:6px;margin-top:16px;padding:12px;border-radius:10px;background:var(--bg)}code{overflow-wrap:anywhere}
</style>
