import type { ReactNode } from 'react'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import TencentUsers from '../TencentUsers'
import TencentStatement from '../TencentStatement'
import TencentPush from '../TencentPush'
import { ConfirmHost } from '../../components/feedback'
import type { TencentLine, TencentPushSettings, TencentStatementPreview, TencentUser } from '../../api'

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

function user(over: Partial<TencentUser>): TencentUser {
  return {
    id: 1, username: 'u', studio: 'S', role: 4, role_name: 'supplier_01', platforms: [], status: 1, created_at: 1,
    supplier_accounts: 0, ccmax_accounts: 0, proxy_channels: 0, manager_id: 0, manager_username: '', members: 0,
    ...over,
  }
}

describe('腾讯一站 · 用户', () => {
  it('lists only 老台子 and CCMax users with their tags and filters on those two', async () => {
    const user_ = userEvent.setup()
    mockApi({
      'GET /api/tencent/users': {
        list: [
          user({ id: 1, username: 'old1', role: 4, role_name: 'supplier_01', supplier_accounts: 3 }),
          user({ id: 2, username: 'cc1', role: 8, role_name: 'supplier_ccmax', ccmax_accounts: 2, manager_username: 'mgr1' }),
          user({ id: 3, username: 'mgr1', role: 9, role_name: 'ccmax_manager', members: 1 }),
          user({ id: 4, username: 'proxy1', role: 1, role_name: 'proxy', proxy_channels: 5 }),
          user({ id: 5, username: 'khub1', role: 6, role_name: 'supplier_02' }),
        ],
      },
    })
    render(<TencentUsers />)
    await screen.findByText('old1')
    expect(screen.queryByText('proxy1')).not.toBeInTheDocument()
    expect(screen.queryByText('khub1')).not.toBeInTheDocument()
    const tag = (name: string) => within(screen.getByText(name, { selector: 'td.font-mono' }).closest('tr')!).getAllByText(/^(老台子|CCMax|CCMax Manager)$/)[0].textContent
    expect(tag('old1')).toBe('老台子')
    expect(tag('cc1')).toBe('CCMax')
    expect(tag('mgr1')).toBe('CCMax Manager')

    const filter = screen.getByLabelText('筛选')
    expect(within(filter).getAllByRole('option').map((o) => o.textContent)).toEqual(['全部', '老台子', 'CCMax'])
    await user_.selectOptions(filter, 'old')
    expect(screen.getByText('old1')).toBeInTheDocument()
    expect(screen.queryByText('cc1')).not.toBeInTheDocument()
    await user_.selectOptions(filter, 'ccmax')
    expect(screen.queryByText('old1')).not.toBeInTheDocument()
    expect(screen.getByText('cc1')).toBeInTheDocument()
    expect(screen.getAllByText('mgr1').length).toBeGreaterThan(0)
    expect(document.body.textContent).not.toContain('经理')
  })

  it('creates only the three roles, validates the form and shows the generated password once', async () => {
    const user_ = userEvent.setup()
    const calls = mockApi({
      'GET /api/tencent/users': { list: [user({ id: 9, username: 'mgr1', role: 9, role_name: 'ccmax_manager' })] },
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
    await screen.findByText('mgr1')
    expect(within(screen.getByLabelText('角色')).getAllByRole('option').map((o) => o.textContent)).toEqual([
      '老台子',
      'CCMax 号商',
      'CCMax Manager',
    ])
    const create = screen.getByRole('button', { name: '创建' })

    await user_.type(screen.getByLabelText('用户名'), 'ab')
    await user_.click(create)
    expect(screen.getByRole('alert')).toHaveTextContent('用户名须为 3-32 位')
    await user_.clear(screen.getByLabelText('用户名'))
    await user_.type(screen.getByLabelText('用户名'), 'cc_user1')
    await user_.click(create)
    expect(screen.getByRole('alert')).toHaveTextContent('请填写供应商名称')
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0)

    await user_.selectOptions(screen.getByLabelText('角色'), 'supplier_ccmax')
    await user_.selectOptions(screen.getByLabelText('归属 Manager'), '9')
    await user_.type(screen.getByLabelText('供应商名称'), '  CC Co ')
    await user_.click(create)

    await screen.findByText('Gen3ratedPassw0rdXyz')
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
      username: 'cc_user1',
      studio: 'CC Co',
      role: 'supplier_ccmax',
      manager_id: 9,
    })
    expect(screen.getByText(/old-user/)).toBeInTheDocument()
    await user_.click(screen.getByRole('button', { name: '我已保存' }))
    expect(screen.queryByText('Gen3ratedPassw0rdXyz')).not.toBeInTheDocument()
  })
})

// The template's header rows, as in 长河理想9月对账单-3.xlsx.
const EXCEL_LINE_HEADERS = [
  '供应商名称', '账单月份', '模型厂商名称', '模型名称', '账号ID', 'key', '付给官方的费用（不区分币种）', '税率', '对应折扣',
  '腾讯支付金额（不区分币种）', '参考汇率\nhttps://www.kylc.com/huilv/d-boc-usd.html', '开票金额（RMB/元）', '官方导出的账单名称', '来源系统 / 上号人',
]
const EXCEL_SUMMARY_HEADERS = ['供应商名称', '账单月份', '模型厂商名称', '付给官方的费用（不区分币种）', '开票金额（RMB/元）', '备注']

