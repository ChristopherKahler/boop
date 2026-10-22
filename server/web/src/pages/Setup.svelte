<script lang="ts">
  import { api, type Client, type ProjectCreated, type Status } from '../lib/api'
  import { router } from '../lib/router.svelte'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Input from '../lib/ui/Input.svelte'
  import CodeBlock from '../lib/ui/CodeBlock.svelte'
  import Notice from '../lib/ui/Notice.svelte'
  import QR from '../lib/ui/QR.svelte'
  import ThisDevice from '../lib/ui/ThisDevice.svelte'
  import IntakesPanel from '../lib/ui/IntakesPanel.svelte'
  import { fly } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { dur, panel } from '../lib/motion'

  // First visit: from nothing to a push on your phone. Every step works for
  // any operator; nothing here is specific to one install.
  let { onfinished }: { onfinished: () => void } = $props()

  const STEPS = ['Your first client', 'Connect a service', 'Your phone', 'Test push']
  let step = $state(0)
  let status = $state<Status | null>(null)
  let error = $state('')
  let clientName = $state('')
  let projectName = $state('')
  let client = $state<Client | null>(null)
  let project = $state<ProjectCreated | null>(null)
  let busy = $state(false)
  let testMsg = $state('')
  let testOk = $state(false)

  async function refresh() {
    try {
      status = await api.status()
    } catch (e: any) {
      error = e.message
    }
  }
  $effect(() => {
    refresh()
  })

  async function createFirst() {
    busy = true
    error = ''
    try {
      // Rename the built-in client rather than leave an empty "Internal" behind.
      const existing = (await api.clients()).clients
      const internal = existing.find((c) => c.slug === 'internal' && c.project_count === 0)
      client = internal ? await api.updateClient(internal.id, { name: clientName.trim() }) : await api.createClient({ name: clientName.trim() })
      project = await api.createProject({ name: projectName.trim(), client_id: client.id })
      step = 1
    } catch (e: any) {
      error = e.message
    } finally {
      busy = false
    }
  }

  async function sendTest() {
    busy = true
    testMsg = ''
    try {
      const r = await api.test(project?.id, true)
      const sent = r.deliveries.filter((d) => d.status === 'sent').length
      testOk = sent > 0
      if (r.deliveries.length === 0) testMsg = 'Nothing was sent: no phone has notifications turned on yet. Go back one step.'
      else if (sent) testMsg = `Sent to ${sent} device${sent === 1 ? '' : 's'}. Check your phone.`
      else testMsg = 'The push did not go through: ' + r.deliveries.map((d) => `${d.device_name}: ${d.error || d.status}`).join('; ')
      await refresh()
    } catch (e: any) {
      testMsg = e.message
    } finally {
      busy = false
    }
  }

  async function finish() {
    try {
      await api.updateSettings({ setup_completed: true })
      onfinished()
      router.navigate('/', true)
    } catch (e: any) {
      error = e.message
    }
  }

  const here = typeof location !== 'undefined' ? location.origin : ''
  // The phone needs the hub's public address (BOOP_BASE_URL), not whatever this
  // browser happens to use (often 127.0.0.1 on the hub's own machine).
  const origin = $derived(status?.base_url || here)
  const insecure = typeof location !== 'undefined' && location.protocol !== 'https:' && !['localhost', '127.0.0.1'].includes(location.hostname)
</script>

