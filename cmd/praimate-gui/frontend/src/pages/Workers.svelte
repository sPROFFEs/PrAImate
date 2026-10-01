<script>
  import { onMount, onDestroy } from 'svelte'
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
    instructions: '', timeoutSeconds: 120, maxInputBytes: 65536,
    allowEdits: false,
    allowCommands: false,
    maxOutputTokens: runtime === 'native' ? 2048 : 0,
  })
  let config = { workspace: '', profiles: [profile('primary', 'codex'), profile('middle', 'claude'), profile('fast', '', 'native')] }
  let clis = []
  let modelSuggestions = {}
  let runs = []
  let selected = ''
  let snapshot = null
  let task = ''
  let newTask = ''
  let error = ''
  let loading = true
  let showCreate = false
  let editingTitle = false
  let renameTitle = ''
  let deletePending = false
  let open = { primary: true, middle: true, fast: true }
  let disposed = false
  let submitting = false
  let snapshotStamp = ''
  let modelRequests = new Map()
  let visibleCounts = { primary: 80, middle: 80, fast: 80 }
  $: eventsByTier = Object.fromEntries(tiers.map(({ id }) => [id, (snapshot?.events || []).filter((event) => event.tier === id)]))
  const poller = createPoller(loadRuns, { delay: () => runs.some((run) => run.status === 'running') ? 1000 : 10000 })
  const refresh = () => poller.request()

  async function loadRuns() {
    try {
      const nextRuns = (await api.workerRuns()) || []
      if (disposed) return
      runs = nextRuns
      if (!runs.some((run) => run.id === selected)) selected = runs[0]?.id || ''
      const id = selected
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

  function runtimeChanged(p) {
    p.cli = p.runtime === 'cli' ? 'codex' : ''
    p.endpoint = p.runtime === 'native' ? 'http://localhost:11434/v1' : ''
    p.maxOutputTokens = p.runtime === 'native' ? 2048 : 0
    p.allowEdits = false
    p.allowCommands = false
    config = { ...config }
    if (p.runtime === 'cli') loadModels(p.cli)
  }

  async function loadModels(cli) {
    if (!cli || modelSuggestions[cli]) return
    if (!modelRequests.has(cli)) modelRequests.set(cli, api.listCLIModels(cli).then((models) => {
      if (!disposed) modelSuggestions = { ...modelSuggestions, [cli]: models || [] }
    }).catch(() => {}).finally(() => modelRequests.delete(cli)))
    return modelRequests.get(cli)
  }

  function cliChanged(p) {
    p.model = ''
    p.allowEdits = false
    p.allowCommands = false
    config = { ...config }
    loadModels(p.cli)
  }

  async function start() {
    if (submitting || !newTask.trim()) return
    submitting = true
    error = ''
    try {
      const id = await api.startWorkerRunWithConfig(newTask, config)
      selected = id
      newTask = ''
      task = ''
      showCreate = false
      await refresh()
    } catch (e) { error = String(e) } finally { submitting = false }
  }

  async function continueRun() {
    if (submitting || !selected || !task.trim()) return
    submitting = true
    error = ''
    try {
      await api.continueWorkerRun(selected, task)
      task = ''
      await refresh()
    } catch (e) { error = String(e) } finally { submitting = false }
  }

  async function cancel() {
    if (!selected) return
    try { await api.cancelWorkerRun(selected); await refresh() }
    catch (e) { error = String(e) }
  }

  function selectRun(id) {
    selected = id
    editingTitle = false
    snapshot = null
    visibleCounts = { primary: 80, middle: 80, fast: 80 }
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

  function runProfile(tier) { return (snapshot?.profiles || []).find((p) => p.tier === tier) }
  function shortTask(value) { return value.length > 42 ? value.slice(0, 42) + '…' : value }
</script>

<section class="workers">
  <header class="heading">
    <div>
      <h1>Workers</h1>
      <p>One task. Three workers. Follow every handoff and pick up where you left off.</p>
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
            <button disabled={snapshot.status === 'running'} on:click={() => deletePending = true}>Delete</button>
          {/if}
        </div>
        <p class="chat-meta">{snapshot.workspace} · Started {new Date(snapshot.startedAt).toLocaleString()}</p>
        <div class="runbar"><span>{snapshot.status} · {snapshot.currentTask || snapshot.task}</span>{#if snapshot.status === 'running'}<button on:click={cancel}>Stop</button>{/if}</div>
        {#if snapshot.turns?.length}<div class="turns">{#each snapshot.turns as turn}<p><strong>You:</strong> {turn.task}<br /><strong>Reasoner:</strong> {turn.result || turn.error}</p>{/each}</div>{/if}
        <div class="lanes">
          {#each tiers as tier}
            <section class:closed={!open[tier.id]}>
              <button class="lanehead" aria-expanded={open[tier.id]} on:click={() => open = { ...open, [tier.id]: !open[tier.id] }}>
                <strong>{tier.label}<small> · {runProfile(tier.id)?.cli || 'Local'} / {runProfile(tier.id)?.model || '—'}</small></strong><span>{open[tier.id] ? 'Hide' : 'Show'}</span>
              </button>
              {#if open[tier.id]}
                <div class="messages">
                  {#if eventsByTier[tier.id].length > visibleCounts[tier.id]}<button class="earlier" on:click={() => visibleCounts = { ...visibleCounts, [tier.id]: visibleCounts[tier.id] + 80 }}>Show earlier activity ({eventsByTier[tier.id].length - visibleCounts[tier.id]})</button>{/if}
                  {#each eventsByTier[tier.id].slice(-visibleCounts[tier.id]) as event}
                    <article class={event.kind}>
                      <small>{event.kind} · {new Date(event.timestamp).toLocaleTimeString()}{event.usage?.source === 'provider' ? ` · ${event.usage.inputTokens}/${event.usage.outputTokens} tokens` : ''}</small>
                      <pre>{event.text}</pre>
                    </article>
                  {/each}
                  {#if !eventsByTier[tier.id].length}<p class="empty">No activity yet</p>{/if}
                </div>
              {/if}
            </section>
          {/each}
        </div>
        {#if snapshot.result}<p class="result"><strong>Result</strong><br />{snapshot.result}</p>{/if}
        {#if snapshot.error}<p class="error" role="alert">{snapshot.error}</p>{/if}
        <div class="composer">
          <textarea aria-label="Follow-up task" bind:value={task} rows="3" placeholder="Ask the reasoner to continue this chat…" disabled={snapshot.status === 'running'}></textarea>
          <button class="primary" disabled={submitting || snapshot.status === 'running' || !task.trim()} on:click={continueRun}>{submitting ? 'Sending…' : 'Continue chat'}</button>
        </div>
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
        {#if error}<p class="error" role="alert">{error}</p>{/if}
        <label>Task for the reasoner <textarea bind:value={newTask} rows="3" placeholder="Describe the task to complete…"></textarea></label>
        <div class="settings">
      <label class="workspace">Workspace
        <span><input bind:value={config.workspace} placeholder="/absolute/path/to/project" /><button on:click={chooseWorkspace}>Choose</button></span>
      </label>
      <div class="profiles">
        {#each tiers as tier, index}
          {@const p = config.profiles[index]}
          <fieldset>
            <legend>{tier.label}</legend>
            <label>Runtime
              <select bind:value={config.profiles[index].runtime} on:change={() => runtimeChanged(p)}><option value="cli">CLI</option><option value="native">Local model / compatible API</option></select>
            </label>
            {#if p.runtime === 'cli'}
              <label>CLI
                <select bind:value={config.profiles[index].cli} on:change={() => cliChanged(p)}>
                  <option value="">Select CLI</option>
                  {#if p.cli && !clis.some((cli) => cli.id === p.cli)}<option value={p.cli}>{p.cli}</option>{/if}
                  {#each clis as cli}<option value={cli.id} disabled={!cli.available || cli.capabilities?.managedWorker === false}>{cli.label}{cli.capabilities?.managedWorker === false ? ' (managed workers unsupported)' : cli.available ? '' : ' (unavailable)'}</option>{/each}
                </select>
              </label>
              <label class="check"><input type="checkbox" bind:checked={config.profiles[index].allowEdits} /> Allow workspace edits</label>
            {:else}
              <label>Endpoint <input bind:value={config.profiles[index].endpoint} placeholder="http://localhost:11434/v1" /></label>
              <label>Output token limit <input type="number" min="1" max="32768" bind:value={config.profiles[index].maxOutputTokens} /></label>
              <label class="check"><input type="checkbox" bind:checked={config.profiles[index].allowEdits} /> Allow workspace edits</label>
            {/if}
            <label class="check"><input type="checkbox" bind:checked={config.profiles[index].allowCommands} /> Allow commands with user approval</label>
            <label>Model <input list={'worker-models-' + tier.id} bind:value={config.profiles[index].model} placeholder={p.runtime === 'cli' ? 'Model ID or alias' : 'Local model'} /></label>
            <datalist id={'worker-models-' + tier.id}>
              {#each (modelSuggestions[p.cli] || []) as model}<option value={model}></option>{/each}
            </datalist>
            <label>Additional instructions <textarea rows="2" bind:value={config.profiles[index].instructions}></textarea></label>
            <label>Timeout (seconds) <input type="number" min="1" max="600" bind:value={config.profiles[index].timeoutSeconds} /></label>
            <label>Maximum input bytes <input type="number" min="1024" max="1048576" bind:value={config.profiles[index].maxInputBytes} /></label>
          </fieldset>
        {/each}
      </div>
        </div>
        <div class="dialog-actions"><button on:click={() => showCreate = false}>Cancel</button><button class="primary" disabled={submitting || !newTask.trim()} on:click={start}>{submitting ? 'Creating…' : 'Create chat'}</button></div>
      </div>
    </div>
  {/if}

  {#if deletePending}
    <div class="dialog-backdrop">
      <div class="delete-dialog" role="dialog" aria-modal="true" aria-label="Delete worker chat" use:focusDialog={{ onClose: () => deletePending = false }}>
        <h2>Delete worker chat?</h2><p>This removes its saved task history and worker activity.</p>
        {#if error}<p class="error" role="alert">{error}</p>{/if}
        <div class="dialog-actions"><button on:click={() => deletePending = false}>Cancel</button><button on:click={deleteRun}>Delete chat</button></div>
      </div>
    </div>
  {/if}
</section>

<style>
  .workers { padding: 0; height: 100%; overflow: auto; background: var(--bg); color: var(--text); }
  .heading,.runbar { display:flex; align-items:center; justify-content:space-between; gap:16px; }
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
  .profiles { display:grid; grid-template-columns:repeat(3,minmax(210px,1fr)); gap:12px; margin-bottom:12px; }
  fieldset { border:1px solid var(--border); border-radius:8px; min-width:0; padding:12px; }
  legend { font-weight:600; }
  label { display:block; font-size:.85rem; margin-bottom:10px; }
  input, select, textarea { display:block; width:100%; box-sizing:border-box; margin-top:4px; padding:7px; border:1px solid var(--border-bright); border-radius:var(--radius-sm); color:var(--text); background:var(--bg-input); }
  input::placeholder, textarea::placeholder { color:var(--text-dim); }
  input:focus, select:focus, textarea:focus { outline:2px solid var(--focus); outline-offset:2px; border-color:var(--focus); }
  select option { background:var(--bg-panel); color:var(--text); }
  .check { display:flex; align-items:center; gap:8px; } .check input { width:auto; margin:0; }
  .composer { display:flex; gap:10px; align-items:end; margin:16px 0; }
  .composer textarea { flex:1; }
  small { color:var(--text-dim); }
  .runbar { margin:8px 0; }
  .lanes { display:flex; gap:10px; min-height:320px; }
  .lanes section { flex:1; min-width:0; border:1px solid var(--border); border-radius:var(--radius); display:flex; flex-direction:column; background:var(--bg-panel); box-shadow:var(--shadow-sm); overflow:hidden; }
  .lanes section.closed { flex:0 0 52px; }
  .lanehead { width:100%; display:flex; justify-content:space-between; border:0; border-bottom:1px solid var(--border); border-radius:8px 8px 0 0; }
  .closed .lanehead { writing-mode:vertical-rl; min-height:300px; align-items:center; }
  .messages { overflow:auto; max-height:55vh; padding:10px; }
  .earlier { width:100%; margin-bottom:10px; }
  article { padding:12px; margin-bottom:9px; border-radius:7px; background:var(--bg-raised); }
  article.request { border-left:3px solid var(--accent); } article.response { border-left:3px solid var(--ok); }
  article.error { border-left:3px solid var(--err); }
  pre { white-space:pre-wrap; overflow-wrap:anywhere; font:inherit; margin:5px 0 0; }
  .error { color:var(--err); } .empty { color:var(--text-dim); }
  .result { padding:12px; border:1px solid var(--border); border-radius:8px; background:var(--bg-panel); white-space:pre-wrap; }
  .turns { max-height:180px; overflow:auto; border:1px solid var(--border); border-radius:8px; padding:8px; margin-bottom:10px; background:var(--bg-panel); white-space:pre-wrap; }
  @media (max-width:900px) { .profiles { grid-template-columns:1fr; } .lanes { overflow-x:auto; } .lanes section:not(.closed) { flex:0 0 70vw; } }
  @media (max-width:700px) { .chat-layout { grid-template-columns:1fr; } .chat-list { flex-direction:row; max-height:none; border-right:0; border-bottom:1px solid var(--border); padding:0 0 10px; } .chat-list button { min-width:170px; width:170px; } }
</style>
