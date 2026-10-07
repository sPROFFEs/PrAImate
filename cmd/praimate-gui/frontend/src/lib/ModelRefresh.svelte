<script>
  import { createEventDispatcher } from 'svelte'
  import { api } from './api.js'
  export let cli = ''
  export let studio = false
  let busy = false
  let error = ''
  const dispatch = createEventDispatcher()

  async function refresh() {
    const requestedCLI = cli
    busy = true
    error = ''
    try {
      const models = await (studio ? api.studioRefreshCLIModels(requestedCLI) : api.refreshCLIModels(requestedCLI))
      if (requestedCLI === cli) dispatch('models', { cli: requestedCLI, models: models || [] })
    } catch (e) { if (requestedCLI === cli) error = String(e) }
    finally { busy = false }
  }
</script>

<div class="model-refresh">
  <button type="button" class="btn sm" disabled={!cli || busy} on:click={refresh}>{busy ? 'Refreshing models…' : 'Refresh models'}</button>
  {#if error}<span role="alert">{error}</span>{/if}
</div>

<style>
  .model-refresh { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 6px; }
  span { color: var(--err); font-size: 12px; }
</style>
