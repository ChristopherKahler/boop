<script lang="ts">
  import { api } from '../api'
  import { pushState, enablePush, disablePush, defaultDeviceName, isIOS, type PushState } from '../push'
  import Card from './Card.svelte'
  import Button from './Button.svelte'
  import Input from './Input.svelte'
  import Notice from './Notice.svelte'
  import StatusDot from './StatusDot.svelte'
  import { panel } from '../motion'

  // Turns push on for this phone (or browser): the installed web app is the
  // phone app, so this card is all a phone needs.
  let { onchange }: { onchange?: () => void } = $props()

  let mode = $state<PushState | null>(null)
  let serverError = $state('')
  let name = $state(defaultDeviceName())
  let busy = $state(false)
  let error = $state('')
  let note = $state('')

  async function refresh() {
    try {
      const s = await pushState()
      mode = s.state
      serverError = s.error ?? ''
    } catch (e: any) {
      error = e.message
    }
  }
  $effect(() => {
    refresh()
  })

  async function run(fn: () => Promise<void>, done: string) {
    busy = true
    error = ''
    note = ''
    try {
      await fn()
      note = done
      await refresh()
      onchange?.()
    } catch (e: any) {
      error = e.message
    } finally {
      busy = false
    }
  }

  const turnOn = () => run(() => enablePush(name.trim() || defaultDeviceName()), 'Notifications are on for this device.')
  const turnOff = () => run(() => disablePush(), 'Notifications are off for this device.')
  const test = () =>
    run(async () => {
      const r = await api.test(undefined, true)
      const sent = r.deliveries.filter((d) => d.status === 'sent').length
      if (!sent) throw new Error(r.deliveries.length ? r.deliveries.map((d) => `${d.device_name}: ${d.error || d.status}`).join('; ') : 'No device is registered for push yet.')
    }, 'Test push sent. It should arrive in a few seconds.')
</script>

<Card title="This device">
  {#if mode === null}
    <p class="muted caption">Checking…</p>
  {:else if mode === 'on'}
    <div class="row"><StatusDot tone="ok">Receiving alerts on this device</StatusDot></div>
    <div class="buttons">
      <Button onclick={test} disabled={busy}>Send test push</Button>
      <Button variant="secondary" onclick={turnOff} disabled={busy}>Turn off</Button>
    </div>
  {:else if mode === 'off'}
    <p class="secondary lead">Get alerts on this device as notifications, with an Acknowledge button.</p>
    <form
      class="buttons"
      onsubmit={(e) => {
        e.preventDefault()
        turnOn()
      }}
    >
      <Input bind:value={name} aria-label="Device name" maxlength={80} style="width: 200px" />
      <Button type="submit" disabled={busy}>Turn on notifications</Button>
    </form>
  {:else if mode === 'needs-install'}
    <Notice tone="info">
      On {isIOS() ? 'iPhone and iPad' : 'this device'}, notifications work once the hub is on your home screen: tap the Share button, choose
      <b>Add to Home Screen</b>, open it from there, and come back to this page.
    </Notice>
  {:else if mode === 'denied'}
    <Notice tone="warn">Notifications are blocked for this site. Allow them in the browser's site settings, then reload this page.</Notice>
  {:else if mode === 'server-off'}
    <Notice tone="warn">Web push is not configured on the hub{serverError ? `: ${serverError}` : ''}.</Notice>
  {:else}
    <Notice tone="warn">This browser cannot receive web push notifications. On Android use Chrome; on iPhone use Safari and add the hub to the home screen.</Notice>
  {/if}
  {#if note}<div transition:panel class="msg"><Notice tone="good">{note}</Notice></div>{/if}
  {#if error}<div transition:panel class="msg"><Notice tone="bad">{error}</Notice></div>{/if}
</Card>

<style>
  .lead { font: var(--up-type-meta); line-height: 1.6; margin-bottom: 12px; }
  .buttons { display: flex; gap: var(--up-space-3); flex-wrap: wrap; align-items: center; margin-top: 12px; }
  .row { display: flex; align-items: center; gap: 8px; }
  .msg { margin-top: 12px; }
</style>
