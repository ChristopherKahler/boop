<script lang="ts">
  import { api, type Client, type Level, type Project, type ProjectCreated } from '../lib/api'
  import { LEVEL_LABEL } from '../lib/levels'
  import { relative } from '../lib/format'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Input from '../lib/ui/Input.svelte'
  import Select from '../lib/ui/Select.svelte'
  import Switch from '../lib/ui/Switch.svelte'
  import SettingRow from '../lib/ui/SettingRow.svelte'
  import CodeBlock from '../lib/ui/CodeBlock.svelte'
  import Notice from '../lib/ui/Notice.svelte'
  import Empty from '../lib/ui/Empty.svelte'
  import ProjectIcon from '../lib/ui/ProjectIcon.svelte'
  import IconPicker from '../lib/ui/IconPicker.svelte'
  import LevelPicker from '../lib/ui/LevelPicker.svelte'
  import IntakesPanel from '../lib/ui/IntakesPanel.svelte'
  import { panel, soft, pop } from '../lib/motion'
  import Skeleton from '../lib/ui/Skeleton.svelte'

  let clients = $state<Client[]>([])
  let projects = $state<Project[]>([])
  let loaded = $state(false)
  let error = $state('')
  let newClient = $state('')
  let newProject = $state('')
  let newProjectClient = $state('')
  let creating = $state(false)
  let revealed = $state<ProjectCreated | null>(null)
  let editingClient = $state<string | null>(null)
  let editingProject = $state<string | null>(null)
  let confirmDelete = $state<string | null>(null)

  async function load() {
    try {
      const [c, p] = await Promise.all([api.clients(), api.projects()])
      clients = c.clients
      projects = p.projects
      if (!newProjectClient || !clients.some((x) => x.id === newProjectClient)) newProjectClient = clients[0]?.id ?? ''
    } catch (e: any) {
      error = e.message
    } finally {
      loaded = true
    }
  }
  $effect(() => {
    load()
  })

  async function run(fn: () => Promise<unknown>) {
    error = ''
    try {
      await fn()
    } catch (e: any) {
      error = e.message
    }
  }

  async function createClient() {
    if (!newClient.trim()) return
    creating = true
    await run(async () => {
      const c = await api.createClient({ name: newClient.trim() })
      newClient = ''
      newProjectClient = c.id
      await load()
    })
    creating = false
  }

  async function createProject() {
    if (!newProject.trim()) return
    creating = true
    await run(async () => {
      revealed = await api.createProject({ name: newProject.trim(), client_id: newProjectClient || undefined })
      newProject = ''
      await load()
    })
    creating = false
  }

  const patchClient = (c: Client, patch: Partial<Client>) =>
    run(async () => {
      await api.updateClient(c.id, patch)
      await load() // inheriting projects change too
    })

  const patchProject = (p: Project, patch: Partial<Project>) =>
    run(async () => {
      const updated = await api.updateProject(p.id, patch)
      projects = projects.map((x) => (x.id === p.id ? updated : x))
      if (patch.client_id) await load()
    })

  const removeClient = (c: Client) =>
    run(async () => {
      await api.deleteClient(c.id)
      confirmDelete = null
      await load()
    })

  const removeProject = (p: Project) =>
    run(async () => {
      await api.deleteProject(p.id)
      confirmDelete = null
      await load()
    })

  const rotate = (p: Project) => run(async () => (revealed = await api.rotateKey(p.id)))

  function levelsText(set: Level[]): string {
    return set.length ? set.map((l) => LEVEL_LABEL[l].toLowerCase()).join(', ') : 'nothing'
  }

  const clientOptions = $derived(clients.map((c) => ({ value: c.id, label: c.name })))
  const origin = typeof location !== 'undefined' ? location.origin : 'https://boop.example.com'
</script>

