<script>
  import { onMount } from 'svelte'
  import logo from '../assets/monke-icon.png'
  import { assertAccessResponse } from './remoteClient.js'
  export let connect
  const mobile = window.praimateMobile
  const credentials = mobile?.credentials || {get:async()=>sessionStorage.getItem('praimate.remote.token')||'',set:async value=>sessionStorage.setItem('praimate.remote.token',value),clear:async()=>sessionStorage.removeItem('praimate.remote.token')}
  let code = '', name = mobile ? 'Android device' : 'Browser', token = '', admin = false, busy = false, error = ''
  async function login() {
    busy = true; error = ''
    try {
      let credential = token.trim()
      if (!admin) {
        const response = await fetch('/v1/pair', {method:'POST',redirect:'manual',headers:{'Content-Type':'application/json'},body:JSON.stringify({code:code.trim(),name:name.trim()})})
        await assertAccessResponse(response)
        const result = await response.json()
        if (!response.ok) throw new Error(result.error?.message || 'Pairing failed')
        credential = result.token
      }
      await credentials.set(credential)
      await connect(mobile ? 'android-keystore-session' : credential)
    } catch (e) { error = String(e.message || e) }
    finally { busy = false }
  }
  onMount(async () => { busy=true;try { const saved=await credentials.get();if(saved)await connect(saved) } catch(e) { error=e.message || 'Server unavailable. Reconnect or pair again.' } finally { busy=false } })
</script>

<main class="remote-login">
  <form on:submit|preventDefault={login}>
    <img src={logo} alt="PrAImate" width="72" height="72" />
    <h1>PrAImate Server</h1>
    <p>Connect to your projects, conversations and workers.</p>
    {#if mobile}<p class="hint">{mobile.serverName} · {mobile.serverURL}</p><button class="switch" type="button" on:click={()=>mobile.configure()}>Choose another server</button><button class="switch" type="button" on:click={()=>mobile.signInAccess().catch(e=>error=e.message)}>Cloudflare Access sign-in</button>{/if}
    {#if admin}
      <label>Administrator token<input type="password" bind:value={token} autocomplete="off" required /></label>
    {:else}
      <label>Device name<input bind:value={name} maxlength="128" required /></label>
      <label>Pairing code<input bind:value={code} inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{8}" placeholder="8-digit code" required /></label>
      <p class="hint">Create a code from the server console or an authorized administrator.</p>
    {/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <button class="btn primary" type="submit" disabled={busy}>{busy ? 'Connecting…' : 'Connect'}</button>
    <button class="switch" type="button" on:click={() => {admin=!admin;error=''}}>{admin ? 'Use a pairing code' : 'Use an administrator token'}</button>
  </form>
</main>

<style>
  .remote-login{display:grid;place-items:center;min-height:100vh;padding:24px;background:var(--bg,#17191d);color:var(--text,#edf0f5)}form{width:min(100%,420px);display:grid;gap:18px;padding:32px;border:1px solid var(--border,#353943);border-radius:20px;background:var(--bg-panel,#22252b);box-shadow:0 16px 64px #0003}img{justify-self:center}h1,p{margin:0;text-align:center}h1{font-size:25px}p{color:var(--text-dim,#a6afbd);line-height:1.5}label{display:grid;gap:7px;font-size:13px}input{width:100%;box-sizing:border-box;padding:12px;border-radius:10px;border:1px solid var(--border,#414651);background:var(--bg,#17191d);color:inherit;font:inherit}.hint{font-size:12px}.error{color:var(--err,#ff8b94)}.switch{border:0;background:transparent;color:var(--accent,#9bb8ff);cursor:pointer;font:inherit;font-size:12px}
</style>
