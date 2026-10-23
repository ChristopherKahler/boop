// Thin typed client for the Boop API. Admin endpoints are unauthenticated in v1.

export type Level = 'info' | 'success' | 'warning' | 'error' | 'critical'
export const LEVELS: Level[] = ['info', 'success', 'warning', 'error', 'critical']

export interface Client {
  id: string
  name: string
  slug: string
  /** Levels that push to the phone; the rest only land in the inbox. */
  push_levels: Level[]
  /** Push a "resolved" notification when an alert that pushed recovers. */
  push_recoveries: boolean
  project_count: number
  created_at: string
  updated_at: string
}

export interface Project {
  id: string
  name: string
  slug: string
  icon: string
  notify: boolean
  min_level: Level | ''
  client_id: string
  client_name: string
  client_slug: string
  /** null = use the client's routing. */
  push_levels: Level[] | null
  effective_push_levels: Level[]
  created_at: string
  updated_at: string
}

export type AlertStatus = 'open' | 'acknowledged' | 'resolved'

/** One problem: the occurrences sharing a fingerprint until it resolves. */
export interface Alert {
  id: string
  project_id: string
  project_name: string
  project_slug: string
  project_icon: string
  client_id: string
  client_name: string
  fingerprint: string
  source: string
  title: string
  level: Level
  status: AlertStatus
  count: number
  first_seen: string
  last_seen: string
  last_event_id?: string
  notified_at: string | null
  acknowledged_at: string | null
  acknowledged_by?: string
  resolved_at: string | null
  resolved_by?: string
  resolve_event_id?: string
}

export interface AlertPage {
  alerts: Alert[]
  next_cursor?: string
  open: number
  acknowledged: number
}

export interface ProjectCreated extends Project {
  api_key: string
}

/** A button on the notification and in the event detail that opens a URL. */
export interface EventAction {
  label: string
  url: string
}

/** Present on grouped listings: this row stands for `count` occurrences of its fingerprint. */
export interface GroupInfo {
  count: number
  first_seen: string
  last_seen: string
}

export interface Event {
  id: string
  external_id?: string
  project_id: string
  project_name: string
  project_slug: string
  project_icon: string
  client_id: string
  client_name: string
  source: string
  type: string
  level: Level
  title: string
  body: string
  fingerprint: string
  data: Record<string, unknown>
  occurred_at: string
  created_at: string
  silenced: boolean
  silence_id?: string
  actions?: EventAction[]
  group?: GroupInfo
  /** The alert this event opened, repeated or resolved. */
  alert_id?: string
  /** Why it did or did not push. */
  route?: string
}

export type IntakeKind = 'sentry' | 'healthchecks' | 'uptime-kuma' | 'github' | 'nightwatch' | 'generic'

/** A webhook address for one monitoring service on one project. */
export interface Intake {
  id: string
  project_id: string
  project_name: string
  client_name: string
  kind: IntakeKind
  name: string
  token_hint: string
  signing_secret_set: boolean
  mapping: Record<string, unknown>
  received_count: number
  last_received_at: string | null
  last_result: string
  created_at: string
}

export interface IntakeCreated extends Intake {
  token: string
  url: string
}

/** What a webhook payload turns into (the preview endpoint). */
export interface MappedEvent {
  title: string
  body?: string
  level?: Level
  fingerprint?: string
  source?: string
  resolve?: boolean
  actions?: EventAction[]
}

export interface SummaryDurations {
  count: number
  median_seconds: number
  average_seconds: number
  fastest_seconds: number
  slowest_seconds: number
}

export interface SummaryAlert {
  id: string
  project: string
  title: string
  level: Level
  source: string
  first_seen: string
  last_seen: string
  count: number
  status: 'open' | 'resolved' | 'recovered'
  time_to_acknowledge_seconds: number | null
  time_to_resolve_seconds: number | null
}

/** One client's month (GET /api/v1/clients/{id}/summary). */
export interface ClientSummary {
  client_id: string
  client_name: string
  client_slug: string
  month: string
  month_label: string
  timezone: string
  totals: { alerts: number; resolved: number; recovered: number; resolved_by_hand: number; still_open: number; acknowledged: number; occurrences: number; events: number; pushes: number }
  by_level: { level: Level; count: number }[]
  projects: { project: string; alerts: number; open: number }[]
  time_to_acknowledge: SummaryDurations
  time_to_resolve: SummaryDurations
  alerts: SummaryAlert[]
  recoveries: SummaryAlert[]
  carried_over: number
  previous_month: string
  next_month: string
  retention_note?: string
}

export type SilenceField = 'fingerprint' | 'title' | 'source'

