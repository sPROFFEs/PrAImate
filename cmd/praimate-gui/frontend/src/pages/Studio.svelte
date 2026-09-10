<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import SkillBindingsEditor from '../lib/SkillBindingsEditor.svelte'
  import SkillChoiceDraft from '../lib/SkillChoiceDraft.svelte'
  import { activePage, openChatId, pageRevision, showToast } from '../lib/stores.js'
  import { localRoutingUnavailableMessage, supportsLocalRouting } from '../lib/localRouting.js'

  let chats = []
  let agents = []
  let clis = []
  let loading = true
  let error = ''
  let form = null
  let localOpt = null
  let preflightWarnings = null

  let cfg = null
  let cfgSaving = false
  let mcpServers = []

  function normalizeToolsForCli(cli, tools) {
    if (cli === 'claude' || cli === 'openclaude') return tools || ''
    if (cli === 'codex' || cli === 'gemini') return ['edits', 'full'].includes(tools) ? tools : ''
    if (cli === 'opencode' || cli === 'praimate-code') return ['plan', 'full'].includes(tools) ? tools : ''
    return ''
  }

  function toolLevelsForCli(c) {
    if (c === 'claude' || c === 'openclaude') return [{id:'',label:'Safe',hint:''}, {id:'ask',label:'Ask',hint:''}, {id:'edits',label:'Edits',hint:''}, {id:'full',label:'Full',hint:''}]
    if (c === 'opencode' || c === 'praimate-code') return [{id:'plan',label:'Plan',hint:''}, {id:'',label:'Build',hint:''}, {id:'full',label:'Full',hint:''}]
    return [{id:'',label:'Safe',hint:''}, {id:'ask',label:'Ask',hint:''}, {id:'edits',label:'Edits',hint:''}, {id:'full',label:'Full',hint:''}]
  }

  function openConfig(chat) {
    error = ''
    cfg = {
      chat, name: chat.Title || '', cli: chat.CLIAgent, model: chat.Settings?.model || '',
      tools: normalizeToolsForCli(chat.CLIAgent, chat.Settings?.tools),
      localEndpoint: chat.Settings?.local?.endpoint || '',
      localApiKey: chat.Settings?.local?.api_key || '',
      localModel: chat.Settings?.local?.model || '',
      suggestions: [], modelLoading: true,
      mcps: (chat.Settings?.mcp_servers || []).slice(),
    }
    if (clis.length === 0) api.listCLIs().then((r) => { clis = r || [] }).catch(() => {})
    if (localOpt === null) api.localLLMModels().then((r) => { localOpt = r }).catch(() => { localOpt = { configured: false } })
    api.listMCPServers().then((r) => { mcpServers = (r || []).filter((s) => s.enabled) }).catch(() => {})
    cfgCliChanged()
  }

  async function cfgCliChanged() {
    if (!cfg) return
    if (cfg.localEndpoint && !supportsLocalRouting(cfg.cli)) { cfg.localEndpoint = ''; cfg.localModel = '' }
    cfg.modelLoading = true
    cfg.suggestions = (await api.listCLIModels(cfg.cli).catch(() => [])) || []
    cfg.modelLoading = false
  }

  async function saveConfig() {
    if (!cfg) return
    cfgSaving = true; error = ''
    try {
      await api.updateChatConfig(cfg.chat.ID, cfg.cli, cfg.model.trim(), normalizeToolsForCli(cfg.cli, cfg.tools), cfg.localEndpoint.trim(), cfg.localApiKey, cfg.localModel.trim())
      if (cfg.name.trim() && cfg.name.trim() !== cfg.chat.Title) {
        await api.renameChat(cfg.chat.ID, cfg.name.trim())
      }
      await api.setChatMCPServers(cfg.chat.ID, cfg.mcps || [])
      cfg = null
      await load()
    } catch (e) { error = String(e) } finally { cfgSaving = false }
  }

  function continueLaunch() {
    if (!form) return
    // launch() owns the busy transition. Setting it here makes launch's
    // re-entry guard return immediately after a preflight warning.
    form.busy = false
    preflightWarnings = null
    launch()
  }

  $: sessions = chats.filter((chat) => chat.Settings?.surface === 'studio')
  $: editorAgents = agents.filter((agent) => !agent.surfaces?.length || agent.surfaces.includes('editor'))
  $: selectedAgent = form?.agentID ? agents.find((agent) => agent.id === form.agentID) : null
  $: compatibleCLIs = !form
    ? []
    : selectedAgent?.supports?.length
      ? clis.filter((cli) => selectedAgent.supports.includes(cli.id))
      : clis
  $: localRoutable = !!form && supportsLocalRouting(form.cli)

  function agentName(chat) {
    if (!chat.AgentID) return 'No agent persona'
    return agents.find((agent) => agent.id === chat.AgentID)?.name || chat.AgentID
  }

  function fmtDate(value) {
    try { return new Date(value).toLocaleString() } catch { return value }
  }

  async function load() {
    loading = true
    try {
      const [loadedChats, loadedAgents] = await Promise.all([
        api.listChats(),
        api.listAgents().catch(() => []),
      ])
      chats = loadedChats || []
      agents = loadedAgents || []
      error = ''
    } catch (e) {
      error = String(e)
    } finally {
      loading = false
    }
  }

  async function openNew() {
    error = ''
    try {
      if (!clis.length) clis = (await api.listCLIs()) || []
      if (localOpt === null) localOpt = await api.localLLMModels().catch(() => ({ configured: false }))
      const first = clis.find((cli) => cli.available) || clis[0]
      form = {
        agentID: '', cli: first?.id || '', model: '', folder: '', useLocal: false,
        name: '',
        localModel: '', suggestions: [], busy: false, preflight: null, preflightChecked: false, skillChoices: null,
      }
      await cliChanged()
    } catch (e) {
      error = String(e)
    }
  }

  function invalidatePreflight() {
    if (!form) return
    form = { ...form, preflight: null, preflightChecked: false }
  }

  async function agentChanged() {
    if (!form) return
    const nextAgent = agents.find((agent) => agent.id === form.agentID)
    const allowed = nextAgent?.supports?.length
      ? clis.filter((cli) => nextAgent.supports.includes(cli.id))
      : clis
    if (!allowed.some((cli) => cli.id === form.cli && cli.available)) {
      form.cli = allowed.find((cli) => cli.available)?.id || allowed[0]?.id || ''
    }
    invalidatePreflight()
    await cliChanged()
  }

  async function cliChanged() {
    if (!form) return
    if (form.useLocal && !supportsLocalRouting(form.cli)) form.useLocal = false
    form = { ...form, suggestions: [], modelLoading: true, preflight: null, preflightChecked: false }
    const cli = form.cli
    const suggestions = cli ? await api.listCLIModels(cli).catch(() => []) : []
    if (form?.cli === cli) form = { ...form, suggestions: suggestions || [], modelLoading: false }
  }

  async function pickFolder() {
    try {
      const folder = await api.pickFolder()
      if (folder && form) {
        form.folder = folder
        invalidatePreflight()
      }
    } catch (e) {
      error = String(e)
    }
  }

  async function launch() {
    if (!form || form.busy) return
    if (!form.folder) {
      error = 'Pick a project folder first.'
      return
    }
    if (!form.cli) {
      error = 'Install and select a CLI first.'
      return
    }
    const local = form.useLocal && localOpt?.configured
    const endpoint = local ? localOpt.endpoint : ''
    const localModel = local ? form.localModel.trim() : ''
    const model = local ? '' : form.model.trim()
    form.busy = true
    error = ''
    try {
      if (!form.preflightChecked) {
        const check = await api.preflightExecution(form.agentID, 'studio', form.cli, model, 'edits', form.folder, endpoint, localModel)
        form = { ...form, busy: false, preflight: check, preflightChecked: true }
        if (!check?.ok) {
          error = (check?.issues || []).filter((issue) => issue.severity === 'error').map((issue) => issue.message).join('\n') || 'Studio preflight failed.'
          return
        }
        const warnings = (check?.issues || []).filter((i) => i.severity !== 'error')
        if (warnings.length > 0) {
          preflightWarnings = warnings
          return
        }
        form.busy = true
      }
      const cli = form.cli
      const folder = form.folder
      showToast({ title: 'Opening Studio', message: `Starting ${agentName({ AgentID: form.agentID })} with ${cli} in ${folder}`, tone: 'busy', duration: 0, dismissible: false })
      const createdChatId = await api.prepareStudioChat(folder, form.agentID, cli, model, endpoint, localModel, form.skillChoices)
      await api.openEditorWindow(folder, form.agentID, cli, model, createdChatId, endpoint, '', localModel)
      if (form.name && createdChatId) await api.renameChat(createdChatId, form.name)
      form = null
      await load()
      showToast({ title: 'Studio opened', message: 'The session is ready in a separate Studio window.', tone: 'ok' })
    } catch (e) {
      error = String(e)
      showToast({ title: 'Studio failed to open', message: String(e), tone: 'err', duration: 0 })
      if (form) form.busy = false
    }
  }

  async function reopen(chat) {
    error = ''
    try {
      openChatId.set(chat.ID)
      showToast({ title: 'Opening Studio', message: chat.Title, tone: 'busy', duration: 0, dismissible: false })
      await api.openEditorWindow(chat.WorkspacePath, chat.AgentID || '', chat.CLIAgent || '', chat.Settings?.model || '', chat.ID, '', '', '')
      showToast({ title: 'Studio reopened', message: chat.Title, tone: 'ok' })
    } catch (e) {
      error = String(e)
      showToast({ title: 'Studio failed to open', message: String(e), tone: 'err', duration: 0 })
    }
  }

  async function remove(chat) {
    if (!confirm(`Delete Studio session "${chat.Title}"? This removes its transcript too.`)) return
    try {
      await api.deleteChat(chat.ID)
      await load()
    } catch (e) {
      error = String(e)
    }
  }

  onMount(load)