<div class="stack">
  {#if error}<div transition:panel><Notice tone="bad">{error}</Notice></div>{/if}

  {#if revealed}
    <div transition:panel>
      <Card title="API key for {revealed.name}">
        <p class="secondary lead">Copy this key now. It is shown once and only a hash is stored.</p>
        <CodeBlock code={revealed.api_key} />
        <p class="muted lead" style="margin-top: 16px">Send a test alert:</p>
        <CodeBlock
          code={`curl ${origin}/api/v1/events \\\n  -H "Authorization: Bearer ${revealed.api_key}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"title": "Hello from ${revealed.name}", "level": "error", "fingerprint": "hello"}'`}
        />
        <div class="actions"><Button variant="secondary" onclick={() => (revealed = null)}>Done</Button></div>
      </Card>
    </div>
  {/if}

  <div class="two">
    <Card title="New client">
      <form
        class="new"
        onsubmit={(e) => {
          e.preventDefault()
          createClient()
        }}
      >
        <Input bind:value={newClient} placeholder="Client name" aria-label="Client name" maxlength={80} required />
        <Button type="submit" disabled={creating || !newClient.trim()}>Add</Button>
      </form>
      <p class="muted caption hint">A client groups its projects, names them on your phone and gets its own monthly summary.</p>
    </Card>
    <Card title="New project">
      <form
        class="new"
        onsubmit={(e) => {
          e.preventDefault()
          createProject()
        }}
      >
        <Input bind:value={newProject} placeholder="Project name" aria-label="Project name" maxlength={80} required />
        <Select bind:value={newProjectClient} options={clientOptions} aria-label="Client" />
        <Button type="submit" disabled={creating || !newProject.trim() || !newProjectClient}>Create</Button>
      </form>
      <p class="muted caption hint">Each project gets its own API key and intake addresses.</p>
    </Card>
  </div>

  {#if !loaded}
    <Card><Skeleton lines={3} height={13} widths={['30%', '60%', '45%']} /></Card>
  {:else if clients.length === 0}
    <Card><Empty title="No clients yet">Add one above, then create its projects.</Empty></Card>
  {/if}

  {#each clients as c (c.id)}
    {@const own = projects.filter((p) => p.client_id === c.id)}
    <div in:soft>
      <Card>
        <div class="chead">
          <div class="ctext">
            <div class="cname"><span class="n">{c.name}</span><span class="mono muted caption">{c.slug}</span></div>
            <div class="muted caption">Pushes {levelsText(c.push_levels)}{c.push_recoveries ? ' · recoveries push' : ''} · {c.project_count} project{c.project_count === 1 ? '' : 's'}</div>
          </div>
          <Button variant="ghost" size="sm" onclick={() => (editingClient = editingClient === c.id ? null : c.id)}>{editingClient === c.id ? 'Close' : 'Routing'}</Button>
        </div>

        {#if editingClient === c.id}
          <div class="edit" transition:panel>
            <SettingRow label="Name">
              <Input value={c.name} onchange={(e) => patchClient(c, { name: (e.currentTarget as HTMLInputElement).value })} style="width: 200px" />
            </SettingRow>
            <SettingRow label="Levels that push" hint="Lit levels push to your phone. The rest only land in the inbox. Projects use this unless they set their own.">
              <LevelPicker value={c.push_levels} onchange={(v) => patchClient(c, { push_levels: v })} />
            </SettingRow>
            <SettingRow label="Push recoveries" hint="When an alert that pushed recovers on its own, send a “Resolved” push.">
              <Switch checked={c.push_recoveries} onchange={(v) => patchClient(c, { push_recoveries: v })} label="Push recoveries" />
            </SettingRow>
            <SettingRow label="Delete client" hint={c.project_count ? 'Move or delete its projects first.' : 'Removes the client.'}>
              {#if confirmDelete === c.id}
                <div class="row" in:pop>
                  <Button variant="secondary" size="sm" onclick={() => (confirmDelete = null)}>Cancel</Button>
                  <Button variant="danger" size="sm" onclick={() => removeClient(c)}>Confirm delete</Button>
                </div>
              {:else}
                <Button variant="danger" size="sm" disabled={c.project_count > 0} onclick={() => (confirmDelete = c.id)}>Delete</Button>
              {/if}
            </SettingRow>
          </div>
        {/if}

        <div class="plist">
          {#if own.length === 0}
            <p class="muted caption">No projects yet.</p>
          {/if}
          {#each own as p (p.id)}
            <div class="project">
              <div class="phead">
                <ProjectIcon icon={p.icon} size={18} />
                <div class="ptext">
                  <div class="pname"><span class="pn">{p.name}</span><span class="mono muted caption">{p.slug}</span></div>
                  <div class="pmeta muted caption">
                    {#if !p.notify}notifications off{:else}pushes {levelsText(p.effective_push_levels)}{p.push_levels ? ' (own routing)' : ''}{/if} · created {relative(p.created_at)}
                  </div>
                </div>
                <Button variant="ghost" size="sm" onclick={() => (editingProject = editingProject === p.id ? null : p.id)}>{editingProject === p.id ? 'Close' : 'Settings'}</Button>
              </div>
              {#if editingProject === p.id}
                <div class="edit" transition:panel>
                  <SettingRow label="Name">
                    <Input value={p.name} onchange={(e) => patchProject(p, { name: (e.currentTarget as HTMLInputElement).value })} style="width: 200px" />
                  </SettingRow>
                  <SettingRow label="Client">
                    <Select value={p.client_id} options={clientOptions} onchange={(e) => patchProject(p, { client_id: (e.currentTarget as HTMLSelectElement).value })} style="width: 200px" />
                  </SettingRow>
                  <SettingRow label="Icon" hint="Shown next to the project name in the inbox.">
                    <IconPicker value={p.icon} onchange={(v) => patchProject(p, { icon: v })} />
                  </SettingRow>
                  <SettingRow label="Push notifications" hint="Turn off to keep everything in the inbox.">
                    <Switch checked={p.notify} onchange={(v) => patchProject(p, { notify: v })} label="Push notifications" />
                  </SettingRow>
                  <SettingRow label="Use the client's routing" hint="Turn off to choose this project's own levels.">
                    <Switch checked={p.push_levels === null} onchange={(v) => patchProject(p, { push_levels: v ? null : [...c.push_levels] })} label="Use the client's routing" />
                  </SettingRow>
                  {#if p.push_levels !== null}
                    <SettingRow label="Levels that push">
                      <LevelPicker value={p.push_levels} onchange={(v) => patchProject(p, { push_levels: v })} />
                    </SettingRow>
                  {/if}
                  <div class="intake-block">
                    <div class="setting">Intakes</div>
                    <p class="muted caption">Addresses that Sentry, Healthchecks, Uptime Kuma, GitHub, Nightwatch or any other service post to. Problems open alerts; recoveries resolve them.</p>
                    <IntakesPanel projectId={p.id} />
                  </div>
                  <SettingRow label="API key" hint="For sending events from your own code (POST /api/v1/events). Rotating invalidates the current key.">
                    <Button variant="secondary" size="sm" onclick={() => rotate(p)}>Rotate key</Button>
                  </SettingRow>
                  <SettingRow label="Delete project" hint="Removes the project, its alerts and every event it received.">
                    {#if confirmDelete === p.id}
                      <div class="row" in:pop>
                        <Button variant="secondary" size="sm" onclick={() => (confirmDelete = null)}>Cancel</Button>
                        <Button variant="danger" size="sm" onclick={() => removeProject(p)}>Confirm delete</Button>
                      </div>
                    {:else}
                      <Button variant="danger" size="sm" onclick={() => (confirmDelete = p.id)}>Delete</Button>
                    {/if}
                  </SettingRow>
                </div>
              {/if}
            </div>
          {/each}
        </div>
      </Card>
    </div>
  {/each}
</div>

<style>
  .lead { font: var(--up-type-meta); line-height: 1.6; margin-bottom: 12px; }
  .actions { display: flex; justify-content: flex-end; margin-top: var(--up-space-4); }
  .two { display: grid; grid-template-columns: 1fr 1fr; gap: var(--up-space-4); }
  .new { display: flex; gap: var(--up-space-3); align-items: center; flex-wrap: wrap; }
  .new :global(input:first-child) { flex: 1; min-width: 140px; }
  .hint { margin-top: 8px; }
  .chead, .phead { display: flex; align-items: center; gap: 12px; }
  .ctext, .ptext { flex: 1; min-width: 0; }
  .cname, .pname { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .n { font: var(--up-type-setting); }
  .pn { font: var(--up-type-row-title); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .pmeta { margin-top: 1px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .edit { margin-top: var(--up-space-4); }
  .plist { margin-top: var(--up-space-4); padding-top: var(--up-space-3); border-top: 1px solid var(--up-border-hairline); display: flex; flex-direction: column; gap: var(--up-space-3); }
  .intake-block { display: flex; flex-direction: column; gap: 6px; padding: 14px 0; border-top: 1px solid var(--up-border-hairline); }
  .project + .project { padding-top: var(--up-space-3); border-top: 1px solid var(--up-border-hairline); }
  @media (max-width: 720px) {
    .two { grid-template-columns: 1fr; }
  }
</style>
