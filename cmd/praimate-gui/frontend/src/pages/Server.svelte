<script>
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import { appEdition, showConfirm as askConfirm } from '../lib/stores.js'
  import { canConfigureServer } from '../lib/editionNavigation.js'
  import CloudflareSettings from '../lib/CloudflareSettings.svelte'

  const remote = !!window.praimateRemote
  $: allowed = canConfigureServer($appEdition, window.praimateRemote?.status?.identity?.role)
  let status = null, devices = [], form = {}, origins = '', baseline = '', initialized = false
  let busy = '', error = '', notice = '', pairing = null, deviceName = 'Mobile device', role = 'control'
  let timer, polling = false, disposed = false, now = Date.now()
  $: draft = {autoStart:!!form.autoStart, listen:form.listen || '', tlsCert:form.tlsCert || '', tlsKey:form.tlsKey || '', allowedOrigins:origins.split('\n').map(value=>value.trim()).filter(Boolean)}
  $: dirty = initialized && JSON.stringify(draft) !== baseline
  $: expired = pairing && Date.parse(pairing.expiresAt) <= now

  function populate(configuration) {
    form = {...configuration}
    origins = (configuration.allowedOrigins || []).join('\n')
    baseline = JSON.stringify({autoStart:!!configuration.autoStart, listen:configuration.listen || '', tlsCert:configuration.tlsCert || '', tlsKey:configuration.tlsKey || '', allowedOrigins:configuration.allowedOrigins || []})
    initialized = true
  }
  async function refresh() {
    const [next, list] = await Promise.all([api.serverStatus(), api.serverDevices()])
    if (disposed) return
    if (!next.running || (status && status.originURL !== next.originURL)) pairing = null
    status = next; devices = list || []; now = Date.now()
    if (!initialized || !dirty) populate(next.configuration)
  }
  async function reload() {
    busy = 'Refreshing'; error = ''
    try { await refresh() } catch(e) { error = String(e) } finally { busy = '' }
  }
  async function save() {
    busy = 'Saving'; error = ''; notice = ''
    try {
      await api.saveServerConfig(draft)
      await refresh(); populate(status.configuration)
      notice = status.running && status.pending ? 'Saved. Apply settings to update the running listener.' : 'Server configuration saved.'
    } catch(e) { error = String(e) } finally { busy = '' }
  }
  async function control(action) {
    if (action !== 'start' && !await askConfirm({title:action === 'stop' ? 'Stop remote access?' : 'Apply Server settings?',message:`Connected devices will disconnect${remote ? ', including this session' : ''}. Chats, workers and local terminals keep running.${remote ? ' Reconnect using the configured address. If the new address is unreachable, use the host GUI to recover.' : ''}`,confirmLabel:action === 'stop' ? 'Stop access' : 'Apply settings'})) return
    busy = action === 'stop' ? 'Stopping access' : 'Starting listener'; error = ''; notice = ''; pairing = null
    try {
      const result = await api.serverListener(action)
      notice = remote ? result : action === 'stop' ? 'Remote access stopped. Local work continues.' : 'Server is listening. Pair a device below.'
      await refresh()
    } catch(e) { error = String(e); await refresh().catch(()=>{}) } finally { busy = '' }
  }
  async function pair() {
    busy = 'Creating pairing code'; error = ''; pairing = null
    try { pairing = await api.serverPairing(deviceName, role); now = Date.now() } catch(e) { error = String(e) } finally { busy = '' }
  }
  async function revoke(device) {
    if (!await askConfirm({title:'Revoke device access?',message:`${device.name} will no longer be able to access this server. It will need a new pairing code to reconnect.`,confirmLabel:'Revoke access',tone:'danger'})) return
    busy = 'Revoking access'; error = ''
    try { await api.revokeServerDevice(device.id); await refresh() } catch(e) { error = String(e) } finally { busy = '' }
  }
  async function copy(value) {
    try { await navigator.clipboard.writeText(value); notice = 'Copied.' } catch { error = 'Clipboard unavailable. Select and copy the address or pairing code.' }
  }
  onMount(async () => {
    if (!allowed) return
    await reload()
    if (!disposed) timer = setInterval(async () => {
      now = Date.now()
      if (busy || polling) return
      polling = true
      try { await refresh() } catch(e) { error = String(e) } finally { polling = false }
    }, 5000)
  })
  onDestroy(() => { disposed = true; clearInterval(timer) })
