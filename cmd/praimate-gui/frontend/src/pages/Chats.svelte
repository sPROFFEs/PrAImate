<script>
  import { onMount, onDestroy, tick } from 'svelte'
  import { api, onChatStream, onApproval } from '../lib/api.js'
  import { activePage, pageRevision, openChatId, pendingTerm, showSkillDeliveryToast } from '../lib/stores.js'
  import SkillBindingsEditor from '../lib/SkillBindingsEditor.svelte'
  import SkillChoiceDraft from '../lib/SkillChoiceDraft.svelte'
  import { renderMarkdown } from '../lib/markdown.js'
  import { findTerminalForChat } from '../lib/terminal.js'
  import { localRoutingUnavailableMessage, supportsLocalRouting } from '../lib/localRouting.js'


  let chats = []
  let agents = []
  let agentNames = new Map()
  let workspaceChats = []
  let error = ''
  let selected = null
  let messages = []
  let draft = ''
  let sending = false
  let threadEl
  let toolsLevel = ''
  let attachments = [] // staged for the next send: {name, path, image}
  // Live turn state (claude/openclaude/codex stream events; other CLIs
  // resolve in one shot and this stays empty until the reply lands).
  let stream = null // { text, tools: [{id, tool, detail, done, ok}] }
  let unsubStream = () => {}
  let unsubApproval = () => {}
  let approvals = [] // pending mid-turn permission requests for this chat
  let detachedChats = new Set()
  let unsubDetached = () => {}
  const TOOL_LEVELS = [
    { id: '', label: 'Safe', hint: 'read & answer only — edits/commands denied' },
    { id: 'ask', label: 'Ask', hint: 'ask you before each edit/command (claude & openclaude; other CLIs run safe)' },
    { id: 'edits', label: 'Edits', hint: 'auto-approve file edits in the chat folder' },
    { id: 'full', label: 'Full', hint: 'skip all approvals — edits AND commands' },
  ]
  const OPENCODE_TOOL_LEVELS = [
    { id: 'plan', label: 'Plan', hint: 'OpenCode plan agent — disallows edit tools' },
    { id: '', label: 'Build', hint: 'OpenCode default build agent' },
    { id: 'full', label: 'Full', hint: 'OpenCode auto-approves permission requests for this chat' },
  ]

  function isOpenCodeLikeCli(cli) {
    return cli === 'opencode' || cli === 'praimate-code'
  }

  function supportsNativeTerminalResume(cli) {
    return ['claude', 'openclaude', 'codex', 'opencode', 'praimate-code'].includes(cli)
  }

  function toolLevelsForCli(cli) {
    return isOpenCodeLikeCli(cli) ? OPENCODE_TOOL_LEVELS : TOOL_LEVELS
  }

  function normalizeToolsForCli(cli, tools) {
    return isOpenCodeLikeCli(cli) && tools !== 'plan' && tools !== 'full' ? '' : (tools || '')
  }

  // Escalation hint: when the last reply shows denied/failed tool calls
  // and the chat isn't already at Full, offer a one-click level bump —
  // the "approve and retry" affordance for CLIs without mid-turn asks.
  $: lastAssistant = [...messages].reverse().find((m) => m.Role === 'assistant')
  $: deniedTools = (lastAssistant?.Meta?.activity || []).some((t) => (t.type === 'tool' || t.tool) && t.ok === false)

  // New clean chat (CLI + model + tools, no agent)
  let creating = false
  let clis = []
  let newCli = ''
  let newModel = ''
  let newTools = ''
  let modelSuggestions = []
  let modelLoading = false
  let starting = false
  // Local LLM (Settings → Local LLM) injected into a new chat. Chats
  // route local through the full launcher machinery, so every CLI works.
  let localOpt = null // { configured, endpoint, hasApiKey, models[], error }
  let newUseLocal = false
  let newLocalModel = ''
  let mcpServers = []
  let newMCPs = []
  let newSkillChoices = null

  // Per-chat settings editor (CLI / model / tools). Works on the open
  // thread and from list rows.
  let cfg = null // {chat, cli, model, tools, local*, suggestions}
  let cfgSaving = false

  // Search across titles and message content.
  let search = ''
  let searchResults = null
  let searchTimer
  function onSearchInput() {
    clearTimeout(searchTimer)
    const q = search.trim()
    if (!q) { searchResults = null; return }
    searchTimer = setTimeout(async () => {
      try {
        searchResults = (await api.searchChats(q)) || []
      } catch (e) {
        error = String(e)
      }
    }, 250)
  }
  $: shownChats = searchResults ?? chats
  // Studio and Code sessions have dedicated tabs. A Studio transcript can
  // still be opened here explicitly, but these rows never pollute Chats.
  $: regularChats = shownChats.filter((c) => c.Settings?.surface !== 'studio' && c.Settings?.surface !== 'code' && c.Settings?.surface !== 'agent-helper' && c.Settings?.surface !== 'workflow')

  function agentName(chat) {
    if (!chat?.AgentID) return ''
    return agentNames.get(chat.AgentID) || chat.AgentID
  }

  // Reopen a code session: reattach its live PTY when possible. The Code
  // page starts and re-binds a replacement only when that process is gone.
  async function reopenCode(chat) {
    error = ''
    try {
      const l = chat.Settings?.local
      const terms = (await api.listTerminalSessions().catch(() => [])) || []
      const live = findTerminalForChat(terms, chat)
      if (live && !live.chatId) {
        await api.bindChatToTerminal(live.id, chat.ID)
        live.chatId = chat.ID
      }
      pendingTerm.set({
        termId: live?.id || '', chatId: chat.ID,
        agentId: chat.AgentID || '', agentName: agentName(chat),
        cli: chat.CLIAgent, cwd: chat.WorkspacePath,
        label: (agentName(chat) || chat.CLIAgent || 'CLI') + (l?.endpoint ? ' · local' : ''),
        model: chat.Settings?.model || '',
        localEndpoint: l?.endpoint || '', localApiKey: l?.api_key || '', localModel: l?.model || '',
        skills: chat.Settings?.skills || [],
        note: live
          ? 'reattached to the running session'
          : supportsNativeTerminalResume(chat.CLIAgent)
            ? 'previous PTY ended — resumed the most recent native session in this folder'
            : 'previous PTY ended — this CLI has no automatic native resume; started a new process with the archived transcript above',
      })
      activePage.set('code')
      pageRevision.update((n) => n + 1)
    } catch (e) {
      error = String(e)
    }
  }

  async function load() {
    try {
      const [loadedChats, loadedAgents] = await Promise.all([
        api.listChats(),
        api.listAgents().catch(() => []),
      ])
      chats = loadedChats || []
      agents = loadedAgents || []
      agentNames = new Map(agents.map((a) => [a.id, a.name || a.id]))
      error = ''
    } catch (e) {
      error = String(e)
    }
    try {
      workspaceChats = (await api.listWorkspaceChats()) || []
    } catch {
      workspaceChats = []
    }
  }

  function openConfig(chat) {
    error = ''
    // Open INSTANTLY with what we already know; the CLI availability
    // probe and model suggestions fill in asynchronously (probing 7
    // CLIs takes seconds — a dialog that waits for it reads as broken).
    cfg = {
      chat,
      name: chat.Title || '',
      cli: chat.CLIAgent,
      model: chat.Settings?.model || '',
      tools: normalizeToolsForCli(chat.CLIAgent, chat.Settings?.tools),
      localEndpoint: chat.Settings?.local?.endpoint || '',
      localApiKey: chat.Settings?.local?.api_key || '',
      localModel: chat.Settings?.local?.model || '',
      suggestions: [],
      modelLoading: true,
      mcps: (chat.Settings?.mcp_servers || []).slice(),
    }
    if (clis.length === 0) {
      api.listCLIs().then((r) => { clis = r || [] }).catch(() => {})
    }
    api.listCLIModels(chat.CLIAgent)
      .then((r) => { if (cfg && cfg.chat.ID === chat.ID) { cfg.suggestions = r || []; cfg.modelLoading = false; cfg = cfg } })
      .catch(() => { if (cfg && cfg.chat.ID === chat.ID) { cfg.modelLoading = false; cfg = cfg } })
    api.mcpServers()
      .then((r) => {
        mcpServers = (r || []).filter((s) => s.enabled)
        if (cfg && cfg.chat.ID === chat.ID) {
          const enabled = new Set(mcpServers.map((s) => s.id))
          cfg.mcps = (cfg.mcps || []).filter((id) => enabled.has(id))
          cfg = cfg
        }
      })
      .catch(() => {})
  }

  async function cfgCliChanged() {
    if (!cfg) return
    cfg.tools = normalizeToolsForCli(cfg.cli, cfg.tools)
    cfg.modelLoading = true
    cfg = cfg
    cfg.suggestions = (await api.listCLIModels(cfg.cli).catch(() => [])) || []
    cfg.modelLoading = false
    cfg = cfg
  }

  async function saveConfig() {
    if (!cfg) return
    cfgSaving = true
    error = ''
    try {
      await api.updateChatConfig(
        cfg.chat.ID, cfg.cli, cfg.model.trim(), normalizeToolsForCli(cfg.cli, cfg.tools),
        cfg.localEndpoint.trim(), cfg.localApiKey, cfg.localModel.trim())
      if (cfg.name.trim() && cfg.name.trim() !== cfg.chat.Title) {
        await api.renameChat(cfg.chat.ID, cfg.name.trim())
      }
      await api.setChatMCPServers(cfg.chat.ID, cfg.mcps || [])
      const id = cfg.chat.ID
      cfg = null
      await load()
      const fresh = chats.find((x) => x.ID === id)
      if (selected?.ID === id && fresh) {
        selected = fresh
        toolsLevel = normalizeToolsForCli(fresh.CLIAgent, fresh.Settings?.tools)
      }
    } catch (e) {
      error = String(e)
    } finally {
      cfgSaving = false
    }
  }

  async function openNewChat() {
    creating = true
    newModel = ''
    newTools = ''
    newUseLocal = false
    newLocalModel = ''
    newMCPs = []
    newSkillChoices = null
    try {
      clis = (await api.listCLIs()) || []
      const firstAvailable = clis.find((c) => c.available)
      newCli = firstAvailable ? firstAvailable.id : (clis[0]?.id ?? '')
      await refreshModels()
      try { localOpt = await api.localLLMModels() } catch { localOpt = null }
      try { mcpServers = ((await api.mcpServers()) || []).filter((s) => s.enabled) } catch { mcpServers = [] }
    } catch (e) {
      error = String(e)
    }
  }

  async function refreshModels() {
    modelSuggestions = []
    if (!newCli) return
    modelLoading = true
    try {
      modelSuggestions = (await api.listCLIModels(newCli)) || []
    } catch {
      modelSuggestions = []
    } finally {
      modelLoading = false
    }
  }

  $: selectedCliInfo = clis.find((c) => c.id === newCli)
  $: modelSupported = !!selectedCliInfo?.modelHint
  $: newToolLevels = toolLevelsForCli(newCli)
  $: if (newTools !== normalizeToolsForCli(newCli, newTools)) newTools = normalizeToolsForCli(newCli, newTools)
  // Per-chat local routing is resolved by the shared execution backend.
  $: newLocalRoutable = supportsLocalRouting(newCli)
  $: if (!newLocalRoutable && newUseLocal) newUseLocal = false

  async function startClean() {
    if (!newCli || starting) return
    starting = true
    error = ''
    try {
      const useLocalNow = newUseLocal && localOpt?.configured
      const chat = await api.startCleanChat(newCli, useLocalNow ? '' : (modelSupported ? newModel.trim() : ''), '')
      const tools = normalizeToolsForCli(newCli, newTools)
      if (useLocalNow) {
        // Route the chat at the configured local endpoint — the launcher
        // applies the per-CLI env/config when the chat runs.
        await api.updateChatConfig(chat.ID, newCli, '', tools, localOpt.endpoint, '', newLocalModel.trim())
      } else if (tools) {
        await api.setChatTools(chat.ID, tools)
      }
      if (newMCPs.length) await api.setChatMCPServers(chat.ID, newMCPs)
      if (newSkillChoices !== null) await api.saveChatSkillChoicesV2(chat.ID, newSkillChoices)
      creating = false
      await load()
      const c = chats.find((x) => x.ID === chat.ID) || chat
      await open(c)
    } catch (e) {
      error = String(e)
    } finally {
      starting = false
    }
  }

  async function openWorkspace(wc) {
    error = ''
    try {
      const res = await api.openWorkspaceChat(wc.id)
      pendingTerm.set({
        termId: res.termId,
        cli: res.cli,
        cwd: res.cwd,
        label: wc.label,
        note: res.note,
      })
      activePage.set('code')
      pageRevision.update((n) => n + 1)
    } catch (e) {
      error = String(e)
    }
  }

  async function open(chat) {
    if (detachedChats.has(chat.ID)) {
      error = 'This chat is open in a detached window. Close that window to reattach it here.'
      return
    }
    selected = chat
    toolsLevel = normalizeToolsForCli(chat.CLIAgent, chat.Settings?.tools)
    attachments = []
    approvals = []
    try {
      messages = (await api.chatMessages(chat.ID)) || []
      await scrollToBottom(true)
    } catch (e) {
      error = String(e)
    }
  }

  function back() {
    // Deny anything still pending — leaving the chat must not strand
    // the CLI waiting on an answer.
    for (const ap of approvals) api.resolveApproval(ap.id, false, false).catch(() => {})
    approvals = []
    selected = null
    messages = []
    draft = ''
    attachments = []
    openChatId.set(null)
  }

  async function setTools(level) {
    if (!selected) return
    try {
      const next = normalizeToolsForCli(selected.CLIAgent, level)
      await api.setChatTools(selected.ID, next)
      toolsLevel = next
      if (selected.Settings) selected.Settings.tools = next
    } catch (e) {
      error = String(e)
    }
  }

  async function attach() {
    if (!selected) return
    try {
      const picked = (await api.pickChatAttachments(selected.ID)) || []
      attachments = [...attachments, ...picked]
    } catch (e) {
      error = String(e)
    }
  }

  function unattach(path) {
    attachments = attachments.filter((a) => a.path !== path)
  }

  function handleStreamEvent(ev) {
    if (!sending || !selected || ev.chatId !== selected.ID) return
    if (!stream) stream = { text: '', tools: [], reasoning: [], steps: [] }
    if (ev.type === 'text') {
      stream.text += ev.text
    } else if (ev.type === 'reasoning') {
      stream.reasoning = [...(stream.reasoning || []), ev.text]
    } else if (ev.type === 'step_start' || ev.type === 'step_finish' || ev.type === 'error') {
      stream.steps = [...(stream.steps || []), { type: ev.type, detail: ev.detail, ok: ev.type !== 'error' && ev.ok !== false }]
    } else if (ev.type === 'tool_start') {
      stream.tools = [...stream.tools, { id: ev.id || '', tool: ev.tool, detail: ev.detail, done: false, ok: true }]
    } else if (ev.type === 'tool_end') {
      const t = [...stream.tools]
      let idx = ev.id ? t.findIndex((x) => x.id === ev.id && !x.done) : -1
      if (idx < 0) idx = t.findIndex((x) => !x.done)
      if (idx >= 0) t[idx] = { ...t[idx], done: true, ok: ev.ok }
      stream.tools = t
    }
    stream = stream
    scrollToBottom()
  }

  async function stop() {
    if (!selected) return
    // Immediate UI feedback — the backend cancellation can lag while
    // the wrapped CLI tears down its own process tree; users don't
    // want to wait for that to see the spinner disappear.
    sending = false
    stream = null
    try { await api.cancelChatTurn(selected.ID) } catch {}
  }

  function handleApproval(req) {
    // The detached child is subscribed before DetachSession resolves. Do not
    // fail-close an approval that belongs to that active renderer.
    if (detachedChats.has(req.chatId)) return
    if (!selected || req.chatId !== selected.ID) {
      // Approval for a chat that isn't open — fail closed immediately
      // rather than letting it hang until the broker timeout.
      api.resolveApproval(req.id, false, false).catch(() => {})
      return
    }
    approvals = [...approvals, req]
    scrollToBottom()
  }

  async function answerApproval(req, allow, always) {
    approvals = approvals.filter((a) => a.id !== req.id)
    try { await api.resolveApproval(req.id, allow, always) } catch (e) { error = String(e) }
  }

  async function send() {
    const text = draft.trim()
    if ((!text && attachments.length === 0) || sending || !selected) return
    const chatID = selected.ID
    sending = true
    error = ''
    stream = null
    const isCommand = text.startsWith('!')
    const staged = attachments
    // Optimistically show the user's message.
    messages = [...messages, { Role: 'user', Content: text, TS: new Date().toISOString(), _pending: true }]
    draft = ''
    attachments = []
    await scrollToBottom(true)
    try {
      if (isCommand) {
        // "!cmd" runs locally in the chat folder — never sent to the model.
        await api.runChatCommand(chatID, text.slice(1))
      } else {
        // Streams live events for CLIs that support it; others resolve
        // in one shot. Interrupting (Stop) resolves with the partial.
        await api.sendChatStream(chatID, text, staged.map((a) => a.path))
      }
      // Replace the optimistic copy with the persisted pair.
      if (selected?.ID === chatID) messages = (await api.chatMessages(chatID)) || messages
      if (selected?.ID === chatID) {
        const refreshed = ((await api.listChats().catch(() => [])) || []).find((chat) => chat.ID === chatID)
        if (refreshed) {
          selected = refreshed
          showSkillDeliveryToast(refreshed.Settings?.skill_runtime, 'Chat')
        }
      }
    } catch (e) {
      error = String(e)
      try {
        if (selected?.ID === chatID) messages = (await api.chatMessages(chatID)) || messages.filter((m) => !m._pending)
      } catch {
        messages = messages.filter((m) => !m._pending)
      }
      attachments = staged
    } finally {
      sending = false
      stream = null
      approvals = [] // turn is over; anything unanswered was denied/cancelled
      await scrollToBottom()
    }
  }

  async function detachChat() {
    if (!selected) return
    if (approvals.length > 0) {
      error = 'Answer the pending approval before detaching this chat.'
      return
    }
    const chat = selected
    error = ''
    try {
      await api.detachSession('chat', chat.ID, chat.Title || 'Chat')
      detachedChats = new Set([...detachedChats, chat.ID])
      // Release only this renderer. The authoritative turn, cancel function,
      // and transcript stay in the main Go process.
      selected = null
      messages = []
      draft = ''
      attachments = []
      approvals = []
      sending = false
      stream = null
      openChatId.set(null)
    } catch (e) {
      error = String(e)
    }
  }

  function onKey(e) {
    // Enter may be part of an IME composition (accented/non-Latin input), not
    // an intent to send the message.
    if (e.isComposing || e.keyCode === 229) return
    // Enter sends; Shift+Enter inserts a newline.
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      send()
    }
  }

  async function remove(chat) {
    if (!confirm(`Delete chat "${chat.Title}"? This removes its messages too.`)) return
    try {
      await api.deleteChat(chat.ID)
      if (selected?.ID === chat.ID) back()
      await load()
    } catch (e) {
      error = String(e)
    }
  }

  let userScrolledUp = false

  function onThreadScroll() {
    if (!threadEl) return
    const distanceFromBottom = threadEl.scrollHeight - threadEl.scrollTop - threadEl.clientHeight
    userScrolledUp = distanceFromBottom > 100
  }

  async function scrollToBottom(force = false) {
    if (force) userScrolledUp = false
    await tick()
    if (threadEl && (force || !userScrolledUp)) {
      threadEl.scrollTop = threadEl.scrollHeight
    }
  }

  function fmtDate(s) {
    try { return new Date(s).toLocaleString() } catch { return s }
  }

  function baseName(p) {
    return String(p).split(/[\\/]/).pop()
  }

  // Studio sends append a focused-file context block for the model —
  // hide it when showing the transcript to the human.
  function cleanMsg(s) {
    return String(s).replace(/\n*\[The user is looking at:[^\]]*\]\s*$/, '')
  }

  function activityTitle(activity) {
    const n = activity?.length || 0
    return `Activity · ${n} event${n === 1 ? '' : 's'}`
  }

  function activityStatus(t) {
    if (t.type === 'reasoning') return '?'
    if (t.type === 'step_start') return '◌'
    if (t.type === 'step_finish') return '✓'
    if (t.type === 'error' || t.ok === false) return '✗'
    return '✓'
  }

  function activityName(t) {
    if (t.type === 'reasoning') return 'reasoning'
    if (t.type === 'step_start') return 'step'
    if (t.type === 'step_finish') return 'step done'
    if (t.type === 'error') return 'error'
    return t.tool || t.type || 'tool'
  }

  function activityDetail(t) {
    return t.type === 'reasoning' ? t.text : t.detail
  }

  function isImg(p) {
    return /\.(png|jpe?g|gif|webp|bmp|svg)$/i.test(String(p))
  }

  onMount(async () => {
    unsubStream = onChatStream(handleStreamEvent)
    unsubApproval = onApproval(handleApproval)
    try { detachedChats = new Set(((await api.detachedWindows()) || []).filter((item) => item.kind === 'chat').map((item) => item.sessionId)) } catch {}
    if (window.runtime?.EventsOn) {
      window.runtime.EventsOn('praimate:detached-windows', (items) => {
        detachedChats = new Set((items || []).filter((item) => item.kind === 'chat').map((item) => item.sessionId))
      })
      unsubDetached = () => window.runtime.EventsOff('praimate:detached-windows')
    }
    await load()
    // If Agents started a chat and routed us here, open it.
    const id = $openChatId
    if (id) {
      const c = chats.find((x) => x.ID === id)
      if (c) await open(c)
    }
  })

  onDestroy(() => {
    unsubStream()
    unsubApproval()
    unsubDetached()
  })
