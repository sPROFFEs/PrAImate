<script>
  import { onMount, onDestroy } from 'svelte'
  import { api, onApproval } from '../lib/api.js'
  import { createPoller } from '../lib/poller.js'
  import WorkerMonitor from '../lib/WorkerMonitor.svelte'
  export let mode
  let snapshot = null
  let clis = []
  let models = {}
  let approvals = []
  let connected = true
  let error = ''
  let disposed = false
  let modelRequests = new Set()
  let cleanup = []
  const poller = createPoller(load, { delay: () => ['running','planning','merging'].includes(snapshot?.status) ? 750 : 5000 })
  async function load() {
    try {
      const [next, pending] = await Promise.all([api.workerRunSnapshot(mode.sessionId), api.workerRunApprovals(mode.sessionId)])
      if (!disposed) { snapshot = next; approvals = pending || []; connected = true; error = '' }
    } catch (e) { if (!disposed) { connected = false; error = String(e) } }
  }
  async function loadModels(cli) {
    if (!cli || models[cli] || modelRequests.has(cli)) return
    modelRequests.add(cli)
    try { const choices = await api.studioListCLIModels(cli); if (!disposed) models = {...models,[cli]:choices || []} }
    catch (e) { error = String(e) }
    finally { modelRequests.delete(cli) }
  }
  async function approve(request, allow) {
    try { await api.resolveApproval(request.id, allow, false); approvals = approvals.filter(r => r.id !== request.id) }
    catch (e) { error = String(e) }
  }
  onMount(() => {
    poller.request()
    api.studioListCLIs().then(value => { if (!disposed) clis = value || [] }).catch(e => error = String(e))
    cleanup.push(onApproval(request => { if (request.chatId === mode.sessionId && !approvals.some(r => r.id === request.id)) approvals = [...approvals,request] }))
    if (window.runtime?.EventsOn) {
      cleanup.push(window.runtime.EventsOn('praimate:approval-resolved', request => { approvals = approvals.filter(item => item.id !== request.id) }))
      cleanup.push(window.runtime.EventsOn('praimate:detached-connected', () => poller.request()))
      cleanup.push(window.runtime.EventsOn('praimate:detached-disconnected', () => { connected = false; error = 'Main PrAImate window disconnected. Execution is managed by the main process.' }))
    }
  })
  onDestroy(() => { disposed = true; poller.stop(); cleanup.forEach(fn => fn?.()) })
</script>
<main class="execution-window">
  <header><div><small>WORKER EXECUTION</small><h1>{snapshot?.title || mode.title}</h1><p>{snapshot?.workspace}</p></div><span class:offline={!connected}>{connected ? 'Connected to PrAImate' : 'Disconnected'}</span></header>
  <p class="hint">Closing this window keeps the run active and its history saved. Approvals are also available in the main window.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#each approvals as request (request.id)}<section class="approval"><strong>Approval required · {request.tool}</strong><pre>{request.detail}</pre><button on:click={() => approve(request,false)}>Deny</button><button on:click={() => approve(request,true)}>Allow once</button></section>{/each}
  {#if snapshot}<fieldset disabled={!connected}><WorkerMonitor {snapshot} {clis} {models} {approvals} on:models={e => loadModels(e.detail)} on:refresh={() => poller.request()} /></fieldset>{:else}<p class="hint">Connecting to the saved execution…</p>{/if}
</main>
<style>
  .execution-window{padding:24px;min-height:100vh;background:var(--bg);color:var(--text)}header{display:flex;align-items:flex-start;justify-content:space-between;gap:16px}h1{font-size:22px;line-height:1.4;margin:6px 0;overflow-wrap:anywhere}header small{font-size:10px;letter-spacing:.1em;color:var(--accent)}header p{color:var(--text-dim);font-size:12px;overflow-wrap:anywhere}header>span{color:var(--ok);font-size:11px;white-space:nowrap}.offline,.error{color:var(--err)}.hint{font-size:12px;color:var(--text-dim);margin:12px 0 22px}fieldset{border:0;padding:0;margin:0;min-width:0}.approval{border:1px solid var(--warn);background:var(--bg-panel);border-radius:var(--radius);padding:16px;margin:12px 0}.approval pre{white-space:pre-wrap;overflow-wrap:anywhere}.approval button{background:var(--bg-raised);color:var(--text);border:1px solid var(--border-bright);border-radius:var(--radius-sm);padding:8px 12px;cursor:pointer;margin-right:8px}
</style>
