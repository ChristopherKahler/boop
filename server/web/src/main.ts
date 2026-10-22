import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'
import { router } from './lib/router.svelte'

const app = mount(App, { target: document.getElementById('app')! })

// The service worker shows push notifications and opens their alert when
// tapped; when the app is already open it asks us to route there instead.
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {})
  navigator.serviceWorker.addEventListener('message', (e) => {
    if (e.data?.type === 'navigate' && typeof e.data.path === 'string' && e.data.path.startsWith('/')) router.navigate(e.data.path)
  })
}

export default app