export interface Silence {
  id: string
  project_id?: string
  project_name?: string
  field: SilenceField
  value: string
  note: string
  created_at: string
}

export interface EventPage {
  events: Event[]
  next_cursor?: string
}

export interface Device {
  id: string
  name: string
  push_registered: boolean
  platform: string
  app_bundle_id: string
  last_seen_at: string | null
  created_at: string
  updated_at: string
}

export interface Delivery {
  id: string
  event_id: string
  device_id: string
  device_name: string
  status: 'sent' | 'failed' | 'skipped'
  apns_id?: string
  error?: string
  attempted_at: string
}

export interface PairingToken {
  id: string
  token?: string
  expires_at: string
  created_at: string
  qr?: { version: number; server: string; token: string }
}

export interface Status {
  version: string
  server: string
  database: string
  database_path: string
  base_url: string
  uptime_seconds: number
  apns: {
    configured: boolean
    error?: string
    missing?: string[]
    team_id?: string
    key_id?: string
    bundle_id?: string
    environment: string
  }
  webpush: { configured: boolean; public_key?: string; error?: string }
  /** Some push channel (web push or APNs) can send. */
  push_ready: boolean
  devices: number
  pushable_devices: number
  projects: number
  events: number
  last_push: Delivery | null
  retention_days: number
  setup_completed: boolean
  admin_auth: boolean
}

export interface AuthState {
  auth_required: boolean
  authenticated: boolean
  /** A fresh install: no login exists yet (create it with the setup code). */
  needs_setup?: boolean
}

export interface Settings {
  retention_days: number
  redact_keys: string[]
  default_redact_keys: string[]
  setup_completed: boolean
  mcp_enabled: boolean
  mcp_token_set: boolean
}

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message)
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (res.status === 204) return undefined as T
  const text = await res.text()
  let parsed: any = null
  try {
    parsed = text ? JSON.parse(text) : null
  } catch {
    parsed = null
  }
  if (!res.ok) {
    if (res.status === 401 && parsed?.error === 'login_required') onLoginRequired?.()
    throw new ApiError(res.status, parsed?.error ?? 'error', parsed?.message ?? `Request failed (${res.status})`)
  }
  return parsed as T
}

/** Called on any 401 with login_required so the app can show the sign-in screen. */
export let onLoginRequired: (() => void) | null = null
export function setLoginHandler(fn: () => void) {
  onLoginRequired = fn
}

