import type { ReactNode } from 'react'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import TencentUsers from '../TencentUsers'
import TencentStatement from '../TencentStatement'
import TencentPush from '../TencentPush'
import { ConfirmHost } from '../../components/feedback'
import type { TencentLine, TencentMonthConfig, TencentPushSettings, TencentSnapshot, TencentStatementPreview, TencentSummaryRow, TencentUser } from '../../api'

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
  '腾讯支付金额（不区分币种）', '参考汇率\nhttps://www.kylc.com/huilv/d-boc-usd.html', '开票金额（RMB/元）', '官方导出的账单名称',
]
const EXCEL_SUMMARY_HEADERS = ['供应商名称', '账单月份', '模型厂商名称', '付给官方的费用（不区分币种）', '开票金额（RMB/元）', '备注']

// October 2026's config, as the backend seeds it.
const octoberConfig: TencentMonthConfig = {
  company: '长河理想',
  boc_rate: 6.6728,
  boc_rate_date: '2026-11-01',
  ccmax_divisor: 7.2,
  vendors: [
    { vendor: 'azure-openai', pricing: 'usd', discount: 0.73, tax_text: '含票', note: 'newapi proxy；原金额USD，已应用约定折扣', label: 'Azure OpenAI 折扣' },
    { vendor: 'azure-claude', pricing: 'usd', discount: 0.95, tax_text: '含票', note: 'newapi proxy；原金额USD，已应用约定折扣', label: 'Azure Claude 折扣' },
    { vendor: 'ConfluxCC', pricing: 'ccmax', discount: 0.5, tax_text: '含税', note: 'CCMax；原RMB÷7.2×折扣×汇率，含税', label: 'CCMax折扣（含税）' },
    { vendor: 'zhipu', pricing: 'rmb', discount: 0.75, tax_text: '不另加税费', note: 'zhipu；人民币原价×折扣，不另加税费、不换汇', label: 'zhipu 折扣' },
  ],
}

// Lines of the October statement, priced as the backend does (full precision).
function priced(over: Partial<TencentLine> & Pick<TencentLine, 'vendor' | 'pricing' | 'official' | 'discount'>): TencentLine {
  const j = over.pricing === 'ccmax' ? (over.official / 7.2) * over.discount : over.official * over.discount
  const k = over.pricing === 'rmb' ? 1 : 6.6728
  return {
    line_key: `${over.vendor}|${over.model}|${over.account_id}`, platform: over.pricing === 'usd' ? 'proxy' : over.pricing === 'ccmax' ? 'ccmax' : 'third_party',
    supplier: '长河理想', bill_month: 10, model: '', account_id: '', key: '', tax_rate: '', bill_name: '', source: '',
    tencent: j, ref_fx_rate: k, invoice_rmb: j * k, ...over,
  }
}
const octoberLines = [
  priced({ vendor: 'azure-claude', pricing: 'usd', model: 'claude-opus-5-5', account_id: '渠道 59814, 59846, 95705', official: 568.03554, discount: 0.95, tax_rate: '含票', bill_name: '长河理想公司10月对账单_newapi-proxy后台账单.zip' }),
  priced({ vendor: 'ConfluxCC', pricing: 'ccmax', model: 'claude-opus-5, claude-opus-5-5', account_id: '全部CCMax账户', official: 1707013.71, discount: 0.5, tax_rate: '含税', bill_name: '长河理想公司10月对账单_CCMax官方日明细.csv' }),
  priced({ vendor: 'zhipu', pricing: 'rmb', model: 'glm-5.3', account_id: '250546', key: 'sk-nu****00002', official: 91409.57, discount: 0.75, tax_rate: '不另加税费', bill_name: '长河理想公司10月对账单_zhipu原始账单.xlsx' }),
]
const octoberSummary: TencentSummaryRow[] = [
  { supplier: '长河理想', bill_month: 10, vendor: 'azure-claude', pricing: 'usd', official: 785.78711, invoice_rmb: 4981.23, note: 'newapi proxy；原金额USD，已应用约定折扣', source: 'newapi proxy', lines: 3 },
  { supplier: '长河理想', bill_month: 10, vendor: 'azure-openai', pricing: 'usd', official: 1343.774884, invoice_rmb: 6545.72, note: 'newapi proxy；原金额USD，已应用约定折扣', source: 'newapi proxy', lines: 5 },
  { supplier: '长河理想', bill_month: 10, vendor: 'ConfluxCC', pricing: 'ccmax', official: 1731601.95, invoice_rmb: 802405.1, note: 'CCMax；原RMB÷7.2×折扣×汇率，含税', source: 'ccmax', lines: 1 },
  { supplier: '长河理想', bill_month: 10, vendor: 'zhipu', pricing: 'rmb', official: 212333.93, invoice_rmb: 159250.45, note: 'zhipu；人民币原价×折扣，不另加税费、不换汇', source: 'third_party', lines: 5 },
]

