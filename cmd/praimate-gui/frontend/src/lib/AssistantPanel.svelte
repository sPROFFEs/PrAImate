<script>
  import { onMount, onDestroy, createEventDispatcher, tick } from 'svelte'
  import { api } from './api.js'
  import { activePage, openChatId, agentStudio, openWorkerId } from './stores.js'
  import { assistantConfig, assistantTranscript } from './voiceInput.js'
  import VoiceButton from './VoiceButton.svelte'
  const dispatch = createEventDispatcher()
  let state = { messages: [], activity: [] }, draft = '', busy = false, error = '', phase = '', field, log
  let unsubscribe = () => {}, transcriptUnsub = () => {}, destroyed = false, delegated = [], poll = null
  async function snapshot() { const result = await api.assistantSnapshot(); if (destroyed) return; state = result; await tick(); if (log) log.scrollTop = log.scrollHeight; await refreshDelegated() }
  async function refreshDelegated() { const ids = [...new Set((state.task?.steps || []).map(step => step.run_id).filter(id => id?.startsWith('worker-')))]; delegated = await Promise.all(ids.slice(-4).map(async id => { try { const run = await api.workerRunSnapshot(id); return {id,title:run.title,status:run.status,result:run.result,error:run.error} } catch(e) { return {id,status:'unavailable'} } })); }
  onMount(async () => {
    poll = setInterval(() => { if (delegated.some(run => run.status === 'running')) refreshDelegated().catch(() => {}) },2000)
    if (window.runtime?.EventsOn) unsubscribe = window.runtime.EventsOn('assistant:event', event => { phase = event.phase; if (event.task) state = { ...state, task: event.task }; if (['completed','failed','cancelled'].includes(phase)) { busy = false; snapshot().catch(e => error = String(e)) } })
    try { await snapshot(); busy = state.task?.status === 'running'; field?.focus() } catch(e) { error = String(e) }
    if (destroyed) return
    transcriptUnsub = assistantTranscript.subscribe(value => { if (!value) return; draft = [draft,value.text].filter(Boolean).join(' '); assistantTranscript.set(null); if (value.autoSend && $assistantConfig?.enabled) send() })
  })
  onDestroy(() => { destroyed = true; unsubscribe(); transcriptUnsub(); clearInterval(poll) })
  async function send() {
    if (busy || !draft.trim() || !$assistantConfig?.enabled) return
    const message = draft.trim(); draft = ''; busy = true; error = ''; phase = 'loading'
    state = { ...state, messages: [...state.messages, { role:'user', text:message }] }
    try { const result = await api.sendAssistant(message, { page:$activePage, chat_id:$openChatId || '',agent_id:$agentStudio?.id || '' }); if (!destroyed) state = result }
    catch(e) { if (!destroyed) { error = String(e); await snapshot().catch(() => {}) } }
    finally { if (!destroyed) { busy = false; await tick(); if (log) log.scrollTop = log.scrollHeight } }
  }
  function transcript(event) { draft = [draft,event.detail.text].filter(Boolean).join(' '); if (event.detail.autoSend) send() }
