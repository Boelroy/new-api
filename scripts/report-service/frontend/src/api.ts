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
  api_key: string      // masked on list/get
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

// Mirrors the role tiers enforced on the backend.
export const ROLE_USER = 1
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
  created_at: number
  updated_at: number
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

  // ---- Provider Testing ----

  testingListProjects: () =>
    request<{ projects: TestProject[] }>('/api/testing/projects'),

  testingCreateProject: (payload: { name: string; url: string; api_key: string }) =>
    request<TestProject>('/api/testing/projects', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  testingGetProject: (id: string) =>
    request<TestProject>(`/api/testing/projects/${encodeURIComponent(id)}`),

  testingUpdateProject: (id: string, payload: { name?: string; url?: string; api_key?: string }) =>
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
