<script lang="ts">
  import { api, type Intake, type IntakeCreated, type IntakeKind, type MappedEvent } from '../api'
  import { relative } from '../format'
  import Button from './Button.svelte'
  import Select from './Select.svelte'
  import Input from './Input.svelte'
  import CodeBlock from './CodeBlock.svelte'
  import Notice from './Notice.svelte'
  import LevelBadge from './LevelBadge.svelte'
  import { panel, pop } from '../motion'

  // Webhook addresses that monitoring services post to for one project.
  let { projectId }: { projectId: string } = $props()

  const KINDS: { value: IntakeKind; label: string }[] = [
    { value: 'sentry', label: 'Sentry' },
    { value: 'healthchecks', label: 'Healthchecks' },
    { value: 'uptime-kuma', label: 'Uptime Kuma' },
    { value: 'github', label: 'GitHub Actions' },
    { value: 'nightwatch', label: 'Laravel Nightwatch' },
    { value: 'generic', label: 'Any other service (JSON mapping)' },
  ]
  const LABEL = Object.fromEntries(KINDS.map((k) => [k.value, k.label])) as Record<IntakeKind, string>
  const SIGNED: IntakeKind[] = ['sentry', 'nightwatch', 'generic']

  const SAMPLE_MAPPING = JSON.stringify(
    {
      title: '{{alert.name}}',
      body: '{{alert.description}}',
      level: '{{alert.severity}}',
      level_map: { p1: 'critical', p2: 'error', p3: 'warning' },
      fingerprint: '{{alert.id}}',
      resolve_when: { field: 'status', equals: ['resolved', 'ok'] },
      url: '{{alert.link}}',
    },
    null,
    2,
  )
  const SAMPLE_PAYLOAD = JSON.stringify({ status: 'firing', alert: { id: 'disk-db-1', name: 'Disk almost full', severity: 'p1', description: '94% used on db-1', link: 'https://example.com/alerts/1' } }, null, 2)

  let intakes = $state<Intake[]>([])
  let error = $state('')
  let kind = $state<IntakeKind>('uptime-kuma')
  let name = $state('')
  let secret = $state('')
  let mapping = $state(SAMPLE_MAPPING)
  let payload = $state(SAMPLE_PAYLOAD)
  let preview = $state<{ events: MappedEvent[]; ignored: string } | null>(null)
  let created = $state<IntakeCreated | null>(null)
  let adding = $state(false)
  let busy = $state(false)
  let confirmDelete = $state<string | null>(null)
  let editingSecret = $state<string | null>(null)
  let newSecret = $state('')

  async function load() {
    try {
      intakes = (await api.intakes(projectId)).intakes
    } catch (e: any) {
      error = e.message
    }
  }
  $effect(() => {
    projectId
    load()
  })

  function parsed(text: string, what: string): unknown {
    try {
      return JSON.parse(text)
    } catch (e: any) {
      throw new Error(`${what} is not valid JSON: ${e.message}`)
    }
  }

  async function run(fn: () => Promise<void>) {
    busy = true
    error = ''
    try {
      await fn()
    } catch (e: any) {
      error = e.message
    } finally {
      busy = false
    }
  }

  const tryPreview = () =>
    run(async () => {
      preview = await api.previewIntake('generic', parsed(payload, 'The sample payload'), parsed(mapping, 'The mapping'))
    })

  const create = () =>
    run(async () => {
      created = await api.createIntake(projectId, {
        kind,
        name: name.trim() || undefined,
        signing_secret: SIGNED.includes(kind) && secret.trim() ? secret.trim() : undefined,
        mapping: kind === 'generic' ? parsed(mapping, 'The mapping') : undefined,
      })
      adding = false
      name = ''
      secret = ''
      preview = null
      await load()
    })

  const rotate = (i: Intake) =>
    run(async () => {
      created = await api.rotateIntake(i.id)
      await load()
    })

  const remove = (i: Intake) =>
    run(async () => {
      await api.deleteIntake(i.id)
      confirmDelete = null
      await load()
    })

  const saveSecret = (i: Intake) =>
    run(async () => {
      await api.updateIntake(i.id, { signing_secret: newSecret.trim() })
      editingSecret = null
      newSecret = ''
      await load()
    })

  function tokenOf(url: string): string {
    return url.slice(url.lastIndexOf('/') + 1)
  }
