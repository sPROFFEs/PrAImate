<script>
  import { onMount, onDestroy, tick } from 'svelte'
  import { api } from '../lib/api.js'
  import { showToast } from '../lib/stores.js'
  import {
    ACCENT_PRESETS,
    themeMode,
    accentColor,
    setThemeMode,
    setAccent,
  } from '../lib/theme.js'

  const themeModes = [
    { id: 'light', label: 'Light' },
    { id: 'dark', label: 'Dark' },
    { id: 'system', label: 'System' },
  ]

  // Update check (Settings parity with `praimate -update`'s probe).
  let updateInfo = null
  let checkingUpdate = false
  async function checkUpdate() {
    checkingUpdate = true
    try {
      updateInfo = await api.checkUpdate()
    } catch (e) {
      error = String(e)
    } finally {
      checkingUpdate = false
    }
  }

  let error = ''
  let agents = []
  let storedData = null
  let deleteModal = false
  let deleteProjects = false
  let deleteProjectsRoot = ''
  let deletePhrase = ''
  let deleteUnderstood = false
  let deletingData = false
  let passwordMsg = ''

  async function requirePasswordNextLaunch() {
    passwordMsg = ''
    error = ''
    try {
      await api.forgetDatabasePassword()
      passwordMsg = 'Password will be required next launch.'
    } catch (e) {
      error = String(e)
    }
  }

  // Backup (git sync of the workspaces root)
  let bk = null            // BackupState
  let bkBusy = ''          // label of the in-flight op, '' = idle
  let bkMsg = ''           // last op result line
  let bkRemote = ''        // remote URL input
  let bkDiverged = null    // {localCommits, remoteCommits} when resolution needed
  let bkSetupMode = 'new'  // 'new' | 'existing'

  function fmtDate(s) {
    try { return new Date(s).toLocaleString() } catch { return s }
  }

  async function bkLoad() {
    try {
      bk = await api.backupStatus()
      bkRemote = bk?.remoteUrl || ''
    } catch (e) {
      bk = null
      error = String(e)
    }
  }

  async function bkOp(label, fn) {
    if (bkBusy) return
    bkBusy = label
    bkMsg = ''
    error = ''
    try {
      const res = await fn()
      if (res) { bk = res; bkRemote = bk.remoteUrl || '' }
      bkDiverged = null
      bkMsg = label + ' ✓'
    } catch (e) {
      error = String(e)
    } finally {
      bkBusy = ''
    }
  }

  async function bkSync() {
    if (bkBusy) return
    bkBusy = 'Sync'
    bkMsg = ''
    error = ''
    bkDiverged = null
    try {
      const res = await api.backupSyncNow()
      applyBackupResult(res)
    } catch (e) {
      error = String(e)
    } finally {
      bkBusy = ''
    }
  }

  function applyBackupResult(res) {
    bk = res.state
    bkRemote = bk.remoteUrl || ''
    if (res.action === 'diverged') {
      bkDiverged = { local: res.localCommits || [], remote: res.remoteCommits || [] }
      bkMsg = 'Local and remote histories differ — choose how to reconcile them below.'
    } else {
      bkMsg = {
        in_sync: 'In sync ✓',
        pushed: 'Pushed local changes ✓',
        pulled: 'Pulled remote changes ✓',
        no_remote: 'Local backup created. Add a remote when you are ready.',
      }[res.action] || res.action
    }
  }

  async function bkConfigure() {
    if (bkBusy) return
    bkBusy = 'Configure'
    bkMsg = ''
    error = ''
    bkDiverged = null
    showToast({
      title: 'Configuring Git backup',
      message: bkSetupMode === 'existing'
        ? 'Testing the remote and comparing local and remote history…'
        : 'Creating the local backup repository and checking the remote…',
      tone: 'busy',
      duration: 0,
      dismissible: false,
    })
    try {
      const res = await api.configureBackup(bkSetupMode, bkRemote.trim())
      applyBackupResult(res)
      // Let Svelte paint the divergence panel before replacing the persistent
      // progress toast with the result notification.
      await tick()
      if (res.action === 'diverged') {
        showToast({
          title: 'Backup connected — action required',
          message: 'Local and remote histories differ. Review the changes and choose a reconciliation option below.',
          tone: 'ok',
          duration: 7000,
        })
      } else {
        showToast({ title: 'Backup configured', message: bkMsg, tone: 'ok' })
      }
    } catch (e) {
      error = String(e)
      showToast({ title: 'Backup configuration failed', message: String(e), tone: 'err', duration: 0 })
      await bkLoad()
    } finally {
      bkBusy = ''
    }
  }

  async function bkTest() {
    if (bkBusy) return
    bkBusy = 'Test'
    bkMsg = ''
    error = ''
    showToast({
      title: 'Testing Git remote',
      message: 'Checking connectivity, authentication, and the default branch…',
      tone: 'busy',
      duration: 0,
      dismissible: false,
    })
    try {
      const branch = await api.testBackupRemote(bkRemote)
      bkMsg = `Connection OK — default branch: ${branch}`
      showToast({
        title: 'Remote connection successful',
        message: `Git can access this repository. Default branch: ${branch}`,
        tone: 'ok',
      })
    } catch (e) {
      error = String(e)
      showToast({ title: 'Remote connection failed', message: String(e), tone: 'err', duration: 0 })
    } finally {
      bkBusy = ''
    }
  }

  async function load() {
    try {
      agents = (await api.listAgents()) || []
      error = ''
    } catch (e) {
      error = String(e)
    }
    await bkLoad()
    try {
      storedData = await api.storedDataInfo()
      deleteProjectsRoot = storedData?.projectsRoot || ''
    } catch (e) {
      error = String(e)
    }
  }

  async function chooseDeleteProjectsRoot() {
    try {
      const picked = await api.pickFolder()
      if (picked) deleteProjectsRoot = picked
    } catch (e) {
      error = String(e)
    }
  }

  function openDeleteModal() {
    deleteProjects = false
    deleteProjectsRoot = storedData?.projectsRoot || ''
    deletePhrase = ''
    deleteUnderstood = false
    deleteModal = true
  }

  async function deleteAllData() {
    if (deletingData || !deleteUnderstood || deletePhrase !== storedData?.phrase) return
    const scope = deleteProjects
      ? `PrAImate data and the projects folder:\n${deleteProjectsRoot}`
      : 'all PrAImate application data (the projects folder will be kept)'
    if (!confirm(`Final confirmation: permanently delete ${scope}?\n\nPrAImate will close and start clean next time.`)) return
    deletingData = true
    error = ''
    try {
      await api.deleteAllStoredData(deleteProjects ? deleteProjectsRoot : '', deletePhrase)
    } catch (e) {
      error = String(e)
      deletingData = false
    }
  }

  // Build tools from source — for platforms where we don't ship a
  // prebuilt bundle (praimate-code off Linux, graphify off linux/amd64).
  let buildInfo = {} // tool -> BuildToolInfo
  let building = '' // tool currently compiling, '' = idle
  let buildLog = []
  let buildUnsub = () => {}

  let prereqModal = null // { name, detail }

  function showPrereqModal(r) {
    prereqModal = r
  }

  function closePrereqModal() {
    prereqModal = null
  }

  async function loadBuildInfo() {
    const next = {}
    for (const t of ['praimate-code', 'graphify']) {
      try { next[t] = await api.buildRequirements(t) } catch (e) { /* ignore */ }
    }
    buildInfo = next
  }

  async function buildTool(t) {
    if (building) return
    building = t
    buildLog = []
    try {
      await api.buildToolFromSource(t)
      buildLog = [...buildLog, '✓ finished']
      await loadBuildInfo()
    } catch (e) {
      buildLog = [...buildLog, '✗ ' + String(e)]
    } finally {
      building = ''
    }
  }

  onMount(async () => {
    await load()
    await loadBuildInfo()
    if (typeof window !== 'undefined' && window.runtime?.EventsOn) {
      window.runtime.EventsOn('praimate:install', (ev) => {
        if (ev && typeof ev.cli === 'string' && ev.cli.startsWith('build:')) {
          buildLog = [...buildLog.slice(-400), ev.line]
        }
      })
      buildUnsub = () => window.runtime.EventsOff('praimate:install')
    }
  })
  onDestroy(() => buildUnsub())
