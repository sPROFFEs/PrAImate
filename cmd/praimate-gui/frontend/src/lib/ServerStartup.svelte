<script>
  import { onMount,onDestroy } from 'svelte'
  import { api } from './api.js'
  import { showConfirm } from './stores.js'
  const host=!window.praimateRemote||!!window.praimateManagedHost
  let status=null,connector=null,busy='',error='',notice='',setup=false,password='',windowsPassword='',consent=false,tunnel=false,logs='',showLogs=false
  let timer,polling=false,disposed=false
  async function refresh(){
    const next=await api.serverStartupStatus()
    if(disposed)return
    status=next
  }
  async function configure(){
    error='';password='';windowsPassword='';consent=false
    try{connector=await api.cloudflareStatus();tunnel=status?.installed?!!status.tunnel:!!connector?.tokenConfigured;setup=true}catch(e){error=String(e)}
  }
  async function install(){
    if(!consent||!password)return
    busy='Configuring system startup';error='';notice=''
    const dbPassword=password,accountPassword=windowsPassword;password='';windowsPassword=''
    try{
      if(tunnel)await api.prepareServerTunnel()
      await api.installServerStartup(dbPassword,accountPassword,tunnel)
      setup=false;consent=false;await refresh()
      notice='Enabled for future system starts. Your current workspace stays open. Use Start background service when you are ready to close this foreground window.'
    }catch(e){error=String(e);await refresh().catch(()=>{})}finally{busy=''}
  }
  async function control(action){
    const messages={start:'If this window owns the foreground workspace, it will close and its running terminals and tasks will stop. Reopen PrAImate to connect to the background service.',stop:'The background server and its running tasks will stop. Remote devices will disconnect.',restart:'The background server will restart. Running tasks will stop and connected devices will reconnect.',uninstall:'Disable and stop this profile’s startup service and remove its local unlock credential. Conversations and the database remain available.'}
    if(messages[action]&&!await showConfirm({title:action==='start'?'Start background service?':action==='uninstall'?'Remove system startup?':`${action==='restart'?'Restart':'Stop'} background service?`,message:messages[action],confirmLabel:action==='uninstall'?'Remove startup':action==='start'?'Start service':action==='restart'?'Restart service':'Stop service',tone:action==='start'?'primary':'danger'}))return
    busy='Updating startup service';error='';notice=''
    try{notice=await api.serverStartupControl(action);await refresh()}catch(e){error=String(e)}finally{busy=''}
  }
  async function inspectLogs(){error='';try{logs=await api.serverStartupLogs();showLogs=true}catch(e){error=String(e)}}
  onMount(async()=>{
    try{await refresh()}catch(e){error=String(e)}
    if(!disposed)timer=setInterval(async()=>{if(busy||polling)return;polling=true;try{await refresh()}catch(e){error=String(e)}finally{polling=false}},10000)
  })
  onDestroy(()=>{disposed=true;clearInterval(timer);password='';windowsPassword=''})
</script>

