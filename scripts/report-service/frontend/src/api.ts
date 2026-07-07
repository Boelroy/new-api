export type LogRow = {
  hour: string
  user_id: number
  username: string
  token_id: number
  token_name: string
  channel_id: number
  channel_name: string
  group: string
  model: string
  request_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  total_tokens: number
  input_cost: number
  output_cost: number
  cache_read_cost: number
  cache_write_cost: number
  total_cost: number
}

export type ChannelRow = {
  id: number
  name: string
  key: string
  status: number
  type: number
  tag: string
  priority: number
  used_usd: number
  last_hour_usd: number
  // Real-time RPM: count of type=2 log rows in the last 60s. Populated by
  // /api/allkeys/data; other endpoints leave it as 0.
  rpm: number
  quota_usd: number | null
  unit_price_cny: number | null
  note: string
}

export type DownstreamPricing = {
  group: string
  discount: number
  note: string
  updated_at: number
}

export type FXRate = {
  date: string
  rate: number
  updated_at: number
}

export type FXRateResponse = {
  rates: FXRate[]
  default_rate: number
}

export type ProfitDailyRow = {
  date: string
  fx_rate: number
  used_usd: number
  revenue_usd: number
  cost_usd: number
  profit_usd: number
  profit_rate: number
}

export type ProfitByKey = {
  channel_id: number
  channel_name: string
  tag: string
  source: 'main' | 'pipi'
  used_usd: number
  unit_price_cny: number
  cost_usd: number
}

export type ProfitByGroup = {
  group: string
  used_usd: number
  discount: number
  revenue_usd: number
}

export type ProfitByTag = {
  tag: string
  source: 'main' | 'pipi'
  used_usd: number
  cost_usd: number
  revenue_usd: number
  profit_usd: number
  profit_rate: number
  key_count: number
}

export type ProfitSummary = {
  start: string
  end: string
  used_usd: number
  revenue_usd: number
  cost_usd: number
  profit_usd: number
  profit_rate: number
  daily: ProfitDailyRow[]
  by_key: ProfitByKey[]
  by_tag: ProfitByTag[]
  by_group: ProfitByGroup[]
  missing_pricing: { channel_ids: number[] | null; groups: string[] | null }
}

export type KeySummary = {
  channels: ChannelRow[]
  total_last_hour: number
}

export type KeyTestResult = {
  key: string
  ok: boolean
  status: number
  latency_ms: number
  error?: string
  message?: string
}

export type DetectProbe = {
  label: string
  intent: string
  status: number
  headers: Record<string, string>
  body: string
  elapsed_ms: number
  retries?: number
  retry_history?: number[]
  stream_event_count?: number
  stream_max_gap_ms?: number
}

export type DetectSignal = {
  code: string
  tier: number
  label: string
  detail: string
  layer: string
  implies: string
}

export type DetectClassification = {
  router_label: string
  router_confidence: string
  backend_label: string
  backend_confidence: string
  signals: DetectSignal[]
  notes?: string[]
}

export type DetectResult = {
  url: string
  model: string
  started_at: string
  probes: DetectProbe[]
  classification: DetectClassification
  llm_report?: string
  llm_error?: string
  grader_model?: string
  grader_ms?: number
}

export type DetectModelsResponse = {
  status: number
  headers: Record<string, string>
  body: string
  elapsed_ms: number
}

// ---- Provider Testing (unified Detect + Eval) ----

export type TestProject = {
  id: string
  name: string
  url: string
  api_key: string          // masked on list/get
  grader_url: string       // empty when grader not configured
  grader_api_key: string   // masked on list/get; empty means no grader
  grader_model: string     // fallback default applied server-side when empty
  created_at: number
  updated_at: number
  run_count?: number
}

export type TestRunStatus = 'running' | 'grading' | 'done' | 'error' | 'cancelled'
export type TestRunKind = 'detect' | 'eval' | 'combined'
export type TestFileKind =
  | 'detect-trace'
  | 'detect-report'
  | 'detect-result'
  | 'eval-trace'
  | 'eval-report'
  | 'stderr'

export type TestRun = {
  id: string
  project_id: string
  model: string
  kind: TestRunKind
  status: TestRunStatus
  pass_at: number
  run_grader: boolean
  detect_trace_bytes: number
  detect_report_bytes: number
  detect_result_bytes: number
  eval_trace_bytes: number
  eval_report_bytes: number
  stderr_bytes: number
  error_msg?: string
  llm_error?: string
  grader_ms: number
  started_at: number
  ended_at?: number
  elapsed_ms?: number
}

