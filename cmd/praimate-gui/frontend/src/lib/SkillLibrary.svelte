<script>
  import { onMount } from 'svelte'
  import { api } from './api.js'
  import { showConfirm } from './stores.js'

  let view = { Revision: 0, Versions: [], Drafts: {} }
  let summaries = []
  let tab = 'installed'
  let query = '', sourceFilter = 'all'
  let busy = false, error = '', notice = ''
  let kind = 'directory', source = '', gitRef = '', subpath = ''
  let inspection = null, selection = [], review = null, reviewed = false
  let installed = null, draft = null, draftKey = '', draftReview = null, parent = null
  let newRef = 'local/my-skill', fileIndex = 0, text = '', fileError = ''
  let newResourcePath = ''
  let showAddResource = false

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
    const ref = newRef.trim() || 'local/my-skill'
    const result = await command('draft-create', { new_ref: ref, files: [{ Path: 'SKILL.md', Content: encode('---\nname: my-skill\ndescription: Describe when to use this procedure.\n---\n\nWrite the procedure here.\n') }] })
    parent = null; await refresh(); await loadDraft(result.key)
  }

  async function deleteDraft(key) {
    const ok = await showConfirm({
      title: 'Delete Skill Draft',
      message: `Delete draft "${key}"? All unpublished edits in this draft will be removed.`
    })
    if (!ok) return
    await command('draft-delete', { key })
    if (draftKey === key) {
      draft = null
      draftKey = ''
      draftReview = null
    }
    await refresh()
    notice = 'Draft deleted.'
  }

  async function deleteInstalled(version) {
    const ok = await showConfirm({
      title: 'Remove Installed Skill',
      message: `Remove "${version.name || version.ref}" from your installed skills catalogue?`
    })
    if (!ok) return
    await command('forget', { ref: version.ref, digest: version.digest })
    installed = null
    await refresh()
    notice = 'Skill removed from installed catalogue.'
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
    const path = newResourcePath.trim()
    if (!path) return
    draft.files = [...draft.files, { Path: path, Content: encode('') }]
    draftReview = null
    newResourcePath = ''
    showAddResource = false
    openFile(draft.files.length - 1)
  }

  function removeFile(idx) {
    if (draft.files[idx].Path === 'SKILL.md') return
    draft.files = draft.files.filter((_, i) => i !== idx)
    draftReview = null
    openFile(0)
  }

  function filePreview(f) { try { return decode(f.Content) } catch (_) { return '[Binary file — preserved in the package]' } }
</script>

