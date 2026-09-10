<script>
  import { onMount } from 'svelte'
  import { api } from './api.js'
  export let choices = null
  let open = false, busy = true, error = '', query = '', versions = []
  $: count = (choices || []).length
  $: shown = versions.filter(v => `${v.name || ''} ${v.description || ''} ${v.ref || ''}`.toLowerCase().includes(query.trim().toLowerCase()))
  onMount(async () => {
    try {
      versions = ((await api.installedSkillVersionsV2()) || []).map(v => ({ ...v, selected: (choices || []).some(c => c.ref === v.ref && c.digest === v.digest), activation: (choices || []).find(c => c.ref === v.ref && c.digest === v.digest)?.activation || 'pinned' }))
    } catch (e) { error = String(e) }
    finally { busy = false }
  })
  function select(v, selected) {
    versions = versions.map(item => ({ ...item, selected: selected && item.ref === v.ref ? item.digest === v.digest : item.ref === v.ref ? false : item.selected }))
  }
  function done() {
    choices = versions.filter(v => v.selected).map(v => ({ ref: v.ref, digest: v.digest, activation: v.activation }))
    open = false
  }
</script>

<div class="draft-control">
  <div class="lbl">Skills <span class="card-sub">optional</span></div>
  <button class="btn draft-open" type="button" disabled={busy} on:click={() => (open = true)}>{busy ? 'Loading skills…' : choices === null ? 'Use agent and app defaults' : count ? `${count} selected` : 'No skills'} <span>›</span></button>
  {#if error && !open}<div class="error" role="alert">{error}</div>{/if}
</div>
{#if open}
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <div class="modal-backdrop draft-backdrop" on:click|self={() => (open = false)}>
    <div class="modal-content draft-modal" role="dialog" aria-modal="true" aria-label="Choose skills">
      <h2>Skills</h2>
      <p class="card-sub">Leave unchanged to use the agent and application defaults, or choose an explicit set for this session.</p>
      {#if error}<div class="banner">{error}</div>{/if}
      <input class="field search" type="search" bind:value={query} placeholder="Search skills" aria-label="Search skills" />
      <div class="list">
        {#each shown as v (`${v.ref}@${v.digest}`)}
          <div class="choice" class:on={v.selected}>
            <label><input type="checkbox" checked={v.selected} disabled={!v.approved} on:change={(e) => select(v, e.currentTarget.checked)} /><span><strong>{v.name || v.ref.split('/').pop()}</strong><small>{v.description}</small>{#if !v.approved}<small class="warn">Review required in Skills → Installed</small>{/if}</span></label>
            {#if v.selected}<select bind:value={v.activation} aria-label={`Execution mode for ${v.name || v.ref}`}><option value="pinned">Always include (portable)</option><option value="auto">Load automatically (managed runs only)</option><option value="manual">Load when requested (managed runs only)</option></select>{/if}
          </div>
        {/each}
      </div>
      <p class="card-sub">Always include sends the instructions in the next request. Automatic and requested modes use PrAImate's managed skill tools; native CLIs cannot discover them on their own.</p>
      <div class="actions"><button class="btn" on:click={() => { choices = null; open = false }}>Use defaults</button><button class="btn primary" on:click={done}>Use selection</button></div>
    </div>
  </div>
{/if}
<style>
  .draft-control { margin-top: 12px; }.draft-open { width:min(100%,420px);display:flex;justify-content:space-between}.error{color:var(--err);font-size:12px}.draft-backdrop{z-index:13000;padding:20px}.draft-modal{max-width:650px;max-height:86vh;display:flex;flex-direction:column}.search{width:100%;margin:12px 0 4px}.list{overflow:auto}.choice{padding:10px;border-bottom:1px solid var(--border)}.choice.on{background:color-mix(in oklch,var(--accent) 7%,transparent)}.choice label{display:flex;gap:9px}.choice label span{display:grid;gap:3px}.choice small{color:var(--text-dim);font-size:12px}.choice .warn{color:var(--warn,#d39e00)}.choice select{margin:8px 0 0 26px;min-width:240px}.actions{display:flex;justify-content:flex-end;gap:8px;padding-top:12px;border-top:1px solid var(--border)}
</style>
