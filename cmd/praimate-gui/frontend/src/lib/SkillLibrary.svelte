<script>
  import { onMount } from 'svelte'
  import { api } from './api.js'
  let view = { Revision: 0, Versions: [], Drafts: {} }
  let summaries = []
  let tab = 'installed'
  let query = '', sourceFilter = 'all'
  let busy = false, error = '', notice = ''
  let kind = 'directory', source = '', gitRef = '', subpath = ''
  let inspection = null, selection = [], review = null, reviewed = false
  let installed = null, draft = null, draftKey = '', draftReview = null, parent = null
  let newRef = 'local/my-skill', fileIndex = 0, text = '', fileError = ''
  $: filteredSummaries = summaries.filter(version => {
    const text = `${version.name || ''} ${version.description || ''} ${version.ref || ''}`.toLowerCase()
    const sourceMatches = sourceFilter === 'all' || (sourceFilter === 'builtin' ? version.source_kind === 'builtin' : sourceFilter === 'own' ? version.source_kind === 'own' : !['builtin', 'own'].includes(version.source_kind))
    return sourceMatches && text.includes(query.trim().toLowerCase())
  })
  const encode = (s) => btoa(Array.from(new TextEncoder().encode(s), b => String.fromCharCode(b)).join(''))
  const decode = (s) => new TextDecoder('utf-8', { fatal: true }).decode(Uint8Array.from(atob(s || ''), c => c.charCodeAt(0)))
  async function refresh() {
    const result = await api.skillLibraryV2({ action: 'list' })
    view = result.view
    summaries = result.summaries || []
  }
  async function run(fn) {
    if (busy) return
    busy = true; error = ''; notice = ''
    try { await fn() } catch (e) { error = String(e) } finally { busy = false }
  }
  onMount(() => run(refresh))
  const command = (action, args = {}) => api.skillLibraryV2({ action, revision: view.Revision, ...args })
  function invalidateImport() { inspection = null; selection = []; review = null; reviewed = false }
  function sourceRequest() { return { kind, source, git_ref: gitRef, subpath } }
  async function pickSource() {
    const selected = await api.pickSkillSourceV2(kind)
    if (selected) { source = selected; invalidateImport() }
  }
  async function inspect() {
    invalidateImport()
    inspection = await command('inspect', sourceRequest())
    if (inspection.git_ref) gitRef = inspection.git_ref
    selection = inspection.packages.map(p => ({ index: p.index, ref: `imported/${p.manifest.Name}`, shared: [], selected: true }))
  }
  async function previewSelection() {
    const choices = selection.filter(p => p.selected).map(({selected, ...p}) => p)
    if (!choices.length) throw new Error('Select at least one candidate')
    review = await command('inspect', { ...sourceRequest(), selections: choices })
    reviewed = false
  }
  async function install() {
    await command('install', { ...sourceRequest(), selections: selection.filter(p => p.selected).map(({selected, ...p}) => p), review: review.review })
    invalidateImport(); await refresh()
    tab = 'installed'
    notice = 'Installed without approval or activation. Review a version below before selecting it in a session.'
  }
  async function read(v) {
    installed = await command('read', { ref: v.ref, digest: v.digest })
    reviewed = false
  }
  async function approve(approved) {
    const v = installed.version
    await command('approve', { ref: v.ref, digest: v.digest, review: v.digest, approved })
    await refresh(); await read(v)
    notice = approved ? 'This exact version is approved for selection, not granted command permissions.' : 'Approval revoked. Future controlled payloads must revalidate it.'
  }
  function openFile(index) {
    fileIndex = index; fileError = ''; text = ''
    try { text = decode(draft.files[index].Content) } catch (_) { fileError = 'Binary resource preserved unchanged; edit its source and reimport to replace it.' }
  }
  function editText() {
    draft.files[fileIndex].Content = encode(text)
    draftReview = null
  }
  async function loadDraft(key) {
    draftKey = key; draft = await command('draft-read', { key }); draftReview = null; openFile(0)
  }
  async function create() {
    const result = await command('draft-create', { new_ref: newRef, files: [{ Path: 'SKILL.md', Content: encode('---\nname: my-skill\ndescription: Describe when to use this procedure.\n---\n\nWrite the procedure here.\n') }] })
    parent = null; await refresh(); await loadDraft(result.key)
  }
  async function beginEdit(fork) {
    const v = installed.version
    const result = await command(fork ? 'fork' : 'edit', { ref: v.ref, digest: v.digest, new_ref: newRef })
    parent = v; await refresh(); await loadDraft(result.key); installed = null; tab = 'authoring'
  }
  async function saveDraft() {
    await command('draft-save', { key: draftKey, files: draft.files, draft_revision: draft.draft_revision }); await refresh()
    draft = await command('draft-read', { key: draftKey })
    draftReview = null; notice = 'Draft saved. Published versions and existing locks are unchanged.'
  }
  async function previewDraft() {
    await saveDraft()
    draftReview = await command('draft-preview', { key: draftKey, ref: parent?.ref, digest: parent?.digest })
  }
  async function publish() {
    await command('publish', { key: draftKey, review: draftReview.review }); await refresh(); draftReview = null
    notice = 'Published immutable version. Review and approve it separately; existing chats keep their old lock.'
  }
  function addResource() {
    const path = prompt('Relative resource path, for example references/example.md')
    if (!path) return
    draft.files = [...draft.files, { Path: path, Content: encode('') }]
    draftReview = null; openFile(draft.files.length - 1)
  }
  function filePreview(f) { try { return decode(f.Content) } catch (_) { return '[Binary file — preserved in the package]' } }
