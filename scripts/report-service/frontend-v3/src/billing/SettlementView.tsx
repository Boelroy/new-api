import { ChevronDown, ChevronRight } from 'lucide-react'
import { Fragment, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge, Button, Card } from '../components/ui'
import {
  type InitialBalance,
  type Overview,
  type Statement,
  type StatementLine,
  type Supplier,
} from './api'
import { DataTable, Money, RequestCount } from './components'

const balanceKinds: Record<string, string> = {
  recharge: 'Recharge',
  recharge_reversal: 'Reversal',
  initial_balance: 'Initial balance',
  initial_balance_reversal: 'Initial balance correction',
}

function InitialBalanceSummary(props: {
  initial?: InitialBalance | null
  currency: string
  onEdit?: () => void
}) {
  const { t } = useTranslation()
  return (
    <div className='flex flex-wrap items-center gap-2 text-xs'>
      <span className='text-muted-foreground'>{t('Initial balance')}</span>
      {props.initial ? (
        <>
          <Money value={props.initial.amount} currency={props.currency} />
          <span className='text-muted-foreground'>{props.initial.date}</span>
        </>
      ) : (
        <Badge tone='warning'>{t('Not set')}</Badge>
      )}
      {props.onEdit && (
        <Button size='sm' variant='outline' onClick={props.onEdit}>
          {t('Set initial balance')}
        </Button>
      )}
    </div>
  )
}

export function SettlementOverview(props: {
  overview: Overview
  onInitial: (supplier: Supplier, initial?: InitialBalance | null) => void
  onOpen: (supplier: Supplier) => void
}) {
  const { t } = useTranslation()
  const suppliers = props.overview.suppliers
  const dates = [...(suppliers[0]?.days ?? [])].map((day) => day.date).reverse()
  return (
    <div className='space-y-5'>
      <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
        {suppliers.map((item) => (
          <Card key={item.supplier.id} className='space-y-3 p-5'>
            <div className='flex items-center gap-2'>
              <span className='text-sm font-medium'>{item.supplier.name}</span>
              <Button
                size='sm'
                variant='ghost'
                className='ml-auto'
                onClick={() => props.onOpen(item.supplier)}
              >
                {t('Open statement')}
              </Button>
            </div>
            <div>
              <div className='mono-label mb-1'>{t('Current balance')}</div>
              <div className='text-xl font-semibold'>
                <Money value={item.balance} currency={item.supplier.currency} />
              </div>
            </div>
            <dl className='grid grid-cols-2 gap-2 text-xs'>
              <dt className='text-muted-foreground'>
                {t('Period settlement')}
              </dt>
              <dd className='text-right'>
                <Money
                  value={item.total_usage}
                  currency={item.supplier.currency}
                />
              </dd>
              <dt className='text-muted-foreground'>{t('Period recharges')}</dt>
              <dd className='text-right'>
                <Money
                  value={item.total_recharges}
                  currency={item.supplier.currency}
                />
              </dd>
            </dl>
            <InitialBalanceSummary
              initial={item.initial_balance}
              currency={item.supplier.currency}
              onEdit={() =>
                props.onInitial(item.supplier, item.initial_balance)
              }
            />
            {Number(item.unpriced_usage) > 0 && (
              <p role='status' className='text-warning text-xs'>
                {t('Unpriced usage (USD)')}:{' '}
                <Money value={item.unpriced_usage} currency='USD' />
              </p>
            )}
          </Card>
        ))}
      </div>
      <DataTable
        headings={[
          'Date',
          'Supplier',
          'Product line',
          'Usage',
          'Settlement amount',
          'Payments',
          'Closing balance',
        ]}
        empty={!dates.length}
      >
        {dates.flatMap((date) =>
          suppliers.map((item) => {
            const day = item.days.find((value) => value.date === date)
            const currency = item.supplier.currency
            const payments = (
              Number(day?.recharges ?? 0) + Number(day?.initial ?? 0)
            ).toFixed(6)
            const products = day?.products?.length ? day.products : [null]
            return products.map((product, index) => (
              <tr
                key={`${date}-${item.supplier.id}-${product?.usage_type ?? ''}-${product?.unit ?? ''}`}
                className={index === 0 ? 'border-border border-t-2' : ''}
              >
                {index === 0 && (
                  <>
                    <td
                      rowSpan={products.length}
                      className='align-top whitespace-nowrap'
                    >
                      {date}
                    </td>
                    <td rowSpan={products.length} className='align-top'>
                      <Button
                        size='sm'
                        variant='ghost'
                        onClick={() => props.onOpen(item.supplier)}
                      >
                        {item.supplier.name}
                      </Button>
                    </td>
                  </>
                )}
                <td className='font-medium'>
                  {product ? product.usage_type : '—'}
                </td>
                <td className='whitespace-nowrap'>
                  {product ? (
                    <Money
                      value={product.quantity}
                      currency={product.unit === 'cny_list' ? 'CNY' : 'USD'}
                    />
                  ) : (
                    '—'
                  )}
                </td>
                <td className='whitespace-nowrap'>
                  {product && (
                    <Money value={product.amount} currency={currency} />
                  )}
                  {product && Number(product.unpriced) > 0 && (
                    <Badge tone='warning' className='ml-2'>
                      {t('Unpriced')}
                    </Badge>
                  )}
                  {!product && '—'}
                </td>
                {index === 0 && (
                  <>
                    <td rowSpan={products.length} className='align-top'>
                      <Money value={payments} currency={currency} />
                    </td>
                    <td
                      rowSpan={products.length}
                      className='align-top font-medium'
                    >
                      <Money
                        value={day?.closing_balance ?? '0'}
                        currency={currency}
                      />
                    </td>
                  </>
                )}
              </tr>
            ))
          })
        )}
      </DataTable>
    </div>
  )
}

