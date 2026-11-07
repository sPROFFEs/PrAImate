<script>
  import { onMount } from 'svelte'
  const remote = window.praimateRemote
  const mobile = window.praimateMobile
  let monitoring = !!mobile?.monitoring
  async function monitor(){try{await mobile.monitor(!monitoring);monitoring=!monitoring}catch(e){error=e.message}}
  let connected = true, error = '', open = false, devices = [], operations = [], pairing = null, role = 'control', name = 'Mobile device', busy = false, supervised = false, terminal = ''
  async function refresh() {
    try {
      operations = (await remote.client.request('/v1/operations')).slice(-12).reverse()
      if (remote.status.identity.role === 'admin') devices = await remote.client.request('/v1/devices')
    } catch(e) { error = e.message }
  }
  async function createPairing() { busy = true; error = ''; try { pairing = await remote.client.request('/v1/pairings', { method:'POST',body:JSON.stringify({name,role}) }) } catch(e) { error=e.message } finally { busy=false } }
  async function revoke(id) { if (!confirm('Revoke this device’s access? Existing server tasks will keep running.')) return; try { await remote.client.request(`/v1/devices/${id}/revoke`, {method:'POST'}); await refresh() } catch(e) { error=e.message } }
  async function stopServer() { if (!confirm('Stop the server and its running sessions?')) return; try { await remote.client.call('api.ServerShutdown', []); error='Server is stopping'; remote.client.stop(); connected=false } catch(e) { error=e.message } }
  async function restartServer() { if (!confirm('Restart the server and interrupt its active sessions?')) return; try { await remote.client.call('api.ServerRestart', []); connected=false; error='Server is restarting' } catch(e) { error=e.message } }
  async function takeControl(){try{await remote.client.call('terminals.claim',[terminal,true]);error='';terminal=''}catch(e){error=e.message}}
  onMount(() => { const status=event=>monitoring=!!event.detail.monitoring;window.addEventListener('praimate:mobile-status',status);remote.client.call('server.capabilities',[]).then(result=>supervised=!!result.supervised).catch(()=>{}); const offConnection=window.runtime.EventsOn('praimate:remote-connection', state => { connected=state.connected; error=state.error || '' });const offWarning=window.runtime.EventsOn('praimate:remote-warning',warning=>{error=warning.message;terminal=warning.terminal||'';open=true});return()=>{offConnection();offWarning();window.removeEventListener('praimate:mobile-status',status)} })
</script>
<aside class:offline={!connected}>
  <button class="connection" on:click={() => {open=!open;if(open)refresh()}} aria-expanded={open}><span class="dot"></span>{connected ? mobile ? 'Connected' : 'Server connected' : 'Reconnecting…'}</button>
  {#if open}<section>
    <strong>Remote connection</strong><p>{remote.status.identity.role} · {mobile?.serverURL || location.host}</p>
    {#if mobile}<button class="btn" on:click={monitor}>{monitoring ? 'Stop background monitoring' : 'Monitor in background'}</button><button class="btn" on:click={()=>mobile.signInAccess().catch(e=>error=e.message)}>Cloudflare Access sign-in</button><p>Server tasks continue when this app closes. Android may limit background monitoring.</p>{/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    {#if mobile}<button class="btn" on:click={()=>mobile.checkUpdates().catch(e=>error=e.message)}>Check Android app updates</button>{/if}
    {#if !mobile && !connected}<button class="btn" on:click={()=>location.reload()}>Reconnect / sign in</button>{/if}
    {#if terminal}<button class="btn" on:click={takeControl}>Take terminal control</button>{/if}
    {#if operations.length}
      <details><summary>Recent operations</summary><p>Reconnecting does not repeat accepted operations. Review interrupted work in its chat or worker run before retrying.</p>
        <div class="operations">{#each operations as operation}<div><strong>{operation.method.replace(/^api\./,'')}</strong><small>{operation.status} · {new Date(operation.startedAt).toLocaleTimeString()}</small>{#if operation.error}<small class="error">{operation.error}</small>{/if}</div>{/each}</div>
        <button class="btn sm" on:click={refresh}>Refresh operations</button>
      </details>
    {/if}
    {#if remote.status.identity.role === 'admin'}
      <label>New device<input bind:value={name} maxlength="128" /></label>
      <label>Access<select bind:value={role}><option value="viewer">View activity</option><option value="control">Run chats, workers and terminals</option><option value="admin">Full administration</option></select></label>
      <button class="btn primary" disabled={busy} on:click={createPairing}>Create pairing code</button>
      {#if pairing}<p class="code">{pairing.code}</p><p>Expires {new Date(pairing.expiresAt).toLocaleTimeString()}</p>{/if}
      <div class="devices">{#each devices as device}<div><span>{device.name}<small>{device.role}{device.revoked ? ' · revoked' : ''}</small></span><button class="btn sm" disabled={device.revoked} on:click={() => revoke(device.id)}>Revoke</button></div>{/each}</div>
      <button class="btn" on:click={stopServer}>Stop server</button>
      {#if supervised}<button class="btn" on:click={restartServer}>Restart server</button>{/if}
    {/if}
    <button class="btn" on:click={() => remote.logout()}>{mobile ? 'Disconnect this device' : 'Disconnect this browser'}</button>
  </section>{/if}
</aside>
<style>
  aside{position:fixed;right:12px;top:8px;z-index:1000;font-size:11px}.connection{display:flex;align-items:center;gap:7px;padding:7px 10px;border-radius:18px;border:1px solid var(--border);background:var(--bg-panel,#25282e);color:var(--text,#eee);cursor:pointer;box-shadow:0 2px 10px #0002}.dot{width:6px;height:6px;border-radius:50%;background:var(--ok,#73d7a1)}.offline .dot{background:var(--warn,#e3b264)}section{margin-top:8px;width:min(340px,85vw);max-height:calc(100dvh - 80px);overflow:auto;padding:18px;display:grid;gap:12px;border:1px solid var(--border);border-radius:14px;background:var(--bg-panel,#25282e);color:var(--text,#eee);box-shadow:0 12px 40px #0004}p{margin:0;overflow-wrap:anywhere;color:var(--text-dim)}label{display:grid;gap:5px}.code{font-size:26px;letter-spacing:4px;color:var(--accent);text-align:center}.error{color:var(--err)}.devices,.operations{max-height:220px;overflow:auto}.devices>div{display:flex;justify-content:space-between;align-items:center;gap:10px;padding:7px 0;border-bottom:1px solid var(--border)}.operations>div{padding:7px 0;overflow-wrap:anywhere}details[open]{display:grid;gap:10px}summary{cursor:pointer}small{display:block;color:var(--text-dim)}input,select{box-sizing:border-box;max-width:100%;font:inherit;padding:7px;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:7px}
</style>
