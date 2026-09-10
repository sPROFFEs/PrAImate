<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import SkillLibrary from '../lib/SkillLibrary.svelte'

  let rollout = { enabled: false }
  let busy = false
  let error = ''

  onMount(async () => {
    try { rollout = (await api.skillsV2RolloutState()) || { enabled: false } }
    catch (e) { error = String(e) }
  })

  async function enable() {
    if (busy) return
    busy = true
    error = ''
    try {
      await api.setSkillsV2RolloutState(true)
      rollout = { enabled: true }
    } catch (e) { error = String(e) }
    finally { busy = false }
  }
</script>

<h1>Skills</h1>
<p class="subtitle">
  One skill library for Chats, Studio, workflows and Terminal. Skills with
  portable instructions work across supported CLIs; CLI-specific tools and
  automatic loading still depend on the selected runtime.
</p>

{#if error}<div class="banner" role="alert">{error}</div>{/if}

{#if !rollout.enabled}
  <div class="card">
    <div class="card-title">Enable skills</div>
    <p class="card-sub">Existing chats remain unchanged. Enabling installs and approves the built-in versions shipped with this PrAImate binary; external packages still require an exact content review.</p>
    <button class="btn primary" disabled={busy} on:click={enable}>{busy ? 'Enabling…' : 'Enable skills'}</button>
  </div>
{:else}
  <div class="card">
    <div class="card-title">Skills enabled</div>
    <p class="card-sub">Agent packages carry their skills. Imported and authored skills can be reused from every supported surface.</p>
  </div>
  <SkillLibrary />
{/if}