</script>

<section aria-label="Skill library">
  {#if error}<div class="banner" role="alert">{error}</div>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}

  <nav class="skill-tabs" aria-label="Skill sections">
    <button class:active={tab === 'installed'} on:click={() => (tab = 'installed')}>Installed <span>{summaries.length}</span></button>
    <button class:active={tab === 'import'} on:click={() => (tab = 'import')}>Import</button>
    <button class:active={tab === 'authoring'} on:click={() => (tab = 'authoring')}>Authoring <span>{Object.keys(view.Drafts || {}).length}</span></button>
  </nav>

  {#if tab === 'installed'}
    <div class="section-head">
      <div><h2>Installed skills</h2><p>Skills available to agents and sessions on this computer.</p></div>
      <button class="btn" disabled={busy} on:click={() => run(refresh)}>Refresh</button>
    </div>
    <div class="library-tools">
      <input class="field" type="search" bind:value={query} placeholder="Search skills" aria-label="Search installed skills" />
      <select class="field" bind:value={sourceFilter} aria-label="Filter skills by source">
        <option value="all">All sources</option><option value="builtin">Built in</option><option value="external">Imported</option><option value="own">Created here</option>
      </select>
    </div>
    <div class="skill-grid">
      {#each filteredSummaries as v (`${v.ref}@${v.digest}`)}
        <article class="skill-card">
          <div class="skill-card-head"><strong>{v.name || v.ref.split('/').pop()}</strong><span class:ready={v.approved}>{v.approved ? 'Ready' : 'Review required'}</span></div>
          <p>{v.description || 'No description provided.'}</p>
          <div class="skill-meta">{v.source_kind === 'builtin' ? 'Built in' : v.source_kind === 'own' ? 'Created here' : 'Imported'}{v.derived_source ? ' · Fork' : ''}</div>
          <button class="btn" disabled={busy} on:click={() => run(() => read(v))}>Open details</button>
        </article>
      {/each}
      {#if !busy && !summaries.length}<div class="empty-state">No skills installed yet. Use Import or Authoring to add one.</div>
      {:else if !busy && !filteredSummaries.length}<div class="empty-state">No skills match this search.</div>{/if}
    </div>
  {:else if tab === 'import'}
    <div class="section-head"><div><h2>Import skills</h2><p>Add a folder, ZIP package, or pinned GitHub repository to the shared library.</p></div></div>
    <div class="import-panel">
    <label>Source type <select class="field" bind:value={kind} on:change={invalidateImport}><option value="directory">Local folder</option><option value="zip">ZIP archive</option><option value="github">GitHub repository</option></select></label>
    <div class="field-label">{kind === 'github' ? 'Repository URL' : kind === 'zip' ? 'ZIP file' : 'Skill folder'}</div>
    <div class="source-row">
      <input class="field" bind:value={source} on:input={invalidateImport} placeholder={kind === 'github' ? 'https://github.com/owner/repository' : 'Choose a local source'} />
      {#if kind !== 'github'}<button class="btn" disabled={busy} on:click={() => run(pickSource)}>Browse…</button>{/if}
    </div>
    {#if kind === 'github'}
      <label>Branch, tag or commit <span class="hint">optional</span><input class="field" bind:value={gitRef} on:input={invalidateImport}/></label>
      <label>Folder inside repository <span class="hint">optional</span><input class="field" bind:value={subpath} on:input={invalidateImport}/></label>
      <p class="hint">PrAImate resolves and pins the exact commit. Credential-bearing URLs and private-network destinations are refused.</p>
    {/if}
    <button class="btn primary" disabled={busy || !source.trim()} on:click={() => run(inspect)}>Review source</button>
    {#if inspection}
      {#each inspection.packages as p, i}
        <div class="candidate">
          <label class="candidate-title"><input type="checkbox" bind:checked={selection[i].selected} on:change={() => { review = null }} /><strong>{p.manifest.Name}</strong></label>
          <p>{p.manifest.Description}</p>
          <label>Library name <input class="field" bind:value={selection[i].ref} on:input={() => { review = null }} /></label>
          <p class="hint">License: {p.manifest.License || 'Not declared. Review before redistributing.'}</p>
          {#each inspection.shared || [] as shared}
            <label><input type="checkbox" on:change={(e) => {
              selection[i].shared = e.currentTarget.checked ? [...selection[i].shared, { Source: shared, Destination: shared }] : selection[i].shared.filter(a => a.Source !== shared)
              review = null
            }} />Include shared resource: {shared}</label>
          {/each}
        </div>
      {/each}
      <button class="btn" disabled={busy} on:click={() => run(previewSelection)}>Review selected content</button>
    {/if}
    {#if review}
      {#each review.packages as p}
        <h3>{p.ref}</h3><code>{p.digest}</code>
        {#each p.files as f}<details><summary>{f.Path}{f.Executable ? ' · executable intent (not permission)' : ''}</summary><pre>{filePreview(f)}</pre></details>{/each}
      {/each}
      <label><input type="checkbox" bind:checked={reviewed}/>I reviewed the exact selected content and resource/license associations.</label>
      <button class="btn primary" disabled={busy || !reviewed} on:click={() => run(install)}>Install reviewed selection</button>
    {/if}
    </div>
  {:else}
    <div class="section-head"><div><h2>Authoring</h2><p>Create and publish reusable skills without leaving PrAImate.</p></div></div>
    <div class="authoring-panel">
    <label>New library name<input class="field" bind:value={newRef}/></label>
    <button class="btn primary" disabled={busy} on:click={() => run(create)}>Create draft</button>
    <div class="draft-list">{#each Object.keys(view.Drafts || {}) as key}<button class="btn" disabled={busy} on:click={() => run(() => { parent = null; return loadDraft(key) })}>Open draft {key}</button>{/each}</div>
    {#if draft}
      <div class="card">
        <h3>Draft</h3>
        <label>Resource<select class="field" value={fileIndex} on:change={(e) => openFile(Number(e.currentTarget.value))}>{#each draft.files as f, i}<option value={i}>{f.Path}</option>{/each}</select></label>
        {#if fileError}<p>{fileError}</p>{:else}<textarea class="field" rows="16" bind:value={text} on:input={editText} aria-label="Draft resource content"></textarea>{/if}
        <button class="btn" disabled={busy} on:click={addResource}>Add text resource</button>
        <button class="btn" disabled={busy} on:click={() => run(saveDraft)}>Save draft</button>
        <button class="btn" disabled={busy} on:click={() => run(previewDraft)}>Review publication</button>
        {#if draftReview}
          {#each draftReview.changes?.Changes || [] as change}<details><summary>Changed: {change.Path}</summary><h4>Before</h4><pre>{change.Before ? filePreview(change.Before) : '[Absent]'}</pre><h4>After</h4><pre>{change.After ? filePreview(change.After) : '[Removed]'}</pre></details>{/each}
          <button class="btn primary" disabled={busy} on:click={() => run(publish)}>Publish (approval still required)</button>
        {/if}
      </div>
    {/if}
    </div>
  {/if}

  {#if installed}
    <!-- svelte-ignore a11y-click-events-have-key-events -->
    <!-- svelte-ignore a11y-no-static-element-interactions -->
    <div class="modal-backdrop" on:click|self={() => (installed = null)}>
    <div class="modal-content detail-modal" role="dialog" aria-modal="true">
      <h2>{summaries.find(v => v.ref === installed.version.ref && v.digest === installed.version.digest)?.name || installed.version.ref}</h2>
      <p>{summaries.find(v => v.ref === installed.version.ref && v.digest === installed.version.digest)?.description || ''}</p>
      <p>Status: {installed.approved ? 'Ready to use' : 'Review required'}. Approval is not evidence of runtime loading.</p>
      <button class="btn" disabled={busy} on:click={() => run(async () => { if (!confirm('Export only the package files? Review resources for secrets and preserve licenses before sharing.')) return; const path = await api.exportSkillPackageV2(installed.version.ref, installed.version.digest); if (path) notice = `Exported ${path}` })}>Export package ZIP</button>
      {#each installed.files as f}<details><summary>{f.Path}{f.Executable ? ' · executable intent' : ''}</summary><pre>{filePreview(f)}</pre></details>{/each}
      <label><input type="checkbox" bind:checked={reviewed}/>I reviewed this exact version. Approval does not grant execution permissions.</label>
      <button class="btn" disabled={busy || (!installed.approved && !reviewed)} on:click={() => run(() => approve(!installed.approved))}>{installed.approved ? 'Revoke approval' : 'Approve exact version'}</button>
      <label>New fork identity<input class="field" bind:value={newRef}/></label>
      <button class="btn" disabled={busy} on:click={() => run(() => beginEdit(true))}>Fork to draft</button>
      {#if installed.version.source_kind === 'own'}<button class="btn" disabled={busy} on:click={() => run(() => beginEdit(false))}>Edit as new version</button>{/if}
      <details class="technical"><summary>Technical identity</summary><code>{installed.version.ref}@{installed.version.digest}</code></details>
      <div class="detail-actions"><button class="btn" on:click={() => (installed = null)}>Close</button></div>
    </div>
    </div>
  {/if}
</section>

<style>
  section { margin-top: 16px; }
  label { display: block; margin: 10px 0; }
  pre { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 300px; overflow: auto; padding: 12px; background: var(--bg-panel); }
  code { overflow-wrap: anywhere; }
  details { margin: 12px 0; }
  summary { cursor: pointer; }
  .btn { margin: 4px; }
  .skill-tabs { display: flex; gap: 2px; border-bottom: 1px solid var(--border); margin-bottom: 18px; }
  .skill-tabs button { border: 0; border-bottom: 2px solid transparent; background: none; color: var(--text-dim); padding: 10px 14px; cursor: pointer; }
  .skill-tabs button.active { color: var(--text); border-bottom-color: var(--accent); }
  .skill-tabs span { margin-left: 5px; font-size: 11px; opacity: .7; }
  .section-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; margin-bottom: 12px; }
  .section-head h2 { margin: 0 0 3px; }
  .section-head p, .skill-card p { margin: 0; color: var(--text-dim); font-size: 12px; line-height: 1.45; }
  .skill-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 10px; }
  .library-tools { display: grid; grid-template-columns: minmax(220px, 1fr) 180px; gap: 8px; margin-bottom: 12px; }
  .skill-card { border: 1px solid var(--border); border-radius: var(--radius); padding: 14px; display: grid; gap: 10px; }
  .skill-card-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; }
  .skill-card-head span { color: var(--warn, #d39e00); font-size: 11px; }
  .skill-card-head span.ready { color: var(--ok, #35c76f); }
  .skill-meta, .hint { color: var(--text-dim); font-size: 11px; }
  .field-label { margin: 10px 0 5px; }
  .import-panel, .authoring-panel { max-width: 760px; }
  .source-row { display: flex; gap: 8px; align-items: center; }
  .source-row .field { flex: 1; }
  .candidate { border-top: 1px solid var(--border); margin-top: 16px; padding-top: 12px; }
  .candidate-title { display: flex; gap: 8px; align-items: center; }
  .draft-list { display: flex; flex-wrap: wrap; gap: 4px; margin: 10px 0; }
  .empty-state { grid-column: 1 / -1; text-align: center; color: var(--text-dim); padding: 40px 12px; }
  .detail-modal { max-width: 760px; max-height: 88vh; overflow-y: auto; }
  .technical { border-top: 1px solid var(--border); padding-top: 8px; }
  .detail-actions { display: flex; justify-content: flex-end; }
</style>
