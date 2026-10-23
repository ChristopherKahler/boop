import { describe, it, expect } from 'vitest'
import { parseRoute, groupPath } from './router.svelte'

describe('parseRoute', () => {
  it('maps paths to routes', () => {
    expect(parseRoute('/')).toEqual({ name: 'alerts', params: {} })
    expect(parseRoute('/alerts/alr_abc')).toEqual({ name: 'alert', params: { id: 'alr_abc' } })
    expect(parseRoute('/events')).toEqual({ name: 'inbox', params: {} })
    expect(parseRoute('/events/evt_abc')).toEqual({ name: 'event', params: { id: 'evt_abc' } })
    expect(parseRoute('/projects/')).toEqual({ name: 'projects', params: {} })
    expect(parseRoute('/settings')).toEqual({ name: 'settings', params: {} })
    expect(parseRoute('/setup')).toEqual({ name: 'setup', params: {} })
    expect(parseRoute('/nope').name).toBe('notfound')
  })
  it('round-trips group paths with awkward fingerprints', () => {
    const path = groupPath('prj_1', 'lib/a.ex:49 KeyError?')
    expect(path).toBe('/groups/prj_1/lib%2Fa.ex%3A49%20KeyError%3F')
    expect(parseRoute(path)).toEqual({ name: 'group', params: { project: 'prj_1', fingerprint: 'lib/a.ex:49 KeyError?' } })
    expect(parseRoute('/groups/prj_1').name).toBe('notfound')
  })
})
