<script>
  import { createEventDispatcher } from 'svelte'
  import { toolLevelsForCli } from './chatTools.js'
  export let cli = ''
  export let value = ''
  export let disabled = false
  const dispatch = createEventDispatcher()
</script>

<div class="toolpick" role="group" aria-label="Tools permissions">
  <span>Tools</span>
  {#each toolLevelsForCli(cli) as level}
    <button type="button" class="btn sm" class:primary={value === level.id} aria-pressed={value === level.id} title={level.hint} {disabled} on:click={() => dispatch('change', level.id)}>{level.label}</button>
  {/each}
</div>

<style>
  .toolpick { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; }
  .toolpick span { font-size: 12px; color: var(--text-dim); margin-right: 4px; }
</style>