export const api = {
  me: () => request<AuthState>('GET', '/api/v1/auth/me'),
  login: (username: string, password: string) => request<AuthState>('POST', '/api/v1/auth/login', { username, password }),
  logout: () => request<void>('POST', '/api/v1/auth/logout'),
  setupLogin: (code: string, username: string, password: string) => request<AuthState>('POST', '/api/v1/auth/setup', { code, username, password }),

  status: () => request<Status>('GET', '/api/v1/status'),
  settings: () => request<Settings>('GET', '/api/v1/settings'),
  updateSettings: (patch: Partial<Settings>) => request<Settings>('PATCH', '/api/v1/settings', patch),

  events: (params: { project?: string; client?: string; alert?: string; level?: string; source?: string; fingerprint?: string; before?: string; limit?: number; silenced?: string; grouped?: boolean } = {}) => {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '' && v !== false) q.set(k, String(v))
    const qs = q.toString()
    return request<EventPage>('GET', '/api/v1/events' + (qs ? '?' + qs : ''))
  },
  event: (id: string) => request<Event>('GET', `/api/v1/events/${encodeURIComponent(id)}`),
  eventDeliveries: (id: string) => request<{ deliveries: Delivery[] }>('GET', `/api/v1/events/${encodeURIComponent(id)}/deliveries`),

  alerts: (params: { status?: string; client?: string; project?: string; level?: string; before?: string; limit?: number } = {}) => {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') q.set(k, String(v))
    const qs = q.toString()
    return request<AlertPage>('GET', '/api/v1/alerts' + (qs ? '?' + qs : ''))
  },
  alert: (id: string) => request<Alert>('GET', `/api/v1/alerts/${encodeURIComponent(id)}`),
  acknowledge: (id: string) => request<Alert>('POST', `/api/v1/alerts/${encodeURIComponent(id)}/acknowledge${byQuery()}`),
  resolve: (id: string) => request<Alert>('POST', `/api/v1/alerts/${encodeURIComponent(id)}/resolve${byQuery()}`),
  reopen: (id: string) => request<Alert>('POST', `/api/v1/alerts/${encodeURIComponent(id)}/reopen`),

  intakes: (project?: string) => request<{ intakes: Intake[]; kinds: { kind: IntakeKind; label: string }[] }>('GET', '/api/v1/intakes' + (project ? '?project=' + encodeURIComponent(project) : '')),
  createIntake: (project: string, input: { kind: IntakeKind; name?: string; signing_secret?: string; mapping?: unknown }) =>
    request<IntakeCreated>('POST', `/api/v1/projects/${project}/intakes`, input),
  updateIntake: (id: string, patch: { name?: string; signing_secret?: string; mapping?: unknown }) => request<Intake>('PATCH', `/api/v1/intakes/${id}`, patch),
  rotateIntake: (id: string) => request<IntakeCreated>('POST', `/api/v1/intakes/${id}/rotate`),
  deleteIntake: (id: string) => request<void>('DELETE', `/api/v1/intakes/${id}`),
  previewIntake: (kind: IntakeKind, payload: unknown, mapping?: unknown, headers?: Record<string, string>) =>
    request<{ events: MappedEvent[]; ignored: string }>('POST', '/api/v1/intakes/preview', { kind, payload, mapping, headers }),

  clientSummary: (id: string, month?: string) =>
    request<ClientSummary>('GET', `/api/v1/clients/${encodeURIComponent(id)}/summary` + (month ? '?month=' + encodeURIComponent(month) : '')),

  clients: () => request<{ clients: Client[] }>('GET', '/api/v1/clients'),
  createClient: (input: { name: string; push_levels?: Level[]; push_recoveries?: boolean }) => request<Client>('POST', '/api/v1/clients', input),
  updateClient: (id: string, patch: Partial<Pick<Client, 'name' | 'push_levels' | 'push_recoveries'>>) => request<Client>('PATCH', `/api/v1/clients/${id}`, patch),
  deleteClient: (id: string) => request<void>('DELETE', `/api/v1/clients/${id}`),

  projects: () => request<{ projects: Project[] }>('GET', '/api/v1/projects'),
  createProject: (input: { name: string; icon?: string; client_id?: string }) => request<ProjectCreated>('POST', '/api/v1/projects', input),
  updateProject: (id: string, patch: Partial<Pick<Project, 'name' | 'icon' | 'notify' | 'client_id' | 'push_levels'>>) =>
    request<Project>('PATCH', `/api/v1/projects/${id}`, patch),
  deleteProject: (id: string) => request<void>('DELETE', `/api/v1/projects/${id}`),
  rotateKey: (id: string) => request<ProjectCreated>('POST', `/api/v1/projects/${id}/rotate-key`),

  webpush: () => request<{ configured: boolean; public_key?: string; error?: string }>('GET', '/api/v1/webpush'),
  subscribePush: (subscription: PushSubscriptionJSON, name: string) => request<Device>('POST', '/api/v1/webpush/subscriptions', { subscription, name }),
  unsubscribePush: (endpoint: string) => request<void>('DELETE', '/api/v1/webpush/subscriptions', { endpoint }),

  devices: () => request<{ devices: Device[] }>('GET', '/api/v1/devices'),
  updateDevice: (id: string, patch: { name?: string }) => request<Device>('PATCH', `/api/v1/devices/${id}`, patch),
  deleteDevice: (id: string) => request<void>('DELETE', `/api/v1/devices/${id}`),

  createPairing: () => request<PairingToken>('POST', '/api/v1/pairing'),
  pendingPairings: () => request<{ pairing_tokens: PairingToken[] }>('GET', '/api/v1/pairing'),
  revokePairing: (id: string) => request<void>('DELETE', `/api/v1/pairing/${id}`),

  silences: () => request<{ silences: Silence[]; fields: SilenceField[]; silenced_events: number }>('GET', '/api/v1/silences'),
  silence: (id: string) => request<Silence>('GET', `/api/v1/silences/${id}`),
  unsilence: (eventId: string) => request<{ event: Event; deliveries: Delivery[] }>('POST', `/api/v1/events/${encodeURIComponent(eventId)}/unsilence`),
  createSilence: (input: { field: SilenceField; value: string; project_id?: string; note?: string }) => request<Silence>('POST', '/api/v1/silences', input),
  deleteSilence: (id: string) => request<void>('DELETE', `/api/v1/silences/${id}`),

  test: (project_id?: string, force = true) =>
    request<{ event: Event; deliveries: Delivery[]; apns_configured: boolean; route: string }>('POST', '/api/v1/test', { ...(project_id ? { project_id } : {}), force }),
}

/** Tells the server an alert was acted on from the installed phone app. */
function byQuery(): string {
  try {
    return window.matchMedia?.('(display-mode: standalone)').matches ? '?by=phone' : ''
  } catch {
    return ''
  }
}
