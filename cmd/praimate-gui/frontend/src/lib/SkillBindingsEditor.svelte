<script>
  import { onMount, createEventDispatcher } from 'svelte'
  import { api } from './api.js'

  export let chatID
  const dispatch = createEventDispatcher()
  let open = false
  let busy = true
  let error = ''
  let notice = ''
  let versions = []
  let query = ''

  $: selectedCount = versions.filter(version => version.selected).length
  $: pinnedCount = versions.filter(v => v.selected && v.activation === 'pinned').length
  $: exceedsBudget = pinnedCount > 3
  $: filteredVersions = versions.filter(version => `${version.name || ''} ${version.description || ''} ${version.ref || ''}`.toLowerCase().includes(query.trim().toLowerCase()))

  onMount(load)

  async function load() {
    busy = true
    error = ''
    try {
      const [selection, installed] = await Promise.all([
        api.chatSkillsV2(chatID),
        api.installedSkillVersionsV2()
      ])
      versions = (installed || []).map(version => {
        const binding = (selection?.config?.bindings || []).find(item => item.ref === version.ref)
        const locked = (selection?.lock?.entries || []).find(item => item.ref === version.ref && item.digest === version.digest)
        return {
          ...version,
          selected: !!binding && !!locked && binding.activation !== 'off',
          activation: binding?.activation || 'pinned'
        }
      })
    } catch (e) {
      error = String(e)
    } finally {
      busy = false
    }
  }

  function selectVersion(version, selected) {
    error = ''
    if (selected) {
      versions = versions.map(item => ({
        ...item,
        selected: item.ref === version.ref ? item.digest === version.digest : item.selected
      }))
    } else {
      version.selected = false
      versions = [...versions]
    }
  }

  async function save() {
    if (busy) return
    if (exceedsBudget) {
      error = `Context budget limit: A maximum of 3 skills can be set to "Always include" (pinned) simultaneously (currently ${pinnedCount}). Please change additional skills to "Load automatically" or deselect them.`
      return
    }
    busy = true
    error = ''
    notice = ''
    try {
      const choices = versions
        .filter(version => version.selected)
        .map(version => ({ ref: version.ref, digest: version.digest, activation: version.activation }))
      const selection = await api.saveChatSkillChoicesV2(chatID, choices)
      dispatch('saved', selection)
      notice = 'Skills saved. Runtime delivery is reported in the session activity.'
      open = false
    } catch (e) {
      error = String(e)
    } finally {
      busy = false
    }
  }
</script>

