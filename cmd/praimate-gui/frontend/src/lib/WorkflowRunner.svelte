<script>
  import { createEventDispatcher, onDestroy, onMount } from 'svelte'
  import { api, onTurn, onWorkflowStream, onChatStream } from './api.js'
  import { renderMarkdown } from './markdown.js'
  import { localRoutingUnavailableMessage, supportsLocalRouting } from './localRouting.js'

  export let agent
  export let localOpt = null

  const dispatch = createEventDispatcher()

  let error = ''
  let workflow = null
  let runMode = 'single'
  let cli = ''
  let runModel = ''
  let runSuggestions = []
  let runModelLoading = false
  let runUseLocal = false
  let runLocalModel = ''
  let runLocalOpt = null
  let cwd = ''
  let inputs = {}
  let inputsByWorkflow = {}
  let privacyCounts = null
  let running = false
  let turns = []
  let result = null
  let workflowStream = null
  let runConversation = []
  let runDraft = ''
  let runChatSending = false
  let runChatStream = null
  let cleanedWorkflowChatID = ''
  let preflight = null
  let unsubscribe = () => {}
  let unsubscribeWorkflow = () => {}
  let unsubscribeRunChat = () => {}
  $: runLocalRoutable = supportsLocalRouting(cli)

  onMount(() => {
    init(agent)
  })

  function init(a) {
    cli = a?.supports?.[0] || 'claude'
    runModel = ''
    runSuggestions = []
    runUseLocal = false
    runLocalModel = ''
    runMode = 'single'
    const def = a?.workflows?.find((w) => w.name === a.default_workflow)
    workflow = def || a?.workflows?.[0] || null
    inputs = {}
    inputsByWorkflow = {}
    privacyCounts = null
    result = null
    turns = []
    workflowStream = null
    runConversation = []
    runDraft = ''
    runChatSending = false
    runChatStream = null
    preflight = null
    runLocalOpt = localOpt
    if (workflow) for (const inp of workflow.inputs || []) inputs[inp.name] = inp.default || ''
    initAllWorkflowInputs(a)
    if (runLocalOpt === null) {
      api.localLLMModels().then((r) => { runLocalOpt = r }).catch(() => { runLocalOpt = { configured: false } })
    }
    loadRunModels()
  }

  function pickWorkflow(w) {
    workflow = w
    inputs = {}
    privacyCounts = null
    for (const inp of w.inputs || []) inputs[inp.name] = inp.default || ''
  }

  function initAllWorkflowInputs(a) {
    const next = {}
    for (const w of a?.workflows || []) {
      next[w.name] = {}
      for (const inp of w.inputs || []) next[w.name][inp.name] = inp.default || ''
    }
    inputsByWorkflow = next
  }

  async function runCliChanged() {
    if (runUseLocal && !runLocalRoutable) runUseLocal = false
    preflight = null
    await loadRunModels()
  }

  async function loadRunModels() {
    if (!cli) { runSuggestions = []; return }
    runModelLoading = true
    try { runSuggestions = (await api.listCLIModels(cli)) || [] } catch { runSuggestions = [] }
    finally { runModelLoading = false }
  }

  function workflowInputText() {
    if (runMode === 'all') {
      return Object.values(inputsByWorkflow).flatMap((m) => Object.values(m || {})).join(' ')
    }
    return Object.values(inputs).join(' ')
  }

  async function chooseFolder() {
    try { const p = await api.pickFolder(); if (p) { cwd = p; preflight = null } } catch (e) { error = String(e) }
  }

  async function review() {
    if (!cwd.trim()) {
      error = 'Pick a working folder first.'
      return
    }
    try {
      privacyCounts = (await api.privacyPreview(workflowInputText())) || {}
    } catch (e) { error = String(e) }
  }

  function resetWorkflowStream() {
    workflowStream = { current: '', turnIndex: -1, text: '', reasoning: [], tools: [], steps: [] }
  }

  function handleWorkflowStream(ev) {
    if (!workflowStream) resetWorkflowStream()
    if (ev.type === 'turn_start') {
      workflowStream = {
        current: ev.workflow_name || workflowStream.current || '',
        turnIndex: Number.isInteger(ev.turn_index) ? ev.turn_index : -1,
        text: '',
        reasoning: [],
        tools: [],
        steps: [{ type: ev.type, detail: ev.detail || ev.workflow_name || '', ok: ev.ok !== false }],
      }
      return
    }
    if (ev.workflow_name) workflowStream.current = ev.workflow_name
    if (ev.type === 'text') workflowStream.text += ev.text || ''
    else if (ev.type === 'reasoning' && ev.text) workflowStream.reasoning = [...workflowStream.reasoning, ev.text]
    else if (ev.type === 'tool_start') workflowStream.tools = [...workflowStream.tools, { id: ev.id, tool: ev.tool, detail: ev.detail, done: false, ok: true }]
    else if (ev.type === 'tool_end') {
      const idx = ev.id ? workflowStream.tools.findIndex((t) => t.id === ev.id && !t.done) : workflowStream.tools.findIndex((t) => !t.done)
      if (idx >= 0) {
        workflowStream.tools[idx] = { ...workflowStream.tools[idx], done: true, ok: ev.ok !== false }
        workflowStream.tools = [...workflowStream.tools]
      }
    } else if (ev.type === 'step_start' || ev.type === 'step_finish' || ev.type === 'workflow_start' || ev.type === 'workflow_finish' || ev.type === 'turn_start' || ev.type === 'turn_finish' || ev.type === 'error') {
      workflowStream.steps = [...workflowStream.steps, { type: ev.type, detail: ev.detail || ev.workflow_name || '', ok: ev.ok !== false }]
    }
    workflowStream = workflowStream
  }

  function workflowStatusText() {
    if (!workflowStream?.current) return 'Starting workflow run…'
    const turn = workflowStream.turnIndex >= 0 ? ` · turn ${workflowStream.turnIndex + 1}` : ''
    return `Current workflow: ${workflowStream.current}${turn}`
  }

  function handleRunChatStream(ev) {
    if (!result?.chat_id || ev.chatId !== result.chat_id) return
    if (!runChatStream) runChatStream = { text: '', reasoning: [], tools: [], steps: [] }
    if (ev.type === 'text') runChatStream.text += ev.text || ''
    else if (ev.type === 'reasoning' && ev.text) runChatStream.reasoning = [...runChatStream.reasoning, ev.text]
    else if (ev.type === 'tool_start') runChatStream.tools = [...runChatStream.tools, { id: ev.id, tool: ev.tool, detail: ev.detail, done: false, ok: true }]
    else if (ev.type === 'tool_end') {
      const idx = ev.id ? runChatStream.tools.findIndex((t) => t.id === ev.id && !t.done) : runChatStream.tools.findIndex((t) => !t.done)
      if (idx >= 0) {
        runChatStream.tools[idx] = { ...runChatStream.tools[idx], done: true, ok: ev.ok !== false }
        runChatStream.tools = [...runChatStream.tools]
      }
    } else if (ev.type?.startsWith('step_') || ev.type === 'error') {
      runChatStream.steps = [...runChatStream.steps, { type: ev.type, detail: ev.detail || ev.text || '', ok: ev.ok !== false }]
    }
    runChatStream = runChatStream
  }

  function runLocalParams() {
    const local = runUseLocal && runLocalOpt?.configured && runLocalRoutable
    return {
      model: local ? '' : runModel.trim(),
      endpoint: local ? runLocalOpt.endpoint : '',
      apiKey: '',
      localModel: local ? runLocalModel.trim() : '',
    }
  }

  async function startRun() {
    if (!cwd.trim()) {
      error = 'Pick a working folder first.'
      privacyCounts = null
      return
    }
    const local = runLocalParams()
    preflight = await api.preflightExecution(agent.id, 'workflow', cli, local.model, '', cwd.trim(), local.endpoint, local.localModel)
      .catch((e) => ({ ok: false, issues: [{ severity: 'error', message: String(e) }] }))
    if (!preflight?.ok) {
      error = (preflight?.issues || []).filter((i) => i.severity === 'error').map((i) => i.message).join('\n') || 'Execution preflight failed.'
      return
    }
    const warnings = (preflight.issues || []).filter((i) => i.severity === 'warning')
    if (warnings.length && !window.confirm(`${warnings.map((i) => i.message).join('\n\n')}\n\nContinue with this workflow run?`)) return
    running = true
    turns = []
    result = null
    resetWorkflowStream()
    runConversation = []
    runDraft = ''
    runChatSending = false
    runChatStream = null
    error = ''
    unsubscribe = onTurn((t) => { turns = [...turns, t] })
    unsubscribeWorkflow = onWorkflowStream(handleWorkflowStream)
    const runCwd = cwd.trim()
    try {
      result = runMode === 'all'
        ? await api.runAllWorkflows(agent.id, cli, local.model, runCwd, inputsByWorkflow, local.endpoint, local.apiKey, local.localModel)
        : await api.runWorkflow(agent.id, workflow.name, cli, local.model, runCwd, inputs, local.endpoint, local.apiKey, local.localModel)
      if (result?.chat_id) {
        runConversation = (await api.chatMessages(result.chat_id).catch(() => [])) || []
        unsubscribeRunChat()
        unsubscribeRunChat = onChatStream(handleRunChatStream)
      }
    } catch (e) {
      error = String(e)
    } finally {
      unsubscribe()
      unsubscribeWorkflow()
      running = false
      privacyCounts = null
    }
  }

  async function sendRunChat() {
    const text = runDraft.trim()
    if (!text || runChatSending || !result?.chat_id) return
    runChatSending = true
    runChatStream = null
    error = ''
    runConversation = [...runConversation, { Role: 'user', Content: text, TS: new Date().toISOString(), _pending: true }]
    runDraft = ''
    try {
      await api.sendChatStream(result.chat_id, text, [])
      runConversation = (await api.chatMessages(result.chat_id)) || runConversation
    } catch (e) {
      error = String(e)
      runConversation = runConversation.filter((m) => !m._pending)
      runDraft = text
    } finally {
      runChatSending = false
      runChatStream = null
    }
  }

  function onRunChatKey(e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      sendRunChat()
    }
  }

  async function stopRunChat() {
    if (!result?.chat_id) return
    runChatSending = false
    runChatStream = null
    try { await api.cancelChatTurn(result.chat_id) } catch {}
  }

  async function cleanupRunWorkflowChat() {
    const chatID = result?.chat_id
    unsubscribeRunChat()
    unsubscribeRunChat = () => {}
    if (chatID && chatID !== cleanedWorkflowChatID) {
      cleanedWorkflowChatID = chatID
      try { await api.cancelChatTurn(chatID) } catch {}
      try { await api.deleteChat(chatID) } catch {}
    }
  }

  async function close() {
    await cleanupRunWorkflowChat()
    dispatch('close')
  }

  onDestroy(() => {
    unsubscribe()
    unsubscribeWorkflow()
    cleanupRunWorkflowChat()
  })

  $: matchTotal = privacyCounts ? Object.values(privacyCounts).reduce((a, b) => a + b, 0) : 0
