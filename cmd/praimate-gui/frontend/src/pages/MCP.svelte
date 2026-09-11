<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { showConfirm } from '../lib/stores.js'
  import { commandForMCPForm, envForMCPForm } from '../lib/mcpForm.js'

  let catalogue = []
  let servers = []
  let error = ''
  let notice = ''

  // custom-server form
  let showCustom = false
  let cName = ''
  let cTransport = 'stdio'
  let cCommand = ''
  let cURL = ''
  let cEnv = ''
  let editingID = ''

  // import JSON dialog
  let showImport = false
  let importJSON = ''
  let importing = false

  // probe / test state
  let probing = {} // id -> boolean
  let probeResults = {} // id -> { ok, tools, error, latency }
  let expandedTools = {} // id -> boolean

  function resetForm() {
    showCustom = false
    editingID = ''
    cName = ''
    cTransport = 'stdio'
    cCommand = ''
    cURL = ''
    cEnv = ''
  }

  function openAdd() {
    if (showCustom && !editingID) {
      resetForm()
      return
    }
    resetForm()
    showCustom = true
  }

  function edit(s) {
    editingID = s.id
    cName = s.name || ''
    cTransport = s.transport || 'stdio'
    cCommand = commandForMCPForm(s)
    cURL = s.url || ''
    cEnv = envForMCPForm(s)
    showCustom = true
    error = ''
  }

  function configureTemplate(entry) {
    editingID = ''
    cName = entry.name
    cTransport = entry.transport || 'stdio'
    cCommand = commandForMCPForm(entry)
    cURL = entry.url || ''
    cEnv = entry.auth?.env_var ? `${entry.auth.env_var}=` : ''
    showCustom = true
    error = ''
  }

  async function saveCustom() {
    if (!cName.trim()) { error = 'Local MCP needs a name'; return }
    const command = cTransport === 'stdio' ? cCommand.trim() : ''
    const url = cTransport === 'stdio' ? '' : cURL.trim()
    try {
      if (editingID) {
        await api.updateMCPServer(editingID, cName.trim(), cTransport, command, url, cEnv)
      } else {
        await api.addCustomMCP(cName.trim(), cTransport, command, url, cEnv)
      }
      resetForm()
      await load()
    } catch (e) {
      error = String(e)
    }
  }

  async function load() {
    try {
      catalogue = (await api.mcpCatalogue()) || []
      servers = (await api.mcpServers()) || []
      error = ''
    } catch (e) {
      error = String(e)
    }
  }

  async function toggle(s) {
    try { await api.setMCPEnabled(s.id, !s.enabled); await load() } catch (e) { error = String(e) }
  }

  async function remove(s) {
    const ok = await showConfirm({
      title: 'Disconnect MCP Server',
      message: `Disconnect and remove "${s.name}"?`
    })
    if (!ok) return
    try { await api.deleteMCPServer(s.id); await load() } catch (e) { error = String(e) }
  }

  async function testServer(s) {
    probing[s.id] = true
    probing = probing
    error = ''
    try {
      const res = await api.testMCPServer(s.id)
      probeResults[s.id] = res
      probeResults = probeResults
      if (res.ok && res.tools?.length) {
        expandedTools[s.id] = true
      }
    } catch (e) {
      probeResults[s.id] = { ok: false, error: String(e) }
      probeResults = probeResults
    } finally {
      probing[s.id] = false
      probing = probing
    }
  }

  function toggleTools(id) {
    expandedTools[id] = !expandedTools[id]
    expandedTools = expandedTools
  }

  async function runImport() {
    if (!importJSON.trim()) {
      error = 'Please paste a JSON configuration block.'
      return
    }
    importing = true
    error = ''
    try {
      const count = await api.importMCPServersJSON(importJSON.trim())
      notice = `Successfully imported ${count} MCP server(s).`
      showImport = false
      importJSON = ''
      await load()
    } catch (e) {
      error = String(e)
    } finally {
      importing = false
    }
  }

  onMount(load)
</script>

<h1>MCP Servers</h1>
<p class="subtitle">
  Model Context Protocol servers extend your agents with external tools and data sources. PrAImate bridges them automatically when sessions start.
