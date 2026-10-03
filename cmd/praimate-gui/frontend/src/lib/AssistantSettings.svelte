<script>
  import { onMount } from 'svelte'
  import { api } from './api.js'
  import { refreshAssistantConfig, assistantConfig } from './voiceInput.js'
  import AssistantModels from './AssistantModels.svelte'
  import VoiceButton from './VoiceButton.svelte'
  import { showConfirm } from './stores.js'
  let config, error = '', notice = '', saving = false, tab = 'general'
  const tabs = ['general', 'model', 'voice', 'permissions', 'runtime', 'activity']
  let activity = [], hosts = [], microphoneText = ''
  onMount(async () => { try { config = structuredClone(await refreshAssistantConfig()); activity = (await api.assistantSnapshot()).activity || []; hosts = await api.listLocalHosts() } catch (e) { error = String(e) } })
  async function clearHistory() {
    if (!await showConfirm({title:'Clear assistant history?',message:'This deletes the dedicated assistant conversation and action audit. Saved tasks and delegated worker histories are retained.',confirmLabel:'Clear history'})) return
    try { await api.clearAssistantHistory(); activity = []; notice = 'Assistant history cleared.' } catch(e) { error = String(e) }
  }
  function chooseHost(event) { const host = hosts.find(host => host.id === event.currentTarget.value); if (host) { config.endpoint = host.endpoint; config.model = (host.nativeModels || host.activeModels || [])[0] || ''; config = config } }
  function selectModel() { if (config.model_id === 'lfm-efficient') { config.context = 2048; config.output = 512 } else if (config.model_id === 'qwen-quality') { config.context = 4096; config.output = 768 } }
  function preset(name) {
    for (const capability of Object.keys(config.permissions)) config.permissions[capability] = ['read','navigate'].includes(capability) ? 'allow' : ['system','network','filesystem'].includes(capability) || name === 'read-only' ? 'deny' : name === 'full' ? 'allow' : 'ask'
    config = config
  }
  async function save() {
    saving = true; error = ''; notice = ''
    try { await api.saveAssistantConfig(config); assistantConfig.set(structuredClone(config)); notice = 'Assistant and voice settings saved.' }
    catch (e) { error = String(e) } finally { saving = false }
  }
  async function health() { saving = true; error = ''; try { await api.assistantHealth(); notice = 'Selected model is ready.' } catch (e) { error = String(e) } finally { saving = false } }
</script>

