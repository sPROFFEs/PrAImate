<script>
  // SessionPanel — a flyout listing every open chat/Studio/Code session
  // the user currently has, with a live indicator for chats that are
  // mid-stream. Click an item to jump back to it without losing the
  // running conversation. Triggered from the sidebar's "⚡ Sessions"
  // button.
  //
  // Cross-process Studio windows aren't directly focusable from this
  // process (Wails v2 = one window per process), so clicking a Studio
  // session re-opens the editor window for its folder + chat — if the
  // existing window is still alive, the OS just gives the user two; if
  // it crashed, this brings it back. Trade-off documented in the
  // tooltip.
  import { onMount, onDestroy } from 'svelte'
  import { floatingPanel } from './floatingPanel.js'
  import { api } from './api.js'
  import { showConfirm } from './stores.js'
  import { term, findTerminalForChat } from './terminal.js'
  import { activePage, pageRevision, openChatId, pendingTerm } from './stores.js'

  let chats = []
  let agents = []
  let agentNames = new Map()
  let active = new Set()
  let liveTerms = new Map() // chatID → termID (live PTY we can resume)
  let open = false
  let loading = false
  let timer = null
  let trigger
  let error = ''
  let deleting = false

  async function load() {
    if (loading) return
    loading = true
    try {
      const [cs, ids, terms, loadedAgents] = await Promise.all([
        api.listChats(),
        api.activeChatIDs().catch(() => []),
        api.listTerminalSessions().catch(() => []),
        api.listAgents().catch(() => []),
      ])
      chats = (cs || []).filter(c => c.Settings?.surface !== 'workers' && c.Settings?.surface !== 'workflow')
      agents = loadedAgents || []
      agentNames = new Map(agents.map((a) => [a.id, a.name || a.id]))
      active = new Set(ids || [])
      const m = new Map()
      for (const t of (terms || [])) if (t.chatId) m.set(t.chatId, t)
      // Heal sessions created by older builds where a quick navigation could
      // leave the PTY unbound. The fallback is deliberately unique-by-folder
      // and CLI, so it cannot steal another chat's running process.
      for (const c of chats) {
        if (surfaceOf(c) !== 'code' || m.has(c.ID)) continue
        const recovered = findTerminalForChat(terms, c)
        if (!recovered) continue
        try { await api.bindChatToTerminal(recovered.id, c.ID) } catch { continue }
        recovered.chatId = c.ID
        m.set(c.ID, recovered)
      }
      liveTerms = m
    } catch (e) {
      error = String(e)
    } finally {
      loading = false
    }
  }

  function surfaceOf(c) {
    const s = c?.Settings?.surface || ''
    if (s === 'studio') return 'studio'
    if (s === 'code') return 'code'
    if (s === 'agent-helper') return 'helper'
    return 'chat'
  }

  function surfaceLabel(s) {
    return s === 'studio' ? 'Studio' : s === 'code' ? 'Code' : s === 'helper' ? 'Agent helper' : 'Chat'
  }

  function agentName(c) {
    if (!c?.AgentID) return ''
    return agentNames.get(c.AgentID) || c.AgentID
  }

  function supportsNativeTerminalResume(cli) {
    return ['claude', 'openclaude', 'codex', 'opencode', 'praimate-code', 'praimate-cli'].includes(cli)
  }

  function fmtAgo(iso) {
    if (!iso) return ''
    const t = new Date(iso).getTime()
    if (Number.isNaN(t)) return ''
    const ms = Date.now() - t
    const s = Math.round(ms / 1000)
    if (s < 60) return `${s}s ago`
    const m = Math.round(s / 60)
    if (m < 60) return `${m}m ago`
    const h = Math.round(m / 60)
    if (h < 24) return `${h}h ago`
    return `${Math.round(h / 24)}d ago`
  }

  async function deleteSession(c) {
    await api.cancelChatTurn(c.ID)
    const live = liveTerms.get(c.ID)
    if (live) await term.close(live.id)
    await api.deleteChat(c.ID)
    if ($openChatId === c.ID) openChatId.set('')
    pageRevision.update(n => n + 1)
  }

  async function closeSession(c) {
    if (deleting) return
    deleting = true
    try {
      const ok = await showConfirm({
        title: 'Delete session?',
        message: `Delete "${c.Title || c.ID}" from PrAImate? This stops its running reply or terminal and permanently deletes the saved conversation and messages throughout the application. It does not just remove this item from the panel. Project files are kept. This cannot be undone.`,
        confirmLabel: 'Delete session'
      })
      if (!ok) return
      error = ''
      await deleteSession(c)
      await load()
    } catch (e) { error = String(e) }
    finally { deleting = false }
  }

  async function closeAllSessions() {
    if (deleting || !chats.length) return
    const sessions = [...chats]
    deleting = true
    try {
      const ok = await showConfirm({
        title: 'Delete all listed sessions?',
        message: `Permanently delete all ${sessions.length} listed conversations and their messages throughout PrAImate? Running replies and terminals will stop. This includes saved sessions that are not running. Project files are kept. This cannot be undone.`,
        confirmLabel: 'Delete all sessions'
      })
      if (!ok) return
      error = ''
      for (const c of sessions) await deleteSession(c)
    } catch (e) { error = String(e) }
    finally { await load(); deleting = false }
  }

  async function jump(c) {
    open = false
    const s = surfaceOf(c)
    if (s === 'studio') {
      // Re-open the Studio window for this chat's folder. If the
      // original window is still alive the OS will surface a second
      // — Wails v2 has no cross-process focus primitive on Linux.
      try {
        await api.openEditorWindow(c.WorkspacePath || '', c.AgentID || '', c.CLIAgent || '', c.Settings?.model || '', c.ID, c.Settings?.local?.endpoint || '', c.Settings?.local?.api_key || '', c.Settings?.local?.model || '')
      } catch (e) { console.error(e) }
      return
    }
    if (s === 'code') {
      // Code sessions live on the Code page. If we have a live PTY
      // for this chat, reattach to its existing stream — no second
      // process. If not (GUI was restarted or the process died), use
      // that CLI's native resume command in the same folder. The presence of a
      // live term is also what the green "live" dot in the row
      // signals to the user.
      const live = liveTerms.get(c.ID)
      pendingTerm.set({
        termId: live ? live.id : '',
        chatId: c.ID,
        agentId: c.AgentID || '',
        agentName: agentName(c),
        cli: c.CLIAgent || '',
        cwd: c.WorkspacePath || '',
        label: c.Title || '',
        model: c.Settings?.model || '',
        localEndpoint: c.Settings?.local?.endpoint || '',
        localApiKey: c.Settings?.local?.api_key || '',
        localModel: c.Settings?.local?.model || '',
        note: live
          ? ''
          : supportsNativeTerminalResume(c.CLIAgent)
            ? 'previous PTY ended — resumed the most recent native session in this folder'
            : 'previous PTY ended — this CLI has no automatic native resume; started a new process with the archived transcript above',
      })
      activePage.set('code')
      pageRevision.update((n) => n + 1)
      return
    }
    openChatId.set(c.ID)
    activePage.set('chats')
  }

  onMount(() => {
    load()
    // Refresh every 3s while the panel is open so the live indicator
    // tracks streams without the user hammering a refresh button.
    timer = setInterval(() => { if (open && !document.hidden) load() }, 3000)
  })
  onDestroy(() => clearInterval(timer))

  function toggle() {
    open = !open
    if (open && !document.hidden) load()
  }
