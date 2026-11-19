import type { ReactNode } from 'react'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import TencentUsers from '../TencentUsers'
import TencentStatement from '../TencentStatement'
import TencentPush from '../TencentPush'
import type { TencentLine, TencentPushSettings, TencentStatementPreview } from '../../api'

// The pages are tested without the app shell (header / sidebar fetch the
// session and need the router + theme).
vi.mock('../../components/Layout', () => ({
  default: (props: { title: string; children: ReactNode }) => (
    <div>
      <h1>{props.title}</h1>
      {props.children}
    </div>
  ),
}))

type Call = { method: string; url: string; body: any }

// mockApi answers fetch by "METHOD /path?query"; a function handler sees the
// request body, a number answers that HTTP status.
function mockApi(routes: Record<string, unknown>) {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ method, url: input, body })
      const handler = routes[`${method} ${input}`]
      if (handler === undefined) return new Response(`no mock for ${method} ${input}`, { status: 500 })
      const value = typeof handler === 'function' ? handler(body) : handler
      if (typeof value === 'number') return new Response('not found', { status: value })
      return new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }),
  )
  return calls
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('腾讯一站 · 用户', () => {
  it('validates the form, sends the platform role and shows the generated password once', async () => {
    const user = userEvent.setup()
    const calls = mockApi({
      'GET /api/tencent/users': { list: [] },
      'GET /api/tencent/users?platform=ccmax': {
        list: [{ id: 9, username: 'mgr1', role: 9, status: 1, platforms: ['ccmax'] }],
      },
      'POST /api/tencent/users': (body: any) => ({
        id: 21,
        username: body.username,
        studio: body.studio,
        role: body.role,
        manager_id: body.manager_id ?? 0,
        password: 'Gen3ratedPassw0rdXyz',
        studio_shared_with: ['old-user'],
      }),
    })
    render(<TencentUsers />)
    const create = screen.getByRole('button', { name: '创建' })

    await user.type(screen.getByLabelText('用户名'), 'ab')
    await user.click(create)
    expect(screen.getByRole('alert')).toHaveTextContent('用户名须为 3-32 位')
    await user.clear(screen.getByLabelText('用户名'))
    await user.type(screen.getByLabelText('用户名'), 'cc_user1')
    await user.click(create)
    expect(screen.getByRole('alert')).toHaveTextContent('请填写供应商名称')
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0)

    await user.selectOptions(screen.getByLabelText('平台角色'), 'supplier_ccmax')
    await screen.findByRole('option', { name: 'mgr1' })
    await user.selectOptions(screen.getByLabelText('归属经理'), '9')
    await user.type(screen.getByLabelText('供应商名称'), '  CC Co ')
    await user.click(create)

    await screen.findByText('Gen3ratedPassw0rdXyz')
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
      username: 'cc_user1',
      studio: 'CC Co',
      role: 'supplier_ccmax',
      manager_id: 9,
    })
    expect(screen.getByText(/old-user/)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '我已保存' }))
    expect(screen.queryByText('Gen3ratedPassw0rdXyz')).not.toBeInTheDocument()
  })
})

function line(over: Partial<TencentLine>): TencentLine {
  return {
    line_key: '2026-10|third_party|1001|gpt-5',
    platform: 'third_party',
    user_id: 1,
    supplier: 'Studio A',
    bill_month: 10,
    vendor: 'OpenAI',
    model: 'gpt-5',
    account_id: '1001',
    key: '****abcd1234',
    official_usd: 10,
    tax_rate: '',
    discount: 1,
    tencent_usd: 10,
    ref_fx_rate: 7.2,
    invoice_rmb: 72,
    bill_name: '',
    source: 'third_party - alice',
    cost_cny: 72,
    cost_usd: 0,
    rmb_priced: false,
    discount_from: 'default',
    ...over,
  }
}

