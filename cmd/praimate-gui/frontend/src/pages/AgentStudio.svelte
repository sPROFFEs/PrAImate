<script>
  // Agent authoring studio — an IDE-like view for creating/editing agents.
  //   left   : vertical split — agent file tree (top, agent.yaml +
  //            knowledge folder + RAG index) and knowledge/RAG controls
  //            (bottom)
  //   center : tabbed editor — the agent definition (saved to the DB) plus
  //            any opened knowledge files (saved to disk). Right-click a
  //            selection to ask the authoring assistant about it.
  //   right  : an ephemeral AI assistant (CLI/model switchable, not saved)
  import { onMount, onDestroy, tick } from 'svelte'
  import { get } from 'svelte/store'
  import { api, onChatStream, onApproval } from '../lib/api.js'
  import { agentStudio } from '../lib/stores.js'
  import CodeEditor from '../lib/CodeEditor.svelte'
  import ContextMenu from '../lib/ContextMenu.svelte'
  import { langOf as fileLang } from '../lib/langOf.js'
  import { renderMarkdown } from '../lib/markdown.js'

  const DEF = '__definition__'
  const RUNTIME = '__runtime__'

  let error = ''
  let notice = ''
  let skillsPreview = ''
  let skillsPreviewBusy = false
  async function previewSkills() {
    skillsPreviewBusy = true
    skillsPreview = ''
    try { skillsPreview = JSON.stringify(await api.previewAgentSkillsV2(tabs.find(t => t.key === DEF)?.content || ''), null, 2) }
    catch (e) { skillsPreview = String(e) }
    finally { skillsPreviewBusy = false }
  }

  function dismissError() {
    error = ''
  }

  // --- agent identity / new-agent name prompt ---
  // Read the store SYNCHRONOUSLY at init so the name prompt is the very
  // first thing painted for a new agent — no flash of the empty editor.
  const initCfg = get(agentStudio) || {}
  let agentId = ''
  let isNew = !initCfg.id
  let agentName = ''
  let creationChoice = !initCfg.id
  let needName = false
  let newName = ''
  let creating = false
  let guided = false
  let guidedStep = 1
  let guidedPreview = null
  let guidedForm = {
    name: '', purpose: '', knowledge: '', preset: 'simple',
    supports: ['claude', 'openclaude', 'codex', 'opencode', 'praimate-code'],
    capabilities: { read_project: true, analyze_code: true, use_git: false, execute_commands: false, modify_files: false, network: false, external_services: false },
  }
  let runtimeConfigured = false
  let managedRuns = []
  let selectedRun = null
  let artifactPreview = null
  let runsLoading = false
  let managedRunBusy = false

  // --- tabs (center) ---
  // {key, label, lang, content, dirty, ref, isDef, isRuntime}
  let tabs = []
  let active = ''

  // --- file tree (left-top) ---
  let tree = [] // AgentFileNode[]
  let treeLoading = false

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
    if (initExpanded && tree.length > 0) {
      tree.forEach(n => {
        if (n.isDir) expandedDirs.add(n.rel)
      })
      expandedDirs = new Set(expandedDirs)
      initExpanded = false
    }
  }

  $: visibleTree = buildAndSortAgentTree(tree, expandedDirs)

  function buildAndSortAgentTree(nodes, expanded) {
    const root = { rel: '', name: '', isDir: true, children: {}, depth: -1 }

    for (const n of nodes) {
      const parts = n.rel.split('/')
      let curr = root
      let currentPath = ''

      for (let i = 0; i < parts.length - 1; i++) {
        currentPath += (i===0?'':'/') + parts[i]
        if (!curr.children[parts[i]]) {
          curr.children[parts[i]] = { name: parts[i], rel: currentPath, isDir: true, children: {}, depth: i }
        }
        curr = curr.children[parts[i]]
      }

      const fileName = parts[parts.length - 1]
      if (!curr.children[fileName]) {
        curr.children[fileName] = { ...n, children: {} }
      } else {
        Object.assign(curr.children[fileName], n)
      }
    }

    const result = []
    function traverse(node) {
      if (node.rel !== '') {
        result.push(node)
        if (node.isDir && !expanded.has(node.rel)) {
          return
        }
      }
      if (node.children) {
        const vals = Object.values(node.children)
        vals.sort((a, b) => {
          if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
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

  // --- knowledge / RAG (left-bottom) ---
  let know = null
  let knowBusy = false
  const BACKENDS = [
    { id: 'claude-cli', label: 'Claude CLI (no key)' },
    { id: 'local', label: 'Local LLM (Settings)' },
    { id: 'code', label: 'Code-only (AST, no docs)' },
    { id: 'openai', label: 'OpenAI (key)' },
    { id: 'claude', label: 'Claude API (key)' },
  ]
  let ragBackend = 'claude-cli'
  let ragKey = ''
  let ragModel = ''
  let ragLocalModels = []
  async function refreshLocalModels() {
    if (ragBackend !== 'local') return
    try {
      const cfg = await api.getLocalLLM()
      if (cfg?.endpoint) {
        ragLocalModels = (await api.testLocalLLM(cfg.endpoint, cfg.apiKey || '')) || []
      }
    } catch { ragLocalModels = [] }
  }
  $: if (ragBackend === 'local' && ragLocalModels.length === 0) refreshLocalModels()
  let ragRunning = false
  let ragLog = []
  let ragElapsed = 0
  let ragStart = 0
  let ragTimer = null
  let ragStopping = false
  let unsubInstall = () => {}

  // --- optional environment requirements script ---
  let requirementsOS = 'linux'
  let requirementsInstructions = ''
  let requirementsScript = ''
  let requirementsBusy = false

  // --- helper chat (right) ---
  let helperChatId = ''
  let helperCli = ''
  let helperModel = ''
  let helperCwd = initCfg.cwd || ''
  let clis = []
  let modelSuggestions = []
  let modelLoading = false
  let modelLoadSeq = 0
  let messages = []
  let draft = ''
  let sending = false
  let stream = null
  let approvals = []
  let threadEl
  let unsubStream = () => {}
  let unsubApproval = () => {}

  // --- ask menu (right-click) ---
  let askSel = null
  const ASK_ACTIONS = ['Improve the wording', 'Make it more concise', 'Explain this', 'Suggest a workflow for this', 'Find issues']

  $: keyNeeded = !['claude-cli', 'local', 'code', ''].includes(ragBackend)
  $: selectedCliInfo = clis.find((c) => c.id === helperCli)
  $: helperModelSupported = !!selectedCliInfo?.modelHint
  $: activeTab = tabs.find((t) => t.key === active)

  // --- layout ---
  let leftOpen = true
  let chatOpen = true
  $: gridCols = `${leftOpen ? '300px' : '34px'} 1fr ${chatOpen ? '360px' : '34px'}`
  let cursorInfo = { line: 1, col: 1, selLen: 0 }
  let ctx = null

  function fileMenu(ev, n) {
    ev.preventDefault()
    if (n.isIndex) return // RAG-index files are managed by graphify; don't expose dangerous ops
    ctx = {
      x: ev.clientX,
      y: ev.clientY,
      items: [
        { label: 'Open',   action: () => openFile(n.rel) },
        { label: 'Rename…', action: () => renameFile(n.rel) },
        { label: 'Delete',  danger: true, action: () => rmFile(n.rel) },
      ],
    }
  }
  $: dirtyCount = tabs.filter((t) => t.dirty).length
  const langLabel = (p) => fileLang(p).toUpperCase()

  const langOf = fileLang
  async function revealKnowledgeFolder() {
    if (!agentId) return
    try { await api.openAgentKnowledgeFolder(agentId); dismissError() } catch (e) { error = String(e) }
  }

  // --- load everything for an agent id ---
  async function loadAll(id) {
    error = ''
    agentId = id
    isNew = false
    needName = false
    try {
      const [defYaml, runtimeJSON] = await Promise.all([api.agentYAML(id), api.agentRuntimeJSON(id)])
      const a = (await api.listAgents())?.find((x) => x.id === id)
      agentName = a?.name || id
      requirementsOS = a?.requirements?.os || 'linux'
      requirementsInstructions = a?.requirements?.instructions || ''
      requirementsScript = a?.requirements?.script || ''
      tabs = [{ key: DEF, label: 'agent.yaml', lang: 'yaml', content: defYaml, dirty: false, ref: null, isDef: true, isRuntime: false }]
      runtimeConfigured = !!runtimeJSON
      if (runtimeJSON) tabs = [...tabs, { key: RUNTIME, label: 'runtime.json', lang: 'json', content: runtimeJSON, dirty: false, ref: null, isDef: false, isRuntime: true }]
      active = DEF
      await refreshTree()
      await loadKnowledge()
      await refreshManagedRuns()
      await tick()
      tabs.find((t) => t.isDef)?.ref?.setExternal(defYaml)
    } catch (e) {
      error = String(e)
    }
  }

  async function refreshTree() {
    if (!agentId) { tree = []; return }
    try { tree = (await api.agentKnowledgeTree(agentId)) || [] } catch { tree = [] }
  }
  async function loadKnowledge() {
    if (!agentId) { know = null; return }
    try { know = await api.getAgentKnowledge(agentId) } catch (e) { error = String(e) }
  }
  async function refreshLeftTree() {
    treeLoading = true
    try { await refreshTree(); await loadKnowledge() }
    finally { treeLoading = false }
  }

  async function refreshManagedRuns() {
    if (!agentId) { managedRuns = []; return }
    runsLoading = true
    try { managedRuns = (await api.listManagedRuns(agentId)) || [] }
    catch { managedRuns = [] }
    finally { runsLoading = false }
  }

  async function inspectManagedRun(runId) {
    artifactPreview = null
    try { selectedRun = await api.managedRunDetails(runId) }
    catch (e) { error = String(e) }
  }

  async function inspectArtifact(name) {
    if (!selectedRun) return
    try { artifactPreview = { name, content: await api.managedArtifactText(selectedRun.id, name) } }
    catch (e) { error = String(e) }
  }

  function closeRunInspector() { selectedRun = null; artifactPreview = null }
  function formatBytes(n) { return n < 1024 ? `${n} B` : `${(n / 1024).toFixed(1)} KB` }

  async function resumeManagedRun() {
    if (!selectedRun?.canResume || managedRunBusy) return
    managedRunBusy = true
    error = ''
    try {
      selectedRun = await api.resumeManagedRun(selectedRun.id)
      await refreshManagedRuns()
    } catch (e) {
      error = String(e)
      try { selectedRun = await api.managedRunDetails(selectedRun.id) } catch {}
    } finally {
      const runId = selectedRun?.id
      approvals = approvals.filter((ap) => ap.chatId !== runId)
      managedRunBusy = false
    }
  }

  async function stopManagedRun() {
    if (!selectedRun || !managedRunBusy) return
    try { await api.cancelManagedRun(selectedRun.id) } catch (e) { error = String(e) }
  }

  async function createNamed() {
    if (creating || !newName.trim()) return
    creating = true
    error = ''
    try {
      const a = await api.createAgentFromName(newName.trim())
      // Load in place — do NOT change the agentStudio store, or the
      // {#key} in App.svelte would remount us and leak a 2nd helper chat.
      await loadAll(a.id)
      // The agent now has a row + on-disk folder — boot the helper
      // chat pinned to it so the CLI launches with cwd = the agent's
      // dir (NOT /home/<user>) and can edit ./agent.yaml + ./knowledge/.
      await bootHelperChat()
    } catch (e) {
      error = String(e)
    } finally {
      creating = false
    }
  }

  function chooseManual() {
    creationChoice = false
    needName = true
  }

  function chooseGuided() {
    creationChoice = false
    guided = true
    guidedStep = 1
    guidedPreview = null
  }

  async function importFromCreation() {
    creating = true
    error = ''
    try {
      const imported = await api.importAgentDialog()
      if (!imported?.id) return
      creationChoice = false
      await loadAll(imported.id)
      await bootHelperChat()
    } catch (e) { error = String(e) }
    finally { creating = false }
  }

  function guidedRequest() {
    return {
      ...guidedForm,
      name: guidedForm.name.trim(),
      purpose: guidedForm.purpose.trim(),
      supports: guidedForm.supports,
      capabilities: guidedForm.capabilities,
    }
  }

  function recommendedPreset() {
    const c = guidedForm.capabilities
    return c.execute_commands || c.modify_files || c.use_git || c.network || c.external_services ? 'tool-enabled' : 'simple'
  }

  async function guidedNext() {
    error = ''
    if (guidedStep === 1 && (!guidedForm.name.trim() || !guidedForm.purpose.trim())) {
      error = 'Name and purpose are required.'
      return
    }
    if (guidedStep < 4) { guidedStep += 1; return }
    try { guidedPreview = await api.previewGuidedAgent(guidedRequest()) }
    catch (e) { error = String(e) }
  }

  async function createGuided() {
    if (creating || !guidedPreview) return
    creating = true
    error = ''
    try {
      const created = await api.createGuidedAgent(guidedRequest())
      guided = false
      await loadAll(created.id)
      await bootHelperChat()
    } catch (e) { error = String(e) }
    finally { creating = false }
  }

  async function openRuntime() {
    const existing = tabs.find((t) => t.isRuntime)
    if (existing) { active = existing.key; return }
    try {
      const body = runtimeConfigured
        ? await api.agentRuntimeJSON(agentId)
        : await api.enableAgentRuntime(agentId)
      runtimeConfigured = true
      tabs = [...tabs, { key: RUNTIME, label: 'runtime.json', lang: 'json', content: body, dirty: false, ref: null, isDef: false, isRuntime: true }]
      active = RUNTIME
    } catch (e) { error = String(e) }
  }

  // --- tabs ---
  async function openFile(rel) {
    const ex = tabs.find((t) => t.key === rel)
    if (ex) { active = rel; return }
    try {
      const content = await api.agentReadKnowledgeFile(agentId, rel)
      tabs = [...tabs, { key: rel, label: rel.split('/').pop(), lang: langOf(rel), content, dirty: false, ref: null, isDef: false, isRuntime: false }]
      active = rel
      await tick()
      tabs.find((t) => t.key === rel)?.ref?.setExternal(content)
      dismissError()
    } catch (e) { error = String(e) }
  }
  function closeTab(key) {
    if (key === DEF) return // definition tab stays
    tabs = tabs.filter((t) => t.key !== key)
    if (active === key) active = tabs[tabs.length - 1]?.key || DEF
  }
  function closeOtherTabs() {
    tabs = tabs.filter((t) => t.isDef || t.key === active)
  }
  function closeAllTabs() {
    tabs = tabs.filter((t) => t.isDef)
    active = DEF
  }
  function onWindowKey(e) {
    const ctrl = e.ctrlKey || e.metaKey
    if (!ctrl) return
    const k = e.key.toLowerCase()
    if (k === 's') { e.preventDefault(); if (e.shiftKey) saveAllTabs(); else saveActive() }
    else if (k === 'w' && active && active !== DEF) { e.preventDefault(); closeTab(active) }
  }

  async function saveAllTabs() {
    for (const t of tabs) {
      if (!t.dirty) continue
      const body = t.ref?.getValue() ?? t.content
      try {
        if (t.isDef) {
          const saved = await api.saveAgentYAML(body)
          agentId = saved.id
          agentName = saved.name
          try { await api.syncAgentYAMLToDisk(saved.id, helperCwd) } catch {}
        } else if (t.isRuntime) {
          await api.saveAgentRuntimeJSON(agentId, body)
        } else {
          await api.agentWriteKnowledgeFile(agentId, t.key, body)
        }
        t.dirty = false
        dismissError()
      } catch (e) { error = String(e) }
    }
    tabs = tabs
    await refreshTree(); await loadKnowledge()
  }
  function onEdit(tab, content) {
    tab.content = content
    tab.dirty = true
    tabs = tabs
  }

  // bootHelperChat starts (or restarts) the authoring assistant's chat
  // pinned to the user-visible working folder. Idempotent — if a
  // helper is already running on a STALE agentId it's torn down first
  // so we don't leak chats or write into the wrong cwd.
  async function bootHelperChat() {
    if (!agentId) return
    if (helperChatId) {
      try { await api.deleteChat(helperChatId) } catch {}
      helperChatId = ''
      messages = []
    }
    try {
      const c = await api.startAgentHelperChat(helperCli, helperModel, helperCwd, agentId)
      helperChatId = c.ID
      helperCwd = c.WorkspacePath || helperCwd
      dismissError()
    } catch (e) {
      error = String(e)
    }
  }

  async function reloadDefFromDisk() {
    if (!agentId) return
    try {
      const body = await api.readAgentYAMLFromDisk(agentId, helperCwd)
      const def = tabs.find((t) => t.isDef)
      if (def) {
        def.content = body
        def.dirty = false
        await tick()
        def.ref?.setExternal(body)
        tabs = tabs
        notice = 'Reloaded agent.yaml from disk'
      }
      dismissError()
    } catch (e) { error = String(e) }
  }
  async function saveActive() {
    const t = activeTab
    if (!t) return
    const body = t.ref?.getValue() ?? t.content
    try {
      if (t.isDef) {
        const saved = await api.saveAgentYAML(body)
        agentId = saved.id
        agentName = saved.name
        // Mirror the new YAML to ./agent.yaml in the helper workspace.
        try { await api.syncAgentYAMLToDisk(saved.id, helperCwd) } catch {}
        notice = `Saved ${saved.name}`
        await refreshTree()
        await loadKnowledge()
      } else if (t.isRuntime) {
        await api.saveAgentRuntimeJSON(agentId, body)
        notice = 'Saved runtime.json'
      } else {
        await api.agentWriteKnowledgeFile(agentId, t.key, body)
        notice = `Saved ${t.label}`
      }
      t.dirty = false
      tabs = tabs
      dismissError()
    } catch (e) { error = String(e) }
  }

  // --- knowledge actions ---
  async function setKnowMode(mode) {
    if (!agentId) return
    try {
      // Raw/RAG need a folder to read/index — create it if the user
      // jumped straight to a mode without enabling the base first.
      if ((mode === 'raw' || mode === 'rag') && !know?.exists) await api.enableAgentKnowledge(agentId)
      await api.setAgentKnowledgeMode(agentId, mode)
      const y = await api.agentYAML(agentId)
      const d = tabs.find((t) => t.isDef)
      if (d) { d.content = y; d.dirty = false; d.ref?.setExternal(y); tabs = tabs }
      await loadKnowledge()
      dismissError()
    } catch (e) { error = String(e) }
  }
  async function enableKnow() {
    if (!agentId) return
    try { await api.enableAgentKnowledge(agentId); await refreshTree(); await loadKnowledge(); dismissError() }
    catch (e) { error = String(e) }
  }
  async function addKnowFiles(folder) {
    if (!agentId) return
    try {
      if (!know?.exists) await api.enableAgentKnowledge(agentId)
      folder ? await api.pickAgentKnowledgeFolder(agentId) : await api.pickAgentKnowledgeFiles(agentId)
      await refreshTree(); await loadKnowledge()
      dismissError()
    } catch (e) { error = String(e) }
  }
  async function newFilePrompt() {
    if (!agentId) return
    const name = window.prompt ? window.prompt('New file (e.g. notes.md, script.py, run.sh, or subdir/file):', '') : ''
    if (!name) return
    try {
      const rel = await api.agentCreateKnowledgeFile(agentId, name)
      await refreshTree(); await loadKnowledge()
      await openFile(rel)
      dismissError()
    } catch (e) { error = String(e) }
  }
  async function renameFile(rel) {
    if (!agentId) return
    const next = window.prompt ? window.prompt('Rename to (slash-relative path):', rel) : ''
    if (!next || next === rel) return
    try {
      const dst = await api.agentRenameKnowledgeFile(agentId, rel, next)
      // Update any open tab pointing at the old name so it tracks the new one.
      const t = tabs.find((x) => x.key === rel)
      if (t) { t.key = dst; t.label = dst.split('/').pop(); t.lang = langOf(dst); tabs = tabs }
      if (active === rel) active = dst
      await refreshTree(); await loadKnowledge()
      dismissError()
    } catch (e) { error = String(e) }
  }
  async function rmFile(rel) {
    try {
      await api.deleteAgentKnowledgeFile(agentId, rel)
      closeTab(rel)
      await refreshTree(); await loadKnowledge()
      dismissError()
    } catch (e) { error = String(e) }
  }
  async function installGraphify() {
    if (knowBusy) return
    knowBusy = true
    try { await api.installBundledGraphify(); notice = 'graphify installed.'; await loadKnowledge(); dismissError() }
    catch (e) { error = String(e) } finally { knowBusy = false }
  }
  async function buildRAG() {
    if (!agentId || ragRunning) return
    ragRunning = true; ragStopping = false; ragLog = []; ragElapsed = 0; ragStart = Date.now(); error = ''
    ragTimer = setInterval(() => { ragElapsed = (Date.now() - ragStart) / 1000 }, 200)
    try {
      await api.buildAgentRAG(agentId, ragBackend, ragKey, ragModel)
      notice = 'RAG index built.'
      await refreshTree(); await loadKnowledge()
      dismissError()
    } catch (e) {
      if (ragStopping) notice = 'RAG indexing stopped.'
      else error = 'RAG build failed: ' + String(e)
    }
    finally { ragRunning = false; if (ragTimer) { clearInterval(ragTimer); ragTimer = null } }
  }
  async function pickRequirementsScript() {
    if (!agentId || requirementsBusy) return
    requirementsBusy = true
    try {
      const saved = await api.pickAgentRequirementsScript(agentId, requirementsOS, requirementsInstructions)
      requirementsOS = saved.requirements?.os || requirementsOS
      requirementsInstructions = saved.requirements?.instructions || ''
      requirementsScript = saved.requirements?.script || ''
      const yaml = await api.agentYAML(agentId)
      const def = tabs.find((t) => t.isDef)
      if (def) { def.content = yaml; def.dirty = false; def.ref?.setExternal(yaml); tabs = tabs }
      notice = `Requirements script ${requirementsScript} attached.`
      dismissError()
    } catch (e) { error = String(e) }
    finally { requirementsBusy = false }
  }
  async function stopRAG() {
    if (!agentId || !ragRunning || ragStopping) return
    ragStopping = true
    try { await api.cancelAgentRAG(agentId) }
    catch (e) { ragStopping = false; error = String(e) }
  }
  async function copyRAGLog() {
    try {
      await navigator.clipboard.writeText(ragLog.join('\n'))
      notice = 'RAG log copied.'
      dismissError()
    } catch (e) { error = 'Could not copy RAG log: ' + String(e) }
  }

  // --- right-click ask menu → routes to the authoring assistant ---
  function onAskCtx(e) {
    askSel = { ...e.detail }
    activeTab?.ref?.openAskMenu(buildAskDom)
  }
  function closeAsk() { activeTab?.ref?.closeAskMenu(); askSel = null }
  function buildAskDom() {
    const root = document.createElement('div')
    root.className = 'ask-menu'
    const head = document.createElement('div')
    head.className = 'ask-head'
    head.textContent = `Ask the assistant — lines ${askSel.fromLine}–${askSel.toLine}`
    const x = document.createElement('div')
    x.className = 'ask-close'; x.setAttribute('role', 'button'); x.tabIndex = 0; x.textContent = '×'
    x.onclick = closeAsk
    head.appendChild(x); root.appendChild(head)
    for (const act of ASK_ACTIONS) {
      const b = document.createElement('div')
      b.className = 'ask-item'; b.setAttribute('role', 'button'); b.tabIndex = 0; b.textContent = act
      b.onclick = () => askAgent(act)
      b.onkeydown = (ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); askAgent(act) } }
      root.appendChild(b)
    }
    const row = document.createElement('div'); row.className = 'ask-free'
    const input = document.createElement('input'); input.className = 'field'; input.placeholder = 'Or tell it what to do…'
    input.onkeydown = (ev) => { if (ev.key === 'Enter' && input.value.trim()) askAgent(input.value); if (ev.key === 'Escape') closeAsk(); ev.stopPropagation() }
    const go = document.createElement('div'); go.className = 'ask-go'; go.setAttribute('role', 'button'); go.tabIndex = 0; go.textContent = 'Go'
    go.onclick = () => input.value.trim() && askAgent(input.value)
    row.appendChild(input); row.appendChild(go); root.appendChild(row)
    root.onmousedown = (ev) => ev.stopPropagation()
    setTimeout(() => input.focus(), 0)
    return root
  }
  async function askAgent(instruction) {
    if (!askSel || !instruction.trim()) return
    const s = askSel
    closeAsk()
    chatOpen = true
    const snippet = s.text.length > 3000 ? s.text.slice(0, 3000) + '…' : s.text
    await sendMsg(`While authoring agent "${agentName}", help with this (lines ${s.fromLine}–${s.toLine} of ${activeTab?.label}):\n\nInstruction: ${instruction.trim()}\n\nSelected:\n"""\n${snippet}\n"""`)
  }

  // --- helper chat ---
  function handleStreamEvent(ev) {
    if (!sending || ev.chatId !== helperChatId) return
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
    stream = stream; scrollChat()
  }
  function handleApproval(req) {
    const isManagedResume = managedRunBusy && req.chatId === selectedRun?.id
    if (req.chatId !== helperChatId && !isManagedResume) { api.resolveApproval(req.id, false, false).catch(() => {}); return }
    approvals = [...approvals, req]
  }
  async function answerApproval(req, allow, always) {
    approvals = approvals.filter((a) => a.id !== req.id)
    try { await api.resolveApproval(req.id, allow, always) } catch (e) { error = String(e) }
  }
  async function send() { const text = draft.trim(); if (!text) return; draft = ''; await sendMsg(text) }
  async function sendMsg(text) {
    if (!text || sending || !helperChatId) return
    sending = true; stream = null
    messages = [...messages, { Role: 'user', Content: text, TS: new Date().toISOString(), _pending: true }]
    await scrollChat()
    try {
      // Give the helper the exact draft visible in the editor, not only the
      // last DB-saved version mirrored when the chat started.
      const def = tabs.find((t) => t.isDef)
      const before = def?.ref?.getValue() ?? def?.content ?? ''
      if (agentId && def) await api.writeAgentYAMLDraftToDisk(agentId, helperCwd, before)
      await api.sendChatStream(helperChatId, text, [])
      messages = (await api.chatMessages(helperChatId)) || messages
      // Pull a successful tool edit straight back into the editor. Keep it
      // dirty until “Save agent” validates and commits it to the DB.
      if (agentId && def) {
        try {
          const after = await api.readAgentYAMLFromDisk(agentId, helperCwd)
          if (after !== before) {
            def.content = after
            await tick()
            def.ref?.setExternal(after)
            def.dirty = true
            tabs = tabs
            notice = 'The assistant updated agent.yaml — review it, then click Save agent.'
          }
        } catch (e) {
          // The assistant turn is already complete. A refresh failure should
          // not discard its persisted reply or the optimistic user message.
          error = `Assistant replied, but agent.yaml could not be refreshed: ${String(e)}`
        }
      }
    } catch (e) { error = String(e); messages = messages.filter((m) => !m._pending) }
    finally { sending = false; stream = null; approvals = []; await scrollChat() }
  }
  function onKey(e) {
    if (e.isComposing || e.keyCode === 229) return
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
  }
  async function stopChat() { try { await api.cancelChatTurn(helperChatId) } catch {} }
  async function applyHelperConfig() {
    if (!helperChatId) return
    // Keep write-capable tools pinned. OpenCode plan/build modes may reject
    // edits in run mode, so helper chats need full when using it.
    const tools = helperCli === 'opencode' || helperCli === 'praimate-code' ? 'full' : 'edits'
    try { await api.updateChatConfig(helperChatId, helperCli, helperModelSupported ? helperModel.trim() : '', tools, '', '', '') } catch (e) { error = String(e) }
  }
  async function onHelperCli() {
    const seq = ++modelLoadSeq
    modelLoading = true
    modelSuggestions = (await api.listCLIModels(helperCli).catch(() => [])) || []
    if (seq === modelLoadSeq) modelLoading = false
    helperModel = ''
    await applyHelperConfig()
  }
  async function chooseHelperCwd() {
    try {
      const folder = await api.pickFolder()
      if (!folder || folder === helperCwd) return
      helperCwd = folder
      await bootHelperChat()
      notice = `Assistant working folder: ${helperCwd}`
    } catch (e) { error = String(e) }
  }
  async function scrollChat() { await tick(); if (threadEl) threadEl.scrollTop = threadEl.scrollHeight }
  function fmtDate(ts) { try { return new Date(ts).toLocaleTimeString() } catch { return '' } }
  function cleanMsg(s) { return (s || '').replace(/\n{3,}/g, '\n\n').trim() }
  function activityTitle(activity) { const n = activity?.length || 0; return `Activity · ${n} event${n === 1 ? '' : 's'}` }
  function activityStatus(t) { if (t.type === 'reasoning') return '?'; if (t.type === 'step_start') return '◌'; if (t.type === 'step_finish') return '✓'; if (t.type === 'error' || t.ok === false) return '✗'; return '✓' }
  function activityName(t) { if (t.type === 'reasoning') return 'reasoning'; if (t.type === 'step_start') return 'step'; if (t.type === 'step_finish') return 'step done'; if (t.type === 'error') return 'error'; return t.tool || t.type || 'tool' }
  function activityDetail(t) { return t.type === 'reasoning' ? t.text : t.detail }

  async function close() {
    if (helperChatId) { try { await api.deleteChat(helperChatId) } catch {} }
    agentStudio.set(null)
  }

  onMount(async () => {
    try { clis = (await api.listCLIs()) || [] } catch {}
    const firstAvail = clis.find((c) => c.available)
    helperCli = firstAvail ? firstAvail.id : (clis[0]?.id ?? 'claude')
    modelLoading = true
    modelSuggestions = (await api.listCLIModels(helperCli).catch(() => [])) || []
    modelLoading = false
    if (initCfg.id) {
      await loadAll(initCfg.id)
      // Existing-agent flow: the agent already has a row + disk folder,
      // so we can boot the helper now with cwd = AgentDir.
      await bootHelperChat()
    }
    // New-agent flow: bootHelperChat() runs from createNamed() AFTER
    // the user names the agent and the DB row exists. Booting earlier
    // would launch the CLI in /home/<user> with no agent.yaml on disk.
    unsubStream = onChatStream(handleStreamEvent)
    unsubApproval = onApproval(handleApproval)
    if (typeof window !== 'undefined' && window.runtime?.EventsOn) {
      window.runtime.EventsOn('praimate:install', (ev) => {
        if (ev && ev.cli === 'graphify:' + agentId) ragLog = [...ragLog, ev.line]
      })
      unsubInstall = () => window.runtime.EventsOff('praimate:install')
    }
  })
  onDestroy(() => { unsubStream(); unsubApproval(); unsubInstall(); if (ragTimer) clearInterval(ragTimer) })
</script>

<svelte:window on:keydown={onWindowKey} />

<ContextMenu menu={ctx} on:close={() => (ctx = null)} />

{#if selectedRun}
  <div class="name-overlay run-overlay">
    <section class="name-card run-card" role="dialog" aria-modal="true" aria-label="Managed run details">
      <div class="run-title"><div><h2>Managed run</h2><div class="mono run-id">{selectedRun.id}</div></div><div class="row2">
        {#if selectedRun.canResume}<button class="btn primary" on:click={resumeManagedRun} disabled={managedRunBusy}>{managedRunBusy ? 'Resuming…' : 'Resume checkpoint'}</button>{/if}
        {#if managedRunBusy}<button class="btn" on:click={stopManagedRun}>Stop</button>{/if}
        <button class="xbtn" aria-label="Close run details" on:click={closeRunInspector}>×</button>
      </div></div>
      <div class="run-summary"><span class="run-state {selectedRun.state}">{selectedRun.state}</span><span>{selectedRun.turns} turn(s)</span><span>{new Date(selectedRun.startedAt).toLocaleString()}</span></div>
      {#if selectedRun.inputLimitBytes}
        <p>Controlled input: {selectedRun.inputBytes || 0} / {selectedRun.inputLimitBytes} bytes across attempts; skill content: {selectedRun.skillBytes || 0} bytes. These are not provider tokens, cost, or private CLI context.</p>
      {/if}
      {#if selectedRun.evidenceVerified}<p>Required artifact presence, size and configured hashes verified. This is not a code-quality verdict.</p>{/if}
      {#each approvals.filter((ap) => ap.chatId === selectedRun.id) as ap (ap.id)}
        <div class="approval"><div>⚠ Managed tool approval: <strong>{ap.tool}</strong></div>
          {#if ap.detail}<div class="mono approval-detail">{ap.detail}</div>{/if}
          <div class="row2"><button class="btn sm" on:click={() => answerApproval(ap, false, false)}>Deny</button><button class="btn sm" on:click={() => answerApproval(ap, true, false)}>Allow once</button><button class="btn sm primary" on:click={() => answerApproval(ap, true, true)}>Always this run</button></div>
        </div>
      {/each}
      {#if selectedRun.error}<div class="banner">{selectedRun.error}</div>{/if}
      {#if selectedRun.final}<div class="run-section"><strong>Final response</strong><div class="markdown run-final">{@html renderMarkdown(selectedRun.final)}</div></div>{/if}
      <div class="run-columns">
        <div class="run-section"><strong>Working memory · {selectedRun.memory?.length || 0}</strong>
          {#if selectedRun.memory?.length}
            {#each selectedRun.memory as item}<div class="run-item"><span class="tag">{item.kind}{item.status ? ` · ${item.status}` : ''}</span><b>{item.title || ''}</b><div>{item.content}</div></div>{/each}
          {:else}<div class="hint">No per-run memory items.</div>{/if}
        </div>
        <div class="run-section"><strong>Artifacts · {selectedRun.artifacts?.length || 0}</strong>
          {#if selectedRun.artifacts?.length}
            {#each selectedRun.artifacts as artifact}<button class="artifact-row" on:click={() => inspectArtifact(artifact.name)}><span>{artifact.name}</span><span>{formatBytes(artifact.size)}</span></button>{/each}
          {:else}<div class="hint">No artifacts.</div>{/if}
        </div>
      </div>
      {#if artifactPreview}<div class="run-section artifact-preview"><strong>{artifactPreview.name}</strong><pre>{artifactPreview.content}</pre></div>{/if}
    </section>
  </div>
{/if}

{#if creationChoice}
  <div class="name-overlay">
    <div class="name-card creation-card">
      <h2>Create new agent</h2>
      <p class="sub">Choose how much help you want. Manual creation remains the same YAML editor.</p>
      {#if error}<div class="banner error-banner" role="alert"><span>{error}</span><button class="error-close" aria-label="Cerrar error" on:click={dismissError}>×</button></div>{/if}
      <div class="creation-options">
        <button class="creation-option" on:click={chooseGuided}>
          <strong>Guided creation</strong><span>Describe the agent and choose capabilities. PrAImate generates explicit, reviewable configuration.</span>
        </button>
        <button class="creation-option" on:click={chooseManual}>
          <strong>Manual creation</strong><span>Name the agent, then open the existing YAML authoring studio.</span>
        </button>
        <button class="creation-option" on:click={importFromCreation} disabled={creating}>
          <strong>Import agent</strong><span>Open an existing YAML or portable .praimate-agent package.</span>
        </button>
      </div>
      <div class="row2" style="margin-top:14px; justify-content:flex-end"><button class="btn" on:click={close}>Cancel</button></div>
    </div>
  </div>
{:else if guided}
  <div class="name-overlay">
    <div class="name-card creation-card guided-card">
      <div class="wizard-progress">Step {guidedStep} of 4</div>
      {#if guidedStep === 1}
        <h2>Identity and purpose</h2>
        <p class="sub">Use plain language. These fields become portable agent metadata and instructions.</p>
        <label class="lbl" for="guided-name">Name</label>
        <input id="guided-name" class="field" placeholder="e.g. Code Reviewer" bind:value={guidedForm.name} />
        <label class="lbl" for="guided-purpose">What should it do?</label>
        <textarea id="guided-purpose" class="field" rows="5" placeholder="Review software projects and report concrete defects…" bind:value={guidedForm.purpose}></textarea>
      {:else if guidedStep === 2}
        <h2>Knowledge</h2>
        <p class="sub">Documents are attached after creation and travel inside the portable agent package.</p>
        <label class="choice"><input type="radio" bind:group={guidedForm.knowledge} value="" /> No additional knowledge</label>
        <label class="choice"><input type="radio" bind:group={guidedForm.knowledge} value="raw" /> Raw documents — the CLI reads files directly</label>
        <label class="choice"><input type="radio" bind:group={guidedForm.knowledge} value="rag" /> Indexed knowledge — Graphify retrieval</label>
      {:else if guidedStep === 3}
        <h2>Capabilities</h2>
        <p class="sub">These are explicit declarations, not hidden permission grants. Native runs use CLI permissions; Autonomous runs use PrAImate's approval broker.</p>
        <div class="cap-grid">
          <label class="choice"><input type="checkbox" bind:checked={guidedForm.capabilities.read_project} /> Read project files</label>
          <label class="choice"><input type="checkbox" bind:checked={guidedForm.capabilities.analyze_code} /> Analyze source code</label>
          <label class="choice"><input type="checkbox" bind:checked={guidedForm.capabilities.use_git} /> Use Git</label>
          <label class="choice"><input type="checkbox" bind:checked={guidedForm.capabilities.execute_commands} /> Execute commands</label>
          <label class="choice"><input type="checkbox" bind:checked={guidedForm.capabilities.modify_files} /> Modify files</label>
          <label class="choice"><input type="checkbox" bind:checked={guidedForm.capabilities.network} /> Access the Internet</label>
          <label class="choice"><input type="checkbox" bind:checked={guidedForm.capabilities.external_services} /> Use external services</label>
        </div>
      {:else}
        <h2>Runtime preset</h2>
        <p class="sub">Presets expand into explicit runtime.json fields. Recommendation: <strong>{recommendedPreset()}</strong>.</p>
        <div class="preset-grid">
          {#each [
            {id:'simple', title:'Simple', text:'Native CLI, safe default, step-by-step.'},
            {id:'tool-enabled', title:'Tool-enabled', text:'Native CLI with declared capabilities and conservative permissions.'},
            {id:'autonomous', title:'Autonomous', text:'Managed project, command, knowledge and MCP tools with approvals, checkpoints, working memory and artifacts.'}
          ] as preset}
            <label class="preset" class:selected={guidedForm.preset === preset.id}>
              <input type="radio" bind:group={guidedForm.preset} value={preset.id} on:change={() => (guidedPreview = null)} />
              <strong>{preset.title}</strong><span>{preset.text}</span>
            </label>
          {/each}
        </div>
        {#if guidedPreview}
          <div class="summary-card">
            <strong>Generated capability summary</strong>
            {#each guidedPreview.capabilitySummary || [] as item}<div>✓ {item}</div>{/each}
            {#each guidedPreview.warnings || [] as warning}<div class="warn">⚠ {warning}</div>{/each}
            <div class="mono summary-id">Agent ID: {guidedPreview.agent.id}</div>
          </div>
        {/if}
      {/if}
      {#if error}<div class="banner error-banner" role="alert"><span>{error}</span><button class="error-close" aria-label="Cerrar error" on:click={dismissError}>×</button></div>{/if}
      <div class="row2 wizard-actions">
        <button class="btn" on:click={() => { if (guidedStep === 1) { guided = false; creationChoice = true } else { guidedStep -= 1; guidedPreview = null } }}>Back</button>
        <span class="grow"></span>
        {#if guidedStep < 4}
          <button class="btn primary" on:click={guidedNext}>Continue</button>
        {:else if !guidedPreview}
          <button class="btn primary" on:click={guidedNext}>Review configuration</button>
        {:else}
          <button class="btn primary" on:click={createGuided} disabled={creating}>{creating ? 'Creating…' : 'Create agent'}</button>
        {/if}
      </div>
    </div>
  </div>
{:else if needName}
  <div class="name-overlay">
    <div class="name-card">
      <h2>Name your agent</h2>
      <p class="sub">A folder is created under your praimate config and the template is loaded with this name.</p>
      {#if error}
        <div class="banner error-banner" role="alert">
          <span>{error}</span>
          <button class="error-close" title="Cerrar error" aria-label="Cerrar error" on:click={dismissError}>×</button>
        </div>
      {/if}
      <input class="field" placeholder="e.g. Code Review" bind:value={newName} on:keydown={(e) => e.key === 'Enter' && createNamed()} autofocus />
      <div class="row2" style="margin-top:14px; justify-content:flex-end">
        <button class="btn" on:click={close}>Cancel</button>
        <button class="btn primary" on:click={createNamed} disabled={creating || !newName.trim()}>{creating ? 'Creating…' : 'Create & open'}</button>
      </div>
    </div>
  </div>
{:else}
<div class="astudio" style="grid-template-columns: {gridCols}">
  <!-- LEFT -->
  {#if !leftOpen}
    <button class="rail" title="Show files" on:click={() => (leftOpen = true)}>▸<span class="rail-label">Files</span></button>
  {:else}
  <aside class="left">
    <div class="left-head">
      <strong class="grow">{agentName}</strong>
      <button class="xbtn" title="Refresh file tree" on:click={refreshLeftTree} disabled={treeLoading}>{treeLoading ? '…' : '↻'}</button>
      <button class="xbtn" title="Open knowledge folder in file manager" on:click={revealKnowledgeFolder}>🗂</button>
      <button class="xbtn" title="New file" on:click={newFilePrompt}>＋</button>
      <button class="xbtn" title="Hide" on:click={() => (leftOpen = false)}>◂</button>
    </div>

    <!-- file tree: agent.yaml (definition, in the DB) + the knowledge/
         folder (everything inside it IS the agent's knowledge base) -->
    <div class="files">
      <button class="tree-item" class:on={active === DEF} on:click={() => (active = DEF)}>📄 agent.yaml <span class="tag">definition</span></button>
      {#if runtimeConfigured}
        <button class="tree-item" class:on={active === RUNTIME} on:click={openRuntime}>⚙ runtime.json <span class="tag">capabilities</span></button>
      {:else}
        <button class="tree-item runtime-add" on:click={openRuntime}>＋ Advanced capabilities</button>
      {/if}
      {#if know?.exists}
        <div class="tree-row" style="padding-left:8px"><span class="tree-item dir">📁 knowledge <span class="tag">{tree.filter((n) => !n.isIndex && !n.isDir).length} file(s)</span></span></div>
        {#if tree.length === 0}
          <div class="tree-row" style="padding-left:24px"><span class="empty-note">empty — add documents below</span></div>
        {/if}
        {#each visibleTree as n}
          <div class="tree-row" style="padding-left:{16 + n.depth * 16}px" title={n.rel}>
            {#if n.isDir}
              <button class="tree-item dir" class:idx={n.isIndex} on:click={() => toggleDir(n.rel)}>
                {n.isIndex ? '🗂' : (expandedDirs.has(n.rel) ? '📂' : '📁')} {n.name}{#if n.isIndex} <span class="tag idx">RAG index</span>{/if}
              </button>
            {:else}
              <div class="tree-item-wrap grow" class:on={active === 'know/' + n.rel}>
                <button
                  class="tree-item file grow"
                  on:click={() => openFile(n.rel)}
                  on:contextmenu={(ev) => fileMenu(ev, n)}
                  title={n.isIndex ? n.rel : `${n.rel} — right-click for options`}>{n.isIndex ? '◦' : '📄'} {n.name}</button>
                {#if !n.isIndex}<button class="tree-act danger-hover" on:click={() => rmFile(n.rel)} title="Delete file">✕</button>{/if}
              </div>
            {/if}
          </div>
        {/each}
      {:else}
        <div class="tree-row" style="padding-left:8px"><span class="empty-note">No knowledge base yet — enable it below to add documents.</span></div>
      {/if}
      <div class="row2" style="padding:8px 4px 2px">
        <button class="btn sm" on:click={() => addKnowFiles(false)}>+ Files</button>
        <button class="btn sm" on:click={() => addKnowFiles(true)}>+ Folder</button>
        <button class="btn sm" on:click={newFilePrompt}>+ New</button>
      </div>
    </div>

    <!-- knowledge / RAG controls -->
    <div class="kctl">
      {#if know && !know.exists}
        <div class="lbl2">Knowledge base</div>
        <div class="hint">This agent has no knowledge folder yet. Enable it to add documents and pick a Raw or RAG mode.</div>
        <button class="btn sm primary" on:click={enableKnow}>＋ Enable knowledge base</button>
      {:else}
      <div class="lbl2">Knowledge mode</div>
      <div class="seg">
        <button class="seg-btn" class:on={know?.mode === '' || !know} on:click={() => setKnowMode('')}>None</button>
        <button class="seg-btn" class:on={know?.mode === 'raw'} on:click={() => setKnowMode('raw')}>Raw</button>
        <button class="seg-btn" class:on={know?.mode === 'rag'} on:click={() => setKnowMode('rag')}>RAG</button>
      </div>

      {#if know?.mode === 'rag'}
        {#if !know.graphifyInstalled}
          <div class="hint" style="color:var(--warn); line-height: 1.45; border: 1px solid color-mix(in oklch, var(--warn) 30%, transparent); padding: 8px; border-radius: var(--radius-sm); background: color-mix(in oklch, var(--warn) 8%, transparent)">
            ⚠️ <strong>Graphify is required</strong> for RAG knowledge indexing and retrieval. Without Graphify installed on your system, the agent cannot query its embedded knowledge graph.
          </div>
          <button class="btn sm primary" disabled={knowBusy} on:click={installGraphify}>{knowBusy ? 'Installing…' : 'Install graphify'}</button>
        {:else}
          <div class="hint" style="font-size: 11px; color: var(--text-dim); margin-bottom: 4px;">
            💡 RAG index powered by <strong>graphify</strong>. Graphify must remain installed on your system for agents to query this knowledge graph at runtime.
          </div>
          <div class="lbl2">RAG backend</div>
          <select class="field sm" bind:value={ragBackend}>{#each BACKENDS as b}<option value={b.id}>{b.label}</option>{/each}</select>
          {#if keyNeeded}
            <input class="field sm mono" type="password" placeholder="API key" bind:value={ragKey} />
            <input class="field sm mono" placeholder="model (optional)" bind:value={ragModel} />
          {:else if ragBackend === 'local'}
            <input
              class="field sm mono"
              placeholder="local model name — REQUIRED (e.g. qwen2.5-coder:7b)"
              list="rag-local-models"
              bind:value={ragModel} />
            <datalist id="rag-local-models">
              {#each ragLocalModels as m}<option value={m}></option>{/each}
            </datalist>
            <div class="hint" style="font-size:11px; color:var(--text-dim)">
              The local endpoint must serve this model under its OpenAI-compatible API. Without an explicit name, graphify defaults to an OpenAI model and your local server returns 404.
            </div>
          {/if}
          <button class="btn sm primary" on:click={ragRunning ? stopRAG : buildRAG} disabled={ragStopping}>{ragRunning ? (ragStopping ? 'Stopping…' : 'Stop indexing') : (know.hasIndex ? 'Re-index' : 'Build RAG index')}</button>
          {#if ragRunning || ragLog.length}
            <div class="rag-bar" class:run={ragRunning}><div class="rag-fill"></div></div>
            <div class="rag-meta">
              {#if ragRunning}<span class="spin">◌</span> indexing… {ragElapsed.toFixed(1)}s
              {:else if know.hasIndex}✓ index ready ({ragElapsed.toFixed(1)}s)
              {:else}done ({ragElapsed.toFixed(1)}s){/if}
            </div>
            <div class="rag-log-head"><span>Graphify log</span><button class="btn sm" on:click={copyRAGLog} disabled={ragLog.length === 0}>Copy log</button></div>
            <pre class="rag-log">{ragLog.join('\n') || '(waiting for graphify output…)'}</pre>
          {:else if know.hasIndex}
            <div class="hint" style="color:var(--ok)">✓ RAG index present (see graphify-out above).</div>
          {/if}
        {/if}
      {:else if know?.mode === 'raw'}
        <div class="hint">Raw mode: the CLI reads these files directly — no indexing.</div>
      {:else}
        <div class="hint">Pick Raw (read files directly) or RAG (graphify-indexed retrieval) to give this agent a knowledge base.</div>
      {/if}
      {/if}
    </div>

    <div class="kctl requirements">
      <div class="lbl2">Environment requirements</div>
      {#if !agentId}
        <div class="hint">Save the new agent first to attach an optional setup script.</div>
      {:else}
        <div class="hint">This script is packed with the agent but never runs on import. The recipient must explicitly run it from the Agents page.</div>
        <select class="field sm" bind:value={requirementsOS} disabled={requirementsBusy}>
          <option value="linux">Linux</option><option value="windows">Windows</option>
        </select>
        <textarea class="field sm" rows="3" placeholder="Additional instructions shown after the script runs" bind:value={requirementsInstructions} disabled={requirementsBusy}></textarea>
        <button class="btn sm" on:click={pickRequirementsScript} disabled={requirementsBusy}>{requirementsBusy ? 'Attaching…' : requirementsScript ? `Replace ${requirementsScript}` : 'Attach requirements script…'}</button>
      {/if}
    </div>

    <div class="kctl managed-runs">
      <div class="run-title"><div class="lbl2">Managed runs</div><button class="xbtn" title="Refresh managed runs" on:click={refreshManagedRuns} disabled={runsLoading}>{runsLoading ? '…' : '↻'}</button></div>
      {#if managedRuns.length === 0}<div class="hint">No managed Autonomous runs yet. Native runs are not recorded here.</div>{/if}
      {#each managedRuns.slice(0, 5) as run}
        <button class="run-row" on:click={() => inspectManagedRun(run.id)}><span class="run-state {run.state}">{run.state}</span><span class="grow">{new Date(run.startedAt).toLocaleString()}</span><span>{run.turns}t</span></button>
      {/each}
    </div>
  </aside>
  {/if}

  <!-- CENTER -->
  <section class="center">
    <div class="center-head">
      <button class="xbtn" title="Back to Agents" on:click={close}>← Agents</button>
      <div class="tabbar grow">
        {#each tabs as t}
          <div class="tab" class:active={t.key === active}
               on:auxclick={(e) => { if (e.button === 1 && !t.isDef) { e.preventDefault(); closeTab(t.key) } }}
               role="presentation">
            <button class="tab-name" on:click={() => (active = t.key)} title={t.key}>{t.label}{t.dirty ? ' •' : ''}</button>
            {#if !t.isDef}<button class="tab-x" title="Close (Ctrl+W) — middle-click also closes" on:click={() => closeTab(t.key)}>×</button>{/if}
          </div>
        {/each}
      </div>
      {#if activeTab?.isDef && agentId}
        <button class="xbtn" title="Reload agent.yaml from disk (pull helper-CLI edits)" on:click={reloadDefFromDisk}>⟳</button>
      {/if}
      <button class="xbtn" title="Save all (Ctrl+Shift+S)" on:click={saveAllTabs} disabled={dirtyCount === 0}>💾·{dirtyCount}</button>
      <button class="xbtn" title="Close other tabs" on:click={closeOtherTabs} disabled={tabs.length < 2}>↹</button>
      <button class="xbtn" title="Close all tabs" on:click={closeAllTabs} disabled={tabs.length < 2}>✕</button>
      <button class="btn primary" on:click={saveActive}>{activeTab?.isDef ? 'Save agent' : activeTab?.isRuntime ? 'Save runtime' : 'Save file'}</button>
      <button class="btn" disabled={skillsPreviewBusy} on:click={previewSkills}>Preview skills</button>
    </div>
    {#if error}
      <div class="banner error-banner" role="alert">
        <span>{error}</span>
        <button class="error-close" title="Cerrar error" aria-label="Cerrar error" on:click={dismissError}>×</button>
      </div>
    {/if}
    {#if notice}<div class="note">{notice}</div>{/if}
    {#if skillsPreview}
      <details open><summary>Skill resolution — not runtime loading</summary>
        <pre role="status" style="max-height:240px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere">{skillsPreview}</pre>
      </details>
    {/if}
    <div class="editor-stack">
      {#each tabs as t (t.key)}
        <div class="editor-host" style:display={t.key === active ? 'flex' : 'none'}>
          <CodeEditor bind:this={t.ref} value={t.content} lang={t.lang}
            on:change={(e) => onEdit(t, e.detail)}
            on:cursor={(e) => { if (t.key === active) cursorInfo = e.detail }}
            on:askctx={onAskCtx} />
        </div>
      {/each}
    </div>
    {#if activeTab}
      <div class="statusbar">
        <span class="sb-item mono" title={activeTab.key}>{activeTab.key}</span>
        <span class="sb-sep"></span>
        <span class="sb-item">{langLabel(activeTab.key)}</span>
        <span class="sb-sep"></span>
        <span class="sb-item">Ln {cursorInfo.line}, Col {cursorInfo.col}{cursorInfo.selLen ? ` (${cursorInfo.selLen} sel)` : ''}</span>
        <span class="grow"></span>
        {#if activeTab.dirty}<span class="sb-item warn">● Modified</span>{:else}<span class="sb-item ok">✓ Saved</span>{/if}
      </div>
    {/if}
  </section>

  <!-- RIGHT -->
  {#if !chatOpen}
    <button class="rail" title="Show assistant" on:click={() => (chatOpen = true)}>◂<span class="rail-label">Assistant</span></button>
  {:else}
  <aside class="chat">
    <div class="chat-head">
      <strong class="grow">Authoring assistant</strong>
      <button class="xbtn" title="Hide" on:click={() => (chatOpen = false)}>▸</button>
    </div>
    <div class="row2" style="padding:0 2px 6px">
      <select class="field sm" style="max-width:130px" bind:value={helperCli} on:change={onHelperCli}>{#each clis as c}<option value={c.id} disabled={!c.available}>{c.id}{c.available ? '' : ' (n/a)'}</option>{/each}</select>
      <input class="field sm mono grow" list="helper-models" placeholder={helperModelSupported ? 'model (blank = default)' : 'no model flag'} bind:value={helperModel} on:change={applyHelperConfig} disabled={!helperModelSupported} />
      <datalist id="helper-models">{#each modelSuggestions as m}<option value={m}></option>{/each}</datalist>
    </div>
    <div class="row2" style="padding:0 2px 6px">
      <input class="field sm mono grow" value={helperCwd} title={helperCwd} placeholder="assistant working folder" readonly />
      <button class="btn sm" title="Choose the folder where praimate-code reads and writes ./agent.yaml" on:click={chooseHelperCwd}>Folder…</button>
    </div>
    {#if modelLoading}<div class="hint" style="padding:0 2px 6px">Loading models...</div>{/if}
    <div class="thread" bind:this={threadEl}>
      {#if messages.length === 0 && !sending}<div class="hint">Ask me to draft instructions, suggest workflows, pick CLIs/tools, or review your YAML — or right-click a selection in the editor. I'm not saved to the agent.</div>{/if}
      {#each messages as m}
        <div class="msg {m.Role === 'user' ? 'user' : 'assistant'}" class:pending={m._pending}>
          <div class="who">{m.Role}{m.TS ? ' · ' + fmtDate(m.TS) : ''}</div>
          {#if m.Meta?.activity?.length}
            <details class="activity-block">
              <summary>{activityTitle(m.Meta.activity)}</summary>
              <div class="tool-feed">
                {#each m.Meta.activity as t}
                  <div class="tool-line" class:err={t.ok === false || t.type === 'error'} class:reasoning-line={t.type === 'reasoning'}>
                    {activityStatus(t)} {activityName(t)} {activityDetail(t) || ''}
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
        <div class="msg assistant"><div class="who">assistant</div>
          {#if stream?.reasoning?.length}<div class="tool-feed">{#each stream.reasoning as r}<div class="tool-line reasoning-line">? reasoning {r}</div>{/each}</div>{/if}
          {#if stream?.steps?.length}<div class="tool-feed">{#each stream.steps as s}<div class="tool-line" class:err={!s.ok}>{s.ok ? (s.type === 'step_finish' ? '✓' : '◌') : '✗'} {s.type === 'error' ? 'error' : s.type === 'step_finish' ? 'step done' : 'step'} {s.detail || ''}</div>{/each}</div>{/if}
          {#if stream?.tools?.length}<div class="tool-feed">{#each stream.tools as t}<div>{t.done ? (t.ok ? '✓' : '✗') : '◌'} {t.tool}</div>{/each}</div>{/if}
          {#if stream?.text}<div class="markdown">{@html renderMarkdown(stream.text)}</div><span class="cursor">▍</span>{:else}<span class="typing">…thinking</span>{/if}
        </div>
      {/if}
      {#each approvals as ap (ap.id)}
        <div class="approval"><div>⚠ Permission: <strong>{ap.tool}</strong></div>
          <div class="row2" style="margin-top:6px"><button class="btn sm primary" on:click={() => answerApproval(ap, true, false)}>Allow</button><button class="btn sm" on:click={() => answerApproval(ap, true, true)}>Always</button><button class="btn sm danger" on:click={() => answerApproval(ap, false, false)}>Deny</button></div>
        </div>
      {/each}
    </div>
    <div class="composer">
      <textarea class="field" rows="2" placeholder="Ask the assistant to help build this agent…" bind:value={draft} on:keydown={onKey} disabled={sending}></textarea>
      {#if sending}<button class="btn danger" on:click={stopChat}>■</button>{:else}<button class="btn primary" on:click={send} disabled={!draft.trim() || !helperChatId}>Send</button>{/if}
    </div>
  </aside>
  {/if}
</div>
{/if}

<style>
  .astudio { display: grid; grid-template-rows: minmax(0, 1fr); gap: 10px; height: 100vh; padding: 10px; box-sizing: border-box; overflow: hidden; background: var(--bg); }
  .rail { border: 1px solid var(--border); border-radius: var(--radius); background: var(--bg-panel); color: var(--text-dim); cursor: pointer; font-size: 12px; display: flex; flex-direction: column; align-items: center; gap: 8px; padding-top: 10px; }
  .rail-label { writing-mode: vertical-rl; letter-spacing: 0.08em; }

  /* LEFT */
  .left { display: flex; flex-direction: column; min-height: 0; border: 1px solid var(--border); border-radius: var(--radius); background: var(--bg-panel); }
  .left-head { display: flex; align-items: center; gap: 4px; padding: 8px 10px; border-bottom: 1px solid var(--border); font-size: 13px; }
  .files { flex: 1 1 50%; min-height: 80px; overflow-y: auto; padding: 6px 4px; border-bottom: 1px solid var(--border); }
  .tree-row { display: flex; align-items: center; gap: 2px; }
  .tree-item-wrap { display: flex; align-items: center; border-radius: var(--radius-sm); width: 100%; min-width: 0; }
  .tree-item-wrap:hover { background: var(--bg-raised); }
  .tree-item-wrap.on { background: var(--bg-raised); }
  .tree-item-wrap .tree-act { display: none; background: none; border: none; color: var(--text-dim); cursor: pointer; padding: 3px 6px; border-radius: 4px; font-size: 10px; margin-left: auto; flex-shrink: 0; }
  .tree-item-wrap:hover .tree-act { display: block; }
  .tree-item-wrap .tree-act:hover { background: rgba(220, 53, 69, 0.2); color: var(--err, #e5484d); }
  .tree-item { display: flex; align-items: center; width: 100%; text-align: left; background: none; border: none; color: var(--text); font-size: 12px; padding: 3px 6px; border-radius: 6px; cursor: pointer; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  button.tree-item:hover { background: var(--bg-raised); }
  .tree-item.on { background: var(--bg-raised); font-weight: 600; }
  .tree-item.dir { color: var(--text-dim); cursor: pointer; }
  .tree-item.idx { color: var(--text-dim); }
  .tag { font-size: 10px; color: var(--text-dim); border: 1px solid var(--border); border-radius: 4px; padding: 0 4px; }
  .tag.idx { color: var(--accent, #7c6cf2); }

  .kctl { flex: 1 1 50%; min-height: 0; overflow-y: auto; padding: 8px 10px; display: flex; flex-direction: column; gap: 8px; }
  .lbl2 { font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-dim); }
  .seg { display: flex; }
  .seg-btn { flex: 1; background: var(--bg-panel); border: 1px solid var(--border); color: var(--text-dim); padding: 5px 0; cursor: pointer; font-size: 12px; }
  .seg-btn + .seg-btn { border-left: none; }
  .seg-btn:first-child { border-radius: 7px 0 0 7px; }
  .seg-btn:last-child { border-radius: 0 7px 7px 0; }
  .seg-btn.on { background: var(--bg-raised); color: var(--text); font-weight: 600; }
  .row2 { display: flex; gap: 6px; align-items: center; }
  .hint { font-size: 12px; color: var(--text-dim); padding: 4px 2px; line-height: 1.4; }

  .rag-bar { height: 6px; border-radius: 4px; background: var(--bg-raised); overflow: hidden; position: relative; }
  .rag-bar .rag-fill { position: absolute; inset: 0; width: 100%; background: var(--accent, #7c6cf2); opacity: 0.25; }
  .rag-bar.run .rag-fill { width: 35%; animation: slide 1.1s ease-in-out infinite; opacity: 0.85; }
  @keyframes slide { 0% { left: -35%; } 100% { left: 100%; } }
  .rag-meta { font-size: 11px; color: var(--text-dim); }
  .rag-log-head { display: flex; align-items: center; justify-content: space-between; color: var(--text-dim); font-size: 11px; }
  .rag-log-head .btn { margin: 0; padding: 3px 7px; }
  .spin { display: inline-block; animation: spin 1s steps(8) infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  /* The log fills the remaining vertical space so it's readable. */
  .rag-log { flex: 1; min-height: 90px; overflow-y: auto; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 8px 10px; font-family: var(--mono); font-size: 11px; color: var(--text-dim); white-space: pre-wrap; word-break: break-word; }

  /* CENTER */
  .center { display: flex; flex-direction: column; min-width: 0; min-height: 0; overflow: hidden; }
  .center-head { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
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
  .tab { display: flex; align-items: center; border: 1px solid var(--border); border-radius: 8px 8px 0 0; background: var(--bg-panel); font-size: 12px; }
  .tab.active { background: var(--bg); }
  .tab-name { background: none; border: none; color: var(--text); padding: 5px 4px 5px 10px; cursor: pointer; font-size: 12px; }
  .tab-x { background: none; border: none; color: var(--text-dim); cursor: pointer; padding: 5px 8px 5px 2px; }
  .editor-stack { flex: 1; min-height: 0; display: flex; flex-direction: column; border: 1px solid var(--border); border-radius: var(--radius); overflow: hidden; }
  .editor-host { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .editor-host :global(.cm-host) { flex: 1; }
  .error-banner { display: flex; align-items: center; gap: 10px; justify-content: space-between; }
  .error-close { background: transparent; border: 0; color: currentColor; cursor: pointer; font-size: 18px; line-height: 1; padding: 0 2px; }
  .note { background: var(--bg-panel); border: 1px solid var(--border); border-radius: 8px; padding: 6px 10px; font-size: 12px; color: var(--text-dim); margin-bottom: 6px; }

  /* RIGHT */
  .chat { display: flex; flex-direction: column; min-height: 0; border: 1px solid var(--border); border-radius: var(--radius); background: var(--bg-panel); padding: 8px; }
  .chat-head { display: flex; align-items: center; gap: 6px; padding-bottom: 8px; font-size: 13px; }
  .thread { flex: 1; overflow-y: auto; min-height: 0; }
  .msg { padding: 7px 9px; border-radius: 8px; margin-bottom: 6px; font-size: 13px; white-space: pre-wrap; line-height: 1.45; }
  .msg.user { background: var(--bg-raised); }
  .msg.assistant { background: var(--bg); border: 1px solid var(--border); }
  .msg.pending { opacity: 0.6; }
  .who { font-size: 10.5px; color: var(--text-dim); margin-bottom: 3px; }
  .tool-feed { font-size: 11px; color: var(--text-dim); border-left: 2px solid var(--border); padding: 3px 8px; margin-bottom: 6px; }
  .activity-block { margin: 3px 0 6px; }
  .activity-block summary { cursor: pointer; color: var(--text-dim); font-size: 11px; user-select: none; }
  .activity-block .tool-feed { margin-bottom: 0; }
  .tool-line { white-space: pre-wrap; }
  .tool-line.err { color: var(--danger, #e5484d); }
  .reasoning-line { color: var(--accent, #7c6cf2); }
  .typing { color: var(--text-dim); font-style: italic; }
  .cursor { animation: blink 1s steps(1) infinite; }
  @keyframes blink { 50% { opacity: 0; } }
  .approval { border: 1px solid var(--warn, #d4a72c); border-radius: 8px; padding: 8px; margin: 6px 0; font-size: 12px; }
  .composer { display: flex; gap: 6px; align-items: flex-end; padding-top: 8px; }
  .composer textarea { resize: none; }

  .field.sm { font-size: 12px; padding: 4px 6px; }
  .btn.sm { padding: 4px 10px; font-size: 12px; }
  .xbtn { background: none; border: none; color: var(--text-dim); cursor: pointer; font-size: 13px; padding: 2px 6px; border-radius: 6px; }
  .xbtn:hover { background: var(--bg-raised); color: var(--text); }
  .xbtn.danger:hover { color: var(--err, #e5484d); }
  .grow { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mono { font-family: var(--mono); }

  /* new-agent name overlay */
  .name-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.5); display: flex; align-items: center; justify-content: center; z-index: 50; }
  .name-card { width: 420px; max-width: 92vw; background: var(--bg-panel); border: 1px solid var(--border); border-radius: var(--radius); padding: 22px 24px; }
  .name-card h2 { margin: 0 0 4px; font-size: 16px; }
  .name-card .sub { color: var(--text-dim); font-size: 12.5px; margin: 0 0 14px; }
  .creation-card { width: 620px; max-height: 88vh; overflow-y: auto; }
  .guided-card { width: 680px; }
  .creation-options { display: grid; gap: 10px; }
  .creation-option { display: flex; flex-direction: column; gap: 4px; text-align: left; padding: 14px 16px; color: var(--text); background: var(--bg); border: 1px solid var(--border); border-radius: 10px; cursor: pointer; }
  .creation-option:hover { border-color: var(--accent); background: var(--bg-raised); }
  .creation-option span, .preset span { color: var(--text-dim); font-size: 12px; line-height: 1.4; }
  .wizard-progress { color: var(--accent); font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: .08em; margin-bottom: 6px; }
  .wizard-actions { margin-top: 18px; align-items: center; }
  .choice { display: flex; align-items: center; gap: 8px; padding: 8px 4px; font-size: 13px; }
  .cap-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 3px 18px; }
  .preset-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 9px; }
  .preset { display: grid; grid-template-columns: 18px 1fr; gap: 3px 6px; padding: 11px; border: 1px solid var(--border); border-radius: 9px; cursor: pointer; }
  .preset input { grid-row: 1 / 3; }
  .preset.selected { border-color: var(--accent); background: var(--bg-raised); }
  .summary-card { margin-top: 12px; padding: 12px; border: 1px solid var(--border); border-radius: 9px; background: var(--bg); font-size: 12px; line-height: 1.6; }
  .summary-card .warn { color: var(--warn); margin-top: 5px; }
  .summary-id { color: var(--text-dim); margin-top: 5px; }
  .runtime-add { color: var(--accent); }
  .managed-runs { flex: 0 0 auto; max-height: 190px; border-top: 1px solid var(--border); }
  .run-title { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; }
  .run-row, .artifact-row { display: flex; align-items: center; gap: 8px; width: 100%; border: 1px solid var(--border); border-radius: 7px; padding: 6px 8px; color: var(--text); background: var(--bg); text-align: left; cursor: pointer; font-size: 11px; }
  .run-row:hover, .artifact-row:hover { border-color: var(--accent); }
  .run-state { border-radius: 999px; padding: 1px 7px; font-size: 10px; color: var(--text-dim); background: var(--bg-raised); text-transform: uppercase; }
  .run-state.completed { color: var(--ok); }
  .run-state.failed, .run-state.stalled { color: var(--danger, #e5484d); }
  .run-state.running, .run-state.waiting { color: var(--accent); }
  .run-overlay { z-index: 70; }
  .run-card { width: min(900px, 92vw); max-height: 88vh; overflow-y: auto; }
  .run-card h2 { margin-bottom: 2px; }
  .run-id { font-size: 11px; color: var(--text-dim); }
  .run-summary { display: flex; gap: 10px; align-items: center; margin: 12px 0; color: var(--text-dim); font-size: 11px; }
  .run-columns { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
  .run-section { border: 1px solid var(--border); border-radius: 9px; padding: 11px; margin-top: 10px; }
  .run-final { margin-top: 7px; max-height: 260px; overflow-y: auto; }
  .run-item { margin-top: 8px; padding-top: 8px; border-top: 1px solid var(--border); font-size: 12px; overflow-wrap: anywhere; }
  .run-item b { margin-left: 6px; }
  .artifact-row { justify-content: space-between; margin-top: 7px; }
  .artifact-preview pre { max-height: 260px; overflow: auto; white-space: pre-wrap; font-size: 11px; background: var(--bg); padding: 8px; border-radius: 6px; }

  /* right-click ask menu — HARDCODED black-on-white (WebKitGTK can't
     reliably render theme vars inside CodeMirror's tooltip). */
  :global(.cm-tooltip.ask-tooltip), :global(.cm-tooltip:has(> .ask-menu)) { background: transparent !important; border: none !important; padding: 0 !important; }
  :global(.ask-menu) { width: 280px; padding: 10px; background: #ffffff; color: #1a1a1a; border: 1px solid #c9c9c9; border-radius: 10px; box-shadow: 0 8px 30px rgba(0,0,0,0.45); font-family: inherit; }
  :global(.ask-menu .ask-head) { font-size: 12px; color: #666; display: flex; justify-content: space-between; align-items: center; margin-bottom: 4px; }
  :global(.ask-menu .ask-item) { display: block; width: 100%; text-align: left; color: #1a1a1a; background: #fff; font-size: 13px; padding: 6px 8px; border-radius: 6px; cursor: pointer; user-select: none; }
  :global(.ask-menu .ask-item:hover) { background: #ececec; color: #000; }
  :global(.ask-menu .ask-free) { display: flex; gap: 4px; margin-top: 8px; }
  :global(.ask-menu .ask-free input) { font-size: 12px; padding: 4px 6px; flex: 1; min-width: 0; background: #fff; color: #1a1a1a; border: 1px solid #c9c9c9; border-radius: 6px; }
  :global(.ask-menu .ask-free input::placeholder) { color: #888; }
  :global(.ask-menu .ask-go) { background: #2563eb; color: #fff; border-radius: 8px; font-size: 12px; padding: 5px 12px; cursor: pointer; user-select: none; }
  :global(.ask-menu .ask-close) { color: #666; cursor: pointer; font-size: 16px; line-height: 1; user-select: none; }
</style>
