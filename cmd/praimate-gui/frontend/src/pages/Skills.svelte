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
  portable instructions can be reused across supported CLIs; how they are
  loaded depends on the selected runtime.
</p>

<section class="card skill-help" aria-labelledby="skill-loading-title">
  <div class="card-title" id="skill-loading-title">How skills reach the model</div>
  <p class="card-sub">PrAImate manages your approved skill library. Both native CLIs and managed agent sessions discover and load skills automatically via the internal MCP bridge or portable system prompts.</p>
  <div class="loading-grid">
    <div class="loading-item">
      <span class="loading-badge">1</span>
      <div><strong>Automatic (MCP)</strong><p>The model discovers active skills via the internal MCP tools <code>load_skill</code> and <code>list_available_skills</code>, loading instructions on-demand without exhausting context.</p></div>
    </div>
    <div class="loading-item">
      <span class="loading-badge">2</span>
      <div><strong>Always include</strong><p>PrAImate embeds the approved skill body directly in the prompt context from turn 1. Best for fundamental guidelines and rules.</p></div>
    </div>
    <div class="loading-item">
      <span class="loading-badge">3</span>
      <div><strong>Workflow-scoped</strong><p>Skills attached to specific workflows activate only when running that workflow, keeping each specialized task focused and isolated.</p></div>
    </div>
  </div>
</section>

{#if error}<div class="banner" role="alert">{error}</div>{/if}

{#if !rollout.enabled}
  <div class="card">
    <div class="card-title">Enable skills</div>
    <p class="card-sub">Existing chats remain unchanged. Enabling installs and approves the built-in versions shipped with this PrAImate binary; external packages still require an exact content review.</p>
    <button class="btn primary" disabled={busy} on:click={enable}>{busy ? 'Enabling…' : 'Enable skills'}</button>
  </div>
{:else}
  <SkillLibrary />
{/if}

<style>
  .skill-help { margin: 18px 0; }
  .loading-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-top: 14px; }
  .loading-item { display: flex; gap: 10px; padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--bg-raised); color: var(--text); }
  .loading-item strong { font-size: 13px; color: var(--text); }
  .loading-item p { margin: 5px 0 0; color: var(--text-dim); font-size: 12px; line-height: 1.45; }
  .loading-badge { display: grid; place-items: center; flex: 0 0 22px; height: 22px; border-radius: 50%; background: var(--accent); color: var(--accent-fg); font-weight: 700; font-size: 12px; }
  .skill-boundary { margin-top: 14px; padding: 11px 12px; border-left: 3px solid var(--warn); background: color-mix(in oklch, var(--warn) 10%, transparent); color: var(--text-dim); font-size: 12px; line-height: 1.5; border-radius: 0 var(--radius-sm) var(--radius-sm) 0; }
  code { font-family: var(--mono, monospace); font-size: .92em; }
  @media (max-width: 860px) { .loading-grid { grid-template-columns: 1fr; } }
</style>
