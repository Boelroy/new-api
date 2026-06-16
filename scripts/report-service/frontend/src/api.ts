export type LogRow = {
  hour: string
  user_id: number
  username: string
  token_id: number
  token_name: string
  channel_id: number
  channel_name: string
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
  used_usd: number
  last_hour_usd: number
  quota_usd: number | null
}

export type KeySummary = {
  channels: ChannelRow[]
  total_last_hour: number
}

async function request<T>(url: string, opts?: RequestInit): Promise<T> {
  const res = await fetch(url, opts)
  if (res.status === 401) {
    window.location.href = '/login'
    throw new Error('Unauthorized')
  }
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || res.statusText)
  }
  return res.json()
}

export const api = {
  login: (username: string, password: string) =>
    fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    }),

  logout: () => fetch('/api/logout', { method: 'POST' }),

  getReport: (start: string, end: string) =>
    request<LogRow[]>(`/api/report?start=${start}&end=${end}`),

  getKeysData: () => request<KeySummary>('/api/keys/data'),

  saveQuotas: (payload: { key: string; quota_usd: number }[]) =>
    request<{ saved: number }>('/api/keys/quota', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  batchCreateChannels: (suffix: string, channels: { key: string; quota_usd: number }[]) =>
    request<{ created: { id: number; name: string }[]; count: number }>('/api/channels/batch-create', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ suffix, channels }),
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
}
