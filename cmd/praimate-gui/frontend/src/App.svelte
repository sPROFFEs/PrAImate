<script>
  import { onMount } from 'svelte'
  import { api } from './lib/api.js'
  import { activePage, pageRevision, prefetchCLIs, agentStudio } from './lib/stores.js'
  import { initTheme, themeMode, setThemeMode } from './lib/theme.js'
  import logo from './assets/monke-icon.png'
  import mascot from './assets/monke-mascot.png'
  import Setup from './pages/Setup.svelte'
  import SessionPanel from './lib/SessionPanel.svelte'
  import PrivacyNotice from './lib/PrivacyNotice.svelte'
  import DatabaseUnlock from './lib/DatabaseUnlock.svelte'
  import Toast from './lib/Toast.svelte'

  // Lucide-style outline icon paths (24x24 viewBox, stroke-based).
  const icons = {
    code: 'M8 9l-4 3 4 3M16 9l4 3-4 3M13 5l-2 14',
    chats: 'M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z',
    studio: 'M3 3h18v18H3zM3 9h18M9 21V9',
    run: 'M12 8V4m0 0h4m-4 0H8m-4 9a8 8 0 0 1 16 0v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2zM9 14h.01M15 14h.01',
    skills: 'M12 2l2.4 7.4 7.6 2.6-7.6 2.6L12 22l-2.4-7.4L2 12l7.6-2.6z',
    clis: 'M4 17l6-6-6-6M12 19h8M2 4a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2z',
    mcp: 'M12 22v-5M9 8V2M15 8V2M6 8h12v5a6 6 0 0 1-12 0z',
    localllm: 'M4 4h16v16H4zM9 9h6v6H9zM9 1v3M15 1v3M9 20v3M15 20v3M1 9h3M1 15h3M20 9h3M20 15h3',
    settings:
      'M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
    sun: 'M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4',
    moon: 'M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z',
    info: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 10v7M12 7h.01',
    chevronLeft: 'M15 18l-6-6 6-6',
    chevronRight: 'M9 18l6-6-6-6',
  }

  // Sidebar collapse — icons-only rail. Persisted to localStorage so it
  // survives reloads; default expanded on first run.
  let collapsed = false
  try {
    collapsed = localStorage.getItem('praimate:sidebar-collapsed') === '1'
  } catch {}
  function toggleCollapsed() {
    collapsed = !collapsed
    try {
      localStorage.setItem('praimate:sidebar-collapsed', collapsed ? '1' : '0')
    } catch {}
  }

  // Code-oriented order: lead with the live coding terminal, then
  // conversations and agents, with config last.
  const pages = [
    { id: 'code', label: 'Code', icon: icons.code, load: () => import('./pages/Code.svelte') },
    { id: 'chats', label: 'Chats', icon: icons.chats, load: () => import('./pages/Chats.svelte') },
    { id: 'studio', label: 'Studio', icon: icons.studio, load: () => import('./pages/Studio.svelte') },
    { id: 'agents', label: 'Agents', icon: icons.run, load: () => import('./pages/Agents.svelte') },
    { id: 'skills', label: 'Skills', icon: icons.skills, load: () => import('./pages/Skills.svelte') },
    { id: 'clis', label: 'CLI & Tools', icon: icons.clis, load: () => import('./pages/CLIs.svelte') },
    { id: 'localllm', label: 'Local LLM', icon: icons.localllm, load: () => import('./pages/LocalLLM.svelte') },
    { id: 'mcp', label: 'MCP', icon: icons.mcp, load: () => import('./pages/MCP.svelte') },
    { id: 'settings', label: 'Settings', icon: icons.settings, load: () => import('./pages/Settings.svelte') },
    { id: 'about', label: 'About', icon: icons.info, load: () => import('./pages/About.svelte') },
  ]

  // Load only the surface the user is looking at. In particular, detached
  // windows no longer parse the full main UI and the main window does not pay
  // CodeMirror's memory cost until Studio/agent editing is opened.
  let pageComponent = null
  let requestedPage = ''
  let pageLoadToken = 0
  async function loadPage(id) {
    if (requestedPage === id && pageComponent) return
    requestedPage = id
    pageComponent = null
    const token = ++pageLoadToken
    const page = pages.find((item) => item.id === id) || pages[0]
    try {
      const module = await page.load()
      if (token === pageLoadToken) pageComponent = module.default
    } catch (e) {
      if (token === pageLoadToken) health = { ok: false, error: `Could not load ${page.label}: ${String(e)}` }
    }
  }

  let specialComponent = null
  let specialError = ''
  let requestedSpecial = ''
  let specialLoadToken = 0
  async function loadSpecial(kind) {
    if (requestedSpecial === kind && specialComponent) return
    requestedSpecial = kind
    specialComponent = null
    specialError = ''
    const token = ++specialLoadToken
    const loaders = {
      detached: () => import('./pages/DetachedSession.svelte'),
      editor: () => import('./pages/Editor.svelte'),
      agentStudio: () => import('./pages/AgentStudio.svelte'),
    }
    try {
      const module = await loaders[kind]()
      if (token === specialLoadToken) specialComponent = module.default
    } catch (e) {
      if (token === specialLoadToken) specialError = `Could not load window: ${String(e)}`
    }
  }

  let health = null
  // Detached chat/terminal children are presentation-only processes. Detect
  // them before asking for the encrypted DB password; the main process owns
  // the database and sends this window only its scoped session data.
  let detachedMode = null
  // Studio mode: this process was spawned as a document-editor window —
  // render the Editor shell instead of the main app.
  let editorMode = null
  // First-run setup when no launcher config exists yet.
  let firstRun = null
  let privacyNotice = null
  let databaseLock = null
  let closeBlocked = null

  async function loadUnlockedApp() {
    try {
      editorMode = await api.editorMode()
    } catch {
      editorMode = { active: false }
    }
    try {
      privacyNotice = await api.privacyNotice()
    } catch {
      privacyNotice = { required: true }
    }
    try {
      firstRun = await api.firstRun()
    } catch {
      firstRun = { needed: false }
    }
    try {
      health = await api.health()
    } catch (e) {
      health = { ok: false, error: String(e) }
    }
    // Warm the CLI & Tools detection cache in the background so the tab
    // opens instantly instead of probing on first view. Only for the
    // main app window (skip in editor/setup modes).
    if (editorMode && !editorMode.active && !firstRun?.needed) {
      prefetchCLIs()
    }
  }

  let zoomLevel = 1.0;

  function handleKeydown(e) {
    if (e.ctrlKey || e.metaKey) {
      if (e.key === '=' || e.key === '+') {
        e.preventDefault()
        zoomLevel = Math.min(zoomLevel + 0.1, 3.0)
        document.body.style.zoom = zoomLevel
      } else if (e.key === '-') {
        e.preventDefault()
        zoomLevel = Math.max(zoomLevel - 0.1, 0.5)
        document.body.style.zoom = zoomLevel
      } else if (e.key === '0') {
        e.preventDefault()
        zoomLevel = 1.0
        document.body.style.zoom = zoomLevel
      }
    }
  }

  onMount(async () => {
    window.addEventListener('keydown', handleKeydown)
    initTheme()
    try {
      detachedMode = await api.detachedMode()
    } catch {
      detachedMode = { active: false }
    }
    if (detachedMode?.active) {
      // A Studio/Chat/Terminal child must acknowledge the broker before
      // loading its potentially heavy surface. This keeps the parent launch
      // call bounded even when a project tree or editor bundle is large.
      try { await api.detachedRendererReady() } catch {}
      return
    }
    if (window.runtime?.EventsOn) {
      window.runtime.EventsOn('praimate:close-blocked', (event) => { closeBlocked = event })
    }
    try {
      databaseLock = await api.databaseLockStatus()
    } catch (e) {
      databaseLock = { unlocked: false, setupRequired: false, error: String(e) }
    }
    if (databaseLock?.unlocked) {
      await loadUnlockedApp()
    }
  })

  async function databaseUnlocked(event) {
    databaseLock = { ...databaseLock, unlocked: true, setupRequired: false }
    await loadUnlockedApp()
  }

  function setupDone() {
    firstRun = { ...firstRun, needed: false }
  }

  function privacyAccepted() {
    privacyNotice = { ...privacyNotice, required: false }
  }

  // Quick theme cycle in the sidebar footer: dark → light → system.
  const modeOrder = ['dark', 'light', 'system']
  function cycleTheme() {
    const next = modeOrder[(modeOrder.indexOf($themeMode) + 1) % modeOrder.length]
    setThemeMode(next)
  }
  $: themeIcon =
    $themeMode === 'dark' ? icons.moon : $themeMode === 'light' ? icons.sun : icons.monitor

  // Re-key the page component on navigation (and explicit attach revisions)
  // so Code/Chats consume freshly queued cross-page requests.
  $: current = pages.find((p) => p.id === $activePage) || pages[0]
  $: if (detachedMode?.active) loadSpecial(detachedMode.kind === 'studio' ? 'editor' : 'detached')
  $: if (!detachedMode?.active && editorMode?.active) loadSpecial('editor')
  $: if (!detachedMode?.active && editorMode && !editorMode.active && $agentStudio) loadSpecial('agentStudio')
  $: if (!detachedMode?.active && editorMode && !editorMode.active && !firstRun?.needed && !$agentStudio) loadPage($activePage)