function preview(): TencentStatementPreview {
  return {
    month: '2026-10', bill_month: 10, config: octoberConfig, headers: [], summary_headers: [],
    lines: octoberLines, summary: octoberSummary, total_invoice_rmb: 973182.5, warnings: [],
  }
}

function snapshot(status: 'draft' | 'confirmed'): TencentSnapshot {
  return {
    statement: { month: '2026-10', status, meta: { month: '2026-10', config: octoberConfig, ccmax_first_account: 0 }, warnings: [], lines: 3, generated_at: 1, confirmed_at: 0 },
    lines: octoberLines, summary: octoberSummary, total_invoice_rmb: 973182.5,
  }
}

function statementRoutes(snap: unknown) {
  return {
    'GET /api/tencent/statement?month=2026-10': preview(),
    'GET /api/tencent/statements/2026-10': snap,
    'GET /api/tencent/config?month=2026-10': {
      month: '2026-10', config: octoberConfig, from: 'month', inherited: '2026-10',
      default_rate_date: '2026-11-01', boc_rate_source_url: 'https://www.kylc.com/huilv/d-boc-usd.html',
    },
    'GET /api/tencent/settings': { proxy_profile_id: 0, system_fx_rate: 7.2, upload_discount_pct: 88 },
    'GET /api/remote-newapi/profiles': {
      profiles: [
        { id: 1, name: 'aaaasl', host: 'http://a:3000', default_models: '', default_group: '' },
        { id: 6, name: 'ttttt', host: 'http://43.172.95.125:3000', default_models: '', default_group: '' },
      ],
    },
  }
}

// The page opens on last month; tests pin October 2026.
async function openOctober() {
  fireEvent.change(screen.getByLabelText('账单月份'), { target: { value: '2026-10' } })
  await screen.findByText('全部CCMax账户')
}

const cells = (row: HTMLElement) => within(row).getAllByRole('cell').map((c) => c.textContent)

