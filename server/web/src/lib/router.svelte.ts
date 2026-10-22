// Minimal history-based router. The Go server serves index.html for any
// unknown path, so deep links work.

export interface Route {
  name: 'alerts' | 'alert' | 'summary' | 'inbox' | 'event' | 'group' | 'projects' | 'devices' | 'settings' | 'setup' | 'notfound'
  params: Record<string, string>
}

function parse(path: string): Route {
  const p = path.replace(/\/+$/, '') || '/'
  if (p === '/') return { name: 'alerts', params: {} }
  const al = p.match(/^\/alerts\/([^/]+)$/)
  if (al) return { name: 'alert', params: { id: decodeURIComponent(al[1]) } }
  if (p === '/events') return { name: 'inbox', params: {} }
  const sm = p.match(/^\/clients\/([^/]+)\/summary$/)
  if (sm) return { name: 'summary', params: { id: decodeURIComponent(sm[1]) } }
  const ev = p.match(/^\/events\/([^/]+)$/)
  if (ev) return { name: 'event', params: { id: decodeURIComponent(ev[1]) } }
  const grp = p.match(/^\/groups\/([^/]+)\/([^/]+)$/)
  if (grp) return { name: 'group', params: { project: decodeURIComponent(grp[1]), fingerprint: decodeURIComponent(grp[2]) } }
  if (p === '/projects') return { name: 'projects', params: {} }
  if (p === '/devices') return { name: 'devices', params: {} }
  if (p === '/settings') return { name: 'settings', params: {} }
  if (p === '/setup') return { name: 'setup', params: {} }
  return { name: 'notfound', params: {} }
}

const state = $state({ path: typeof location !== 'undefined' ? location.pathname : '/' })

export const router = {
  get path() {
    return state.path
  },
  get route(): Route {
    return parse(state.path)
  },
  navigate(path: string, replace = false) {
    if (path === state.path) return
    if (replace) history.replaceState(null, '', path)
    else history.pushState(null, '', path)
    state.path = path
    window.scrollTo(0, 0)
  },
}

if (typeof window !== 'undefined') {
  window.addEventListener('popstate', () => {
    state.path = location.pathname
  })
}

/** Click handler for <a> elements: intercept plain left-clicks. */
export function link(e: MouseEvent) {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
  const a = (e.currentTarget as HTMLAnchorElement) ?? null
  const href = a?.getAttribute('href')
  if (!href || !href.startsWith('/')) return
  e.preventDefault()
  router.navigate(href)
}

/** Path of the occurrences page for a fingerprint within a project. */
export function groupPath(project: string, fingerprint: string): string {
  return `/groups/${encodeURIComponent(project)}/${encodeURIComponent(fingerprint)}`
}

export { parse as parseRoute }
