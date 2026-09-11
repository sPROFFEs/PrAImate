<script>
  // Agents — see / edit / add / delete agents, and launch them on any
  // allowed surface: Chat (interpreter), Terminal (live CLI), or
  // Editor (document studio). Editing happens on the YAML wire format
  // in the embedded CodeMirror editor; saving re-validates through the
  // same parser `praimate agent import` uses.
  import { onDestroy, onMount, tick } from 'svelte'
  import { api, onRequirementsProgress } from '../lib/api.js'
  import { activePage, pageRevision, openChatId, pendingTerm, agentStudio, showToast } from '../lib/stores.js'
  import CodeEditor from '../lib/CodeEditor.svelte'
  import WorkflowRunner from '../lib/WorkflowRunner.svelte'
  import SkillChoiceDraft from '../lib/SkillChoiceDraft.svelte'
  import { LOCAL_ROUTABLE_CLIS, localRoutingUnavailableMessage, supportsLocalRouting } from '../lib/localRouting.js'

  let agents = []
  let runtimeModes = {}
  let error = ''
  let notice = ''
  let requirementsRunning = ''
  let requirementsResult = null
  let requirementsProgress = null
  let requirementsNow = Date.now()
  let requirementsLogEl = null
  let requirementsTimer = null
  let unsubscribeRequirements = () => {}
  let agentImportReview = null
  let agentImportApproved = false
  let importingAgent = false

  function duration(ms) {
    const total = Math.max(0, Math.floor(ms / 1000))
    const minutes = Math.floor(total / 60)
    const seconds = total % 60
    return minutes ? `${minutes}m ${String(seconds).padStart(2, '0')}s` : `${seconds}s`
  }

  function requirementsActivity(p) {
    if (p?.phase === 'canceling') return 'Stopping process…'
    const idle = requirementsNow - (p?.lastOutputAt || p?.startedAt || requirementsNow)
    if (idle >= 60000) return `No output for ${duration(idle)} — the script may be waiting or stalled`
    if (idle >= 15000) return `Waiting for output (${duration(idle)})`
    return 'Receiving output'
  }

  async function updateRequirementsProgress(ev) {
    if (!ev?.agentID) return
    const previous = requirementsProgress?.agentID === ev.agentID
      ? requirementsProgress
      : { agentID: ev.agentID, output: '', phase: 'running', startedAt: ev.at, lastOutputAt: ev.at }
    requirementsProgress = {
      ...previous,
      phase: ev.state === 'output' ? previous.phase : ev.state,
      startedAt: ev.state === 'started' ? ev.at : previous.startedAt,
      lastOutputAt: ev.state === 'output' ? ev.at : previous.lastOutputAt,
      output: ev.text ? (previous.output + ev.text).slice(-65536) : previous.output
    }
    requirementsNow = Date.now()
    await tick()
    if (requirementsLogEl) requirementsLogEl.scrollTop = requirementsLogEl.scrollHeight
  }

  // view: 'list' | 'edit' | 'run'
  let view = 'list'

  function allows(a, surface) {
    if (surface === 'terminal' && runtimeModes[a.id]?.mode === 'agentic') return false
    return !a.surfaces?.length || a.surfaces.includes(surface)
  }

  async function load() {
    try {
      const loadedAgents = (await api.listAgents()) || []
      const configs = await Promise.all(loadedAgents.map(async (agent) => {
        try { return [agent.id, await api.agentRuntimeConfig(agent.id)] }
        catch { return [agent.id, { mode: 'invalid', agenticCompatible: false }] }
      }))
      runtimeModes = Object.fromEntries(configs)
      agents = loadedAgents
      error = ''
    } catch (e) {
      error = String(e)
    }
  }

  // --- launch dialog (surface + CLI + model + folder) ------------------------

  // dlg: {agent|null, surface: 'chat'|'terminal'|'studio', cli, model,
  //       cliOptions: [{id,label,available}], suggestions, folder, busy}
  let dlg = null
  let allClis = []
  let localOpt = null // { configured, endpoint, hasApiKey, models[], error }
  function isLocalRoutable(cli) {
    return supportsLocalRouting(cli)
  }
  $: dlgLocalRoutable = !!dlg && isLocalRoutable(dlg.cli)

  function invalidateDlgPreflight() {
    if (!dlg) return
    error = ''
    dlg.preflight = null
    dlg.preflightChecked = false
    dlg = dlg
  }

  function cliOptionsFor(agent) {
    if (!agent) return allClis
    return (agent.supports || []).map((s) => {
      const info = allClis.find((c) => c.id === s)
      return { id: s, label: info?.label || s, available: info ? info.available : true }
    })
  }

  function openLaunch(agent, surface) {
    error = ''
    // Open INSTANTLY; availability + model suggestions stream in (the
    // CLI probe takes seconds and a frozen button reads as broken).
    const cliOptions = agent
      ? (agent.supports || []).map((s) => ({ id: s, label: s, available: true }))
      : allClis.length
        ? allClis
        : [{ id: 'claude', label: 'claude', available: true }]
    dlg = {
      agent,
      surface,
      cli: cliOptions[0]?.id || 'claude',
      model: '',
      name: '',
      cliOptions,
      suggestions: [],
      modelLoading: false,
      folder: '',
      busy: false,
      useLocal: false,
      localModel: '',
      capabilities: null,
      preflight: null,
      preflightChecked: false,
      skillChoices: null,
    }
    if (localOpt === null) {
      api.localLLMModels().then((r) => { localOpt = r }).catch(() => { localOpt = { configured: false } })
    }
    const fill = () => {
      if (!dlg) return
      dlg.cliOptions = cliOptionsFor(agent)
      const cur = dlg.cliOptions.find((c) => c.id === dlg.cli)
      if (!cur || !cur.available) {
        const first = dlg.cliOptions.find((c) => c.available) || dlg.cliOptions[0]
        if (first) dlg.cli = first.id
      }
      dlg = dlg
      dlgCliChanged()
    }
    if (allClis.length === 0) {
      api.listCLIs().then((r) => { allClis = r || []; fill() }).catch(() => {})
    } else {
      fill()
    }
  }

  async function dlgCliChanged() {
    if (!dlg) return
    // Drop the local toggle if the new CLI can't route on this surface.
    if (dlg.useLocal && !isLocalRoutable(dlg.cli)) { dlg.useLocal = false; dlg = dlg }
    dlg.modelLoading = true
    dlg.preflight = null
    dlg.preflightChecked = false
    dlg = dlg
    const selectedCLI = dlg.cli
    api.executionCapabilities(selectedCLI).then((r) => {
      if (dlg?.cli === selectedCLI) { dlg.capabilities = r; dlg = dlg }
    }).catch(() => {})
    dlg.suggestions = (await api.listCLIModels(dlg.cli).catch(() => [])) || []
    dlg.modelLoading = false
    dlg = dlg
  }

  async function dlgPickFolder() {
    try {
      const p = await api.pickFolder()
      if (p && dlg) { dlg.folder = p; error = ''; invalidateDlgPreflight() }
    } catch (e) {
      error = String(e)
    }
  }

  let preflightWarnings = null

  function continueLaunch() {
    if (!dlg) return
    // dlgGo() owns the busy transition; leaving this true trips its
    // re-entry guard and strands the launch after a warning.
    dlg.busy = false
    preflightWarnings = null
    dlgGo()
  }

  async function dlgGo() {
    if (!dlg || dlg.busy) return
    dlg.busy = true
    error = ''
    const { agent, surface, cli, folder } = dlg
    const sessionName = dlg.name.trim()
    const model = dlg.model.trim()
    const local = dlg.useLocal && localOpt?.configured
    const lEnd = local ? localOpt.endpoint : ''
    const lKey = ''
    const lModel = local ? dlg.localModel.trim() : ''
    if (local && !LOCAL_ROUTABLE_CLIS.includes(cli)) {
      error = `${cli} local-LLM routing is not supported on this surface.`
      dlg.busy = false
      return
    }
    try {
      if (!folder) {
        error = 'Pick a project folder first.'
        dlg.busy = false
        return
      }
      if (!dlg.preflightChecked) {
        const tools = surface === 'studio' ? 'edits' : ''
        const check = await api.preflightExecution(agent?.id || '', surface, cli, local ? '' : model, tools, folder, lEnd, lModel)
        dlg.preflight = check
        dlg.preflightChecked = true
        dlg.busy = false
        dlg = dlg
        if (!check?.ok) {
          error = (check?.issues || []).filter((i) => i.severity === 'error').map((i) => i.message).join('\n') || 'Execution preflight failed.'
          return
        }
        const warnings = (check?.issues || []).filter((i) => i.severity !== 'error')
        if (warnings.length > 0) {
          preflightWarnings = warnings
          return
        }
        dlg.busy = true
      }
      const agentLabel = agent?.name || cli
      const surfaceLabel = surface === 'chat' ? 'chat' : surface === 'terminal' ? 'terminal' : 'Studio'
      showToast({
        title: `Opening ${surfaceLabel}`,
        message: `Starting ${agentLabel} with ${cli} in ${folder}`,
        tone: 'busy', duration: 0, dismissible: false,
      })
      if (surface === 'chat') {
        const c = await api.startChat(agent.id, cli, folder)
        if (dlg.skillChoices !== null) await api.saveChatSkillChoicesV2(c.ID, dlg.skillChoices)
        if (sessionName) await api.renameChat(c.ID, sessionName)
        if (local) await api.updateChatConfig(c.ID, cli, '', '', lEnd, lKey, lModel)
        else if (model) await api.updateChatConfig(c.ID, cli, model, '', '', '', '')
        dlg = null
        showToast({ title: 'Chat ready', message: `${agentLabel} is connected through ${cli}.`, tone: 'ok' })
        openChatId.set(c.ID)
        activePage.set('chats')
        return
      }
      if (surface === 'terminal') {
        const created = await api.startCodeSessionWithSkills(
          agent ? agent.id : '', cli, local ? '' : model, folder, lEnd, lModel, dlg.skillChoices)
        const termId = created.termId
        const chatId = created.chatId
        if (sessionName && chatId) await api.renameChat(chatId, sessionName)
        dlg = null
        showToast({ title: 'Terminal ready', message: `${agentLabel} is running through ${cli}.`, tone: 'ok' })
        pendingTerm.set({ termId, chatId, cli, cwd: folder, label: sessionName || ((agent ? agent.name : cli) + (local ? ' · local' : '')), note: '' })
        activePage.set('code')
        pageRevision.update((n) => n + 1)
        return
      }
      // studio
      const createdChatId = await api.prepareStudioChat(folder, agent ? agent.id : '', cli, local ? '' : model, lEnd, lModel, dlg.skillChoices)
      await api.openEditorWindow(folder, agent ? agent.id : '', cli, local ? '' : model, createdChatId, lEnd, lKey, lModel)
      if (sessionName && createdChatId) await api.renameChat(createdChatId, sessionName)
      dlg = null
      notice = 'Studio window opened.'
      showToast({ title: 'Studio opened', message: `${agentLabel} is ready in a separate Studio window.`, tone: 'ok' })
    } catch (e) {
      error = String(e)
      showToast({ title: 'Launch failed', message: String(e), tone: 'err', duration: 0 })
      if (dlg) dlg.busy = false
    }
  }

  // --- YAML editor -----------------------------------------------------------

  let yamlText = ''
  let editing = null // agent being edited, or {id:''} for new
  let editorRef
  let saving = false

  // --- knowledge panel (inside the edit view) --------------------------------

  let know = null // AgentKnowledgeInfo for the agent being edited
  let knowBusy = false
  let ragBackend = 'claude-cli' // graphify backend for RAG indexing
  let ragKey = ''
  let ragModel = ''
  let ragLog = []
  let unsubRag = () => {}

  async function loadKnowledge(id) {
    know = null
    if (!id) return
    try {
      know = await api.getAgentKnowledge(id)
    } catch (e) {
      error = String(e)
    }
  }

  async function setKnowMode(mode) {
    if (!editing?.id) return
    try {
      await api.setAgentKnowledgeMode(editing.id, mode)
      // The YAML changed (knowledge: field) — refresh the editor too.
      yamlText = await api.agentYAML(editing.id)
      editorRef?.setExternal(yamlText)
      await loadKnowledge(editing.id)
      await load()
    } catch (e) {
      error = String(e)
    }
  }

  async function addKnowFiles(folder) {
    if (!editing?.id) return
    try {
      const files = folder
        ? await api.pickAgentKnowledgeFolder(editing.id)
        : await api.pickAgentKnowledgeFiles(editing.id)
      if (know) { know.files = files || []; know = know }
    } catch (e) {
      error = String(e)
    }
  }

  async function rmKnowFile(rel) {
    try {
      const files = await api.deleteAgentKnowledgeFile(editing.id, rel)
      if (know) { know.files = files || []; know = know }
    } catch (e) {
      error = String(e)
    }
  }

  async function installGraphify() {
    if (knowBusy) return
    knowBusy = true
    error = ''
    try {
      await api.installBundledGraphify()
      notice = 'Bundled graphify installed.'
      await loadKnowledge(editing.id)
    } catch (e) {
      error = String(e)
    } finally {
      knowBusy = false
    }
  }

  async function buildRAG() {
    if (!editing?.id || knowBusy) return
    knowBusy = true
    ragLog = []
    error = ''
    try {
      await api.buildAgentRAG(editing.id, ragBackend, ragKey, ragModel)
      notice = 'RAG index built.'
      await loadKnowledge(editing.id)
    } catch (e) {
      error = String(e)
    } finally {
      knowBusy = false
    }
  }

  async function edit(a) {
    error = ''
    try {
      yamlText = await api.agentYAML(a.id)
      editing = a
      view = 'edit'
      loadKnowledge(a.id)
    } catch (e) {
      error = String(e)
    }
  }

  async function createNew() {
    error = ''
    try {
      yamlText = await api.newAgentTemplateYAML()
      editing = { id: '', name: 'new agent' }
      know = null
      view = 'edit'
    } catch (e) {
      error = String(e)
    }
  }

  async function save() {
    saving = true
    error = ''
    try {
      const body = editorRef?.getValue() ?? yamlText
      const saved = await api.saveAgentYAML(body)
      notice = `Saved ${saved.name}`
      // Stay in the editor (now bound to the saved id) so the knowledge
      // step is available right after creating an agent.
      editing = saved
      await loadKnowledge(saved.id)
      await load()
    } catch (e) {
      error = String(e)
    } finally {
      saving = false
    }
  }

  async function importYAML() {
	if (importingAgent) return
	importingAgent = true
	error = ''
    try {
      const review = await api.reviewAgentImportDialog()
      if (!review) return
      if (!review.review_digest) {
        notice = `Imported ${review.agent.name}`
        await load()
        return
      }
      agentImportReview = review
      agentImportApproved = false
    } catch (e) { error = String(e) }
	finally { importingAgent = false }
  }

  async function approveAgentImport() {
    if (!agentImportReview || !agentImportApproved || importingAgent) return
    importingAgent = true
    error = ''
    try {
      const agent = await api.importReviewedAgentPack(agentImportReview.path, agentImportReview.review_digest)
      const count = (agentImportReview.skills || []).length
      notice = `Imported ${agent.name} with ${count} reviewed skill bundle${count === 1 ? '' : 's'}.`
      agentImportReview = null
      agentImportApproved = false
      await load()
    } catch (e) { error = String(e) }
    finally { importingAgent = false }
  }

  async function exportYAML(a) {
    try {
      // Pack-aware: .praimate-agent (yaml + knowledge) by default,
      // bare YAML when the user picks that extension in the dialog.
      const path = await api.exportAgentPackDialog(a.id)
      if (path) notice = `Exported to ${path}`
    } catch (e) { error = String(e) }
  }

  async function remove(a) {
    if (!confirm(`Delete agent "${a.name}"?`)) return
    try {
      await api.deleteAgent(a.id)
      await load()
    } catch (e) { error = String(e) }
  }

  async function runRequirements(a) {
    if (requirementsRunning || !confirm(`Run ${a.requirements.script} for "${a.name}"? This script can install software and change this computer.`)) return
    requirementsRunning = a.id
    requirementsResult = null
    const now = Date.now()
    requirementsProgress = { agentID: a.id, phase: 'starting', output: '', startedAt: now, lastOutputAt: now }
    error = ''
    try { requirementsResult = { agentID: a.id, ...(await api.runAgentRequirements(a.id)) } }
    catch (e) { error = String(e) }
    finally { requirementsRunning = '' }
  }

  async function cancelRequirements(a) {
    if (requirementsRunning !== a.id) return
    requirementsProgress = { ...requirementsProgress, phase: 'canceling' }
    try { await api.cancelAgentRequirements(a.id) }
    catch (e) { error = String(e) }
  }

  // --- workflow run (ported from the old Run page) ---------------------------

  let runAgent = null

  function openRun(a) {
    runAgent = a
    view = 'run'
  }

  function backToList() {
    view = 'list'
    runAgent = null
    editing = null
    know = null
  }

  onMount(() => {
    load()
    unsubscribeRequirements = onRequirementsProgress(updateRequirementsProgress)
    requirementsTimer = setInterval(() => (requirementsNow = Date.now()), 1000)
  })

  onDestroy(() => {
    unsubscribeRequirements()
    if (requirementsTimer) clearInterval(requirementsTimer)
  })