describe('腾讯一站 · 月对账', () => {
  it('shows the October statement as the xlsx shows it: A–M lines, vendor totals, 开票金额合计 and the parameters', async () => {
    mockApi(statementRoutes(404))
    render(<TencentStatement />)
    await openOctober()
    const [summaryTable, linesTable] = screen.getAllByRole('table')
    expect(within(summaryTable).getAllByRole('columnheader').map((h) => h.textContent)).toEqual(EXCEL_SUMMARY_HEADERS)
    expect(within(linesTable).getAllByRole('columnheader').map((h) => h.textContent)).toEqual(EXCEL_LINE_HEADERS)

    const summaryRows = within(summaryTable).getAllByRole('row').slice(1)
    expect(summaryRows.map(cells)).toEqual([
      ['长河理想', '10', 'azure-claude', '785.79 USD', '4,981.23 RMB', 'newapi proxy；原金额USD，已应用约定折扣'],
      ['长河理想', '10', 'azure-openai', '1,343.77 USD', '6,545.72 RMB', 'newapi proxy；原金额USD，已应用约定折扣'],
      ['长河理想', '10', 'ConfluxCC', '1,731,601.95 RMB', '802,405.10 RMB', 'CCMax；原RMB÷7.2×折扣×汇率，含税'],
      ['长河理想', '10', 'zhipu', '212,333.93 RMB', '159,250.45 RMB', 'zhipu；人民币原价×折扣，不另加税费、不换汇'],
      ['开票金额合计', '', '973,182.50 RMB', ''],
    ])

    // Lines: G / J / L in each pricing's currency, rounded for display only.
    const row = (text: string) => within(linesTable).getByText(text).closest('tr')!
    expect(cells(row('渠道 59814, 59846, 95705'))).toEqual([
      '长河理想', '10', 'azure-claude', 'claude-opus-5-5', '渠道 59814, 59846, 95705', '', '568.04 USD', '含票', '95%',
      '539.63 USD', '6.6728', '3,600.87 RMB', '长河理想公司10月对账单_newapi-proxy后台账单.zip',
    ])
    expect(cells(row('全部CCMax账户'))).toEqual([
      '长河理想', '10', 'ConfluxCC', 'claude-opus-5, claude-opus-5-5', '全部CCMax账户', '', '1,707,013.71 RMB', '含税', '50%',
      '118,542.62 USD', '6.6728', '791,011.19 RMB', '长河理想公司10月对账单_CCMax官方日明细.csv',
    ])
    expect(cells(row('250546'))).toEqual([
      '长河理想', '10', 'zhipu', 'glm-5.3', '250546', 'sk-nu****00002', '91,409.57 RMB', '不另加税费', '75%',
      '68,557.18 RMB', '1.0000', '68,557.18 RMB', '长河理想公司10月对账单_zhipu原始账单.xlsx',
    ])

    const params = screen.getByLabelText('参数区')
    expect(params).toHaveTextContent('2026-11-01 现汇卖出价 6.6728')
    expect(params).toHaveTextContent('CCMax RMB/USD 除数 7.2')
    expect(params).toHaveTextContent('Azure OpenAI 折扣 73%')
    expect(params).toHaveTextContent('计费期间：2026-10-01 00:00至2026-11-01 00:00（北京时间，结束不含）')
    expect(screen.getByRole('button', { name: /导出 xlsx/ })).toBeDisabled()
  })

  it('locks a confirmed month and links the exports to the snapshot', async () => {
    const user = userEvent.setup()
    mockApi(statementRoutes(snapshot('confirmed')))
    render(<TencentStatement />)
    await openOctober()
    await screen.findByText('已确认')

    expect(screen.getByRole('button', { name: '重新生成' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '确认' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '保存到本月' })).toBeDisabled()
    expect(screen.getByLabelText('折扣 zhipu')).toBeDisabled()
    expect(screen.getByRole('link', { name: /导出 xlsx/ })).toHaveAttribute('href', '/api/tencent/statements/2026-10/export')
    await user.selectOptions(screen.getByLabelText('厂商'), 'zhipu')
    expect(screen.getByRole('link', { name: /导出 zhipu/ })).toHaveAttribute('href', '/api/tencent/statements/2026-10/export?vendor=zhipu')
  })

  it('generates a draft, then confirms it', async () => {
    const user = userEvent.setup()
    const calls = mockApi({
      ...statementRoutes(404),
      'POST /api/tencent/statements/2026-10/generate': snapshot('draft'),
      'POST /api/tencent/statements/2026-10/confirm': snapshot('confirmed'),
    })
    render(<ConfirmHostAnd><TencentStatement /></ConfirmHostAnd>)
    await openOctober()
    await user.click(screen.getByRole('button', { name: '生成对账单' }))
    await screen.findByText('草稿')
    expect(screen.getByRole('button', { name: '重新生成' })).toBeEnabled()
    expect(screen.getByRole('link', { name: /导出 xlsx/ })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '确认' }))
    await user.click(await screen.findByRole('button', { name: '确认对账单' }))
    await screen.findByText('已确认')
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual(
      expect.arrayContaining(['POST /api/tencent/statements/2026-10/generate', 'POST /api/tencent/statements/2026-10/confirm']),
    )
  })

  it('saves a vendor discount and a new vendor to the month’s config and reloads the bill', async () => {
    const user = userEvent.setup()
    const calls = mockApi({
      ...statementRoutes(404),
      'PUT /api/tencent/config': (body: any) => ({ ok: true, config: body.config }),
    })
    render(<TencentStatement />)
    await openOctober()
    expect(await screen.findByText('本月配置')).toBeInTheDocument()
    await user.clear(screen.getByLabelText('折扣 azure-openai'))
    await user.type(screen.getByLabelText('折扣 azure-openai'), '0.8')
    await user.click(screen.getByRole('button', { name: /添加厂商/ }))
    await user.type(screen.getByLabelText('厂商 5'), 'minimax')
    await user.click(screen.getByRole('button', { name: '保存到本月' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const body = calls.find((c) => c.method === 'PUT')?.body
    expect(body.month).toBe('2026-10')
    expect(body.config).toEqual({
      ...octoberConfig,
      vendors: [
        { ...octoberConfig.vendors[0], discount: 0.8 },
        ...octoberConfig.vendors.slice(1),
        { vendor: 'minimax', pricing: 'rmb', discount: null, tax_text: '不另加税费', note: '', label: '' },
      ],
    })
    await waitFor(() => expect(calls.filter((c) => c.url === '/api/tencent/statement?month=2026-10').length).toBeGreaterThanOrEqual(2))
  })

  it('refuses a discount outside 0–1', async () => {
    const user = userEvent.setup()
    const calls = mockApi(statementRoutes(404))
    render(<TencentStatement />)
    await openOctober()
    await screen.findByText('本月配置')
    await user.clear(screen.getByLabelText('折扣 zhipu'))
    await user.type(screen.getByLabelText('折扣 zhipu'), '75')
    await user.click(screen.getByRole('button', { name: '保存到本月' }))
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)
  })
})

describe('腾讯一站 · 月对账 · proxy 上游', () => {
  it('picks the upstream among the remote profiles', async () => {
    const user = userEvent.setup()
    const calls = mockApi({
      ...statementRoutes(404),
      'PUT /api/tencent/settings': (body: any) => ({ proxy_profile_id: body.proxy_profile_id, system_fx_rate: 7.2, upload_discount_pct: 88 }),
    })
    render(<TencentStatement />)
    await openOctober()
    await screen.findByRole('option', { name: 'ttttt（http://43.172.95.125:3000）' })
    expect(screen.getByLabelText('上游实例')).toHaveValue('0')
    await user.selectOptions(screen.getByLabelText('上游实例'), '6')
    await user.click(screen.getByRole('button', { name: '保存上游' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ proxy_profile_id: 6 })
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
