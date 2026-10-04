<script>
  import { createEventDispatcher, tick } from 'svelte'
  import { api } from './api.js'
  import { focusDialog } from './focusDialog.js'
  import WorkerGraph from './WorkerGraph.svelte'
  import WorkerProfiles from './WorkerProfiles.svelte'
  import VoiceButton from './VoiceButton.svelte'
  import { activeWorkerRun, workerExecutions, workerEventLabel, workerLabel, workerTiers, workerUsage, workerTaskRoute } from './workerActivity.js'
  export let snapshot
  export let clis = []
  export let models = {}
  const dispatch = createEventDispatcher()
  let selected = ''
  let filter = 'all'
  let follow = true
  let focusActive = true
  let timeline
  let now = Date.now()
  let busy = false
  let error = ''
  let settings = null
  let showGraph = false
  let draft = ''
  let lastRun = ''
  let lastActivity = ''
  let visibleCount = 120
  let viewedWorker = ''
  $: active = activeWorkerRun(snapshot)
  $: executions = workerExecutions(snapshot)
  $: if (snapshot.id !== lastRun) { selected = ''; lastRun = snapshot.id; error = ''; draft = ''; settings = null; showGraph = snapshot.status === 'draft' }
  $: if (!executions.some(e => e.id === selected)) selected = executions.find(e => ['running', 'waiting'].includes(e.status))?.id || executions.at(-1)?.id || ''
  $: if (viewedWorker !== selected) { viewedWorker = selected; visibleCount = 120 }
  $: current = executions.find(e => e.id === selected)
  $: if (focusActive && active && executions.length) selectActive(executions)
  $: events = (current?.events || []).filter(e => filter === 'all' || (filter === 'io' ? ['input','request','stream','output','response'].includes(e.kind) : filter === 'delegation' ? ['delegation','delegated_result','delegated_error'].includes(e.kind) : filter === 'errors' ? ['error','failed','delegated_error'].includes(e.kind) : e.kind === 'reasoning'))
  $: usage = workerUsage(snapshot)
  $: tasks = snapshot.dag?.tasks || []
  $: activityKey = `${selected}:${filter}:${events.length}:${events.at(-1)?.text?.length}`
  $: if (activityKey !== lastActivity) { lastActivity = activityKey; if (follow) scrollLatest() }
  function clock(node) { const timer = setInterval(() => now = Date.now(), 1000); return { destroy: () => clearInterval(timer) } }
  function elapsed(start, end = now) { const seconds = Math.max(0, Math.floor((Number(end) - new Date(start).getTime()) / 1000)); return `${Math.floor(seconds / 60)}m ${seconds % 60}s` }
  async function scrollLatest() { await tick(); if (timeline) timeline.scrollTop = timeline.scrollHeight }
  function selectActive(executions) {
    const worker = executions.find(e => e.id === selected && e.status === 'running') || executions.find(e => e.status === 'running') || executions.find(e => e.status === 'waiting')
    if (worker) selected = worker.id
  }
  async function action(fn) {
    busy = true; error = ''
    const id = snapshot.id
    try { await fn(); if (snapshot.id === id) dispatch('refresh') }
    catch (e) { if (snapshot.id === id) error = String(e) }
    finally { busy = false }
  }
  function openSettings() {
    settings = JSON.parse(JSON.stringify({ workspace: snapshot.workspace, profiles: snapshot.profiles }))
    for (const p of settings.profiles) if (p.runtime === 'cli') dispatch('models', p.cli)
  }
  async function saveSettings() {
    await action(async () => { await api.updateWorkerRunConfig(snapshot.id, settings); settings = null })
  }
  async function retryWithoutTimeout() {
    const id = snapshot.id
    const config = { workspace: snapshot.workspace, profiles: snapshot.profiles.map(p => ({ ...p, timeoutSeconds: p.tier === 'primary' ? 0 : p.timeoutSeconds })) }
    await action(async () => {
      await api.updateWorkerRunConfig(id, config)
      await api.retryWorkerPlanning(id)
    })
  }
  async function continueRun() {
    if (!draft.trim()) return
    await action(async () => { await api.continueWorkerRun(snapshot.id, draft); draft = '' })
  }
