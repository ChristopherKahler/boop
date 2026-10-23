<script lang="ts">
  import type { AlertStatus } from '../api'
  let { status }: { status: AlertStatus } = $props()
  const LABEL: Record<AlertStatus, string> = { open: 'Open', acknowledged: 'Acknowledged', resolved: 'Resolved' }
</script>

<span class="status {status}">{LABEL[status]}</span>

<style>
  .status {
    display: inline-flex;
    align-items: center;
    font: var(--up-type-small);
    padding: 1px 8px;
    border-radius: var(--up-radius-pill);
    white-space: nowrap;
  }
  .open { background: var(--boop-error); color: var(--boop-error-strong); }
  .acknowledged { background: var(--boop-warning); color: var(--boop-warning-strong); }
  .resolved { background: var(--boop-success); color: var(--boop-success-strong); }
</style>