function preview(fx: number, lines: TencentLine[]): TencentStatementPreview {
  return {
    month: '2026-10',
    bill_month: 10,
    fx_rate: fx,
    fx_rate_from: fx === 7.2 ? 'system' : 'month',
    ref_fx_rate: fx,
    ref_fx_rate_from: 'fx_rate',
    headers: ['供应商名称', '账单月份', '模型厂商名称', '模型名称', '账号ID', 'key', '付给官方的费用（不区分币种）', '税率', '对应折扣',
      '腾讯支付金额（不区分币种）', '参考汇率\nhttps://www.kylc.com/huilv/d-boc-usd.html', '开票金额（RMB/元）', '官方导出的账单名称', '来源系统 / 上号人'],
    summary_headers: [],
    lines,
    summary: [...new Set(lines.map((l) => l.supplier))].map((s) => {
      const ls = lines.filter((l) => l.supplier === s)
      return {
        supplier: s,
        bill_month: 10,
        vendor: ls[0].vendor,
        source: ls[0].platform,
        official_usd: ls.reduce((a, l) => a + l.official_usd, 0),
        invoice_rmb: ls.reduce((a, l) => a + l.invoice_rmb, 0),
        lines: ls.length,
      }
    }),
    warnings: [],
  }
}

function statementRoutes(fx: () => number, snapshot: unknown) {
  return {
    'GET /api/tencent/statement?month=2026-10': () =>
      preview(fx(), [
        line({ official_usd: Math.round((72 / fx()) * 100) / 100 }),
        line({ line_key: '2026-10|proxy|3001|gpt-5', platform: 'proxy', supplier: 'Proxy Co', user_id: 3, account_id: '3001', key: '' }),
      ]),
    'GET /api/tencent/params/months': { list: [], system_fx_rate: 7.2 },
    'GET /api/tencent/params/suppliers': { list: [], upload_discount_pct: 88, platforms: [] },
    'GET /api/tencent/statements/2026-10': snapshot,
  }
}

// The page opens on last month; tests pin October 2026.
async function openOctober() {
  fireEvent.change(screen.getByLabelText('账单月份'), { target: { value: '2026-10' } })
  await screen.findAllByText('Studio A')
}

describe('腾讯一站 · 月对账', () => {
  it('refreshes the preview after the month rate is saved', async () => {
    const user = userEvent.setup()
    let fx = 7.2
    const calls = mockApi({
      ...statementRoutes(() => fx, 404),
      'PUT /api/tencent/params/months': (body: any) => {
        fx = body.fx_rate
        return { ok: true }
      },
    })
    render(<TencentStatement />)
    await openOctober()
    expect(screen.getAllByText('$10.00').length).toBeGreaterThan(0)

    await user.type(screen.getByLabelText('系统汇率'), '7.5')
    await user.click(screen.getByRole('button', { name: '保存汇率' }))

    await waitFor(() => expect(screen.getAllByText('$9.60').length).toBeGreaterThan(0))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ month: '2026-10', fx_rate: 7.5, ref_fx_rate: null })
    expect(calls.filter((c) => c.url === '/api/tencent/statement?month=2026-10').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText(/系统汇率 7.5（本月设置）/)).toBeInTheDocument()
  })

  it('locks a confirmed month and links the exports to the snapshot', async () => {
    const user = userEvent.setup()
    mockApi(
      statementRoutes(() => 7.2, {
        statement: { month: '2026-10', status: 'confirmed', fx_rate: 7.2, ref_fx_rate: 7.2, warnings: [], lines: 2, generated_at: 1, confirmed_at: 2 },
        lines: [],
        summary: [],
      }),
    )
    render(<TencentStatement />)
    await openOctober()
    await screen.findByText('已确认')

    expect(screen.getByRole('button', { name: '重新生成' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '确认' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: '生成对账单' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: /导出全部 xlsx/ })).toHaveAttribute('href', '/api/tencent/statements/2026-10/export')

    await user.selectOptions(screen.getByLabelText('供应商'), 'Studio A')
    expect(screen.getByRole('link', { name: /导出 Studio A/ })).toHaveAttribute(
      'href',
      '/api/tencent/statements/2026-10/export?supplier=Studio%20A',
    )
  })

  it('offers generation for a month without a snapshot and keeps export disabled', async () => {
    const user = userEvent.setup()
    const calls = mockApi({
      ...statementRoutes(() => 7.2, 404),
      'POST /api/tencent/statements/2026-10/generate': {
        statement: { month: '2026-10', status: 'draft', fx_rate: 7.2, ref_fx_rate: 7.2, warnings: [], lines: 2, generated_at: 1, confirmed_at: 0 },
        lines: [],
        summary: [],
      },
    })
    render(<TencentStatement />)
    await openOctober()
    expect(screen.getByRole('button', { name: /导出全部 xlsx/ })).toBeDisabled()

    await user.click(await screen.findByRole('button', { name: '生成对账单' }))
    await screen.findByText('草稿')
    expect(calls.some((c) => c.method === 'POST' && c.url === '/api/tencent/statements/2026-10/generate')).toBe(true)
    expect(screen.getByRole('button', { name: '重新生成' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '确认' })).toBeEnabled()
    expect(screen.getByRole('link', { name: /导出全部 xlsx/ })).toBeInTheDocument()
  })
})

