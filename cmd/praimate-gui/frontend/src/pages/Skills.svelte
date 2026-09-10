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
  <p class="card-sub">PrAImate owns the approved skill library. A CLI does not need to list a skill in its own catalogue for PrAImate to deliver it.</p>
  <div class="loading-grid">
    <div class="loading-item">
      <span class="loading-badge">1</span>
      <div><strong>Always include</strong><p>PrAImate reads the approved skill and includes its instructions in the next controlled request. This is the most portable option.</p></div>
    </div>
    <div class="loading-item">
      <span class="loading-badge">2</span>
      <div><strong>Load automatically</strong><p>Only managed/agentic runs can do this. The model receives <code>skill.load</code> and <code>skill.read</code> tools and requests a skill when the task needs it.</p></div>
    </div>
    <div class="loading-item">
      <span class="loading-badge">3</span>
      <div><strong>Load when requested</strong><p>Only managed/agentic runs can do this. The skill is not loaded until the model or workflow explicitly requests the approved reference.</p></div>
    </div>
  </div>
  <div class="skill-boundary"><strong>Important:</strong> native CLI sessions do not expose a verified skill broker today. On those sessions, dynamic modes are rejected or omitted; use <strong>Always include</strong> when the surface supports controlled delivery. A delivery receipt proves that PrAImate sent the payload, not that the model followed it.</div>
</section>

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

<style>
  .skill-help { margin: 18px 0; }
  .loading-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-top: 14px; }
  .loading-item { display: flex; gap: 10px; padding: 12px; border: 1px solid var(--border); border-radius: 8px; background: color-mix(in oklch, var(--surface, #151515) 88%, var(--accent) 12%); }
  .loading-item strong { font-size: 13px; }
  .loading-item p { margin: 5px 0 0; color: var(--text-dim); font-size: 12px; line-height: 1.45; }
  .loading-badge { display: grid; place-items: center; flex: 0 0 22px; height: 22px; border-radius: 50%; background: var(--accent); color: #fff; font-weight: 700; font-size: 12px; }
  .skill-boundary { margin-top: 14px; padding: 11px 12px; border-left: 3px solid var(--warn, #d39e00); background: color-mix(in oklch, var(--warn, #d39e00) 8%, transparent); color: var(--text-dim); font-size: 12px; line-height: 1.5; }
  code { font-family: var(--mono, monospace); font-size: .92em; }
  @media (max-width: 860px) { .loading-grid { grid-template-columns: 1fr; } }
</style>
