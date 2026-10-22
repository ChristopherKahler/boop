<script lang="ts">
  import { api } from '../lib/api'
  import Card from '../lib/ui/Card.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Input from '../lib/ui/Input.svelte'
  import Notice from '../lib/ui/Notice.svelte'
  import { panel } from '../lib/motion'

  let { onsuccess }: { onsuccess: () => void } = $props()
  let username = $state('')
  let password = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit() {
    busy = true
    error = ''
    try {
      await api.login(username, password)
      onsuccess()
    } catch (e: any) {
      error = e.status === 401 ? 'Wrong user name or password.' : e.message
      password = ''
    } finally {
      busy = false
    }
  }
</script>

<div class="login">
  <Card title="Sign in">
    <p class="secondary lead">Sign in with the hub's user name and password.</p>
    <form
      onsubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      <Input bind:value={username} placeholder="Username" aria-label="Username" autocomplete="username" required />
      <Input bind:value={password} placeholder="Password" aria-label="Password" type="password" autocomplete="current-password" required />
      {#if error}<div transition:panel><Notice tone="bad">{error}</Notice></div>{/if}
      <Button type="submit" disabled={busy || !username || !password}>{busy ? 'Signing in' : 'Sign in'}</Button>
    </form>
  </Card>
</div>

<style>
  .login { max-width: 380px; margin: var(--up-space-7) auto 0; }
  .lead { font: var(--up-type-meta); line-height: 1.6; margin-bottom: 12px; }
  form { display: flex; flex-direction: column; gap: var(--up-space-3); }
</style>
