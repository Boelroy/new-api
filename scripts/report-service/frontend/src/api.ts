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
  // Unmasked key. Populated by /api/allkeys/data ONLY when status === 3
  // (auto-disabled) so the CSV export can surface dead keys for rotation.
  // Never populated for enabled or manually-disabled channels.
  full_key?: string
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

export type DownstreamDaily = {
  group: string
  date: string
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
  source: 'main' | 'pipi' | 'remote'
  used_usd: number
  cost_usd: number
  revenue_usd: number
  profit_usd: number
  profit_rate: number
  key_count: number
}

export type ProfitByRemoteChannel = {
  profile_id: number
  profile_name: string
  channel_id: number
  channel_name: string
  used_usd: number
  cost_usd: number
  revenue_usd: number
  profit_usd: number
  profit_rate: number
  unit_price_cny?: number | null
  downstream_discount?: number | null   // USD → USD multiplier used for revenue
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
  by_remote_channel?: ProfitByRemoteChannel[]
  remote_used_usd?: number
  remote_cost_usd?: number
  remote_revenue_usd?: number
  remote_profit_usd?: number
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

// Mirrors the role tiers enforced on the backend. ROLE_TESTER,
// ROLE_STUDIO_OPERATOR and ROLE_PROJECT_ADMIN are horizontal
// specializations (Key Tester + Provider Testing / batch-create scoped to
// bound studio / Key Capacity + Key Tester) and do NOT inherit admin
// permissions via numeric compare.
export const ROLE_USER = 1
export const ROLE_STUDIO_OPERATOR = 2
export const ROLE_REMOTE_STUDIO_OPERATOR = 3
export const ROLE_TESTER = 5
export const ROLE_PROJECT_ADMIN = 7
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
  // host / user_id / has_token / pool tuning knobs are only returned to
  // super_admin; studio_operator gets a slimmed profile (name + defaults
  // only) so the upstream URL and credentials never leak into their UI.
  // Treat these as optional at the type level to reflect the wire shape.
  host?: string
  user_id?: number
  has_token?: boolean
  default_models: string   // preloaded into the batch-upload models field (anthropic)
  default_group: string    // preloaded into the batch-upload group field (anthropic uploads)
  // Preloaded into the batch-upload group + models fields when the Gemini
  // preset is active. Empty ⇒ frontend falls back to built-in defaults.
  default_gemini_group?: string
  default_gemini_models?: string
  // Same story for Vertex AI (channel_type=41). Vertex model naming
  // conventions differ from AI Studio (publisher prefix, @publisher
  // suffix), so a separate default list is cleaner than reusing the
  // Gemini one. Empty ⇒ frontend falls back to a hard-coded default.
  default_vertex_models?: string
  pool_interval_sec?: number  // pool refill cadence (seconds)
  pool_batch_size?: number    // ceiling for how many keys the pool refill uploads per tick
  auto_mode?: boolean         // when true scheduler sizes batch against live RPM
  rpm_base?: number           // 1 key handles this many RPM (n = ceil(rpm / rpm_base))
  rpm_min?: number            // below this RPM the pool tick uploads 0
  created_at: number
  updated_at: number
}

export type StudioPolicy = {
  studio: string
  accepting_keys: boolean
  has_row: boolean       // false = implicit default (no policy row), true = explicit
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
  unit_price_cny?: number | null       // 本地维护的成本；null = 未录入
  downstream_cny?: number | null       // 最新配置的下游单价；null = 未配置
  downstream_cny_date?: string         // 上一次配置的日期 YYYY-MM-DD
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
  // Optional channel.other — Azure uses it for api-version; Vertex goes
  // through /vertex/create so region isn't set here.
  other?: string
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
  unit_price_cny?: number | null
  note?: string
}

export type RemoteChannelLastHourResponse = {
  data: Record<string, number>    // channel_id -> quota (raw units), 1h window
  rpm?: Record<string, number>    // channel_id -> requests / min (60s window)
  tpm?: Record<string, number>    // channel_id -> tokens / min (60s window)
  err_rpm?: Record<string, number> // channel_id -> ERROR requests / min (60s window, LogTypeError=5)
}

