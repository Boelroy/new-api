import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  billingDate,
  request,
  setSession,
  type DailySummary,
  type Session,
  type Source,
  type Statement,
  type StatementLine,
  type Supplier,
} from '../api'
import App from '../App'
import { Money } from '../components'
import EntryForm from '../EntryForm'
import i18n from '../i18n'
import { SettlementOverview, SettlementSheet } from '../SettlementView'
import StatementView from '../StatementView'

const supplier: Supplier = { id: 11, name: 'Supplier Alpha', currency: 'CNY' }
const session: Session = {
  user: {
    id: 3,
    username: 'supplier-alpha',
    role: 'supplier',
    supplier_id: 11,
    disabled: false,
  },
  csrf_token: 'billing-csrf',
}
function reportSource(): Source {
  return {
    id: 1,
    supplier_id: 11,
    name: 'Dev OpenAI',
    kind: 'report',
    base_url: 'https://reports.example.test',
    credential_env: 'SUPPLIER_BILLING_SOURCE_DEV_TOKEN',
    channel_ids: '22',
    selector: '',
    exclude_selector: false,
    profile_id: 0,
    gateway_user_id: 1,
    usage_type: 'OpenAI',
    settlement_mode: 'settlement',
    direction: 'expense',
    discount: '1.000000',
    fx_rate: '7.000000',
    currency: 'USD',
    last_sync_at: 0,
    last_error: '',
  }
}
function dailySummary(owner = supplier, amount = '100.000000'): DailySummary {
  return {
    timezone: 'Asia/Shanghai',
    start: '2026-10-01',
    end: '2026-10-01',
    heads: [owner],
    totals: [
      {
        supplier_id: owner.id,
        supplier_name: owner.name,
        gateway_usage: amount,
        request_count: '12',
      },
    ],
    days: [
      {
        date: '2026-10-01',
        gateway_usage: amount,
        request_count: '12',
        heads: [
          {
            supplier_id: owner.id,
            supplier_name: owner.name,
            gateway_usage: amount,
            request_count: '12',
            sources_synced: true,
            vendors: [
              {
                vendor_id: owner.id,
                vendor_name: `${owner.name} studio`,
                gateway_usage: amount,
                request_count: '12',
              },
            ],
          },
        ],
      },
    ],
  }
}
function statement(owner = supplier, balance = '100.000000'): Statement {
  return {
    supplier: owner,
    balance,
    opening_balance: '150.000000',
    total_recharges: '0.000000',
    total_usage: '50.000000',
    gateway_usage: '0.000000',
    unpriced_usage: '0.000000',
    days: [
      {
        date: '2026-10-01',
        usage: '50.000000',
        gateway_usage: '0.000000',
        unpriced_usage: '0.000000',
        request_count: '0',
        recharges: '0.000000',
        closing_balance: balance,
        sources_synced: true,
      },
    ],
    usage: [],
    recharges: [],
    ledger: [],
    source_status: [],
  }
}
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}
function app() {
  return render(
    <I18nextProvider i18n={i18n}>
      <App />
    </I18nextProvider>
  )
}

beforeEach(async () => {
  setSession(null)
  localStorage.clear()
  await i18n.changeLanguage('en')
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
  setSession(null)
})

