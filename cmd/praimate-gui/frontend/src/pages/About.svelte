<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import {
    privacyDisclosures,
    privacyIntroduction,
  } from '../lib/privacyContent.js'
  import logo from '../assets/monke-icon.png'

  let info = null
  let error = ''

  onMount(async () => {
    try {
      info = await api.about()
    } catch (e) {
      error = String(e)
    }
  })
</script>

<h1>About</h1>
<p class="subtitle">Version, platform, privacy, and local-data boundaries.</p>

{#if error}<div class="banner">{error}</div>{/if}

<div class="card hero">
  <img src={logo} alt="PrAImate" />
  <div>
    <div class="product">{info?.name || 'PrAImate'}</div>
    <div class="card-sub">
      {#if info}Version {info.version} · {info.os}/{info.arch}{:else}Loading runtime information…{/if}
    </div>
    <div class="tagline">One harness, every agent — workflows & MCP.</div>
  </div>
</div>

<h2>Privacy</h2>
<div class="card privacy">
  <p class="privacy-introduction">{privacyIntroduction}</p>
  <div class="privacy-list">
    {#each privacyDisclosures as disclosure}
      <div class="privacy-item">
        <strong>{disclosure.title}</strong>
        <p>{disclosure.body}</p>
      </div>
    {/each}
  </div>
</div>

<h2>Encryption status</h2>
<div class="card stack">
  <div class="row">
    <span class="pill {info?.databaseEncrypted ? 'ok' : 'err'}">
      {info?.databaseEncrypted ? 'Encrypted' : 'Unavailable'}
    </span>
    <strong>{info?.databaseCipher || 'AES-256-XTS'}</strong>
  </div>
  {#if info?.dbPath}
    <div class="path"><span>Database</span><code>{info.dbPath}</code></div>
    <div class="path"><span>Password envelope</span><code>{info.dbKeyPath}</code></div>
  {/if}
</div>

<h2>Compatibility</h2>
<div class="card stack">
  <p>Available for Linux, Windows and macOS on Apple Silicon.</p>
</div>

<style>
  h2 { margin: 24px 0 8px; font-size: 16px; }
  .hero { display: flex; align-items: center; gap: 16px; }
  .hero img { width: 58px; height: 58px; border-radius: 13px; }
  .product { font-size: 22px; font-weight: 750; }
  .tagline { margin-top: 7px; color: var(--text-dim); }
  .stack { display: flex; flex-direction: column; gap: 12px; }
  .stack p { margin: 4px 0 0; color: var(--text-dim); line-height: 1.55; }
  .privacy { padding-top: 0; padding-bottom: 0; }
  .privacy-introduction {
    margin: 0;
    padding: 15px 0;
    color: var(--text-dim);
    line-height: 1.6;
  }
  .privacy-list { border-top: 1px solid var(--border); }
  .privacy-item { padding: 14px 0; border-bottom: 1px solid var(--border); }
  .privacy-item:last-child { border-bottom: 0; }
  .privacy-item p {
    margin: 4px 0 0;
    color: var(--text-dim);
    line-height: 1.55;
  }
  .path { display: grid; grid-template-columns: 90px minmax(0, 1fr); gap: 8px; }
  .path span { color: var(--text-dim); }
  .path code { overflow-wrap: anywhere; }
</style>