export type TestRunDetail = TestRun & {
  files: Partial<Record<TestFileKind, string>>
}

export type TestRunLiveStatus = {
  id: string
  status: TestRunStatus
  started_at: number
  ended_at?: number
  elapsed_ms?: number
  error_msg?: string
  stderr?: string
  stderr_trimmed?: boolean
}

// Storage key for the API key used by the /profit gate.
const PROFIT_KEY_STORAGE = 'report_api_key'

export function getProfitApiKey(): string {
  try {
    return localStorage.getItem(PROFIT_KEY_STORAGE) ?? ''
  } catch {
    return ''
  }
}

export function setProfitApiKey(key: string) {
  try {
    if (key) localStorage.setItem(PROFIT_KEY_STORAGE, key)
    else localStorage.removeItem(PROFIT_KEY_STORAGE)
  } catch { /* ignore */ }
}

async function request<T>(url: string, opts?: RequestInit): Promise<T> {
  // Auto-inject X-API-Key from localStorage so the /profit gate can
  // authenticate without relying on cookies. Restricted to /api/profit/*
  // because the same header on other endpoints would short-circuit the
  // server's auth middleware to super_admin and bypass role gates.
  const headers = new Headers(opts?.headers ?? {})
  const apiKey = getProfitApiKey()
  if (apiKey && !headers.has('X-API-Key') && url.startsWith('/api/profit/')) {
    headers.set('X-API-Key', apiKey)
  }
  const res = await fetch(url, { ...(opts ?? {}), headers })
  if (res.status === 401) {
    // /profit handles its own auth via the gate; other pages bounce to /login.
    if (typeof window !== 'undefined' && !window.location.pathname.startsWith('/profit')) {
      window.location.href = '/login'
    }
    throw new Error('Unauthorized')
  }
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || res.statusText)
  }
  return res.json()
}

export type CacheStatsBucket = {
  bucket: string
  requests: number
  prompt_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  completion_tokens: number
  hit_pct: number
  reuse_x: number
}

export type CacheStatsResponse = {
  buckets: CacheStatsBucket[]
  summary: {
    requests: number
    prompt_tokens: number
    completion_tokens: number
    cache_read_tokens: number
    cache_write_tokens: number
    hit_pct: number
    reuse_x: number
  }
  range: {
    start: string
    end: string
    bucket: 'hour' | 'day'
    model: string
  }
}

// Mirrors the role tiers enforced on the backend. ROLE_TESTER and
// ROLE_STUDIO_OPERATOR are horizontal specializations (Key Tester +
// Provider Testing / batch-create scoped to bound studio) and do NOT
// inherit admin permissions via numeric compare.
export const ROLE_USER = 1
export const ROLE_STUDIO_OPERATOR = 2
export const ROLE_TESTER = 5
export const ROLE_ADMIN = 10
export const ROLE_SUPER_ADMIN = 100

export type AuthMe = {
  role: number
  user_id?: number
  username?: string
  studio?: string
}

export type AuthUser = {
  id: number
  username: string
  role: number
  studio: string
  status: number       // 1 = enabled, 0 = disabled
  disabled_at: number  // last time disabled; 0 = never
  created_at: number
  updated_at: number
}

export type RemoteProfile = {
  id: number
  name: string
  host: string
  user_id: number
  has_token: boolean
  created_at: number
  updated_at: number
}

export type RemoteChannel = {
  id: number
  name: string
  type: number
  status: number
  group: string
  tag: string
  priority: number
  weight: number
  models: string
  used_quota: number
  created_time: number
  // Merged in from the local remote_channel_meta table:
  quota_usd?: number | null
  note?: string
}

export type RemoteChannelListResponse = {
  channels: RemoteChannel[]
  total: number
  host: string
  user_id: number
  truncated: boolean
}

export type RemoteChannelCreateItem = {
  key: string
  quota_usd?: number | null
  note?: string
}

export type RemoteChannelCreateRequest = {
  profile_id: number
  name_prefix: string
  type?: number
  models: string
  group?: string
  tag?: string
  priority?: number
  base_url?: string
  items: RemoteChannelCreateItem[]
}

