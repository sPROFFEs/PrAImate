<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { activePage, showToast, showConfirm } from '../lib/stores.js'

  let studioStatus = null
  let studioActionBusy = false
  let recentProjects = []
  let clis = []
  let agents = []
  let loading = true
  let error = ''

  let openProjectForm = {
    workspacePath: '',
    cli: 'praimate-code',
    model: '',
    agentID: '',
    tools: 'edits',
    busy: false
  }

  async function load() {
    loading = true
    error = ''
    try {
      const [status, recents, loadedClis, loadedAgents] = await Promise.all([
        api.studioGetStatus().catch(() => null),
        api.studioListRecentProjects().catch(() => []),
        api.listCLIs().catch(() => []),
        api.listAgents().catch(() => [])
      ])
      studioStatus = status
      recentProjects = recents || []
      clis = loadedClis || []
      agents = loadedAgents || []
      if (!openProjectForm.cli && clis.length > 0) {
        const first = clis.find(c => c.id === 'praimate-code') || clis[0]
        openProjectForm.cli = first.id
      }
    } catch (e) {
      error = String(e)
    } finally {
      loading = false
    }
  }

  async function handleStudioInstall() {
    studioActionBusy = true
    error = ''
    try {
      showToast({ title: 'Installing Studio', message: 'Downloading Code-OSS runtime and provisioning PrAImate built-in extension...', tone: 'busy' })
      studioStatus = await api.studioInstall()
      showToast({ title: 'Studio Installed', message: 'PrAImate Studio environment is ready.', tone: 'ok' })
    } catch (e) {
      error = String(e)
      showToast({ title: 'Studio Install Failed', message: String(e), tone: 'err' })
    } finally {
      studioActionBusy = false
    }
  }

  async function handleStudioUpdate() {
    studioActionBusy = true
    error = ''
    try {
      showToast({ title: 'Updating Studio', message: 'Updating integration and extensions...', tone: 'busy' })
      studioStatus = await api.studioUpdate()
      showToast({ title: 'Studio Updated', message: 'PrAImate Studio updated successfully.', tone: 'ok' })
    } catch (e) {
      error = String(e)
      showToast({ title: 'Studio Update Failed', message: String(e), tone: 'err' })
    } finally {
      studioActionBusy = false
    }
  }

  async function handleStudioRepair() {
    studioActionBusy = true
    error = ''
    try {
      showToast({ title: 'Repairing Studio', message: 'Repairing permissions and configs...', tone: 'busy' })
      studioStatus = await api.studioRepair()
      showToast({ title: 'Studio Repaired', message: 'PrAImate Studio repaired successfully.', tone: 'ok' })
    } catch (e) {
      error = String(e)
      showToast({ title: 'Studio Repair Failed', message: String(e), tone: 'err' })
    } finally {
      studioActionBusy = false
    }
  }

  async function handleOpenInStudio(folderPath) {
    if (studioStatus?.state === 'not_installed') {
      const ok = await showConfirm({
        title: 'PrAImate Studio Not Installed',
        message: 'PrAImate Studio (Code-OSS IDE) is not installed yet. Would you like to install it now?'
      })
      if (ok) {
        await handleStudioInstall()
      } else {
        return
      }
    }

    let ws = folderPath || openProjectForm.workspacePath
    if (!ws) {
      ws = await api.pickFolder()
      if (ws) openProjectForm.workspacePath = ws
      else return
    }
    openProjectForm.busy = true
    error = ''
    try {
      showToast({ title: 'Launching Studio', message: `Opening ${ws} in PrAImate Studio...`, tone: 'busy' })
      await api.studioOpenProject(
        ws,
        openProjectForm.cli,
        openProjectForm.model,
        openProjectForm.agentID,
        openProjectForm.tools
      )
      recentProjects = (await api.studioListRecentProjects()) || []
      showToast({ title: 'Studio Launched', message: `PrAImate Studio opened in ${ws}. If Studio was already running, use Developer: Reload Window to activate the updated extension.`, tone: 'ok' })
    } catch (e) {
      error = String(e)
      showToast({ title: 'Launch Failed', message: String(e), tone: 'err' })
    } finally {
      openProjectForm.busy = false
    }
  }

  async function pickOpenProjectFolder() {
    try {
      const folder = await api.pickFolder()
      if (folder) {
        openProjectForm.workspacePath = folder
        error = ''
      }
    } catch (e) {
      error = String(e)
    }
  }

  onMount(load)