</script>

<div class="row" style="margin-bottom:4px">
  <h1 class="grow" style="margin:0">Studio</h1>
  <button class="btn" on:click={load} disabled={loading}>{loading ? 'Refreshing…' : 'Refresh'}</button>
  <button class="btn primary" on:click={openNew}>+ New Studio</button>
</div>
<p class="subtitle">Document-focused agent sessions live here. Reopen an existing workspace or start a new Studio with an optional agent persona.</p>

{#if error}<div class="banner">{error}</div>{/if}
{#if loading && !chats.length}<div class="empty">Loading Studio sessions…</div>{/if}
{#if !loading && sessions.length === 0}<div class="empty">No Studio sessions yet. Press “New Studio” to open one.</div>{/if}

{#each sessions as chat}
  <div class="card row">
    <div class="grow">
      <div class="card-title">{chat.Title}</div>
      <div class="card-sub">{agentName(chat)} · {chat.CLIAgent}{chat.Settings?.model ? ` · ${chat.Settings.model}` : ''} · {fmtDate(chat.UpdatedAt)}</div>
      <div class="card-sub mono studio-path">{chat.WorkspacePath}</div>
    </div>
    <button class="btn primary" on:click={() => reopen(chat)}>Open Studio</button>
    <button class="btn" on:click={() => openConfig(chat)}>Edit</button>
    <button class="btn danger" on:click={() => remove(chat)}>Delete</button>
  </div>
{/each}

{#if form}
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <div class="modal-backdrop" on:click|self={() => !form.busy && (form = null)}>
    <div class="modal-content studio-modal" role="dialog" aria-modal="true" aria-labelledby="new-studio-title">
      <h2 id="new-studio-title">New Studio</h2>
      <p class="subtitle">Choose the workspace, optional persona, and CLI used by the Studio assistant.</p>

      <label class="lbl" for="studio-name">Session Name</label>
      <input id="studio-name" class="field" bind:value={form.name} placeholder="e.g. Frontend Work" />

      <label class="lbl" for="studio-agent">Agent persona</label>
      <select id="studio-agent" class="field" bind:value={form.agentID} on:change={agentChanged}>
        <option value="">No agent persona</option>
        {#each editorAgents as agent}<option value={agent.id}>{agent.name}</option>{/each}
      </select>

      <label class="lbl" for="studio-cli">CLI</label>
      <select id="studio-cli" class="field" bind:value={form.cli} on:change={cliChanged}>
        {#each compatibleCLIs as cli}
          <option value={cli.id} disabled={!cli.available}>{cli.label}{cli.available ? '' : ' — not installed'}</option>
        {/each}
      </select>

      {#if localOpt?.configured && localRoutable}
        <label class="row local-toggle">
          <input type="checkbox" bind:checked={form.useLocal} on:change={invalidatePreflight} />
          <span>Use the local LLM <span class="card-sub mono">{localOpt.endpoint}</span></span>
        </label>
      {:else if localOpt?.configured}
        <div class="card-sub" style="margin-top:10px">{localRoutingUnavailableMessage(form.cli)}</div>
      {/if}

      {#if form.useLocal && localOpt?.configured && localRoutable}
        <label class="lbl" for="studio-local-model">Local model</label>
        <input id="studio-local-model" class="field mono" list="studio-local-models" bind:value={form.localModel} on:input={invalidatePreflight} placeholder="model on your endpoint" />
        <datalist id="studio-local-models">{#each localOpt.models || [] as model}<option value={model}></option>{/each}</datalist>
      {:else}
        <label class="lbl" for="studio-model">Model <span class="card-sub">(blank = CLI default)</span></label>
        <input id="studio-model" class="field mono" list="studio-models" bind:value={form.model} on:input={invalidatePreflight} />
        <datalist id="studio-models">{#each form.suggestions || [] as model}<option value={model}></option>{/each}</datalist>
        {#if form.modelLoading}<div class="card-sub">Loading models…</div>{/if}
      {/if}

      <label class="lbl" for="studio-folder">Project folder *</label>
      <div class="row">
        <input id="studio-folder" class="field grow mono" bind:value={form.folder} on:input={invalidatePreflight} placeholder="folder the Studio may read and edit" />
        <button class="btn" on:click={pickFolder}>Browse…</button>
      </div>

      <SkillChoiceDraft bind:choices={form.skillChoices} />

      <div class="row actions" style="margin-top:20px">
        <button class="btn" on:click={() => (form = null)} disabled={form.busy}>Cancel</button>
        <button class="btn primary" on:click={launch} disabled={form.busy}>{form.busy ? 'Opening…' : 'Open Studio'}</button>
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

{#if cfg}
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <div class="picker-backdrop" on:click={() => (cfg = null)}>
    <div class="picker" on:click|stopPropagation role="dialog" style="max-width:640px; max-height:90vh; overflow-y:auto; display:flex; flex-direction:column;">
      <div class="picker-head">
        <strong class="grow">Settings — {cfg.chat.Title || cfg.chat.WorkspacePath}</strong>
        <button class="picker-x" on:click={() => (cfg = null)}>×</button>
      </div>
      <div class="picker-body grow" style="padding:16px;">
        <div class="card-sub" style="margin-bottom:12px;">Switching the CLI starts a fresh session on the next message; the history stays.</div>
        <label class="lbl">Session Name</label>
        <input class="field" style="max-width:320px; margin-bottom:12px" bind:value={cfg.name} />

        <label class="lbl">CLI</label>
        <select class="field" style="max-width:320px" bind:value={cfg.cli} on:change={cfgCliChanged}>
          {#if clis.length === 0}<option value={cfg.cli}>{cfg.cli} (probing CLIs…)</option>{/if}
          {#each clis as c}
            <option value={c.id} disabled={!c.available && c.id !== cfg.chat.CLIAgent}>
              {c.label}{c.available ? '' : ' — not installed'}
            </option>
          {/each}
        </select>
        <label class="lbl">Model (blank = CLI default)</label>
        <input class="field mono" style="max-width:420px" list="cfg-model-suggestions" bind:value={cfg.model} />
        <datalist id="cfg-model-suggestions">
          {#each cfg.suggestions || [] as m}<option value={m}></option>{/each}
        </datalist>
        {#if cfg.modelLoading}<div class="card-sub">Loading models...</div>{/if}
        <label class="lbl">Tools</label>
        <div class="row">
            {#each toolLevelsForCli(cfg.cli) as lvl}
              <button class="btn sm" class:primary={cfg.tools === lvl.id} title={lvl.hint} on:click={() => (cfg.tools = lvl.id)}>{lvl.label}</button>
            {/each}
        </div>
        {#if localOpt?.configured && supportsLocalRouting(cfg.cli)}
          <label class="row" style="margin-top:10px; gap:8px; cursor:pointer">
            <input type="checkbox" checked={!!cfg.localEndpoint} on:change={(e) => {
              cfg.localEndpoint = e.currentTarget.checked ? localOpt.endpoint : ''
              cfg.localModel = e.currentTarget.checked ? cfg.localModel || localOpt.models?.[0] || '' : ''
            }} />
            <span>Use the local LLM <span class="card-sub mono">{localOpt.endpoint}</span></span>
          </label>
          {#if cfg.localEndpoint}
            <label class="lbl" style="margin-top:8px">Local model</label>
            <input class="field mono" style="max-width:420px" list="cfg-local-models" bind:value={cfg.localModel} placeholder="model on your endpoint" />
            <datalist id="cfg-local-models">{#each localOpt.models || [] as m}<option value={m}></option>{/each}</datalist>
          {/if}
        {/if}
        {#key cfg.chat.ID}<SkillBindingsEditor chatID={cfg.chat.ID} />{/key}
        <label class="lbl" style="margin-top:10px">MCP servers</label>
        {#if mcpServers.length === 0}
          <div class="card-sub">No enabled MCP servers.</div>
        {:else}
          <div class="mcp-grid">
            {#each mcpServers as server}
              <label class="mcp-card">
                <input
                  type="checkbox"
                  checked={cfg.mcps?.includes(server.id)}
                  on:change={(e) => {
                    cfg.mcps = e.currentTarget.checked
                      ? [...(cfg.mcps || []), server.id]
                      : (cfg.mcps || []).filter((id) => id !== server.id)
                  }} />
                <span><strong>{server.name}</strong> <span class="card-sub">{server.transport}</span></span>
              </label>
            {/each}
          </div>
        {/if}
      </div>
      <div class="picker-foot" style="justify-content:flex-end;">
        <button class="btn" on:click={() => (cfg = null)}>Cancel</button>
        <button class="btn primary" on:click={saveConfig} disabled={cfgSaving}>{cfgSaving ? 'Saving…' : 'Save'}</button>
      </div>
    </div>
  </div>
{/if}
<style>
  .studio-path { margin-top: 3px; overflow-wrap: anywhere; }
  .studio-modal { max-width: 640px; max-height: 90vh; overflow-y: auto; }
  .local-toggle { margin-top: 12px; cursor: pointer; }
  .preflight { margin-top: 12px; }
  .actions { justify-content: flex-end; margin-top: 16px; }
  .picker-backdrop { position: fixed; inset: 0; z-index: 10000; background: rgba(0,0,0,0.5); display: flex; align-items: center; justify-content: center; padding: 24px; }
  .picker { background: var(--bg-panel); border: 1px solid var(--border); border-radius: 12px; box-shadow: 0 12px 40px rgba(0,0,0,0.25); width: 100%; }
  .picker-head { display: flex; align-items: center; padding: 12px 16px; border-bottom: 1px solid var(--border); }
  .picker-x { background: none; border: none; font-size: 20px; line-height: 1; color: var(--text-dim); cursor: pointer; }
  .picker-foot { padding: 12px 16px; border-top: 1px solid var(--border); display: flex; gap: 8px; }
  .mcp-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 8px; margin-top: 8px; }
  .mcp-card { display: flex; align-items: flex-start; gap: 8px; padding: 8px; border: 1px solid var(--border); border-radius: 6px; cursor: pointer; }
  .mcp-card:hover { background: var(--bg-raised, rgba(255,255,255,0.04)); }
  .mcp-card input { margin-top: 2px; }
</style>