const pushSettings: TencentPushSettings = {
  statement: { webhook: '', enabled: true, day: 2, hour: 10, base_url: '' },
  statement_last_month: '2026-09',
  supplier_quota: { webhook_set: true, webhook_last4: 'abcd' },
  ccmax_report: { webhook: 'https://lark.example/ccmax', hours: '10,22' },
  proxy_report: { webhook: '', hours: '' },
  ops_daily_spend: { enabled: true, webhook: 'https://lark.example/ops', daily_spend: false, daily_spend_hour: 9 },
}

describe('腾讯一站 · 推送', () => {
  it('saves the statement push and the existing push settings, and lists push records', async () => {
    const user = userEvent.setup()
    const calls = mockApi({
      'GET /api/tencent/push-settings': pushSettings,
      'GET /api/tencent/push-logs?limit=50': {
        list: [{ id: 1, kind: 'statement', trigger: 'schedule', month: '2026-09', ok: false, error: 'lark HTTP 500: boom', created_at: 1 }],
      },
      'PUT /api/tencent/push-settings': (body: any) => ({ ...pushSettings, statement: { ...pushSettings.statement, ...body.statement } }),
    })
    render(<TencentPush />)
    await screen.findByText('lark HTTP 500: boom')
    const logRow = screen.getByText('lark HTTP 500: boom').closest('tr')!
    expect(within(logRow).getByText('失败')).toBeInTheDocument()
    expect(within(logRow).getByText('定时')).toBeInTheDocument()
    expect(screen.getByText('Webhook（已设置，…abcd）')).toBeInTheDocument()

    await user.type(screen.getByLabelText('对账推送 Webhook'), 'https://lark.example/stmt')
    await user.click(screen.getByLabelText('每月自动推送上月'))
    await user.clear(screen.getByLabelText('推送日'))
    await user.type(screen.getByLabelText('推送日'), '5')
    await user.click(screen.getByRole('button', { name: '保存对账推送' }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(1))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({
      statement: { webhook: 'https://lark.example/stmt', enabled: false, day: 5, hour: 10, base_url: '' },
    })

    await user.clear(screen.getByLabelText('CC Max 推送小时'))
    await user.type(screen.getByLabelText('CC Max 推送小时'), '9')
    await user.click(screen.getByLabelText('推送每日消耗'))
    await user.click(screen.getByRole('button', { name: '保存现有推送配置' }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(2))
    // The masked quota webhook is only sent when a new one is typed.
    expect(calls.filter((c) => c.method === 'PUT')[1].body).toEqual({
      ccmax_report: { webhook: 'https://lark.example/ccmax', hours: '9' },
      proxy_report: { webhook: '', hours: '' },
      ops_daily_spend: { enabled: true, webhook: 'https://lark.example/ops', daily_spend: true, daily_spend_hour: 9 },
    })
  })

  it('pushes the chosen month by hand', async () => {
    const user = userEvent.setup()
    const calls = mockApi({
      'GET /api/tencent/push-settings': { ...pushSettings, statement: { ...pushSettings.statement, webhook: 'https://lark.example/stmt' } },
      'GET /api/tencent/push-logs?limit=50': { list: [] },
      'POST /api/tencent/statement-push': (body: any) => ({ month: body.month, status: 'draft', rows: [] }),
    })
    render(<TencentPush />)
    fireEvent.change(await screen.findByLabelText('推送月份'), { target: { value: '2026-10' } })
    await user.click(screen.getByRole('button', { name: '立即推送' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ month: '2026-10' })
  })
})
