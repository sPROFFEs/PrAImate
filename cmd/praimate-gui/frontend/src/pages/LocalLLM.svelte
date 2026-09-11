<script>
  // Local LLM — self-hosted OpenAI-compatible endpoints (Ollama,
  // GPUStack, vLLM, LiteLLM…). Multi-host support with model-batch loading.
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { showConfirm } from '../lib/stores.js'
  import { endpointTransport } from '../lib/endpointSecurity.js'

  let hosts = []
  let selectedHostId = ''
  let activeHost = { id: '', name: '', endpoint: '', apiKey: '', hasApiKey: false, removeApiKey: false, contextTokens: 0, outputTokens: 0, isDefault: true }

  let error = ''
  let notice = ''
  let testing = false
  let saving = false
  let models = null
  let selectedModels = new Set()
  let applyBusy = ''
  let appliedModels = []
  let cliStatus = { opencode: false, openclaude: false }

  $: transport = endpointTransport(activeHost.endpoint)

  async function load() {
    try {
      hosts = (await api.listLocalHosts()) || []
      if (!hosts.length) {
        hosts = [{
          id: 'default',
          name: 'Default Host',
          endpoint: 'http://localhost:11434',
          hasApiKey: false,
          contextTokens: 0,
          outputTokens: 0,
          isDefault: true
        }]
      }
      if (!selectedHostId || !hosts.some(h => h.id === selectedHostId)) {
        const def = hosts.find(h => h.isDefault) || hosts[0]
        selectedHostId = def.id
      }
      selectHost(selectedHostId)
      await refreshAppliedModels()
    } catch (e) {
      error = String(e)
    }
  }

  function selectHost(id) {
    selectedHostId = id
    const found = hosts.find(h => h.id === id)
    if (found) {
      activeHost = { ...found, apiKey: '', removeApiKey: false }
    }
    models = null
    selectedModels = new Set()
  }

  function addNewHost() {
    const id = 'host_' + Date.now()
    const newH = {
      id,
      name: 'New Host',
      endpoint: 'http://localhost:11434',
      hasApiKey: false,
      apiKey: '',
      removeApiKey: false,
      contextTokens: 0,
      outputTokens: 0,
      isDefault: hosts.length === 0
    }
    hosts = [...hosts, newH]
    selectHost(id)
  }

  async function test() {
    testing = true
    error = ''
    try {
      const res = (await api.testLocalLLM(activeHost.endpoint, activeHost.apiKey)) || []
      models = res
      selectedModels = new Set(res) // select all by default on probe
    } catch (e) {
      error = String(e)
    } finally {
      testing = false
    }
  }

  function toggleModel(model) {
    if (selectedModels.has(model)) {
      selectedModels.delete(model)
    } else {
      selectedModels.add(model)
    }
    selectedModels = new Set(selectedModels)
  }

  function selectAllModels(val) {
    if (val && models) {
      selectedModels = new Set(models)
    } else {
      selectedModels = new Set()
    }
  }

  async function save() {
    saving = true
    error = ''
    try {
      activeHost.contextTokens = Number(activeHost.contextTokens) || 0
      activeHost.outputTokens = Number(activeHost.outputTokens) || 0
      await api.saveLocalHost(activeHost)
      activeHost.apiKey = ''
      activeHost.removeApiKey = false
      await load()
      notice = `Host "${activeHost.name}" saved.`
    } catch (e) {
      error = String(e)
    } finally {
      saving = false
    }
  }

  async function deleteCurrentHost() {
    const ok = await showConfirm({
      title: 'Delete Host',
      message: `Delete host configuration "${activeHost.name}"?`
    })
    if (!ok) return
    try {
      await api.deleteLocalHost(activeHost.id)
      selectedHostId = ''
      await load()
      notice = 'Host deleted.'
    } catch (e) {
      error = String(e)
    }
  }

  async function makeDefaultHost() {
    try {
      await api.setDefaultLocalHost(activeHost.id)
      await load()
      notice = `"${activeHost.name}" is now the default host.`
    } catch (e) {
      error = String(e)
    }
  }

  async function refreshAppliedModels() {
    try {
      appliedModels = (await api.listAppliedCLIModels()) || []
      cliStatus = (await api.localCLIStatus()) || cliStatus
    } catch {}
  }

  async function applySelectedModels(cli) {
    if (!selectedModels.size) {
      error = 'Please select at least one model to apply.'
      return
    }
    applyBusy = cli
    error = ''
    try {
      const list = Array.from(selectedModels)
      notice = await api.applyModelsToCLI(cli, activeHost.id, list)
      await refreshAppliedModels()
    } catch (e) {
      error = String(e)
    } finally {
      applyBusy = ''
    }
  }

  async function removeAppliedModel(item) {
    applyBusy = item.model
    error = ''
    try {
      const cliTarget = item.CLI.includes('openclaude') ? 'openclaude' : 'opencode'
      notice = await api.removeModelFromCLI(cliTarget, item.HostID, item.Model)
      await refreshAppliedModels()
    } catch (e) {
      error = String(e)
    } finally {
      applyBusy = ''
    }
  }

  onMount(async () => {
    await load()
  })