<section class="card startup" aria-labelledby="startup-title">
  <div class="state"><div><h2 id="startup-title">System startup</h2><p class="muted">Run this Server profile in the background when the machine starts, including before desktop login.</p></div>{#if status}<span class="badge" class:ready={status.bootReady}>{status.running?'Running':status.bootReady?'Enabled at boot':status.installed?'Needs attention':'Not configured'}</span>{/if}</div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if status?.error}<p class="error">{status.error}</p>{/if}
  {#if notice}<p class="notice" role="status">{notice}</p>{/if}
  {#if status?.installed}<p class="muted">{status.note}</p>{#if status.name}<p class="muted executable">Service: <code>{status.name}</code></p>{/if}{#if status.binary}<p class="muted executable">Executable: <code>{status.binary}</code></p>{/if}{/if}
  {#if !host}<p class="muted">Authorize installation and OS service changes from the Server desktop app on its host. This remote view can inspect status and logs.</p>{/if}
  <div class="actions">
    {#if host&&status?.supported}
      <button class="btn" disabled={!!busy} on:click={configure}>{status.installed?'Update startup configuration':'Set up system startup'}</button>
      {#if status.installed}
        <button class="btn" disabled={!!busy} on:click={()=>control(status.enabled?'disable':'enable')}>{status.enabled?'Disable system startup':'Enable system startup'}</button>
        <button class="btn primary" disabled={!!busy||status.running||!status.enabled} on:click={()=>control('start')}>Start background service</button>
        <button class="btn" disabled={!!busy||!status.running} on:click={()=>control('stop')}>Stop</button>
        {#if window.praimateManagedHost}<button class="btn" disabled={!!busy||!status.running||!status.enabled} on:click={()=>control('restart')}>Restart</button>{/if}
        <button class="btn danger" disabled={!!busy} on:click={()=>control('uninstall')}>Remove startup</button>
      {/if}
    {/if}
    {#if status?.installed}<button class="btn" disabled={!!busy} on:click={inspectLogs}>View logs</button>{/if}
  </div>
  {#if status?.installed&&!status.enabled}<p class="muted">Startup is disabled. Enable it before starting or restarting the background service. Disabling startup does not stop an already running service.</p>{/if}
  {#if setup&&host}
    <form class="setup" on:submit|preventDefault={install}>
      <h3>Unattended startup</h3>
      <p class="muted">The service runs under your OS account and opens this encrypted profile. {status?.platform==='windows'?'Its database unlock credential is protected with Windows DPAPI and account-only file access.':'Its database password is stored in a separate private service credential file, readable by your OS account. Keep the host and its profile directory protected.'} Removing startup deletes that credential. The current database password does not change.</p>
      <label>Current database password<input type="password" autocomplete="current-password" bind:value={password} disabled={!!busy} required /></label>
      {#if status?.platform==='windows'}<label>Windows account password<input type="password" autocomplete="off" bind:value={windowsPassword} disabled={!!busy} required /></label><p class="muted">Use your account password, not Windows Hello or a PIN. It is passed once to Windows Task Scheduler and is not retained by PrAImate. Windows may request administrator authorization.</p>{/if}
      <label class="check"><input type="checkbox" bind:checked={tunnel} disabled={!!busy||!connector?.tokenConfigured} /><span>Run the configured Cloudflare Tunnel with the background Server</span></label>
      {#if connector?.installed&&!connector?.managed}<p class="muted">A separate connector service is already installed. Remove it in Cloudflare Tunnel below before selecting Server-managed tunnel startup; its saved tunnel settings remain available.</p>{/if}
      <label class="check"><input type="checkbox" bind:checked={consent} disabled={!!busy} required /><span>Allow this host to unlock this profile automatically at system startup.</span></label>
      <div class="actions"><button class="btn primary" type="submit" disabled={!!busy||!consent||!password||(status?.platform==='windows'&&!windowsPassword)}>Enable system startup</button><button class="btn" type="button" disabled={!!busy} on:click={()=>{setup=false;password='';windowsPassword=''}}>Cancel</button></div>
    </form>
  {/if}
  {#if showLogs}<details open><summary>Background service log</summary><pre>{logs}</pre><button class="btn sm" on:click={inspectLogs}>Refresh log</button></details>{/if}
  {#if busy}<p class="muted" role="status">{busy}…</p>{/if}
</section>

<style>
  .startup{margin-bottom:18px;padding:22px;min-width:0}.state{display:flex;align-items:flex-start;justify-content:space-between;gap:14px;flex-wrap:wrap}.state>div{flex:1;min-width:180px}h2{font-size:16px;margin:0 0 8px}h3{font-size:14px;margin:0 0 10px}.muted{color:var(--text-dim);font-size:12px;line-height:1.6;overflow-wrap:anywhere}.badge{font-size:11px;border:1px solid var(--border);padding:6px 10px;border-radius:20px;color:var(--text-dim)}.badge.ready{color:var(--ok);border-color:color-mix(in srgb,var(--ok) 45%,var(--border))}.actions{display:flex;gap:9px;flex-wrap:wrap;align-items:center;margin-top:14px}.error{color:var(--err);font-size:12px;overflow-wrap:anywhere}.notice{font-size:12px;padding:12px;border:1px solid var(--border);background:var(--bg);border-radius:10px;line-height:1.6}.setup{margin-top:18px;padding-top:18px;border-top:1px solid var(--border);max-width:720px}label{display:grid;gap:7px;margin:12px 0;font-size:12px}input:not([type=checkbox]){box-sizing:border-box;width:100%;border:1px solid var(--border);border-radius:8px;padding:10px 12px;font:inherit;background:var(--bg);color:var(--text)}.check{display:flex;align-items:flex-start;gap:9px;line-height:1.5}.check input{flex-shrink:0;margin-top:3px}.executable code{overflow-wrap:anywhere}details{margin-top:18px;font-size:12px}summary{cursor:pointer}pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:340px;overflow:auto;padding:14px;background:var(--bg);border:1px solid var(--border);border-radius:9px;font:11px var(--mono)}@media(max-width:620px){.startup{padding:16px}.actions .btn{white-space:normal}}
</style>