</script>

<div class="wrap">
  <button bind:this={trigger} aria-expanded={open} aria-controls="session-flyout" class="sess-btn" title="Saved and running sessions" on:click={toggle} class:on={open}>
    <span class="dot" class:live={active.size > 0}></span>
    <span class="lbl">Sessions{active.size ? ` · ${active.size} live` : ''}</span>
  </button>

  {#if open}
    <div class="sheet" id="session-flyout" role="dialog" aria-label="Sessions" tabindex="-1" use:floatingPanel={{ anchor: trigger, close: () => open = false }}>
      <div class="sheet-head">
        <strong class="grow">Sessions</strong>
        <button class="close-all" title="Delete all listed sessions and their saved history" on:click={closeAllSessions} disabled={loading || deleting || chats.length === 0}>Delete all</button>
        <button class="x" title="Refresh" on:click={load} disabled={loading}>↻</button>
        <button class="x" title="Close panel" aria-label="Close sessions panel" on:click={() => (open = false)}>×</button>
      </div>
      {#if error}<div class="panel-error" role="alert">{error}</div>{/if}
      <div class="panel-hint">Saved conversations and running terminals. Deleting a session removes its history from PrAImate.</div>
      {#if chats.length === 0}
        <div class="empty">No chats yet — start one from the Code, Chats, or Agents page.</div>
      {/if}
      {#each chats as c}
        {@const s = surfaceOf(c)}
        {@const liveStream = active.has(c.ID)}
        {@const liveTerm = s === 'code' && liveTerms.has(c.ID)}
        <div class="row" role="presentation">
          <button class="row-main grow" on:click={() => jump(c)} title={liveTerm ? `${c.Title} (PTY running — click to reattach)` : `Jump to ${c.Title}`}>
            <span class="pulse" class:live={liveStream || liveTerm}></span>
            <span class="surf surf-{s}">{surfaceLabel(s)}</span>
            <span class="title grow">{c.Title || c.ID}</span>
            <span class="meta">{#if c.AgentID}Agent: {agentName(c)} · {/if}{c.CLIAgent || ''} · {fmtAgo(c.UpdatedAt || c.CreatedAt)}</span>
          </button>
          <button class="row-close" disabled={deleting} aria-label={`Delete session ${c.Title || c.ID}`} title="Delete session and saved history" on:click|stopPropagation={() => closeSession(c)}>×</button>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .wrap { position: relative; }
  .sess-btn {
    display: inline-flex; align-items: center; gap: 8px;
    background: none; border: 1px solid transparent;
    color: var(--text); cursor: pointer;
    padding: 5px 10px; font-size: 12px; border-radius: 8px;
    width: 100%; text-align: left;
  }
  .sess-btn:hover, .sess-btn.on {
    background: var(--bg-raised, rgba(255,255,255,0.06));
    border-color: var(--border);
  }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--text-dim); flex: none; }
  .dot.live { background: #4ec9b0; box-shadow: 0 0 0 0 rgba(78,201,176,0.5); animation: pulse 1.6s infinite; }
  .lbl { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

  .sheet {
    position: fixed;
    width: min(540px, calc(100vw - 24px));
    max-height: min(640px, calc(100dvh - 24px));
    overflow-y: auto;
    background: var(--bg-raised, var(--bg-panel));
    border: 1px solid var(--border-bright, var(--border));
    border-radius: 10px;
    box-shadow: var(--shadow-overlay, 0 12px 36px rgba(0,0,0,0.25));
    z-index: 19000;
  }
  .panel-error { padding: 12px; color: var(--err); overflow-wrap: anywhere; }
  .panel-hint { padding: 10px 12px; font-size: 11px; color: var(--text-dim); border-bottom: 1px solid var(--border); }
  .sheet-head { position: sticky; top: 0; background: var(--bg-raised, var(--bg-panel)); z-index: 1; display: flex; gap: 6px; align-items: center; padding: 8px 10px; border-bottom: 1px solid var(--border); }
  .sheet-head .x { background: none; border: none; color: var(--text-dim); cursor: pointer; padding: 2px 8px; font-size: 14px; }
  .close-all { background: none; border: 1px solid var(--border); border-radius: 5px; color: var(--err); cursor: pointer; font-size: 11px; padding: 3px 6px; }
  .close-all:disabled { cursor: default; opacity: .5; }
  .empty { padding: 16px; color: var(--text-dim); font-size: 12px; }

  .row {
    display: flex; align-items: stretch;
    border-bottom: 1px solid var(--border);
  }
  .row:last-child { border-bottom: none; }
  .row:hover { background: var(--bg-panel); }
  .row-main {
    display: flex; align-items: center; gap: 8px;
    flex: 1; text-align: left;
    background: none; border: none; color: var(--text);
    padding: 8px 10px; font-size: 12px;
    cursor: pointer; min-width: 0;
  }
  .row-close {
    background: none; border: none; color: var(--text-dim);
    cursor: pointer; padding: 0 12px; font-size: 16px;
    border-left: 1px solid var(--border);
  }
  .row-close:hover { color: var(--err, #e85c5c); background: color-mix(in oklch, var(--err, #e85c5c) 12%, transparent); }
  .pulse { width: 8px; height: 8px; border-radius: 50%; background: var(--border); flex: none; }
  .pulse.live { background: #4ec9b0; animation: pulse 1.6s infinite; }
  .surf {
    font-size: 10px; letter-spacing: 0.04em;
    padding: 2px 6px; border-radius: 6px;
    background: var(--bg-panel); color: var(--text-dim);
    flex: none;
  }
  .surf-studio { color: var(--accent); }
  .surf-code   { color: var(--warn); }
  .surf-helper { color: var(--accent); }
  .title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta { font-size: 10px; color: var(--text-dim); max-width: 42%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  @media (prefers-reduced-motion: reduce) { .dot.live, .pulse.live { animation: none; } }
  .grow { flex: 1; min-width: 0; }

  @keyframes pulse {
    0%   { box-shadow: 0 0 0 0 rgba(78,201,176,0.45); }
    70%  { box-shadow: 0 0 0 6px rgba(78,201,176,0); }
    100% { box-shadow: 0 0 0 0 rgba(78,201,176,0); }
  }
</style>
