<script>
  import ServerStartup from './ServerStartup.svelte'
  import ConfirmModal from './ConfirmModal.svelte'
  import logo from '../assets/monke-icon.png'
  export let message=''
  export let native
  let error=''
  async function foreground(){try{await native.UseForegroundServer();location.reload()}catch(e){error=String(e)}}
</script>
<main class="recovery">
  <header><img src={logo} width="48" height="48" alt="PrAImate" /><div><h1>Background Server needs attention</h1><p>{message}</p></div></header>
  <p>The database stays with its current owner. Use the service controls and logs below, then retry the connection. To configure this profile in foreground mode, stop its service first.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div class="actions"><button class="btn primary" on:click={()=>location.reload()}>Retry connection</button><button class="btn" on:click={foreground}>Open foreground mode</button></div>
  <ServerStartup />
</main>
<ConfirmModal />
<style>
  .recovery{box-sizing:border-box;width:100%;max-width:1040px;margin:0 auto;padding:36px 24px}header{display:flex;align-items:center;gap:18px}h1{font-size:24px;margin:0 0 8px;letter-spacing:-.025em}p{font-size:13px;line-height:1.6;color:var(--text-dim);overflow-wrap:anywhere}.actions{display:flex;gap:10px;flex-wrap:wrap;margin:20px 0}.error{color:var(--err)}@media(max-width:620px){.recovery{padding:24px 12px}header{align-items:flex-start}h1{font-size:20px}}
</style>