<section class="assistant-settings card">
  <h2>Assistant & Voice</h2>
  <p class="subtitle">An optional local application assistant and independent voice input. Installing a model enables neither feature.</p>
  <div class="tabs" role="tablist" aria-label="Assistant settings">
    {#each tabs as name}<button class="btn sm" class:active={tab === name} role="tab" aria-selected={tab === name} on:click={() => tab = name}>{name[0].toUpperCase() + name.slice(1)}</button>{/each}
  </div>
  {#if error}<p class="banner" role="alert">{error}</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if config}
    {#if tab === 'general'}
      <label><input type="checkbox" bind:checked={config.enabled}> Enable application assistant</label>
      <label>Assistant shortcut <input bind:value={config.shortcut} placeholder="Mod+Shift+Space"></label>
      <label><input type="checkbox" bind:checked={config.start_with_app}> Load the selected model when the application unlocks</label>
      <label><input type="checkbox" bind:checked={config.keep_loaded}> Keep assistant model loaded</label>
      <label>Unload after idle seconds <input type="number" min="10" max="86400" bind:value={config.idle_seconds}></label>
      <p class="subtitle">The monkey opens the assistant. Mod means Ctrl on Linux/Windows and Cmd on macOS.</p>
    {:else if tab === 'model'}
      <label>Selected model <select bind:value={config.model_id} on:change={selectModel}>
        <option value="lfm-efficient">LFM2.5 350M · Efficient</option><option value="qwen-quality">Qwen3.5 0.8B · Quality</option><option value="existing">Existing Local LLM endpoint</option>
      </select></label>
      {#if config.model_id === 'lfm-efficient'}
        <p class="card-sub">Best for brief replies and greetings. Choose Quality for more reliable application actions.</p>
      {/if}
      {#if config.model_id === 'existing'}
        <p class="subtitle">Use a configured Local LLM host URL and exact model ID. Saved host credentials stay in the backend.</p>
        {#if hosts.length}<label>Saved Local LLM profile <select on:change={chooseHost}><option value="">Choose a saved profile…</option>{#each hosts as host}<option value={host.id}>{host.name}</option>{/each}</select></label>{/if}
        <label>Endpoint <input bind:value={config.endpoint} placeholder="http://127.0.0.1:1234/v1"></label>
        <label>Model ID <input bind:value={config.model} placeholder="Exact backend model identifier"></label>
      {/if}
      <AssistantModels />
      <button class="btn" disabled={saving || !config.enabled} on:click={health}>Check saved model</button>
    {:else if tab === 'voice'}
      <label><input type="checkbox" bind:checked={config.voice.enabled}> Enable local voice input</label>
      <label>Whisper model <select bind:value={config.voice.model_id}><option value="whisper-tiny">Tiny · Fastest</option><option value="whisper-base">Base · Balanced</option><option value="whisper-small">Small · More accurate</option></select></label>
      <label>Language <input bind:value={config.voice.language} placeholder="auto, en, es…"></label>
      <label>CPU threads <input type="number" min="1" max="256" bind:value={config.voice.threads}></label>
      <label>Push-to-talk shortcut <input bind:value={config.voice.shortcut} placeholder="Mod+Shift+M"></label>
      <label><input type="checkbox" bind:checked={config.voice.keep_loaded}> Keep Whisper loaded between recordings</label>
      <label><input type="checkbox" bind:checked={config.voice.auto_send}> Automatically send transcribed text</label>
      <p class="subtitle">Hold Dictate or the shortcut while a message field is focused. By default the transcript stays editable. Audio is processed locally, never stored; recordings stop after two minutes.</p>
      <div data-voice-composer><h3>Test microphone</h3><p class="subtitle">Save and enable Voice Input first, then hold Dictate. The test transcript is never submitted.</p><VoiceButton context={{page:'settings'}} on:transcript={event => microphoneText = event.detail.text}/><textarea aria-label="Microphone test transcript" bind:value={microphoneText} rows="2" placeholder="Your test transcription appears here…"></textarea></div>
      <AssistantModels />
    {:else if tab === 'permissions'}
      <div class="tabs"><button class="btn sm" on:click={() => preset('read-only')}>Read Only</button><button class="btn sm" on:click={() => preset('standard')}>Standard</button><button class="btn sm" on:click={() => preset('full')}>Full</button></div>
      <p class="subtitle">These permissions are independent of Agents and Workers. System commands, network and files remain denied in every preset; configure them explicitly.</p>
      <div class="permission-grid">{#each Object.keys(config.permissions) as capability}<label>{capability}<select bind:value={config.permissions[capability]}><option value="allow">Allow</option><option value="ask">Ask each time</option><option value="deny">Deny</option></select></label>{/each}</div>
    {:else if tab === 'runtime'}
      <div class="permission-grid">
        {#each [['context','Context tokens',2048,131072],['output','Output tokens',64,32768],['threads','CPU threads',1,256],['gpu_layers','GPU layers',0,1024],['batch','Batch size',1,8192],['max_turns','Model turns',1,64],['max_actions','Actions',1,64],['max_failures','Failed attempts',1,10],['max_delegations','Delegated tasks',0,10],['command_seconds','Command timeout seconds',1,300]] as [key,label,min,max]}
          <label>{label}<input type="number" {min} {max} bind:value={config[key]}></label>
        {/each}
        <label>Temperature <input type="number" min="0" max="2" step="0.05" bind:value={config.temperature}></label>
        <label>Top P <input type="number" min="0.01" max="1" step="0.01" bind:value={config.top_p}></label>
        <label>Top K <input type="number" min="0" max="1000" bind:value={config.top_k}></label>
        <label>Repeat penalty <input type="number" min="0.1" max="3" step="0.05" bind:value={config.repeat_penalty}></label>
      </div>
    {:else}
      <button class="btn sm" on:click={async () => { try { activity = (await api.assistantSnapshot()).activity || [] } catch(e) { error = String(e) } }}>Refresh activity</button><button class="btn sm danger" on:click={clearHistory}>Clear assistant history</button>
      <p class="subtitle">Activity and assistant conversation are stored in the encrypted application database. This audit records action names and argument keys, never argument values.</p>
      {#each activity.slice(-30).reverse() as entry}<div class="audit"><strong>{entry.action}</strong><span>{entry.decision} · {entry.status}</span><small>{new Date(entry.at).toLocaleString()}</small></div>{:else}<p>No assistant actions yet.</p>{/each}
    {/if}
    <button class="btn primary" disabled={saving} on:click={save}>{saving ? 'Saving…' : 'Save Assistant & Voice'}</button>
  {/if}
</section>
<style>
  .assistant-settings { margin-bottom:24px; }
  h2 { font-size:18px; margin:0 0 8px; }
  .tabs { display:flex; gap:6px; flex-wrap:wrap; margin:14px 0; }
  .active { border-color:var(--accent); color:var(--accent); }
  label { display:block; margin:12px 0; font-size:13px; }
  input:not([type=checkbox]),select,textarea { display:block; width:100%; max-width:420px; margin-top:5px; border:1px solid var(--border); border-radius:8px; padding:8px; color:var(--text); background:var(--bg-input); }
  input[type=checkbox] { margin-right:6px; }
  .permission-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(180px,1fr)); gap:0 18px; }
  .audit { display:flex; gap:12px; flex-wrap:wrap; padding:8px 0; border-bottom:1px solid var(--border); }
  .audit small { color:var(--text-dim); margin-left:auto; }
</style>