<div class="skill-control">
  <div class="lbl">Skills</div>
  <button class="btn skill-open" type="button" on:click={() => (open = true)} disabled={busy}>
    <span>{busy ? 'Loading skills…' : selectedCount ? `${selectedCount} selected` : 'Choose skills…'}</span>
    <span aria-hidden="true">›</span>
  </button>
  {#if notice}<div class="card-sub" role="status">{notice}</div>{/if}
  {#if error && !open}<div class="skill-error" role="alert">{error}</div>{/if}
</div>

{#if open}
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <div class="modal-backdrop skill-backdrop" on:click|self={() => !busy && (open = false)}>
    <div class="modal-content skill-modal" role="dialog" aria-modal="true" aria-labelledby={`skills-title-${chatID}`}>
      <div class="skill-head">
        <div>
          <h2 id={`skills-title-${chatID}`}>Skills</h2>
          <p>Select the procedures this session may use and when they are loaded.</p>
        </div>
        <button class="icon-button" type="button" aria-label="Close skills" on:click={() => (open = false)} disabled={busy}>×</button>
      </div>

      {#if error}<div class="banner" role="alert">{error}</div>{/if}
      {#if exceedsBudget}<div class="banner" role="alert" style="background: rgba(211, 158, 0, 0.15); border-color: #d39e00; color: var(--text); margin-bottom: 8px">⚠️ {pinnedCount}/3 pinned skills selected. A maximum of 3 skills can be set to "Always include" simultaneously to preserve the session context budget.</div>{/if}
      <div class="skill-search"><input class="field" type="search" bind:value={query} placeholder="Search skills" aria-label="Search skills" /></div>
      <div class="skill-list">
        {#each filteredVersions as version (`${version.ref}@${version.digest}`)}
          <div class="skill-row" class:selected={version.selected} class:unapproved={!version.approved}>
            <label class="skill-choice">
              <input type="checkbox" checked={version.selected} disabled={busy || !version.approved} on:change={(event) => selectVersion(version, event.currentTarget.checked)} />
              <span class="skill-copy">
                <strong>{version.name || version.ref.split('/').pop()}</strong>
                <span>{version.description || 'No description provided.'}</span>
                {#if !version.approved}<small>Review and approve this skill in Installed before using it.</small>{/if}
              </span>
            </label>
            {#if version.selected}
              <label class="mode-choice">
                <span>Execution mode</span>
                <select bind:value={version.activation} disabled={busy} aria-label={`Execution mode for ${version.name || version.ref}`}>
                  <option value="pinned">Always include (portable)</option>
                  <option value="auto">Load automatically (managed runs only)</option>
                  <option value="manual">Load when requested (managed runs only)</option>
                </select>
              </label>
            {/if}
          </div>
        {/each}
        {#if !busy && !versions.length}<div class="empty-state">No skills installed. Import or create one from the Skills page first.</div>{/if}
        {#if !busy && versions.length && !filteredVersions.length}<div class="empty-state">No skills match this search.</div>{/if}
      </div>

      <p class="mode-note"><strong>Portable:</strong> Always include sends the approved instructions in the next controlled request. <strong>Managed only:</strong> automatic/requested modes use <code>skill.load</code> and <code>skill.read</code>; native CLIs do not map these tools, so they cannot discover a skill by themselves.</p>
      <div class="skill-actions">
        <button class="btn" type="button" on:click={() => (open = false)} disabled={busy}>Cancel</button>
        <button class="btn primary" type="button" on:click={save} disabled={busy}>{busy ? 'Saving…' : 'Save skills'}</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .skill-control { margin-top: 12px; }
  .skill-open { width: min(100%, 420px); display: flex; justify-content: space-between; align-items: center; margin: 4px 0; }
  .skill-error { color: var(--err); font-size: 12px; margin-top: 5px; }
  .skill-backdrop { z-index: 12000; padding: 20px; }
  .skill-modal { max-width: 680px; padding: 0; max-height: min(760px, 90vh); display: flex; flex-direction: column; overflow: hidden; background: var(--bg-panel); color: var(--text); border: 1px solid var(--border); }
  .skill-head { display: flex; align-items: flex-start; gap: 16px; padding: 18px 20px 14px; border-bottom: 1px solid var(--border); }
  .skill-head > div { flex: 1; }
  .skill-head h2 { margin: 0 0 4px; color: var(--text); }
  .skill-head p, .mode-note { margin: 0; color: var(--text-dim); font-size: 12px; }
  .icon-button { border: 0; background: none; color: var(--text-dim); font-size: 22px; cursor: pointer; }
  .skill-list { overflow-y: auto; padding: 8px 20px; }
  .skill-search { padding: 10px 20px 2px; }
  .skill-search .field { width: 100%; }
  .skill-row { padding: 12px 0; border-bottom: 1px solid var(--border); }
  .skill-row:last-child { border-bottom: 0; }
  .skill-row.selected { background: var(--accent-soft); margin-inline: -10px; padding-inline: 10px; border-radius: var(--radius-sm); }
  .skill-row.unapproved { opacity: .68; }
  .skill-choice { display: flex; align-items: flex-start; gap: 10px; cursor: pointer; }
  .skill-copy { display: grid; gap: 3px; }
  .skill-copy strong { color: var(--text); }
  .skill-copy span, .skill-copy small { color: var(--text-dim); font-size: 12px; line-height: 1.4; }
  .skill-copy small { color: var(--warn); font-weight: 500; }
  .mode-choice { display: flex; align-items: center; gap: 10px; margin: 10px 0 0 26px; color: var(--text-dim); font-size: 12px; }
  .mode-choice select { flex: 1; min-width: 220px; }
  .empty-state { color: var(--text-dim); text-align: center; padding: 32px 12px; }
  .mode-note { padding: 10px 20px; border-top: 1px solid var(--border); }
  .skill-actions { display: flex; justify-content: flex-end; gap: 8px; padding: 12px 20px; border-top: 1px solid var(--border); }
</style>