</script>

{#if cfg}
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <div class="picker-backdrop" on:click={() => (cfg = null)}>
    <div class="picker" on:click|stopPropagation role="dialog" style="max-width:640px; max-height:90vh; overflow-y:auto; display:flex; flex-direction:column;">
      <div class="picker-head">
        <strong class="grow">Chat settings — {cfg.chat.Title}</strong>
        <button class="picker-x" on:click={() => (cfg = null)}>×</button>
      </div>
      <div class="picker-body grow" style="padding:16px;">
        <div class="card-sub" style="margin-bottom:12px;">Switching the CLI starts a fresh session on the next message; the history stays.</div>

    <label class="lbl">Session Name</label>
    <input class="field" style="max-width:320px; margin-bottom:12px" bind:value={cfg.name} />

    <label class="lbl">CLI</label>
    <select class="field" style="max-width:320px" bind:value={cfg.cli} on:change={cfgCliChanged}>
      {#if clis.length === 0}
        <option value={cfg.cli}>{cfg.cli} (probing CLIs…)</option>
      {/if}
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
        <input class="field mono" style="max-width:420px" list="cfg-local-models" bind:value={cfg.localModel} placeholder="model on your endpoint" />
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

    {#key cfg.chat.ID}
      <SkillBindingsEditor chatID={cfg.chat.ID} on:saved={(e) => { if (e.detail.config) cfg.skills = [] }} />
    {/key}
    {#if cfg.chat.Settings?.skill_runtime}
      {@const runtime = cfg.chat.Settings.skill_runtime}
      <div class="card-sub" role="status" style="margin-top:8px">
        Skills: {runtime.status || 'unknown'} · {runtime.coverage || 'unknown'} · epoch {runtime.context_epoch || 0}
        {#if runtime.delivered?.length} · {runtime.delivered.length} controlled block{runtime.delivered.length === 1 ? '' : 's'}{/if}
        {#if runtime.status === 'prepared'} — pending delivery{/if}
        {#if runtime.error_code} — {runtime.error_code}{/if}
      </div>
    {/if}
    <label class="lbl" style="margin-top:10px">MCP servers <span class="card-sub" style="font-weight:400">— exposed only to this chat.</span></label>
    {#if mcpServers.length === 0}
      <div class="card-sub">No enabled MCP servers. Connect and enable one on the MCP page first.</div>
    {:else}
      <div class="mcp-grid">
        {#each mcpServers as server}
          <label class="mcp-choice">
            <input
              type="checkbox"
              checked={cfg.mcps?.includes(server.id)}
              on:change={(e) => {
                cfg.mcps = e.currentTarget.checked
                  ? [...(cfg.mcps || []), server.id]
                  : (cfg.mcps || []).filter((id) => id !== server.id)
                cfg = cfg
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

{#if selected}
  <div class="row" style="margin-bottom: 12px">
    <button class="btn" on:click={back}>← Chats</button>
    <div class="grow">
      <strong>{selected.Title}</strong>
      {#if selected.AgentID}<span class="pill">Agent: {agentName(selected)}</span>{/if}
      <span class="pill">{selected.CLIAgent}</span>
      {#if selected.Settings?.model}<span class="pill">{selected.Settings.model}</span>{/if}
      {#if selected.ExitKind}<span class="pill" class:ok={selected.ExitKind === 'completed'}>{selected.ExitKind}</span>{/if}
      {#if selected.Settings?.skill_runtime}
        {@const runtime = selected.Settings.skill_runtime}
        <span class="pill" title="PrAImate payload evidence; this does not prove the model followed the skill">
          Skills: {runtime.status || 'unknown'}
          {#if runtime.delivered?.length} · {runtime.delivered.map((skill) => skill.ref).join(', ')}{/if}
        </span>
      {/if}
    </div>
    <div class="toolpick" title="How much the CLI agent may do: edit files, run commands">
      <span class="lbl-inline">Tools</span>
      {#each toolLevelsForCli(selected.CLIAgent) as lvl}
        <button
          class="btn sm"
          class:primary={toolsLevel === lvl.id}
          title={lvl.hint}
          on:click={() => setTools(lvl.id)}>{lvl.label}</button>
      {/each}
    </div>
    <button class="btn" on:click={() => openConfig(selected)} title="Change the CLI / model / tools behind this chat">⚙ Edit</button>
    <button class="btn danger" on:click={() => remove(selected)}>Delete</button>
  </div>

  {#if error}<div class="banner">{error}</div>{/if}

  <div class="thread" bind:this={threadEl} on:scroll={onThreadScroll}>
    {#if messages.length === 0}
      <div class="empty">No messages yet — say something below to begin.</div>
    {/if}
    {#each messages as m}
      {#if m.Role === 'command'}
        <div class="msg command" class:pending={m._pending}>
          <div class="who">shell{m.TS ? ' · ' + fmtDate(m.TS) : ''}</div>
          <pre class="cmd-out">{m.Content}</pre>
        </div>
      {:else}
        <div class="msg {m.Role === 'user' ? 'user' : 'assistant'}" class:pending={m._pending}>
          <div class="who">{m.Role}{m.TS ? ' · ' + fmtDate(m.TS) : ''}{m.Meta?.interrupted ? ' · interrupted' : ''}</div>
          <!-- content rendered below; studio context block stripped -->
          {#if m.Meta?.activity?.length}
            <details class="activity-block">
              <summary>{activityTitle(m.Meta.activity)}</summary>
              <div class="tool-feed">
                {#each m.Meta.activity as t}
                  <div class="tool-row" class:err={t.ok === false || t.type === 'error'} class:reasoning-row={t.type === 'reasoning'}>
                    <span class="tool-status">{activityStatus(t)}</span>
                    <span class="tool-name">{activityName(t)}</span>
                    {#if activityDetail(t)}<span class="tool-detail mono" class:reasoning-detail={t.type === 'reasoning'}>{activityDetail(t)}</span>{/if}
                  </div>
                {/each}
              </div>
            </details>
          {/if}
          {#if m.Role === 'user'}
            {cleanMsg(m.Content)}
          {:else}
            <div class="markdown">{@html renderMarkdown(cleanMsg(m.Content))}</div>
          {/if}
          {#if m.Meta?.attachments}
            <div class="att-row">
              {#each m.Meta.attachments as path}
                {#if isImg(path)}
                  {#await api.attachmentDataURL(path) then url}
                    <img class="att-img" src={url} alt={baseName(path)} title={path} />
                  {:catch}
                    <span class="pill" title={path}>🖼 {baseName(path)}</span>
                  {/await}
                {:else}
                  <span class="pill" title={path}>📄 {baseName(path)}</span>
                {/if}
              {/each}
            </div>
          {/if}
        </div>
      {/if}
    {/each}
    {#if sending}
      <div class="msg assistant">
        <div class="who">assistant</div>
        {#if stream?.reasoning?.length}
          <div class="tool-feed reasoning-live">
            {#each stream.reasoning as r}
              <div class="tool-row reasoning-row"><span class="tool-status">?</span><span class="tool-name">reasoning</span><span class="tool-detail reasoning-detail">{r}</span></div>
            {/each}
          </div>
        {/if}
        {#if stream?.steps?.length}
          <div class="tool-feed">
            {#each stream.steps as s}
              <div class="tool-row" class:err={!s.ok}><span class="tool-status">{s.ok ? '◌' : '✗'}</span><span class="tool-name">{s.type === 'error' ? 'error' : s.type === 'step_finish' ? 'step done' : 'step'}</span>{#if s.detail}<span class="tool-detail mono">{s.detail}</span>{/if}</div>
            {/each}
          </div>
        {/if}
        {#if stream?.tools?.length}
          <div class="tool-feed">
            {#each stream.tools as t}
              <div class="tool-row" class:err={t.done && !t.ok}>
                <span class="tool-status">{t.done ? (t.ok ? '✓' : '✗') : '◌'}</span>
                <span class="tool-name">{t.tool}</span>
                {#if t.detail}<span class="tool-detail mono">{t.detail}</span>{/if}
              </div>
            {/each}
          </div>
        {/if}
        {#if stream?.text}
          <div class="markdown">{@html renderMarkdown(stream.text)}</div><span class="cursor">▍</span>
        {:else}
          <span class="typing">…thinking</span>
        {/if}
      </div>
    {/if}
    {#each approvals as ap (ap.id)}
      <div class="approval-card">
        <div class="approval-head">⚠ The agent asks permission to use <strong>{ap.tool}</strong></div>
        {#if ap.detail}<div class="approval-detail mono">{ap.detail}</div>{/if}
        <div class="row" style="margin-top:8px">
          <button class="btn primary" on:click={() => answerApproval(ap, true, false)}>Allow once</button>
          <button class="btn" on:click={() => answerApproval(ap, true, true)}>Always allow “{ap.tool}” here</button>
          <button class="btn danger" on:click={() => answerApproval(ap, false, false)}>Deny</button>
        </div>
      </div>
    {/each}
  </div>

  {#if deniedTools && !sending && toolsLevel !== 'full'}
    <div class="escalate">
      Some tool calls were denied or failed on the last reply.
      {#if selected.CLIAgent === 'claude' || selected.CLIAgent === 'openclaude'}
        Switch to <button class="btn sm" on:click={() => setTools('ask')}>Ask</button> to approve them live, or
      {/if}
      {isOpenCodeLikeCli(selected.CLIAgent) ? 'switch to Full access and ask again:' : 'raise the level and ask again:'}
      {#if !isOpenCodeLikeCli(selected.CLIAgent)}<button class="btn sm" on:click={() => setTools('edits')}>Allow edits</button>{/if}
      <button class="btn sm" on:click={() => setTools('full')}>Full access</button>
    </div>
  {/if}

  {#if attachments.length > 0}
    <div class="att-row" style="margin-bottom: 8px">
      {#each attachments as a}
        <span class="pill" title={a.path}>
          {a.image ? '🖼' : '📄'} {a.name}
          <button class="chip-x" on:click={() => unattach(a.path)} title="Remove">×</button>
        </span>
      {/each}
    </div>
  {/if}
  <div class="composer">
    <button class="btn" on:click={attach} disabled={sending} title="Attach images, PDFs or documents — the agent reads them from disk">📎</button>
    <textarea
      class="field"
      rows="2"
      placeholder="Message the agent…  (Enter sends · Shift+Enter newline · !cmd runs a shell command in the chat folder)"
      bind:value={draft}
      on:keydown={onKey}
      disabled={sending}></textarea>
    <button class="btn" on:click={detachChat} disabled={approvals.length > 0} title="Move this chat into its own window">Detach</button>
    {#if sending}
      <button class="btn danger" on:click={stop} title="Interrupt the turn — text streamed so far is kept">■ Stop</button>
    {:else}
      <button class="btn primary" on:click={send} disabled={!draft.trim() && attachments.length === 0}>Send</button>
    {/if}
  </div>
{:else}
  <div class="row" style="margin-bottom: 4px">
    <h1 class="grow" style="margin:0">Chats</h1>
    <button class="btn primary" on:click={openNewChat}>+ New chat</button>
  </div>
  <p class="subtitle">Conversations persist to the shared database. Start a clean chat on any CLI/model, or use an agent persona from the Agents page.</p>

  <input
    class="field"
    style="max-width:420px; margin-bottom:10px"
    placeholder="Search chats (titles and message content)…"
    bind:value={search}
    on:input={onSearchInput} />
  {#if searchResults !== null}
    <div class="card-sub" style="margin-bottom:8px">{regularChats.length} chat match(es) for “{search.trim()}”</div>
  {/if}

  {#if error}<div class="banner">{error}</div>{/if}

  {#if creating}
    <div class="card">
      <div class="card-title">New clean chat</div>
      <div class="card-sub">A plain conversation with the CLI — no agent persona. Pick the CLI and (optionally) pin the model it should use.</div>
      <label class="lbl">CLI</label>
      <select class="field" style="max-width:320px" bind:value={newCli} on:change={refreshModels}>
        {#if clis.length === 0}
          <option value="">probing installed CLIs…</option>
        {/if}
        {#each clis as c}
          <option value={c.id} disabled={!c.available}>
            {c.label}{c.available ? '' : ' — not installed'}
          </option>
        {/each}
      </select>
      {#if localOpt?.configured && newLocalRoutable}
        <label class="row" style="margin-top:12px; gap:8px; cursor:pointer">
          <input type="checkbox" bind:checked={newUseLocal} />
          <span>Use the local LLM from Settings <span class="card-sub mono">{localOpt.endpoint}</span></span>
        </label>
      {:else if localOpt?.configured}
        <div class="card-sub" style="margin-top:10px">{localRoutingUnavailableMessage(newCli)}</div>
      {/if}

      {#if newUseLocal && localOpt?.configured && newLocalRoutable}
        <label class="lbl">Local model</label>
        <input
          class="field mono"
          style="max-width:420px"
          list="local-model-suggestions"
          placeholder="model on your endpoint (e.g. llama3.1, qwen2.5-coder)"
          bind:value={newLocalModel} />
        <datalist id="local-model-suggestions">
          {#each localOpt.models || [] as m}<option value={m}></option>{/each}
        </datalist>
        {#if localOpt.error}<div class="card-sub" style="color: var(--warn)">Couldn't list models from the endpoint: {localOpt.error}. You can still type a model name.</div>{/if}
      {:else}
        <label class="lbl">Model {modelSupported ? `(${selectedCliInfo.modelHint})` : '(this CLI has no model flag — it uses its own config)'}</label>
        <input
          class="field mono"
          style="max-width:420px"
          list="model-suggestions"
          placeholder={modelSupported ? 'blank = CLI default' : 'not supported'}
          bind:value={newModel}
          disabled={!modelSupported} />
        <datalist id="model-suggestions">
          {#each modelSuggestions as m}<option value={m}></option>{/each}
        </datalist>
        {#if modelLoading}<div class="card-sub">Loading models...</div>{/if}
      {/if}
      <label class="lbl">Tools</label>
      <div class="row">
        {#each newToolLevels as lvl}
          <button class="btn sm" class:primary={newTools === lvl.id} title={lvl.hint} on:click={() => (newTools = lvl.id)}>{lvl.label}</button>
        {/each}
      </div>
      <label class="lbl">MCP servers <span class="card-sub">(optional, per chat)</span></label>
      {#if mcpServers.length === 0}
        <div class="card-sub">No enabled MCP servers. Add one on the MCP page first.</div>
      {:else}
        <div class="mcp-grid">
          {#each mcpServers as server}
            <label class="mcp-choice">
              <input type="checkbox" value={server.id} bind:group={newMCPs} />
              <span><strong>{server.name}</strong> <span class="card-sub">{server.transport}</span></span>
            </label>
          {/each}
        </div>
      {/if}
      <SkillChoiceDraft bind:choices={newSkillChoices} />
      <div class="row" style="margin-top:12px">
        <button class="btn primary" on:click={startClean} disabled={starting || !newCli}>
          {starting ? 'Starting…' : 'Start chat'}
        </button>
        <button class="btn" on:click={() => (creating = false)}>Cancel</button>
      </div>
    </div>
  {/if}

  {#if regularChats.length === 0 && !creating}
    <div class="empty">{searchResults !== null ? 'No chats match the search.' : 'No chats yet — press “New chat”, or start one from an agent on the Agents page.'}</div>
  {/if}
  {#each regularChats as chat}
    <div class="card row">
      <div class="grow" style="cursor:pointer" on:click={() => open(chat)} on:keydown={(e) => e.key === 'Enter' && open(chat)} role="button" tabindex="0">
        <div class="card-title">{chat.Title}</div>
        <div class="card-sub">
          {chat.CLIAgent}
          {#if detachedChats.has(chat.ID)} · detached window{/if}
          {#if chat.AgentID} · Agent: {agentName(chat)}{/if}
          {#if chat.Settings?.model} · model: {chat.Settings.model}{/if}
          · {fmtDate(chat.UpdatedAt)}
          {#if chat.ExitKind} · {chat.ExitKind}{/if}
        </div>
      </div>
      <button class="btn" on:click={() => open(chat)} disabled={detachedChats.has(chat.ID)}>{detachedChats.has(chat.ID) ? 'Detached' : 'Open'}</button>
      <button class="btn" on:click={() => openConfig(chat)} title="Change CLI / model / tools">Edit</button>
      <button class="btn danger" on:click={() => remove(chat)}>Delete</button>
    </div>
  {/each}

  {/if}

<style>
  .thread {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    padding: 14px;
    /* header row (~48px) + composer (~60px) + page padding: the thread
       takes everything else so there's no dead gap under the composer. */
    height: calc(100vh - 172px);
    overflow-y: auto;
    margin-bottom: 12px;
  }
  .composer {
    display: flex;
    gap: 8px;
    align-items: flex-end;
  }
  .composer textarea { resize: none; }
  .msg.pending { opacity: 0.6; }
  .typing { color: var(--text-dim); font-style: italic; }
  .toolpick { display: flex; align-items: center; gap: 4px; }
  .lbl-inline { color: var(--text-dim); font-size: 12px; margin-right: 2px; }
  .btn.sm { padding: 3px 10px; font-size: 12px; }
  .msg.command { background: var(--bg); border: 1px dashed var(--border); }
  .cmd-out {
    margin: 0;
    font-family: var(--mono, ui-monospace, monospace);
    font-size: 12px;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .att-row { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
  .att-img {
    max-width: 260px;
    max-height: 180px;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    display: block;
  }
  .chip-x {
    background: none;
    border: none;
    color: var(--text-dim);
    cursor: pointer;
    font-size: 13px;
    padding: 0 0 0 4px;
  }
  .chip-x:hover { color: var(--text); }
  .tool-feed {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin: 4px 0 8px;
    padding: 6px 8px;
    border-left: 2px solid var(--border);
    font-size: 12px;
    color: var(--text-dim);
  }
  .activity-block { margin: 4px 0 8px; }
  .activity-block summary { cursor: pointer; color: var(--text-dim); font-size: 12px; user-select: none; }
  .activity-block .tool-feed { margin-bottom: 0; }
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
  .cursor { animation: blink 1s steps(1) infinite; }
  @keyframes blink { 50% { opacity: 0; } }
  .approval-card {
    border: 1px solid var(--warning, #d4a72c);
    border-radius: var(--radius);
    background: var(--panel);
    padding: 10px 12px;
    margin: 8px 0;
  }
  .approval-head { font-size: 13px; }
  .approval-detail {
    margin-top: 6px;
    font-size: 12px;
    color: var(--text-dim);
    word-break: break-all;
  }
  .escalate {
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    padding: 8px 10px;
    margin-bottom: 8px;
    font-size: 12px;
    color: var(--text-dim);
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
  }
  .mcp-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
    gap: 6px;
    margin-top: 5px;
  }
  .mcp-choice {
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 7px 9px;
    border: 1px solid var(--border);
    border-radius: 8px;
    cursor: pointer;
    font-size: 12px;
  }
  .mcp-choice:hover { background: var(--bg-raised); }
</style>
