<script>
  import { onMount, onDestroy, tick } from 'svelte'
  import { Terminal } from '@xterm/xterm'
  import { FitAddon } from '@xterm/addon-fit'
  import '@xterm/xterm/css/xterm.css'
  import { api } from '../lib/api.js'
  import SkillBindingsEditor from '../lib/SkillBindingsEditor.svelte'
  import SkillChoiceDraft from '../lib/SkillChoiceDraft.svelte'
  import { term, onTermData, onTermExit, decodeBase64Bytes, findTerminalForChat } from '../lib/terminal.js'
  import { localRoutingUnavailableMessage, supportsLocalRouting } from '../lib/localRouting.js'
  import { pendingTerm, showSkillPreparationToast } from '../lib/stores.js'
  import { get } from 'svelte/store'


  let chats = []
  let workspaceChats = []
  let agentNames = new Map()
  $: codeChats = chats.filter((c) => c.Settings?.surface === 'code')

  function fmtDate(s) {
    if (!s || s.startsWith('0001-')) return ''
    return new Date(s).toLocaleString()
  }

  function agentName(chat) {
    return agentNames.get(chat.AgentID) || chat.AgentID
  }

  async function remove(chat) {
    if (!confirm(`Delete code session "${chat.Title || chat.WorkspacePath}"?`)) return
    try {
      await api.deleteChat(chat.ID)
      await load()
    } catch (e) {
      error = String(e)
    }
  }

  async function reopenCode(chat) {
    const terms = (await api.listTerminalSessions().catch(() => [])) || []
    const live = findTerminalForChat(terms, chat)
    if (live) {
      if (detachedTerms.has(live.id)) {
        error = 'This terminal is open in a detached window. Close that window to reattach it here.'
        return
      }
      try { await api.bindChatToTerminal(live.id, chat.ID) } catch {}
      attachPending({ termId: live.id, chat })
      return
    }
    attachPending({ termId: '', chat, cli: chat.CLIAgent, cwd: chat.WorkspacePath })
  }

  async function openWorkspace(wc) {
    attachPending({
      termId: '',
      chat: { ID: wc.id, Settings: { skills: [] } },
      cli: wc.agent,
      cwd: wc.sandbox,
    })
  }

  let agents = []
  let runtimeModes = {}
  let error = ''

  // PrAImate Code bundled CLI install state
  let codeInstalled = true
  let installing = false
  let installLog = ''
  // Set when no prebuilt exists for this OS/arch — offer the local
  // compile (BuildToolFromSource) instead of a retry that can't work.
  let compileOffer = false
  let unsubInstall = () => {}
  let unsubDetached = () => {}
  let detachedTerms = new Set()

  // setup state
  let agent = null
  let cli = ''
  let sessionName = ''
  let model = ''
  let cwd = ''
  let sessionLabel = '' // header label for the running session

  // CLI + model pickers (shared by the clean-session card and the
  // agent-config view). clis: [{id, available, modelHint}].
  let cfg = null
  let cfgSaving = false
  let mcpServers = []

  function toolLevelsForCli(c) {
    if (c === 'claude' || c === 'openclaude') return [{id:'',label:'Safe',hint:''}, {id:'ask',label:'Ask',hint:''}, {id:'edits',label:'Edits',hint:''}, {id:'full',label:'Full',hint:''}]
    if (c === 'opencode' || c === 'praimate-code') return [{id:'plan',label:'Plan',hint:''}, {id:'',label:'Build',hint:''}, {id:'full',label:'Full',hint:''}]
    return [{id:'',label:'Safe',hint:''}, {id:'ask',label:'Ask',hint:''}, {id:'edits',label:'Edits',hint:''}, {id:'full',label:'Full',hint:''}]
  }
  function normalizeToolsForCli(c, t) {
    if (c === 'opencode' || c === 'praimate-code') return t === 'plan' || t === 'full' ? t : ''
    return t || ''
  }
  function cfgCliChanged() {
    cfg.tools = normalizeToolsForCli(cfg.cli, cfg.tools)
    cfg.model = ''
    cfg.suggestions = []
    cfg.modelLoading = true
    api.listCLIModels(cfg.cli).then(r => { if (cfg) { cfg.suggestions = r || []; cfg.modelLoading = false } }).catch(() => { if (cfg) { cfg.modelLoading = false } })
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
    if (clis.length === 0) api.listCLIs().then(r => clis = r || []).catch(() => {})
    api.listCLIModels(chat.CLIAgent).then(r => { if (cfg && cfg.chat.ID === chat.ID) { cfg.suggestions = r || []; cfg.modelLoading = false } }).catch(() => {})
    api.mcpServers().then(r => { mcpServers = (r || []).filter(s => s.enabled) }).catch(() => {})
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

  let clis = []
  let modelSuggestions = []
  let modelLoading = false
  let modelLoadSeq = 0
  $: selectedCliInfo = clis.find((c) => c.id === cli)
  $: modelSupported = !!selectedCliInfo?.modelHint
  $: terminalAgents = agents.filter((a) => runtimeModes[a.id]?.mode === 'native' && (!a.surfaces?.length || a.surfaces.includes('terminal')))

  // Local LLM (Settings → Local LLM). useLocal routes the session at the
  // configured endpoint; localModel picks from its live model list.
  let localOpt = null // { configured, endpoint, hasApiKey, models[], error }
  let useLocal = false
  let localModel = ''
  $: localRoutable = supportsLocalRouting(cli)
  $: if (!localRoutable && useLocal) useLocal = false

  async function loadModels() {
    const seq = ++modelLoadSeq
    if (!cli) { modelSuggestions = []; return }
    modelLoading = true
    try { modelSuggestions = (await api.listCLIModels(cli)) || [] } catch { modelSuggestions = [] }
    finally { if (seq === modelLoadSeq) modelLoading = false }
  }
  // Refresh model suggestions whenever the chosen CLI changes.
  $: if (cli) { loadModels() }

  async function checkCode() {
    try { codeInstalled = await api.praimateCodeInstalled() } catch { codeInstalled = false }
  }

  async function installCode() {
    installing = true
    installLog = ''
    error = ''
    compileOffer = false
    try {
      // Returns { ok, log, error, noPrebuiltAsset } — never a string.
      const res = await api.installPraimateCode()
      installLog = res?.log || ''
      if (res && res.ok === false) {
        if (res.noPrebuiltAsset) {
          compileOffer = true
        } else {
          error = 'Install failed: ' + (res.error || 'unknown error')
        }
      }
      await checkCode()
    } catch (e) {
      error = 'Install failed: ' + String(e)
    } finally {
      installing = false
    }
  }

  // Compile PrAImate Code locally (clones the repo, builds with bun).
  // Output streams over "praimate:install" into installLog.
  async function compileCode() {
    installing = true
    installLog = ''
    error = ''
    compileOffer = false
    try {
      await api.buildToolFromSource('praimate-code')
      await checkCode()
    } catch (e) {
      error = 'Compile failed: ' + String(e)
    } finally {
      installing = false
    }
  }

  // running terminal
  let started = false
  // Skills attached to the current Code chat (cached locally; persists
  // to the chat row through SetChatSkills on Done).
  let sessionChatId = ''
  let sessionSkills = []
  let exited = false
  let termId = null
  let el            // xterm host div
  let xterm = null
  let fit = null
  let unsubData = () => {}
  let unsubExit = () => {}
  let ro = null

  async function load() {
    try {
      const [loadedChats, loadedAgents] = await Promise.all([
        api.listChats(),
        api.listAgents().catch(() => []),
      ])
      chats = loadedChats || []
      agents = loadedAgents || []
      agentNames = new Map(agents.map((a) => [a.id, a.name || a.id]))
      const configs = await Promise.all(loadedAgents.map(async (agent) => {
        try { return [agent.id, await api.agentRuntimeConfig(agent.id)] }
        catch { return [agent.id, { mode: 'invalid' }] }
      }))
      runtimeModes = Object.fromEntries(configs)
    } catch (e) {
      error = String(e)
    }
    try {
      workspaceChats = (await api.listWorkspaceChats()) || []
    } catch {
      workspaceChats = []
    }
    try {
      clis = (await api.listCLIs()) || []
      // Default the clean-session CLI to the first installed one.
      if (!cli) {
        const firstAvail = clis.find((c) => c.available)
        cli = firstAvail ? firstAvail.id : (clis[0]?.id ?? 'claude')
      }
    } catch { /* clis stay empty; the select still works with defaults */ }
    try { localOpt = await api.localLLMModels() } catch { localOpt = null }
  }

  function pick(a) {
    agent = a
    cli = (a.supports && a.supports[0]) || 'claude'
    model = ''
    error = ''
    initialSkillChoices = null
  }

  // Start a clean session — no agent persona, just CLI + model + folder.
  function newClean() {
    agent = null
    model = ''
    error = ''
    initialSkillChoices = null
    if (!cli) {
      const firstAvail = clis.find((c) => c.available)
      cli = firstAvail ? firstAvail.id : (clis[0]?.id ?? 'claude')
    }
    cleanSetup = true
  }
  let cleanSetup = false
  let initialSkillChoices = null

  async function notifyTerminalSkills(chatID) {
    if (!chatID) return
    try {
      const selection = await api.chatSkillsV2(chatID)
      const refs = (selection?.config?.bindings || []).map((binding) => binding.ref)
      showSkillPreparationToast(refs, 'Terminal')
    } catch { /* the terminal remains usable if the evidence lookup fails */ }
  }

  async function chooseFolder() {
    try {
      const p = await term.pickFolder()
      if (p) cwd = p
    } catch (e) {
      error = String(e)
    }
  }

  async function launch() {
    if (!cwd) { error = 'Pick a project folder first.'; return }
    if (!cli) { error = 'Pick a CLI first.'; return }
    const local = useLocal && localOpt?.configured
    if (local && !localRoutable) {
      error = `${cli} local-LLM routing is not supported by PrAImate.`
      return
    }
    error = ''
    try {
      // agent.id when launched from an agent; '' for a clean session
      // (StartTerminal skips the persona/context-file write).
      const created = await api.startCodeSessionWithSkills(
        agent ? agent.id : '',
        cli,
        local ? '' : (modelSupported ? model.trim() : ''),
        cwd,
        local ? localOpt.endpoint : '',
        local ? localModel.trim() : '',
        initialSkillChoices
      )
      termId = created.termId
      sessionChatId = created.chatId
    } catch (e) {
      error = String(e)
      return
    }
    sessionLabel = (agent ? agent.name : cli) + (local ? ' · local' : '')
    // Persist and bind before yielding control, otherwise a quick navigation
    // can leave a live PTY that the Sessions panel cannot find again.
    try {
      if (sessionChatId && sessionName.trim()) {
        await api.renameChat(sessionChatId, sessionName.trim())
      }
      if (sessionChatId) {
        try { sessionSkills = (await api.chatSkills(sessionChatId)) || [] } catch {}
        await notifyTerminalSkills(sessionChatId)
      }
    } catch { /* recording must not stop the terminal */ }
    started = true
    await tick()
    try {
      await mountXterm()
    } catch (e) {
      error = `Terminal renderer failed: ${String(e)}`
    }
  }

  async function mountXterm() {
    xterm = new Terminal({
      fontFamily: 'JetBrains Mono, ui-monospace, monospace',
      fontSize: 13,
      cursorBlink: true,
      theme: { background: '#101218', foreground: '#e6e9f0' },
    })
    fit = new FitAddon()
    xterm.loadAddon(fit)
    xterm.open(el)
    fit.fit()
    xterm.focus()

    xterm.attachCustomKeyEventHandler((e) => {
      if (e.type === 'keydown' && e.ctrlKey && (e.key === 'c' || e.key === 'C') && xterm.hasSelection()) {
        const text = xterm.getSelection()
        if (text && window.runtime?.ClipboardSetText) {
          window.runtime.ClipboardSetText(text)
          xterm.clearSelection()
          return false
        }
      }
      return true
    })

    // keystrokes → PTY
    xterm.onData((d) => term.write(termId, d))
    // PTY → screen. Subscribe before requesting the snapshot, queueing live
    // chunks until replay finishes. Byte offsets remove the overlap between
    // the snapshot and chunks emitted while the request was in flight.
    let replaying = true
    const queued = []
    unsubData = onTermData(termId, (data, meta) => {
      if (replaying) queued.push({ data, meta })
      else xterm?.write(data)
    })
    unsubExit = onTermExit(termId, () => {
      exited = true
      xterm.write('\r\n\x1b[2m[process exited — press “New session” to start again]\x1b[0m\r\n')
    })

    // keep PTY size in sync with the pane
    const sync = () => {
      try {
        fit.fit()
        term.resize(termId, xterm.cols, xterm.rows)
      } catch { /* pane not visible yet */ }
    }
    sync()
    ro = new ResizeObserver(sync)
    ro.observe(el)

    let cursor = 0
    try {
      // Bound Code chats use the persisted transcript, which includes output
      // from prior processes and survives an application restart. The backend
      // returns the current process offset alongside it so queued live events
      // are still de-duplicated exactly once.
      let snap
      if (sessionChatId) {
        try {
          snap = await term.codeSnapshot(sessionChatId, termId)
        } catch {
          // Session persistence is best-effort. If its history file or PTY
          // binding failed, replay the live buffer instead of leaving a
          // healthy terminal blank after its initial output was emitted.
          snap = await term.snapshot(termId)
        }
      } else {
        snap = await term.snapshot(termId)
      }
      cursor = Number(snap?.endOffset || 0)
      if (snap?.data) xterm?.write(decodeBase64Bytes(snap.data))
    } catch { /* a very short-lived process may already be gone */ }
    for (const item of queued) {
      if (!item.meta) {
        xterm?.write(item.data)
        continue
      }
      const start = Number(item.meta.startOffset || 0)
      const end = Number(item.meta.endOffset || start + item.data.length)
      if (end <= cursor) continue
      const skip = Math.max(0, cursor - start)
      xterm?.write(skip ? item.data.slice(skip) : item.data)
      cursor = end
    }
    replaying = false
  }

  function teardown(closeTerminal = false) {
    unsubData(); unsubExit()
    if (ro) { ro.disconnect(); ro = null }
    if (closeTerminal && termId) term.close(termId)
    if (xterm) { xterm.dispose(); xterm = null }
    termId = null
  }

  function reset() {
    teardown(true)
    started = false
    exited = false
    sessionChatId = ''
    sessionSkills = []
  }

  async function detachTerminal() {
    if (!termId) return
    const id = termId
    error = ''
    try {
      await api.detachSession('terminal', id, sessionLabel || cli || 'Terminal')
      detachedTerms = new Set([...detachedTerms, id])
      teardown(false)
      started = false
      exited = false
      sessionChatId = ''
      sessionSkills = []
    } catch (e) {
      error = String(e)
    }
  }

  // Attach to a PTY the Chats / Sessions page already started, OR
  // resume the CLI's latest native session in the same folder when the prior
  // PTY is gone (or fall back to a normal launch for unsupported CLIs).
  // The Sessions panel signals "PTY is gone" by setting termId=''; we
  // launch with the native resume selector in that case.
  async function attachPending(p) {
    sessionLabel = p.label
    cli = p.cli
    cwd = p.cwd
    agent = p.agentId ? agents.find((a) => a.id === p.agentId) || { id: p.agentId, name: p.agentName || p.agentId } : null
    sessionChatId = p.chatId || ''
    if (sessionChatId) {
      try { sessionSkills = (await api.chatSkills(sessionChatId)) || [] } catch { sessionSkills = [] }
      await notifyTerminalSkills(sessionChatId)
    }
    if (p.termId) {
      // Live PTY — just reattach xterm to the existing stream.
      termId = p.termId
      started = true
      await tick()
      try {
        await mountXterm()
      } catch (e) {
        error = `Terminal renderer failed: ${String(e)}`
        return
      }
      if (p.note) {
        xterm.write(`\x1b[2m[${p.note}]\x1b[0m\r\n`)
      }
      return
    }
    // No live PTY — start from the persisted chat truth. This restores the
    // local route and the exact versioned skill bindings atomically.
    try {
      if (sessionChatId) {
        termId = await api.startTerminalForChat(sessionChatId, true)
      } else {
        termId = await term.start(
          p.agentId || '', cli, p.model || '', cwd,
          p.localEndpoint || '', p.localApiKey || '', p.localModel || '', true, sessionSkills)
      }
    } catch (e) {
      error = String(e)
      return
    }
    started = true
    await tick()
    try {
      await mountXterm()
    } catch (e) {
      error = `Terminal renderer failed: ${String(e)}`
      return
    }
    if (p.note) {
      xterm.write(`\x1b[2m[${p.note}]\x1b[0m\r\n`)
    }
  }

  onMount(() => {
    load()
    checkCode()
    api.detachedWindows().then((items) => {
      detachedTerms = new Set((items || []).filter((item) => item.kind === 'terminal').map((item) => item.sessionId))
    }).catch(() => {})
    // Live install/compile output for the PrAImate Code card.
    if (typeof window !== 'undefined' && window.runtime?.EventsOn) {
      window.runtime.EventsOn('praimate:install', (ev) => {
        if (ev?.cli === 'praimate-code' || ev?.cli === 'build:praimate-code') {
          installLog = (installLog ? installLog + '\n' : '') + ev.line
          const lines = installLog.split('\n')
          if (lines.length > 200) installLog = lines.slice(-200).join('\n')
        }
      })
      unsubInstall = () => window.runtime.EventsOff('praimate:install')
      window.runtime.EventsOn('praimate:detached-windows', (items) => {
        detachedTerms = new Set((items || []).filter((item) => item.kind === 'terminal').map((item) => item.sessionId))
      })
      unsubDetached = () => window.runtime.EventsOff('praimate:detached-windows')
    }
    const p = get(pendingTerm)
    if (p) {
      pendingTerm.set(null)
      attachPending(p)
    }
  })
  // Page navigation/minimising detaches the renderer but intentionally keeps
  // the PTY alive. Stop/New session are the explicit process-ending actions.
  onDestroy(() => { unsubInstall(); unsubDetached(); teardown(false) })
</script>

<div class="row" style="margin-bottom:4px">
  <h1 class="grow" style="margin:0">Code</h1>
  {#if !started && !cleanSetup}
    <button class="btn primary" on:click={newClean}>+ New session</button>
  {/if}
</div>
<p class="subtitle">Run a CLI live in a project folder — the real tool, with streaming, tool calls, and file edits. Start a clean session on any CLI/model, or launch an agent persona.</p>

{#if error}<div class="banner">{error}</div>{/if}

{#if !started && !codeInstalled}
  <div class="card">
    <div class="card-title">PrAImate Code (bundled coding CLI) not installed</div>
    <div class="card-sub">
      PrAImate Code is our version-pinned build of OpenCode. Install it once
      (~150MB download) to use it as a CLI here and via <span class="mono">praimate code</span>.
    </div>
    <div class="row" style="margin-top:10px">
      <button class="btn primary" on:click={installCode} disabled={installing}>
        {installing ? 'Installing…' : 'Install PrAImate Code'}
      </button>
      {#if compileOffer}
        <button class="btn" on:click={compileCode} disabled={installing}>Compile from source</button>
      {/if}
    </div>
    {#if compileOffer}
      <div class="card-sub" style="margin-top:8px; color: var(--warn)">
        No prebuilt PrAImate Code is published for this OS/arch. You can compile
        it locally instead — needs <span class="mono">git</span> and
        <span class="mono">bun</span>{navigator.platform?.startsWith('Win') ? ' plus Git Bash' : ''}
        on PATH (several minutes, large download).
      </div>
    {/if}
    {#if installLog}<pre class="mono" style="margin-top:10px; max-height:140px; overflow:auto">{installLog}</pre>{/if}
  </div>
{/if}

{#if !started}
  {#if cleanSetup}
    <!-- New clean session: pick CLI + model + folder, no agent persona. -->
    <div class="card">
      <div class="card-title">New code session</div>
      <div class="card-sub">Run a CLI live in your project folder — no agent persona or context file is written. Pick the CLI and (optionally) the model.</div>
      <label class="lbl">Session Name</label>
      <input class="field" style="max-width:320px; margin-bottom:10px" bind:value={sessionName} placeholder="e.g. My Code Session" />
      <label class="lbl">CLI</label>
      <select class="field" style="max-width:320px" bind:value={cli}>
        {#if clis.length === 0}<option value="">probing installed CLIs…</option>{/if}
        {#each clis as c}
          <option value={c.id} disabled={!c.available}>{c.label || c.id}{c.available ? '' : ' — not installed'}</option>
        {/each}
      </select>
      {#if localOpt?.configured && localRoutable}
        <label class="row" style="margin-top:12px; gap:8px; cursor:pointer">
          <input type="checkbox" bind:checked={useLocal} />
          <span>Use the local LLM from Settings <span class="card-sub mono">{localOpt.endpoint}</span></span>
        </label>
      {:else if localOpt?.configured}
        <div class="card-sub" style="margin-top:10px">{localRoutingUnavailableMessage(cli)}</div>
      {/if}

      {#if useLocal && localOpt?.configured && localRoutable}
        <label class="lbl">Local model</label>
        {#if localOpt.allModels?.length}
          <select class="field mono" style="max-width:420px; margin-bottom:6px" bind:value={localModel}>
            <option value="">Select a detected model…</option>
            {#each localOpt.hosts || [] as host}
              <optgroup label={`${host.name} (${host.endpoint})`}>
                {#each host.models as m}
                  <option value={m}>{m}</option>
                {/each}
              </optgroup>
            {/each}
          </select>
        {/if}
        <input class="field mono" style="max-width:420px" list="code-local-models" placeholder="or type model name (e.g. qwen2.5-coder)" bind:value={localModel} />
        <datalist id="code-local-models">{#each localOpt.models || [] as m}<option value={m}></option>{/each}</datalist>
        {#if localOpt.error && !localOpt.allModels?.length}<div class="card-sub" style="color: var(--warn)">Couldn't list models from the endpoint: {localOpt.error}. You can still type a model name.</div>{/if}
      {:else}
        <label class="lbl">Model {modelSupported ? `(${selectedCliInfo.modelHint})` : '(this CLI has no model flag — it uses its own config)'}</label>
        <input
          class="field mono"
          style="max-width:420px"
          list="code-models"
          placeholder={modelSupported ? 'blank = CLI default' : 'not supported'}
          bind:value={model}
          disabled={!modelSupported} />
        <datalist id="code-models">{#each modelSuggestions as m}<option value={m}></option>{/each}</datalist>
        {#if modelLoading}<div class="card-sub">Loading models...</div>{/if}
      {/if}
      <label class="lbl">Project folder</label>
      <div class="row">
        <input class="field grow" bind:value={cwd} placeholder="/path/to/your/project" />
        <button class="btn" on:click={chooseFolder}>Browse…</button>
      </div>
      <SkillChoiceDraft bind:choices={initialSkillChoices} />
      <div class="row" style="margin-top:14px">
        <button class="btn primary" on:click={launch} disabled={!cwd || !cli}>Launch {cli || 'CLI'} here</button>
        <button class="btn" on:click={() => (cleanSetup = false)}>Cancel</button>
      </div>
    </div>
  {:else if !agent}
  {#if codeChats.length > 0}
    <h1 style="font-size:16px; margin-top:24px">Code sessions</h1>
    <p class="subtitle">Live CLI sessions in a project folder. Reopen reattaches the same running process and restores its terminal history.</p>
    {#each codeChats as chat}
      <div class="card row">
        <div class="grow">
          <div class="card-title">{chat.Title || chat.WorkspacePath}</div>
          <div class="card-sub mono">
            {chat.WorkspacePath} <br/>
            {chat.CLIAgent}
            {#if chat.AgentID} · Agent: {agentName(chat)}{/if}
            {#if chat.Settings?.local?.endpoint}· local {chat.Settings.local.model || chat.Settings.local.endpoint}
            {:else if chat.Settings?.model}· {chat.Settings.model}{/if}
            · {fmtDate(chat.UpdatedAt)}
          </div>
        </div>
        <button class="btn primary" on:click={() => reopenCode(chat)}>Reopen</button>
        <button class="btn" on:click={() => openConfig(chat)}>Edit</button>
        <button class="btn danger" on:click={() => remove(chat)}>Delete</button>
      </div>
    {/each}
  {/if}

  {#if workspaceChats.length > 0}
    <h1 style="font-size:16px; margin-top:24px">Workspace chats</h1>
    <p class="subtitle">Existing workpath-based chats. Open one to resume its CLI session in the Code terminal.</p>
    {#each workspaceChats as wc}
      <div class="card row">
        <div class="grow">
          <div class="card-title">{wc.label}</div>
          <div class="card-sub">
            {wc.agent}{#if wc.template} · {wc.template}{/if} · {fmtDate(wc.lastUsed)}
          </div>
        </div>
        <button class="btn" on:click={() => openWorkspace(wc)}>Open in Code</button>
      </div>
    {/each}
  {/if}
  {:else}
    <div class="row" style="margin-bottom:14px">
      <button class="btn" on:click={() => (agent = null)}>← Agents</button>
      <strong>{agent.name}</strong>
    </div>
    <label class="lbl">Session Name</label>
    <input class="field" style="max-width:320px; margin-bottom:10px" bind:value={sessionName} placeholder="e.g. Debugging" />
    <label class="lbl">CLI</label>
    <select class="field" bind:value={cli} style="max-width:240px">
      {#each agent.supports || [] as s}<option value={s}>{s}</option>{/each}
    </select>
    {#if modelSupported}
      <label class="lbl">Model <span class="card-sub">(optional — blank uses the CLI default)</span></label>
      <input class="field" list="code-models" bind:value={model} placeholder="provider/model or model name" style="max-width:420px" />
      <datalist id="code-models">{#each modelSuggestions as m}<option value={m}></option>{/each}</datalist>
      {#if modelLoading}<div class="card-sub">Loading models...</div>{/if}
    {/if}
    <label class="lbl">Project folder</label>
    <div class="row">
      <input class="field grow" bind:value={cwd} placeholder="/path/to/your/project" />
      <button class="btn" on:click={chooseFolder}>Browse…</button>
    </div>
    <SkillChoiceDraft bind:choices={initialSkillChoices} />
    <div style="margin-top:16px">
      <button class="btn primary" on:click={launch} disabled={!cwd}>Launch {cli} here</button>
    </div>
    <p class="card-sub" style="margin-top:10px">
      PrAImate writes the agent's instructions into the folder's context
      file ({cli === 'codex' || cli === 'opencode' ? 'AGENTS.md' : 'CLAUDE.md'})
      so {cli} adopts the persona. The CLI must be installed and on PATH.
    </p>
  {/if}
{:else}
  <div class="row" style="margin-bottom:10px">
    <div class="grow"><strong>{sessionLabel}</strong> <span class="pill">{cli}</span>{#if model}<span class="pill">{model}</span>{/if} <span class="card-sub mono">{cwd}</span></div>
    {#if exited}<button class="btn primary" on:click={reset}>New session</button>{/if}
    <button class="btn" on:click={detachTerminal} disabled={exited} title="Move this terminal into its own window">Detach</button>
    <button class="btn danger" on:click={reset}>Stop</button>
  </div>
  <div class="termhost" bind:this={el}></div>
{/if}

<style>
  .termhost {
    height: calc(100vh - 150px);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: #101218;
    padding: 8px;
    overflow: hidden;
  }
</style>


{#if cfg}
  <!-- svelte-ignore a11y-no-static-element-interactions -->
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
          {#each cfg.suggestions as m}<option value={m}></option>{/each}
        </datalist>
        {#if cfg.modelLoading}<div class="card-sub">Loading models...</div>{/if}
        <label class="lbl">Tools</label>
        <div class="row">
            {#each toolLevelsForCli(cfg.cli) as lvl}
              <button class="btn sm" class:primary={cfg.tools === lvl.id} title={lvl.hint} on:click={() => (cfg.tools = lvl.id)}>{lvl.label}</button>
            {/each}
        </div>
        {#if localOpt?.configured && supportsLocalRouting(cfg.cli)}
          <label class="row" style="margin-top:12px; gap:8px; cursor:pointer">
            <input type="checkbox" checked={!!cfg.localEndpoint} on:change={(e) => { if (e.target.checked) { cfg.localEndpoint = localOpt.endpoint } else { cfg.localEndpoint = ''; cfg.localModel = '' } }} />
            <span>Use the local LLM from Settings <span class="card-sub mono">{localOpt.endpoint}</span></span>
          </label>
          {#if cfg.localEndpoint}
            <label class="lbl" style="margin-top:8px">Local model</label>
            {#if localOpt.allModels?.length}
              <select class="field mono" style="max-width:420px; margin-bottom:6px" bind:value={cfg.localModel}>
                <option value="">Select a detected model…</option>
                {#each localOpt.hosts || [] as host}
                  <optgroup label={`${host.name} (${host.endpoint})`}>
                    {#each host.models as m}
                      <option value={m}>{m}</option>
                    {/each}
                  </optgroup>
                {/each}
              </select>
            {/if}
            <input class="field mono" style="max-width:420px" list="cfg-local-models" bind:value={cfg.localModel} placeholder="or type model name (e.g. qwen2.5-coder)" />
            <datalist id="cfg-local-models">{#each localOpt.models || [] as m}<option value={m}></option>{/each}</datalist>
          {/if}
        {:else if cfg.localEndpoint}
          <div class="card-sub" style="margin-top:8px">{localRoutingUnavailableMessage(cfg.cli)}</div>
          <div class="row" style="margin-top:8px">
            {#if cfg.cli === 'claude' && clis.some((c) => c.id === 'openclaude' && c.available)}
              <button class="btn sm" on:click={() => { cfg.cli = 'openclaude'; cfgCliChanged() }}>Switch to OpenClaude</button>
            {/if}
            <button class="btn sm" on:click={() => { cfg.localEndpoint = ''; cfg.localModel = ''; cfg = cfg }}>Clear local route</button>
          </div>
        {/if}
        {#key cfg.chat.ID}<SkillBindingsEditor chatID={cfg.chat.ID} />{/key}
        <label class="lbl" style="margin-top:10px">MCP servers</label>
        {#if mcpServers.length === 0}
          <div class="card-sub">No enabled MCP servers.</div>
        {:else}
          <div class="mcp-grid">
            {#each mcpServers as mcp}
              <label class="mcp-choice">
                <input type="checkbox" value={mcp.id} bind:group={cfg.mcps} />
                <span><strong>{mcp.name}</strong> <span class="card-sub">{mcp.transport}</span></span>
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
