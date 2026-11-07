<script>
  import { onMount } from 'svelte'
  import { activePage, appEdition, agentStudio, pendingTerm, pageRevision } from './stores.js'
  import { visibleNavigation } from './editionNavigation.js'
  import { api } from './api.js'
  import { term } from './terminal.js'
  import { mobileNavigation } from './mobileNavigation.js'
  import { remoteRoute } from './remoteNavigation.js'
  import { dismissMobileDialog } from './mobileDialogs.js'
  import { themeMode, setThemeMode } from './theme.js'
  import SessionPanel from './SessionPanel.svelte'
  import logo from '../assets/monke-icon.png'

  const mobile = window.praimateMobile
  const workspaceView = new URLSearchParams(location.hash.slice(1)).has('workspace')
  const groups = ['Workspace', 'Automation', 'Configure']
  let menu = false, error = '', terminalID = '', terminalPicker = false, sessions = []
  let ready = false, selectedChat = '', selectedWorker = '', routeTimer
  let leaving = false
  $: navigation = visibleNavigation(mobileNavigation, $appEdition, window.praimateRemote?.status?.identity?.role)
  $: currentID = workspaceView ? 'studio' : $agentStudio ? 'agents' : $activePage
  $: current = navigation.find(page => page.id === currentID)
  $: if ($activePage) menu = false
  $: if (ready && !workspaceView) remember(remoteRoute(currentID,{chat:selectedChat,worker:selectedWorker,terminal:terminalID,agent:$agentStudio?.id || ''}))

  function remember(route) {
    history.replaceState(null,'',route)
    clearTimeout(routeTimer)
    routeTimer=setTimeout(()=>mobile.rememberRoute(route).catch(e=>error=e.message),200)
  }
  async function canLeave() {
    if(leaving)return false
    leaving=true
    try{return !mobile.leaveEditor || await mobile.leaveEditor()}finally{leaving=false}
  }
  async function switchServer(){try{if(await canLeave())await mobile.configure()}catch(e){error=e.message}}

  async function navigate(page) {
    menu = false
    try {
      if (!await canLeave()) return
      if (workspaceView) await mobile.transport.request('navigate', {route:`#page=${page}`})
      else activePage.set(page)
    } catch (e) { error = e.message }
  }
  async function key(value) {
    if (!terminalID) { await listTerminals(); return }
    try { await term.write(terminalID, value) } catch (e) { error = e.message }
  }
  async function listTerminals() {
    try { sessions = (await api.listTerminalSessions()) || []; terminalPicker = true }
    catch (e) { error = e.message }
  }
  function selectTerminal(session) {
    terminalID = session.id
    terminalPicker = false
    pendingTerm.set({termId:session.id, chatId:session.chatId || '', cli:session.name, cwd:session.cwd})
    activePage.set('code')
    pageRevision.update(value => value + 1)
  }
  function cycleTheme() {
    const modes = ['dark','light','system']
    setThemeMode(modes[(modes.indexOf($themeMode) + 1) % modes.length])
  }
  onMount(() => {
    mobile.onBack = () => {
      if (terminalPicker) { terminalPicker=false; return true }
      if (dismissMobileDialog(document)) return true
      if (menu) { menu=false; return true }
      if (mobile.leaveEditor) { const agent=!!$agentStudio;canLeave().then(ok=>{if(ok&&!agent)return mobile.back()}).catch(e=>error=e.message);return true }
      if(!workspaceView&&$activePage!=='dashboard'){activePage.set('dashboard');return true}
      return false
    }
    const resize = () => document.documentElement.style.setProperty('--app-height', `${window.visualViewport?.height || window.innerHeight}px`)
    const select = event => terminalID = event.detail?.id || ''
    const selection = event => { if(event.detail?.page==='chats')selectedChat=event.detail.id || '';if(event.detail?.page==='workers')selectedWorker=event.detail.id || '' }
    const restored = () => ready = true
    window.addEventListener('praimate:mobile-terminal', select)
    window.addEventListener('praimate:mobile-selection', selection)
    window.addEventListener('praimate:route-ready', restored)
    window.visualViewport?.addEventListener('resize', resize)
    resize()
    return () => { clearTimeout(routeTimer);window.visualViewport?.removeEventListener('resize',resize); window.removeEventListener('praimate:mobile-terminal',select);window.removeEventListener('praimate:mobile-selection',selection);window.removeEventListener('praimate:route-ready',restored); delete mobile.onBack }
  })
