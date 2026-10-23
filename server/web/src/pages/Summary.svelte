<script lang="ts">
  import { api, type ClientSummary } from '../lib/api'
  import { link, router } from '../lib/router.svelte'
  import { LEVEL_LABEL } from '../lib/levels'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Notice from '../lib/ui/Notice.svelte'
  import LevelBadge from '../lib/ui/LevelBadge.svelte'
  import Skeleton from '../lib/ui/Skeleton.svelte'

  // A client's month: what to show them for the monitoring retainer.
  let { id }: { id: string } = $props()
  let month = $state(typeof location !== 'undefined' ? (new URLSearchParams(location.search).get('month') ?? '') : '')
  let sum = $state<ClientSummary | null>(null)
  let error = $state('')

  $effect(() => {
    id
    month
    sum = null
    error = ''
    api
      .clientSummary(id, month || undefined)
      .then((s) => (sum = s))
      .catch((e) => (error = e.message))
  })

  function go(m: string) {
    month = m
    router.navigate(`/clients/${encodeURIComponent(id)}/summary?month=${m}`, true)
  }

  function dur(sec: number | null | undefined): string {
    if (sec === null || sec === undefined) return '—'
    const s = Math.round(sec)
    if (s < 60) return `${s}s`
    if (s < 3600) return `${Math.floor(s / 60)}m`
    if (s < 86400) return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`
    return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`
  }
  function when(ts: string, tz: string): string {
    return new Date(ts).toLocaleString('en-US', { timeZone: tz, month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false })
  }
  const OUTCOME: Record<string, string> = { recovered: 'Recovered on its own', resolved: 'Resolved by hand', open: 'Still open' }
</script>