</script>

{#if agent}
  {#if running}
    <div class="card">
      <div class="card-title">
        Running {agent.name} · {runMode === 'all' ? 'all workflows' : workflow.name} on {cli}…
      </div>
      <div class="card-sub">
        {workflowStatusText()}
      </div>
    </div>
    {#if workflowStream}
      {#if workflowStream.reasoning.length}
        <div class="tool-feed">
          {#each workflowStream.reasoning as r}
            <div class="tool-row reasoning-row"><span class="tool-status">?</span><span class="tool-name">reasoning</span><span class="tool-detail reasoning-detail">{r}</span></div>
          {/each}
        </div>
      {/if}
      {#if workflowStream.steps.length}
        <div class="tool-feed">
          {#each workflowStream.steps.slice(-20) as s}
            <div class="tool-row" class:err={!s.ok}>
              <span class="tool-status">{s.ok ? '◌' : '✗'}</span>
              <span class="tool-name">{s.type}</span>
              {#if s.detail}<span class="tool-detail mono">{s.detail}</span>{/if}
            </div>
          {/each}
        </div>
      {/if}
      {#if workflowStream.tools.length}
        <div class="tool-feed">
          {#each workflowStream.tools as t}
            <div class="tool-row" class:err={t.done && !t.ok}>
              <span class="tool-status">{t.done ? (t.ok ? '✓' : '✗') : '◌'}</span>
              <span class="tool-name">{t.tool}</span>
              {#if t.detail}<span class="tool-detail mono">{t.detail}</span>{/if}
            </div>
          {/each}
        </div>
      {/if}
      {#if workflowStream.text}
        <div class="msg assistant"><div class="who">assistant streaming</div><div class="markdown">{@html renderMarkdown(workflowStream.text)}</div><span class="cursor">▍</span></div>
      {/if}
    {/if}
    {#each turns as t}
      <div class="msg user"><div class="who">{t.workflow_name || 'workflow'} · you (turn {t.index + 1})</div>{t.user_msg}</div>
      <div class="msg assistant"><div class="who">assistant · {t.duration_ms}ms</div><div class="markdown">{@html renderMarkdown(t.reply)}</div></div>
    {/each}
  {:else if result}
    <div class="row" style="margin-bottom:14px">
      <button class="btn" on:click={close}>← Agents</button>
      <span class="pill" class:ok={result.outcome === 'completed'} class:err={result.outcome !== 'completed'}>{result.outcome}</span>
      {#if result.chat_id}<span class="pill">temporary session</span>{/if}
    </div>
    {#if error}<div class="banner">{error}</div>{/if}
    {#if result.error}<div class="banner">{result.error}</div>{/if}
    {#if runConversation.length}
      {#each runConversation as m}
        <div class={'msg ' + (m.Role === 'user' ? 'user' : 'assistant') + (m._pending ? ' pending' : '')}>
          <div class="who">{m.Role === 'user' ? 'you' : 'assistant'}</div>
          {#if m.Role === 'user'}{m.Content}{:else}<div class="markdown">{@html renderMarkdown(m.Content)}</div>{/if}
        </div>
      {/each}
    {:else}
      {#each result.turns || [] as t}
        <div class="msg user"><div class="who">{t.workflow_name || 'workflow'} · you (turn {t.index + 1})</div>{t.user_msg}</div>
        <div class="msg assistant"><div class="who">assistant · {t.duration_ms}ms</div><div class="markdown">{@html renderMarkdown(t.reply)}</div></div>
      {/each}
    {/if}
    {#if runChatStream}
      {#if runChatStream.reasoning.length}
        <div class="tool-feed">
          {#each runChatStream.reasoning as r}
            <div class="tool-row reasoning-row"><span class="tool-status">?</span><span class="tool-name">reasoning</span><span class="tool-detail reasoning-detail">{r}</span></div>
          {/each}
        </div>
      {/if}
      {#if runChatStream.steps.length}
        <div class="tool-feed">
          {#each runChatStream.steps.slice(-20) as s}
            <div class="tool-row" class:err={!s.ok}>
              <span class="tool-status">{s.ok ? '◌' : '✗'}</span>
              <span class="tool-name">{s.type}</span>
              {#if s.detail}<span class="tool-detail mono">{s.detail}</span>{/if}
            </div>
          {/each}
        </div>
      {/if}
      {#if runChatStream.tools.length}
        <div class="tool-feed">
          {#each runChatStream.tools as t}
            <div class="tool-row" class:err={t.done && !t.ok}>
              <span class="tool-status">{t.done ? (t.ok ? '✓' : '✗') : '◌'}</span>
              <span class="tool-name">{t.tool}</span>
              {#if t.detail}<span class="tool-detail mono">{t.detail}</span>{/if}
            </div>
          {/each}
        </div>
      {/if}
      {#if runChatStream.text}
        <div class="msg assistant"><div class="who">assistant streaming</div><div class="markdown">{@html renderMarkdown(runChatStream.text)}</div><span class="cursor">▍</span></div>
      {/if}
    {/if}
    {#if result.chat_id}
      <div class="workflow-composer">
        <textarea class="field" bind:value={runDraft} rows="3" placeholder="Continue this workflow conversation..." on:keydown={onRunChatKey} disabled={runChatSending}></textarea>
        <div class="row">
          <button class="btn primary" on:click={sendRunChat} disabled={runChatSending || !runDraft.trim()}>{runChatSending ? 'Sending…' : 'Send'}</button>
          {#if runChatSending}<button class="btn" on:click={stopRunChat}>Stop</button>{/if}
        </div>
      </div>
    {/if}
  {:else}
    <div class="row" style="margin-bottom:14px">
      <button class="btn" on:click={close}>← Agents</button>
      <strong>{agent.name} — run workflow</strong>
    </div>
    {#if error}<div class="banner">{error}</div>{/if}
    {#if (agent.workflows || []).length > 0}
      <label class="lbl">Run mode</label>
      <div class="row" style="flex-wrap:wrap">
        <button class="btn" class:primary={runMode === 'single'} on:click={() => (runMode = 'single', privacyCounts = null)}>One workflow</button>
        <button class="btn" class:primary={runMode === 'all'} on:click={() => (runMode = 'all', privacyCounts = null)}>Run all workflows</button>
      </div>
    {/if}
    {#if runMode === 'single' && (agent.workflows || []).length > 1}
      <label class="lbl">Workflow</label>
      <div class="row" style="flex-wrap:wrap">
        {#each agent.workflows as w}
          <button class="btn" class:primary={workflow?.name === w.name} on:click={() => pickWorkflow(w)}>{w.name}</button>
        {/each}
      </div>
    {/if}
    {#if workflow}
      <label class="lbl">CLI</label>
      <select class="field" bind:value={cli} on:change={runCliChanged} style="max-width:240px">
        {#each agent.supports || [] as s}<option value={s}>{s}</option>{/each}
      </select>
      {#if runLocalOpt?.configured && runLocalRoutable}
        <label class="row" style="margin-top:10px; gap:8px; cursor:pointer">
          <input type="checkbox" bind:checked={runUseLocal} />
          <span>Use a configured local LLM</span>
        </label>
      {:else if runLocalOpt?.configured}
        <div class="card-sub" style="margin-top:10px">{localRoutingUnavailableMessage(cli)}</div>
      {/if}
      {#if preflight?.issues?.length}
        {#each preflight.issues as issue}
          <div class="banner" class:ok={issue.severity !== 'error'}>{issue.severity === 'error' ? 'Blocked' : 'Warning'}: {issue.message}</div>
        {/each}
      {/if}
      {#if runUseLocal && runLocalOpt?.configured && runLocalRoutable}
        <label class="lbl">Local model</label>
        <input class="field mono" style="max-width:420px" list="run-local-models" bind:value={runLocalModel} placeholder="model on your endpoint" />
        <datalist id="run-local-models">{#each runLocalOpt.models || [] as m}<option value={m}></option>{/each}</datalist>
        {#if runLocalOpt.error}<div class="card-sub" style="color: var(--warn)">Couldn't list models: {runLocalOpt.error}. You can still type a model name.</div>{/if}
      {:else}
        <label class="lbl">Model (blank = CLI default)</label>
        <input class="field mono" style="max-width:420px" list="run-model-suggestions" bind:value={runModel} />
        <datalist id="run-model-suggestions">{#each runSuggestions as m}<option value={m}></option>{/each}</datalist>
        {#if runModelLoading}<div class="card-sub">Loading models...</div>{/if}
      {/if}
      <label class="lbl">Working folder *</label>
      <div class="row">
        <input class="field grow" bind:value={cwd} placeholder="pick the folder the workflow can change" />
        <button class="btn" on:click={chooseFolder}>Browse…</button>
      </div>
      {#if runMode === 'single'}
        {#each workflow.inputs || [] as inp}
          <label class="lbl">{inp.prompt || inp.name}{inp.required ? ' *' : ''}</label>
          <input class="field" bind:value={inputs[inp.name]} placeholder={inp.placeholder || ''} />
        {/each}
      {:else}
        {#each agent.workflows || [] as w}
          <div style="margin-top:12px">
            <div class="card-title" style="font-size:13px">{w.name}</div>
            {#if (w.inputs || []).length === 0}
              <div class="card-sub">No inputs.</div>
            {/if}
            {#each w.inputs || [] as inp}
              <label class="lbl">{inp.prompt || inp.name}{inp.required ? ' *' : ''}</label>
              <input
                class="field"
                value={inputsByWorkflow[w.name]?.[inp.name] || ''}
                placeholder={inp.placeholder || ''}
                on:input={(e) => {
                  inputsByWorkflow[w.name] = { ...(inputsByWorkflow[w.name] || {}), [inp.name]: e.currentTarget.value }
                  inputsByWorkflow = inputsByWorkflow
                }} />
            {/each}
          </div>
        {/each}
      {/if}
      {#if privacyCounts === null}
        <div style="margin-top:18px"><button class="btn primary" on:click={review}>Continue</button></div>
      {:else}
        <div class="card" style="margin-top:18px">
          {#if matchTotal === 0}
            <div class="card-title">Privacy scan: clean</div>
          {:else}
            <div class="card-title" style="color:var(--warn)">Privacy scan: {matchTotal} match(es)</div>
            <div class="card-sub">
              Sent REDACTED to the CLI:
              {#each Object.entries(privacyCounts) as [cat, n]}<span class="pill warn">{cat} ×{n}</span>{/each}
            </div>
          {/if}
          <div class="row" style="margin-top:10px">
            <button class="btn primary" on:click={startRun}>{runMode === 'all' ? 'Run all workflows' : 'Run workflow'}</button>
            <button class="btn" on:click={() => (privacyCounts = null)}>Back</button>
          </div>
        </div>
      {/if}
    {/if}
  {/if}
{/if}

<style>
  .tool-feed {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin: 6px 0 10px;
    padding: 6px 8px;
    border-left: 2px solid var(--border);
    font-size: 12px;
    color: var(--text-dim);
  }
  .tool-row { display: flex; gap: 6px; align-items: baseline; min-width: 0; }
  .tool-row.err .tool-status { color: var(--danger, #e5484d); }
  .reasoning-row .tool-status { color: var(--accent, #7c6cf2); }
  .tool-status { width: 1em; flex: none; }
  .tool-name { font-weight: 600; flex: none; }
  .tool-detail {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 11px;
  }
  .reasoning-detail { white-space: pre-wrap; text-overflow: clip; }
  .msg.pending { opacity: 0.7; }
  .workflow-composer {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-top: 12px;
  }
  .workflow-composer textarea {
    width: 100%;
    resize: vertical;
  }
  .cursor { animation: blink 1s steps(1) infinite; }
  @keyframes blink { 50% { opacity: 0; } }
</style>
