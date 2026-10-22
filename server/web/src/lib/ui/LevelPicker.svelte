<script lang="ts">
  import { LEVELS, type Level } from '../api'
  import { LEVEL_LABEL } from '../levels'

  // A row of toggle chips, one per level: lit levels push to the phone, the
  // rest only land in the inbox.
  let { value, onchange, disabled = false, label = 'Levels that push' }: { value: Level[]; onchange: (v: Level[]) => void; disabled?: boolean; label?: string } =
    $props()

  function toggle(l: Level) {
    const next = value.includes(l) ? value.filter((x) => x !== l) : [...value, l]
    onchange(LEVELS.filter((x) => next.includes(x)))
  }
</script>

<div class="picker" role="group" aria-label={label}>
  {#each LEVELS as l (l)}
    <button type="button" class="chip {l}" class:on={value.includes(l)} aria-pressed={value.includes(l)} {disabled} onclick={() => toggle(l)}>
      <span class="dot"></span>{LEVEL_LABEL[l]}
    </button>
  {/each}
</div>

<style>
  .picker { display: flex; flex-wrap: wrap; gap: 6px; }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font: var(--up-type-small);
    height: 26px;
    padding: 0 10px;
    border-radius: var(--up-radius-pill);
    border: none;
    cursor: pointer;
    background: var(--up-bg);
    color: var(--up-text-muted);
    box-shadow: inset 0 0 0 1px var(--up-border-control);
    transition: background 120ms ease-out, color 120ms ease-out, box-shadow 120ms ease-out;
  }
  .chip:disabled { cursor: default; opacity: 0.6; }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--up-border-control); }
  .chip.on { box-shadow: none; color: var(--up-ink); }
  .chip.on .dot { background: currentColor; }
  .info.on { background: var(--boop-info); color: var(--boop-info-strong); }
  .success.on { background: var(--boop-success); color: var(--boop-success-strong); }
  .warning.on { background: var(--boop-warning); color: var(--boop-warning-strong); }
  .error.on { background: var(--boop-error); color: var(--boop-error-strong); }
  .critical.on { background: var(--boop-critical); color: var(--boop-critical-strong); }
</style>
