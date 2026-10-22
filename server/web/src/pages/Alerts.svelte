<script lang="ts">
  import { api, LEVELS, type Alert, type Client } from '../lib/api'
  import { link } from '../lib/router.svelte'
  import { relative, seenRange } from '../lib/format'
  import { LEVEL_LABEL } from '../lib/levels'
  import Card from '../lib/ui/Card.svelte'
  import Select from '../lib/ui/Select.svelte'
  import Button from '../lib/ui/Button.svelte'
  import LevelBadge from '../lib/ui/LevelBadge.svelte'
  import AlertStatus from '../lib/ui/AlertStatus.svelte'
  import Empty from '../lib/ui/Empty.svelte'
  import Notice from '../lib/ui/Notice.svelte'
  import ProjectIcon from '../lib/ui/ProjectIcon.svelte'
  import Skeleton from '../lib/ui/Skeleton.svelte'
  import { rowIn, reorder, panel } from '../lib/motion'

  const STATUSES = [
    { value: 'active', label: 'Needs attention' },
    { value: 'open', label: 'Open' },
    { value: 'acknowledged', label: 'Acknowledged' },
    { value: 'resolved', label: 'Resolved' },
    { value: '', label: 'All' },
  ]

  let alerts = $state<Alert[]>([])
  let clients = $state<Client[]>([])
  let counts = $state({ open: 0, acknowledged: 0 })
  let cursor = $state<string | undefined>()
  let loading = $state(true)
  let loadingMore = $state(false)
  let error = $state('')
  let status = $state(readStatus())
  let client = $state('')
  let level = $state('')
  let busy = $state<string | null>(null)
  let now = $state(new Date())

  function readStatus(): string {
    try {
      return localStorage.getItem('boop.alerts.status') ?? 'active'
    } catch {
      return 'active'
    }
  }
  function setStatus(v: string) {
    status = v
    try {
      localStorage.setItem('boop.alerts.status', v)
    } catch {
      /* private mode */
    }
  }

  async function load(reset = true) {
    try {
      error = ''
      if (reset) loading = true
      else loadingMore = true
      const page = await api.alerts({ status, client, level, before: reset ? undefined : cursor, limit: 50 })
      alerts = reset ? page.alerts : [...alerts, ...page.alerts]
      counts = { open: page.open, acknowledged: page.acknowledged }
      cursor = page.next_cursor
      now = new Date()
    } catch (e: any) {
      error = e.message
    } finally {
      loading = false
      loadingMore = false
    }
  }

  $effect(() => {
    status
    client
    level
    load(true)
  })
  $effect(() => {
    api.clients().then((r) => (clients = r.clients)).catch(() => {})
    const t = setInterval(() => load(true), 30_000)
    const tick = setInterval(() => (now = new Date()), 15_000)
    return () => {
      clearInterval(t)
      clearInterval(tick)
    }
  })

  async function act(a: Alert, what: 'acknowledge' | 'resolve', e: MouseEvent) {
    e.preventDefault()
    e.stopPropagation()
    busy = a.id
    try {
      await (what === 'acknowledge' ? api.acknowledge(a.id) : api.resolve(a.id))
      await load(true)
    } catch (err: any) {
      error = err.message
    } finally {
      busy = null
    }
  }
</script>

