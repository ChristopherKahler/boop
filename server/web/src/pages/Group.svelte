<script lang="ts">
  import { api, type Event } from '../lib/api'
  import { link } from '../lib/router.svelte'
  import { relative, fullDate, seenRange } from '../lib/format'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import LevelBadge from '../lib/ui/LevelBadge.svelte'
  import Notice from '../lib/ui/Notice.svelte'
  import Empty from '../lib/ui/Empty.svelte'
  import ProjectIcon from '../lib/ui/ProjectIcon.svelte'
  import Skeleton from '../lib/ui/Skeleton.svelte'
  import { rowIn } from '../lib/motion'

  let { project, fingerprint }: { project: string; fingerprint: string } = $props()
  let head = $state<Event | null>(null)
  let events = $state<Event[]>([])
  let cursor = $state<string | undefined>()
  let loading = $state(true)
  let loadingMore = $state(false)
  let error = $state('')
  let now = $state(new Date())

  async function load(reset = true) {
    try {
      error = ''
      if (reset) loading = true
      else loadingMore = true
      if (reset) {
        const g = await api.events({ project, fingerprint, grouped: true, limit: 1 })
        head = g.events[0] ?? null
      }
      const page = await api.events({ project, fingerprint, before: reset ? undefined : cursor, limit: 50 })
      events = reset ? page.events : [...events, ...page.events]
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
    project
    fingerprint
    load(true)
  })
</script>

<div class="stack">
  <div class="crumb"><a href="/events" onclick={link}>Events</a><span class="faint">/</span><span class="muted mono">{fingerprint}</span></div>

  {#if error}
    <Notice tone="bad">{error}</Notice>
  {/if}

  {#if head}
    <Card>
      <div class="head">
        <div class="meta">
          <span class="secondary proj"><ProjectIcon icon={head.project_icon} size={14} /><span>{head.project_name}</span></span>
          <span class="faint">·</span>
          <LevelBadge level={head.level} />
          {#if head.source}<span class="faint">·</span><span class="muted">{head.source}</span>{/if}
        </div>
        <h1>{head.title} <span class="count">{head.group?.count ?? events.length} occurrence{(head.group?.count ?? events.length) === 1 ? '' : 's'}</span></h1>
        {#if head.group}<p class="secondary">{seenRange(head.group.first_seen, head.group.last_seen, now)}</p>{/if}
        <div class="facts">
          <div><span class="k">Fingerprint</span><span class="mono">{fingerprint}</span></div>
          {#if head.group}
            <div><span class="k">First seen</span><span title={head.group.first_seen}>{fullDate(head.group.first_seen)}</span></div>
            <div><span class="k">Last seen</span><span title={head.group.last_seen}>{fullDate(head.group.last_seen)}</span></div>
          {/if}
        </div>
      </div>
    </Card>
  {/if}

  <Card flush title="Occurrences">
    {#if loading && events.length === 0}
      <Skeleton rows={4} />
    {:else if events.length === 0}
      <Empty title="No occurrences">Nothing with this fingerprint in this project.</Empty>
    {:else}
      {#each events as e, i (e.id)}
        <a class="ev" href="/events/{e.id}" onclick={link} in:rowIn={{ i }}>
          <div class="main">
            <div class="title">{e.title}</div>
            {#if e.body}<div class="body">{e.body}</div>{/if}
          </div>
          <div class="lvl"><LevelBadge level={e.level} />{#if e.silenced}<span class="caption silenced">silenced</span>{/if}</div>
          <div class="time" title={e.created_at}>{relative(e.created_at, now)}</div>
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
  .crumb { display: flex; gap: 8px; align-items: center; font: var(--up-type-meta); }
  .head { display: flex; flex-direction: column; gap: 8px; }
  .proj { display: inline-flex; gap: 6px; align-items: center; }
  .meta { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; font: var(--up-type-meta); }
  h1 { font: var(--up-type-metric); letter-spacing: -0.01em; word-break: break-word; display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
  .count { font: var(--up-type-small); color: var(--up-accent-hover); background: var(--up-accent-tint); border-radius: var(--up-radius-pill); padding: 2px 10px; }
  .facts { display: grid; grid-template-columns: 1fr 1fr; gap: 8px 24px; margin-top: var(--up-space-4); padding-top: var(--up-space-4); border-top: 1px solid var(--up-border-hairline); }
  .facts > div { display: flex; flex-direction: column; gap: 2px; font: var(--up-type-meta); min-width: 0; }
  .facts span:last-child { overflow: hidden; text-overflow: ellipsis; }
  .k { font: var(--up-type-caption); color: var(--up-text-muted); }
  .ev { display: grid; grid-template-columns: 1fr auto 64px; gap: var(--up-space-4); align-items: center; padding: 12px var(--up-space-4); border-radius: var(--up-radius-control); color: var(--up-ink); transition: background 120ms ease-out; }
  .ev:hover { background: var(--up-bg-hover); color: var(--up-ink); }
  .main { min-width: 0; }
  .title { font: var(--up-type-row-title); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .body { font: var(--up-type-meta); color: var(--up-text-muted); margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .time { font: var(--up-type-caption); color: var(--up-text-muted); text-align: right; }
  .lvl { display: flex; flex-direction: column; align-items: flex-start; gap: 2px; }
  .silenced { color: var(--up-text-inactive); }
  .more { display: flex; justify-content: center; padding: 10px 0 4px; }
  @media (max-width: 600px) { .facts { grid-template-columns: 1fr; } }
</style>