</script>

<div class="intakes">
  {#if error}<div transition:panel><Notice tone="bad">{error}</Notice></div>{/if}

  {#if created}
    <div class="created" transition:panel>
      <p class="secondary lead">Address for <b>{LABEL[created.kind]}</b>. Copy it now: it is shown once and only a hash is kept.</p>
      <CodeBlock code={created.url} wrap />
      <div class="howto caption">
        {#if created.kind === 'sentry'}
          In Sentry: <b>Settings → Developer Settings → Create New Integration → Internal Integration</b>. Webhook URL: the address above. Turn on
          <b>Alert Rule Action</b> and, under Webhooks, tick <b>issue</b>. Save, then copy its <b>Client Secret</b> into “Signing secret” here. Add the integration
          as an action in your alert rules.
        {:else if created.kind === 'healthchecks'}
          In Healthchecks: <b>Integrations → Webhook</b>. For both “down” and “up”: method <b>POST</b>, the address above, header
          <code>Content-Type: application/json</code>, and body:
          <CodeBlock code={'{"status": "$STATUS", "now": "$NOW", "check": $JSON}'} />
        {:else if created.kind === 'uptime-kuma'}
          In Uptime Kuma: <b>Settings → Notifications → Setup Notification</b>, type <b>Webhook</b>, Post URL: the address above, Request Body:
          <b>application/json</b>. Then turn the notification on for your monitors.
        {:else if created.kind === 'github'}
          In GitHub: the repository's <b>Settings → Webhooks → Add webhook</b>. Payload URL: the address above. Content type: <b>application/json</b>. Secret:
          <code class="mono">{tokenOf(created.url)}</code>. Events: <b>Let me select individual events → Workflow runs</b>.
        {:else if created.kind === 'nightwatch'}
          In Nightwatch: the application's <b>Settings → Issues → Webhooks</b>. URL: the address above. Copy the webhook's <b>signing secret</b> into “Signing
          secret” here.
        {:else}
          Point the service's webhook at the address above. Each request becomes an event using this intake's mapping.
        {/if}
      </div>
      <div class="row end"><Button variant="secondary" size="sm" onclick={() => (created = null)}>Done</Button></div>
    </div>
  {/if}

  {#if intakes.length === 0 && !adding}
    <p class="muted caption">No intakes yet. Add one for each service that watches this project.</p>
  {/if}
  {#each intakes as i (i.id)}
    <div class="intake">
      <div class="ihead">
        <div class="itext">
          <div class="iname">{i.name} <span class="muted caption">{LABEL[i.kind]} · …{i.token_hint}</span></div>
          <div class="caption muted">
            {#if i.last_received_at}{i.received_count} received · last {relative(i.last_received_at)} · {i.last_result}{:else}Nothing received yet{/if}
            {#if SIGNED.includes(i.kind)} · {i.signing_secret_set ? 'signature checked' : 'no signing secret'}{/if}
          </div>
        </div>
        <div class="row">
          {#if SIGNED.includes(i.kind)}
            <Button variant="ghost" size="sm" onclick={() => ((editingSecret = editingSecret === i.id ? null : i.id), (newSecret = ''))}>Secret</Button>
          {/if}
          <Button variant="ghost" size="sm" disabled={busy} onclick={() => rotate(i)}>New address</Button>
          {#if confirmDelete === i.id}
            <span in:pop class="row">
              <Button variant="secondary" size="sm" onclick={() => (confirmDelete = null)}>Cancel</Button>
              <Button variant="danger" size="sm" onclick={() => remove(i)}>Confirm</Button>
            </span>
          {:else}
            <Button variant="danger" size="sm" onclick={() => (confirmDelete = i.id)}>Delete</Button>
          {/if}
        </div>
      </div>
      {#if editingSecret === i.id}
        <form
          class="row secret"
          transition:panel
          onsubmit={(e) => {
            e.preventDefault()
            saveSecret(i)
          }}
        >
          <Input bind:value={newSecret} type="password" placeholder="Signing secret from the service (empty to remove)" aria-label="Signing secret" style="flex: 1" />
          <Button size="sm" type="submit" disabled={busy}>Save</Button>
        </form>
      {/if}
    </div>
  {/each}

  {#if adding}
    <div class="add" transition:panel>
      <div class="row wrap">
        <Select bind:value={kind} options={KINDS} aria-label="Service" />
        <Input bind:value={name} placeholder="Name (optional)" aria-label="Intake name" maxlength={80} style="width: 180px" />
      </div>
      {#if SIGNED.includes(kind)}
        <Input bind:value={secret} type="password" placeholder="Signing secret (optional; you can add it later)" aria-label="Signing secret" />
      {/if}
      {#if kind === 'generic'}
        <p class="caption muted">
          Mapping: each field is a template where <code>{'{{path}}'}</code> is a value from the payload (<code>{'{{a.b.0.c}}'}</code>, <code>{'{{path|fallback}}'}</code>).
          Add <code>"items": "alerts"</code> when one request carries a list of alerts.
        </p>
        <div class="two">
          <label class="caption muted">Mapping<textarea class="mono" rows="12" bind:value={mapping} spellcheck="false"></textarea></label>
          <label class="caption muted">Sample payload<textarea class="mono" rows="12" bind:value={payload} spellcheck="false"></textarea></label>
        </div>
        <div class="row"><Button variant="secondary" size="sm" disabled={busy} onclick={tryPreview}>Preview</Button></div>
        {#if preview}
          <div class="preview" transition:panel>
            {#if preview.events.length === 0}<p class="caption muted">Nothing would be stored: {preview.ignored}</p>{/if}
            {#each preview.events as ev, n (n)}
              <div class="pv">
                <LevelBadge level={ev.level ?? 'error'} />
                <div class="pvt">
                  <div>{ev.title}{#if ev.resolve} <span class="caption muted">(resolves)</span>{/if}</div>
                  <div class="caption muted">{ev.body ?? ''} · fingerprint <span class="mono">{ev.fingerprint}</span></div>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      {/if}
      <div class="row end">
        <Button variant="secondary" size="sm" onclick={() => (adding = false)}>Cancel</Button>
        <Button size="sm" disabled={busy} onclick={create}>Create intake</Button>
      </div>
    </div>
  {:else}
    <div><Button variant="secondary" size="sm" onclick={() => ((adding = true), (created = null))}>Add intake</Button></div>
  {/if}
</div>

<style>
  .intakes { display: flex; flex-direction: column; gap: var(--up-space-3); width: 100%; }
  .lead { font: var(--up-type-meta); line-height: 1.6; margin-bottom: 8px; }
  .created, .add { display: flex; flex-direction: column; gap: 10px; padding: 12px; border-radius: var(--up-radius-control); background: var(--up-bg-hover); }
  .howto { line-height: 1.7; color: var(--up-text-secondary); }
  .intake { padding: 8px 0; border-top: 1px solid var(--up-border-hairline); }
  .ihead { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
  .itext { flex: 1; min-width: 200px; }
  .iname { font: var(--up-type-meta); color: var(--up-ink); }
  .row { display: flex; gap: 6px; align-items: center; }
  .row.wrap { flex-wrap: wrap; }
  .row.end { justify-content: flex-end; }
  .secret { margin-top: 8px; }
  .two { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  label { display: flex; flex-direction: column; gap: 4px; }
  textarea { font: var(--up-type-code); border: 1px solid var(--up-border-control); border-radius: var(--up-radius-control); padding: 8px; background: var(--up-bg); color: var(--up-ink); resize: vertical; }
  .preview { display: flex; flex-direction: column; gap: 6px; }
  .pv { display: flex; gap: 10px; align-items: flex-start; font: var(--up-type-meta); }
  .pvt { min-width: 0; }
  code { font: var(--up-type-code); }
  @media (max-width: 640px) { .two { grid-template-columns: 1fr; } }
</style>
