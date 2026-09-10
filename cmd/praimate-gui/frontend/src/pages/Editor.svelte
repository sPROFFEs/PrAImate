<script>
  // Document studio window (plan §14-P1) — rendered INSTEAD of the main
  // app when this process was spawned with `-editor <folder>`. Left:
  // file tree. Center: tabbed CodeMirror editors. Right: the chat pane
  // driving the agent. The agent's file edits stream in live via
  // "praimate:editor-fs" events and merge into open tabs with a
  // cursor-preserving diff; user keystrokes flush to disk debounced so
  // the agent's next turn reads what's on screen.
  import { onMount, onDestroy, tick } from 'svelte'
  import { api, onChatStream, onApproval } from '../lib/api.js'
  import SkillBindingsEditor from '../lib/SkillBindingsEditor.svelte'
  import CodeEditor from '../lib/CodeEditor.svelte'
  import ContextMenu from '../lib/ContextMenu.svelte'
  import { langOf as fileLang } from '../lib/langOf.js'
  import { renderMarkdown } from '../lib/markdown.js'
  import { showSkillDeliveryToast } from '../lib/stores.js'

  export let folder = ''
  export let chatId = ''

  let files = []
  let agentName = ''
  let error = ''
  let treeLoading = false
  let tabs = [] // [{path, content, dirty, ref, flushTimer, externalPending}]
  let active = '' // active tab path
  let cursorInfo = { line: 1, col: 1, selLen: 0 }
  let quickOpen = false
  let quickOpenQuery = ''
  let quickOpenIndex = 0
  let ctx = null

  let expandedDirs = new Set()
  let initExpanded = true

  function toggleDir(dirPath) {
    if (expandedDirs.has(dirPath)) {
      expandedDirs.delete(dirPath)
    } else {
      expandedDirs.add(dirPath)
    }
    expandedDirs = new Set(expandedDirs)
  }

  $: {
    if (initExpanded && files.length > 0) {
      files.forEach(f => {
        let parts = f.split('/')
        let dir = ''
        for (let i = 0; i < parts.length - 1; i++) {
          dir += (i===0?'':'/') + parts[i]
          expandedDirs.add(dir)
        }
      })
      expandedDirs = new Set(expandedDirs)
      initExpanded = false
    }
  }

  $: treeNodes = buildAndSortTree(files, expandedDirs)

  function buildAndSortTree(filesArr, expanded) {
    const root = { name: '', path: '', type: 'dir', children: {}, depth: -1 }

    for (const f of filesArr) {
      const parts = f.split('/')
      let curr = root
      let currentPath = ''

      for (let i = 0; i < parts.length - 1; i++) {
        currentPath += (i===0?'':'/') + parts[i]
        if (!curr.children[parts[i]]) {
          curr.children[parts[i]] = { name: parts[i], path: currentPath, type: 'dir', children: {}, depth: i }
        }
        curr = curr.children[parts[i]]
      }

      const fileName = parts[parts.length - 1]
      curr.children[fileName] = { name: fileName, path: f, type: 'file', depth: parts.length - 1 }
    }

    const result = []
    function traverse(node) {
      if (node.path !== '') {
        result.push(node)
        if (node.type === 'dir' && !expanded.has(node.path)) {
          return
        }
      }
      if (node.children) {
        const vals = Object.values(node.children)
        vals.sort((a, b) => {
          if (a.type !== b.type) return a.type === 'dir' ? -1 : 1
          return a.name.localeCompare(b.name)
        })
        for (const v of vals) {
          traverse(v)
        }
      }
    }

    traverse(root)
    return result
  }

  async function deleteFile(rel) {
    if (!confirm(`Delete ${rel}? This can't be undone from inside the editor.`)) return
    try {
      await api.editorDeleteFile(rel)
      const t = tabs.find((x) => x.path === rel)
      if (t) close(rel)
      await loadTree()
      error = ''
    } catch (e) { error = String(e) }
  }
  function fileMenu(ev, f) {
    ev.preventDefault()
    ctx = {
      x: ev.clientX,
      y: ev.clientY,
      items: [
        { label: 'Open',   action: () => open(f) },
        { label: 'Rename…', action: () => renameFile(f) },
        { label: 'Delete',  danger: true, action: () => deleteFile(f) },
      ],
    }
  }

  const lang = fileLang
  const langLabel = (p) => fileLang(p).toUpperCase()
  $: dirtyCount = tabs.filter((t) => t.dirty).length
  async function revealFolder() {
    try { await api.openEditorFolder(); error = '' } catch (e) { error = String(e) }
  }
  async function renameFile(rel) {
    const next = window.prompt ? window.prompt('Rename to (slash-relative path):', rel) : ''
    if (!next || next === rel) return
    try {
      const dst = await api.editorRenameFile(rel, next)
      await loadTree()
      const t = tabs.find((x) => x.path === rel)
      if (t) { t.path = dst; tabs = tabs }
      if (active === rel) active = dst
      error = ''
    } catch (e) { error = String(e) }
  }

  async function loadTree() {
    treeLoading = true
    try {
      files = (await api.editorListFiles()) || []
      error = ''
    } catch (e) {
      error = String(e)
    } finally {
      treeLoading = false
    }
  }

  async function open(path) {
    const existing = tabs.find((t) => t.path === path)
    if (existing) { active = path; return }
    try {
      const content = await api.editorReadFile(path)
      tabs = [...tabs, { path, content, dirty: false, ref: null, flushTimer: null, externalPending: false }]
      active = path
      error = ''
    } catch (e) {
      error = String(e)
    }
  }

  function close(path) {
    const t = tabs.find((x) => x.path === path)
    if (t?.dirty) flush(t)
    tabs = tabs.filter((x) => x.path !== path)
    if (active === path) active = tabs[tabs.length - 1]?.path || ''
  }
  function closeOthers(keep) {
    for (const t of tabs) if (t.path !== keep && t.dirty) flush(t)
    tabs = tabs.filter((t) => t.path === keep)
    active = keep
  }
  function closeAll() {
    for (const t of tabs) if (t.dirty) flush(t)
    tabs = []
    active = ''
  }
  async function saveAll() {
    for (const t of tabs) if (t.dirty) await flush(t)
  }

  function onEdit(t, content) {
    t.content = content
    t.dirty = true
    tabs = tabs
    clearTimeout(t.flushTimer)
    // Debounced flush keeps disk (what the agent reads) close to the
    // screen without a write per keystroke.
    t.flushTimer = setTimeout(() => flush(t), 600)
  }

  async function flush(t) {
    if (!t.dirty) return
    try {
      await api.editorWriteFile(t.path, t.content)
      t.dirty = false
      tabs = tabs
      error = ''
    } catch (e) {
      error = String(e)
    }
  }

  // Inline new-file input (window.prompt is unavailable in WebView2).
  let newFileName = ''
  let showNewFile = false

  async function newFile() {
    const name = newFileName.trim()
    if (!name) return
    try {
      const rel = await api.editorCreateFile(name)
      showNewFile = false
      newFileName = ''
      await loadTree()
      await open(rel)
      error = ''
    } catch (e) {
      error = String(e)
    }
  }

  // External change (the agent wrote a file): merge into the open tab
  // with the minimal-span diff; refresh the tree for creates/renames.
  async function onFsEvent(ev) {
    await loadTree()
    const t = tabs.find((x) => x.path === ev.path)
    if (!t) return
    try {
      const fresh = await api.editorReadFile(ev.path)
      if (t.dirty) {
        // The user has unflushed keystrokes — don't clobber them.
        // Surface a banner on the tab instead.
        t.externalPending = true
        t.freshContent = fresh
        tabs = tabs
        return
      }
      t.content = fresh
      t.ref?.setExternal(fresh)
      tabs = tabs
    } catch {}
  }

  function acceptExternal(t) {
    t.content = t.freshContent
    t.dirty = false
    t.externalPending = false
    t.ref?.setExternal(t.freshContent)
    tabs = tabs
  }

  function keepMine(t) {
    t.externalPending = false
    tabs = tabs
    flush(t) // our version wins on disk
  }

  // --- layout: collapsible side panes ---------------------------------------

  let treeOpen = true
  let chatOpen = true
  $: gridCols = `${treeOpen ? '220px' : '30px'} 1fr ${chatOpen ? '340px' : '30px'}`

  // --- toolbar + preview ---------------------------------------------------

  let preview = false
  const TOOLBAR = [
    { label: 'B', title: 'Bold', act: (r) => r.wrapSelection('**', '**') },
    { label: 'I', title: 'Italic', act: (r) => r.wrapSelection('*', '*') },
    { label: 'S', title: 'Strikethrough', act: (r) => r.wrapSelection('~~', '~~') },
    { label: 'H1', title: 'Heading 1', act: (r) => r.toggleLinePrefix('# ') },
    { label: 'H2', title: 'Heading 2', act: (r) => r.toggleLinePrefix('## ') },
    { label: 'H3', title: 'Heading 3', act: (r) => r.toggleLinePrefix('### ') },
    { label: '•', title: 'Bullet list', act: (r) => r.toggleLinePrefix('- ') },
    { label: '1.', title: 'Numbered list', act: (r) => r.toggleLinePrefix('1. ') },
    { label: '☑', title: 'Task list', act: (r) => r.toggleLinePrefix('- [ ] ') },
    { label: '❝', title: 'Quote', act: (r) => r.toggleLinePrefix('> ') },
    { label: '</>', title: 'Inline code', act: (r) => r.wrapSelection('`', '`') },
    { label: '```', title: 'Code block', act: (r) => r.wrapSelection('\n```\n', '\n```\n', 'code') },
    { label: '🔗', title: 'Link', act: (r) => r.wrapSelection('[', '](url)') },
    { label: '▦', title: 'Table', act: (r) => r.insertSnippet('\n| Column | Column |\n| --- | --- |\n| cell | cell |\n') },
    { label: '—', title: 'Horizontal rule', act: (r) => r.insertSnippet('\n---\n') },
  ]
  function toolbarAct(item) {
    const t = tabs.find((x) => x.path === active)
    if (t?.ref) item.act(t.ref)
  }
  $: previewHTML = preview && activeTab ? renderMarkdown(activeTab.content) : ''

  // --- right-click → ask the agent about the selection ----------------------
  //
  // The menu is rendered THROUGH CodeMirror's tooltip layer, anchored
  // at the selection. Any independent overlay (whatever its z-index,
  // even portaled to <body>) can be painted under the editor text by
  // buggy webkit GPU paths; the editor's own tooltip channel is the
  // one overlay it guarantees to layer correctly everywhere.

  let ask = null // {text, fromLine, toLine}
  const ASK_ACTIONS = [
    'Improve the writing',
    'Fix grammar and spelling',
    'Make it more concise',
    'Expand with more detail',
    'Summarize it',
    'Translate to English',
  ]

  function closeAsk() {
    activeTab?.ref?.closeAskMenu()
    ask = null
  }

  function onAskCtx(e) {
    ask = { ...e.detail }
    const t = tabs.find((x) => x.path === active)
    t?.ref?.openAskMenu(buildAskDom)
  }

  // The tooltip DOM is built imperatively (CodeMirror owns its
  // lifecycle), so its styles live under :global(.ask-menu …) below.
  function buildAskDom() {
    const root = document.createElement('div')
    root.className = 'ask-menu'

    const head = document.createElement('div')
    head.className = 'ask-head'
    head.textContent = `Ask the agent — lines ${ask.fromLine}–${ask.toLine}`
    // NB: these are <div role=button>, not <button>. The Studio's
    // WebKitGTK fails to resolve var()/oklch() colours on <button>
    // elements (the items rendered dark, visible only on hover), while a
    // <div> resolves them fine — so we use clickable divs.
    const x = document.createElement('div')
    x.className = 'ask-close'
    x.setAttribute('role', 'button')
    x.tabIndex = 0
    x.textContent = '×'
    x.onclick = closeAsk
    head.appendChild(x)
    root.appendChild(head)

    for (const act of ASK_ACTIONS) {
      const b = document.createElement('div')
      b.className = 'ask-item'
      b.setAttribute('role', 'button')
      b.tabIndex = 0
      b.textContent = act
      b.onclick = () => askAgent(act)
      b.onkeydown = (ev) => {
        if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); askAgent(act) }
      }
      root.appendChild(b)
    }

    const row = document.createElement('div')
    row.className = 'ask-free'
    const input = document.createElement('input')
    input.className = 'field'
    input.placeholder = 'Or tell it what to do…'
    input.onkeydown = (ev) => {
      if (ev.key === 'Enter' && input.value.trim()) askAgent(input.value)
      if (ev.key === 'Escape') closeAsk()
      ev.stopPropagation() // keep CM from swallowing keystrokes
    }
    const go = document.createElement('button')
    go.className = 'ask-go'
    go.textContent = 'Go'
    go.onclick = () => input.value.trim() && askAgent(input.value)
    row.appendChild(input)
    row.appendChild(go)
    root.appendChild(row)

    // Don't let clicks inside the menu move the editor selection.
    root.onmousedown = (ev) => ev.stopPropagation()
    setTimeout(() => input.focus(), 0)
    return root
  }

  async function askAgent(instruction) {
    if (!ask || !instruction.trim() || sending) return
    const a = ask
    closeAsk()
    const snippet = a.text.length > 4000 ? a.text.slice(0, 4000) + '…' : a.text
    const msg =
      `In ${active} (lines ${a.fromLine}–${a.toLine}), apply this instruction to the selected text and edit the file directly, keeping everything else unchanged.\n\n` +
      `Instruction: ${instruction.trim()}\n\nSelected text:\n"""\n${snippet}\n"""`
    await sendText(msg)
  }

  // --- chat pane ---------------------------------------------------------

  let chat = null
  let messages = []
  let draft = ''
  let sending = false
  let stream = null
  let approvals = []
  let threadEl
  let unsubStream = () => {}
  let unsubApproval = () => {}

  async function loadChat() {
    try {
      messages = (await api.chatMessages(chatId)) || []
      await scrollToBottom()
    } catch (e) {
      error = String(e)
    }
  }

  function handleStreamEvent(ev) {
    if (!sending || ev.chatId !== chatId) return
    if (!stream) stream = { text: '', tools: [], reasoning: [], steps: [] }
    if (ev.type === 'text') stream.text += ev.text
    else if (ev.type === 'reasoning') stream.reasoning = [...(stream.reasoning || []), ev.text]
    else if (ev.type === 'step_start' || ev.type === 'step_finish' || ev.type === 'error') stream.steps = [...(stream.steps || []), { type: ev.type, detail: ev.detail, ok: ev.type !== 'error' && ev.ok !== false }]
    else if (ev.type === 'tool_start') stream.tools = [...stream.tools, { id: ev.id || '', tool: ev.tool, detail: ev.detail, done: false, ok: true }]
    else if (ev.type === 'tool_end') {
      const t = [...stream.tools]
      let idx = ev.id ? t.findIndex((x) => x.id === ev.id && !x.done) : -1
      if (idx < 0) idx = t.findIndex((x) => !x.done)
      if (idx >= 0) t[idx] = { ...t[idx], done: true, ok: ev.ok }
      stream.tools = t
    }
    stream = stream
    scrollToBottom()
  }

  function handleApproval(req) {
    if (req.chatId !== chatId) {
      api.resolveApproval(req.id, false, false).catch(() => {})
      return
    }
    approvals = [...approvals, req]
  }

  async function answerApproval(req, allow, always) {
    approvals = approvals.filter((a) => a.id !== req.id)
    try { await api.resolveApproval(req.id, allow, always) } catch (e) { error = String(e) }
  }

  async function send() {
    const text = draft.trim()
    if (!text) return
    draft = ''
    await sendText(text)
  }

  async function sendText(text) {
    if (!text || sending) return
    // Flush every dirty tab first so the agent reads what's on screen.
    for (const t of tabs) if (t.dirty) await flush(t)
    sending = true
    stream = null
    error = ''
    const focused = active ? `\n\n[The user is looking at: ${active} — open files: ${tabs.map((t) => t.path).join(', ')}]` : ''
    messages = [...messages, { Role: 'user', Content: text, TS: new Date().toISOString(), _pending: true }]
    await scrollToBottom()
    try {
      if (text.startsWith('!')) {
        await api.runChatCommand(chatId, text.slice(1))
      } else {
        await api.sendChatStream(chatId, text + focused, [])
      }
      messages = (await api.chatMessages(chatId)) || messages
      const refreshed = ((await api.listChats().catch(() => [])) || []).find((item) => item.ID === chatId)
      if (refreshed) {
        chat = refreshed
        showSkillDeliveryToast(refreshed.Settings?.skill_runtime, 'Studio')
      }
    } catch (e) {
      error = String(e)
      messages = messages.filter((m) => !m._pending)
    } finally {
      sending = false
      stream = null
      approvals = []
      await scrollToBottom()
    }
  }

  async function stop() {
    try { await api.cancelChatTurn(chatId) } catch {}
  }

  function onKey(e) {
    if (e.isComposing || e.keyCode === 229) return
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
  }

  async function scrollToBottom() {
    await tick()
    if (threadEl) threadEl.scrollTop = threadEl.scrollHeight
  }

  function fmtDate(s) {
    try { return new Date(s).toLocaleTimeString() } catch { return s }
  }

  // The focused-file context suffix is for the model, not the human —
  // hide it in the transcript.
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

  function onWindowKey(e) {
    if (e.key === 'Escape') {
      if (ask) { closeAsk(); return }
      if (quickOpen) { quickOpen = false; return }
    }
    const ctrl = e.ctrlKey || e.metaKey
    if (!ctrl) return
    // Ctrl+S: save (flush) active tab now. Ctrl+Shift+S: save all.
    if (e.key.toLowerCase() === 's') {
      e.preventDefault()
      if (e.shiftKey) saveAll()
      else { const t = tabs.find((x) => x.path === active); if (t) flush(t) }
      return
    }
    // Ctrl+W: close active tab.
    if (e.key.toLowerCase() === 'w' && active) {
      e.preventDefault()
      close(active)
      return
    }
    // Ctrl+P: quick-open palette.
    if (e.key.toLowerCase() === 'p') {
      e.preventDefault()
      quickOpenQuery = ''
      quickOpenIndex = 0
      quickOpen = true
    }
  }

  $: quickOpenMatches = (() => {
    const q = quickOpenQuery.trim().toLowerCase()
    const pool = files.filter((f) => !tabs.find((t) => t.path === f) || true) // include open files for fast switching
    if (!q) return pool.slice(0, 30)
    return pool.filter((f) => f.toLowerCase().includes(q)).slice(0, 30)
  })()

  async function quickOpenPick(rel) {
    quickOpen = false
    if (rel) await open(rel)
  }

  function quickOpenKey(e) {
    if (e.key === 'ArrowDown') { e.preventDefault(); quickOpenIndex = Math.min(quickOpenIndex + 1, quickOpenMatches.length - 1) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); quickOpenIndex = Math.max(quickOpenIndex - 1, 0) }
    else if (e.key === 'Enter') { e.preventDefault(); quickOpenPick(quickOpenMatches[quickOpenIndex]) }
    else if (e.key === 'Escape') { quickOpen = false }
  }

  let unsubFs = () => {}
  onMount(async () => {
    // Studio runs as a brokered child process. Signal readiness before any
    // project scan or chat hydration so the main window does not time out
    // while a large workspace is being indexed.
    try { await api.detachedRendererReady() } catch {}
    unsubStream = onChatStream(handleStreamEvent)
    unsubApproval = onApproval(handleApproval)
    if (typeof window !== 'undefined' && window.runtime?.EventsOn) {
      window.runtime.EventsOn('praimate:editor-fs', onFsEvent)
      unsubFs = () => window.runtime.EventsOff('praimate:editor-fs')
    }
    await loadTree()
    await loadChat()
    try {
      chat = (await api.listChats())?.find((c) => c.ID === chatId) || null
      if (chat?.AgentID) {
        const agents = (await api.listAgents()) || []
        agentName = agents.find((a) => a.id === chat.AgentID)?.name || chat.AgentID
        document.title = `PrAImate Studio — ${agentName} — ${folder.split(/[\\/]/).pop()}`
      }
    } catch {}
    // Open the first markdown file so the window isn't empty.
    const first = files.find((f) => /\.md$/i.test(f)) || files[0]
    if (first) await open(first)
  })
  onDestroy(() => { unsubStream(); unsubApproval(); unsubFs() })

  $: activeTab = tabs.find((t) => t.path === active)