describe('supplier billing workspace', () => {
  it('lets a supplier set its own price without manager-only claim actions', async () => {
    const usd: Supplier = { id: 21, name: '皮皮', currency: 'USD' }
    const onPrice = vi.fn()
    render(
      <I18nextProvider i18n={i18n}>
        <SettlementSheet
          statement={{
            ...statement(usd, '0.000000'),
            lines: [
              {
                date: '2026-10-30',
                kind: 'usage',
                usage_type: 'aws',
                description: '',
                origin: 'sync',
                quantity: '100.000000',
                unit: 'gateway_usd',
                unit_price: '',
                currency: 'USD',
                discount: '',
                fx_rate: '',
                rate_mode: 'multiply',
                settlement_status: 'unpriced',
                direction: 'expense',
                original_amount: '0.000000',
                change: '0.000000',
                balance: '0.000000',
                note: '',
                recharge_id: 0,
                claim: null,
              },
            ],
          }}
          onPrice={onPrice}
        />
      </I18nextProvider>
    )
    const row = screen.getByRole('row', { name: /aws/ })
    expect(
      within(row).queryByRole('button', { name: 'Supplier claim' })
    ).not.toBeInTheDocument()
    await userEvent
      .setup()
      .click(within(row).getByRole('button', { name: 'Set price' }))
    expect(onPrice).toHaveBeenCalled()
  })
  it('breaks each supplier day into product lines in the settlement overview', () => {
    const usd: Supplier = { id: 21, name: '皮皮', currency: 'USD' }
    const day = statement(usd, '-310.000000').days[0]
    render(
      <I18nextProvider i18n={i18n}>
        <SettlementOverview
          overview={{
            suppliers: [
              {
                ...statement(usd, '-310.000000'),
                days: [
                  {
                    ...day,
                    products: [
                      {
                        usage_type: 'aws',
                        unit: 'gateway_usd',
                        quantity: '100.000000',
                        unpriced: '0.000000',
                        amount: '300.000000',
                        request_count: 9,
                      },
                      {
                        usage_type: 'ccmax',
                        unit: 'cny_list',
                        quantity: '3058.350000',
                        unpriced: '3058.350000',
                        amount: '0.000000',
                        request_count: 0,
                      },
                    ],
                  },
                ],
              },
            ],
          }}
          onInitial={vi.fn()}
          onOpen={vi.fn()}
        />
      </I18nextProvider>
    )
    const aws = screen.getByRole('row', { name: /aws/ })
    expect(within(aws).getByText('$300.00')).toBeInTheDocument()
    const ccmax = screen.getByRole('row', { name: /ccmax/ })
    expect(within(ccmax).getByText('¥3,058.35')).toBeInTheDocument()
    expect(within(ccmax).getByText('Unpriced')).toBeInTheDocument()
  })
  it('maps remote channel tags with recent values, owners and an all-except option', async () => {
    const usd: Supplier = { id: 21, name: '皮皮', currency: 'USD' }
    const alice: Supplier = { id: 22, name: 'Alice', currency: 'USD' }
    const bodies: Record<string, unknown>[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init: RequestInit) => {
        bodies.push({ url, ...JSON.parse(String(init.body)) })
        if (url === '/api/sources/discover') {
          return response([
            {
              value: 'promi1',
              quantity: '17946.24',
              unit: 'gateway_usd',
              source_id: 0,
              source_name: '',
              supplier_id: 0,
            },
            {
              value: 'alice',
              quantity: '10267.6',
              unit: 'gateway_usd',
              source_id: 9,
              source_name: 'Alice tags',
              supplier_id: 22,
            },
          ])
        }
        return response({ id: 30 }, 201)
      })
    )
    const onSaved = vi.fn()
    const user = userEvent.setup()
    render(
      <I18nextProvider i18n={i18n}>
        <EntryForm
          target={{ kind: 'source' }}
          supplier={usd}
          suppliers={[usd, alice]}
          start='2026-10-30'
          end='2026-10-30'
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      </I18nextProvider>
    )
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Mapping type' }),
      'remote_tag'
    )
    expect(
      screen.queryByRole('textbox', { name: /Channel IDs/ })
    ).not.toBeInTheDocument()
    await user.type(
      screen.getByRole('textbox', { name: 'Source name' }),
      '七牛'
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Report Service URL' }),
      'https://gw.example.test/report'
    )
    await user.type(
      screen.getByRole('textbox', { name: /Credential environment variable/ }),
      'SUPPLIER_BILLING_SOURCE_DEV'
    )
    await user.type(
      screen.getByRole('spinbutton', { name: /Remote profile ID/ }),
      '1'
    )
    await user.type(screen.getByRole('textbox', { name: 'Usage type' }), '七牛')
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Settlement mode' }),
      'usage_only'
    )
    await user.click(screen.getByRole('button', { name: 'Load recent values' }))
    expect(await screen.findByText('Alice · Alice tags')).toBeInTheDocument()
    expect(screen.getByText('Unmapped')).toBeInTheDocument()
    expect(bodies[0]).toEqual({
      url: '/api/sources/discover',
      kind: 'remote_tag',
      base_url: 'https://gw.example.test/report',
      credential_env: 'SUPPLIER_BILLING_SOURCE_DEV',
      profile_id: 1,
      gateway_user_id: 0,
    })
    await user.click(
      screen.getByRole('checkbox', {
        name: 'Map every tag except the selected ones',
      })
    )
    await user.click(screen.getByRole('checkbox', { name: /alice/ }))
    await user.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(bodies[1]).toMatchObject({
      url: '/api/sources',
      kind: 'remote_tag',
      supplier_id: 21,
      profile_id: 1,
      selector: 'alice',
      exclude_selector: true,
      direction: 'expense',
      currency: 'USD',
      settlement_mode: 'usage_only',
    })
    expect(bodies[1]).not.toHaveProperty('channel_ids')
  })
  it('shows sheet rows with running balances, claim gaps and posts a CNY ÷ rate price', async () => {
    const usd: Supplier = { id: 21, name: '皮皮', currency: 'USD' }
    const line = (value: Partial<StatementLine>): StatementLine => ({
      date: '2026-10-30',
      kind: 'usage',
      usage_type: 'openai 号池',
      description: '',
      origin: 'sync',
      quantity: '122.407600',
      unit: 'gateway_usd',
      unit_price: '3.9',
      currency: 'CNY',
      discount: '1',
      fx_rate: '6.7',
      rate_mode: 'divide',
      settlement_status: 'settled',
      direction: 'expense',
      original_amount: '477.389640',
      change: '-71.252185',
      balance: '39162.967815',
      note: '',
      recharge_id: 0,
      claim: null,
      ...value,
    })
    const sheet: Statement = {
      ...statement(usd, '99162.967815'),
      opening_balance: '39234.220000',
      initial_balance: null,
      usage: ['gpt-6-astra', 'gpt-4.1', 'gpt-6-astra'].map((model, id) => ({
        id,
        date: '2026-10-30',
        source_id: 1,
        source_name: 'Dev OpenAI',
        usage_type: 'openai 号池',
        description: model,
        quantity: model === 'gpt-4.1' ? '22.4076' : '50',
        unit: 'gateway_usd',
        unit_price: '3.9',
        currency: 'CNY',
        discount: '1',
        fx_rate: '6.7',
        rate_mode: 'divide' as const,
        amount: '1.000000',
        origin: 'sync' as const,
        settlement_status: 'settled' as const,
        created_by: 1,
        created_at: 0,
        request_count: 2,
      })),
      lines: [
        line({
          claim: {
            id: 1,
            supplier_id: 21,
            date: '2026-10-30',
            usage_type: 'openai 号池',
            quantity: '122.4076',
            amount: '71.25',
            note: '',
          },
        }),
        line({
          usage_type: 'aws',
          settlement_status: 'unpriced',
          change: '0.000000',
          original_amount: '0.000000',
          unit_price: '',
          fx_rate: '',
          rate_mode: 'multiply',
        }),
        line({
          kind: 'recharge',
          usage_type: '',
          change: '60000.000000',
          balance: '99162.967815',
        }),
      ],
    }
    const onPrice = vi.fn()
    const user = userEvent.setup()
    render(
      <I18nextProvider i18n={i18n}>
        <SettlementSheet
          statement={sheet}
          onPrice={onPrice}
          onClaim={vi.fn()}
          onInitial={vi.fn()}
        />
      </I18nextProvider>
    )
    const pool = screen.getByRole('row', { name: /openai 号池/ })
    expect(within(pool).getByText('÷ 6.7')).toBeInTheDocument()
    expect(within(pool).getByText(/58\.21/)).toBeInTheDocument()
    expect(within(pool).getByText('Matched')).toBeInTheDocument()
    expect(within(pool).getAllByText('$71.25').length).toBeGreaterThan(0)
    expect(
      within(screen.getByRole('row', { name: /aws/ })).getByText('Unpriced')
    ).toBeInTheDocument()
    expect(
      within(screen.getByRole('row', { name: /Recharge/ })).getAllByText(
        '$99,162.97'
      )
    ).not.toHaveLength(0)
    expect(screen.getByText('Not set')).toBeInTheDocument()
    await user.click(within(pool).getByRole('button', { name: 'Details' }))
    const astraRows = screen.getAllByRole('row', { name: /gpt-6-astra/ })
    const astra = astraRows[astraRows.length - 1]
    expect(within(astra).getByText('$100.00')).toBeInTheDocument()
    expect(within(astra).getByText('4')).toBeInTheDocument()
    expect(
      screen.getAllByRole('row', { name: /gpt-4\.1/ }).length
    ).toBeGreaterThan(0)
    await user.click(within(pool).getByRole('button', { name: 'Set price' }))
    expect(onPrice).toHaveBeenCalledWith(sheet.lines?.[0])

    const submitted: unknown[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init: RequestInit) => {
        submitted.push(JSON.parse(String(init.body)))
        return response({ ok: true, priced: 2 })
      })
    )
    const onSaved = vi.fn()
    render(
      <I18nextProvider i18n={i18n}>
        <EntryForm
          target={{
            kind: 'price',
            line: line({ settlement_status: 'unpriced' }),
          }}
          supplier={usd}
          suppliers={[usd]}
          start='2026-10-30'
          end='2026-10-30'
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      </I18nextProvider>
    )
    await user.type(
      screen.getByRole('spinbutton', { name: /Unit price/ }),
      '3.9'
    )
    await user.type(
      screen.getByRole('spinbutton', { name: /Exchange rate/ }),
      '6.7'
    )
    expect(screen.getByRole('status')).toHaveTextContent('58.21%')
    await user.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(submitted[0]).toEqual({
      supplier_id: 21,
      date: '2026-10-30',
      usage_type: 'openai 号池',
      unit_price: '3.9',
      currency: 'CNY',
      fx_rate: '6.7',
    })
  })
  it('defaults to all heads and opens a day into head and supplier usage without financial tables', async () => {
    const second: Supplier = { id: 12, name: 'Supplier Beta', currency: 'USD' }
    const data = dailySummary(supplier, '100.000000')
    const beta = dailySummary(second, '200.000000')
    data.heads.push(second)
    data.totals.push(beta.totals[0])
    data.days[0].heads.push(beta.days[0].heads[0])
    data.days[0].gateway_usage = '300.000000'
    data.days[0].request_count = '24'
    data.days[0].heads[1].sources_synced = false
    const fetcher = vi.fn(async (url: string) => {
      if (url === '/api/session') {
        return response({
          ...session,
          user: { ...session.user, role: 'billing_manager', supplier_id: 0 },
        })
      }
      if (url === '/api/suppliers') return response([supplier, second])
      if (url.startsWith('/api/daily-summary?')) return response(data)
      throw new Error(`Unexpected URL: ${url}`)
    })
    vi.stubGlobal('fetch', fetcher)
    const user = userEvent.setup()
    app()
    const row = await screen.findByRole('row', { name: /2026-10-01/ })
    expect(screen.getByRole('combobox', { name: 'Supplier head' })).toHaveValue(
      '0'
    )
    expect(
      screen.getByRole('columnheader', { name: 'Supplier Alpha (USD)' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('columnheader', { name: 'Supplier Beta (USD)' })
    ).toBeInTheDocument()
    expect(within(row).getByText('$300.00')).toBeInTheDocument()
    expect(within(row).getByText('Awaiting sync')).toBeInTheDocument()
    expect(screen.getAllByRole('table')).toHaveLength(1)
    expect(
      fetcher.mock.calls.some(
        ([url]) =>
          url.includes('supplier_id=') || url.startsWith('/api/statement')
      )
    ).toBe(false)
    const expand = within(row).getByRole('button', {
      name: 'View supplier breakdown for 2026-10-01',
    })
    await user.click(expand)
    expect(expand).toHaveAttribute('aria-expanded', 'true')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: 'Supplier Alpha' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: 'Supplier Beta' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('row', { name: /^Supplier Alpha studio/ })
    ).toHaveTextContent('$100.00')
    expect(
      screen.getByRole('row', { name: /^Supplier Beta studio/ })
    ).toHaveTextContent('$200.00')
    await user.click(expand)
    expect(
      screen.queryByRole('heading', { name: 'Supplier Alpha' })
    ).not.toBeInTheDocument()
    expect(expand).toHaveAttribute('aria-expanded', 'false')
  })

  it('groups supplier relationships by head while leaving technical source data unloaded', async () => {
    const second: Supplier = { id: 12, name: 'Supplier Beta', currency: 'USD' }
    const fetcher = vi.fn(async (url: string) => {
      if (url === '/api/session') {
        return response({
          ...session,
          user: { ...session.user, role: 'billing_manager', supplier_id: 0 },
        })
      }
      if (url === '/api/suppliers') return response([supplier, second])
      if (url.startsWith('/api/daily-summary?')) return response(dailySummary())
      if (url === '/api/vendors') {
        return response([
          {
            id: 43,
            supplier_id: 11,
            name: 'Alice studio',
            source_studio: 'internal-alice-binding',
            key_count: 2,
          },
          {
            id: 47,
            supplier_id: 12,
            name: 'Pipi studio',
            source_studio: 'internal-pipi-binding',
            key_count: 3,
          },
        ])
      }
      throw new Error(`Unexpected URL: ${url}`)
    })
    vi.stubGlobal('fetch', fetcher)
    const user = userEvent.setup()
    app()
    await user.click(
      await screen.findByRole('button', { name: 'Supplier relationships' })
    )
    const alice = await screen.findByRole('row', { name: /Alice studio/ })
    expect(within(alice).getByRole('cell', { name: '2' })).toBeInTheDocument()
    expect(screen.getByRole('row', { name: /Pipi studio/ })).toHaveTextContent(
      '3'
    )
    expect(
      screen.getByRole('heading', { name: 'Supplier Alpha' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: 'Supplier Beta' })
    ).toBeInTheDocument()
    expect(screen.queryByText('internal-alice-binding')).not.toBeInTheDocument()
    expect(screen.queryByText('Channel IDs')).not.toBeInTheDocument()
    expect(screen.queryByText('Report Service URL')).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Connection settings' })
    ).toHaveAttribute('aria-expanded', 'false')
    expect(fetcher.mock.calls.some(([url]) => url === '/api/sources')).toBe(
      false
    )
  })

  it('scopes a supplier head login to its own daily usage and relationships', async () => {
    const fetcher = vi.fn(async (url: string) => {
      if (url === '/api/session') return response(session)
      if (url === '/api/suppliers') return response([supplier])
      if (
        url.startsWith('/api/daily-summary?') &&
        url.includes('supplier_id=11')
      ) {
        return response(dailySummary())
      }
      if (url === '/api/vendors?supplier_id=11') {
        return response([
          {
            id: 43,
            supplier_id: 11,
            name: 'Own studio',
            source_studio: 'own-binding',
            key_count: 2,
          },
        ])
      }
      if (url.startsWith('/api/statement?') && url.includes('supplier_id=11')) {
        return response(statement())
      }
      throw new Error(`Unexpected URL: ${url}`)
    })
    vi.stubGlobal('fetch', fetcher)
    const user = userEvent.setup()
    app()
    await screen.findByRole('row', { name: /2026-10-01/ })
    const select = screen.getByRole('combobox', { name: 'Supplier head' })
    expect(select).toBeDisabled()
    expect(within(select).getAllByRole('option')).toHaveLength(1)
    expect(
      screen.queryByRole('option', { name: 'All supplier heads' })
    ).not.toBeInTheDocument()
    const finance = screen.getByRole('button', {
      name: 'Balance and payment details',
    })
    expect(finance).toHaveAttribute('aria-expanded', 'false')
    expect(
      fetcher.mock.calls.some(([url]) => url.startsWith('/api/statement'))
    ).toBe(false)
    await user.click(finance)
    await screen.findByRole('button', { name: 'Usage details' })
    expect(
      screen.queryByRole('button', { name: 'Daily totals' })
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: 'Supplier relationships' })
    )
    await screen.findByRole('row', { name: /Own studio/ })
    expect(
      screen.queryByRole('button', { name: 'Connection settings' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Supplier heads and accounts' })
    ).not.toBeInTheDocument()
    expect(
      fetcher.mock.calls.some(
        ([url]) => url === '/api/vendors' || url === '/api/sources'
      )
    ).toBe(false)
  })

  it('switches a source to usage only without sending fallback prices', async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ ok: true }))
    vi.stubGlobal('fetch', fetcher)
    const saved = vi.fn()
    const user = userEvent.setup()
    render(
      <I18nextProvider i18n={i18n}>
        <EntryForm
          target={{ kind: 'source', source: reportSource() }}
          supplier={supplier}
          suppliers={[supplier]}
          start='2026-10-01'
          end='2026-10-02'
          onClose={vi.fn()}
          onSaved={saved}
        />
      </I18nextProvider>
    )
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Settlement mode' }),
      'usage_only'
    )
    expect(
      screen.queryByRole('spinbutton', { name: /Discount/ })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('spinbutton', { name: /Exchange rate/ })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(saved).toHaveBeenCalled())
    expect(fetcher).toHaveBeenCalledWith(
      '/api/sources/1',
      expect.objectContaining({ method: 'PUT' })
    )
    const body = JSON.parse(String(fetcher.mock.calls[0][1].body)) as Record<
      string,
      unknown
    >
    expect(body).toMatchObject({
      settlement_mode: 'usage_only',
      discount: '',
      fx_rate: '',
      gateway_user_id: 1,
    })
  })

  it('requires explicit pricing when an unpriced source switches to settlement', async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ ok: true }))
    vi.stubGlobal('fetch', fetcher)
    const source: Source = {
      ...reportSource(),
      settlement_mode: 'usage_only',
      discount: '',
      fx_rate: '',
    }
    const saved = vi.fn()
    const user = userEvent.setup()
    render(
      <I18nextProvider i18n={i18n}>
        <EntryForm
          target={{ kind: 'source', source }}
          supplier={supplier}
          suppliers={[supplier]}
          start='2026-10-01'
          end='2026-10-02'
          onClose={vi.fn()}
          onSaved={saved}
        />
      </I18nextProvider>
    )
    await user.selectOptions(
      screen.getByRole('combobox', { name: /Settlement mode/ }),
      'settlement'
    )
    const discount = screen.getByRole('spinbutton', { name: /Discount/ })
    const rate = screen.getByRole('spinbutton', { name: /Exchange rate/ })
    expect(discount).toHaveValue(null)
    expect(rate).toHaveValue(null)
    await user.click(screen.getByRole('button', { name: 'Submit' }))
    expect(fetcher).not.toHaveBeenCalled()
    await user.type(discount, '88')
    await user.type(rate, '7')
    await user.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(saved).toHaveBeenCalled())
    expect(JSON.parse(String(fetcher.mock.calls[0][1].body))).toMatchObject({
      settlement_mode: 'settlement',
      discount: '0.880000',
      fx_rate: '7',
    })
  })

  it('shows imported gateway volume and exact request counts without presenting unpriced usage as a zero charge', async () => {
    const data = statement(supplier, '500.000000')
    data.opening_balance = '500.000000'
    data.total_usage = '0.000000'
    data.gateway_usage = '300.000000'
    data.unpriced_usage = '300.000000'
    data.days = [
      {
        ...data.days[0],
        usage: '0.000000',
        gateway_usage: '300.000000',
        unpriced_usage: '300.000000',
        request_count: '9007199254740993',
      },
    ]
    data.source_status = [
      {
        id: 1,
        name: 'Dev OpenAI',
        last_sync_at: 1790812800,
        sync_failed: false,
        missing_days: 0,
      },
    ]
    data.usage = [
      {
        id: 1,
        date: '2026-10-01',
        source_id: 1,
        source_name: 'Dev OpenAI',
        usage_type: 'OpenAI',
        description: 'OpenAI unpriced batch',
        quantity: '300.000000',
        unit: 'USD',
        unit_price: '',
        currency: 'USD',
        discount: '',
        fx_rate: '',
        amount: '0.000000',
        origin: 'sync',
        settlement_status: 'unpriced',
        created_by: 1,
        created_at: 1790812800,
      },
    ]
    const user = userEvent.setup()
    render(
      <I18nextProvider i18n={i18n}>
        <StatementView statement={data} />
      </I18nextProvider>
    )
    expect(
      screen.getByRole('columnheader', { name: 'Gateway usage (USD)' })
    ).toBeInTheDocument()
    expect(screen.getAllByText('$300.00').length).toBeGreaterThan(0)
    expect(screen.getByText('9,007,199,254,740,993')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(
      'Unpriced usage does not reduce the prepaid balance. Configure pricing and sync again to settle it.'
    )
    await user.click(screen.getByRole('button', { name: 'Usage details' }))
    const row = screen.getByRole('row', { name: /OpenAI unpriced batch/ })
    expect(within(row).getByText('Unpriced')).toBeInTheDocument()
    expect(within(row).queryByText(/(?:[$¥]|CN¥)0\.00/)).not.toBeInTheDocument()
  })

  it('shows gateway user filters, keeps existing bindings immutable and submits a numeric source user ID', async () => {
    const source = reportSource()
    let submitted: Record<string, unknown> | undefined
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url === '/api/session') {
          return response({
            ...session,
            user: { ...session.user, role: 'billing_manager', supplier_id: 0 },
          })
        }
        if (url === '/api/suppliers') return response([supplier])
        if (url.startsWith('/api/statement?')) return response(statement())
        if (url.startsWith('/api/daily-summary?')) {
          return response(dailySummary())
        }
        if (url.startsWith('/api/vendors')) return response([])
        if (url === '/api/sources' && init?.method === 'POST') {
          submitted = JSON.parse(String(init.body)) as Record<string, unknown>
          return response({ ok: true })
        }
        if (url === '/api/sources') {
          return response([
            source,
            {
              ...source,
              id: 2,
              name: 'All users source',
              channel_ids: '24',
              gateway_user_id: 0,
            },
          ])
        }
        throw new Error(`Unexpected URL: ${url}`)
      })
    )
    const user = userEvent.setup()
    app()
    await user.click(
      await screen.findByRole('button', { name: 'Supplier relationships' })
    )
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Supplier head' }),
      '11'
    )
    await user.click(
      screen.getByRole('button', { name: 'Connection settings' })
    )
    const row = await screen.findByRole('row', { name: /Dev OpenAI/ })
    expect(within(row).getByRole('cell', { name: '1' })).toBeInTheDocument()
    expect(screen.getByText('All gateway users')).toBeInTheDocument()
    await user.click(within(row).getByRole('button', { name: 'Edit' }))
    expect(
      screen.getByRole('spinbutton', { name: /Gateway user ID/ })
    ).toHaveAttribute('readonly')
    expect(
      screen.getByRole('spinbutton', { name: /Gateway user ID/ })
    ).toHaveValue(1)
    await user.click(screen.getByRole('button', { name: 'Close dialog' }))
    await user.click(screen.getByRole('button', { name: 'Add source' }))
    const gatewayUser = screen.getByRole('spinbutton', {
      name: /Gateway user ID/,
    })
    expect(gatewayUser).toHaveValue(0)
    await user.type(
      screen.getByRole('textbox', { name: 'Source name' }),
      'Dev Admin'
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Report Service URL' }),
      'https://reports.example.test'
    )
    await user.type(
      screen.getByRole('textbox', { name: /Credential environment variable/ }),
      'SUPPLIER_BILLING_SOURCE_DEV_TOKEN'
    )
    await user.type(screen.getByRole('textbox', { name: /Channel IDs/ }), '23')
    await user.type(
      screen.getByRole('textbox', { name: 'Usage type' }),
      'OpenAI'
    )
    await user.type(
      screen.getByRole('spinbutton', { name: /Exchange rate/ }),
      '7'
    )
    await user.clear(gatewayUser)
    await user.type(gatewayUser, '1')
    await user.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(submitted).toMatchObject({
      gateway_user_id: 1,
      supplier_id: 11,
      channel_ids: '23',
    })
  })

  it('uses the configured URL prefix while retaining billing session protections', async () => {
    vi.stubEnv('BASE_URL', '/supplier-billing/')
    const fetcher = vi.fn().mockResolvedValue(response({ ok: true }))
    vi.stubGlobal('fetch', fetcher)
    setSession(session)

    await request('/usage', { method: 'POST', body: { supplier_id: 11 } })

    expect(fetcher).toHaveBeenCalledWith(
      '/supplier-billing/api/usage',
      expect.objectContaining({
        method: 'POST',
        credentials: 'same-origin',
        body: JSON.stringify({ supplier_id: 11 }),
        headers: expect.objectContaining({
          'X-CSRF-Token': 'billing-csrf',
          'X-Requested-With': 'supplier-billing',
        }),
      })
    )
  })

  it('posts supplier usage immediately, keeps retries idempotent and exposes no manager actions', async () => {
    const submitted: Record<string, unknown>[] = []
    const headers: HeadersInit[] = []
    const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/session') return response(session)
      if (url === '/api/suppliers') return response([supplier])
      if (url.startsWith('/api/statement?')) return response(statement())
      if (url.startsWith('/api/daily-summary?')) return response(dailySummary())
      if (url.startsWith('/api/vendors')) return response([])
      if (url === '/api/usage') {
        submitted.push(
          JSON.parse(String(init?.body)) as Record<string, unknown>
        )
        headers.push(init?.headers ?? {})
        if (submitted.length === 1) {
          return response({ error: 'Database operation failed' }, 503)
        }
        return response({ ok: true })
      }
      throw new Error(`Unexpected URL: ${url}`)
    })
    vi.stubGlobal('fetch', fetcher)
    const user = userEvent.setup()
    app()
    await screen.findByRole('button', { name: 'Add usage' })
    expect(
      screen.queryByRole('button', { name: 'Record recharge' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Supplier heads and accounts' })
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole('combobox', { name: 'Supplier head' })
    ).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Add usage' }))
    expect(
      screen.getByText(
        'Submitted usage is charged immediately to the prepaid balance.'
      )
    ).toBeInTheDocument()
    await user.type(
      screen.getByRole('textbox', { name: 'Usage type' }),
      'OpenAI'
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Description' }),
      'Additional batch'
    )
    await user.type(screen.getByRole('spinbutton', { name: 'Quantity' }), '2')
    await user.type(screen.getByRole('textbox', { name: 'Unit' }), 'request')
    await user.type(
      screen.getByRole('spinbutton', { name: 'Unit price' }),
      '10'
    )
    await user.clear(screen.getByRole('spinbutton', { name: /Discount/ }))
    await user.type(screen.getByRole('spinbutton', { name: /Discount/ }), '88')
    await user.click(screen.getByRole('button', { name: 'Submit' }))
    await screen.findByRole('alert')
    expect(screen.getByRole('textbox', { name: 'Description' })).toHaveValue(
      'Additional batch'
    )
    await user.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(submitted).toHaveLength(2)
    expect(submitted[1]).toEqual(submitted[0])
    expect(submitted[0]).toMatchObject({
      supplier_id: 11,
      quantity: '2',
      unit_price: '10',
      currency: 'CNY',
      discount: '0.880000',
      fx_rate: '1',
    })
    expect(new Headers(headers[0]).get('X-CSRF-Token')).toBe('billing-csrf')
  })

  it('clears the previous daily summary while another supplier head is loading', async () => {
    const second: Supplier = { id: 12, name: 'Supplier Beta', currency: 'USD' }
    let finish: ((value: Response) => void) | undefined
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (url === '/api/session') {
          return response({
            ...session,
            user: { ...session.user, role: 'billing_manager', supplier_id: 0 },
          })
        }
        if (url === '/api/suppliers') return response([supplier, second])
        if (
          url.startsWith('/api/daily-summary?') &&
          !url.includes('supplier_id=')
        ) {
          return response(dailySummary(supplier, '123.456789'))
        }
        if (url.includes('supplier_id=12')) {
          return new Promise<Response>((resolve) => {
            finish = resolve
          })
        }
        throw new Error(`Unexpected URL: ${url}`)
      })
    )
    const user = userEvent.setup()
    app()
    await screen.findAllByText(/123\.46/)
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Supplier head' }),
      '12'
    )
    expect(screen.queryAllByText(/123\.46/)).toHaveLength(0)
    expect(screen.getByRole('status')).toHaveTextContent('Loading…')
    await act(async () => finish?.(response(dailySummary(second, '42.000000'))))
    await screen.findAllByText('$42.00')
  })

  it('treats session service failure as a retryable failure instead of showing a login form', async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(response({ error: 'Service unavailable' }, 503))
      .mockResolvedValue(response({ error: 'Please sign in.' }, 401))
    vi.stubGlobal('fetch', fetcher)
    const user = userEvent.setup()
    app()
    await screen.findByRole('alert')
    expect(
      screen.queryByRole('textbox', { name: 'Username' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByRole('textbox', { name: 'Username' })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('distinguishes pending source days from synced zero usage and keeps today provisional', () => {
    const data = statement()
    data.source_status = [
      {
        id: 1,
        name: 'Dev OpenAI',
        last_sync_at: 0,
        sync_failed: true,
        missing_days: 1,
      },
    ]
    data.days = [
      {
        date: billingDate(new Date(Date.now() - 172800000)),
        usage: '0.000000',
        recharges: '0.000000',
        closing_balance: '100.000000',
        sources_synced: false,
        gateway_usage: '0.000000',
        unpriced_usage: '0.000000',
        request_count: '0',
      },
      {
        date: billingDate(new Date(Date.now() - 86400000)),
        usage: '0.000000',
        recharges: '0.000000',
        closing_balance: '100.000000',
        sources_synced: true,
        gateway_usage: '0.000000',
        unpriced_usage: '0.000000',
        request_count: '0',
      },
      {
        date: billingDate(),
        usage: '0.000000',
        recharges: '0.000000',
        closing_balance: '100.000000',
        sources_synced: true,
        gateway_usage: '0.000000',
        unpriced_usage: '0.000000',
        request_count: '0',
      },
    ]
    render(
      <I18nextProvider i18n={i18n}>
        <StatementView statement={data} />
      </I18nextProvider>
    )
    expect(screen.getByText('Dev OpenAI')).toBeInTheDocument()
    expect(screen.getByText('Sync failed')).toBeInTheDocument()
    expect(screen.getByText('Pending days: 1')).toBeInTheDocument()
    expect(screen.getByText('Awaiting sync')).toBeInTheDocument()
    expect(screen.getByText('Sources synced')).toBeInTheDocument()
    expect(screen.getByText('In progress')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(
      'Unsynced sources may add expenses to this statement.'
    )
  })

  it('preserves all six monetary decimals for large balances and responds to language changes', async () => {
    const view = render(
      <I18nextProvider i18n={i18n}>
        <Money value='9007199254740991.123456' currency='USD' exact />
        <Money value='-0.000001' currency='USD' exact />
        <Money value='53024.050128' currency='USD' />
        <Money value='-0.004' currency='USD' />
      </I18nextProvider>
    )
    expect(view.container).toHaveTextContent('$53,024.05')
    expect(screen.getByTitle('53024.050128')).toHaveTextContent('$53,024.05')
    expect(screen.getByTitle('-0.004')).toHaveTextContent(/^\$0\.00$/)
    expect(view.container).toHaveTextContent('$9,007,199,254,740,991.123456')
    expect(view.container).toHaveTextContent('-$0.000001')
    await act(async () => {
      await i18n.changeLanguage('fr')
    })
    expect(view.container.textContent).toContain('991,123456')
    expect(view.container.textContent).toContain('-0,000001')
  })
})