function line(over: Partial<TencentLine>): TencentLine {
  return {
    line_key: '2026-10|third_party|1001',
    platform: 'third_party',
    user_id: 1,
    supplier: 'Studio A',
    bill_month: 10,
    vendor: 'anthropic',
    model: 'claude-opus-4-1, claude-sonnet-4-5',
    account_id: '1001',
    key: 'anthropic-A-001 ***tegAA',
    official_usd: 11,
    tax_rate: '含票',
    discount: 0.88,
    tencent_usd: 9.68,
    ref_fx_rate: 7.25,
    invoice_rmb: 70.18,
    bill_name: 'anthropic+1001+Studio A',
    source: 'third_party',
    cost_cny: 77,
    cost_usd: 0,
    ...over,
  }
}

const proxyLine = line({
  line_key: '2026-10|proxy|3001', platform: 'proxy', supplier: 'Proxy Co', vendor: 'azure-openai', model: 'gpt-5',
  account_id: '3001', key: 'ch-a #3001', official_usd: 7, tencent_usd: 6.16, invoice_rmb: 44.66,
  bill_name: 'azure-openai+3001+Proxy Co', source: 'newapi proxy',
})

// CC Max bills one row per supplier: no account id, F counts the accounts.
const ccmaxLine = line({
  line_key: '2026-10|ccmax|bob', platform: 'ccmax', supplier: 'bob', vendor: 'claude', model: 'claude-opus-4-1',
  account_id: '', key: '2 个账号', official_usd: 7, tencent_usd: 6.16, invoice_rmb: 44.66, bill_name: 'claude+bob', source: 'ccmax',
})

function preview(lines: TencentLine[]): TencentStatementPreview {
  return {
    month: '2026-10', bill_month: 10, fx_rate: 7, fx_rate_from: 'month', ref_fx_rate: 7.25, ref_fx_rate_from: 'month',
    discount: 0.88, discount_from: 'upload', tax_rate: '含票', tax_rate_from: 'default',
    headers: [], summary_headers: [], lines,
    summary: lines.map((l) => ({
      supplier: l.supplier, bill_month: 10, vendor: l.vendor, source: l.source, official_usd: l.official_usd, invoice_rmb: l.invoice_rmb, lines: 1,
    })),
    warnings: [],
  }
}

function snapshot(status: 'draft' | 'confirmed', lines: TencentLine[]) {
  const p = preview(lines)
  return {
    statement: { month: '2026-10', status, fx_rate: 7, ref_fx_rate: 7.25, warnings: [], lines: lines.length, generated_at: 1, confirmed_at: 0 },
    lines,
    summary: p.summary,
  }
}

function statementRoutes(snap: unknown) {
  return {
    'GET /api/tencent/statement?month=2026-10': preview([line({}), proxyLine, ccmaxLine]),
    'GET /api/tencent/statements/2026-10': snap,
    'GET /api/tencent/params/months': { list: [], system_fx_rate: 7.2, default_discount: 0.88, default_tax_rate: '含票' },
    'GET /api/tencent/settings': {
      proxy_upstream: { url: 'http://up.example:3000', token_set: true, token_last4: 'WXYZ', user: '1', socks: '' },
      system_fx_rate: 7.2,
      upload_discount_pct: 88,
    },
  }
}

// The page opens on last month; tests pin October 2026.
async function openOctober() {
  fireEvent.change(screen.getByLabelText('账单月份'), { target: { value: '2026-10' } })
  await screen.findAllByText('Proxy Co')
}

