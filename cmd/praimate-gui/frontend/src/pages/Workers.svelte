<script>
  import VoiceButton from '../lib/VoiceButton.svelte'
  import WorkerMonitor from '../lib/WorkerMonitor.svelte'
  import WorkerProfiles from '../lib/WorkerProfiles.svelte'
  import { onMount, onDestroy } from 'svelte'
  import { openWorkerId } from '../lib/stores.js'
  import { api } from '../lib/api.js'
  import { createPoller } from '../lib/poller.js'
  import { focusDialog } from '../lib/focusDialog.js'

  const tiers = [
    { id: 'primary', label: 'Reasoner' },
    { id: 'middle', label: 'Middle' },
    { id: 'fast', label: 'Fast' },
  ]
  const profile = (tier, cli, runtime = 'cli') => ({
    tier, runtime, cli: runtime === 'cli' ? cli : '', model: '',
    endpoint: runtime === 'native' ? 'http://localhost:11434/v1' : '',
    instructions: '', reasoningEffort: '', timeoutSeconds: runtime === 'cli' ? 0 : tier === 'primary' ? 600 : 300, maxInputBytes: 65536,
    allowEdits: false,
    allowCommands: false,
    maxOutputTokens: runtime === 'native' ? 2048 : 0,
  })
  let config = { workspace: '', accessMode:'supervised',maxRetries:0,retryDelaySeconds:5, profiles: [profile('primary', 'codex'), profile('middle', 'claude'), profile('fast', '', 'native')] }
  let clis = []
  let modelSuggestions = {}
  let runs = []
  let selected = $openWorkerId || ''
  let snapshot = null
	let approvals = []
  let task = ''
  let newTask = ''
  let executionMode = 'parallel'
  let maxParallel = 2
  let error = ''
  let loading = true
  let showCreate = false
  let editingTitle = false
  let renameTitle = ''
  let deletePending = false
  let disposed = false
  let submitting = false
  let snapshotStamp = ''
  let modelRequests = new Map()
  const poller = createPoller(loadRuns, { delay: () => runs.some((run) => ['running', 'planning', 'merging'].includes(run.status)) ? 1000 : 10000 })
  const refresh = () => poller.request()

  async function loadRuns() {
    try {
      const nextRuns = (await api.workerRuns()) || []
      if (disposed) return
      runs = nextRuns
      if ($openWorkerId && runs.some(run => run.id === $openWorkerId)) { selected = $openWorkerId; openWorkerId.set(null) }
      if (!runs.some((run) => run.id === selected)) selected = runs[0]?.id || ''
      const id = selected
      const pending = id ? await api.workerRunApprovals(id) : []
      if (!disposed && selected === id) approvals = pending || []
      const summary = runs.find((run) => run.id === id)
      const stamp = `${id}:${summary?.updatedAt}:${summary?.status}`
      if (id && (!snapshot || summary?.status === 'running' || snapshotStamp !== stamp)) {
        const current = await api.workerRunSnapshot(id)
        if (!disposed && selected === id) { snapshot = current; snapshotStamp = stamp }
      } else if (!id) { snapshot = null; snapshotStamp = '' }
    } catch (e) { error = String(e) }
  }

  onMount(() => {
    ;(async () => {
      try { clis = (await api.listCLIs()) || [] }
      catch (e) { error = String(e) }
      try {
        const saved = await api.workerConfig()
        if (saved?.profiles?.length) config = { ...saved, profiles: tiers.map((tier, index) => saved.profiles.find((p) => p.tier === tier.id) || config.profiles[index]) }
      } catch (e) {
        // A fresh installation has no worker profile yet.
        if (!String(e).includes('not found')) error = String(e)
      }
      await Promise.all(config.profiles.filter((p) => p.runtime === 'cli').map((p) => loadModels(p.cli)))
      await refresh()
      loading = false
    })()
    const onVisible = () => { if (!document.hidden) refresh() }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  })
  onDestroy(() => { disposed = true; poller.stop() })

  async function chooseWorkspace() {
    try {
      const folder = await api.pickFolder()
      if (folder) config = { ...config, workspace: folder }
    } catch (e) { error = String(e) }
  }

  async function loadModels(cli) {
    if (!cli || modelSuggestions[cli]) return
    if (!modelRequests.has(cli)) modelRequests.set(cli, api.listCLIModels(cli).then((models) => {
      if (!disposed) modelSuggestions = { ...modelSuggestions, [cli]: models || [] }
    }).catch(() => {}).finally(() => modelRequests.delete(cli)))
    return modelRequests.get(cli)
  }

  async function start() {
    if (submitting || !newTask.trim()) return
    submitting = true
    error = ''
    try {
      const id = executionMode === 'parallel' ? await api.planWorkerDAG(newTask, config, maxParallel) : await api.startWorkerRunWithConfig(newTask, config)
      selected = id
      newTask = ''
      task = ''
      showCreate = false
      await refresh()
    } catch (e) { error = String(e) } finally { submitting = false }
  }

  function selectRun(id) {
    selected = id
    editingTitle = false
    error = ''
    snapshot = null
    refresh()
  }

  async function renameRun() {
    if (!selected || !renameTitle.trim()) return
    try {
      await api.renameWorkerRun(selected, renameTitle.trim())
      editingTitle = false
      await refresh()
    } catch (e) { error = String(e) }
  }

  async function deleteRun() {
    if (!selected) return
    try {
      await api.deleteWorkerRun(selected)
      deletePending = false
      selected = ''
      snapshot = null
      await refresh()
    } catch (e) { error = String(e) }
  }

  function shortTask(value) { return value.length > 42 ? value.slice(0, 42) + '…' : value }
