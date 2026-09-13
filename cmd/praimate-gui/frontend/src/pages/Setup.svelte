<script>
  // First-run setup — shown instead of the app when no launcher config
  // exists. Three options: empty workspaces root,
  // root + bundled samples, or clone an existing backup remote.
  import { createEventDispatcher } from 'svelte'
  import { api } from '../lib/api.js'
  import logo from '../assets/monke-icon.png'

  export let defaultRoot = ''

  const dispatch = createEventDispatcher()

  let root = defaultRoot
  let samples = true
  let agents = true
  let mode = 'new' // 'new' | 'clone'
  let cloneURL = ''
  let busy = false
  let error = ''
  let probeMsg = ''

  async function browse() {
    try {
      const p = await api.pickFolder()
      if (p) root = p
    } catch (e) {
      error = String(e)
    }
  }

  async function testRemote() {
    probeMsg = ''
    error = ''
    try {
      await api.testBackupRemote(cloneURL.trim())
      probeMsg = '✓ remote reachable'
    } catch (e) {
      error = String(e)
    }
  }

  async function go() {
    if (busy) return
    busy = true
    error = ''
    try {
      await api.completeFirstRun(root.trim(), samples, agents, mode === 'clone' ? cloneURL.trim() : '')
      dispatch('done')
    } catch (e) {
      error = String(e)
    } finally {
      busy = false
    }
  }
</script>

<div class="setup">
  <div class="setup-card">
    <div class="setup-brand">
      <img src={logo} alt="PrAImate" />
      <div>
        <h1 style="margin:0">Welcome to PrAImate</h1>
        <p class="subtitle" style="margin:4px 0 0">One-minute setup — where should your chats and templates live?</p>
      </div>
    </div>

    {#if error}<div class="banner">{error}</div>{/if}

    <label class="lbl">Workspaces folder</label>
    <div class="row">
      <input class="field grow mono" bind:value={root} placeholder={defaultRoot} />
      <button class="btn" on:click={browse}>Browse…</button>
    </div>

    <label class="lbl" style="margin-top:14px">Start from</label>
    <div class="row">
      <button class="btn" class:primary={mode === 'new'} on:click={() => (mode = 'new')}>A fresh folder</button>
      <button class="btn" class:primary={mode === 'clone'} on:click={() => (mode = 'clone')}>Clone an existing backup</button>
    </div>

    {#if mode === 'new'}
      <label class="row" style="margin-top:12px; gap:8px; cursor:pointer">
        <input type="checkbox" bind:checked={samples} />
        <span>Seed the bundled sample templates (recommended for a first look)</span>
      </label>
      <label class="row" style="margin-top:8px; gap:8px; cursor:pointer">
        <input type="checkbox" bind:checked={agents} />
        <span>Import the starter agents (Reverse Ghidra, Code Review, Dev Team, Security Review, Agent Builder)</span>
      </label>
    {:else}
      <label class="lbl" style="margin-top:12px">Backup remote (git URL — HTTPS or SSH)</label>
      <div class="row">
        <input class="field grow mono" bind:value={cloneURL} placeholder="git@github.com:you/praimate-backup.git" />
        <button class="btn" on:click={testRemote} disabled={!cloneURL.trim()}>Test</button>
      </div>
      {#if probeMsg}<div class="card-sub" style="margin-top:4px">{probeMsg}</div>{/if}
      <p class="subtitle" style="margin-top:6px">
        Restores chats, agents, templates and settings from the remote. If it contains an encrypted database,
        PrAImate will next ask for that backup's existing password. Do not create a different password.
      </p>
    {/if}

    <div class="row" style="margin-top:20px">
      <button class="btn primary" on:click={go} disabled={busy || !root.trim() || (mode === 'clone' && !cloneURL.trim())}>
        {busy ? 'Setting up…' : mode === 'clone' ? 'Restore backup & continue' : 'Create & continue'}
      </button>
    </div>
  </div>
</div>

<style>
  .setup {
    height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--bg);
  }
  .setup-card {
    width: 560px;
    max-width: 92vw;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 26px 28px;
  }
  .setup-brand {
    display: flex;
    gap: 14px;
    align-items: center;
    margin-bottom: 18px;
  }
  .setup-brand img { width: 44px; height: 44px; border-radius: 10px; }
</style>
