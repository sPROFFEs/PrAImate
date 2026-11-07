<script>
  import { onMount, onDestroy } from 'svelte'
  import { focusDialog } from './focusDialog.js'
  export let client
  export let initial = '/'
  export let done
  let path=initial, folders=[],busy=false,error='',sequence=0
  async function load(next=path){
    const current=++sequence;busy=true;error=''
    try{
      const valid=await client.call('projects.validate',[next])
      const entries=await client.call('files.list',[{workspace:valid,path:'.'}])
      if(current!==sequence)return
      path=valid;folders=entries.filter(entry=>entry.isDir&&!entry.symlink)
    }catch(e){if(current===sequence)error=e.message}
    finally{if(current===sequence)busy=false}
  }
  function child(name){const separator=path.includes('\\')?'\\':'/';return path.replace(/[\\/]+$/,'')+separator+name}
  function parent(){const value=path.replace(/[\\/]+$/,'');const slash=Math.max(value.lastIndexOf('/'),value.lastIndexOf('\\'));if(slash<0)return path;return value.slice(0,slash+1)||'/'}
  async function choose(){busy=true;error='';try{done(await client.call('projects.validate',[path]))}catch(e){error=e.message}finally{busy=false}}
  onMount(()=>load())
  onDestroy(()=>sequence++)
</script>
<div class="folder-backdrop" role="presentation">
  <section role="dialog" aria-modal="true" aria-labelledby="server-folder-title" use:focusDialog={{onClose:()=>done('')}}>
    <h2 id="server-folder-title">Server folder</h2><p>Choose a folder on your PrAImate server.</p>
    <form on:submit|preventDefault={()=>load()}><input aria-label="Server folder path" bind:value={path} /><button class="btn" disabled={busy}>Go</button></form>
    <div class="folder-actions"><button class="btn" disabled={busy} on:click={()=>load(parent())}>Up</button><button class="btn" disabled={busy} on:click={()=>load()}>Refresh</button></div>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <div class="folders" aria-busy={busy}>{#if busy}<p>Loading folders…</p>{:else}{#each folders as folder}<button class="btn folder" on:click={()=>load(child(folder.name))}>▸ {folder.name}</button>{/each}{#if !folders.length&&!error}<p>No subfolders. You can select this folder or enter another path.</p>{/if}{/if}</div>
    <footer><button class="btn" on:click={()=>done('')}>Cancel</button><button class="btn primary" disabled={busy} on:click={choose}>Use folder</button></footer>
  </section>
</div>
<style>
  .folder-backdrop{position:fixed;inset:0;z-index:1300;background:#0007;display:grid;place-items:center;padding:16px}section{width:min(580px,100%);max-height:calc(var(--app-height,100dvh) - 32px);display:flex;flex-direction:column;gap:12px;padding:20px;background:var(--bg-panel);color:var(--text);border:1px solid var(--border);border-radius:16px;box-shadow:var(--shadow-overlay)}h2,p{margin:0}p{color:var(--text-dim);font-size:12px}form,.folder-actions,footer{display:flex;gap:8px}input{min-width:0;flex:1;font:inherit;font-family:var(--mono);background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:8px;padding:8px}.folders{overflow:auto;min-height:120px;max-height:45vh;display:flex;flex-direction:column;gap:5px}.folder{justify-content:flex-start;overflow-wrap:anywhere}.error{color:var(--err)}footer{justify-content:flex-end}
</style>