export function SettlementSheet(props: {
  statement: Statement
  onInitial?: () => void
  onPrice?: (line: StatementLine) => void
  onClaim?: (line: StatementLine) => void
}) {
  const { t } = useTranslation()
  const statement = props.statement
  const currency = statement.supplier.currency
  const lines = statement.lines ?? []
  // Suppliers may price their own lines; claims stay with managers.
  const actions = Boolean(props.onPrice || props.onClaim)
  const [expanded, setExpanded] = useState<Set<number>>(() => new Set())
  const columns = actions ? 12 : 11
  // An imported line groups many rows under one price; its detail is those
  // rows summed by model, tag or uploader (the row description).
  const lineDetails = (line: StatementLine) => {
    const groups = new Map<
      string,
      { quantity: number; requests: number; amount: number }
    >()
    for (const row of statement.usage) {
      if (
        row.origin !== 'sync' ||
        row.date !== line.date ||
        row.usage_type !== line.usage_type ||
        row.unit !== line.unit ||
        row.settlement_status !== line.settlement_status ||
        row.unit_price !== line.unit_price ||
        row.fx_rate !== line.fx_rate ||
        (row.rate_mode ?? 'multiply') !== line.rate_mode
      ) {
        continue
      }
      const key = row.description || row.source_name
      const current = groups.get(key) ?? { quantity: 0, requests: 0, amount: 0 }
      current.quantity += Number(row.quantity)
      current.requests += row.request_count ?? 0
      current.amount += Number(row.amount)
      groups.set(key, current)
    }
    return [...groups.entries()].sort((a, b) => b[1].quantity - a[1].quantity)
  }
  const cards = [
    { label: 'Current balance', value: statement.balance },
    { label: 'Opening balance', value: statement.opening_balance },
    { label: 'Period settlement', value: statement.total_usage },
    { label: 'Period recharges', value: statement.total_recharges },
  ]
  return (
    <div className='space-y-5'>
      <div className='grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4'>
        {cards.map((card) => (
          <Card key={card.label} className='p-5'>
            <div className='mono-label mb-3'>{t(card.label)}</div>
            <div className='text-xl font-semibold tracking-tight break-all'>
              <Money value={card.value} currency={currency} />
            </div>
          </Card>
        ))}
      </div>
      <Card className='space-y-2 p-4'>
        <InitialBalanceSummary
          initial={statement.initial_balance}
          currency={currency}
          onEdit={props.onInitial}
        />
        {Number(statement.unpriced_usage) > 0 && (
          <p role='status' className='text-warning text-sm'>
            {t(
              'Unpriced usage does not reduce the prepaid balance. Set a price for each product line and day.'
            )}
          </p>
        )}
        <p className='text-muted-foreground text-xs'>
          {t(
            'Usage × unit price = original amount; original amount ÷ exchange rate = settlement amount.'
          )}
        </p>
      </Card>
      <DataTable
        headings={[
          'Date',
          'Product line',
          'Usage',
          'Unit price',
          'Original amount',
          'Exchange rate',
          'Discount',
          'Settlement amount',
          'Payments',
          'Balance',
          'Supplier claim',
          ...(actions ? ['Actions'] : []),
        ]}
        empty={!lines.length}
      >
        {lines.map((line, index) => {
          const usage = line.kind === 'usage'
          const unpriced = line.settlement_status === 'unpriced'
          // Balances are exact server decimals; only the claim gap is derived
          // here, at the supplier sheet's two-decimal precision.
          const gap = line.claim
            ? (-Number(line.change) - Number(line.claim.amount)).toFixed(2)
            : '0'
          const open = expanded.has(index)
          const expandable = usage && line.origin === 'sync'
          const details = open ? lineDetails(line) : []
          return (
            <Fragment
              key={`${line.date}-${line.kind}-${line.usage_type}-${index}`}
            >
              <tr>
                <td className='whitespace-nowrap'>{line.date}</td>
                <td>
                  <div className='flex items-center gap-1 font-medium'>
                    {expandable && (
                      <Button
                        size='sm'
                        variant='ghost'
                        className='size-6 p-0'
                        aria-expanded={open}
                        aria-label={t('Details')}
                        onClick={() =>
                          setExpanded((current) => {
                            const next = new Set(current)
                            if (next.has(index)) next.delete(index)
                            else next.add(index)
                            return next
                          })
                        }
                      >
                        {open ? (
                          <ChevronDown
                            className='size-3.5'
                            aria-hidden='true'
                          />
                        ) : (
                          <ChevronRight
                            className='size-3.5'
                            aria-hidden='true'
                          />
                        )}
                      </Button>
                    )}
                    {usage || line.kind === 'claim'
                      ? line.usage_type
                      : t(balanceKinds[line.kind] ?? line.kind)}
                    {line.direction === 'purchase' && (
                      <Badge tone='brand'>{t('Purchase')}</Badge>
                    )}
                  </div>
                  {(line.description || line.note) && (
                    <div className='text-muted-foreground mt-1 max-w-56 text-xs break-words'>
                      {line.description || line.note}
                    </div>
                  )}
                </td>
                <td className='whitespace-nowrap'>
                  {usage ? (
                    <Money
                      value={line.quantity}
                      currency={line.unit === 'cny_list' ? 'CNY' : 'USD'}
                    />
                  ) : (
                    '—'
                  )}
                </td>
                <td>
                  {usage && !unpriced ? (
                    <Money value={line.unit_price} currency={line.currency} />
                  ) : (
                    '—'
                  )}
                </td>
                <td>
                  {usage && !unpriced ? (
                    <Money
                      value={line.original_amount}
                      currency={line.currency}
                    />
                  ) : (
                    '—'
                  )}
                </td>
                <td>
                  {usage && !unpriced
                    ? line.rate_mode === 'divide'
                      ? `÷ ${line.fx_rate}`
                      : `× ${line.fx_rate}`
                    : '—'}
                </td>
                <td className='whitespace-nowrap'>
                  {usage && !unpriced ? (
                    <span title={t('Unit price ÷ exchange rate')}>
                      {(
                        ((Number(line.unit_price) *
                          Number(line.discount || 1)) /
                          (line.rate_mode === 'divide'
                            ? Number(line.fx_rate)
                            : 1 / Number(line.fx_rate))) *
                        100
                      ).toFixed(2)}
                      %
                    </span>
                  ) : (
                    '—'
                  )}
                </td>
                <td className='font-medium'>
                  {usage && unpriced && (
                    <Badge tone='warning'>{t('Unpriced')}</Badge>
                  )}
                  {usage && !unpriced && (
                    <Money
                      value={(-Number(line.change)).toFixed(6)}
                      currency={currency}
                    />
                  )}
                  {!usage && '—'}
                </td>
                <td>
                  {usage || line.kind === 'claim' ? (
                    '—'
                  ) : (
                    <Money value={line.change} currency={currency} />
                  )}
                </td>
                <td className='font-medium whitespace-nowrap'>
                  <Money value={line.balance} currency={currency} />
                </td>
                <td className='whitespace-nowrap'>
                  {line.claim ? (
                    <div className='space-y-1 text-xs'>
                      <div>
                        <Money value={line.claim.quantity} currency='USD' /> ·{' '}
                        <Money value={line.claim.amount} currency={currency} />
                      </div>
                      {Math.abs(Number(gap)) >= 0.01 ? (
                        <Badge tone='warning'>
                          {t('Difference')}{' '}
                          <Money value={gap} currency={currency} />
                        </Badge>
                      ) : (
                        <Badge tone='success'>{t('Matched')}</Badge>
                      )}
                    </div>
                  ) : (
                    '—'
                  )}
                </td>
                {actions && (
                  <td>
                    <div className='flex gap-2'>
                      {usage && line.origin === 'sync' && (
                        <Button size='sm' onClick={() => props.onPrice?.(line)}>
                          {t('Set price')}
                        </Button>
                      )}
                      {props.onClaim && (usage || line.kind === 'claim') && (
                        <Button
                          size='sm'
                          variant='outline'
                          onClick={() => props.onClaim?.(line)}
                        >
                          {t('Supplier claim')}
                        </Button>
                      )}
                    </div>
                  </td>
                )}
              </tr>
              {open && (
                <tr className='bg-muted/30'>
                  <td colSpan={columns}>
                    <table className='w-full text-xs'>
                      <thead className='text-muted-foreground'>
                        <tr>
                          <th className='py-1 text-left font-medium'>
                            {t('Item')}
                          </th>
                          <th className='py-1 text-right font-medium'>
                            {t('Usage')}
                          </th>
                          <th className='py-1 text-right font-medium'>
                            {t('Requests')}
                          </th>
                          <th className='py-1 text-right font-medium'>
                            {t('Settlement amount')}
                          </th>
                        </tr>
                      </thead>
                      <tbody>
                        {details.map(([name, value]) => (
                          <tr key={name}>
                            <td className='py-1'>{name}</td>
                            <td className='py-1 text-right'>
                              <Money
                                value={value.quantity.toFixed(6)}
                                currency={
                                  line.unit === 'cny_list' ? 'CNY' : 'USD'
                                }
                              />
                            </td>
                            <td className='py-1 text-right'>
                              <RequestCount value={String(value.requests)} />
                            </td>
                            <td className='py-1 text-right'>
                              {unpriced ? (
                                '—'
                              ) : (
                                <Money
                                  value={value.amount.toFixed(6)}
                                  currency={currency}
                                />
                              )}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </td>
                </tr>
              )}
            </Fragment>
          )
        })}
      </DataTable>
    </div>
  )
}
