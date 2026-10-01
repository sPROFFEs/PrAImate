<script>
  import { createEventDispatcher, onDestroy } from 'svelte'
  import { api } from './api.js'
  import { assistantConfig, beginCapture, cancelCapture, matchesShortcut } from './voiceInput.js'
  export let disabled = false
  export let context = {}
  const dispatch = createEventDispatcher()
  let element, capture, pressed = false, pending = false, error = '', destroyed = false, keyHeld = false, keyCode = '', generation = 0
  async function start() {
    if (disabled || pending || pressed || !$assistantConfig?.voice?.enabled) return
    pressed = true; generation++; error = ''
    const current = generation
    try {
      const next = await beginCapture()
      if (current !== generation || !pressed || destroyed) { next.cancel(); return }
      capture = next
      capture.onLimit = finish
      if (!pressed || destroyed) capture.cancel()
    } catch (e) { if (!destroyed && current === generation) { error = String(e); pressed = false } }
  }
  async function finish() {
    if (!pressed) return
    pressed = false
    if (!capture) { cancelCapture(); return }
    pending = true
    const current = generation
    try {
      const recording = capture; capture = null
      const encoded = await recording.finish()
      if (destroyed || current !== generation) return
      const result = await api.transcribeVoice(encoded, context)
      if (!destroyed && current === generation && result.text) dispatch('transcript', { text: result.text, autoSend: !!$assistantConfig?.voice?.auto_send })
    } catch (e) { if (!destroyed) error = String(e) }
    finally { pending = false }
  }
  function cancel() { generation++; pressed = false; capture?.cancel(); capture = null; cancelCapture(); if (pending) api.cancelVoice().catch(() => {}) }
  $: if (!$assistantConfig?.voice?.enabled && (pressed || pending)) cancel()
  function keydown(e) {
    const composer = element?.closest('[data-voice-composer]')
    if (!e.repeat && matchesShortcut(e, $assistantConfig?.voice?.shortcut) && composer?.contains(document.activeElement)) {
      e.preventDefault(); keyHeld = true; keyCode = e.code || e.key; start()
    }
  }
  function keyup(e) {
    if (keyHeld && (e.code || e.key) === keyCode) { keyHeld = false; finish() }
  }
  onDestroy(() => { destroyed = true; if (pressed || pending) cancel() })
</script>

<svelte:window on:keydown={keydown} on:keyup={keyup} on:blur={() => { if (pressed) cancel() }} />
{#if $assistantConfig?.voice?.enabled}
  <span class="voice-input" bind:this={element}>
    <button type="button" class="btn composer-action" class:recording={pressed} disabled={disabled || pending}
      aria-label={pressed ? 'Recording. Release to transcribe' : 'Hold to dictate'} title={'Hold to dictate · ' + $assistantConfig.voice.shortcut}
      on:pointerdown={e => { if (e.button !== 0) return; e.currentTarget.setPointerCapture(e.pointerId); start() }}
      on:pointerup={finish} on:pointercancel={cancel}
      on:keydown={e => { if ((e.key === ' ' || e.key === 'Enter') && !e.repeat && !e.ctrlKey && !e.metaKey) { e.preventDefault(); start() } }}
      on:keyup={e => { if (e.key === ' ' || e.key === 'Enter') finish() }}>
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><rect x="9" y="2" width="6" height="12" rx="3"/><path d="M5 10v2a7 7 0 0 0 14 0v-2M12 19v3M8 22h8"/></svg>
      <span>{pending ? 'Transcribing…' : pressed ? 'Recording…' : 'Dictate'}</span>
    </button>
    {#if pending}<button class="btn sm" type="button" on:click={cancel}>Cancel</button>{/if}
    {#if error}<span class="voice-error" role="alert">{error}</span>{/if}
  </span>
{/if}
<style>
  .voice-input { display:flex; align-items:center; gap:4px; flex-wrap:wrap; }
  .recording { color:var(--danger, #e34f68); border-color:currentColor; }
  .voice-error { color:var(--danger, #e34f68); max-width:260px; font-size:12px; }
</style>