<div class="stack">
  <div class="summary">
    <span class="big">{counts.open}</span><span class="muted">open</span>
    <span class="faint">·</span>
    <span class="big">{counts.acknowledged}</span><span class="muted">acknowledged</span>
  </div>

  <div class="toolbar">
    <Select value={status} options={STATUSES} onchange={(e) => setStatus((e.currentTarget as HTMLSelectElement).value)} aria-label="Status" />
    <Select bind:value={client} options={[{ value: '', label: 'All clients' }, ...clients.map((c) => ({ value: c.id, label: c.name }))]} aria-label="Client" />
    <Select bind:value={level} options={[{ value: '', label: 'All levels' }, ...LEVELS.map((l) => ({ value: l, label: LEVEL_LABEL[l] }))]} aria-label="Level" />
    <span class="spacer"></span>
    <Button variant="secondary" size="sm" onclick={() => load(true)} disabled={loading}>Refresh</Button>
  </div>

  {#if error}
    <div transition:panel><Notice tone="bad">{error}</Notice></div>
  {/if}

  <Card flush>
    {#if loading && alerts.length === 0}
      <Skeleton rows={5} />
    {:else if alerts.length === 0}
      <Empty title={status === 'active' || status === 'open' ? 'Nothing needs attention' : 'No alerts here'}>
        Warnings, errors and critical events open an alert. Repeats of the same problem are counted on it, and a recovery closes it.
        See every event in <a href="/events" onclick={link}>Events</a>.
      </Empty>
    {:else}
      {#each alerts as a, i (a.id)}
        <a class="row" href="/alerts/{a.id}" onclick={link} in:rowIn={{ i }} animate:reorder>
          <div class="who">
            <span class="client">{a.client_name}</span>
            <span class="proj"><ProjectIcon icon={a.project_icon} size={12} /><span>{a.project_name}</span></span>
          </div>
          <div class="main">
            <div class="title">
              <span class="t">{a.title}</span>
              {#if a.count > 1}<span class="count" title="{a.count} occurrences">×{a.count}</span>{/if}
            </div>
            <div class="seen">{seenRange(a.first_seen, a.last_seen, now)}</div>
          </div>
          <div class="state">
            <LevelBadge level={a.level} />
            <AlertStatus status={a.status} />
          </div>
          <div class="side">
            <span class="time" title={a.last_seen}>{relative(a.last_seen, now)}</span>
            {#if a.status === 'open'}
              <Button variant="secondary" size="sm" disabled={busy === a.id} onclick={(e: MouseEvent) => act(a, 'acknowledge', e)}>Ack</Button>
            {:else if a.status === 'acknowledged'}
              <Button variant="secondary" size="sm" disabled={busy === a.id} onclick={(e: MouseEvent) => act(a, 'resolve', e)}>Resolve</Button>
            {/if}
          </div>
        </a>
      {/each}
      {#if cursor}
        <div class="more">
          <Button variant="ghost" size="sm" onclick={() => load(false)} disabled={loadingMore}>{loadingMore ? 'Loading' : 'Load more'}</Button>
        </div>
      {/if}
    {/if}
  </Card>
</div>

<style>
  .summary { display: flex; align-items: baseline; gap: 8px; font: var(--up-type-meta); }
  .big { font: var(--up-type-metric); color: var(--up-ink); }
  .toolbar { display: flex; gap: var(--up-space-3); align-items: center; flex-wrap: wrap; }
  .spacer { flex: 1; }
  .row {
    display: grid;
    grid-template-columns: 140px minmax(0, 1fr) 110px 90px;
    gap: var(--up-space-4);
    align-items: center;
    padding: 12px var(--up-space-4);
    border-radius: var(--up-radius-control);
    color: var(--up-ink);
    transition: background 120ms ease-out;
  }
  .row:hover { background: var(--up-bg-hover); color: var(--up-ink); }
  .who { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .client { font: var(--up-type-meta); color: var(--up-ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .proj { font: var(--up-type-caption); color: var(--up-text-secondary); display: flex; gap: 5px; align-items: center; min-width: 0; }
  .proj span:last-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .main { min-width: 0; }
  .title { font: var(--up-type-row-title); display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .title .t { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .count { font: var(--up-type-small); color: var(--up-accent-hover); background: var(--up-accent-tint); border-radius: var(--up-radius-pill); padding: 1px 8px; flex: none; }
  .seen { font: var(--up-type-meta); color: var(--up-text-muted); margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .state { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; }
  .side { display: flex; flex-direction: column; align-items: flex-end; gap: 6px; }
  .time { font: var(--up-type-caption); color: var(--up-text-muted); }
  .more { display: flex; justify-content: center; padding: 10px 0 4px; }
  @media (max-width: 640px) {
    .row { grid-template-columns: minmax(0, 1fr) auto; grid-template-rows: auto auto auto; row-gap: 6px; }
    .who { grid-column: 1; grid-row: 1; flex-direction: row; gap: 8px; align-items: baseline; }
    .side { grid-column: 2; grid-row: 1 / span 2; }
    .main { grid-column: 1; grid-row: 2; }
    .state { grid-column: 1 / span 2; grid-row: 3; flex-direction: row; align-items: center; gap: 10px; }
  }
</style>