</script>

{#if view === 'edit'}
  <div class="row" style="margin-bottom: 12px">
    <button class="btn" on:click={backToList}>← Agents</button>
    <strong class="grow">{editing?.id ? `Edit ${editing.name}` : 'New agent'}</strong>
    <button class="btn primary" on:click={save} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
  </div>
  {#if error}<div class="banner">{error}</div>{/if}
  {#if notice}<div class="card card-sub">{notice}</div>{/if}

  <div class="edit-stack">
  <div class="agent-editor">
    <CodeEditor bind:this={editorRef} value={yamlText} lang="yaml" />
  </div>

  <div class="card">
    <div class="card-title">Knowledge base</div>
    {#if !editing?.id}
      <div class="card-sub">Save the agent first — then you can attach documents here.</div>
    {:else if !know}
      <div class="card-sub">Loading…</div>
    {:else}
      <div class="card-sub">
        Documents live in <span class="mono">{know.dir}</span> and travel inside the agent's
        <span class="mono">.praimate-agent</span> pack on export. The folder is the same for both
        formats — you can switch later without breaking the agent.
      </div>
      <label class="lbl">Format</label>
      <div class="row">
        <button class="btn sm" class:primary={know.mode === ''} on:click={() => setKnowMode('')}>None</button>
        <button class="btn sm" class:primary={know.mode === 'raw'} on:click={() => setKnowMode('raw')}
          title="The agent reads the documents directly with its file tools — best under a few MB.">Raw documents</button>
        <button class="btn sm" class:primary={know.mode === 'rag'} on:click={() => setKnowMode('rag')}
          title="A graphify knowledge-graph index over the same folder; the agent queries it for retrieval.">RAG (graphify)</button>
      </div>
      {#if know.mode === 'rag' && !know.graphifyInstalled}
        <div class="banner" style="margin-top:8px">
          RAG mode needs <strong>graphify</strong>.
          <button class="btn sm primary" style="margin-left:8px" on:click={installGraphify} disabled={knowBusy}>
            {knowBusy ? 'Installing…' : 'Install bundled graphify'}
          </button>
          <span class="card-sub"> — PrAImate's self-contained build, no Python needed. Until then the agent just reads the files directly.</span>
        </div>
      {/if}
      {#if know.mode === 'rag' && know.graphifyInstalled}
        <label class="lbl" style="margin-top:8px">Indexing backend</label>
        <div class="row">
          <select class="field" style="max-width:320px" bind:value={ragBackend}>
            <option value="claude-cli">Claude CLI (uses your install · no key)</option>
            <option value="code">Code only (no key · skips documents)</option>
            {#if know.localEndpoint}
              <option value="local">Local LLM — OpenAI compatible</option>
              <option value="local-ollama">Ollama — optimized local backend</option>
            {/if}
            <option value="claude">Anthropic API</option>
            <option value="openai">OpenAI</option>
            <option value="kimi">Kimi (Moonshot)</option>
          </select>
          {#if !['code', 'claude-cli', 'local', 'local-ollama'].includes(ragBackend)}
            <input class="field grow mono" type="password" placeholder="API key for the backend" bind:value={ragKey} />
          {/if}
        </div>
        {#if ragBackend === 'local' || ragBackend === 'local-ollama'}
          <div class="row" style="margin-top:6px">
            <input
              class="field grow mono"
              placeholder={ragBackend === 'local-ollama'
                ? 'Ollama model, e.g. qwen2.5-coder:7b'
                : 'model name at the endpoint'}
              bind:value={ragModel}
            />
          </div>
        {:else if ragBackend !== 'code' && ragBackend !== 'claude-cli'}
          <div class="row" style="margin-top:6px">
            <input class="field grow mono" placeholder="model (optional — blank uses the backend default)" bind:value={ragModel} />
          </div>
        {/if}
        <div class="card-sub" style="margin-top:4px">
          {#if ragBackend === 'claude-cli'}
            Uses your installed, signed-in Claude CLI to summarize documents — no API key, no extra cost. Recommended.
          {:else if ragBackend === 'code'}
            Builds a code knowledge-graph (functions, calls, imports) only. Documents/PDFs are skipped — pick an LLM backend to index those.
          {:else if ragBackend === 'local'}
            Routes through the saved OpenAI-compatible endpoint (<span class="mono">{know.localEndpoint}</span>) — no cloud key.
          {:else if ragBackend === 'local-ollama'}
            Uses graphify's Ollama backend with single-request concurrency and Ollama-specific context handling
            (<span class="mono">{know.localEndpoint}</span>).
          {:else}
            Documents are summarized by the chosen LLM (uses your key, costs tokens). Code is still AST-extracted for free.
          {/if}
        </div>
      {/if}
      {#if know.mode !== ''}
        <label class="lbl">Documents ({(know.files || []).length})</label>
        {#each know.files || [] as f}
          <div class="row" style="padding:2px 0">
            <span class="grow mono" style="font-size:12px">{f}</span>
            <button class="chip-x" title="Remove" on:click={() => rmKnowFile(f)}>×</button>
          </div>
        {/each}
        <div class="row" style="margin-top:8px">
          <button class="btn" on:click={() => addKnowFiles(false)}>Add files…</button>
          <button class="btn" on:click={() => addKnowFiles(true)}>Add folder…</button>
          {#if know.mode === 'rag'}
            <button class="btn primary" on:click={buildRAG}
              disabled={knowBusy || !know.graphifyInstalled || ((know.files || []).length === 0)}>
              {knowBusy ? 'Indexing…' : know.hasIndex ? 'Rebuild RAG index' : 'Build RAG index'}
            </button>
            {#if know.hasIndex}<span class="pill ok">index ready</span>{/if}
          {/if}
        </div>
      {/if}
    {/if}
  </div>
  </div>
{:else if view === 'run' && runAgent}
  <WorkflowRunner agent={runAgent} localOpt={localOpt} on:close={backToList} />
{:else}
  <div class="row" style="margin-bottom: 4px">
    <h1 class="grow" style="margin:0">Agents</h1>
    <button class="btn" on:click={() => openLaunch(null, 'studio')} title="Open the document studio without an agent persona">Open studio…</button>
    <button class="btn" disabled={importingAgent} on:click={importYAML}>{importingAgent ? 'Importing…' : 'Import…'}</button>
    <button class="btn primary" on:click={() => agentStudio.set({ id: '' })}>+ New agent</button>
  </div>
  <p class="subtitle">Portable YAML agents. Launch them in a Chat, a live Terminal, or the document Studio — each agent declares which surfaces it allows.</p>

  {#if error}<div class="banner">{error}</div>{/if}
  {#if notice}<div class="card card-sub">{notice}</div>{/if}

  {#if agentImportReview}
    <div class="modal-backdrop" style="z-index:12000" on:click|self={() => !importingAgent && (agentImportReview = null)}>
      <div class="modal-content" role="dialog" aria-modal="true" aria-labelledby="agent-import-title" style="max-width:680px">
        <h2 id="agent-import-title">Review agent package</h2>
        <p><strong>{agentImportReview.agent.name}</strong> · <span class="mono">{agentImportReview.agent.id}</span></p>
        <p class="card-sub">Import installs the agent, knowledge and requirements, then approves only the immutable skill versions listed here. It does not execute scripts or grant tools.</p>
        <div style="max-height:46vh;overflow:auto">
          {#if !(agentImportReview.skills || []).length}<p class="card-sub">No skills are bundled.</p>{/if}
          {#each agentImportReview.skills || [] as skill}
            <div class="card">
              <strong>{skill.ref}</strong><br><code>{skill.digest}</code>
              {#each skill.files || [] as file}
                <details>
                  <summary>{file.path} · {file.bytes} bytes{file.executable ? ' · executable intent (not permission)' : ''}</summary>
                  <code>{file.sha256}</code>
                  {#if file.binary}<p class="card-sub">Binary resource preserved by digest; it is never executed by import.</p>{:else}<pre style="white-space:pre-wrap;overflow-wrap:anywhere;max-height:240px;overflow:auto">{file.text}</pre>{/if}
                </details>
              {/each}
            </div>
          {/each}
        </div>
        <label class="row" style="align-items:flex-start;margin:14px 0">
          <input type="checkbox" bind:checked={agentImportApproved} />
          <span>I reviewed this exact package and approve its listed skill digests for automatic use by this agent.</span>
        </label>
        <div class="row" style="justify-content:flex-end">
          <button class="btn" disabled={importingAgent} on:click={() => (agentImportReview = null)}>Cancel</button>
          <button class="btn primary" disabled={importingAgent || !agentImportApproved} on:click={approveAgentImport}>Import agent and skills</button>
        </div>
      </div>
    </div>
  {/if}

  {#if dlg}
    <!-- svelte-ignore a11y-click-events-have-key-events -->
    <!-- svelte-ignore a11y-no-static-element-interactions -->
    <div class="modal-backdrop" on:click|self={() => !dlg.busy && (dlg = null)}>
    <div class="modal-content launch-modal" role="dialog" aria-modal="true" aria-labelledby="agent-launch-title">
      <div class="card-title">
        <span id="agent-launch-title">
        {dlg.surface === 'chat' ? 'New chat' : dlg.surface === 'terminal' ? 'Open terminal' : 'Open studio'}
        {dlg.agent ? ` — ${dlg.agent.name}` : ''}
        </span>
      </div>
      <label class="lbl">Session Name (optional)</label>
      <input class="field" style="max-width:320px; margin-bottom:10px" bind:value={dlg.name} placeholder="e.g. Refactor API" />

      <label class="lbl">CLI</label>
      <select class="field" style="max-width:320px" bind:value={dlg.cli} on:change={dlgCliChanged}>
        {#each dlg.cliOptions as c}
          <option value={c.id} disabled={!c.available}>{c.label}{c.available ? '' : ' — not installed'}</option>
        {/each}
      </select>
      {#if localOpt?.configured && dlgLocalRoutable}
        <label class="row" style="margin-top:10px; gap:8px; cursor:pointer">
          <input type="checkbox" bind:checked={dlg.useLocal} on:change={invalidateDlgPreflight} />
          <span>Use the local LLM from Settings <span class="card-sub mono">{localOpt.endpoint}</span></span>
        </label>
      {:else if localOpt?.configured}
        <div class="card-sub" style="margin-top:10px">{localRoutingUnavailableMessage(dlg.cli)}</div>
      {/if}

      {#if dlg.useLocal && localOpt?.configured && dlgLocalRoutable}
        <label class="lbl">Local model</label>
        <input class="field mono" style="max-width:420px" list="launch-local-models" bind:value={dlg.localModel} on:input={invalidateDlgPreflight} placeholder="model on your endpoint" />
        <datalist id="launch-local-models">{#each localOpt.models || [] as m}<option value={m}></option>{/each}</datalist>
        {#if localOpt.error}<div class="card-sub" style="color: var(--warn)">Couldn't list models: {localOpt.error}. You can still type a model name.</div>{/if}
      {:else}
        <label class="lbl">Model (blank = CLI default)</label>
        <input class="field mono" style="max-width:420px" list="launch-model-suggestions" bind:value={dlg.model} on:input={invalidateDlgPreflight} />
        <datalist id="launch-model-suggestions">
          {#each dlg.suggestions as m}<option value={m}></option>{/each}
        </datalist>
        {#if dlg.modelLoading}<div class="card-sub">Loading models...</div>{/if}
      {/if}
      {#if dlg.capabilities}
        <div class="card-sub" style="margin-top:10px">
          Capabilities: {dlg.capabilities.streaming ? 'streaming' : 'buffered'} · {dlg.capabilities.resume ? 'resume' : 'no resume'} · {dlg.capabilities.mcp ? 'MCP' : 'no MCP'} · permissions {(dlg.capabilities.toolLevels || []).map((x) => x || 'safe').join(', ')}
        </div>
      {/if}
      {#if true}
        <label class="lbl">Project folder *</label>
        <div class="row">
          <input class="field grow mono" bind:value={dlg.folder} on:input={invalidateDlgPreflight} placeholder="pick the folder the agent works in" />
          <button class="btn" on:click={dlgPickFolder}>Browse…</button>
        </div>
      {/if}
      <SkillChoiceDraft bind:choices={dlg.skillChoices} />
      {#if error}<div class="banner error-banner" role="alert" style="margin-top:12px">{error}</div>{/if}
      <div class="row" style="margin-top:12px">
        <button class="btn primary" on:click={dlgGo} disabled={dlg.busy}>{dlg.busy ? 'Starting…' : 'Launch'}</button>
        <button class="btn" on:click={() => { dlg = null; error = '' }} disabled={dlg.busy}>Cancel</button>
      </div>
    </div>
    </div>
  {/if}

{#if preflightWarnings}
  <div class="modal-backdrop" style="z-index: 12000" on:click|self={() => (preflightWarnings = null)}>
    <div class="modal-content warning-modal" role="dialog" aria-modal="true" style="border: 1px solid #d39e00; max-width: 500px">
      <h2 style="color: #d39e00; display:flex; align-items:center; gap:8px">
        <span style="font-size:20px">⚠️</span> Preflight Warnings
      </h2>
      <div style="margin: 16px 0; max-height: 50vh; overflow-y: auto">
        {#each preflightWarnings as issue}
          <div style="background: rgba(211, 158, 0, 0.1); color: var(--text); padding: 10px 14px; border-radius: 6px; margin-bottom: 8px; font-size: 13px;">
            {issue.message}
          </div>
        {/each}
      </div>
      <div class="row actions" style="justify-content: flex-end; margin-top:20px">
        <button class="btn" on:click={() => (preflightWarnings = null)}>Cancel</button>
        <button class="btn" style="background: #d39e00; color: #fff; border-color: #d39e00" on:click={continueLaunch}>Continue</button>
      </div>
    </div>
  </div>
{/if}

  {#each agents as a}
    <div class="card">
      <div class="row">
        <div class="grow">
          <div class="card-title">{a.name} <span class="card-sub mono">({a.id})</span> {#if runtimeModes[a.id]?.mode === 'agentic'}<span class="pill warn">Managed Autonomous</span>{:else if runtimeModes[a.id]?.mode === 'invalid'}<span class="pill danger">Invalid runtime</span>{:else}<span class="pill">Native CLI</span>{/if}</div>
          <div class="card-sub">{a.description?.split('\n')[0]}</div>
        </div>
        {#if allows(a, 'chat')}<button class="btn primary" on:click={() => openLaunch(a, 'chat')}>Chat</button>{/if}
        {#if allows(a, 'terminal')}<button class="btn" on:click={() => openLaunch(a, 'terminal')}>Terminal</button>{/if}
        {#if allows(a, 'editor')}<button class="btn" on:click={() => openLaunch(a, 'studio')}>Studio</button>{/if}
      </div>
      <div class="row" style="margin-top: 8px">
        <div class="grow">
          {#each a.supports || [] as s}<span class="pill">{s}</span>{/each}
          {#each a.surfaces?.length ? a.surfaces : ['chat', 'terminal', 'editor'] as s}<span class="pill ok">{s}</span>{/each}
          {#each a.workflows || [] as w}<span class="pill warn">{w.name}</span>{/each}
        </div>
        {#if (a.workflows || []).length > 0}
          <button class="btn" on:click={() => openRun(a)}>Run workflow</button>
        {/if}
        {#if a.requirements}
          <button class="btn" on:click={() => runRequirements(a)} disabled={requirementsRunning === a.id}>{requirementsRunning === a.id ? 'Running requirements…' : 'Run requirements script'}</button>
        {/if}
        <button class="btn" on:click={() => agentStudio.set({ id: a.id })}>Edit</button>
        <button class="btn" on:click={() => exportYAML(a)}>Export</button>
        <button class="btn danger" on:click={() => remove(a)}>Delete</button>
      </div>
      {#if requirementsRunning === a.id && requirementsProgress?.agentID === a.id}
        <div class="requirements-progress">
          <div class="row">
            <strong>{requirementsProgress.phase === 'starting' ? 'Starting requirements…' : 'Requirements running'}</strong>
            <span class="pill warn">{duration(requirementsNow - requirementsProgress.startedAt)}</span>
            <span class="card-sub grow">{requirementsActivity(requirementsProgress)}</span>
            <button class="btn danger sm" on:click={() => cancelRequirements(a)} disabled={requirementsProgress.phase === 'canceling'}>
              {requirementsProgress.phase === 'canceling' ? 'Stopping…' : 'Stop'}
            </button>
          </div>
          <div class="card-sub">Remaining time is unavailable unless the script reports its own progress; live output below shows its current phase.</div>
          {#if requirementsProgress.output}<pre bind:this={requirementsLogEl}>{requirementsProgress.output}</pre>{/if}
        </div>
      {/if}
      {#if requirementsResult?.agentID === a.id}
        <div class="requirements-result" class:failed={!requirementsResult.success}>
          <strong>{requirementsResult.success ? 'Requirements script completed' : 'Requirements script failed'}</strong>
          {#if requirementsResult.error}<div class="mono">{requirementsResult.error}</div>{/if}
          {#if requirementsResult.output}<pre>{requirementsResult.output}</pre>{/if}
          {#if requirementsResult.instructions}<div class="card-sub"><strong>Additional instructions:</strong> {requirementsResult.instructions}</div>{/if}
        </div>
      {/if}
    </div>
  {/each}
{/if}

<style>
  .requirements-result { margin-top: 10px; border: 1px solid var(--ok); border-radius: var(--radius-sm); padding: 8px 10px; color: var(--text-dim); }
  .requirements-result.failed { border-color: var(--err); }
  .requirements-result pre { max-height: 180px; overflow: auto; white-space: pre-wrap; margin: 7px 0; font: 11px/1.4 var(--mono); }
  .requirements-progress { margin-top: 10px; border: 1px solid var(--warn); border-radius: var(--radius-sm); padding: 8px 10px; color: var(--text-dim); }
  .requirements-progress pre { max-height: 260px; overflow: auto; white-space: pre-wrap; margin: 7px 0 0; font: 11px/1.4 var(--mono); background: var(--bg); padding: 8px; border-radius: 6px; }
  .agent-editor { flex: 1; min-height: 240px; }
  .edit-stack { display: flex; flex-direction: column; gap: 12px; height: calc(100vh - 120px); }
  .edit-stack .card { flex: none; max-height: 42vh; overflow-y: auto; margin-top: 0; }
  .btn.sm { padding: 3px 10px; font-size: 12px; }
  .chip-x {
    background: none;
    border: none;
    color: var(--text-dim);
    cursor: pointer;
    font-size: 13px;
  }
  .chip-x:hover { color: var(--text); }
  .launch-modal { max-width: 620px; max-height: 88vh; overflow-y: auto; }
</style>