describe('腾讯一站 · 月对账', () => {
  it('shows the bill as the template’s 汇总表 and API账单 columns, one row per account', async () => {
    mockApi(statementRoutes(404))
    render(<TencentStatement />)
    await openOctober()
    const [summaryTable, linesTable] = screen.getAllByRole('table')
    expect(within(summaryTable).getAllByRole('columnheader').map((h) => h.textContent)).toEqual(EXCEL_SUMMARY_HEADERS)
    expect(within(linesTable).getAllByRole('columnheader').map((h) => h.textContent)).toEqual(EXCEL_LINE_HEADERS)
    const row = within(linesTable).getByText('1001').closest('tr')!
    expect(within(row).getAllByRole('cell').map((c) => c.textContent)).toEqual([
      'Studio A', '10', 'anthropic', 'claude-opus-4-1, claude-sonnet-4-5', '1001', 'anthropic-A-001 ***tegAA',
      '11.00 USD', '含票', '88%', '9.68 USD', '7.25', '70.18', 'anthropic+1001+Studio A', 'third_party',
    ])
    const cc = within(linesTable).getByText('2 个账号').closest('tr')!
    expect(within(cc).getAllByRole('cell').map((c) => c.textContent)).toEqual([
      'bob', '10', 'claude', 'claude-opus-4-1', '', '2 个账号', '7.00 USD', '含票', '88%', '6.16 USD', '7.25', '44.66', 'claude+bob', 'ccmax',
    ])
    expect(within(linesTable).getAllByRole('row')).toHaveLength(4)
    expect(screen.getByText('未生成（实时预览）')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /导出全部 xlsx/ })).toBeDisabled()
    // No per-row editing any more.
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
  })

  it('locks a confirmed month and links the exports to the snapshot', async () => {
    const user = userEvent.setup()
    mockApi(statementRoutes(snapshot('confirmed', [line({}), proxyLine])))
    render(<TencentStatement />)
    await openOctober()
    await screen.findByText('已确认')

    expect(screen.getByRole('button', { name: '重新生成' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '确认' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '保存参数' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: '生成对账单' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: /导出全部 xlsx/ })).toHaveAttribute('href', '/api/tencent/statements/2026-10/export')

    await user.selectOptions(screen.getByLabelText('供应商'), 'Studio A')
    expect(screen.getByRole('link', { name: /导出 Studio A/ })).toHaveAttribute(
      'href',
      '/api/tencent/statements/2026-10/export?supplier=Studio%20A',
    )
    expect(screen.queryByText('Proxy Co', { selector: 'td' })).not.toBeInTheDocument()
  })

  it('generates a draft, then confirms it', async () => {
    const user = userEvent.setup()
    const calls = mockApi({
      ...statementRoutes(404),
      'POST /api/tencent/statements/2026-10/generate': snapshot('draft', [line({})]),
      'POST /api/tencent/statements/2026-10/confirm': snapshot('confirmed', [line({})]),
    })
    render(<ConfirmHostAnd><TencentStatement /></ConfirmHostAnd>)
    await openOctober()
    await user.click(screen.getByRole('button', { name: '生成对账单' }))
    await screen.findByText('草稿')
    expect(screen.getByRole('button', { name: '重新生成' })).toBeEnabled()
    expect(screen.getByRole('link', { name: /导出全部 xlsx/ })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '确认' }))
    await user.click(await screen.findByRole('button', { name: '确认对账单' }))
    await screen.findByText('已确认')
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual(
      expect.arrayContaining(['POST /api/tencent/statements/2026-10/generate', 'POST /api/tencent/statements/2026-10/confirm']),
    )
  })

  it('saves the month’s rates, discount and tax rate and reloads the bill', async () => {
    const user = userEvent.setup()
    const calls = mockApi({
      ...statementRoutes(404),
      'PUT /api/tencent/params/months': { ok: true },
    })
    render(<TencentStatement />)
    await openOctober()
    await user.type(screen.getByLabelText('参考汇率（K）'), '7.1')
    await user.type(screen.getByLabelText('折扣（I）'), '0.9')
    await user.click(screen.getByRole('button', { name: '保存参数' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({
      month: '2026-10', fx_rate: null, ref_fx_rate: 7.1, discount: 0.9, tax_rate: null,
    })
    await waitFor(() => expect(calls.filter((c) => c.url === '/api/tencent/statement?month=2026-10').length).toBeGreaterThanOrEqual(2))
  })
})

describe('腾讯一站 · 月对账 · proxy 上游', () => {
  it('shows the token masked and sends a new token only when typed', async () => {
    const user = userEvent.setup()
    const calls = mockApi({
      ...statementRoutes(404),
      'PUT /api/tencent/settings': (body: any) => ({
        proxy_upstream: { url: body.proxy_upstream.url, token_set: true, token_last4: 'ABCD', user: body.proxy_upstream.user, socks: '' },
        system_fx_rate: 7.2,
        upload_discount_pct: 88,
      }),
    })
    render(<TencentStatement />)
    await openOctober()
    expect(await screen.findByText('管理令牌（已设置 …WXYZ）')).toBeInTheDocument()
    expect(screen.getByLabelText('上游地址')).toHaveValue('http://up.example:3000')

    await user.clear(screen.getByLabelText('上游地址'))
    await user.type(screen.getByLabelText('上游地址'), 'http://43.0.0.1:3000')
    await user.click(screen.getByRole('button', { name: '保存上游' }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(1))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ proxy_upstream: { url: 'http://43.0.0.1:3000', user: '1' } })

    await user.type(screen.getByLabelText('管理令牌'), 'new-token-ABCD')
    await user.click(screen.getByRole('button', { name: '保存上游' }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PUT')).toHaveLength(2))
    expect(calls.filter((c) => c.method === 'PUT')[1].body.proxy_upstream.token).toBe('new-token-ABCD')
    expect(await screen.findByText('管理令牌（已设置 …ABCD）')).toBeInTheDocument()
    expect(screen.getByLabelText('管理令牌')).toHaveValue('')
  })
})

function ConfirmHostAnd(props: { children: ReactNode }) {
  return (
    <>
      <ConfirmHost />
      {props.children}
    </>
  )
}

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