</script>

<svelte:window on:keydown={onWindowKey} />

<ContextMenu menu={ctx} on:close={() => (ctx = null)} />

{#if quickOpen}
  <div class="qopen-backdrop" on:click={() => (quickOpen = false)} on:keydown={() => {}} role="presentation">
    <div class="qopen" on:click|stopPropagation on:keydown|stopPropagation role="presentation">
      <input
        class="qopen-input mono"
        placeholder="Go to file… (↑↓ to navigate, Enter to open, Esc to close)"
        autofocus
        bind:value={quickOpenQuery}
        on:input={() => (quickOpenIndex = 0)}
        on:keydown={quickOpenKey} />
      <div class="qopen-list">
        {#each quickOpenMatches as f, i}
          <button class="qopen-item" class:on={i === quickOpenIndex} on:click={() => quickOpenPick(f)} on:mouseover={() => (quickOpenIndex = i)} on:focus={() => (quickOpenIndex = i)}>
            <span class="mono grow">{f}</span>
            {#if tabs.find((t) => t.path === f)}<span class="sb-item">open</span>{/if}
          </button>
        {/each}
        {#if quickOpenMatches.length === 0}<div class="qopen-empty">No files match.</div>{/if}
      </div>
    </div>
  </div>
{/if}

<div class="studio" style="grid-template-columns: {gridCols}">
  {#if !treeOpen}
    <button class="rail" title="Show files" on:click={() => (treeOpen = true)}>▸<span class="rail-label">Files</span></button>
  {:else}
  <aside class="tree">
    <div class="tree-head">
      <button class="btn sm" title="Hide files" on:click={() => (treeOpen = false)}>◂</button>
      <span class="grow mono" title={folder}>{folder.split(/[\\/]/).pop()}</span>
      <button class="btn sm" on:click={loadTree} disabled={treeLoading} title="Refresh file tree">{treeLoading ? '…' : '↻'}</button>
      <button class="btn sm" on:click={revealFolder} title="Open folder in file manager">🗂</button>
      <button class="btn sm" on:click={() => (showNewFile = !showNewFile)} title="New file">＋</button>
    </div>
    {#if showNewFile}
      <div class="row" style="padding: 0 4px 8px; gap: 4px">
        <input
          class="field grow mono"
          style="font-size: 12px; padding: 4px 6px"
          placeholder="notes.md, script.py, app.sh, …"
          bind:value={newFileName}
          on:keydown={(e) => e.key === 'Enter' && newFile()} />
        <button class="btn sm primary" on:click={newFile}>OK</button>
      </div>
    {/if}
    {#each treeNodes as n}
      {#if n.type === 'dir'}
        <div class="tree-item-wrap" style="padding-left: {n.depth * 16}px">
          <button class="tree-item grow" on:click={() => toggleDir(n.path)} title={n.path}>
            <span class="file-icon" style="opacity:1">{expandedDirs.has(n.path) ? '📂' : '📁'}</span>
            <span class="file-name" style="font-weight:600">{n.name}</span>
          </button>
        </div>
      {:else}
        <div class="tree-item-wrap" class:active={n.path === active} style="padding-left: {n.depth * 16}px">
          <button
            class="tree-item grow"
            on:click={() => open(n.path)}
            on:contextmenu={(ev) => fileMenu(ev, n.path)}
            title={`${n.path} — right-click for options`}>
            <span class="file-icon">📄</span> <span class="file-name">{n.name}</span>
          </button>
          <button class="tree-act danger-hover" title="Delete file" on:click={() => deleteFile(n.path)}>✕</button>
        </div>
      {/if}
    {/each}
    {#if files.length === 0}<div class="card-sub" style="padding:8px">No editable files yet — create one.</div>{/if}
  </aside>
  {/if}

  <section class="editor-col">
    {#if error}<div class="banner error-banner"><span>{error}</span><button class="error-close" title="Dismiss error" aria-label="Dismiss error" on:click={() => (error = '')}>×</button></div>{/if}
    <div class="tabrow">
      <div class="tabbar grow">
        {#each tabs as t}
          <div class="tab" class:active={t.path === active}
               on:auxclick={(e) => { if (e.button === 1) { e.preventDefault(); close(t.path) } }}
               role="presentation">
            <button class="tab-name" on:click={() => (active = t.path)} title={t.path}>{t.path.split('/').pop()}{t.dirty ? ' •' : ''}</button>
            <button class="tab-x" title="Close (Ctrl+W) — middle-click also closes" on:click={() => close(t.path)}>×</button>
          </div>
        {/each}
      </div>
      {#if tabs.length > 0}
        <button class="tb-btn" title="Save all dirty tabs (Ctrl+Shift+S)" on:click={saveAll} disabled={dirtyCount === 0}>💾 Save all{dirtyCount > 0 ? ` (${dirtyCount})` : ''}</button>
        <button class="tb-btn" title="Close other tabs" on:click={() => active && closeOthers(active)} disabled={tabs.length < 2}>Close others</button>
        <button class="tb-btn danger-hover" title="Close all tabs" on:click={closeAll}>Close all</button>
      {/if}
    </div>
    {#if activeTab}
      <div class="toolbar">
        {#each TOOLBAR as item}
          <button class="tb-btn" title={item.title} on:click={() => toolbarAct(item)}>{item.label}</button>
        {/each}
        <span class="grow"></span>
        <button class="tb-btn" class:tb-active={preview} title="Toggle rendered preview" on:click={() => (preview = !preview)}>👁 Preview</button>
      </div>
    {/if}
    <div class="editor-split">
      <div class="editor-stack" class:half={preview}>
        {#each tabs as t (t.path)}
          <div class="editor-host" style:display={t.path === active ? 'flex' : 'none'}>
            {#if t.externalPending}
              <div class="conflict">
                The agent changed <span class="mono">{t.path}</span> while you had unsaved edits.
                <button class="btn sm" on:click={() => acceptExternal(t)}>Take agent's version</button>
                <button class="btn sm" on:click={() => keepMine(t)}>Keep mine</button>
              </div>
            {/if}
            <CodeEditor
              bind:this={t.ref}
              value={t.content}
              lang={lang(t.path)}
              on:change={(e) => onEdit(t, e.detail)}
              on:cursor={(e) => { if (t.path === active) cursorInfo = e.detail }}
              on:askctx={onAskCtx} />
          </div>
        {/each}
      </div>
      {#if preview && activeTab}
        <div class="preview-pane md">{@html previewHTML}</div>
      {/if}
    </div>
    {#if tabs.length === 0}
      <div class="empty" style="margin-top:40px">Open a file from the tree — the agent's edits appear here live.</div>
    {:else if activeTab}
      <div class="statusbar">
        <span class="sb-item mono" title={activeTab.path}>{activeTab.path}</span>
        <span class="sb-sep"></span>
        <span class="sb-item">{langLabel(activeTab.path)}</span>
        <span class="sb-sep"></span>
        <span class="sb-item">Ln {cursorInfo.line}, Col {cursorInfo.col}{cursorInfo.selLen ? ` (${cursorInfo.selLen} sel)` : ''}</span>
        <span class="grow"></span>
        {#if activeTab.dirty}<span class="sb-item warn">● Modified</span>{:else}<span class="sb-item ok">✓ Saved</span>{/if}
      </div>
    {/if}
  </section>


  {#if !chatOpen}
    <button class="rail" title="Show agent chat" on:click={() => (chatOpen = true)}>◂<span class="rail-label">Chat</span></button>
  {:else}
  <aside class="chatpane">
    <div class="chat-head">
      <strong class="grow">{chat?.Title || 'Agent chat'}</strong>
      {#if chat?.AgentID}<span class="pill">Agent: {agentName || chat.AgentID}</span>{/if}
        <span class="pill">{chat?.CLIAgent || ''}</span>
        {#if chat?.Settings?.skill_runtime}
          <span class="pill" title="Controlled payload evidence; native private context is not inspected">
            skills: {chat.Settings.skill_runtime.status || 'unknown'} · {chat.Settings.skill_runtime.coverage || 'unknown'}
          </span>
        {/if}
      <button class="btn sm" title="Hide chat" on:click={() => (chatOpen = false)}>▸</button>
    </div>
    {#if chatId}
      <div style="max-height:35vh;overflow:auto;padding:0 10px">
        <SkillBindingsEditor chatID={chatId} on:saved={loadChat} />
      </div>
    {/if}
    <div class="thread" bind:this={threadEl}>
      {#each messages as m}
        <div class="msg {m.Role === 'user' ? 'user' : 'assistant'}" class:pending={m._pending}>
          <div class="who">{m.Role}{m.TS ? ' · ' + fmtDate(m.TS) : ''}</div>
          {#if m.Meta?.activity?.length}
            <details class="activity-block">
              <summary>{activityTitle(m.Meta.activity)}</summary>
              <div class="tool-feed">
                {#each m.Meta.activity as t}
                  <div class="tool-row" class:err={t.ok === false || t.type === 'error'} class:reasoning-row={t.type === 'reasoning'}>
                    {activityStatus(t)} {activityName(t)} <span class="mono" class:reasoning-detail={t.type === 'reasoning'}>{activityDetail(t) || ''}</span>
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
        </div>
      {/each}
      {#if sending}
        <div class="msg assistant">
          <div class="who">assistant</div>
          {#if stream?.reasoning?.length}
            <div class="tool-feed">
              {#each stream.reasoning as r}
                <div class="tool-row reasoning-row">? reasoning <span class="reasoning-detail">{r}</span></div>
              {/each}
            </div>
          {/if}
          {#if stream?.steps?.length}
            <div class="tool-feed">
              {#each stream.steps as s}
                <div class="tool-row" class:err={!s.ok}>{s.ok ? (s.type === 'step_finish' ? '✓' : '◌') : '✗'} {s.type === 'error' ? 'error' : s.type === 'step_finish' ? 'step done' : 'step'} <span class="mono">{s.detail || ''}</span></div>
              {/each}
            </div>
          {/if}
          {#if stream?.tools?.length}
            <div class="tool-feed">
              {#each stream.tools as t}
                <div class="tool-row">{t.done ? (t.ok ? '✓' : '✗') : '◌'} {t.tool} <span class="mono">{t.detail || ''}</span></div>
              {/each}
            </div>
          {/if}
          {#if stream?.text}<div class="markdown">{@html renderMarkdown(stream.text)}</div><span class="cursor">▍</span>{:else}<span class="typing">…working</span>{/if}
        </div>
      {/if}
      {#each approvals as ap (ap.id)}
        <div class="approval-card">
          <div>⚠ Permission: <strong>{ap.tool}</strong></div>
          {#if ap.detail}<div class="mono card-sub">{ap.detail}</div>{/if}
          <div class="row" style="margin-top:6px">
            <button class="btn sm primary" on:click={() => answerApproval(ap, true, false)}>Allow</button>
            <button class="btn sm" on:click={() => answerApproval(ap, true, true)}>Always</button>
            <button class="btn sm danger" on:click={() => answerApproval(ap, false, false)}>Deny</button>
          </div>
        </div>
      {/each}
    </div>
    <div class="composer">
      <textarea
        class="field"
        rows="2"
        placeholder="Ask the agent to write or edit the docs…"
        bind:value={draft}
        on:keydown={onKey}
        disabled={sending}></textarea>
      {#if sending}
        <button class="btn danger" on:click={stop}>■</button>
      {:else}
        <button class="btn primary" on:click={send} disabled={!draft.trim()}>Send</button>
      {/if}
    </div>
  </aside>
  {/if}
</div>

<!-- The ask-menu renders inside CodeMirror's tooltip layer; see
     buildAskDom() and the :global(.ask-menu) styles. -->

<style>
  .error-banner { display: flex; align-items: center; gap: 10px; justify-content: space-between; }
  .error-close { background: transparent; border: 0; color: currentColor; cursor: pointer; font-size: 18px; line-height: 1; padding: 0 2px; }
  .studio {
    display: grid;
    /* columns set inline (collapsible side panes) */
    grid-template-rows: minmax(0, 1fr);
    gap: 10px;
    height: 100vh;
    padding: 10px;
    box-sizing: border-box;
    overflow: hidden;
  }
  .rail {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg-panel);
    color: var(--text-dim);
    cursor: pointer;
    font-size: 12px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding-top: 10px;
  }
  .rail:hover { color: var(--text); }
  .rail-label { writing-mode: vertical-rl; letter-spacing: 0.08em; }
  .tree {
    overflow-y: auto;
    min-height: 0;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg-panel);
    padding: 6px;
  }
  .tree-head { display: flex; align-items: center; gap: 6px; padding: 4px 6px 8px; font-size: 12px; color: var(--text-dim); }
  /* Identical square buttons so the hide/new controls line up with the
     folder label instead of floating at mismatched heights. */
  .tree-head :global(.btn.sm),
  .tree-head .btn.sm {
    width: 24px;
    height: 24px;
    padding: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    line-height: 1;
    flex: none;
  }
  .tree-head .grow { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tree-item-wrap {
    display: flex;
    align-items: center;
    border-radius: 6px;
    margin-bottom: 2px;
  }
  .tree-item-wrap:hover { background: var(--bg-raised, rgba(255,255,255,0.06)); }
  .tree-item-wrap.active { background: var(--bg-raised, rgba(255,255,255,0.1)); }
  .tree-item-wrap .tree-act {
    display: none;
    background: none;
    border: none;
    color: var(--text-dim);
    cursor: pointer;
    padding: 4px 6px;
    border-radius: 4px;
    font-size: 10px;
  }
  .tree-item-wrap:hover .tree-act { display: block; }
  .tree-item-wrap .tree-act:hover { background: rgba(220, 53, 69, 0.2); color: #ff6b6b; }
  .tree-item {
    display: flex;
    align-items: center;
    width: 100%;
    text-align: left;
    background: none;
    border: none;
    color: var(--text);
    font-size: 12px;
    padding: 4px 6px;
    cursor: pointer;
    overflow: hidden;
  }
  .tree-item .file-icon { margin-right: 6px; opacity: 0.7; font-size: 11px; }
  .tree-item .file-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .editor-col { display: flex; flex-direction: column; min-width: 0; min-height: 0; overflow: hidden; }
  .tabrow { display: flex; align-items: stretch; gap: 4px; margin-bottom: 6px; min-width: 0; }
  .tabrow .tb-btn { flex: 0 0 auto; }
  .tabbar { display: flex; gap: 4px; flex-wrap: nowrap; overflow-x: auto; overflow-y: hidden; scrollbar-width: thin; min-width: 0; }
  .tabbar .tab { flex: 0 0 auto; max-width: 220px; }
  .tabbar .tab .tab-name { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 180px; }
  .statusbar {
    display: flex; gap: 10px; align-items: center;
    border-top: 1px solid var(--border);
    background: var(--bg-panel);
    color: var(--text-dim);
    font-size: 11px;
    padding: 4px 10px;
    flex: 0 0 auto;
  }
  .statusbar .sb-item { display: inline-flex; gap: 4px; align-items: center; }
  .statusbar .sb-item.warn { color: var(--warn, #d4a72c); }
  .statusbar .sb-item.ok   { color: var(--ok, #4ec9b0); }
  .statusbar .sb-sep { width: 1px; height: 12px; background: var(--border); }
  .qopen-backdrop {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.45);
    display: flex; justify-content: center; align-items: flex-start;
    padding-top: 10vh;
    z-index: 9999;
  }
  .qopen {
    width: min(620px, 90vw);
    background: var(--bg-raised, var(--bg-panel));
    border: 1px solid var(--border-bright, var(--border));
    border-radius: 10px;
    box-shadow: 0 12px 40px rgba(0,0,0,0.4);
    overflow: hidden;
    display: flex; flex-direction: column;
  }
  .qopen-input {
    border: none; outline: none;
    background: transparent; color: var(--text);
    padding: 12px 14px;
    font-size: 13px;
    border-bottom: 1px solid var(--border);
  }
  .qopen-list { max-height: 360px; overflow-y: auto; }
  .qopen-item {
    display: flex; gap: 8px; align-items: center;
    width: 100%;
    background: none; border: none; color: var(--text);
    padding: 6px 12px; text-align: left;
    cursor: pointer; font-size: 12px;
  }
  .qopen-item.on, .qopen-item:hover { background: var(--bg-panel); }
  .qopen-empty { padding: 16px 14px; color: var(--text-dim); font-size: 12px; }
  .tab {
    display: flex;
    align-items: center;
    border: 1px solid var(--border);
    border-radius: 8px 8px 0 0;
    background: var(--bg-panel);
    font-size: 12px;
  }
  .tab.active { background: var(--bg); border-bottom-color: var(--bg); }
  .tab-name { background: none; border: none; color: var(--text); padding: 5px 4px 5px 10px; cursor: pointer; font-size: 12px; }
  .tab-x { background: none; border: none; color: var(--text-dim); cursor: pointer; padding: 5px 8px 5px 2px; }
  .editor-host { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .editor-host :global(.cm-host) { flex: 1; }
  .conflict {
    border: 1px solid var(--warn, #d4a72c);
    border-radius: var(--radius);
    padding: 6px 10px;
    margin-bottom: 6px;
    font-size: 12px;
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .chatpane {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg-panel);
    padding: 8px;
    min-height: 0;
  }
  .chat-head { display: flex; gap: 6px; align-items: center; padding-bottom: 8px; font-size: 13px; }
  .thread { flex: 1; overflow-y: auto; min-height: 0; }
  .composer { display: flex; gap: 6px; align-items: flex-end; padding-top: 8px; }
  .composer textarea { resize: none; }
  .tool-feed { font-size: 11px; color: var(--text-dim); border-left: 2px solid var(--border); padding: 4px 8px; margin-bottom: 6px; }
  .activity-block { margin: 3px 0 6px; }
  .activity-block summary { cursor: pointer; color: var(--text-dim); font-size: 11px; user-select: none; }
  .activity-block .tool-feed { margin-bottom: 0; }
  .tool-row.err { color: var(--danger, #e5484d); }
  .reasoning-row { color: var(--accent, #7c6cf2); white-space: pre-wrap; }
  .reasoning-detail { white-space: pre-wrap; }
  .typing { color: var(--text-dim); font-style: italic; }
  .cursor { animation: blink 1s steps(1) infinite; }
  @keyframes blink { 50% { opacity: 0; } }
  .approval-card { border: 1px solid var(--warn, #d4a72c); border-radius: var(--radius); padding: 8px; margin: 6px 0; font-size: 12px; }
  .msg.pending { opacity: 0.6; }
  .btn.sm { padding: 3px 10px; font-size: 12px; }
  .toolbar {
    display: flex;
    gap: 2px;
    align-items: center;
    flex-wrap: wrap;
    padding: 4px 0 6px;
  }
  .tb-btn {
    background: var(--bg-panel);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text);
    font-size: 12px;
    padding: 3px 8px;
    cursor: pointer;
    min-width: 28px;
  }
  .tb-btn:hover { background: var(--bg-raised, rgba(255,255,255,0.08)); }
  .tb-btn.tb-active { background: var(--bg-raised, rgba(255,255,255,0.12)); }
  .editor-split { flex: 1; min-height: 0; display: flex; gap: 8px; }
  .editor-stack { flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column; }
  .editor-stack.half { flex: 1; }
  .preview-pane {
    flex: 1;
    min-width: 0;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg-panel);
    padding: 14px 18px;
    font-size: 14px;
    line-height: 1.55;
  }
  .preview-pane :global(h1), .preview-pane :global(h2), .preview-pane :global(h3) { margin: 0.8em 0 0.4em; }
  .preview-pane :global(code) { background: var(--bg); padding: 1px 4px; border-radius: 4px; font-size: 12px; }
  .preview-pane :global(pre) { background: var(--bg); padding: 8px 10px; border-radius: 6px; overflow-x: auto; }
  .preview-pane :global(blockquote) { border-left: 3px solid var(--border); margin: 0.5em 0; padding-left: 10px; color: var(--text-dim); }
  .preview-pane :global(table) { border-collapse: collapse; }
  .preview-pane :global(td), .preview-pane :global(th) { border: 1px solid var(--border); padding: 4px 8px; }
  .preview-pane :global(img) { max-width: 100%; }
  .preview-pane :global(li.task-item),
  .preview-pane :global(li:has(> input[type='checkbox'])) { list-style: none; margin-left: -1.2em; }
  .preview-pane :global(input[type='checkbox']) {
    margin-right: 6px;
    vertical-align: -1px;
    accent-color: var(--accent, #7c6cf2);
  }
  /* Ask-menu lives inside CodeMirror's tooltip (imperative DOM →
     :global). HARDCODED light palette (black text on white) regardless of
     the app theme — the Studio's WebKitGTK does not reliably render
     var()/oklch() colours inside this tooltip, so we use plain literal
     colours that always have contrast. The .cm-tooltip wrapper is
     stripped (class-based for WebKitGTK, which lacks :has()). */
  :global(.cm-tooltip.ask-tooltip),
  :global(.cm-tooltip:has(> .ask-menu)) {
    background: transparent !important;
    border: none !important;
    padding: 0 !important;
  }
  :global(.ask-menu) {
    width: 300px;
    padding: 10px;
    font-family: inherit;
    background: #ffffff;
    color: #1a1a1a;
    border: 1px solid #c9c9c9;
    border-radius: 10px;
    box-shadow: 0 8px 30px rgba(0, 0, 0, 0.45);
  }
  :global(.ask-menu .ask-head) {
    font-size: 12px;
    color: #666666;
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 4px;
  }
  :global(.ask-menu .ask-item) {
    display: block;
    width: 100%;
    text-align: left;
    color: #1a1a1a;
    background: #ffffff;
    font-size: 13px;
    padding: 6px 8px;
    border-radius: 6px;
    cursor: pointer;
    line-height: 1.3;
    font-family: inherit;
    user-select: none;
  }
  :global(.ask-menu .ask-item:hover) { background: #ececec; color: #000000; }
  :global(.ask-menu .ask-free) { display: flex; gap: 4px; margin-top: 8px; align-items: center; }
  :global(.ask-menu .ask-free input) {
    font-size: 12px;
    padding: 4px 6px;
    flex: 1;
    min-width: 0;
    background: #ffffff;
    color: #1a1a1a;
    border: 1px solid #c9c9c9;
    border-radius: 6px;
  }
  :global(.ask-menu .ask-free input::placeholder) { color: #888888; }
  :global(.ask-menu .ask-go) {
    background: #2563eb;
    border: none;
    border-radius: 8px;
    color: #ffffff;
    font-size: 12px;
    padding: 5px 12px;
    cursor: pointer;
  }
  :global(.ask-menu .ask-close) {
    color: #666666;
    opacity: 0.9;
    cursor: pointer;
    font-size: 16px;
    line-height: 1;
    user-select: none;
  }
</style>
