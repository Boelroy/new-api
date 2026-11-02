/// <reference types="vite/client" />

export type User = {
  id: number
  username: string
  role: 'billing_manager' | 'supplier'
  supplier_id: number
  disabled: boolean
}
export type Session = { user: User; csrf_token: string }
export type Supplier = { id: number; name: string; currency: string }
export type Vendor = {
  id: number
  supplier_id: number
  name: string
  source_studio: string
  key_count: number
}
export type DailySummary = {
  timezone: string
  start: string
  end: string
  heads: Supplier[]
  totals: {
    supplier_id: number
    supplier_name: string
    gateway_usage: string
    request_count: string
  }[]
  days: {
    date: string
    gateway_usage: string
    request_count: string
    heads: {
      supplier_id: number
      supplier_name: string
      gateway_usage: string
      request_count: string
      sources_synced: boolean
      vendors: {
        vendor_id: number
        vendor_name: string
        gateway_usage: string
        request_count: string
      }[]
    }[]
  }[]
}
// report: explicit channel IDs; report_group: a gateway user's groups;
// remote_tag: a remote profile's channel tags; ccmax: CC Max uploaders.
export type SourceKind = 'report' | 'report_group' | 'remote_tag' | 'ccmax'
export type MappingOption = {
  value: string
  quantity: string
  unit: string
  source_id: number
  source_name: string
  supplier_id: number
}
export type Source = {
  id: number
  supplier_id: number
  name: string
  kind: SourceKind
  base_url: string
  credential_env: string
  channel_ids: string
  selector: string
  exclude_selector: boolean
  profile_id: number
  gateway_user_id: number
  usage_type: string
  settlement_mode: 'settlement' | 'usage_only'
  discount: string
  fx_rate: string
  currency: string
  last_sync_at: number
  last_error: string
}
export type Usage = {
  id: number
  date: string
  source_id: number
  source_name: string
  usage_type: string
  description: string
  quantity: string
  unit: string
  unit_price: string
  currency: string
  discount: string
  fx_rate: string
  rate_mode?: 'multiply' | 'divide'
  amount: string
  origin: 'manual' | 'sync'
  settlement_status: 'settled' | 'unpriced'
  created_by: number
  created_at: number
  request_count?: number
  input_tokens?: number
  output_tokens?: number
  cache_read_tokens?: number
  cache_write_tokens?: number
}
export type Recharge = {
  id: number
  date: string
  amount: string
  currency: string
  reference: string
  note: string
  created_by: number
  created_at: number
  reversed?: boolean
}
// One product line (usage type) on one day; unpriced is the part of quantity
// that has no agreed price yet and so adds nothing to amount.
export type DailyProduct = {
  usage_type: string
  unit: string
  quantity: string
  unpriced: string
  amount: string
  request_count: number
}
export type InitialBalance = {
  id: number
  supplier_id: number
  revision: number
  date: string
  amount: string
  note: string
  created_at: number
}
export type Claim = {
  id: number
  supplier_id: number
  date: string
  usage_type: string
  quantity: string
  amount: string
  note: string
}
// One settlement-sheet row: a priced product line for a day, or a balance
// movement (recharge, reversal, initial balance). Balance is after the row.
export type StatementLine = {
  date: string
  kind: string
  usage_type: string
  description: string
  origin: string
  quantity: string
  unit: string
  unit_price: string
  currency: string
  discount: string
  fx_rate: string
  rate_mode: string
  settlement_status: string
  original_amount: string
  change: string
  balance: string
  note: string
  recharge_id: number
  claim: Claim | null
}
export type Statement = {
  supplier: Supplier
  balance: string
  opening_balance: string
  initial_balance?: InitialBalance | null
  lines?: StatementLine[]
  total_recharges: string
  total_usage: string
  gateway_usage: string
  unpriced_usage: string
  days: {
    date: string
    usage: string
    gateway_usage: string
    unpriced_usage: string
    request_count: string
    recharges: string
    initial?: string
    closing_balance: string
    sources_synced: boolean
    products?: DailyProduct[]
  }[]
  source_status: {
    id: number
    name: string
    last_sync_at: number
    sync_failed: boolean
    missing_days: number
  }[]
  usage: Usage[]
  recharges: Recharge[]
  ledger: {
    id: number
    date: string
    kind: string
    amount: string
    usage_id: number
    recharge_id: number
    note: string
    created_by: number
    created_at: number
  }[]
}

export type Overview = {
  suppliers: Omit<Statement, 'usage' | 'recharges' | 'ledger' | 'lines'>[]
}

let csrfToken = ''
let onUnauthorized: (() => void) | undefined

export function setSession(session: Session | null) {
  csrfToken = session?.csrf_token ?? ''
}

export function setUnauthorizedHandler(handler?: () => void) {
  onUnauthorized = handler
}

export class APIError extends Error {
  constructor(
    public status: number,
    message: string
  ) {
    super(message)
  }
}

export async function request<T>(
  path: string,
  options: { method?: string; body?: unknown; signal?: AbortSignal } = {}
): Promise<T> {
  const response = await fetch(`${import.meta.env.BASE_URL}api${path}`, {
    method: options.method ?? 'GET',
    credentials: 'same-origin',
    signal: options.signal,
    headers: {
      'Content-Type': 'application/json',
      'X-Requested-With': 'supplier-billing',
      ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  })
  const payload: unknown = await response.json().catch(() => null)
  if (options.signal?.aborted) throw new DOMException('Aborted', 'AbortError')
  if (!response.ok) {
    const error =
      payload && typeof payload === 'object' && 'error' in payload
        ? String(payload.error)
        : 'Request failed'
    if (
      response.status === 401 &&
      path !== '/login' &&
      error !== 'Invalid username or password.'
    ) {
      onUnauthorized?.()
    }
    throw new APIError(response.status, error)
  }
  return payload as T
}

export function billingDate(date = new Date()): string {
  return new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(date)
}

export function errorMessage(error: unknown): string {
  if (error instanceof TypeError) return 'Network request failed'
  return error instanceof Error ? error.message : 'Request failed'
}

export function discountPercent(factor: string): string {
  return (Number(factor) * 100).toFixed(4).replace(/0+$/, '').replace(/\.$/, '')
}