</p>

{#if error}<div class="banner error-banner" role="alert">{error}</div>{/if}
{#if notice}<div class="card card-sub" style="border-left: 3px solid var(--ok);">{notice}</div>{/if}

<div class="row" style="margin-bottom:14px; gap:8px">
  <button class="btn primary" on:click={openAdd}>
    {showCustom && !editingID ? 'Cancel' : '+ Add MCP server'}
  </button>
  <button class="btn" on:click={() => { showImport = !showImport; error = '' }}>
    {showImport ? 'Cancel Import' : 'Import JSON…'}
  </button>
</div>

{#if showImport}
  <div class="card" style="border-color: var(--accent); margin-bottom: 16px;">
    <div class="card-title">Import MCP configuration from JSON</div>
    <div class="card-sub">Paste your <code>mcpServers</code> configuration from Claude Desktop, Cursor, or Cline.</div>
    <textarea class="field mono" rows="8" bind:value={importJSON} placeholder={`{\n  "mcpServers": {\n    "github": {\n      "command": "npx",\n      "args": ["-y", "@modelcontextprotocol/server-github"],\n      "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "..." }\n    }\n  }\n}`}></textarea>
    <div class="row" style="margin-top:10px">
      <button class="btn primary" on:click={runImport} disabled={importing}>{importing ? 'Importing…' : 'Import Servers'}</button>
      <button class="btn" on:click={() => (showImport = false)}>Cancel</button>
    </div>
  </div>
{/if}

{#if showCustom}
  <div class="card" style="margin-bottom: 16px;">
    <div class="card-title">{editingID ? 'Edit local MCP server' : 'Configure a local MCP server'}</div>
    <div class="card-sub">Configure a local process, container, or endpoint hosted on infrastructure you control.</div>
    <label class="lbl" for="mcp-name">Name</label>
    <input id="mcp-name" class="field" bind:value={cName} placeholder="e.g. GitHub Tools, SQL Database, Filesystem" />
    <label class="lbl" for="mcp-transport" style="margin-top:8px">Transport</label>
    <select id="mcp-transport" class="field" style="max-width:220px" bind:value={cTransport}>
      <option value="stdio">stdio (local executable command)</option>
      <option value="http">http (remote endpoint)</option>
      <option value="sse">sse (Server-Sent Events)</option>
    </select>
    {#if cTransport === 'stdio'}
      <label class="lbl" for="mcp-command" style="margin-top:8px">Command (executable + args)</label>
      <input id="mcp-command" class="field mono" bind:value={cCommand} placeholder="npx -y @modelcontextprotocol/server-filesystem /path/to/dir" />
    {:else}
      <label class="lbl" for="mcp-url" style="margin-top:8px">URL</label>
      <input id="mcp-url" class="field mono" bind:value={cURL} placeholder="http://127.0.0.1:9000/mcp" />
    {/if}
    <label class="lbl" for="mcp-environment" style="margin-top:8px">Environment variables (KEY=VALUE per line)</label>
    <textarea id="mcp-environment" class="field mono" rows="3" bind:value={cEnv} placeholder="GITHUB_TOKEN=...&#10;API_KEY=..."></textarea>
    <div class="row" style="margin-top:12px">
      <button class="btn primary" on:click={saveCustom}>{editingID ? 'Save changes' : 'Add server'}</button>
      {#if editingID}<button class="btn" on:click={resetForm}>Cancel</button>{/if}
    </div>
  </div>
{/if}

{#if servers.length > 0}
  <h2 style="font-size:16px; margin-top:20px">Your MCP servers ({servers.length})</h2>
  <div class="servers-grid">
    {#each servers as s (s.id)}
      <div class="card mcp-server-card">
        <div class="row" style="align-items: flex-start; justify-content: space-between">
          <div class="grow">
            <div class="card-title row" style="align-items:center; gap:8px">
              <span>{s.name}</span>
              <span class="pill sm mono">{s.transport}</span>
              <span class="pill sm" class:ok={s.enabled}>{s.enabled ? 'Enabled' : 'Disabled'}</span>
            </div>
            <div class="card-sub mono" style="margin-top:4px; overflow-wrap:anywhere">{s.url || s.command}</div>
          </div>
          <div class="row" style="gap:6px">
            <button class="btn sm" on:click={() => testServer(s)} disabled={probing[s.id]}>
              {probing[s.id] ? 'Probing…' : 'Test connection'}
            </button>
            <button
              class="btn sm"
              class:primary={!s.enabled}
              aria-label={`${s.enabled ? 'Disable' : 'Enable'} ${s.name}`}
              on:click={() => toggle(s)}>
              {s.enabled ? 'Disable' : 'Enable'}
            </button>
            <button class="btn sm" on:click={() => edit(s)}>Edit</button>
            <button class="btn sm danger" on:click={() => remove(s)}>Remove</button>
          </div>
        </div>

        <!-- Probe results banner -->
        {#if probeResults[s.id]}
          <div class="probe-result" class:err={!probeResults[s.id].ok}>
            {#if probeResults[s.id].ok}
              <div class="row" style="align-items:center; justify-content:space-between">
                <span>✓ Connection online ({probeResults[s.id].latency}) · {probeResults[s.id].tools?.length || 0} tools available</span>
                {#if probeResults[s.id].tools?.length}
                  <button class="btn sm" on:click={() => toggleTools(s.id)}>
                    {expandedTools[s.id] ? 'Hide tools' : 'View tools'}
                  </button>
                {/if}
              </div>
            {:else}
              <div>✗ Error connecting: {probeResults[s.id].error}</div>
            {/if}
          </div>
        {/if}

        <!-- Exposed Tools list -->
        {#if expandedTools[s.id] && probeResults[s.id]?.tools?.length}
          <div class="tools-inspector">
            <div class="card-sub bold" style="margin-bottom:6px">Tools exposed by this server:</div>
            <div class="tools-list">
              {#each probeResults[s.id].tools as tool}
                <div class="tool-badge-item">
                  <span class="tool-badge mono">{tool.name}</span>
                  {#if tool.description}<span class="tool-desc">{tool.description}</span>{/if}
                </div>
              {/each}
            </div>
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}

<h2 style="font-size:16px; margin-top:28px">Local catalogue</h2>
<p class="subtitle">
  Pre-configured popular MCP utilities that you can set up with one click.
</p>
<div class="catalogue-grid">
  {#each catalogue as entry}
    <div class="card row" style="align-items: center">
      <div class="grow">
        <div class="card-title">{entry.name}</div>
        <div class="card-sub">{entry.description}</div>
      </div>
      {#if servers.some((server) => server.id === entry.key)}
        <button class="btn sm" on:click={() => edit(servers.find((server) => server.id === entry.key))}>Edit</button>
      {:else}
        <button class="btn sm" on:click={() => configureTemplate(entry)}>Configure</button>
      {/if}
    </div>
  {/each}
</div>

<style>
  .servers-grid { display: flex; flex-direction: column; gap: 10px; margin-top: 8px; }
  .mcp-server-card { display: flex; flex-direction: column; gap: 10px; }
  .probe-result { padding: 8px 12px; border-radius: var(--radius-sm); font-size: 12.5px; background: color-mix(in oklch, var(--ok) 12%, transparent); border: 1px solid color-mix(in oklch, var(--ok) 40%, transparent); color: var(--text); }
  .probe-result.err { background: color-mix(in oklch, var(--err) 12%, transparent); border-color: color-mix(in oklch, var(--err) 40%, transparent); color: var(--err); }
  .tools-inspector { background: var(--bg-raised); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 10px 12px; margin-top: 4px; }
  .tools-list { display: grid; gap: 6px; }
  .tool-badge-item { display: flex; flex-direction: column; gap: 2px; padding: 4px 0; border-bottom: 1px solid var(--border); }
  .tool-badge-item:last-child { border-bottom: 0; }
  .tool-badge { font-weight: 600; font-size: 12px; color: var(--accent); }
  .tool-desc { font-size: 11.5px; color: var(--text-dim); }
  .catalogue-grid { display: grid; gap: 8px; }
</style>
