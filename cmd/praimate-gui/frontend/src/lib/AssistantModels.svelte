<script>
  import { onMount, onDestroy } from 'svelte'
  import { api } from './api.js'
  import { showConfirm } from './stores.js'
  let catalog = null
  let busy = ''
  let progress = null
  let error = ''
  let notice = ''
  let installing = false
  let poll = null
  let unsubscribe = () => {}
  const groups = [ ['assistant', 'Assistant models'], ['speech', 'Voice models'], ['runtime', 'Inference runtimes'] ]
  const size = (bytes) => bytes ? `${Math.round(bytes / 1048576)} MiB` : 'Size available in the release catalog'
  async function refresh() {
    catalog = await api.assistantArtifactCatalog()
    if (catalog.active) { busy = 'active'; installing = true; progress = catalog.progress }
    else { busy = ''; installing = false }
  }
  onMount(async () => {
    if (window.runtime?.EventsOn) unsubscribe = window.runtime.EventsOn('assistant:artifact-progress', (event) => { progress = event })
    poll = setInterval(() => { if (busy === 'active') refresh().catch((e) => { error = String(e) }) }, 2000)
    try { await refresh() } catch (e) { error = String(e) }
  })
  onDestroy(() => { unsubscribe?.(); clearInterval(poll) })
  async function ask(action, model) {
    const approved = await showConfirm({
      title: `${action === 'remove' ? 'Remove' : 'Install'} ${model.name}?`,
      message: action === 'remove' ? 'This removes the downloaded package from this device. Chats, typed input and other models are preserved.' : `Download approximately ${size(model.approximate_bytes)}${model.runtime_id ? ' plus its required runtime' : ''} from PrAImate GitHub Releases?`,
      confirmLabel: action === 'remove' ? 'Remove package' : 'Install package',
      tone: action === 'remove' ? 'danger' : 'primary',
    })
    if (approved) await operate(action, model)
  }
  async function operate(action, model) {
    busy = model.artifact_id; installing = action === 'install'; error = ''; notice = ''; progress = null
    try {
      if (action === 'install') { await api.installAssistantArtifact(model.artifact_id); notice = `${model.name} installed and verified.` }
      else if (action === 'verify') { const result = await api.verifyAssistantArtifact(model.artifact_id); notice = `${model.name} verified · ${result.artifact.license_name} · ${result.catalog_version}` }
      else { await api.removeAssistantArtifact(model.artifact_id); notice = `${model.name} removed. Other application data was preserved.` }
    } catch (e) { error = String(e) }
    finally { busy = ''; try { await refresh() } catch (e) { error = String(e) } }
  }
  async function cancel() {
    try { await api.cancelAssistantArtifactInstall(); notice = 'Cancellation requested. Partial downloads can be resumed.'; await refresh() }
    catch (e) { error = String(e) }
  }
</script>

<section class="assistant-models" aria-labelledby="assistant-models-title">
  <h2 id="assistant-models-title">Assistant &amp; Voice · Local models</h2>
  <p class="muted">Choose the models you want to install. Models stay on this device; installing a package does not enable the Assistant or microphone.</p>
  {#if error}<div class="banner" role="alert">{error}</div>{/if}
  {#if notice}<div class="card status" role="status">{notice}</div>{/if}
  {#if !catalog}<p class="muted">Loading model catalog…</p>
  {:else}
    {#if catalog.notice}<div class="card status">{catalog.notice}</div>{/if}
    <p class="muted source">Distributed through PrAImate GitHub Releases. Packages are verified against the release SHA256SUMS file. Download sizes below are estimates.</p>
    {#if busy && installing}
      <div class="card status" role="status" aria-live="polite">
        <strong>{progress?.phase || 'Preparing model installation…'}</strong>
        {#if progress}<p class="muted">{progress.artifact_id} · {size(progress.downloaded)} / {size(progress.total)}</p><progress max={progress.total || 1} value={progress.downloaded || 0}></progress>{/if}
        <button class="btn" on:click={cancel}>Cancel installation</button>
      </div>
    {/if}
    {#each groups as [kind, title]}
      <h3>{title}</h3>
      <div class="model-grid">
        {#each catalog.items.filter((model) => model.kind === kind) as model}
          <article class="card model">
            <div class="model-heading"><strong>{model.name}</strong>{#if model.installed}<span class="pill ok">Installed</span>{/if}</div>
            <p class="muted">{model.description}</p>
            <p class="muted">{size(model.approximate_bytes)}{#if model.default_context} · {model.default_context} context{/if}</p>
            <div class="actions">
              <button class="btn" disabled={!!busy || !catalog.distribution_ready} on:click={() => ask('install', model)}>{model.installed ? 'Repair package' : 'Install'}</button>
              {#if model.installed}<button class="btn" disabled={!!busy} on:click={() => operate('verify', model)}>Verify</button><button class="btn" disabled={!!busy} on:click={() => ask('remove', model)}>Remove</button>{/if}
            </div>
          </article>
        {/each}
      </div>
    {/each}
  {/if}
</section>

<style>
  .assistant-models { margin: 28px 0; }
  h2 { font-size: 18px; } h3 { margin: 22px 0 12px; font-size: 14px; }
  .model-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr)); gap: 12px; }
  .model { padding: 18px; display: flex; flex-direction: column; }
  .model-heading { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 8px; }
  .actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: auto; padding-top: 10px; }
  .status { padding: 16px; margin: 12px 0; }
  .source { font-size: 12px; }
  progress { width: 100%; accent-color: var(--accent); margin-bottom: 12px; }
</style>