</script>
<aside class="assistant-panel" aria-label="PrAImate assistant">
  <header><div><strong>PrAImate Assistant</strong><small>{$assistantConfig?.model_id === 'existing' ? $assistantConfig.model : $assistantConfig?.model_id || 'Local application assistant'}</small></div><button class="btn sm" title="Assistant settings" on:click={() => activePage.set('settings')}>Settings</button><button class="btn sm" aria-label="Close assistant" on:click={() => dispatch('close')}>×</button></header>
  {#if !$assistantConfig?.enabled}
    <div class="empty"><h3>Your application assistant</h3><p>Choose a local model and enable Assistant in Settings. Voice input can be enabled separately.</p><button class="btn primary" on:click={() => activePage.set('settings')}>Configure Assistant & Voice</button></div>
  {:else}
    <div class="messages" bind:this={log} aria-live="polite">
      {#each state.messages || [] as message}<article class:user={message.role === 'user'}><small>{message.role === 'user' ? 'You' : 'Assistant'}</small><p>{message.text}</p></article>{:else}<div class="empty"><h3>What would you like to do?</h3><p>Find a conversation, inspect your workers, manage MCP servers or delegate a task.</p></div>{/each}
    </div>
    {#if state.task}<details><summary>Task · {state.task.status}{busy ? ' · '+phase : ''}</summary>{#each state.task.steps || [] as step}<p class="step">{step.action} · {step.status}{step.run_id ? ' · '+step.run_id : ''}</p>{#if step.detail}<p class="step">{step.detail}</p>{/if}{/each}</details>{/if}
    {#each delegated as run}<div class="delegated"><strong>Worker · {run.status}</strong><p>{run.title}</p>{#if run.result}<p>{run.result.slice(0,400)}</p>{/if}<button class="btn sm" on:click={() => { openWorkerId.set(run.id); activePage.set('workers') }}>Open execution</button></div>{/each}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
  {/if}
    <form data-voice-composer on:submit|preventDefault={send}>
      <textarea bind:this={field} bind:value={draft} rows="2" aria-label="Assistant message" placeholder="Ask PrAImate…" disabled={busy} on:keydown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() } }}></textarea>
      <div class="controls"><VoiceButton disabled={busy} context={{page:$activePage,chat_id:$openChatId || '',agent_id:$agentStudio?.id || ''}} on:transcript={transcript}/>{#if busy}<button type="button" class="btn danger" on:click={() => api.cancelAssistant()}>Stop</button>{:else}<button type="submit" class="btn primary" disabled={!draft.trim() || !$assistantConfig?.enabled}>Send</button>{/if}</div>
    </form>
</aside>
<style>
  .assistant-panel { position:fixed; z-index:800; right:22px; bottom:22px; width:min(440px,calc(100vw - 32px)); height:min(620px,calc(100vh - 44px)); display:flex; flex-direction:column; background:var(--bg-panel, #202329); color:var(--text); border:1px solid var(--border-bright); border-radius:18px; box-shadow:var(--shadow-overlay); isolation:isolate; overflow:hidden; animation:appear .18s ease-out; }
  header { display:flex; align-items:center; gap:8px; padding:16px; border-bottom:1px solid var(--border); }
  header div { flex:1; } header small { display:block; color:var(--text-dim); margin-top:3px; }
  .messages { flex:1; overflow:auto; padding:16px; }
  article { margin-bottom:14px; padding:12px; border:1px solid var(--border); border-radius:12px; }
  article.user { background:var(--bg-input); margin-left:24px; } article small { color:var(--text-dim); } article p { white-space:pre-wrap; overflow-wrap:anywhere; margin:6px 0 0; line-height:1.55; font-size:13px; }
  .empty { padding:24px; color:var(--text-dim); line-height:1.6; }
  form { padding:14px; border-top:1px solid var(--border); } textarea { box-sizing:border-box; width:100%; background:var(--bg-input); color:var(--text); border:1px solid var(--border); border-radius:10px; padding:10px; resize:vertical; font:inherit; }
  .controls { display:flex; justify-content:space-between; margin-top:8px; } details { padding:8px 16px; font-size:12px; max-height:140px; overflow:auto; } .step { overflow-wrap:anywhere; }
  .delegated { padding:10px 16px; font-size:12px; background:var(--bg-input); border-top:1px solid var(--border); max-height:120px; overflow:auto; }
  .delegated p { overflow-wrap:anywhere; white-space:pre-wrap; }
  .error { color:var(--danger, #e34f68); padding:0 16px; font-size:12px; overflow-wrap:anywhere; }
  @keyframes appear { from { opacity:0; transform:translateY(8px); } to { opacity:1; transform:none; } }
  @media(prefers-reduced-motion:reduce) { .assistant-panel { animation:none; } }
</style>
