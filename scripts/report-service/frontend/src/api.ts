import { withBase } from './basePath'

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
  // Raw channels."group" value — comma-separated when the channel serves
  // several groups.
  group: string
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

// ---- Error Center ----

export type ErrorRow = {
  id: number
  created_at: number
  channel_id: number
  channel_name: string
  group: string
  model_name: string
  token_name: string
  status_code: string
  error_code: string
  error_type: string
  request_path: string
  content: string
}

export type ErrorFacet = { value: string; count: number }

export type ErrorFacets = {
  total: number
  groups: ErrorFacet[]
  status_codes: ErrorFacet[]
  error_codes: ErrorFacet[]
  window_sec: number
}

export type ErrorListResponse = {
  rows: ErrorRow[]
  total: number
  page: number
  page_size: number
}

export type ErrorQuery = {
  window_sec?: number
  group?: string
  model?: string
  channel_id?: number
  status_code?: string
  error_code?: string
  q?: string
  page?: number
  page_size?: number
}

export function errorQueryString(params: ErrorQuery): string {
  const qs = new URLSearchParams()
  Object.entries(params).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') qs.set(k, String(v))
  })
  return qs.toString()
}

// ---- Lark balance alerts, per channel group ----

// One row of /api/notify/status: the group's live balance plus the effective
// thresholds it would be judged against. `configured` is false when the group
// has no override row and is inheriting the server defaults.
export type NotifyGroupState = {
  group: string
  channels: number
  channels_with_quota: number
  total_quota_usd: number
  total_used_usd: number
  total_remaining_usd: number
  last_hour_usd: number
  eta_hours: number | null
  configured: boolean
  muted: boolean
  usd_threshold: number
  hours_threshold: number
  would_alert: { hours?: boolean; usd?: boolean }
}

export type NotifyStatus = {
  lark_configured: boolean
  default_thresholds: { hours: number; usd: number }
  groups: NotifyGroupState[]
  last_notified: Record<string, string>
}

// A stored override. null usd/hours means "inherit the server default".
export type NotifyThreshold = {
  group: string
  usd_threshold: number | null
  hours_threshold: number | null
  enabled: boolean
  note: string
  updated_at: number
}