</script>

<header class="mobile-toolbar">
  <button class="menu-toggle" aria-expanded={menu} aria-label="Open navigation" on:click={() => menu = !menu}>
    <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16" /></svg>
  </button>
  <img src={logo} alt="PrAImate" width="28" height="28" />
  <div class="heading"><strong>{current?.label || 'PrAImate'}</strong><small>{mobile.serverName}</small></div>
</header>

{#if menu}
  <button class="mobile-shade" aria-label="Close navigation" on:click={() => menu = false}></button>
  <nav class="mobile-navigation" aria-label="Mobile navigation">
    <div class="drawer-brand"><img src={logo} alt="" width="42" height="42" /><div><strong>PrAImate</strong><small>{mobile.serverName}</small></div></div>
    <div class="server-address">{mobile.serverURL}</div>
    {#each groups as group}
      <div class="nav-group">{group}</div>
      {#each navigation.filter(page => page.group === group) as page}
        <button class="mobile-nav-item" class:active={currentID === page.id} aria-current={currentID === page.id ? 'page' : undefined} on:click={() => navigate(page.id)}>
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d={page.icon} /></svg><span>{page.label}</span>
        </button>
      {/each}
    {/each}
    {#if !workspaceView && !$agentStudio}<div class="drawer-sessions"><SessionPanel /></div>{/if}
    <div class="drawer-footer"><button class="btn sm" on:click={switchServer}>Switch server</button><button class="btn sm" on:click={cycleTheme}>Theme: {$themeMode}</button></div>
  </nav>
{/if}

{#if $activePage === 'code'}
  <div class="terminal-keys" aria-label="Terminal keyboard">
    <button on:click={listTerminals} aria-label="Choose terminal"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 5h18v14H3zM7 9h.01M11 9h.01M15 9h.01M7 13h.01M11 13h6" /></svg></button>
    <button on:click={() => key('\u001b')}>Esc</button><button on:click={() => key('\t')}>Tab</button>
    <button on:click={() => key('\u0003')}>Ctrl+C</button><button on:click={() => key('\u0004')}>Ctrl+D</button>
    <button on:click={() => key('\u001b[A')} aria-label="Arrow up">↑</button><button on:click={() => key('\u001b[B')} aria-label="Arrow down">↓</button>
    <button on:click={() => key('\u001b[D')} aria-label="Arrow left">←</button><button on:click={() => key('\u001b[C')} aria-label="Arrow right">→</button>
  </div>
{/if}

{#if terminalPicker}
  <div class="terminal-picker-backdrop"><section class="terminal-picker" role="dialog" aria-modal="true" aria-labelledby="mobile-terminal-title">
    <header><h2 id="mobile-terminal-title">Open terminals</h2><button class="btn sm" on:click={() => terminalPicker = false}>Close</button></header>
    {#each sessions as session}<button class="terminal-option" class:selected={terminalID === session.id} on:click={() => selectTerminal(session)}><strong>{session.name}</strong><small>{session.cwd}</small></button>{/each}
    {#if !sessions.length}<p>Open a Code session to use the terminal keyboard.</p>{/if}
  </section></div>
{/if}
{#if error}<div class="mobile-error" role="alert">{error}<button class="btn sm" on:click={() => error = ''}>Dismiss</button></div>{/if}

<style>
  svg{width:20px;height:20px;fill:none;stroke:currentColor;stroke-width:1.7;stroke-linecap:round;stroke-linejoin:round}
  .mobile-toolbar{position:fixed;inset:0 0 auto;height:48px;z-index:950;display:flex;align-items:center;gap:10px;padding:0 128px 0 10px;background:var(--bg-panel);border-bottom:1px solid var(--border)}
  .menu-toggle{width:34px;height:38px;display:grid;place-items:center;border:0;border-radius:8px;background:transparent;color:var(--text);flex-shrink:0}.menu-toggle:active{background:var(--bg-raised)}.heading{min-width:0}.heading strong{display:block;font-size:13px;line-height:1.3;letter-spacing:-.02em}.heading small{display:block;max-width:155px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:10px;color:var(--text-dim)}
  .mobile-shade{position:fixed;inset:48px 0 0;background:var(--overlay);z-index:960;border:0;backdrop-filter:blur(3px)}
  .mobile-navigation{position:fixed;top:48px;left:0;bottom:0;width:min(300px,85vw);z-index:970;padding:20px 12px 16px;display:flex;flex-direction:column;gap:3px;overflow:auto;background:linear-gradient(160deg,var(--bg-panel),var(--bg));border-right:1px solid var(--border);box-shadow:var(--shadow-overlay);animation:slide-in 160ms var(--ease)}
  .drawer-brand{display:flex;gap:12px;align-items:center;padding:0 8px}.drawer-brand strong{font-size:20px;letter-spacing:-.035em}.drawer-brand small{display:block;color:var(--text-dim);font-size:11px}.server-address{font-size:10px;color:var(--text-dim);padding:10px 9px 4px;overflow-wrap:anywhere}.nav-group{font-size:9px;font-weight:650;text-transform:uppercase;letter-spacing:.12em;color:var(--text-dim);padding:18px 10px 6px}
  .mobile-nav-item{display:flex;align-items:center;gap:12px;min-height:43px;padding:9px 12px;border:1px solid transparent;border-radius:10px;background:transparent;color:var(--text-dim);text-align:left;font:inherit;font-weight:550}.mobile-nav-item.active{background:var(--accent-soft);border-color:color-mix(in srgb,var(--accent) 25%,transparent);color:var(--accent)}.mobile-nav-item:active{background:var(--bg-raised)}.drawer-sessions{margin-top:14px}.drawer-footer{display:flex;gap:8px;justify-content:space-between;border-top:1px solid var(--border);padding-top:14px;margin-top:14px}
  .terminal-keys{position:fixed;bottom:0;left:0;right:0;display:flex;gap:5px;overflow:auto;background:var(--bg-panel);padding:5px;z-index:900;border-top:1px solid var(--border);scrollbar-width:none}.terminal-keys button{flex-shrink:0;min-width:42px;height:38px;padding:0 10px;display:grid;place-items:center;border:1px solid var(--border);border-radius:8px;color:var(--text);background:var(--bg-raised);font:11px var(--mono)}
  .terminal-picker-backdrop{position:fixed;inset:0;z-index:1300;padding:20px;display:grid;place-items:center;background:var(--overlay)}.terminal-picker{width:min(480px,100%);padding:18px;border:1px solid var(--border);border-radius:16px;background:var(--bg-panel);box-shadow:var(--shadow-overlay);max-height:75vh;overflow:auto}.terminal-picker header{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:16px}.terminal-picker h2{margin:0;font-size:17px}.terminal-option{display:block;width:100%;text-align:left;color:var(--text);border:1px solid var(--border);border-radius:10px;padding:12px;background:var(--bg);margin-top:8px}.terminal-option.selected{border-color:var(--accent)}.terminal-option small{display:block;overflow-wrap:anywhere;color:var(--text-dim);font-size:11px;margin-top:4px}
  .mobile-error{position:fixed;bottom:60px;left:12px;right:12px;background:var(--bg-panel);border:1px solid var(--err);border-radius:12px;padding:12px;z-index:1400;display:flex;gap:12px;align-items:center}
  @keyframes slide-in{from{transform:translateX(-12px);opacity:0}to{transform:translateX(0);opacity:1}}@media(prefers-reduced-motion:reduce){.mobile-navigation{animation:none}.mobile-shade{backdrop-filter:none}}
</style>
