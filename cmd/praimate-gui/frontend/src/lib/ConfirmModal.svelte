<script>
  import { confirmModal } from './stores.js'
  import { focusDialog } from './focusDialog.js'
</script>

{#if $confirmModal}
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <div class="modal-backdrop confirm-backdrop" on:click|self={() => $confirmModal.resolve(false)}>
    <div class="modal-content confirm-modal" role="alertdialog" aria-modal="true" aria-labelledby="confirm-title" aria-describedby="confirm-message" use:focusDialog={{ onClose: () => $confirmModal.resolve(false) }}>
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
    background: var(--overlay);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 20px;

  }
  .confirm-modal {
    max-width: 440px;
    width: 100%;
    background: var(--bg-panel);
    border: 1px solid var(--border-bright);
    border-radius: var(--radius);
    padding: 22px;
    box-shadow: var(--shadow-overlay);
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