</script>

<section class="monitor" aria-label="Worker execution monitor" use:clock>
  <div class="toolbar">
    <div class="run-state"><span class:live={active} class:failed={snapshot.status === 'failed'} class="dot"></span><strong>{snapshot.status === 'planning' ? 'Reasoner is preparing the task plan' : snapshot.status}</strong><span class="muted">{tasks.length ? `${tasks.filter(t => t.status === 'completed').length}/${tasks.length} tasks complete` : `${executions.length} worker assignments`}</span></div>
    <div class="buttons"><label><input type="checkbox" bind:checked={focusActive} /> Follow active worker</label><button disabled={busy || active} on:click={openSettings}>Run settings</button>{#if active}<button class="stop" disabled={busy} on:click={() => action(() => api.cancelWorkerRun(snapshot.id))}>Stop execution</button>{/if}</div>
  </div>
  {#if snapshot.error}<div class="failure" role="alert"><strong>Execution needs attention</strong><p>{snapshot.error}</p><small>Partial activity and task changes are retained. Inspect them before retrying.</small>{#if snapshot.dag && !tasks.length && !active}<div class="buttons"><button disabled={busy} on:click={openSettings}>Adjust reasoner settings</button><button disabled={busy} on:click={() => action(() => api.retryWorkerPlanning(snapshot.id))}>Retry planning</button>{#if snapshot.profiles?.some(p => p.tier === 'primary' && p.timeoutSeconds > 0) && /deadline|timeout/i.test(snapshot.error)}<button disabled={busy} on:click={retryWithoutTimeout}>Retry with no reasoner timeout</button>{/if}</div>{/if}</div>{/if}
  {#if snapshot.dag && !tasks.length}<p class="muted">Planning stage: only Reasoner runs. Middle and Fast start after a plan is ready and you review and run its tasks.</p>{/if}
  {#if error}<p class="failure" role="alert">{error}</p>{/if}
  <div class="profiles">
    {#each workerTiers as tier}
      {@const p = snapshot.profiles?.find(p => p.tier === tier.id)}
      {@const count = executions.filter(e => e.tier === tier.id && ['running','waiting'].includes(e.status)).length}
      <div><span class="muted">{tier.label}</span><strong>{p?.cli || 'Local / API'} <small>{p?.model || 'No model'}</small></strong><span class="muted">{count ? `${count} active assignment${count > 1 ? 's' : ''}` : snapshot.status === 'planning' && tier.id !== 'primary' ? 'Waiting for the plan' : 'No active assignment'} · {p?.timeoutSeconds ? `${p.timeoutSeconds}s per call` : 'No call timeout'}</span></div>
    {/each}
  </div>
  <div class="stats"><span>{executions.filter(e => e.status === 'running').length} working</span><span>{executions.filter(e => e.status === 'waiting').length} awaiting delegation</span><span>{tasks.filter(t => ['pending','blocked'].includes(t.status)).length} queued / blocked</span><span>{usage.calls ? `${(usage.input + usage.output).toLocaleString()} reported tokens · ${usage.calls} calls` : 'Token usage not reported yet'}</span></div>
  {#if tasks.length}<div class="task-board" aria-label="Task assignments">
    {#each tasks as task (task.id)}
      {@const route = workerTaskRoute(task, snapshot)}
      <button class:task-failed={['failed','blocked','cancelled'].includes(task.status)} on:click={() => { const execution = executions.find(e => e.taskID === task.id); if (execution) { selected = execution.id; focusActive = false } else showGraph = true }}>
        <span><strong>{task.id}</strong><small>{task.status}{task.review ? ` · ${task.review}` : ''}</small></span>
        <p>{task.description}</p><small>{workerLabel(task.worker?.profile)} · {route.cli || 'Local / API'} / {route.model}</small>
        <small>Depends on: {task.dependencies?.join(', ') || 'none'}</small>
        {#if task.error}<span class="task-error">{task.error}</span>{/if}
      </button>
    {/each}
  </div>{/if}
  <div class="execution-layout">
    <nav class="assignments" aria-label="Worker assignments">
      <h3>Assignments <small>{executions.length}</small></h3>
      {#each executions as execution (execution.id)}
        <button class:selected={selected === execution.id} on:click={() => { selected = execution.id; follow = true; focusActive = false }}>
          <span><strong>{workerLabel(execution.tier)}</strong><small>{execution.status}</small></span>
          <small>{execution.cli || 'Local / API'} / {execution.model}</small>
          <p>{execution.taskID || execution.assignment || 'Worker invocation'}</p>
          {#if execution.parentID}<small>From {workerLabel(executions.find(e => e.id === execution.parentID)?.tier)}</small>{/if}
        </button>
      {/each}
      {#if !executions.length}<p class="muted">Preparing the first worker. Its activity will appear here.</p>{/if}
    </nav>
    <div class="console">
      {#if current}
        <header class="console-head"><div><strong>{workerLabel(current.tier)} · {current.cli || 'Local / API'}</strong><p>{current.model}{current.reasoningEffort ? ` · effort ${current.reasoningEffort}` : ''} · {current.status} · {current.phase || 'activity'}{current.step ? ` · call ${current.step}` : ''}</p></div><span>{elapsed(current.startedAt, ['running','waiting'].includes(current.status) ? now : new Date(current.updatedAt).getTime())}</span></header>
        <details class="assignment"><summary>Assignment, workspace and handoff</summary><p>{current.assignment}</p><code>{current.workspace}</code>{#if current.parentID}<p>Delegated by {workerLabel(executions.find(e => e.id === current.parentID)?.tier)}. Results return to that worker.</p>{/if}</details>
        <div class="filters" role="group" aria-label="Activity filter">
          {#each [['all','Activity'],['io','Input / output'],['reasoning','Reported reasoning'],['delegation','Handoffs'],['errors','Errors']] as [value,label]}<button class:selected={filter === value} on:click={() => filter = value}>{label}</button>{/each}
          <label><input type="checkbox" bind:checked={follow} on:change={() => { if (follow) scrollLatest() }} /> Follow live</label>
        </div>
        {#if ['running','waiting'].includes(current.status)}<div class="waiting"><span class="dot live"></span>{current.status === 'waiting' ? 'Waiting for a delegated worker.' : current.phase === 'tool' ? 'Host tool is running or waiting for your approval.' : `Call active for ${elapsed(current.callStartedAt || current.startedAt)}. ${current.lastProgressAt ? `Last reported activity ${elapsed(current.lastProgressAt)} ago.` : 'The backend has not reported progress yet.'}`} {#if current.timeoutSeconds && current.status !== 'waiting' && current.phase !== 'tool'}Configured call timeout: {current.timeoutSeconds}s.{/if}</div>{/if}
        <div class="timeline" bind:this={timeline} role="region" aria-label="Worker activity" on:scroll={() => { if (timeline.scrollHeight - timeline.scrollTop - timeline.clientHeight > 70) follow = false }}>
          {#if events.length > visibleCount}<button on:click={() => { follow = false; visibleCount += 120 }}>Show earlier activity ({events.length - visibleCount})</button>{/if}
          {#each events.slice(-visibleCount) as event}
            <article class:error-event={['error','failed','delegated_error'].includes(event.kind)} class:handoff={event.kind === 'delegation'}>
              <header><strong>{workerEventLabel(event)}</strong><time>{new Date(event.timestamp).toLocaleTimeString()}</time></header><pre>{event.text}</pre>
            </article>
          {/each}
          {#if !events.length}<p class="muted">{filter === 'reasoning' ? 'No reasoning was reported by this backend. This view shows only reasoning the CLI or provider exposes.' : 'No events for this filter.'}</p>{/if}

        </div>
      {:else}<div class="blank"><h3>Execution activity</h3><p>Worker calls, tools, delegations and results appear as the backend reports them.</p></div>{/if}
    </div>
  </div>
  {#if snapshot.dag && tasks.length}<button class="graph-toggle" aria-expanded={showGraph} on:click={() => showGraph = !showGraph}>{showGraph ? 'Hide task plan and review controls' : snapshot.status === 'draft' ? 'Review plan and start tasks' : 'Task plan, Git diffs and review controls'}</button>{#if showGraph}<WorkerGraph {snapshot} {clis} {models} on:models on:refresh />{/if}{/if}
  {#if snapshot.result}<details class="result"><summary>Run result</summary><pre>{snapshot.result}</pre></details>{/if}
  {#if !snapshot.dag}<div class="composer" data-voice-composer><VoiceButton disabled={active || busy} context={{page:'workers',worker_id:snapshot.id}} on:transcript={event => { draft = [draft,event.detail.text].filter(Boolean).join(' '); if (event.detail.autoSend) continueRun() }} /><textarea rows="2" bind:value={draft} disabled={active || busy} aria-label="Follow-up worker task" placeholder="Continue with a new instruction…"></textarea><button class="primary" disabled={active || busy || !draft.trim()} on:click={continueRun}>Continue</button></div>{/if}
</section>
{#if settings}
  <div class="backdrop"><div class="settings" role="dialog" aria-modal="true" aria-label="Worker run settings" use:focusDialog={{onClose:() => { if (!busy) settings = null }}}>
    <header><h2>Run settings</h2><button disabled={busy} aria-label="Close settings" on:click={() => settings = null}>×</button></header>
    <p>Changes apply to future calls in this saved run. Stop execution first. Existing task overrides and recorded results keep their original routes.</p>
    <code>{settings.workspace}</code><WorkerProfiles bind:config={settings} {clis} {models} on:models />
    {#if error}<p class="failure" role="alert">{error}</p>{/if}<footer><button disabled={busy} on:click={() => settings = null}>Cancel</button><button class="primary" disabled={busy || active} on:click={saveSettings}>{busy ? 'Saving…' : 'Save run profiles'}</button></footer>
  </div></div>
{/if}
<style>
  .monitor{display:grid;gap:16px;min-width:0}.toolbar,.buttons,.run-state,.console-head,.filters,.stats{display:flex;align-items:center;gap:12px;flex-wrap:wrap}.toolbar,.console-head{justify-content:space-between}.muted,small,time{color:var(--text-dim)}small,time{font-size:11px}.dot{width:8px;height:8px;border-radius:50%;background:var(--text-dim);display:inline-block;flex-shrink:0}.dot.live{background:var(--ok);box-shadow:0 0 0 4px color-mix(in srgb,var(--ok) 14%,transparent);animation:pulse 2s ease-in-out infinite}.dot.failed{background:var(--err)}button{font:inherit;font-size:12px;cursor:pointer;border:1px solid var(--border-bright);border-radius:var(--radius-sm);background:var(--bg-panel);color:var(--text);padding:8px 12px;transition:background 150ms var(--ease),border-color 150ms var(--ease)}button:hover:not(:disabled){background:var(--bg-raised);border-color:var(--accent)}button:disabled{opacity:.5;cursor:default}.primary,.selected{border-color:var(--accent);background:color-mix(in srgb,var(--accent) 9%,var(--bg-panel))}.primary{background:var(--accent);color:var(--accent-fg)}.stop{color:var(--err)}.profiles{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px}.profiles>div{display:grid;gap:7px;border:1px solid var(--border);background:var(--bg-panel);border-radius:var(--radius);padding:14px;min-width:0}.profiles strong{overflow-wrap:anywhere}.profiles strong small{display:block;margin-top:4px}.stats{font-size:12px;justify-content:space-between;color:var(--text-dim);padding:0 3px}.failure{border:1px solid color-mix(in srgb,var(--err) 45%,var(--border));border-radius:var(--radius);background:color-mix(in srgb,var(--err) 6%,var(--bg-panel));padding:16px;overflow-wrap:anywhere}.failure p{margin:8px 0;white-space:pre-wrap}.failure .buttons{margin-top:12px}.execution-layout{display:grid;grid-template-columns:240px minmax(0,1fr);gap:14px}.assignments{display:flex;flex-direction:column;gap:8px;max-height:650px;overflow:auto}.assignments h3{margin:0 0 4px;font-size:14px}.assignments button{text-align:left;display:grid;gap:7px;padding:12px}.assignments button>span{display:flex;justify-content:space-between;gap:5px}.assignments p{margin:0;font-size:12px;max-height:3em;overflow:hidden;overflow-wrap:anywhere}.console{min-width:0;border:1px solid var(--border);border-radius:var(--radius);background:var(--bg-panel);overflow:hidden}.console-head{padding:16px;border-bottom:1px solid var(--border);font-size:13px}.console-head p{font-size:12px;color:var(--text-dim);margin:5px 0 0}.assignment{padding:10px 16px;font-size:12px;border-bottom:1px solid var(--border)}summary{cursor:pointer;color:var(--text-dim)}.assignment p,code{overflow-wrap:anywhere;white-space:pre-wrap;line-height:1.5}.filters{padding:10px 14px;border-bottom:1px solid var(--border);gap:6px}.filters button{padding:5px 8px;font-size:11px}.filters label{font-size:11px;margin-left:auto;color:var(--text-dim);display:flex;align-items:center;gap:4px}.timeline{scrollbar-color:var(--border-bright) var(--bg-panel);height:420px;overflow:auto;padding:14px;scrollbar-gutter:stable}.timeline article{margin-bottom:12px;padding:12px;border:1px solid var(--border);border-radius:9px;background:var(--bg)}.timeline article header{display:flex;justify-content:space-between;gap:8px;font-size:11px}.timeline pre,.result pre{white-space:pre-wrap;overflow-wrap:anywhere;font:12px/1.6 var(--font-mono,monospace);margin:10px 0 0}.error-event{border-color:var(--err)!important}.handoff{border-color:var(--accent)!important}.waiting{padding:12px 16px;color:var(--text-dim);font-size:11px;background:var(--bg-raised);line-height:1.6}.waiting .dot{margin-right:8px}.blank{padding:50px 24px;text-align:center;color:var(--text-dim)}.task-board{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:10px}.task-board button{display:grid;gap:8px;text-align:left;min-width:0}.task-board button>span:first-child{display:flex;justify-content:space-between;gap:8px}.task-board p{margin:0;font-size:12px;line-height:1.5;max-height:4.5em;overflow:hidden}.task-board small{overflow-wrap:anywhere}.task-failed{border-color:var(--err)}.task-error{color:var(--err);font-size:11px;overflow-wrap:anywhere}.graph-toggle{justify-self:start}.result{padding:14px;border:1px solid var(--border);border-radius:var(--radius)}.composer{display:flex;gap:10px;align-items:flex-end}.composer textarea{flex:1}textarea{font:inherit;min-width:0;background:var(--bg);color:var(--text);border:1px solid var(--border-bright);border-radius:var(--radius-sm);padding:12px;resize:vertical}.backdrop{position:fixed;inset:0;z-index:200;background:var(--overlay);backdrop-filter:blur(8px);display:grid;place-items:center;padding:24px}.settings{width:min(1050px,100%);max-height:90vh;overflow:auto;display:grid;gap:16px;padding:24px;border:1px solid var(--border-bright);border-radius:var(--radius);background:var(--bg);box-shadow:var(--shadow-overlay)}.settings header,.settings footer{display:flex;gap:10px;align-items:center;justify-content:space-between}.settings h2,.settings p{margin:0}.settings p{font-size:13px;color:var(--text-dim);line-height:1.6}.settings footer{justify-content:flex-end}@keyframes pulse{50%{box-shadow:0 0 0 6px color-mix(in srgb,var(--ok) 4%,transparent)}}@media(prefers-reduced-motion:reduce){.dot.live{animation:none}button{transition:none}}@media(max-width:800px){.profiles{grid-template-columns:1fr}.execution-layout{grid-template-columns:1fr}.assignments{max-height:220px;display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr))}.assignments h3{grid-column:1/-1}.timeline{height:360px}}
</style>
