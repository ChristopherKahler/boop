<script lang="ts">
  import { api, type Alert, type Event } from '../lib/api'
  import { link } from '../lib/router.svelte'
  import { relative, fullDate, seenRange, duration, parseTime } from '../lib/format'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import LevelBadge from '../lib/ui/LevelBadge.svelte'
  import AlertStatus from '../lib/ui/AlertStatus.svelte'
  import Notice from '../lib/ui/Notice.svelte'
  import Empty from '../lib/ui/Empty.svelte'
  import ProjectIcon from '../lib/ui/ProjectIcon.svelte'
  import Skeleton from '../lib/ui/Skeleton.svelte'
  import { rowIn, panel } from '../lib/motion'

  let { id }: { id: string } = $props()
  let alert = $state<Alert | null>(null)
  let events = $state<Event[]>([])
  let cursor = $state<string | undefined>()
  let error = $state('')
  let busy = $state(false)
  let loadingMore = $state(false)
  let now = $state(new Date())

  async function load() {
    try {
      error = ''
      alert = await api.alert(id)
      const page = await api.events({ alert: id, limit: 50 })
      events = page.events
      cursor = page.next_cursor
      now = new Date()
    } catch (e: any) {
      error = e.message
    }
  }
  $effect(() => {
    id
    alert = null
    load()
    const t = setInterval(load, 30_000)
    return () => clearInterval(t)
  })

  async function more() {
    loadingMore = true
    try {
      const page = await api.events({ alert: id, before: cursor, limit: 50 })
      events = [...events, ...page.events]
      cursor = page.next_cursor
    } catch (e: any) {
      error = e.message
    } finally {
      loadingMore = false
    }
  }

  async function act(what: 'acknowledge' | 'resolve' | 'reopen') {
    if (!alert) return
    busy = true
    try {
      alert = what === 'acknowledge' ? await api.acknowledge(alert.id) : what === 'resolve' ? await api.resolve(alert.id) : await api.reopen(alert.id)
    } catch (e: any) {
      error = e.message
    } finally {
      busy = false
    }
  }

  /** "after 4m" between the alert opening and a later time. */
  function after(from: string, to: string | null): string {
    const a = parseTime(from)
    const b = parseTime(to)
    if (!a || !b) return ''
    return `after ${duration((b.getTime() - a.getTime()) / 1000)}`
  }
</script>