</script>

<h1>Settings</h1>
<p class="subtitle">Automation, privacy, backup, appearance, and updates.</p>

{#if error}<div class="banner">{error}</div>{/if}

<h1 style="font-size:16px">Updates</h1>
<div class="card">
  <div class="row">
    <div class="grow">
      <div class="card-title">PrAImate version</div>
      <div class="card-sub">
        {#if updateInfo}
          {#if updateInfo.hasUpdate}
            v{updateInfo.current} → <strong>v{updateInfo.latest} available</strong> — run <span class="mono">praimate -update</span> (refreshes the GUI binary too), or download: <span class="mono">{updateInfo.url}</span>
          {:else}
            v{updateInfo.current} — up to date
          {/if}
        {:else}
          Check GitHub for a newer release.
        {/if}
      </div>
    </div>
    <button class="btn" on:click={checkUpdate} disabled={checkingUpdate}>{checkingUpdate ? 'Checking…' : 'Check for updates'}</button>
  </div>
</div>

<h1 style="font-size:16px; margin-top:24px">Build bundled tools from source</h1>
<p class="subtitle" style="margin-top:-6px">
  On platforms where we don't ship a prebuilt binary, build it locally from our repo.
  We clone the source, compile, install it into <span class="mono">&lt;PrAImate data folder&gt;/bin</span>, and delete the temporary checkout.
</p>
{#each [{ id: 'praimate-code', name: 'PrAImate Code' }, { id: 'graphify', name: 'Graphify (RAG)' }] as t}
  {@const info = buildInfo[t.id]}
  <div class="card">
    <div class="row" style="align-items:flex-start">
      <div class="grow">
        <div class="card-title">{t.name}</div>
        <div class="card-sub">{info?.note || 'Compile this tool locally from source.'}</div>
        {#if info}
          <div class="row" style="gap:6px; flex-wrap:wrap; margin-top:8px">
            {#each info.requirements as r}
              <!-- svelte-ignore a11y-click-events-have-key-events -->
              <!-- svelte-ignore a11y-no-static-element-interactions -->
              <span
                class="pill {r.found ? 'ok' : 'err'}"
                title={r.detail}
                style="cursor: pointer"
                on:click={() => showPrereqModal(r)}
              >
                {r.found ? '✓' : '✗'} {r.name}
              </span>
            {/each}
          </div>
          {#if !info.ready}
            <div class="card-sub" style="margin-top:6px; color: var(--warn)">
              Install the missing tool(s) above, then re-open Settings.
            </div>
          {/if}
        {/if}
      </div>
      <button
        class="btn primary"
        on:click={() => buildTool(t.id)}
        disabled={!info || !info.ready || !!building}>
        {building === t.id ? 'Building…' : 'Build from source'}
      </button>
    </div>
  </div>
{/each}
{#if buildLog.length}
  <pre class="build-log">{buildLog.join('\n')}</pre>
{/if}

<h1 style="font-size:16px; margin-top:24px">Appearance</h1>
<div class="card">
  <div class="row" style="justify-content: space-between">
    <div>
      <div class="card-title">Theme</div>
      <div class="card-sub">Light, dark, or follow the OS preference.</div>
    </div>
    <div class="toggle-group">
      {#each themeModes as m}
        <button class:active={$themeMode === m.id} on:click={() => setThemeMode(m.id)}>
          {m.label}
        </button>
      {/each}
    </div>
  </div>
  <div class="row" style="justify-content: space-between; margin-top: 12px">
    <div>
      <div class="card-title">Accent</div>
      <div class="card-sub">Used for primary buttons and focused fields.</div>
    </div>
    <div class="swatch-row">
      {#each ACCENT_PRESETS as preset}
        <button
          class="swatch"
          class:active={preset.color
            ? $accentColor.toLowerCase() === preset.color.toLowerCase()
            : $accentColor === 'default'}
          style={preset.color ? `background:${preset.color}` : ''}
          title={preset.label}
          on:click={() => setAccent(preset.color ?? 'default')}>
          {#if !preset.color}
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9" /><path d="M5.6 5.6l12.8 12.8" /></svg>
          {/if}
        </button>
      {/each}
    </div>
  </div>
</div>

<h1 style="font-size:16px; margin-top:24px">Backup — git sync</h1>
<p class="subtitle">Mirrors workspaces and a password-encrypted database snapshot to a git remote.</p>

{#if !bk}
  <div class="card card-sub">Loading backup status…</div>
{:else if !bk.supported}
  <div class="card card-sub">No workspaces root configured yet — complete first-run setup.</div>
{:else}
  <div class="card">
    <div class="row" style="justify-content: space-between">
      <div>
        <div class="card-title">Git backup</div>
        <div class="card-sub">
          {#if bk.enabled && bk.initialized}
            {bk.branch || 'main'}
            {#if bk.ahead || bk.behind} · ↑{bk.ahead} ↓{bk.behind}{:else} · in sync{/if}
            {#if bk.lastCommit} · {bk.lastCommit}{/if}
          {:else if bk.enabled}
            enabled, repo not initialised yet
          {:else if bk.initialized}
            paused — repository and history remain on disk
          {:else}
            not configured
          {/if}
        </div>
      {#if bk.lastSyncAt}<div class="card-sub">last sync: {fmtDate(bk.lastSyncAt)}{#if bk.machineId} · machine: <span class="mono">{bk.machineId}</span>{/if}</div>{/if}
      <div class="card-sub" style="margin-top:6px">
        Database snapshots use your PrAImate database password. Restoring on another system requires the same password; workspace files remain normal Git content.
      </div>
      </div>
      {#if bk.initialized}
        <button class="btn" class:primary={bk.enabled} disabled={!!bkBusy}
          on:click={() => bkOp(bk.enabled ? 'Disable' : 'Enable', () => api.setBackupEnabled(!bk.enabled))}>
          {bk.enabled ? 'Disable' : 'Enable'}
        </button>
      {/if}
    </div>

    {#if bk.enabled}
      <label class="lbl" for="backup-remote-url">Remote URL (HTTPS or SSH — uses your Git credentials)</label>
      <div class="row">
        <input id="backup-remote-url" class="field grow mono" placeholder="git@github.com:you/praimate-backup.git" bind:value={bkRemote} />
        <button class="btn" disabled={!!bkBusy || !bkRemote.trim()} on:click={bkTest}>Test</button>
        <button class="btn" disabled={!!bkBusy} on:click={() => bkOp('Save remote', () => api.setBackupRemote(bkRemote))}>Save</button>
      </div>

      <div class="row" style="margin-top:12px">
        <button class="btn primary" disabled={!!bkBusy || !bk.remoteUrl} on:click={bkSync}>
          {bkBusy === 'Sync' ? 'Syncing…' : 'Sync now'}
        </button>
        <button class="btn" disabled={!!bkBusy}
          on:click={() => bkOp(bk.autoSync ? 'Auto-sync off' : 'Auto-sync on', () => api.setBackupAutoSync(!bk.autoSync))}>
          Auto-sync: {bk.autoSync ? 'on' : 'off'}
        </button>
        {#if bk.autoSync}
          <button class="btn" disabled={!!bkBusy}
            on:click={() => bkOp(bk.forceLocal ? 'Force-local off' : 'Force-local on', () => api.setBackupForceLocal(!bk.forceLocal))}>
            Force local: {bk.forceLocal ? 'on' : 'off'}
          </button>
        {/if}
        <div class="grow"></div>
        <button class="btn danger" disabled={!!bkBusy || !bk.remoteUrl}
          on:click={() => confirm('Force-push local state over the remote?') && bkOp('Force push', api.backupForcePush)}>Force push</button>
        <button class="btn danger" disabled={!!bkBusy || !bk.remoteUrl}
          on:click={() => confirm('Discard ALL local changes and reset to the remote?') && confirm('Really? Local commits and uncommitted changes will be lost.') && bkOp('Reset from remote', api.backupResetFromRemote)}>Reset from remote</button>
        <button class="btn danger" disabled={!!bkBusy || !bk.remoteUrl}
          on:click={() => bkOp('Disconnect', api.backupDisconnect)}>Disconnect</button>
      </div>

      {#if bkMsg}<div class="card-sub" style="margin-top:8px">{bkMsg}</div>{/if}

      {#if bkDiverged}
        <div class="card" style="margin-top:10px; border-color: var(--warn)">
          <div class="card-title">Diverged — local and remote both have new commits</div>
          <div class="row" style="align-items: flex-start; margin-top:8px">
            <div class="grow">
              <div class="card-sub" style="font-weight:600">Local only</div>
              {#each bkDiverged.local as c}<div class="mono card-sub">{c}</div>{/each}
              {#if bkDiverged.local.length === 0}<div class="card-sub">(none)</div>{/if}
            </div>
            <div class="grow">
              <div class="card-sub" style="font-weight:600">Remote only</div>
              {#each bkDiverged.remote as c}<div class="mono card-sub">{c}</div>{/each}
              {#if bkDiverged.remote.length === 0}<div class="card-sub">(none)</div>{/if}
            </div>
          </div>
          <div class="row" style="margin-top:10px">
            <button class="btn primary" disabled={!!bkBusy} on:click={() => bkOp('Merge', () => api.resolveBackupDivergence('merge'))}>Merge (keep both)</button>
            <button class="btn" disabled={!!bkBusy} on:click={() => bkOp('Rebase', () => api.resolveBackupDivergence('rebase'))}>Rebase local on remote</button>
            <button class="btn danger" disabled={!!bkBusy}
              on:click={() => confirm('Discard the remote-only commits?') && bkOp('Force push', () => api.resolveBackupDivergence('forcepush'))}>Force push (keep local)</button>
            <button class="btn danger" disabled={!!bkBusy}
              on:click={() => confirm('Discard the local-only commits?') && bkOp('Reset', () => api.resolveBackupDivergence('reset'))}>Reset (keep remote)</button>
          </div>
        </div>
      {/if}
    {:else if !bk.initialized}
      <div class="setup-divider"></div>
      <div class="card-title">Choose how to set up backup</div>
      <div class="setup-options">
        <button class="setup-option" class:selected={bkSetupMode === 'new'} on:click={() => (bkSetupMode = 'new')}>
          <strong>Start a new backup</strong>
          <span>Create local Git history from the current workspace. A remote URL is optional.</span>
        </button>
        <button class="setup-option" class:selected={bkSetupMode === 'existing'} on:click={() => (bkSetupMode = 'existing')}>
          <strong>Connect an existing backup</strong>
          <span>Attach an existing remote, fetch it, and compare it with the current workspace.</span>
        </button>
      </div>

      <label class="lbl" for="backup-setup-remote">
        Remote URL {bkSetupMode === 'new' ? '(optional)' : '(required)'}
      </label>
      <div class="row">
        <input id="backup-setup-remote" class="field grow mono" placeholder="git@github.com:you/praimate-backup.git" bind:value={bkRemote} />
        <button class="btn" disabled={!!bkBusy || !bkRemote.trim()} on:click={bkTest}>Test</button>
      </div>
      {#if bkSetupMode === 'existing'}
        <p class="setup-note">PrAImate will not overwrite either side automatically. If local and remote histories differ, you will choose whether to merge, rebase, keep local, or keep remote.</p>
      {:else}
        <p class="setup-note">This creates commits only inside the configured workspaces folder. It does not change your global Git identity.</p>
      {/if}
      <button class="btn primary" disabled={!!bkBusy || (bkSetupMode === 'existing' && !bkRemote.trim())} on:click={bkConfigure}>
        {bkBusy === 'Configure' ? 'Configuring…' : (bkSetupMode === 'existing' ? 'Connect and compare' : 'Create backup')}
      </button>
    {/if}
  </div>
{/if}

<h1 style="font-size:16px; margin-top:24px">Data and privacy</h1>
<div class="card">
  <div class="row" style="align-items:flex-start">
    <div class="grow">
      <div class="card-title">PrAImate storage</div>
      <div class="card-sub">
        Application data, encrypted database, managed tools, agents, and settings are kept under one folder.
      </div>
      {#if storedData}
        <div class="storage-path"><span>Application data</span><span class="mono">{storedData.dataRoot}</span></div>
        <div class="storage-path"><span>Projects</span><span class="mono">{storedData.projectsRoot || 'not configured'}</span></div>
      {/if}
      <div class="card-sub" style="margin-top:8px">
        API keys saved by PrAImate live in the encrypted database. CLI-owned session data and configuration remain under each CLI's own folders.
      </div>
      <div class="card-sub" style="margin-top:8px">
        The raw database key exists only while PrAImate is unlocked. If automatic unlock was enabled, you can remove its OS-protected credential now.
      </div>
    </div>
    <div style="display:flex; gap:8px; flex-wrap:wrap; justify-content:flex-end">
      <button class="btn" on:click={requirePasswordNextLaunch}>
        Require password next launch
      </button>
      <button class="btn danger" on:click={openDeleteModal} disabled={!storedData}>Delete all stored data…</button>
    </div>
  </div>
  {#if passwordMsg}<div class="card-sub" style="margin-top:8px">{passwordMsg}</div>{/if}
</div>

<style>
  .setup-divider {
    margin: 16px 0;
    border-top: 1px solid var(--border);
  }
  .setup-options {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
    margin: 10px 0 14px;
  }
  .setup-option {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-height: 76px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text);
    text-align: left;
    cursor: pointer;
    font-family: var(--sans);
  }
  .setup-option:hover { background: var(--bg-raised); }
  .setup-option.selected {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .setup-option span {
    color: var(--text-dim);
    font-size: 12px;
    line-height: 1.45;
  }
  .setup-note {
    margin: 8px 0 12px;
    color: var(--text-dim);
    font-size: 12px;
    line-height: 1.5;
  }
  @media (max-width: 700px) {
    .setup-options { grid-template-columns: 1fr; }
  }
  .storage-path {
    display: grid;
    grid-template-columns: 120px minmax(0, 1fr);
    gap: 10px;
    margin-top: 8px;
    font-size: 12px;
    color: var(--text-dim);
  }
  .storage-path .mono {
    overflow-wrap: anywhere;
    color: var(--text);
  }
  .danger-panel {
    border: 1px solid color-mix(in srgb, var(--err) 55%, var(--border));
    background: var(--bg-panel);
    box-shadow:
      0 24px 80px rgba(0, 0, 0, .58),
      0 0 0 1px color-mix(in srgb, var(--err) 12%, transparent);
  }
  .destructive-backdrop {
    background: rgba(0, 0, 0, .72);
    backdrop-filter: blur(4px);
  }
  .danger-copy {
    line-height: 1.55;
    color: var(--text-dim);
    font-size: 13px;
  }
  .confirm-line {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    margin-top: 12px;
    font-size: 13px;
    line-height: 1.4;
  }
</style>

{#if deleteModal}
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <div class="modal-backdrop destructive-backdrop" on:click|self={() => !deletingData && (deleteModal = false)}>
    <div class="modal-content danger-panel" role="dialog" aria-modal="true" aria-labelledby="delete-data-title">
      <h2 id="delete-data-title">Delete all PrAImate data</h2>
      <p class="danger-copy">
        This permanently deletes the encrypted database, its password-protected key envelope, settings, agents,
        skills, MCP credentials, PrAImate-managed tools, and managed-run requests, checkpoints, memory, and artifacts. Managed PrAImate routing is removed
        from supported CLI configuration without deleting unrelated CLI data.
      </p>

      <label class="confirm-line">
        <input type="checkbox" bind:checked={deleteProjects} />
        <span>Also permanently delete the configured projects folder.</span>
      </label>
      {#if deleteProjects}
        <label class="lbl" for="delete-projects-root">Projects folder (must match the configured folder exactly)</label>
        <div class="row">
          <input id="delete-projects-root" class="field grow mono" bind:value={deleteProjectsRoot} />
          <button class="btn" on:click={chooseDeleteProjectsRoot} disabled={deletingData}>Choose…</button>
        </div>
      {/if}

      <label class="confirm-line">
        <input type="checkbox" bind:checked={deleteUnderstood} />
        <span>I understand this cannot be undone and PrAImate will close immediately.</span>
      </label>
      <label class="lbl" for="delete-confirm-phrase">Type <span class="mono">{storedData.phrase}</span></label>
      <input id="delete-confirm-phrase" class="field mono" bind:value={deletePhrase} autocomplete="off" />

      <div class="row" style="margin-top:16px; justify-content:flex-end">
        <button class="btn" on:click={() => (deleteModal = false)} disabled={deletingData}>Cancel</button>
        <button
          class="btn danger"
          on:click={deleteAllData}
          disabled={deletingData || !deleteUnderstood || deletePhrase !== storedData.phrase || (deleteProjects && !deleteProjectsRoot.trim())}>
          {deletingData ? 'Deleting…' : 'Continue to final confirmation'}
        </button>
      </div>
    </div>
  </div>
{/if}

{#if prereqModal}
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <div class="modal-backdrop" on:click={closePrereqModal}>
    <div class="modal-content" on:click|stopPropagation>
      <h2>How to install {prereqModal.name}</h2>
      <p class="subtitle" style="margin-bottom:12px">{prereqModal.detail}</p>
      
      {#if prereqModal.name === 'git'}
        <div class="code-block">
          <strong>Linux (Debian/Ubuntu):</strong> <span class="mono">sudo apt install git</span><br/>
          <strong>Windows:</strong> <span class="mono">winget install --id Git.Git -e --source winget</span>
        </div>
      {:else if prereqModal.name === 'bun'}
        <div class="code-block">
          <strong>Linux:</strong> <span class="mono">curl -fsSL https://bun.sh/install | bash</span><br/>
          <strong>Windows:</strong> <span class="mono">powershell -c "irm bun.sh/install.ps1 | iex"</span>
        </div>
      {:else if prereqModal.name === 'uv'}
        <div class="code-block">
          <strong>Linux:</strong> <span class="mono">curl -LsSf https://astral.sh/uv/install.sh | sh</span><br/>
          <strong>Windows:</strong> <span class="mono">powershell -ExecutionPolicy ByPass -c "irm https://astral.sh/uv/install.ps1 | iex"</span>
        </div>
      {:else if prereqModal.name === 'bash'}
        <div class="code-block">
          <strong>Windows:</strong> Download Git for Windows and ensure "Git Bash" is in your PATH.
        </div>
      {/if}
      
      <div class="row" style="margin-top:16px; justify-content:flex-end">
        <button class="btn" on:click={closePrereqModal}>Close</button>
      </div>
    </div>
  </div>
{/if}