<section aria-label="Skill library">
  {#if error}<div class="banner error-banner" role="alert">{error}</div>{/if}
  {#if notice}<div class="card card-sub" style="border-left:3px solid var(--ok); margin-bottom:12px">{notice}</div>{/if}

  <nav class="skill-tabs" aria-label="Skill sections">
    <button class:active={tab === 'installed'} on:click={() => (tab = 'installed')}>Installed <span>{summaries.length}</span></button>
    <button class:active={tab === 'import'} on:click={() => (tab = 'import')}>Import</button>
    <button class:active={tab === 'authoring'} on:click={() => (tab = 'authoring')}>Authoring <span>{Object.keys(view.Drafts || {}).length}</span></button>
  </nav>

  {#if tab === 'installed'}
    <div class="section-head">
      <div><h2>Installed skills</h2><p>Skills available to agents and sessions on this computer.</p></div>
    </div>
    <div class="library-tools">
      <input class="field" type="search" bind:value={query} placeholder="Search installed skills…" aria-label="Search installed skills" />
      <select class="field" bind:value={sourceFilter} aria-label="Filter by source kind">
        <option value="all">All sources ({summaries.length})</option>
        <option value="builtin">Built-in</option>
        <option value="own">Authored locally</option>
        <option value="imported">Imported</option>
      </select>
    </div>
    <div class="skill-grid">
      {#each filteredSummaries as v (`${v.ref}@${v.digest}`)}
        <div class="skill-card">
          <div class="skill-card-head">
            <strong>{v.name || v.ref.split('/').pop()}</strong>
            <span class:ready={v.approved}>{v.approved ? '✓ Approved' : 'Review required'}</span>
          </div>
          <p>{v.description || 'No description provided.'}</p>
          <div class="skill-meta mono">{v.ref}</div>
          <div class="row" style="justify-content:flex-end; gap:6px; margin-top:4px; flex-wrap:wrap">
            <button class="btn sm" on:click={() => run(async () => { await api.openSkillInStudio(v.ref, v.digest); notice = 'Studio opened for skill.' })}>Open in Studio</button>
            <button class="btn sm primary" on:click={() => run(() => read(v))}>Inspect</button>
            <button class="btn sm danger" on:click={() => run(() => deleteInstalled(v))} title="Remove from installed skills">Remove</button>
          </div>
        </div>
      {/each}
      {#if !busy && !summaries.length}<div class="empty-state">No skills installed yet. Use Import or Authoring to add one.</div>
      {:else if !busy && summaries.length && !filteredSummaries.length}<div class="empty-state">No skills match the current search/filter.</div>{/if}
    </div>

  {:else if tab === 'import'}
    <div class="section-head"><div><h2>Import skills</h2><p>Inspect local directories, ZIP archives, or Git repositories before importing.</p></div></div>
    <div class="authoring-panel">
    <div class="row source-row">
      <select class="field" bind:value={kind} on:change={invalidateImport} aria-label="Source kind">
        <option value="directory">Local folder</option>
        <option value="zip">ZIP package</option>
        <option value="github">Git repository</option>
      </select>
      {#if kind !== 'github'}<button class="btn" disabled={busy} on:click={() => run(pickSource)}>Browse…</button>{/if}
      <input class="field" bind:value={source} placeholder={kind === 'github' ? 'https://github.com/org/repo' : 'Path to source'} on:input={invalidateImport} aria-label="Source path or URL" />
    </div>
    {#if kind === 'github'}
      <div class="row source-row" style="margin-top:8px">
        <input class="field" bind:value={gitRef} placeholder="main (git branch or tag)" on:input={invalidateImport} aria-label="Git ref" />
        <input class="field" bind:value={subpath} placeholder="Optional repository subpath" on:input={invalidateImport} aria-label="Subpath" />
      </div>
    {/if}
    <button class="btn primary" style="margin-top:12px" disabled={busy || !source.trim()} on:click={() => run(inspect)}>Inspect candidates</button>
    {#if inspection}
      {#each inspection.packages as p, i}
        <div class="candidate">
          <label class="candidate-title"><input type="checkbox" bind:checked={selection[i].selected} /><strong>{p.manifest.Name}</strong> ({p.relative_path})</label>
          <p class="card-sub">{p.manifest.Description}</p>
          <label>Imported library name<input class="field" bind:value={selection[i].ref} /></label>
          {#if inspection.shared?.length}
            <div class="card-sub">Shared resources available to this skill:</div>
            {#each inspection.shared || [] as shared}
              <label class="card-sub mono"><input type="checkbox" bind:group={selection[i].shared} value={shared.Path} />{shared.Path}</label>
            {/each}
          {/if}
        </div>
      {/each}
      <button class="btn primary" style="margin-top:12px" disabled={busy || !selection.some(p => p.selected)} on:click={() => run(previewSelection)}>Review selected content</button>
    {/if}
    {#if review}
      {#each review.packages as p}
        <h3>Review {p.ref}</h3>
        {#each p.files as f}<details><summary>{f.Path}{f.Executable ? ' · executable intent' : ''}</summary><pre>{filePreview(f)}</pre></details>{/each}
      {/each}
      <label><input type="checkbox" bind:checked={reviewed} />I inspected the exact file content above. Importing will make this version available for selection.</label>
      <button class="btn primary" disabled={busy || !reviewed} on:click={() => run(install)}>Install reviewed versions</button>
    {/if}
    </div>

  {:else}
    <!-- Authoring Tab -->
    <div class="section-head">
      <div><h2>Authoring</h2><p>Create, edit, and publish reusable skills without leaving PrAImate.</p></div>
    </div>
    
    <div class="authoring-panel">
      <!-- Create new skill bar -->
      <div class="card" style="margin-bottom:16px">
        <div class="card-title">Create a new skill</div>
        <p class="card-sub">Start with a clean SKILL.md template and build procedures, tools, and checklists with full Studio assistance.</p>
        <div class="row" style="margin-top:8px; gap:8px; align-items:center; flex-wrap:wrap">
          <input class="field grow mono" bind:value={newRef} placeholder="local/my-skill-name" />
          <button class="btn primary" disabled={busy || !newRef.trim()} on:click={() => run(async () => { await api.createSkillInStudio(newRef); notice = 'Studio opened for skill editing.' })}>
            + Create in Studio
          </button>
          <button class="btn" disabled={busy || !newRef.trim()} on:click={() => run(create)}>Quick draft</button>
        </div>
      </div>

      <!-- Drafts list -->
      <div class="card" style="margin-bottom:16px">
        <div class="card-title">Active Drafts ({Object.keys(view.Drafts || {}).length})</div>
        {#if Object.keys(view.Drafts || {}).length === 0}
          <div class="empty">No drafts in progress. Enter a name above to start a new draft.</div>
        {:else}
          <div class="draft-grid">
            {#each Object.keys(view.Drafts || {}) as key}
              <div class="draft-card" class:active={draftKey === key}>
                <div class="grow">
                  <div class="draft-title mono">{key}</div>
                  <div class="card-sub">{draftKey === key ? 'Currently open' : 'Saved draft'}</div>
                </div>
                <div class="row" style="gap:6px">
                  <button class="btn sm" class:primary={draftKey === key} disabled={busy} on:click={() => run(() => { parent = null; return loadDraft(key) })}>
                    {draftKey === key ? 'Editing' : 'Open'}
                  </button>
                  <button class="btn sm danger" disabled={busy} on:click={() => run(() => deleteDraft(key))} title="Delete draft">
                    Delete
                  </button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>

      <!-- Editor for open draft -->
      {#if draft}
        <div class="card editor-card">
          <div class="row" style="align-items:center; justify-content:space-between; margin-bottom:12px">
            <div class="card-title" style="margin:0">Editing: <span class="mono">{draftKey}</span></div>
            <div class="row" style="gap:6px">
              <button class="btn sm" disabled={busy} on:click={() => run(saveDraft)}>Save Draft</button>
              <button class="btn sm danger" disabled={busy} on:click={() => run(() => deleteDraft(draftKey))}>Delete Draft</button>
            </div>
          </div>

          <!-- File selector tabs -->
          <div class="draft-files-bar">
            {#each draft.files as f, i}
              <div class="file-pill" class:active={fileIndex === i}>
                <button class="file-pill-btn" on:click={() => openFile(i)}>{f.Path}</button>
                {#if f.Path !== 'SKILL.md'}
                  <button class="file-pill-x" on:click={() => removeFile(i)} title="Remove file">×</button>
                {/if}
              </div>
            {/each}
            <button class="btn sm" on:click={() => (showAddResource = !showAddResource)}>+ Add file</button>
          </div>

          {#if showAddResource}
            <div class="row" style="margin:10px 0; gap:8px; background:var(--bg-raised); padding:10px; border-radius:var(--radius-sm)">
              <input class="field grow mono" bind:value={newResourcePath} placeholder="e.g. references/checklist.md or scripts/run.sh" />
              <button class="btn primary sm" on:click={addResource} disabled={!newResourcePath.trim()}>Add</button>
              <button class="btn sm" on:click={() => (showAddResource = false)}>Cancel</button>
            </div>
          {/if}

          <!-- File content textarea -->
          {#if fileError}
            <div class="banner">{fileError}</div>
          {:else}
            <textarea class="field mono file-editor" rows="18" bind:value={text} on:input={editText} aria-label="Draft resource content" placeholder="Write SKILL.md instructions or resource content..."></textarea>
          {/if}

          <div class="row actions" style="margin-top:14px; gap:8px">
            <button class="btn primary" disabled={busy} on:click={() => run(previewDraft)}>Review publication</button>
          </div>

          {#if draftReview}
            <div class="review-box" style="margin-top:16px; border-top:1px solid var(--border); padding-top:14px">
              <h4>Review Changes before Publishing</h4>
              {#each draftReview.changes?.Changes || [] as change}
                <details open><summary>Changed: {change.Path}</summary>
                  <h4>Before</h4><pre>{change.Before ? filePreview(change.Before) : '[Absent]'}</pre>
                  <h4>After</h4><pre>{change.After ? filePreview(change.After) : '[Removed]'}</pre>
                </details>
              {/each}
              <button class="btn primary" style="margin-top:10px" disabled={busy} on:click={() => run(publish)}>Publish (approval still required)</button>
            </div>
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
        <p class="card-sub">Status: <strong class:ok={installed.approved}>{installed.approved ? 'Ready & Approved' : 'Review required'}</strong>.</p>
        
        <div class="row" style="gap:8px; margin: 12px 0">
          <button class="btn sm" disabled={busy} on:click={() => run(async () => {
            const ok = await showConfirm({ title: 'Export Skill Package', message: 'Export only the package files? Review resources for secrets before sharing.', tone: 'primary', confirmLabel: 'Export' })
            if (!ok) return
            const path = await api.exportSkillPackageV2(installed.version.ref, installed.version.digest)
            if (path) notice = `Exported to ${path}`
          })}>Export package ZIP</button>
          <button class="btn sm" disabled={busy || (!installed.approved && !reviewed)} on:click={() => run(() => approve(!installed.approved))}>
            {installed.approved ? 'Revoke approval' : 'Approve exact version'}
          </button>
        </div>

        {#each installed.files as f}
          <details><summary>{f.Path}{f.Executable ? ' · executable intent' : ''}</summary><pre>{filePreview(f)}</pre></details>
        {/each}

        {#if !installed.approved}
          <label style="margin:12px 0"><input type="checkbox" bind:checked={reviewed}/>I reviewed this exact version.</label>
        {/if}

        <div class="row" style="gap:8px; margin-top:14px; border-top:1px solid var(--border); padding-top:12px">
          <button class="btn sm" disabled={busy} on:click={() => run(() => beginEdit(true))}>Fork to new draft</button>
          {#if installed.version.source_kind === 'own'}
            <button class="btn sm" disabled={busy} on:click={() => run(() => beginEdit(false))}>Edit as new version</button>
          {/if}
          <button class="btn sm danger" disabled={busy} on:click={() => run(() => deleteInstalled(installed.version))}>Remove skill</button>
        </div>

        <details class="technical" style="margin-top:12px"><summary>Technical identity</summary><code>{installed.version.ref}@{installed.version.digest}</code></details>
        <div class="detail-actions" style="margin-top:14px"><button class="btn" on:click={() => (installed = null)}>Close</button></div>
      </div>
    </div>
  {/if}
</section>

<style>
  section { margin-top: 16px; }
  label { display: block; margin: 10px 0; }
  pre { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 300px; overflow: auto; padding: 12px; background: var(--bg-raised); color: var(--text); border: 1px solid var(--border); border-radius: var(--radius-sm); font-family: var(--mono); }
  code { overflow-wrap: anywhere; font-family: var(--mono); }
  details { margin: 10px 0; }
  summary { cursor: pointer; color: var(--text); font-weight: 500; }
  .btn { margin: 2px; }
  .skill-tabs { display: flex; gap: 2px; border-bottom: 1px solid var(--border); margin-bottom: 18px; }
  .skill-tabs button { border: 0; border-bottom: 2px solid transparent; background: none; color: var(--text-dim); padding: 10px 14px; cursor: pointer; font-family: var(--sans); }
  .skill-tabs button.active { color: var(--text); border-bottom-color: var(--accent); font-weight: 600; }
  .skill-tabs span { margin-left: 5px; font-size: 11px; opacity: .7; }
  .section-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; margin-bottom: 12px; }
  .section-head h2 { margin: 0 0 3px; color: var(--text); }
  .section-head p, .skill-card p { margin: 0; color: var(--text-dim); font-size: 12px; line-height: 1.45; }
  .skill-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 10px; }
  .library-tools { display: grid; grid-template-columns: minmax(220px, 1fr) 180px; gap: 8px; margin-bottom: 12px; }
  .skill-card { background: var(--bg-panel); border: 1px solid var(--border); border-radius: var(--radius); padding: 14px; display: grid; gap: 10px; color: var(--text); }
  .skill-card-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; }
  .skill-card-head span { color: var(--warn); font-size: 11px; font-weight: 500; }
  .skill-card-head span.ready { color: var(--ok); }
  .skill-meta { color: var(--text-dim); font-size: 11px; }
  .import-panel, .authoring-panel { max-width: 780px; }
  .source-row { display: flex; gap: 8px; align-items: center; }
  .source-row .field { flex: 1; }
  .candidate { border-top: 1px solid var(--border); margin-top: 16px; padding-top: 12px; }
  .candidate-title { display: flex; gap: 8px; align-items: center; }
  .draft-grid { display: grid; gap: 8px; margin-top: 8px; }
  .draft-card { display: flex; align-items: center; justify-content: space-between; padding: 10px 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--bg-raised); }
  .draft-card.active { border-color: var(--accent); }
  .draft-title { font-weight: 600; font-size: 13px; color: var(--text); }
  .draft-files-bar { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin: 10px 0; }
  .file-pill { display: inline-flex; align-items: center; background: var(--bg-raised); border: 1px solid var(--border); border-radius: var(--radius-sm); overflow: hidden; }
  .file-pill.active { border-color: var(--accent); background: var(--accent-soft); }
  .file-pill-btn { border: 0; background: none; color: var(--text); padding: 5px 10px; cursor: pointer; font-size: 12px; font-family: var(--mono); }
  .file-pill-x { border: 0; background: none; color: var(--text-dim); padding: 5px 8px; cursor: pointer; font-size: 14px; }
  .file-pill-x:hover { color: var(--err); }
  .file-editor { width: 100%; resize: vertical; line-height: 1.55; }
  .empty-state { grid-column: 1 / -1; text-align: center; color: var(--text-dim); padding: 40px 12px; }
  .detail-modal { max-width: 760px; max-height: 88vh; overflow-y: auto; }
  .technical { border-top: 1px solid var(--border); padding-top: 8px; }
  .detail-actions { display: flex; justify-content: flex-end; }
</style>