<div class="stack">
  <div class="crumb"><a href="/" onclick={link}>Alerts</a><span class="faint">/</span><span class="muted mono">{id}</span></div>

  {#if error}
    <div transition:panel><Notice tone="bad">{error}</Notice></div>
  {/if}

  {#if !alert && !error}
    <Card><Skeleton lines={3} height={14} widths={['35%', '70%', '50%']} /></Card>
  {:else if alert}
    <Card>
      <div class="head">
        <div class="meta">
          <span class="secondary">{alert.client_name}</span>
          <span class="faint">·</span>
          <span class="secondary proj"><ProjectIcon icon={alert.project_icon} size={14} /><span>{alert.project_name}</span></span>
          <span class="faint">·</span>
          <LevelBadge level={alert.level} />
          <AlertStatus status={alert.status} />
        </div>
        <h1>{alert.title} {#if alert.count > 1}<span class="count">×{alert.count}</span>{/if}</h1>
        <p class="secondary">{seenRange(alert.first_seen, alert.last_seen, now)}</p>
        <div class="buttons">
          {#if alert.status === 'open'}
            <Button onclick={() => act('acknowledge')} disabled={busy}>Acknowledge</Button>
          {/if}
          {#if alert.status !== 'resolved'}
            <Button variant={alert.status === 'open' ? 'secondary' : 'primary'} onclick={() => act('resolve')} disabled={busy}>Resolve</Button>
          {:else}
            <Button variant="secondary" onclick={() => act('reopen')} disabled={busy}>Reopen</Button>
          {/if}
        </div>
      </div>
      <div class="facts">
        <div><span class="k">First seen</span><span title={alert.first_seen}>{fullDate(alert.first_seen)}</span></div>
        <div><span class="k">Last seen</span><span title={alert.last_seen}>{fullDate(alert.last_seen)} · {relative(alert.last_seen, now)}</span></div>
        <div>
          <span class="k">Pushed</span>
          <span>{#if alert.notified_at}{fullDate(alert.notified_at)} <span class="muted">{after(alert.first_seen, alert.notified_at)}</span>{:else}<span class="muted">Not pushed (see routing on the events)</span>{/if}</span>
        </div>
        <div>
          <span class="k">Acknowledged</span>
          <span>{#if alert.acknowledged_at}{fullDate(alert.acknowledged_at)} <span class="muted">{after(alert.first_seen, alert.acknowledged_at)} · {alert.acknowledged_by}</span>{:else}<span class="muted">Not yet</span>{/if}</span>
        </div>
        <div>
          <span class="k">Resolved</span>
          <span>{#if alert.resolved_at}{fullDate(alert.resolved_at)} <span class="muted">{after(alert.first_seen, alert.resolved_at)} · {alert.resolved_by === 'recovery' ? 'recovered on its own' : alert.resolved_by}</span>{:else}<span class="muted">Not yet</span>{/if}</span>
        </div>
        {#if alert.fingerprint}<div><span class="k">Fingerprint</span><span class="mono">{alert.fingerprint}</span></div>{/if}
        {#if alert.source}<div><span class="k">Source</span><span>{alert.source}</span></div>{/if}
      </div>
    </Card>

    <Card flush title="Occurrences">
      {#if events.length === 0}
        <Empty title="No occurrences stored">Older events may have been removed by retention.</Empty>
      {:else}
        {#each events as e, i (e.id)}
          <a class="ev" href="/events/{e.id}" onclick={link} in:rowIn={{ i }}>
            <div class="main">
              <div class="title">{e.title}{#if e.id === alert.resolve_event_id}<span class="recovery">recovery</span>{/if}</div>
              {#if e.route}<div class="body">{e.route}</div>{:else if e.body}<div class="body">{e.body}</div>{/if}
            </div>
            <div class="lvl"><LevelBadge level={e.level} /></div>
            <div class="time" title={e.created_at}>{relative(e.created_at, now)}</div>
          </a>
        {/each}
        {#if cursor}
          <div class="more"><Button variant="ghost" size="sm" onclick={more} disabled={loadingMore}>{loadingMore ? 'Loading' : 'Load more'}</Button></div>
        {/if}
      {/if}
    </Card>
  {/if}
</div>

<style>
  .crumb { display: flex; gap: 8px; align-items: center; font: var(--up-type-meta); }
  .head { display: flex; flex-direction: column; gap: 8px; }
  .meta { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; font: var(--up-type-meta); }
  .proj { display: inline-flex; gap: 6px; align-items: center; }
  h1 { font: var(--up-type-metric); letter-spacing: -0.01em; word-break: break-word; display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
  .count { font: var(--up-type-small); color: var(--up-accent-hover); background: var(--up-accent-tint); border-radius: var(--up-radius-pill); padding: 2px 10px; }
  .buttons { display: flex; gap: var(--up-space-3); flex-wrap: wrap; margin-top: 4px; }
  .facts { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 24px; margin-top: var(--up-space-4); padding-top: var(--up-space-4); border-top: 1px solid var(--up-border-hairline); }
  .facts > div { display: flex; flex-direction: column; gap: 2px; font: var(--up-type-meta); min-width: 0; }
  .facts span:last-child { overflow: hidden; text-overflow: ellipsis; }
  .k { font: var(--up-type-caption); color: var(--up-text-muted); }
  .ev { display: grid; grid-template-columns: 1fr auto 64px; gap: var(--up-space-4); align-items: center; padding: 12px var(--up-space-4); border-radius: var(--up-radius-control); color: var(--up-ink); transition: background 120ms ease-out; }
  .ev:hover { background: var(--up-bg-hover); color: var(--up-ink); }
  .main { min-width: 0; }
  .title { font: var(--up-type-row-title); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; display: flex; gap: 8px; align-items: baseline; }
  .recovery { font: var(--up-type-small); background: var(--boop-success); color: var(--boop-success-strong); border-radius: var(--up-radius-pill); padding: 1px 8px; }
  .body { font: var(--up-type-meta); color: var(--up-text-muted); margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .time { font: var(--up-type-caption); color: var(--up-text-muted); text-align: right; }
  .lvl { display: flex; }
  .more { display: flex; justify-content: center; padding: 10px 0 4px; }
  @media (max-width: 600px) { .facts { grid-template-columns: 1fr; } }
</style>