</script>

<h1>Local LLM Hosts & Models</h1>
<p class="subtitle">Configure OpenAI-compatible endpoints (Ollama, GPUStack, vLLM, LiteLLM) and batch-register models across OpenCode, PrAImate Code, and OpenClaude.</p>

{#if error}<div class="banner error-banner" role="alert">{error}</div>{/if}
{#if notice}<div class="card card-sub" style="border-left: 3px solid var(--ok);">{notice}</div>{/if}

<!-- Host selector bar -->
<div class="host-tabs-bar">
  <div class="host-tabs">
    {#each hosts as h (h.id)}
      <button class="host-tab" class:active={h.id === selectedHostId} on:click={() => selectHost(h.id)}>
        <span>{h.name || h.endpoint}</span>
        {#if h.isDefault}<span class="pill sm ok" style="margin-left:6px; font-size:10px">Default</span>{/if}
      </button>
    {/each}
  </div>
  <button class="btn sm" on:click={addNewHost}>+ Add Host</button>
</div>

<!-- Active Host Card -->
<div class="card">
  <div class="row" style="align-items: center; justify-content: space-between; margin-bottom: 12px">
    <div class="card-title" style="margin:0">Host Settings — {activeHost.name}</div>
    <div class="row" style="gap:6px">
      {#if !activeHost.isDefault}
        <button class="btn sm" on:click={makeDefaultHost} title="Set as primary default host for sessions">Set as Default</button>
        <button class="btn sm danger" on:click={deleteCurrentHost}>Delete Host</button>
      {/if}
    </div>
  </div>

  <label class="lbl" for="local-llm-name">Host Name / Label</label>
  <input id="local-llm-name" class="field" bind:value={activeHost.name} placeholder="e.g. Local Ollama, GPU Server, Mac Studio" />

  <label class="lbl" for="local-llm-endpoint" style="margin-top:10px">Endpoint URL</label>
  <input id="local-llm-endpoint" class="field mono" bind:value={activeHost.endpoint} placeholder="http://127.0.0.1:11434" />
  {#if transport.insecure}
    <div class="transport-warning" role="alert">
      <span class="transport-label">HTTP</span>
      <div>
        <strong>{transport.loopback ? 'Unencrypted local connection' : 'Unencrypted remote connection'}</strong>
        {#if transport.loopback}
          <p>Traffic stays on this local machine as long as the server listens on loopback.</p>
        {:else}
          <p>Prompts and file contents are sent over unencrypted HTTP. Use HTTPS, a VPN, or an SSH tunnel if connecting over the internet.</p>
        {/if}
      </div>
    </div>
  {/if}

  <label class="lbl" for="local-llm-api-key" style="margin-top:10px">API Key (optional)</label>
  <input id="local-llm-api-key" class="field mono" type="password" bind:value={activeHost.apiKey} placeholder={activeHost.hasApiKey ? 'Saved securely — enter new key to replace' : 'Empty for plain local Ollama'} />
  {#if activeHost.hasApiKey}
    <label class="row" style="margin-top:6px; gap:8px; cursor:pointer">
      <input type="checkbox" bind:checked={activeHost.removeApiKey} />
      <span class="card-sub">Remove the saved API key</span>
    </label>
  {/if}

  <div class="row" style="margin-top:10px">
    <div class="grow">
      <label class="lbl" for="local-llm-context-tokens">Context tokens hint</label>
      <input id="local-llm-context-tokens" class="field" type="number" bind:value={activeHost.contextTokens} placeholder="e.g. 32768" />
    </div>
    <div class="grow">
      <label class="lbl" for="local-llm-output-tokens">Output tokens hint</label>
      <input id="local-llm-output-tokens" class="field" type="number" bind:value={activeHost.outputTokens} placeholder="e.g. 8192" />
    </div>
  </div>

  <div class="row" style="margin-top:16px">
    <button class="btn action-btn" on:click={test} disabled={testing || !activeHost.endpoint}>{testing ? 'Probing…' : 'Test connection'}</button>
    <button class="btn primary action-btn" on:click={save} disabled={saving}>{saving ? 'Saving…' : 'Save Host'}</button>
  </div>
</div>

<!-- Probed Models Card with Checkboxes -->
{#if models !== null}
  <div class="card" style="margin-top:16px">
    <div class="row" style="align-items:center; justify-content:space-between; margin-bottom:10px">
      <div>
        <strong style="font-size:14px">{models.length} model(s) detected on {activeHost.name}</strong>
        <div class="card-sub">Select the models you want to make available to your CLIs.</div>
      </div>
      <div class="row" style="gap:6px">
        <button class="btn sm" on:click={() => selectAllModels(true)}>Select All</button>
        <button class="btn sm" on:click={() => selectAllModels(false)}>Deselect All</button>
      </div>
    </div>

    {#if models.length === 0}
      <div class="empty">Endpoint reachable, but no models were found. Download a model via `ollama pull` or host manager.</div>
    {:else}
      <div class="models-checklist">
        {#each models as m}
          <label class="model-check-item" class:checked={selectedModels.has(m)}>
            <input type="checkbox" checked={selectedModels.has(m)} on:change={() => toggleModel(m)} />
            <span class="mono" style="font-size:12.5px">{m}</span>
          </label>
        {/each}
      </div>

      <div class="row" style="margin-top:16px; flex-wrap:wrap; gap:10px; align-items:center">
        <button class="btn primary" on:click={() => applySelectedModels('opencode')} disabled={!!applyBusy || !selectedModels.size}>
          {applyBusy === 'opencode' ? 'Applying…' : `Apply ${selectedModels.size} model(s) to OpenCode / PrAImate Code`}
        </button>
        <button class="btn" on:click={() => applySelectedModels('openclaude')} disabled={!!applyBusy || !selectedModels.size}>
          {applyBusy === 'openclaude' ? 'Applying…' : `Apply to OpenClaude`}
        </button>
      </div>
    {/if}
  </div>
{/if}

<!-- Active Models list in CLI configs -->
<h2 style="font-size:16px; margin-top:28px">Active Models Loaded in CLIs</h2>
<p class="subtitle">These models are currently written into your CLI configuration files. You can use them directly in terminal/chat without re-configuring.</p>

<div class="card">
  {#if appliedModels.length === 0}
    <div class="empty">No models registered in CLIs yet. Probe a host above and click "Apply" to add models.</div>
  {:else}
    <div class="applied-list">
      {#each appliedModels as item}
        <div class="applied-item">
          <div class="applied-info">
            <span class="mono bold" style="font-size:13px; color:var(--text)">{item.Model}</span>
            <div class="row" style="gap:6px; margin-top:3px; align-items:center">
              <span class="pill sm">{item.HostName}</span>
              <span class="card-sub mono" style="font-size:11px">{item.Endpoint || 'default endpoint'}</span>
              <span class="pill sm ok">{item.CLI}</span>
            </div>
          </div>
          <button class="btn sm danger" on:click={() => removeAppliedModel(item)} disabled={applyBusy === item.Model} title="Remove this model from CLI configuration">
            {applyBusy === item.Model ? '…' : '× Remove'}
          </button>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .host-tabs-bar { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 12px; }
  .host-tabs { display: flex; gap: 4px; overflow-x: auto; flex-wrap: wrap; }
  .host-tab { background: var(--bg-panel); border: 1px solid var(--border); color: var(--text-dim); padding: 7px 14px; border-radius: var(--radius-sm); font-size: 12.5px; cursor: pointer; display: flex; align-items: center; }
  .host-tab.active { background: var(--bg-raised); color: var(--text); border-color: var(--accent); font-weight: 600; }
  .action-btn { min-width: 120px; text-align: center; }
  .models-checklist { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 8px; max-height: 280px; overflow-y: auto; padding: 4px 0; }
  .model-check-item { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--bg-raised); cursor: pointer; }
  .model-check-item.checked { border-color: var(--accent); background: var(--accent-soft); }
  .applied-list { display: flex; flex-direction: column; gap: 8px; }
  .applied-item { display: flex; align-items: center; justify-content: space-between; padding: 10px 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--bg-raised); }
  .transport-warning { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 11px; margin: 8px 0 14px; padding: 12px 13px; border: 1px solid color-mix(in srgb, var(--warn) 45%, var(--border)); border-radius: var(--radius-sm); background: color-mix(in srgb, var(--warn) 8%, var(--bg-panel)); }
  .transport-label { align-self: start; padding: 2px 6px; border-radius: 4px; background: color-mix(in srgb, var(--warn) 18%, transparent); color: var(--warn); font: 700 10px/1.5 var(--mono); letter-spacing: .05em; }
  .transport-warning strong { font-size: 13px; }
  .transport-warning p { margin: 3px 0 0; color: var(--text-dim); font-size: 12px; line-height: 1.5; }
</style>