<div class="setup">
  <div class="hero">
    <h1>Welcome</h1>
    <p class="secondary">Four short steps to your first alert on your phone.</p>
  </div>

  <ol class="steps">
    {#each STEPS as s, i (s)}
      <li class:done={i < step} class:active={i === step}><button type="button" disabled={i > 0 && !project} onclick={() => (step = i)}><span class="num">{i + 1}</span>{s}</button></li>
    {/each}
  </ol>

  {#if error}<div transition:panel><Notice tone="bad">{error}</Notice></div>{/if}

  {#key step}
    <div in:fly={{ x: 12, duration: dur(180), easing: cubicOut }} class="step-body">
      {#if step === 0}
        <Card title="Your first client">
          <p class="lead secondary">
            Alerts are grouped by client: the business whose systems are being watched. Use your own name for your own systems. Each client has projects (a
            website, an app, a job server).
          </p>
          <form
            class="form"
            onsubmit={(e) => {
              e.preventDefault()
              createFirst()
            }}
          >
            <Input bind:value={clientName} placeholder="Client, e.g. Acme Motors" aria-label="Client name" maxlength={80} required />
            <Input bind:value={projectName} placeholder="Project, e.g. Dealer website" aria-label="Project name" maxlength={80} required />
            <div class="actions"><Button type="submit" disabled={busy || !clientName.trim() || !projectName.trim()}>Create</Button></div>
          </form>
        </Card>
      {:else if step === 1 && project}
        <Card title="Connect a service">
          <p class="lead secondary">
            The hub does not watch anything itself: Sentry, Healthchecks, Uptime Kuma, GitHub Actions, Laravel Nightwatch or any service with webhooks sends its
            alerts here. Add an intake for one of them now, or skip and do it later under Clients.
          </p>
          <IntakesPanel projectId={project.id} />
          <p class="lead secondary">Sending from your own code instead? This project's API key:</p>
          <CodeBlock code={project.api_key} />
        </Card>
        <div class="nav"><span class="spacer"></span><Button onclick={() => (step = 2)}>Next</Button></div>
      {:else if step === 2}
        <Card title="Put the app on your phone">
          {#if insecure}
            <Notice tone="warn">This page is not on HTTPS, so phones cannot install the app or receive notifications. Open the hub through its HTTPS address.</Notice>
          {/if}
          <div class="phone">
            <QR data={origin} size={180} />
            <div>
              <p class="lead secondary">Scan this with your phone's camera, or open <span class="mono">{origin}</span>, and sign in.</p>
              <ol class="howto">
                <li><b>Android</b> (Chrome): menu ⋮ → <b>Add to Home screen</b> → Install.</li>
                <li><b>iPhone</b> (Safari, iOS 16.4 or later): Share → <b>Add to Home Screen</b>.</li>
                <li>Open the app from the home screen, go to <b>Devices</b>, tap <b>Turn on notifications</b> and allow them.</li>
              </ol>
              <p class="lead secondary">Doing this on the phone right now? Use the card below.</p>
            </div>
          </div>
        </Card>
        <ThisDevice onchange={refresh} />
        <div class="nav">
          <span class="caption muted">{status?.pushable_devices ?? 0} device{status?.pushable_devices === 1 ? '' : 's'} ready for push</span>
          <span class="spacer"></span><Button onclick={() => (step = 3)}>Next</Button>
        </div>
      {:else if step === 3}
        <Card title="Send a test push">
          <p class="lead secondary">This sends a test alert to every device that has notifications turned on.</p>
          <div class="actions start"><Button onclick={sendTest} disabled={busy}>{busy ? 'Sending' : 'Send test push'}</Button></div>
          {#if testMsg}<div transition:panel style="margin-top: 12px"><Notice tone={testOk ? 'good' : 'warn'}>{testMsg}</Notice></div>{/if}
        </Card>
        <div class="nav"><span class="spacer"></span><Button variant={testOk ? 'primary' : 'secondary'} onclick={finish}>{testOk ? 'Go to alerts' : 'Finish without a test'}</Button></div>
      {/if}
    </div>
  {/key}
</div>

<style>
  .setup { display: flex; flex-direction: column; gap: var(--up-space-5); }
  .step-body { display: flex; flex-direction: column; gap: var(--up-space-5); }
  .hero { padding: var(--up-space-6) 0 0; display: flex; flex-direction: column; gap: 6px; }
  h1 { font: var(--up-type-metric); letter-spacing: -0.01em; }
  .hero p { font: var(--up-type-status-line); }
  .steps { list-style: none; margin: 0; padding: 0; display: flex; gap: var(--up-space-5); border-bottom: 1px solid var(--up-border-hairline); overflow-x: auto; }
  .steps button { background: none; border: none; cursor: pointer; padding: 0 0 10px; font: var(--up-type-ui); color: var(--up-text-muted); display: inline-flex; gap: 8px; align-items: center; white-space: nowrap; }
  .steps button:disabled { cursor: default; opacity: 0.6; }
  .steps .active button { color: var(--up-ink); box-shadow: inset 0 -2px 0 var(--up-accent); }
  .steps .done button { color: var(--up-text-secondary); }
  .num { width: 18px; height: 18px; border-radius: 50%; background: var(--up-bg-hover); box-shadow: var(--up-ring-inset); font: var(--up-type-caption); display: inline-flex; align-items: center; justify-content: center; }
  .active .num { background: var(--up-accent); color: var(--up-text-on-dark); box-shadow: none; }
  .done .num { background: var(--up-operational); }
  .lead { font: var(--up-type-meta); line-height: 1.6; margin: 8px 0; }
  .form { display: flex; flex-direction: column; gap: var(--up-space-3); max-width: 420px; margin-top: 8px; }
  .howto { font: var(--up-type-meta); color: var(--up-text-secondary); line-height: 1.7; padding-left: 20px; margin: 8px 0; }
  .phone { display: flex; gap: var(--up-space-5); align-items: flex-start; }
  .actions { display: flex; justify-content: flex-end; }
  .actions.start { justify-content: flex-start; }
  .nav { display: flex; gap: var(--up-space-3); align-items: center; }
  .spacer { flex: 1; }
  @media (max-width: 600px) { .phone { flex-direction: column; } }
</style>
