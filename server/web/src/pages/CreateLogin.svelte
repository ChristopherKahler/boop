<script lang="ts">
  import { api } from '../lib/api'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Input from '../lib/ui/Input.svelte'
  import Notice from '../lib/ui/Notice.svelte'
  import { panel } from '../lib/motion'

  // A fresh install has no login yet. The server printed a one-time setup code
  // in its log; whoever has it creates the login.
  let { onsuccess }: { onsuccess: () => void } = $props()
  let code = $state('')
  let username = $state('')
  let password = $state('')
  let confirm = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit() {
    error = ''
    if (password !== confirm) {
      error = 'The two passwords do not match.'
      return
    }
    busy = true
    try {
      await api.setupLogin(code, username.trim(), password)
      onsuccess()
    } catch (e: any) {
      error = e.message
    } finally {
      busy = false
    }
  }
</script>

<div class="create">
  <Card title="Create your login">
    <p class="secondary lead">
      This hub has no login yet. Enter the <b>setup code</b> from the server's log (the line <span class="mono">auth.setup_required</span>), then choose a user
      name and password. You will use them on the web and in the phone app.
    </p>
    <form
      onsubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      <Input bind:value={code} placeholder="Setup code, e.g. K7Q2-M9XD" aria-label="Setup code" autocomplete="one-time-code" mono required />
      <Input bind:value={username} placeholder="User name" aria-label="User name" autocomplete="username" maxlength={60} required />
      <Input bind:value={password} placeholder="Password (10+ characters)" aria-label="Password" type="password" autocomplete="new-password" required />
      <Input bind:value={confirm} placeholder="Password again" aria-label="Password again" type="password" autocomplete="new-password" required />
      {#if error}<div transition:panel><Notice tone="bad">{error}</Notice></div>{/if}
      <Button type="submit" disabled={busy || !code || !username.trim() || password.length < 10}>{busy ? 'Creating' : 'Create login'}</Button>
    </form>
  </Card>
</div>

<style>
  .create { max-width: 420px; margin: var(--up-space-7) auto 0; }
  .lead { font: var(--up-type-meta); line-height: 1.6; margin-bottom: 12px; }
  form { display: flex; flex-direction: column; gap: var(--up-space-3); }
</style>