// Row in the scheduled-upload queue. `key_masked` is "…" + last 8 chars;
// the plaintext key never leaves the server.
export type PendingKey = {
  id: number
  profile_id: number
  key_masked: string
  quota_usd: number
  note: string
  name_prefix: string
  group: string
  tag: string
  models: string
  priority: number
  pool_size: number            // 0 = upload immediately, >0 = drip pool of this size
  status: 'pending' | 'active' | 'used' | 'failed'
  remote_channel_id: number    // filled once uploaded
  attempts: number
  failed_reason?: string
  // Cumulative usage joined from remote_channel_current. Zero for
  // rows that haven't yet mapped to a remote channel.
  used_quota_raw: number
  used_usd: number
  // rs_auth_user.id of the operator who enqueued this row. 0 for
  // pre-migration rows (shown to everyone in the studio). Studio
  // operators only see their own new rows via backend filtering.
  uploaded_by: number
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
    defaults?: {
      priority?: number
      unit_price_cny?: number
      // Preset extensions (rc.150+). Older calls omit them and the
      // backend falls back to Anthropic (type=14, group='default').
      type?: number           // 1=OpenAI, 3=Azure, 14=Anthropic (default), 24=Gemini, 41=Vertex
      group?: string          // e.g. 'default' | 'gemini'
      models?: string         // comma-separated; empty → server default
      other?: string          // Vertex region / Azure api-version
      settings?: string       // pre-serialised JSON string, e.g. '{"vertex_key_type":"json"}'
      base_url?: string       // Azure resource endpoint (https://<res>.openai.azure.com)
    },
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

  // rc.154+: per channel-type saved defaults. Omitting `type` reads the
  // legacy Anthropic (14) list, so old callers keep working.
  getBatchCreateModels: (type?: number) =>
    request<{ models: string; type: number }>(
      `/api/config/batch-models${type ? `?type=${type}` : ''}`,
    ),

  saveBatchCreateModels: (models: string, type?: number) =>
    request<{ models: string; type: number }>('/api/config/batch-models', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(type ? { models, type } : { models }),
    }),

  getAllKeys: (start?: string, end?: string) => {
    const params = new URLSearchParams()
    if (start) params.set('start', start)
    if (end) params.set('end', end)
    const qs = params.toString()
    return request<ChannelRow[]>(`/api/allkeys/data${qs ? '?' + qs : ''}`)
  },

  // System-wide realtime RPM (count of type=2 log rows in the last 60s),
  // deliberately not studio-scoped so studio_operator sees global load.
  getAllKeysRpm: () => request<{ rpm: number }>('/api/allkeys/rpm'),

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
    default_models?: string
    default_group?: string
    default_gemini_group?: string
    default_gemini_models?: string
    pool_interval_sec?: number
    pool_batch_size?: number
    auto_mode?: boolean
    rpm_base?: number
    rpm_min?: number
  }) =>
    request<RemoteProfile>('/api/remote-newapi/profiles', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  remoteProfileUpdate: (
    id: number,
    payload: {
      name?: string
      host?: string
      user_id?: number
      access_token?: string
      default_models?: string
      default_group?: string
      default_gemini_group?: string
      default_gemini_models?: string
      pool_interval_sec?: number
      pool_batch_size?: number
      auto_mode?: boolean
      rpm_base?: number
      rpm_min?: number
    },
  ) =>
    request<{ ok: boolean }>(`/api/remote-newapi/profiles/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  remoteStudioPolicyList: (profileID: number) =>
    request<{ items: StudioPolicy[] }>(
      `/api/remote-newapi/studio-policy?profile_id=${profileID}`,
    ),

  remoteStudioPolicyUpsert: (payload: { profile_id: number; studio: string; accepting_keys: boolean }) =>
    request<{ ok: boolean }>('/api/remote-newapi/studio-policy', {
      method: 'POST',
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

  // Trigger the same paginated remote fetch the 15-min snapshot cron
  // does, then UPSERT the local mirror. Studio operators call this from
  // "获取用量" to break out of the cron cadence when they need fresh
  // used_quota now. Backend guards against concurrent refreshes per
  // profile — surface the 429 to the caller.
  remoteChannelsRefresh: (profileID: number) =>
    request<{ ok: boolean; fetched: number; total: number; refreshed: number }>(
      `/api/remote-newapi/channels/refresh?profile_id=${profileID}`,
      { method: 'POST' },
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

  // Local-only bulk write. Any missing pointer field is left untouched
  // per row — so { unit_price_cny: 4.3 } only sets prices, doesn't touch
  // quota_usd / note.
  remoteChannelMetaBulk: (payload: {
    profile_id: number
    channel_ids: number[]
    quota_usd?: number
    unit_price_cny?: number
    note?: string
  }) =>
    request<{ updated: number; failed: number[] }>('/api/remote-newapi/channels/meta/bulk', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  // Per-profile per-day downstream discount (multiplier from used_usd
  // to revenue_usd). Missing days fall back to the latest date ≤ day.
  remoteDownstreamDailyList: (profileID?: number, start?: string, end?: string) => {
    const qs = new URLSearchParams()
    if (profileID != null) qs.set('profile_id', String(profileID))
    if (start) qs.set('start', start)
    if (end) qs.set('end', end)
    const suf = qs.toString()
    return request<{ items: { profile_id: number; date: string; discount: number; note: string; updated_at: number }[] }>(
      `/api/remote-newapi/downstream-daily${suf ? '?' + suf : ''}`,
    )
  },

  remoteDownstreamDailyUpsert: (payload: { profile_id: number; date: string; discount: number; note?: string }) =>
    request<{ ok: boolean }>('/api/remote-newapi/downstream-daily', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  remoteDownstreamDailyDelete: (profileID: number, date: string) =>
    request<{ deleted: number }>(`/api/remote-newapi/downstream-daily?profile_id=${profileID}&date=${date}`, {
      method: 'DELETE',
    }),

  // Deprecated per-channel per-date downstream; kept for schema/api
  // compat only. Frontend no longer calls this.
  remoteChannelDownstreamBulk: (payload: {
    profile_id: number
    channel_ids: number[]
    downstream_cny: number
    date?: string   // YYYY-MM-DD, default today UTC
  }) =>
    request<{ updated: number; date: string }>('/api/remote-newapi/channels/downstream/bulk', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  // Aggregate rpm/tpm/last-hour for the whole profile in one shot — used
  // by the summary cards. Cached 30s on the server; no channel filter.
  remoteStatSummary: (profileID: number) =>
    request<{ rpm: number; tpm: number; quota_last_hour: number; cached: boolean }>(
      `/api/remote-newapi/stat/summary?profile_id=${profileID}`,
    ),

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

  // Scheduled upload queue (drip pool). pool_size=0 uploads immediately
  // on the next scheduler tick; >0 keeps at most that many `active` at a
  // time, waiting for each active row to hit its quota (remote status ≠ 1)
  // before promoting the next pending item.
  remotePendingEnqueue: (payload: {
    profile_id: number
    name_prefix: string
    // Optional channel type override (14 = Anthropic, 24 = Gemini, ...).
    // Backend defaults to 14 when omitted.
    type?: number
    models: string
    group?: string
    tag?: string
    priority?: number
    pool_size: number
    // Studio-operator only: when true the row skips the FIFO pool and
    // goes into the immediate-upload lane (pool_size=0 on the DB row).
    // Ignored for super admin.
    immediate?: boolean
    items: { key: string; quota_usd?: number; note?: string; priority?: number }[]
  }) =>
    request<{ inserted: number; skipped: number; total: number }>('/api/remote-newapi/pending', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  remotePendingList: (profileID: number, statusFilter?: string) => {
    const qs = new URLSearchParams({ profile_id: String(profileID) })
    if (statusFilter) qs.set('status', statusFilter)
    return request<{ items: PendingKey[] }>(`/api/remote-newapi/pending?${qs}`)
  },

  remotePendingDelete: (id: number) =>
    request<{ deleted: number }>(`/api/remote-newapi/pending/${id}`, { method: 'DELETE' }),

  // Vertex AI (channel_type=41) bypasses the pending queue — Vertex
  // needs region + settings JSON that the pending schema doesn't carry,
  // and a batch of SA JSONs is small enough to POST synchronously.
  // `key_json` on each item is the raw service-account JSON (parsed
  // object); the client serialises with JSON.stringify so the backend
  // sees the object exactly as uploaded. Each result carries `ok`
  // and either `channel_id` or `error` — partial success is normal.
  remoteVertexCreate: (payload: {
    profile_id: number
    name_prefix: string
    models: string
    group?: string
    region: string
    items: { key_json: unknown; quota_usd?: number; note?: string }[]
  }) =>
    request<{
      results: { index: number; ok: boolean; channel_id?: number; error?: string }[]
      ok: number
      total: number
    }>('/api/remote-newapi/vertex/create', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  // Azure OpenAI (channel_type=3) also bypasses the pending queue —
  // each Azure resource has its own base_url + api_version pair, which
  // the pending schema doesn't carry. Payload shape mirrors
  // remoteVertexCreate; the batch shares one resource endpoint.
  remoteAzureCreate: (payload: {
    profile_id: number
    name_prefix: string
    models: string
    group?: string
    base_url: string
    api_version?: string
    items: { key: string; quota_usd?: number; note?: string }[]
  }) =>
    request<{
      results: { index: number; ok: boolean; channel_id?: number; error?: string }[]
      ok: number
      total: number
    }>('/api/remote-newapi/azure/create', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  remoteChannelLastHour: (profileID: number, channelIDs: number[]) =>
    request<RemoteChannelLastHourResponse>('/api/remote-newapi/channels/last-hour', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profile_id: profileID, channel_ids: channelIDs }),
    }),

  // Categorised error breakdown for one channel over the past N seconds
  // (default 1h). Groups upstream error logs by (error_type, status_code).
  // Backend caches for 5min per (profile, channel, window).
  remoteChannelErrors: (profileID: number, channelID: number, windowSec = 3600) =>
    request<{
      total: number
      buckets: Array<{ error_type: string; status_code: number; count: number }>
      sample_size?: number
      window_sec?: number
    }>('/api/remote-newapi/channels/errors', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profile_id: profileID, channel_id: channelID, window_sec: windowSec }),
    }),

  // Success / error counts per channel over `windowSec` (default 1h).
  // Uses the remote paginated log endpoint with page_size=1 to pull
  // `total` from pageInfo — cheap regardless of the actual count.
  remoteChannelCounts: (profileID: number, channelIDs: number[], windowSec = 3600) =>
    request<{
      data: Record<string, { success: number; errors: number }>
      window_sec: number
    }>('/api/remote-newapi/channels/counts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profile_id: profileID, channel_ids: channelIDs, window_sec: windowSec }),
    }),

  // Profile-wide aggregate: total success/error counts + bucket
  // distribution by (error_type, status_code) over `windowSec`. Error
  // side is now backed by the local remote_error_log table (kept fresh
  // by a 60s sync loop), so bucket counts are exact rather than
  // sampled. `sync_lag_sec` reflects how stale the local mirror is.
  remoteProfileErrorSummary: (profileID: number, windowSec = 3600) => {
    const qs = new URLSearchParams({ window_sec: String(windowSec) })
    return request<{
      total_success: number
      total_errors: number
      error_rate: number
      buckets: Array<{ error_type: string; status_code: number; count: number; share: number }>
      sample_size: number
      truncated: boolean
      window_sec: number
      cached: boolean
      last_synced_at: number
      sync_lag_sec: number
    }>(`/api/remote-newapi/profiles/${profileID}/error-summary?${qs}`)
  },

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

  listDownstreamDaily: (params?: { group?: string; start?: string; end?: string }) => {
    const q = new URLSearchParams()
    if (params?.group) q.set('group', params.group)
    if (params?.start) q.set('start', params.start)
    if (params?.end) q.set('end', params.end)
    const suffix = q.toString() ? `?${q.toString()}` : ''
    return request<{ items: DownstreamDaily[] }>(`/api/profit/downstream/daily${suffix}`)
  },

  saveDownstreamDaily: (payload: { group: string; date: string; discount: number; note?: string }[]) =>
    request<{ saved: number }>('/api/profit/downstream/daily', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  deleteDownstreamDaily: (group: string, date: string) => {
    const q = new URLSearchParams({ group, date })
    return request<{ ok: boolean }>(`/api/profit/downstream/daily?${q.toString()}`, { method: 'DELETE' })
  },

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

  // ---- Local pool (KeyCapacity → Pool 上 Key tab) ----

  localPoolGetConfig: () =>
    request<LocalPoolConfig>('/api/local-pool/config'),

  localPoolSetConfig: (payload: Partial<LocalPoolConfig>) =>
    request<LocalPoolConfig>('/api/local-pool/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  localPoolGetRPM: () => request<{ rpm: number }>('/api/local-pool/rpm'),

  localPoolEnqueue: (payload: {
    studio: string
    suffix: string
    unit_price_cny?: number
    models?: string
    channels: { key: string; quota_usd: number; unit_price_cny?: number }[]
  }) =>
    request<{ inserted: number; skipped: number; total: number }>('/api/local-pool/enqueue', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  localPoolList: (studio?: string, status?: string) => {
    const qs = new URLSearchParams()
    if (studio) qs.set('studio', studio)
    if (status) qs.set('status', status)
    const suffix = qs.toString()
    return request<{ items: LocalPendingKey[] }>(`/api/local-pool/queue${suffix ? '?' + suffix : ''}`)
  },

  localPoolDelete: (id: number) =>
    request<{ deleted: number }>(`/api/local-pool/pending/${id}`, { method: 'DELETE' }),
}

export type LocalPoolConfig = {
  pool_interval_sec: number
  pool_batch_size: number
  auto_mode: boolean
  rpm_base: number
  rpm_min: number
  // Kept separate from batch_create_default_models so the Pool 上 Key
  // tab has its own model rotation independent of the classic
  // batch-create default.
  default_models: string
  // channels."group" value the scheduler uses when it inserts pool
  // rows. Snapshotted per-pending-row at enqueue so a mid-flight
  // change doesn't retarget already-queued keys. Empty → 'default'.
  default_group: string
}

export type LocalPendingKey = {
  id: number
  studio: string
  suffix: string
  key_masked: string
  quota_usd: number
  unit_price_cny?: number | null
  models: string
  group_name: string
  status: 'pending' | 'active' | 'used' | 'failed'
  priority: number
  channel_id: number
  attempts: number
  failed_reason?: string
  created_at: number
  updated_at: number
}
