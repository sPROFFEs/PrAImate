<script>
  import { onMount, onDestroy } from 'svelte'
  import { term } from './terminal.js'
  import { terminalRenderers } from './terminalRenderers.js'
  import { createTerminalComposer } from './mobileTerminalInput.js'
  export let terminalID = ''
  const viewer = window.praimateRemote?.status?.identity?.role === 'viewer'
  let state={terminalID:'',draft:'',busy:false,error:'',code:''}, takingControl=false
  const composer=createTerminalComposer({write:term.write,bracketedPaste:terminalRenderers.bracketedPaste,changed:next=>state=next})
  $: composer.select(terminalID)
  async function takeControl() {
    const id=terminalID
    takingControl=true
    try { await window.praimateRemote.client.call('terminals.claim',[id,true]);if(id===terminalID)composer.clearError() }
    catch(error){composer.warn(id,error.message,error.code)}
    finally{takingControl=false}
  }
  onMount(()=>window.runtime?.EventsOn?.('praimate:remote-warning', warning => {
    if(warning.terminal)composer.warn(warning.terminal,warning.message,warning.code)
  }))
  onDestroy(()=>composer.dispose())
</script>

<div class="terminal-composer">
  {#if state.error}
    <div class="input-error" role="alert"><span>{state.error}</span>{#if state.code==='terminal_busy'&&!viewer}<button class="btn sm" disabled={takingControl} on:click={takeControl}>{takingControl?'Taking control…':'Take control'}</button>{/if}</div>
  {/if}
  <div class="input-row">
    <textarea rows="2" aria-label="Text to send to terminal" placeholder={!terminalID?'Select a terminal':viewer?'View-only terminal':'Type text or a command…'} value={state.draft} on:input={event=>composer.edit(event.currentTarget.value)} autocapitalize="off" autocorrect="off" autocomplete="off" spellcheck="false" disabled={!terminalID||viewer||state.busy}></textarea>
    <button class="btn primary" aria-label="Send terminal input" disabled={!terminalID||viewer||state.busy||!state.draft} on:click={()=>composer.send()}>{state.busy?'Sending…':'Send ↵'}</button>
  </div>
</div>

<style>
  .terminal-composer{padding:7px 8px 0;background:var(--bg-panel)}.input-row{display:flex;align-items:stretch;gap:8px}.input-row textarea{box-sizing:border-box;flex:1;min-width:0;width:100%;resize:vertical;max-height:140px;border:1px solid var(--border);border-radius:9px;background:var(--bg);color:var(--text);padding:9px 10px;font:16px var(--mono);line-height:1.35}.input-row button{flex-shrink:0;align-self:stretch;min-width:64px;font-size:12px}.input-error{display:flex;align-items:center;flex-wrap:wrap;gap:8px;padding:8px 0;color:var(--err);font-size:12px;overflow-wrap:anywhere}.input-error span{flex:1;min-width:0}
</style>