</script>

<div class="server-page">
  <div class="page-heading"><div><div class="eyebrow">PRAIMATE SERVER</div><h1>Server</h1><p class="subtitle">Connect your Android app or browser to this PrAImate instance.</p></div><button class="btn" disabled={!!busy} on:click={reload}>Refresh</button></div>
  {#if !allowed}
    <div class="card">Server administration is available in the Server edition to administrators.</div>
  {:else}
    {#if error}<div class="feedback error" role="alert">{error}</div>{/if}
    {#if status?.error}<div class="feedback error" role="alert">{status.error}</div>{/if}
    {#if notice}<div class="feedback" role="status">{notice}</div>{/if}
    {#if !status}<div class="card">{busy || 'Server status unavailable. Use Refresh to try again.'}</div>
    {:else}
      <section class="card" aria-label="Remote access status">
        <div class="section-head"><div><h2><span class="indicator" class:running={status.running}></span>{status.running ? 'Remote access is running' : 'Remote access is stopped'}</h2><p class="muted">Stopping access leaves this app and its running tasks open.</p></div><span class="edition">Server edition</span></div>
        {#if status.running}
          <div class="address"><div><span class="muted">Local origin</span><code>{status.originURL}</code></div><button class="btn sm" on:click={() => copy(status.originURL)}>Copy</button></div>
          {#if status.publicURL}<div class="address"><div><span class="muted">Public address · Cloudflare</span><code>{status.publicURL}</code></div><button class="btn sm" on:click={() => copy(status.publicURL)}>Copy</button></div>{/if}
          <p class="muted">{status.publicURL ? 'For remote devices, use the public address after starting your tunnel below.' : 'A loopback origin works only on this host. For your phone, configure a Cloudflare Tunnel below, an SSH/VPN forward, or a network listener with TLS.'}</p>
          {#if status.pending}<p class="pending">Saved settings differ from the active listener ({status.activeConfiguration?.listen}). Apply them when you are ready to reconnect devices.</p>{/if}
        {:else}<p class="muted">Save your connection settings, start access, then create a pairing code. This page does not install a background Server process.</p>{/if}
        <div class="actions">
          {#if status.running}<button class="btn primary" disabled={!!busy || dirty} on:click={() => control('restart')}>Apply settings / restart</button><button class="btn" disabled={!!busy} on:click={() => control('stop')}>Stop access</button>
          {:else}<button class="btn primary" disabled={!!busy || dirty} on:click={() => control('start')}>Start remote access</button>{/if}
          {#if dirty}<span class="muted">Save your changes before starting or applying.</span>{/if}
        </div>
      </section>

      <section class="card" aria-labelledby="connection-title">
        <h2 id="connection-title">Connection settings</h2>
        <p class="muted">Settings are saved in your encrypted database. Command-line flags and environment variables override them at application startup.</p>
        <label class="check"><input type="checkbox" bind:checked={form.autoStart} disabled={!!busy} /><span>Start remote access automatically when this Server profile is opened and unlocked</span></label>
        <label>Listen address and port<input bind:value={form.listen} placeholder="127.0.0.1:8844" disabled={!!busy} spellcheck="false" /></label>
        <p class="muted">Use 127.0.0.1:8844 for Cloudflare Tunnel. A network address such as 0.0.0.0:8844 requires the TLS certificate and key below.</p>
        <div class="fields"><label>TLS certificate file<input bind:value={form.tlsCert} placeholder="Absolute path on the Server host" disabled={!!busy} spellcheck="false" /></label><label>TLS private key file<input bind:value={form.tlsKey} placeholder="Absolute path on the Server host" disabled={!!busy} spellcheck="false" /></label></div>
        <label>Additional browser origins<textarea bind:value={origins} rows="3" placeholder="https://your-client.example.com" disabled={!!busy} spellcheck="false"></textarea></label>
        <p class="muted">Optional. One exact origin per line, without a path. The Server's own origin and configured Cloudflare hostname are already allowed. Device authentication is always required.</p>
        <div class="actions"><button class="btn primary" disabled={!!busy || !dirty} on:click={save}>Save Server settings</button>{#if dirty}<button class="btn" disabled={!!busy} on:click={() => populate(status.configuration)}>Discard edits</button><span class="muted">Unsaved changes</span>{/if}</div>
      </section>

      <section class="card" aria-labelledby="devices-title">
        <h2 id="devices-title">Pairing and devices</h2><p class="muted">Pairing works with direct connections and tunnels. Each code is single use and expires after five minutes.</p>
        <div class="fields"><label>Device name<input bind:value={deviceName} maxlength="128" disabled={!!busy || !status.running} /></label><label>Access level<select bind:value={role} disabled={!!busy || !status.running}><option value="control">Chats, workers and terminals</option><option value="admin">Full administration</option><option value="viewer">View only</option></select></label></div>
        <button class="btn" disabled={!!busy || !status.running || !deviceName.trim()} on:click={pair}>Create pairing code</button>
        {#if !status.running}<p class="muted">Start remote access to create a pairing code.</p>{/if}
        {#if pairing}<div class="pairing" role="status">{#if expired}<strong>Pairing code expired. Create a new one.</strong>{:else}<div class="actions"><strong class="pair-code">{pairing.code}</strong><button class="btn sm" on:click={() => copy(pairing.code)}>Copy code</button></div><span class="muted">{pairing.name} · {pairing.role} · expires {new Date(pairing.expiresAt).toLocaleTimeString()}</span><span class="muted">Enter your reachable Server address in the Android app, complete Cloudflare Access if enabled, then enter this code.</span>{/if}</div>{/if}
        <div class="devices">
          {#each devices.filter(device => !device.revoked) as device (device.id)}<div class="device"><div><strong>{device.name}</strong><span class="muted">{device.role} · {Date.parse(device.expiresAt) <= now ? 'Expired' : 'Paired'} · expires {new Date(device.expiresAt).toLocaleDateString()}</span></div><button class="btn sm" disabled={!!busy} on:click={() => revoke(device)}>Revoke access</button></div>{:else}<p class="muted">No active paired devices.</p>{/each}
        </div>
      </section>
    {/if}
    <CloudflareSettings />
    {#if busy}<p class="muted" role="status">{busy}…</p>{/if}
  {/if}
</div>

<style>
  .server-page{width:100%;min-width:0}.page-heading,.section-head,.actions,.address,.device{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}.page-heading{margin-bottom:24px}.eyebrow{font-size:10px;letter-spacing:.14em;color:var(--accent);font-weight:650}h1{margin:5px 0 8px}h2{font-size:16px;margin:0 0 8px;display:flex;align-items:center;gap:9px}.card{margin-bottom:18px;padding:22px}.muted{color:var(--text-dim);font-size:12px;line-height:1.6;overflow-wrap:anywhere}.edition{border:1px solid var(--border);border-radius:20px;padding:5px 10px;font-size:11px;color:var(--text-dim)}.indicator{height:8px;width:8px;border-radius:50%;background:var(--text-dim)}.indicator.running{background:var(--ok,#65bd98)}.address{background:var(--bg);border:1px solid var(--border);border-radius:10px;padding:12px 14px;margin:10px 0}.address>div{display:grid;gap:4px;min-width:0}code{font-size:13px;overflow-wrap:anywhere}.actions{justify-content:flex-start;margin-top:16px}.fields{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}label{display:grid;gap:7px;font-size:12px;margin:14px 0}.check{display:flex;align-items:center;gap:10px}.check input{width:auto;flex-shrink:0}input,textarea,select{box-sizing:border-box;width:100%;min-width:0;border:1px solid var(--border);border-radius:8px;padding:10px 12px;font:inherit;background:var(--bg);color:var(--text)}textarea{resize:vertical}.feedback{padding:12px 16px;border:1px solid var(--border);border-radius:10px;background:var(--bg-panel);margin:12px 0;font-size:13px;overflow-wrap:anywhere}.error{color:var(--err)}.pending{font-size:12px;color:var(--accent);line-height:1.6}.pairing{display:grid;gap:8px;padding:16px;background:var(--bg);border-radius:10px;margin:14px 0}.pair-code{font-size:28px;letter-spacing:4px;font-variant-numeric:tabular-nums}.pairing .actions{margin:0}.devices{margin-top:20px}.device{padding:14px 0;border-top:1px solid var(--border)}.device>div{display:grid;gap:5px;min-width:0}.device strong{font-size:13px;overflow-wrap:anywhere}@media(max-width:620px){.fields{grid-template-columns:1fr;gap:0}.card{padding:16px}.edition{display:none}.page-heading{align-items:flex-start}.actions .btn{white-space:normal}}
</style>