export type NotifyThresholdsResponse = {
  thresholds: NotifyThreshold[]
  defaults: { hours: number; usd: number }
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
  const res = await fetch(withBase(url), { ...(opts ?? {}), headers })
  if (res.status === 401) {
    // /profit handles its own auth via the gate; other pages bounce to /login.
    if (typeof window !== 'undefined' && !window.location.pathname.startsWith(withBase('/profit'))) {
      window.location.href = withBase('/login')
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
export const ROLE_SUPPLIER_01 = 4
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

// Supplier Account portal shapes. Providers/models mirror the third-party
// portal payloads; accounts/metrics are the report-service scoped views.
export type SupplierProvider = {
  name: string
  shape: string
  need_region: boolean
  exclusive_model: boolean
  auto_select_all: boolean
  key_min_len: number
}

export type SupplierModel = {
  id: number
  label: string
  value: string
  model_name: string
  provider: string
  bill_name: string
  tip: string
}

export type SupplierAccount = {
  id: number
  remote_account_id: number
  provider: string
  models: string
  alias: string
  account_type: number
  remark: string
  key_last8: string
  studio: string
  uploaded_by: number
  username?: string
  created_at: number
  // Per-account USD cost cap (0 = no limit).
  quota_usd: number
}

export type SupplierProviderDefault = {
  models: string[]
  account_type: number
}

export type SupplierSettings = {
  openapi_token_set: boolean
  openapi_token_last4: string
  visible_providers: string[]
  provider_defaults: Record<string, SupplierProviderDefault>
  // Dedicated per-account quota alert webhook (separate from group balance).
  quota_webhook_set: boolean
  quota_webhook_last4: string
  // RMB->USD divisor used to display cost and evaluate quotas.
  fx_rate: number
  // How often the quota alert loop checks usage (seconds).
  quota_tick_sec: number
}

export type SupplierMetric = {
  aid: number
  account_alias: string
  status: string
  requests: number | null
  // Shown to studios as well as admins; null when the upstream omits it.
  cost?: number | null
  success_rate: number | null
  prompt_tokens: number | null
  completion_tokens: number | null
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
  // Preloaded into the batch-upload group + models fields when the OpenAI
  // preset (channel_type=1) is active. Empty ⇒ frontend falls back to the
  // built-in 'openai' group / DEFAULT_OPENAI_MODELS.
  default_openai_group?: string
  default_openai_models?: string
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
  auto_disable?: boolean               // 到额自动禁用开关
  auto_disable_reserve_usd?: number    // 到额前保留的缓冲额度（$）
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
  auto_disable?: boolean
  auto_disable_reserve_usd?: number
}

export type RemoteChannelLastHourResponse = {
  data: Record<string, number>    // channel_id -> quota (raw units), 1h window
  rpm?: Record<string, number>    // channel_id -> requests / min (60s window)
  tpm?: Record<string, number>    // channel_id -> tokens / min (60s window)
  err_rpm?: Record<string, number> // channel_id -> ERROR requests / min (60s window, LogTypeError=5)
}

// Per-channel usage over an arbitrary [start, end] window. `data` is
// channel_id -> quota (raw units, 500000 = $1). `total_used_usd` is the
// sum already converted to USD so the UI doesn't have to.
export type RemoteChannelUsageRangeResponse = {
  data: Record<string, number>
  total_used_usd: number
  start_timestamp: number
  end_timestamp: number
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
    fetch(withBase('/api/login'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    }),

  logout: () => fetch(withBase('/api/logout'), { method: 'POST' }),

  getAuthMe: () => request<AuthMe>('/api/auth/me'),

  // Per-role sidebar visibility overrides (super admin only). `overrides`
  // maps a role value (as string) to the list of hidden nav item keys.
  getSidebarConfig: () => request<{ overrides: Record<string, string[]> }>('/api/sidebar-config'),
  setSidebarConfig: (overrides: Record<string, string[]>) =>
    request<{ ok: boolean; overrides: Record<string, string[]> }>('/api/sidebar-config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ overrides }),
    }),

  // Deployment statistics timezone. Read from /api/auth/config; set here
  // (super admin only).
  setReportTimezone: (timezone: string) =>
    request<{ ok: boolean; timezone: string }>('/api/report-settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ timezone }),
    }),

  // AWS Bedrock config (super admin). default_regions = the list pre-selected in
  // the batch-create picker; region_prefix_map = region→inference-prefix
  // overrides. Read from /api/auth/config; set here.
  setAwsConfig: (payload: { default_regions?: string[]; region_prefix_map?: Record<string, string>; default_group?: string; default_models?: string }) =>
    request<{ ok: boolean; default_regions: string[]; region_prefix_map: Record<string, string>; default_group: string; default_models: string }>('/api/aws-config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

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
      type?: number           // 1=OpenAI, 3=Azure, 14=Anthropic (default), 24=Gemini, 33=AWS, 41=Vertex
      group?: string          // e.g. 'default' | 'gemini'
      models?: string         // comma-separated; empty → server default
      other?: string          // Vertex region / Azure api-version
      settings?: string       // pre-serialised JSON string, e.g. '{"vertex_key_type":"json"}'
      base_url?: string       // Azure resource endpoint (https://<res>.openai.azure.com)
      // AWS Bedrock (type=33): one key fans out to one channel per region.
      // `regions` is the multi-region list; `region` stays for back-compat.
      // The per-region model_mapping prefix is derived server-side from the
      // admin-configured region→prefix map (aws-config), so model_prefix is no
      // longer sent from the multi-region path.
      region?: string
      regions?: string[]
      key_type?: 'ak_sk' | 'api_key'
      model_prefix?: string
      // AWS Bedrock: optional outbound proxy URL → channel.settings.proxy.
      proxy?: string
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

  getNotifyStatus: () => request<NotifyStatus>('/api/notify/status'),

  getNotifyThresholds: () => request<NotifyThresholdsResponse>('/api/notify/thresholds'),

  saveNotifyThresholds: (
    payload: {
      group: string
      usd_threshold: number | null
      hours_threshold: number | null
      enabled: boolean
      note?: string
    }[],
  ) =>
    request<{ saved: number }>('/api/notify/thresholds', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  deleteNotifyThreshold: (group: string) =>
    request<{ ok: boolean }>(`/api/notify/thresholds?group=${encodeURIComponent(group)}`, {
      method: 'DELETE',
    }),

  pushNotifyDigest: () =>
    request<{ ok: boolean; groups: number }>('/api/notify/digest', { method: 'POST' }),

  testNotify: () => request<{ ok: boolean }>('/api/notify/test', { method: 'POST' }),

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
    window.location.href = withBase(`/api/export/csv?start=${start}&end=${end}`)
  },

  testKeys: (keys: string[], model: string, provider: 'claude' | 'openai' = 'claude') =>
    request<{ results: KeyTestResult[] }>('/api/keys/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ keys, model, provider }),
    }),

  detectModels: (url: string, key: string) => {
    const qs = new URLSearchParams({ url, key }).toString()
    return request<DetectModelsResponse>(`/api/detect/models?${qs}`)
  },

  // ---- Error Center (local logs, type=5) ----

  listErrors: (params: ErrorQuery) =>
    request<ErrorListResponse>(`/api/errors?${errorQueryString(params)}`),

  errorFacets: (params: ErrorQuery) =>
    request<ErrorFacets>(`/api/errors/facets?${errorQueryString(params)}`),

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
    default_openai_group?: string
    default_openai_models?: string
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
      default_openai_group?: string
      default_openai_models?: string
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
    auto_disable?: boolean
    auto_disable_reserve_usd?: number
  }) =>
    request<{ updated: number; failed: number[] }>('/api/remote-newapi/channels/meta/bulk', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  // Global on/off + tick interval for the auto-disable-on-quota loop.
  // GET returns current state; POST accepts partial updates.
  remoteAutoDisableConfigGet: () =>
    request<{ enabled: boolean; interval_sec: number }>('/api/remote-newapi/auto-disable/config'),
  remoteAutoDisableConfigSet: (payload: { enabled?: boolean; interval_sec?: number }) =>
    request<{ enabled: boolean; interval_sec: number }>('/api/remote-newapi/auto-disable/config', {
      method: 'POST',
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
  // key_type selects Vertex auth mode: 'json' (default, existing) uploads
  // Service Account JSON blobs via items[].key_json; 'api_key' uploads
  // plain Vertex Express API keys via items[].key. The backend stamps
  // channel.settings = {"vertex_key_type": "<key_type>"} either way.
  remoteVertexCreate: (payload: {
    profile_id: number
    name_prefix: string
    models: string
    group?: string
    region: string
    key_type?: 'json' | 'api_key'
    items: (
      | { key_json: unknown; quota_usd?: number; note?: string }
      | { key: string;       quota_usd?: number; note?: string }
    )[]
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

  // Per-profile visibility allowlist. Empty allowlist = visible to all
  // remote_studio_operator users (default). Populated list = only the
  // listed rs_auth_user ids may see the profile in the operator picker
  // and pass the upload preflight. Admin+ only.
  remoteProfileVisibilityGet: (profileID: number) =>
    request<{
      allowlist: number[]
      operators: { id: number; username: string; studio: string }[]
    }>(`/api/remote-newapi/profiles/${profileID}/visibility`),

  remoteProfileVisibilitySet: (profileID: number, user_ids: number[]) =>
    request<{ ok: boolean; allowlist: number[] }>(
      `/api/remote-newapi/profiles/${profileID}/visibility`,
      {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ user_ids }),
      },
    ),

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

  // AWS Bedrock (channel_type=33) also bypasses the pending queue — the
  // batch shares one region, which the backend bakes into channel.key
  // ("<ak>|<sk>|<region>" for ak_sk, "<apikey>|<region>" for api_key) and
  // into a region-prefixed Claude model_mapping. key_type selects the auth
  // mode; items[].key carries the credential without the region.
  remoteAwsCreate: (payload: {
    profile_id: number
    name_prefix: string
    models: string
    group?: string
    // region = legacy single; regions = one channel per region. Prefer regions.
    region?: string
    regions?: string[]
    key_type?: 'ak_sk' | 'api_key'
    // Optional outbound proxy URL stamped into channel.settings.proxy on every
    // created channel. Omit / empty → no proxy.
    proxy?: string
    items: { key: string; quota_usd?: number; note?: string }[]
  }) =>
    request<{
      results: { index: number; ok: boolean; channel_id?: number; error?: string }[]
      ok: number
      total: number
    }>('/api/remote-newapi/aws/create', {
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

  // Per-channel usage over an arbitrary window. Omit channel_ids to have
  // the backend resolve the caller's owned set (studio operator only
  // sees their own uploads; admin sees the whole profile).
  remoteChannelUsageRange: (payload: {
    profile_id: number
    start_timestamp: number
    end_timestamp: number
    channel_ids?: number[]
  }) =>
    request<RemoteChannelUsageRangeResponse>('/api/remote-newapi/channels/usage-range', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
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

  localPoolAllowedTypes: () =>
    request<{ limits: number[] }>('/api/local-pool/allowed-types'),

  localPoolEnqueue: (payload: {
    studio: string
    suffix: string
    type?: number
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

  // ---- Local model health scheduler (Model Health page) ----

  localHealthGetConfig: () => request<LocalHealthConfig>('/api/local-health/config'),

  localHealthSetConfig: (payload: Partial<LocalHealthConfig>) =>
    request<LocalHealthConfig>('/api/local-health/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  localHealthListRules: () => request<{ rules: LocalHealthRule[] }>('/api/local-health/rules'),

  localHealthCreateRule: (payload: Partial<LocalHealthRule>) =>
    request<LocalHealthRule>('/api/local-health/rules', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  localHealthUpdateRule: (id: number, payload: Partial<LocalHealthRule>) =>
    request<LocalHealthRule>(`/api/local-health/rules/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  localHealthDeleteRule: (id: number) =>
    request<{ ok: boolean }>(`/api/local-health/rules/${id}`, { method: 'DELETE' }),

  localHealthPreviewRule: (id: number) =>
    request<LocalHealthPreview>(`/api/local-health/rules/${id}/preview`),

  localHealthStatus: (params?: { state?: string; tag?: string; channel_id?: number; limit?: number }) => {
    const qs = new URLSearchParams()
    if (params?.state) qs.set('state', params.state)
    if (params?.tag) qs.set('tag', params.tag)
    if (params?.channel_id != null) qs.set('channel_id', String(params.channel_id))
    if (params?.limit != null) qs.set('limit', String(params.limit))
    const suffix = qs.toString()
    return request<LocalHealthStatus>(`/api/local-health/status${suffix ? '?' + suffix : ''}`)
  },

  localHealthEvents: (params?: { channel_id?: number; kind?: string; limit?: number }) => {
    const qs = new URLSearchParams()
    if (params?.channel_id != null) qs.set('channel_id', String(params.channel_id))
    if (params?.kind) qs.set('kind', params.kind)
    if (params?.limit != null) qs.set('limit', String(params.limit))
    const suffix = qs.toString()
    return request<{ events: LocalHealthEvent[] }>(`/api/local-health/events${suffix ? '?' + suffix : ''}`)
  },

  localHealthProbe: (payload: { channel_id: number; model: string }) =>
    request<{ class: string; message: string; error_code: string; seconds: number; http_code: number }>(
      '/api/local-health/probe',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      },
    ),

  // Local Channel Sync: list stored credentials that can be stood up as
  // local channels, and sync a batch of them. Admin+ only (server gate).
  localSyncList: () =>
    request<{ items: SyncableCredential[] }>('/api/remote-newapi/local-sync/syncable'),

  localSync: (items: { profile_id: number; remote_channel_id: number }[]) =>
    request<{ results: LocalSyncResult[]; ok: number; total: number }>(
      '/api/remote-newapi/local-sync',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ items }),
      },
    ),

  // Supplier Account portal. providers/models proxy the third-party portal;
  // accounts/metrics are scoped by role on the server (suppliers see only
  // their own uploads, admin+ see all; cost is stripped for suppliers).
  supplierProviders: () =>
    request<{ list: SupplierProvider[] }>('/api/supplier-account/providers'),

  supplierModels: () =>
    request<{ list: SupplierModel[] }>('/api/supplier-account/models'),

  supplierAccounts: () =>
    request<{ accounts: SupplierAccount[]; fx_rate: number }>('/api/supplier-account/accounts'),

  // Admin-only: pull the portal's account roster into the local table.
  supplierSyncAccounts: () =>
    request<{ synced: number; total: number }>('/api/supplier-account/sync', {
      method: 'POST',
    }),

  // Admin-only: set a per-account USD cost cap (0 clears it).
  supplierSetQuota: (id: number, quota_usd: number) =>
    request<{ id: number; quota_usd: number }>(`/api/supplier-account/accounts/${id}/quota`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ quota_usd }),
    }),

  supplierUploadAccount: (payload: {
    provider: string
    model: string
    api_key: string
    account_id?: string
    account_type?: number
    remark?: string
  }) =>
    request<{ id: number; alias: string; msg: string }>('/api/supplier-account/accounts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  supplierMetrics: (payload: { begin_time: string; end_time: string }) =>
    request<{ accounts: SupplierMetric[] }>('/api/supplier-account/metrics', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  // Admin-only: read + write the OpenAPI token and the supplier-visible
  // provider allowlist. The token is never returned in full (last4 only).
  getSupplierSettings: () =>
    request<SupplierSettings>('/api/supplier-account/settings'),

  setSupplierSettings: (payload: {
    openapi_token?: string
    visible_providers?: string[]
    provider_defaults?: Record<string, SupplierProviderDefault>
    quota_webhook?: string
    fx_rate?: number
    quota_tick_sec?: number
  }) =>
    request<SupplierSettings>('/api/supplier-account/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  // Per-provider default models + account type, for prefilling the upload
  // form. Available to suppliers too.
  getSupplierProviderDefaults: () =>
    request<{ defaults: Record<string, SupplierProviderDefault> }>('/api/supplier-account/provider-defaults'),
}

// One stored credential and its resolved local-channel target. Mirrors the
// backend syncableCredential struct (local_channel_sync.go). key_masked is
// "…" + last 8 chars; plaintext never leaves the server.
export type SyncableCredential = {
  profile_id: number
  profile_name: string
  remote_channel_id: number
  channel_type: number
  channel_type_name: string
  key_type: string
  region: string
  key_masked: string
  resolved_models: string
  resolved_group: string
  channel_name: string
  already_synced: boolean
  local_channel_id: number
  created_at: number
}

export type LocalSyncResult = {
  profile_id: number
  remote_channel_id: number
  ok: boolean
  skipped: boolean
  channel_id?: number
  error?: string
}

export type LocalHealthConfig = {
  enabled: boolean
  tick_sec: number
  probe_batch: number
  bootstrap_batch: number
  probe_timeout_sec: number
  concurrency: number
  max_actions_per_tick: number
  // False when MAIN_SERVICE_URL / MAIN_SERVICE_TOKEN are missing — the
  // scheduler cannot run at all in that case.
  token_configured: boolean
}

export type LocalHealthRule = {
  id: number
  name: string
  match_tag: string
  // -1 means "any channel type".
  match_type: number
  match_group: string
  match_channel_ids: string
  // Empty falls back to the configured batch-create model list for the
  // channel's type.
  candidate_models: string
  enabled: boolean
  // enforce=false is observe-only: transitions are recorded but new-api is
  // never modified.
  enforce: boolean
  probe_interval_sec: number
  down_window_sec: number
  down_fail_min: number
  recover_ok_min: number
  created_at: number
  updated_at: number
}

export type LocalHealthPreview = {
  rule: LocalHealthRule
  channel_count: number
  pair_count: number
  // Every probe is a real billed upstream call — this is the number an
  // operator needs before enabling a rule.
  probes_per_day: number
  sample_channels: {
    channel_id: number
    name: string
    type: number
    status: number
    group: string
    tag: string
    current_models: string
    candidate_models: string
  }[]
  token_configured: boolean
}

export type LocalHealthItem = {
  channel_id: number
  model: string
  rule_id: number
  state: 'unknown' | 'up' | 'down' | 'unsupported'
  consecutive_ok: number
  consecutive_fail: number
  last_ok_at: number
  last_checked_at: number
  next_check_at: number
  last_class: string
  last_error: string
  last_latency_ms: number
  channel_name: string
  tag: string
  group: string
  channel_status: number
  channel_type: number
  in_models: boolean
  disabled_by_us: boolean
}

export type LocalHealthTick = {
  at: number
  probed: number
  ok: number
  model_down: number
  channel_down: number
  throttled: number
  neutral: number
  unsupported: number
  actions: number
  breaker_open: boolean
  duration_ms: number
}

export type LocalHealthStatus = {
  config: LocalHealthConfig
  items: LocalHealthItem[]
  state_counts: Record<string, number>
  // How far behind the due-queue is. A growing lag silently stretches the
  // configured down window.
  queue_lag_sec: number
  channels_disabled: number
  last_tick: LocalHealthTick
}

export type LocalHealthEvent = {
  id: number
  channel_id: number
  model: string
  rule_id: number
  kind: string
  detail: string
  dry_run: boolean
  created_at: number
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
  // Per-studio channel-type allowlist. A studio absent from the map (or with
  // an empty list) is unrestricted. Supported types: 14 Anthropic, 20 OpenRouter.
  studio_type_limits?: Record<string, number[]>
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
