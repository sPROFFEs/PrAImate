<script>
  import { confirmModal } from './stores.js'

  function onKeydown(e) {
    if (!$confirmModal) return
    if (e.key === 'Escape') {
      $confirmModal.resolve(false)
    } else if (e.key === 'Enter') {
      $confirmModal.resolve(true)
    }
  }
</script>

<svelte:window on:keydown={onKeydown} />

{#if $confirmModal}
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <div class="modal-backdrop confirm-backdrop" on:click|self={() => $confirmModal.resolve(false)}>
    <div class="modal-content confirm-modal" role="alertdialog" aria-modal="true" aria-labelledby="confirm-title" aria-describedby="confirm-message">
      <div class="confirm-head">
        <h2 id="confirm-title">{$confirmModal.title}</h2>
      </div>
      <p id="confirm-message" class="confirm-body">{$confirmModal.message}</p>
      <div class="confirm-actions">
        <button class="btn" type="button" on:click={() => $confirmModal.resolve(false)}>
          {$confirmModal.cancelLabel || 'Cancel'}
        </button>
        <button class="btn" class:danger={$confirmModal.tone === 'danger'} class:primary={$confirmModal.tone === 'primary'} type="button" on:click={() => $confirmModal.resolve(true)}>
          {$confirmModal.confirmLabel || 'Confirm'}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .confirm-backdrop {
    z-index: 25000;
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.65);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 20px;
    backdrop-filter: blur(2px);
  }
  .confirm-modal {
    max-width: 440px;
    width: 100%;
    background: var(--bg-panel);
    border: 1px solid var(--border-bright);
    border-radius: var(--radius);
    padding: 22px;
    box-shadow: 0 16px 40px rgba(0, 0, 0, 0.45);
  }
  .confirm-head h2 {
    margin: 0 0 8px;
    font-size: 16px;
    font-weight: 650;
    color: var(--text);
  }
  .confirm-body {
    margin: 0 0 20px;
    color: var(--text-dim);
    font-size: 13px;
    line-height: 1.5;
    white-space: pre-wrap;
  }
  .confirm-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
