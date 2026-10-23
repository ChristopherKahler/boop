// Web Push on this device: subscribe the installed app (or this browser) so
// alerts arrive as phone notifications.

import { api } from './api'

export type PushState =
  | 'unsupported' // this browser cannot do web push
  | 'needs-install' // iPhone/iPad: push only works once added to the home screen
  | 'server-off' // the hub has no web push keys
  | 'denied' // notifications blocked for this site
  | 'off' // possible, not turned on yet
  | 'on'

const NAME_KEY = 'boop.push.name'

export function isIOS(): boolean {
  return /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
}

export function isStandalone(): boolean {
  return window.matchMedia?.('(display-mode: standalone)').matches || (navigator as any).standalone === true
}

export function defaultDeviceName(): string {
  try {
    const saved = localStorage.getItem(NAME_KEY)
    if (saved) return saved
  } catch {
    /* private mode */
  }
  const ua = navigator.userAgent
  if (/Android/.test(ua)) return 'Android phone'
  if (isIOS()) return /iPad/.test(ua) ? 'iPad' : 'iPhone'
  if (/Windows/.test(ua)) return 'Windows browser'
  if (/Mac OS X/.test(ua)) return 'Mac browser'
  return 'Browser'
}

function supported(): boolean {
  return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
}

async function registration(): Promise<ServiceWorkerRegistration> {
  const existing = await navigator.serviceWorker.getRegistration('/')
  if (existing) return existing
  await navigator.serviceWorker.register('/sw.js')
  return navigator.serviceWorker.ready
}

/** What this device can do right now, and its subscription when on. */
export async function pushState(): Promise<{ state: PushState; endpoint?: string; error?: string }> {
  if (!supported()) return { state: isIOS() && !isStandalone() ? 'needs-install' : 'unsupported' }
  if (isIOS() && !isStandalone()) return { state: 'needs-install' }
  const info = await api.webpush()
  if (!info.configured) return { state: 'server-off', error: info.error }
  if (Notification.permission === 'denied') return { state: 'denied' }
  const reg = await registration()
  const sub = await reg.pushManager.getSubscription()
  return sub ? { state: 'on', endpoint: sub.endpoint } : { state: 'off' }
}

function keyBytes(s: string): Uint8Array<ArrayBuffer> {
  const pad = '='.repeat((4 - (s.length % 4)) % 4)
  const raw = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'))
  return Uint8Array.from(raw, (c) => c.charCodeAt(0))
}

/** Ask for permission, subscribe, and register this device with the hub. */
export async function enablePush(name: string): Promise<void> {
  const info = await api.webpush()
  if (!info.configured || !info.public_key) throw new Error('Web push is not configured on the hub.')
  const perm = await Notification.requestPermission()
  if (perm !== 'granted') throw new Error('Notifications were not allowed. Allow them for this site in the browser settings, then try again.')
  const reg = await registration()
  let sub = await reg.pushManager.getSubscription()
  // A subscription made with an older key cannot receive our pushes.
  const key = sub?.options?.applicationServerKey
  if (sub && key && btoaUrl(new Uint8Array(key)) !== info.public_key.replace(/=+$/, '')) {
    await sub.unsubscribe()
    sub = null
  }
  if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(info.public_key) })
  await api.subscribePush(sub.toJSON(), name)
  try {
    localStorage.setItem(NAME_KEY, name)
  } catch {
    /* private mode */
  }
}

/** Unsubscribe this device and forget it on the hub. */
export async function disablePush(): Promise<void> {
  const reg = await registration()
  const sub = await reg.pushManager.getSubscription()
  if (!sub) return
  await api.unsubscribePush(sub.endpoint).catch(() => {})
  await sub.unsubscribe()
}

function btoaUrl(b: Uint8Array): string {
  return btoa(String.fromCharCode(...b)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}