export type RemoteChannelCreateResult = {
  key: string
  ok: boolean
  channel_id?: number
  name?: string
  error?: string
}

export type RemoteChannelCreateResponse = {
  results: RemoteChannelCreateResult[]
  ok: number
  total: number
}

export type RemoteChannelUpdateRequest = {
  profile_id: number
  channel_id: number
  name?: string
  tag?: string
  status?: number
  priority?: number
  group?: string
  models?: string
  quota_usd?: number | null
  note?: string
}

export type RemoteChannelLastHourResponse = {
  data: Record<string, number> // channel_id -> quota (raw units), 1h window
  rpm?: Record<string, number> // channel_id -> requests / min, 60s window
  tpm?: Record<string, number> // channel_id -> tokens / min, 60s window
}

export const api = {
  login: (username: string, password: string) =>
    fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    }),

  logout: () => fetch('/api/logout', { method: 'POST' }),

  getAuthMe: () => request<AuthMe>('/api/auth/me'),

  listUsers: () => request<{ users: AuthUser[] }>('/api/users'),

  createUser: (payload: { username: string; password: string; role: number; studio?: string }) =>
    request<AuthUser>('/api/users', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  updateUser: (id: number, payload: { password?: string; role?: number; studio?: string }) =>
    request<AuthUser>(`/api/users/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  // Admin+ endpoint: reset password only (no role / studio changes). The
  // server enforces an anti-escalation check so admin can't reset a peer
  // or higher-privileged account.
  resetUserPassword: (id: number, password: string) =>
    request<{ ok: boolean }>(`/api/users/${id}/reset-password`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    }),

  // Admin+ endpoints. status=0 also revokes any JWT issued before the
  // disable moment; the user is forced back to login on their next
  // request. Anti-escalation enforced on both server-side.
  disableUser: (id: number) =>
    request<{ ok: boolean; status: number }>(`/api/users/${id}/disable`, { method: 'POST' }),

  enableUser: (id: number) =>
    request<{ ok: boolean; status: number }>(`/api/users/${id}/enable`, { method: 'POST' }),

  deleteUser: (id: number) =>
    request<{ ok: boolean }>(`/api/users/${id}`, { method: 'DELETE' }),

  listStudios: () => request<{ studios: string[] }>('/api/studios'),

  getReport: (start: string, end: string) =>
    request<LogRow[]>(`/api/report?start=${start}&end=${end}`),

  getKeysData: () => request<KeySummary>('/api/keys/data'),

  saveQuotas: (payload: { key: string; quota_usd: number }[]) =>
    request<{ saved: number }>('/api/keys/quota', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  batchCreateChannels: (
    studio: string,
    suffix: string,
    channels: { key: string; quota_usd: number; priority?: number; unit_price_cny?: number }[],
    defaults?: { priority?: number; unit_price_cny?: number },
  ) =>
    request<{ created: { id: number; name: string }[]; count: number }>('/api/channels/batch-create', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ studio, suffix, channels, ...(defaults ?? {}) }),
    }),

  batchUpdateChannelPriority: (channel_ids: number[], priority: number) =>
    request<{ updated: number; priority: number }>('/api/channels/batch-priority', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ channel_ids, priority }),
    }),

  getCacheStats: (params: { start?: string; end?: string; bucket?: 'hour' | 'day'; model?: string }) => {
    const qs = new URLSearchParams()
    if (params.start) qs.set('start', params.start)
    if (params.end) qs.set('end', params.end)
    if (params.bucket) qs.set('bucket', params.bucket)
    if (params.model) qs.set('model', params.model)
    const suffix = qs.toString()
    return request<CacheStatsResponse>(`/api/cache-stats${suffix ? '?' + suffix : ''}`)
  },

  getBatchCreateModels: () =>
    request<{ models: string }>('/api/config/batch-models'),

  saveBatchCreateModels: (models: string) =>
    request<{ models: string }>('/api/config/batch-models', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ models }),
    }),

  getAllKeys: (start?: string, end?: string) => {
    const params = new URLSearchParams()
    if (start) params.set('start', start)
    if (end) params.set('end', end)
    const qs = params.toString()
    return request<ChannelRow[]>(`/api/allkeys/data${qs ? '?' + qs : ''}`)
  },

  exportCSV: (start: string, end: string) => {
    window.location.href = `/api/export/csv?start=${start}&end=${end}`
  },

  testKeys: (keys: string[], model: string) =>
    request<{ results: KeyTestResult[] }>('/api/keys/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ keys, model }),
    }),

  detectModels: (url: string, key: string) => {
    const qs = new URLSearchParams({ url, key }).toString()
    return request<DetectModelsResponse>(`/api/detect/models?${qs}`)
  },

  // ---- Remote New-API inspector ----

  remoteProfiles: () =>
    request<{ profiles: RemoteProfile[] }>('/api/remote-newapi/profiles'),

  remoteProfileCreate: (payload: {
    name: string
    host: string
    user_id: number
    access_token: string
  }) =>
    request<RemoteProfile>('/api/remote-newapi/profiles', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  remoteProfileUpdate: (
    id: number,
    payload: { name?: string; host?: string; user_id?: number; access_token?: string },
  ) =>
    request<{ ok: boolean }>(`/api/remote-newapi/profiles/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  remoteProfileDelete: (id: number) =>
    request<{ ok: boolean }>(`/api/remote-newapi/profiles/${id}`, { method: 'DELETE' }),

  // Read cached channel list from local mirror (remote_channel_current).
  // No hit to the remote — used to render the page immediately on refresh
  // or profile switch. Content freshness comes from the cron sync loop.
  remoteCachedChannels: (profileID: number) =>
    request<{ channels: RemoteChannel[]; total: number; cached_at: number; cached: boolean }>(
      `/api/remote-newapi/channels/cached?profile_id=${profileID}`,
    ),

  remoteFetchChannels: (
    payload: { profile_id?: number; host?: string; user_id?: number; access_token?: string; group?: string; status?: string; type?: string; page_size?: number },
  ) =>
    request<RemoteChannelListResponse>('/api/remote-newapi/channels', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  remoteChannelGet: (profileID: number, channelID: number) =>
    request<{ channel: RemoteChannel }>(
      `/api/remote-newapi/channels/${channelID}?profile_id=${profileID}`,
    ),

  remoteChannelCreate: (payload: RemoteChannelCreateRequest) =>
    request<RemoteChannelCreateResponse>('/api/remote-newapi/channels/create', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  remoteChannelUpdate: (payload: RemoteChannelUpdateRequest) =>
    request<{ ok: boolean }>('/api/remote-newapi/channels', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  remoteChannelDelete: (profileID: number, channelID: number) =>
    request<{ ok: boolean }>(
      `/api/remote-newapi/channels/${channelID}?profile_id=${profileID}`,
      { method: 'DELETE' },
    ),

  remoteTestKey: (key: string, model: string) =>
    request<KeyTestResult>('/api/remote-newapi/channels/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key, model }),
    }),

  remoteChannelLastHour: (profileID: number, channelIDs: number[]) =>
    request<RemoteChannelLastHourResponse>('/api/remote-newapi/channels/last-hour', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profile_id: profileID, channel_ids: channelIDs }),
    }),

  // Historical snapshots written by the periodic sync loop (see
  // startRemoteSnapshotSync). Two shapes:
  //   • without channel_id: latest snapshot per channel within `since`,
  //     used to derive per-row Δ used_quota.
  //   • with channel_id: full time series for that channel, used for the
  //     sparkline that expands under a row.
  remoteSnapshotLatest: (profileID: number, sinceEpoch?: number) => {
    const qs = new URLSearchParams({ profile_id: String(profileID) })
    if (sinceEpoch) qs.set('since', String(sinceEpoch))
    return request<{ latest: Record<string, { captured_at: number; used_quota: number }> }>(
      `/api/remote-newapi/snapshots?${qs}`,
    )
  },

  remoteSnapshotSeries: (profileID: number, channelID: number, sinceEpoch?: number) => {
    const qs = new URLSearchParams({
      profile_id: String(profileID),
      channel_id: String(channelID),
    })
    if (sinceEpoch) qs.set('since', String(sinceEpoch))
    return request<{ channel_id: number; points: { captured_at: number; used_quota: number; status: number }[] }>(
      `/api/remote-newapi/snapshots?${qs}`,
    )
  },

  // ---- Provider Testing ----

  testingListProjects: () =>
    request<{ projects: TestProject[] }>('/api/testing/projects'),

  testingCreateProject: (payload: {
    name: string
    url: string
    api_key: string
    grader_url?: string
    grader_api_key?: string
    grader_model?: string
  }) =>
    request<TestProject>('/api/testing/projects', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  testingGetProject: (id: string) =>
    request<TestProject>(`/api/testing/projects/${encodeURIComponent(id)}`),

  testingUpdateProject: (
    id: string,
    payload: {
      name?: string
      url?: string
      api_key?: string
      grader_url?: string
      grader_api_key?: string
      grader_model?: string
    },
  ) =>
    request<TestProject>(`/api/testing/projects/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  testingDeleteProject: (id: string) =>
    request<{ ok: boolean; deleted_runs: number }>(`/api/testing/projects/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),

  testingListRuns: (projectId: string) =>
    request<{ runs: TestRun[] }>(`/api/testing/projects/${encodeURIComponent(projectId)}/runs`),

  testingStartRun: (
    projectId: string,
    payload: { model: string; pass_at?: number; run_grader?: boolean },
  ) =>
    request<{ run_id: string; project_id: string; started_at: number; run_grader: boolean; model: string; pass_at: number }>(
      `/api/testing/projects/${encodeURIComponent(projectId)}/runs`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      },
    ),

  testingGetRun: (id: string) =>
    request<TestRunDetail>(`/api/testing/runs/${encodeURIComponent(id)}`),

  testingRunStatus: (id: string) =>
    request<TestRunLiveStatus>(`/api/testing/runs/${encodeURIComponent(id)}/status`),

  testingCancelRun: (id: string) =>
    request<{ ok: boolean }>(`/api/testing/runs/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),

  testingRegrade: (id: string, phase: 'detect' | 'eval') =>
    request<{ ok: boolean; phase: string }>(`/api/testing/runs/${encodeURIComponent(id)}/regrade`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ phase }),
    }),

  testingDeleteRun: (id: string) =>
    request<{ ok: boolean; project_id: string }>(`/api/testing/runs/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),

  saveKeyPricing: (payload: { channel_id: number; quota_usd?: number; unit_price_cny?: number; note?: string }[]) =>
    request<{ saved: number }>('/api/keys/pricing', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  bulkSaveKeyPricing: (text: string) =>
    request<{ saved: number; not_found: string[]; errors: { line: number; reason: string }[] }>('/api/keys/pricing/bulk', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text }),
    }),

  getDownstreamPricing: () =>
    request<DownstreamPricing[]>('/api/profit/downstream/pricing'),

  saveDownstreamPricing: (payload: { group: string; discount: number; note: string }[]) =>
    request<{ saved: number }>('/api/profit/downstream/pricing', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  deleteDownstreamPricing: (group: string) =>
    request<{ ok: boolean }>(`/api/profit/downstream/pricing/${encodeURIComponent(group)}`, {
      method: 'DELETE',
    }),

  getFXRates: () => request<FXRateResponse>('/api/profit/fx'),

  saveFXRates: (payload: { date: string; rate: number }[]) =>
    request<{ saved: number }>('/api/profit/fx', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  saveDefaultFXRate: (rate: number) =>
    request<{ ok: boolean; rate: number }>('/api/profit/fx/default', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ rate }),
    }),

  deleteFXRate: (date: string) =>
    request<{ ok: boolean }>(`/api/profit/fx/${encodeURIComponent(date)}`, {
      method: 'DELETE',
    }),

  getProfitDaily: (start: string, end: string) =>
    request<ProfitSummary>(`/api/profit/daily?start=${start}&end=${end}`),

  refreshToday: () =>
    request<{
      ok: boolean
      date: string
      elapsed_ms: number
      local_elapsed_ms?: number
      pipi_refresh_elapsed_ms?: number
      pipi_refresh_error?: string
      pipi_sync_elapsed_ms?: number
      pipi_sync_error?: string
    }>('/api/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    }),

  getRefreshStatus: () =>
    request<{ running: boolean }>('/api/refresh/status'),

  syncPipi: (payload?: { start?: string; end?: string; days?: number }) =>
    request<{ ok: boolean; start: string; end: string }>('/api/profit/pipi/sync', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload ?? {}),
    }),

  getPipiStatus: () =>
    request<{ configured: boolean; start?: string; end?: string; status?: string; last_sync_at?: number }>(
      '/api/profit/pipi/status'
    ),
}