<div class="stack">
  <div class="crumb"><a href="/projects" onclick={link}>Clients</a><span class="faint">/</span><span class="muted">{sum?.client_name ?? id}</span></div>

  {#if error}
    <Notice tone="bad">{error}</Notice>
  {:else if !sum}
    <Card><Skeleton lines={4} height={14} widths={['40%', '70%', '55%', '60%']} /></Card>
  {:else}
    <div class="head">
      <div>
        <h1>{sum.client_name}</h1>
        <div class="nav">
          <button type="button" class="arrow" onclick={() => go(sum!.previous_month)} aria-label="Previous month">‹</button>
          <span class="month">{sum.month_label}</span>
          <button type="button" class="arrow" onclick={() => go(sum!.next_month)} aria-label="Next month">›</button>
          <span class="caption muted">times in {sum.timezone}</span>
        </div>
      </div>
      <a class="download" href={`/api/v1/clients/${encodeURIComponent(id)}/summary.md?month=${sum.month}`} download>
        <Button variant="secondary">Download Markdown</Button>
      </a>
    </div>

    {#if sum.retention_note}<Notice tone="info">{sum.retention_note}</Notice>{/if}

    <div class="metrics">
      <div class="metric"><span class="n">{sum.totals.alerts}</span><span class="l">alerts</span></div>
      <div class="metric"><span class="n">{sum.totals.resolved}</span><span class="l">resolved ({sum.totals.recovered} on their own)</span></div>
      <div class="metric"><span class="n">{sum.totals.still_open}</span><span class="l">still open</span></div>
      <div class="metric"><span class="n">{sum.time_to_acknowledge.count ? dur(sum.time_to_acknowledge.median_seconds) : '—'}</span><span class="l">median time to acknowledge</span></div>
      <div class="metric"><span class="n">{sum.time_to_resolve.count ? dur(sum.time_to_resolve.median_seconds) : '—'}</span><span class="l">median time to resolve</span></div>
      <div class="metric"><span class="n">{sum.totals.events}</span><span class="l">events · {sum.totals.pushes} pushes</span></div>
    </div>

    <div class="two">
      <Card title="Alerts by level">
        {#each sum.by_level as l (l.level)}
          <div class="lrow"><LevelBadge level={l.level} /><span class="cnt">{l.count}</span></div>
        {/each}
      </Card>
      <Card title="By project">
        {#if sum.projects.length === 0}<p class="muted caption">No alerts this month.</p>{/if}
        {#each sum.projects as p (p.project)}
          <div class="lrow"><span>{p.project}</span><span class="cnt">{p.alerts}{p.open ? ` · ${p.open} open` : ''}</span></div>
        {/each}
      </Card>
    </div>

    <Card title="What broke" flush>
      {#if sum.alerts.length === 0}
        <p class="muted caption pad">Nothing broke in {sum.month_label}.</p>
      {:else}
        <div class="table">
          <div class="tr th caption faint"><div>First seen</div><div>Problem</div><div>Level</div><div class="r">×</div><div>Acknowledged</div><div>Resolved</div><div>Outcome</div></div>
          {#each sum.alerts as a (a.id)}
            <a class="tr" href="/alerts/{a.id}" onclick={link}>
              <div class="muted">{when(a.first_seen, sum.timezone)}</div>
              <div class="prob"><span class="t">{a.title}</span><span class="caption muted">{a.project}{a.source ? ` · ${a.source}` : ''}</span></div>
              <div>{LEVEL_LABEL[a.level as keyof typeof LEVEL_LABEL] ?? a.level}</div>
              <div class="r">{a.count}</div>
              <div>{dur(a.time_to_acknowledge_seconds)}</div>
              <div>{dur(a.time_to_resolve_seconds)}</div>
              <div class="outcome {a.status}">{OUTCOME[a.status]}</div>
            </a>
          {/each}
        </div>
      {/if}
    </Card>

    {#if sum.recoveries.length}
      <Card title="Recovered on their own">
        {#each sum.recoveries as a (a.id)}
          <div class="rec"><span class="muted">{when(a.first_seen, sum.timezone)}</span> {a.title} <span class="caption muted">· {a.project} · lasted {dur(a.time_to_resolve_seconds)}</span></div>
        {/each}
      </Card>
    {/if}
    {#if sum.carried_over}<p class="caption muted">{sum.carried_over} alert{sum.carried_over === 1 ? ' was' : 's were'} still open from before this month.</p>{/if}
  {/if}
</div>

<style>
  .crumb { display: flex; gap: 8px; align-items: center; font: var(--up-type-meta); }
  .head { display: flex; justify-content: space-between; align-items: flex-end; gap: 16px; flex-wrap: wrap; }
  h1 { font: var(--up-type-metric); }
  .nav { display: flex; align-items: center; gap: 8px; margin-top: 4px; }
  .month { font: var(--up-type-setting); }
  .arrow { border: 1px solid var(--up-border-control); background: var(--up-bg); border-radius: var(--up-radius-control); width: 28px; height: 28px; cursor: pointer; color: var(--up-ink); }
  .download { text-decoration: none; }
  .metrics { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--up-space-3); }
  .metric { display: flex; flex-direction: column; gap: 2px; padding: 14px 16px; border: 1px solid var(--up-border-hairline); border-radius: var(--up-radius-card); background: var(--up-bg); }
  .n { font: var(--up-type-metric); color: var(--up-ink); }
  .l { font: var(--up-type-caption); color: var(--up-text-muted); }
  .two { display: grid; grid-template-columns: 1fr 1fr; gap: var(--up-space-4); }
  .lrow { display: flex; justify-content: space-between; align-items: center; padding: 6px 0; font: var(--up-type-meta); border-top: 1px solid var(--up-border-hairline); }
  .lrow:first-of-type { border-top: none; }
  .cnt { color: var(--up-ink); }
  .pad { padding: 0 var(--up-space-4) var(--up-space-3); }
  .table { overflow-x: auto; }
  .tr { display: grid; grid-template-columns: 92px minmax(0, 1fr) 62px 26px 92px 74px 104px; gap: 10px; align-items: center; padding: 10px var(--up-space-4); font: var(--up-type-meta); color: var(--up-ink); border-radius: var(--up-radius-control); }
  a.tr:hover { background: var(--up-bg-hover); color: var(--up-ink); }
  .th { padding-bottom: 6px; }
  .prob { display: flex; flex-direction: column; min-width: 0; }
  .prob .t { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .r { text-align: right; }
  .outcome.recovered { color: var(--boop-success-strong); }
  .outcome.open { color: var(--boop-error-strong); }
  .rec { font: var(--up-type-meta); padding: 4px 0; }
  @media (max-width: 640px) {
    .tr { min-width: 680px; }
    .metrics { grid-template-columns: 1fr 1fr; }
    .two { grid-template-columns: 1fr; }
  }
</style>