</script>

<div class="row" style="margin-bottom:4px; align-items:center">
  <h1 class="grow" style="margin:0">PrAImate Studio</h1>
  <button class="btn" on:click={load} disabled={loading}>{loading ? 'Refreshing…' : 'Refresh'}</button>
</div>
<p class="subtitle">Dedicated Code-OSS IDE integrated natively with PrAImate Core agents, workflows, chats, MCP, and PrAImate Code.</p>

{#if error}<div class="banner">{error}</div>{/if}

<!-- NOT INSTALLED BANNER -->
{#if studioStatus && (studioStatus.state === 'not_installed' || studioStatus.state === 'broken')}
  <div class="card not-installed-card" style="margin-bottom: 20px; border-left: 4px solid var(--warn, #d29922);">
    <strong style="font-size: 15px;">PrAImate Studio (Code-OSS IDE) is not installed</strong>
    <p class="card-sub" style="margin: 6px 0 14px;">
      Install the managed Code-OSS development environment (~100MB download) with pre-configured PrAImate Core integration, Open VSX marketplace, terminal, and agent execution.
    </p>
    <div class="row" style="gap: 8px;">
      <button class="btn primary" type="button" disabled={studioActionBusy} on:click={handleStudioInstall}>
        {studioActionBusy ? 'Installing Studio…' : 'Install PrAImate Studio'}
      </button>
      <button class="btn" type="button" on:click={() => activePage.set('clis')}>
        Go to CLI & Tools
      </button>
    </div>
  </div>
{/if}

<!-- 1. STUDIO MANAGEMENT STATUS CARD -->
<div class="card" style="margin-bottom: 20px; border-left: 4px solid var(--accent);">
  <div class="row" style="align-items: flex-start; justify-content: space-between; flex-wrap: wrap; gap: 16px;">
    <div>
      <div class="row" style="gap: 10px; align-items: center;">
        <h2 style="margin: 0; font-size: 17px;">PrAImate Studio</h2>
        {#if studioStatus?.state === 'installed'}
          <span class="badge" style="background: rgba(46, 160, 67, 0.2); color: var(--ok, #3fb950); font-weight: 600;">✓ Installed</span>
        {:else if studioStatus?.state === 'update_available'}
          <span class="badge" style="background: rgba(219, 171, 9, 0.2); color: var(--warn, #d29922); font-weight: 600;">⚙ Update Available</span>
        {:else if studioStatus?.state === 'broken'}
          <span class="badge" style="background: rgba(248, 81, 73, 0.2); color: var(--err, #f85149); font-weight: 600;">🔧 Needs Repair</span>
        {:else}
          <span class="badge" style="background: var(--bg-surface); color: var(--text-dim); font-weight: 600;">Not Installed</span>
        {/if}
      </div>
      <div class="card-sub" style="margin-top: 6px;">
        Version: <strong>{studioStatus?.version || '0.1.0'}</strong> ·
        Base: <strong>{studioStatus?.codeOssVersion || 'Code-OSS'}</strong> ·
        Protocol: <strong>{studioStatus?.protocolVersion || '1'}</strong> ·
        Daemon: <strong>{studioStatus?.backendRunning ? '● Active' : '○ Standby'}</strong>
      </div>
    </div>

    <div class="row" style="gap: 8px;">
      <button
        class="btn primary"
        type="button"
        disabled={studioActionBusy}
        on:click={() => handleOpenInStudio('')}>
        Open Studio
      </button>
      {#if studioStatus?.state === 'not_installed'}
        <button class="btn" type="button" disabled={studioActionBusy} on:click={handleStudioInstall}>
          {studioActionBusy ? 'Installing…' : 'Install'}
        </button>
      {:else}
        <button class="btn" type="button" disabled={studioActionBusy} on:click={handleStudioUpdate}>
          {studioActionBusy ? 'Updating…' : 'Update'}
        </button>
        <button class="btn" type="button" disabled={studioActionBusy} on:click={handleStudioRepair}>
          {studioActionBusy ? 'Repairing…' : 'Repair'}
        </button>
      {/if}
    </div>
  </div>
</div>

<!-- 2. OPEN PROJECT IN STUDIO -->
<div class="card" style="margin-bottom: 24px;">
  <h2 style="margin: 0 0 4px; font-size: 16px;">Open Project</h2>
  <div class="card-sub" style="margin-bottom: 14px;">Launch a workspace project in PrAImate Studio with your preferred CLI and agent configuration.</div>

  <label class="lbl" for="open-proj-ws">Workspace Folder *</label>
  <div class="row" style="margin-bottom: 12px;">
    <input id="open-proj-ws" class="field grow mono" bind:value={openProjectForm.workspacePath} placeholder="/path/to/project" />
    <button class="btn" type="button" on:click={pickOpenProjectFolder}>Browse…</button>
  </div>

  <div class="row" style="gap: 16px; margin-bottom: 12px; flex-wrap: wrap;">
    <div style="flex: 1; min-width: 180px;">
      <label class="lbl" for="open-proj-cli">CLI Engine</label>
      <select id="open-proj-cli" class="field" style="width: 100%;" bind:value={openProjectForm.cli}>
        {#each clis as c}
          <option value={c.id}>{c.label || c.id} {c.available ? '' : '(not installed)'}</option>
        {/each}
        {#if !clis.some(c => c.id === 'praimate-code')}
          <option value="praimate-code">PrAImate Code</option>
        {/if}
      </select>
    </div>

    <div style="flex: 1; min-width: 180px;">
      <label class="lbl" for="open-proj-agent">Agent Persona</label>
      <select id="open-proj-agent" class="field" style="width: 100%;" bind:value={openProjectForm.agentID}>
        <option value="">No persona (Direct CLI)</option>
        {#each agents as a}
          <option value={a.id}>{a.name}</option>
        {/each}
      </select>
    </div>

    <div style="flex: 1; min-width: 180px;">
      <label class="lbl" for="open-proj-tools">Permissions</label>
      <select id="open-proj-tools" class="field" style="width: 100%;" bind:value={openProjectForm.tools}>
        <option value="safe">Safe (Read-only)</option>
        <option value="edits">Edits (Allowed changes)</option>
        <option value="full">Full (Autonomous)</option>
      </select>
    </div>
  </div>

  <div class="row" style="justify-content: flex-end; margin-top: 14px;">
    <button class="btn primary" type="button" disabled={openProjectForm.busy} on:click={() => handleOpenInStudio('')}>
      {openProjectForm.busy ? 'Opening…' : 'Open in Studio'}
    </button>
  </div>
</div>

<!-- 3. RECENT PROJECTS -->
{#if recentProjects.length > 0}
  <div style="margin-bottom: 24px;">
    <h2 style="font-size: 15px; margin: 0 0 10px;">Recent Projects</h2>
    <div style="display: flex; flex-direction: column; gap: 6px;">
      {#each recentProjects as projectPath}
        <div class="card row" style="padding: 10px 14px; align-items: center; justify-content: space-between;">
          <div class="mono" style="font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 75%;">
            <strong>{projectPath.split(/[\\/]/).pop()}</strong>
            <span class="card-sub" style="margin-left: 8px;">{projectPath}</span>
          </div>
          <button class="btn sm" type="button" on:click={() => handleOpenInStudio(projectPath)}>
            Open in Studio
          </button>
        </div>
      {/each}
    </div>
  </div>
{/if}

<style>
  .subtitle { color: var(--text-dim); margin-top: 0; margin-bottom: 16px; font-size: 13px; }
  .banner { background: var(--err-soft, rgba(248, 81, 73, 0.15)); border: 1px solid var(--err, #f85149); color: var(--text); padding: 8px 12px; border-radius: 6px; margin-bottom: 12px; white-space: pre-wrap; font-size: 13px; }
  .badge { background: var(--bg-surface); padding: 2px 8px; border-radius: 4px; font-size: 11px; }
</style>