</script>

{#if !detachedMode}
  <div class="boot-screen">Preparing PrAImate…</div>
{:else if detachedMode.active}
  {#if specialComponent}
    {#if detachedMode.kind === 'studio'}<svelte:component this={specialComponent} folder={detachedMode.folder} chatId={detachedMode.sessionId} />{:else}<svelte:component this={specialComponent} mode={detachedMode} />{/if}
  {:else if specialError}<div class="boot-screen"><div class="banner">{specialError}</div></div>{:else}<div class="boot-screen">Opening session…</div>{/if}
{:else if !databaseLock}
  <div class="boot-screen">Preparing secure storage…</div>
{:else if !databaseLock.unlocked}
  <DatabaseUnlock info={databaseLock} on:unlocked={databaseUnlocked} />
{:else if editorMode?.active}
  {#if specialComponent}<svelte:component this={specialComponent} folder={editorMode.folder} chatId={editorMode.chatId} />{:else if specialError}<div class="boot-screen"><div class="banner">{specialError}</div></div>{:else}<div class="boot-screen">Opening Studio…</div>{/if}
{:else if editorMode && firstRun?.needed}
  <Setup defaultRoot={firstRun.defaultRoot} on:done={setupDone} />
{:else if editorMode && $agentStudio}
  {#key $agentStudio.id ?? 'new'}
    {#if specialComponent}<svelte:component this={specialComponent} />{:else if specialError}<div class="boot-screen"><div class="banner">{specialError}</div></div>{:else}<div class="boot-screen">Opening agent studio…</div>{/if}
  {/key}
{:else if editorMode}
<div class="shell">
  <nav class="sidebar" class:collapsed>
    <!-- Collapsed: the logo itself is the expand button (no floating
         chevron overlapping the icon). Expanded: logo + wordmark + a
         collapse chevron pinned to the right edge. -->
    <div class="brand" class:clickable={collapsed} on:click={collapsed ? toggleCollapsed : undefined} title={collapsed ? 'Expand sidebar' : ''}>
      <img src={logo} alt="PrAImate" />
      {#if !collapsed}
        <span>PrAImate</span>
        <button
          class="icon-btn collapse-btn"
          title="Collapse sidebar"
          on:click|stopPropagation={toggleCollapsed}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d={icons.chevronLeft} /></svg>
        </button>
      {/if}
    </div>
    {#each pages as p}
      <button
        class="nav-item"
        class:active={$activePage === p.id}
        title={collapsed ? p.label : ''}
        on:click={() => activePage.set(p.id)}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d={p.icon} /></svg>
        {#if !collapsed}<span class="nav-label">{p.label}</span>{/if}
      </button>
    {/each}
    <div style="flex:1"></div>
    {#if !collapsed}
      <div class="sessions-slot"><SessionPanel /></div>
    {/if}
    <div class="mascot" style="background-image:url({mascot})" aria-hidden="true"></div>
    <div class="sidebar-footer">
      {#if health}
        {#if health.ok}
          <span class="pill ok">v{health.version}</span>
        {:else}
          <span class="pill err">backend error</span>
        {/if}
      {:else}
        <span></span>
      {/if}
      <button class="icon-btn" title="Theme: {$themeMode}" on:click={cycleTheme}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d={themeIcon} /></svg>
      </button>
    </div>
  </nav>

  <main class="main">
    {#if health && !health.ok}
      <div class="banner">Backend failed to initialise: {health.error}</div>
    {/if}
    {#key `${$activePage}:${$pageRevision}`}
      {#if pageComponent}<svelte:component this={pageComponent} />{:else}<div class="boot-screen">Opening {current.label}…</div>{/if}
    {/key}
  </main>
</div>
{/if}

<Toast />

{#if closeBlocked}
  <div class="picker-backdrop">
    <div class="picker" role="dialog" aria-modal="true" aria-label="Detached windows are still open" style="max-width:520px">
      <div class="picker-head"><strong class="grow">Close secondary windows first</strong><button class="picker-x" on:click={() => (closeBlocked = null)}>×</button></div>
      <div class="picker-body" style="padding:16px">
        <p style="margin-top:0">PrAImate stays open while {closeBlocked.count} secondary session window{closeBlocked.count === 1 ? ' is' : 's are'} running. Close those windows, then close PrAImate.</p>
        {#each closeBlocked.windows || [] as child}
          <div class="card-sub">• {child.title} <span class="pill">{child.kind}</span></div>
        {/each}
      </div>
      <div class="picker-foot" style="justify-content:flex-end"><button class="btn primary" on:click={() => (closeBlocked = null)}>Keep PrAImate open</button></div>
    </div>
  </div>
{/if}

{#if !detachedMode?.active && databaseLock?.unlocked && privacyNotice?.required && !editorMode?.active}
  <PrivacyNotice on:accepted={privacyAccepted} />
{/if}