</script>

<section class="workers">
  <header class="heading">
    <div>
      <h1>Workers</h1>
      <p>Coordinate a task or run a parallel plan. Review every handoff and change.</p>
    </div>
    <button class="primary" disabled={loading} on:click={() => { error = ''; showCreate = true }}>New worker chat</button>
  </header>

  {#if error}<p class="error" role="alert">{error}</p>{/if}

  <div class="chat-layout">
    <nav class="chat-list" aria-label="Saved worker chats">
      {#if !runs.length}<p class="empty">No worker chats yet. Create one to get started.</p>{/if}
      {#each runs as run}
        <button class:active={selected === run.id} on:click={() => selectRun(run.id)}>
          <strong>{shortTask(run.title || run.task)}</strong>
          <small>{run.status} · {new Date(run.updatedAt || run.startedAt).toLocaleString()}</small>
        </button>
      {/each}
    </nav>
    <div class="chat-detail">
      {#if snapshot}
        <div class="chat-heading">
          {#if editingTitle}
            <input aria-label="Worker chat title" bind:value={renameTitle} maxlength="200" on:keydown={(event) => { if (event.key === 'Enter') renameRun(); if (event.key === 'Escape') editingTitle = false }} />
            <button on:click={renameRun}>Save name</button><button on:click={() => editingTitle = false}>Cancel</button>
          {:else}
            <h2>{snapshot.title || snapshot.task}</h2>
            <button on:click={() => { renameTitle = snapshot.title || snapshot.task; editingTitle = true }}>Rename</button>
            <button disabled={['running', 'planning', 'merging'].includes(snapshot.status)} on:click={() => deletePending = true}>Delete</button>
          {/if}
        </div>
        <p class="chat-meta">{snapshot.workspace} · Started {new Date(snapshot.startedAt).toLocaleString()}</p>
        <div class="monitor-link"><button on:click={async () => { try { await api.detachSession('workers', snapshot.id, snapshot.title || 'Worker execution') } catch (e) { error = String(e) } }}>Open execution window ↗</button><span>Track assignments, backend activity and handoffs in a separate window.</span></div>
        <WorkerMonitor {snapshot} {clis} {approvals} models={modelSuggestions} on:models={event => loadModels(event.detail)} on:refresh={refresh} />
      {:else}
        <p class="empty">Select a saved worker chat or create a new one.</p>
      {/if}
    </div>
  </div>

  {#if showCreate}
    <div class="dialog-backdrop">
      <div class="create-dialog" role="dialog" aria-modal="true" aria-label="New worker chat" use:focusDialog={{ onClose: () => { if (!submitting) showCreate = false } }}>
        <header class="dialog-heading"><h2>New worker chat</h2><button aria-label="Close" on:click={() => showCreate = false}>×</button></header>
        <p>Choose a workspace and configure the three workers for this chat. These profiles remain attached to its saved history.</p>
        <label>Execution mode <select bind:value={executionMode}><option value="hierarchical">Hierarchical worker chat</option><option value="parallel">Parallel task graph · isolated Git worktrees</option></select></label>
        {#if executionMode === 'parallel'}<label>Concurrent tasks <input type="number" min="1" max="4" bind:value={maxParallel} /></label><p>The workspace must be a clean Git repository. The reasoner drafts a plan for review before execution; changes reach your branch only after you accept and merge them.</p>{/if}
        {#if error}<p class="error" role="alert">{error}</p>{/if}
        <div data-voice-composer><VoiceButton context={{page:'workers'}} on:transcript={event => { newTask = [newTask,event.detail.text].filter(Boolean).join(' ') }} />
        <label>Task for the reasoner <textarea bind:value={newTask} rows="3" placeholder="Describe the task to complete…"></textarea></label></div>
        <div class="settings">
      <label class="workspace">Workspace
        <span><input bind:value={config.workspace} placeholder="/absolute/path/to/project" /><button on:click={chooseWorkspace}>Choose</button></span>
      </label>
      <WorkerProfiles bind:config {clis} models={modelSuggestions} on:models={event => loadModels(event.detail)} />
        </div>
        <div class="dialog-actions"><button on:click={() => showCreate = false}>Cancel</button><button class="primary" disabled={submitting || !newTask.trim()} on:click={start}>{submitting ? 'Creating…' : executionMode === 'parallel' ? 'Create plan' : 'Create chat'}</button></div>
      </div>
    </div>
  {/if}

  {#if deletePending}
    <div class="dialog-backdrop">
      <div class="delete-dialog" role="dialog" aria-modal="true" aria-label="Delete worker chat" use:focusDialog={{ onClose: () => deletePending = false }}>
        <h2>Delete worker chat?</h2><p>This removes its saved task history, worker activity and temporary worktrees, including any unmerged or failed task changes.</p>
        {#if error}<p class="error" role="alert">{error}</p>{/if}
        <div class="dialog-actions"><button on:click={() => deletePending = false}>Cancel</button><button on:click={deleteRun}>Delete chat</button></div>
      </div>
    </div>
  {/if}
</section>

<style>
  .workers { padding: 0; height: 100%; overflow: auto; background: var(--bg); color: var(--text); }
  .heading { display:flex; align-items:center; justify-content:space-between; gap:16px; }
  h1 { margin:0 0 4px; } p { margin: 4px 0 14px; }
  .heading p { color: var(--text-dim); }
  button, input, select, textarea { font:inherit; }
  button { cursor:pointer; border:1px solid var(--border-bright); background:var(--bg-panel); color:var(--text); border-radius:var(--radius-sm); padding:8px 12px; transition:background 140ms var(--ease), border-color 140ms var(--ease); }
  button:hover:not(:disabled) { background:var(--bg-raised); }
  button:disabled { opacity:.5; cursor:default; }
  button.primary { background:var(--accent); border-color:var(--accent); color:var(--accent-fg); }
  button.primary:hover:not(:disabled) { background:var(--accent); filter:brightness(.92); }
  .chat-layout { display:grid; grid-template-columns:240px minmax(0,1fr); gap:16px; margin-top:16px; min-height:400px; }
  .chat-list { display:flex; flex-direction:column; gap:6px; overflow:auto; max-height:calc(100vh - 155px); border-right:1px solid var(--border); padding-right:12px; }
  .chat-list button { display:flex; flex-direction:column; align-items:flex-start; gap:2px; width:100%; text-align:left; overflow:hidden; }
  .chat-list button strong, .chat-list button small { display:block; max-width:100%; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .chat-list button.active { border-color:var(--accent); background:var(--accent-soft); }
  .chat-detail { min-width:0; }
  .chat-heading { display:flex; align-items:center; gap:8px; }
  .chat-heading h2 { flex:1; min-width:0; overflow-wrap:anywhere; margin:0; }
  .chat-heading input { flex:1; min-width:0; }
  .monitor-link { display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin:16px 0; }
  .monitor-link span { color:var(--text-dim);font-size:12px; }
  .chat-meta { color:var(--text-dim); overflow-wrap:anywhere; }
  .dialog-backdrop { position:fixed; inset:0; z-index:1000; display:flex; align-items:center; justify-content:center; padding:16px; background:var(--overlay); animation:overlay-enter 140ms var(--ease); }
  .create-dialog, .delete-dialog { width:min(1080px,100%); max-height:calc(100vh - 32px); overflow:auto; padding:20px; border:1px solid var(--border-bright); border-radius:var(--radius); background:var(--bg-panel); color:var(--text); box-shadow:var(--shadow-overlay); animation:surface-enter 180ms var(--ease); }
  .delete-dialog { width:min(420px,100%); }
  .dialog-heading, .dialog-actions { display:flex; align-items:center; justify-content:space-between; gap:8px; }
  .dialog-heading h2 { margin:0; }
  .dialog-actions { justify-content:flex-end; }
  .settings { border:1px solid var(--border); border-radius:8px; padding:16px; margin:16px 0; background:var(--bg-panel); }
  .workspace { display:block; margin-bottom:14px; }
  .workspace span { display:flex; gap:8px; }
  .workspace input { flex:1; }
  label { display:block; font-size:.85rem; margin-bottom:10px; }
  input, select, textarea { display:block; width:100%; box-sizing:border-box; margin-top:4px; padding:7px; border:1px solid var(--border-bright); border-radius:var(--radius-sm); color:var(--text); background:var(--bg-input); }
  input::placeholder, textarea::placeholder { color:var(--text-dim); }
  input:focus, select:focus, textarea:focus { outline:2px solid var(--focus); outline-offset:2px; border-color:var(--focus); }
  select option { background:var(--bg-panel); color:var(--text); }
  small { color:var(--text-dim); }
  .error { color:var(--err); } .empty { color:var(--text-dim); }
  @media (max-width:700px) { .chat-layout { grid-template-columns:1fr; } .chat-list { flex-direction:row; max-height:none; border-right:0; border-bottom:1px solid var(--border); padding:0 0 10px; } .chat-list button { min-width:170px; width:170px; } }
</style>
