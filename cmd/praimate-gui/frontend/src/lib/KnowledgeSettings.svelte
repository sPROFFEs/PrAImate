<script>
  import { onMount, createEventDispatcher } from 'svelte'
  import { api } from './api.js'
  export let id
  export let know
  export let disabled = false
  const dispatch = createEventDispatcher()
  let loaded
  let config = {}
  let key = ''
  let removeKey = false
  let busy = false
  let error = ''
  let notice = ''
  let clis = []
  let hosts = []
  let models = []
  let generation = 0
  $: if (know && loaded !== know) {
    loaded = know
    config = { source: 'local', endpoint: '', enrichment_cli: '', enrichment_model: '', enrichment_endpoint: know.localEndpoint || '', max_enrichment_chunks: 64, ...know.config }
    key = ''; removeKey = false
    loadModels()
  }
  onMount(async () => {
    const results = await Promise.allSettled([api.listCLIs(), api.listLocalHosts()])
    if (results[0].status === 'fulfilled') clis = results[0].value || []
    if (results[1].status === 'fulfilled') hosts = results[1].value || []
  })
  async function loadModels() {
    const current = ++generation
    models = []
    try {
      const found = config.enrichment_cli === 'local' ? await api.localLLMHostsModels() : config.enrichment_cli ? await api.listCLIModels(config.enrichment_cli) : []
      if (generation === current) models = config.enrichment_cli === 'local' ? (found || []).filter(host => host.endpoint === config.enrichment_endpoint).flatMap(host => host.models || []) : found || []
    } catch { /* An exact model ID can also be entered. */ }
  }
  async function save(test = false) {
    busy = true; error = ''; notice = ''
    try {
      await api.saveAgentKnowledgeConfig(id, config, key, removeKey)
      key = ''; removeKey = false
      dispatch('saved')
      if (test) await api.testAgentKnowledgeRemote(id)
      notice = test ? 'Remote knowledge service is available.' : 'Knowledge configuration saved.'
    } catch (e) { error = String(e) }
    finally { busy = false }
  }
</script>

<details class="knowledge-settings">
  <summary>Knowledge source and model enrichment</summary>
  {#if disabled}<p class="hint">Save the agent definition before changing knowledge settings.</p>{/if}
  <fieldset disabled={disabled || busy}>
    <label>Knowledge source
      <select class="field" bind:value={config.source}><option value="local">Local documents</option><option value="remote">Remote knowledge service</option></select>
    </label>
    {#if config.source === 'remote'}
      <label>Service URL <input class="field" bind:value={config.endpoint} placeholder="https://server.example/knowledge" /></label>
      <label>API key (optional) <input class="field" type="password" autocomplete="new-password" bind:value={key} placeholder={know?.hasAPIKey ? 'Saved key · leave blank to keep' : 'Bearer token'} /></label>
      {#if know?.hasAPIKey}<label class="check"><input type="checkbox" bind:checked={removeKey} /> Remove saved key</label>{/if}
      <p class="hint">The service must implement PrAImate's health, query, search and read API. Documents stay on the server. Exported agents contain the URL, never the key.</p>
    {:else}
      <label>Built-in index enrichment
        <select class="field" bind:value={config.enrichment_cli} on:change={loadModels}>
          <option value="">Offline · no model</option><option value="local">Local model</option>
          {#each clis as cli}<option value={cli.id} disabled={!cli.available || cli.capabilities?.managedWorker === false}>{cli.label}{!cli.available ? ' (unavailable)' : ''}</option>{/each}
        </select>
      </label>
      {#if config.enrichment_cli}
        {#if config.enrichment_cli === 'local'}
          <label>Configured endpoint
            <input class="field" list={'knowledge-hosts-' + id} bind:value={config.enrichment_endpoint} on:change={loadModels} placeholder="http://localhost:11434/v1" />
          </label>
          <datalist id={'knowledge-hosts-' + id}>{#each hosts as host}<option value={host.endpoint}>{host.name}</option>{/each}</datalist>
        {/if}
        <label>Model <input class="field" list={'knowledge-models-' + id} bind:value={config.enrichment_model} placeholder="Model ID or alias" /></label>
        <datalist id={'knowledge-models-' + id}>{#each models as model}<option value={model}></option>{/each}</datalist>
        <label>Maximum passages per index build <input class="field" type="number" min="1" max="512" bind:value={config.max_enrichment_chunks} /></label>
        <p class="hint">Indexing sends selected source passages to this model using your existing CLI login or local host. Unchanged passages are cached. Generated relations are marked as model inferences; retrieval returns the original source text. Subscription quotas may apply.</p>
      {/if}
    {/if}
    <div class="actions"><button class="btn sm" on:click={() => save()}>Save settings</button>{#if config.source === 'remote'}<button class="btn sm" on:click={() => save(true)}>Save and test connection</button>{/if}</div>
  </fieldset>
  {#if busy}<p class="hint">Saving…</p>{/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p class="hint" role="status">{notice}</p>{/if}
</details>

<style>
  .knowledge-settings { margin: 12px 0; border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 10px; background: var(--bg-elevated, var(--bg)); }
  summary { cursor: pointer; font-weight: 600; }
  fieldset { border: 0; padding: 8px 0 0; margin: 0; min-width: 0; display: grid; gap: 10px; }
  label { display: grid; gap: 4px; font-size: 12px; }
  .check { display: flex; align-items: center; }
  .field { min-width: 0; width: 100%; box-sizing: border-box; }
  .hint { font-size: 12px; color: var(--text-dim); line-height: 1.5; margin: 6px 0; }
  .actions { display: flex; flex-wrap: wrap; gap: 8px; }
  .error { color: var(--danger, #d74453); overflow-wrap: anywhere; }
</style>
