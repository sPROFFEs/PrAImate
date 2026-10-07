<script>
  import ModelRefresh from './ModelRefresh.svelte'
  import { createEventDispatcher } from 'svelte'
  import { workerTiers } from './workerActivity.js'
  export let config
  export let clis = []
  export let models = {}
  const dispatch = createEventDispatcher()
  function backendChanged(p) {
    p.cli = p.runtime === 'cli' ? 'praimate-cli' : ''
    p.endpoint = p.runtime === 'native' ? 'http://localhost:11434/v1' : ''
    p.maxOutputTokens = p.runtime === 'native' ? 2048 : 0
    p.timeoutSeconds = p.runtime === 'cli' ? 0 : p.tier === 'primary' ? 600 : 300
    cliChanged(p)
  }
  function cliChanged(p) {
    p.model = ''; p.reasoningEffort = ''; p.allowEdits = false; p.allowCommands = false
    config = { ...config }
    dispatch('models', p.cli)
  }
</script>
<div class="profiles">
  {#each config.profiles as p, index (p.tier)}
    <fieldset>
      <legend>{workerTiers.find(t => t.id === p.tier)?.label || p.tier}</legend>
      <label>Backend<select bind:value={config.profiles[index].runtime} on:change={() => backendChanged(p)}><option value="cli">CLI</option><option value="native">Local model / compatible API</option></select></label>
      {#if p.runtime === 'cli'}
        <label>CLI<select bind:value={config.profiles[index].cli} on:change={() => cliChanged(p)}>
          {#if !clis.some(c => c.id === p.cli)}<option value={p.cli}>{p.cli || 'Select CLI'}</option>{/if}
          {#each clis as cli}<option value={cli.id} disabled={!cli.available || cli.capabilities?.managedWorker === false}>{cli.label}{!cli.available ? ' (unavailable)' : cli.capabilities?.managedWorker === false ? ' (workers unsupported)' : ''}</option>{/each}
        </select></label>
      {:else}
        <label>Endpoint<input bind:value={config.profiles[index].endpoint} placeholder="http://localhost:11434/v1" /></label>
        <label>Output token limit<input type="number" min="1" max="32768" bind:value={config.profiles[index].maxOutputTokens} /></label>
      {/if}
      <label>Model<input list={'profile-models-' + p.tier} bind:value={config.profiles[index].model} placeholder="Model ID or alias" /></label>
      <datalist id={'profile-models-' + p.tier}>{#each models[p.cli] || [] as model}<option value={model}></option>{/each}</datalist>
      {#if p.runtime === 'cli'}<ModelRefresh cli={p.cli} studio on:models={event => { models = { ...models, [event.detail.cli]: event.detail.models } }} />{/if}
      {#if p.runtime === 'cli' && p.cli === 'codex'}
        <label>Reasoning effort<select value={p.reasoningEffort || ''} on:change={event => config.profiles[index].reasoningEffort = event.target.value}><option value="">CLI default</option>{#each ['low','medium','high','xhigh','max','ultra'] as effort}<option value={effort}>{effort}</option>{/each}</select><small>Use a level supported by the selected model. Higher effort can take longer before reporting output.</small></label>
      {/if}
      <label class="check"><input type="checkbox" bind:checked={config.profiles[index].allowEdits} /> Allow workspace edits</label>
      <label class="check"><input type="checkbox" bind:checked={config.profiles[index].allowCommands} /> Allow commands with user approval</label>
      <label>Call timeout (seconds)<input type="number" min="0" max="3600" bind:value={config.profiles[index].timeoutSeconds} /><small>0 disables the call timeout. A positive value stops the entire CLI call, including startup and reasoning, even while it is making progress. Stop remains available.</small></label>
      <label>Maximum input bytes<input type="number" min="1024" max="1048576" bind:value={config.profiles[index].maxInputBytes} /></label>
      <label>Additional instructions<textarea rows="3" bind:value={config.profiles[index].instructions}></textarea></label>
    </fieldset>
  {/each}
</div>
<style>
  .profiles{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px}fieldset{min-width:0;display:grid;gap:12px;border:1px solid var(--border);border-radius:var(--radius);padding:16px;background:var(--bg-panel)}legend{font-weight:600;padding:0 6px}label{display:grid;gap:5px;font-size:12px}.check{display:flex;align-items:center;gap:8px}input,select,textarea{font:inherit;min-width:0;max-width:100%;border:1px solid var(--border-bright);background:var(--bg);color:var(--text);border-radius:var(--radius-sm);padding:8px}.check input{accent-color:var(--accent)}small{color:var(--text-dim);line-height:1.5}@media(max-width:800px){.profiles{grid-template-columns:1fr}}
</style>
