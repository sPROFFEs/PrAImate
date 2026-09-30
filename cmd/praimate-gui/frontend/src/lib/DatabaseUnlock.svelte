<script>
  import { createEventDispatcher } from 'svelte'
  import { api } from './api.js'
  import logo from '../assets/monke-icon.png'

  export let info

  const dispatch = createEventDispatcher()
  let password = ''
  let confirmation = ''
  let remember = false
  let busy = false
  let error = ''
  let warning = ''
  let unlockedWithWarning = false
  let showPassword = false

  $: setup = !!info?.setupRequired
  $: canSubmit =
    !busy &&
    password.length >= 12 &&
    (!setup || password === confirmation)

  async function submit() {
    if (!canSubmit) return
    busy = true
    error = ''
    warning = ''
    try {
      const result = setup
        ? await api.initializeDatabasePassword(password, confirmation, remember)
        : await api.unlockDatabase(password, remember)
      warning = result?.warning || ''
      password = ''
      confirmation = ''
      if (warning) {
        unlockedWithWarning = true
      } else {
        dispatch('unlocked', result)
      }
    } catch (e) {
      error = String(e).replace(/^Error:\s*/, '')
    } finally {
      busy = false
    }
  }
</script>

<div class="lock-screen">
  <section class="lock-card" aria-labelledby="database-lock-title">
    <div class="brand">
      <img src={logo} alt="" />
      <div>
        <span>PrAImate</span>
        <small>Encrypted local workspace</small>
      </div>
    </div>

    <div class="heading">
      <div class="lock-icon" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8">
          <rect x="4" y="10" width="16" height="11" rx="2" />
          <path d="M8 10V7a4 4 0 0 1 8 0v3M12 14v3" />
        </svg>
      </div>
      <div>
        <h1 id="database-lock-title">
          {setup ? 'Protect your PrAImate data' : 'Unlock PrAImate'}
        </h1>
        <p>
          {setup
            ? 'Create the password that encrypts your local database and portable Git backups.'
            : 'Enter your database password to decrypt your local PrAImate data.'}
        </p>
      </div>
    </div>

    {#if info?.error}
      <div class="message error">{info.error}</div>
    {/if}

    <form on:submit|preventDefault={submit}>
      <label for="database-password">Database password</label>
      <div class="password-row">
        <input
          id="database-password"
          type={showPassword ? 'text' : 'password'}
          value={password}
          on:input={(event) => (password = event.currentTarget.value)}
          minlength="12"
          autocomplete={setup ? 'new-password' : 'current-password'}
          placeholder="At least 12 characters" />
        <button class="show" type="button" on:click={() => (showPassword = !showPassword)}>
          {showPassword ? 'Hide' : 'Show'}
        </button>
      </div>

      {#if setup}
        <label for="database-password-confirmation">Confirm password</label>
        <input
          id="database-password-confirmation"
          type={showPassword ? 'text' : 'password'}
          value={confirmation}
          on:input={(event) => (confirmation = event.currentTarget.value)}
          minlength="12"
          autocomplete="new-password"
          placeholder="Repeat the database password" />
        <p class="hint">
          Store this password safely. Without it—or an unlocked remembered
          installation—the database and its backups cannot be recovered.
        </p>
      {/if}

      {#if info?.rememberSupported}
        <label class="remember">
          <input type="checkbox" bind:checked={remember} />
          <span>
            <strong>Remember on this device</strong>
            <small>
              Uses Windows Credential Manager, macOS Keychain or the Linux desktop Secret Service.
              Leave off to require the password every time PrAImate starts.
            </small>
          </span>
        </label>
      {/if}

      {#if error}<div class="message error">{error}</div>{/if}
      {#if warning}
        <div class="message warning" style="text-align:left; line-height:1.4">
          <div>{warning}</div>
          {#if warning.includes('dial unix') || warning.includes('bus:') || warning.includes('dbus')}
            <div style="margin-top: 12px; font-size: 0.9em; opacity: 0.95;">
              <strong>Running in WSL?</strong> This environment lacks a desktop keyring (Secret Service). To use "Remember password", you can install one:
              <pre class="mono" style="background: rgba(0,0,0,0.15); padding: 8px; border-radius: 4px; margin-top: 6px; margin-bottom: 6px; white-space: pre-wrap; font-size: 0.85em; user-select: text;">sudo apt install dbus-x11 gnome-keyring
export $(dbus-launch)
eval "$(echo '\n' | gnome-keyring-daemon --unlock)"</pre>
              Or simply click <strong>Continue without remembering</strong> below.
            </div>
          {/if}
        </div>
      {/if}

      {#if unlockedWithWarning}
        <button class="primary" type="button" on:click={() => dispatch('unlocked', { unlocked: true, warning })}>
          Continue without remembering
        </button>
      {:else}
        <button class="primary" type="submit" disabled={!canSubmit}>
          {busy ? 'Unlocking…' : setup ? 'Create password and continue' : 'Unlock PrAImate'}
        </button>
      {/if}
    </form>

    <footer>
      The password is processed locally. It is never sent to an LLM provider
      or stored in the Git remote.
    </footer>
  </section>
</div>

<style>
  .lock-screen {
    min-height: 100vh;
    display: grid;
    place-items: center;
    padding: 32px;
    color: var(--text);
    background:
      radial-gradient(circle at 50% 0%, rgba(93, 214, 154, 0.09), transparent 35%),
      var(--bg);
  }
  .lock-card {
    width: min(480px, 100%);
    padding: 30px;
    border: 1px solid var(--border);
    border-radius: 16px;
    background: var(--bg-panel);
    box-shadow: 0 24px 80px rgba(0, 0, 0, 0.36);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 11px;
    padding-bottom: 22px;
    border-bottom: 1px solid var(--border);
  }
  .brand img { width: 38px; height: 38px; border-radius: 9px; }
  .brand div { display: grid; gap: 2px; }
  .brand span { font-weight: 700; letter-spacing: 0.01em; }
  .brand small, footer, .hint, .remember small {
    color: var(--text-dim);
  }
  .heading {
    display: flex;
    gap: 15px;
    margin: 25px 0 24px;
  }
  .lock-icon {
    flex: 0 0 auto;
    width: 42px;
    height: 42px;
    display: grid;
    place-items: center;
    color: var(--ok);
    background: rgba(34, 197, 94, 0.1);
    border-radius: 11px;
  }
  .lock-icon svg { width: 23px; height: 23px; }
  h1 { margin: 0 0 7px; font-size: 22px; line-height: 1.2; }
  p { margin: 0; line-height: 1.55; }
  form { display: grid; gap: 10px; }
  label { margin-top: 4px; font-size: 13px; font-weight: 600; }
  input[type='password'], input[type='text'] {
    box-sizing: border-box;
    width: 100%;
    height: 42px;
    padding: 0 12px;
    color: inherit;
    background: var(--bg-input);
    border: 1px solid var(--border);
    border-radius: 8px;
    outline: none;
  }
  input:focus { border-color: #5dd69a; box-shadow: 0 0 0 3px rgba(93, 214, 154, 0.12); }
  .password-row { position: relative; }
  .password-row input { padding-right: 62px; }
  button.show {
    position: absolute;
    right: 7px;
    top: 7px;
    height: 28px;
    border: 0;
    color: var(--text-dim);
    background: transparent;
    cursor: pointer;
  }
  .hint { font-size: 12px; line-height: 1.5; margin: 0 0 3px; }
  .remember {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    margin: 10px 0 4px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 9px;
    cursor: pointer;
  }
  .remember input { margin-top: 3px; accent-color: #5dd69a; }
  .remember span { display: grid; gap: 4px; }
  .remember small { font-weight: 400; line-height: 1.4; }
  .primary {
    height: 44px;
    margin-top: 8px;
    border: 0;
    border-radius: 8px;
    color: var(--bg-panel);
    background: var(--ok);
    font-weight: 700;
    cursor: pointer;
  }
  .primary:disabled { opacity: 0.45; cursor: not-allowed; }
  .message {
    padding: 10px 12px;
    border-radius: 8px;
    font-size: 13px;
    line-height: 1.45;
  }
  .message.error {
    color: var(--err);
    border: 1px solid var(--border);
    background: rgba(255, 0, 0, 0.1);
  }
  .message.warning {
    color: var(--warn);
    border: 1px solid var(--border);
    background: rgba(255, 200, 0, 0.1);
  }
  footer {
    margin-top: 23px;
    padding-top: 17px;
    border-top: 1px solid var(--border, #2a2e36);
    font-size: 11px;
    line-height: 1.5;
  }
</style>
