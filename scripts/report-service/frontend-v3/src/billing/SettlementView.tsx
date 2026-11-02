import { useTranslation } from 'react-i18next'

import { Badge, Button, Card } from '../components/ui'
import {
  type InitialBalance,
  type Overview,
  type Statement,
  type StatementLine,
  type Supplier,
} from './api'
import { DataTable, Money } from './components'

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
          ...suppliers.flatMap((item) => [
            `${item.supplier.name} · ${t('Settlement amount')}`,
            `${item.supplier.name} · ${t('Payments')}`,
            `${item.supplier.name} · ${t('Closing balance')}`,
          ]),
        ]}
        empty={!dates.length}
      >
        {dates.map((date) => (
          <tr key={date}>
            <td className='whitespace-nowrap'>{date}</td>
            {suppliers.map((item) => {
              const day = item.days.find((value) => value.date === date)
              const currency = item.supplier.currency
              const payments = (
                Number(day?.recharges ?? 0) + Number(day?.initial ?? 0)
              ).toFixed(6)
              return [
                <td key={`${item.supplier.id}-usage`}>
                  <Money value={day?.usage ?? '0'} currency={currency} />
                  {Number(day?.unpriced_usage ?? 0) > 0 && (
                    <Badge tone='warning' className='ml-2'>
                      {t('Unpriced')}
                    </Badge>
                  )}
                </td>,
                <td key={`${item.supplier.id}-payments`}>
                  <Money value={payments} currency={currency} />
                </td>,
                <td key={`${item.supplier.id}-balance`} className='font-medium'>
                  <Money
                    value={day?.closing_balance ?? '0'}
                    currency={currency}
                  />
                </td>,
              ]
            })}
          </tr>
        ))}
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
  const manager = Boolean(props.onPrice)
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
          'Settlement amount',
          'Payments',
          'Balance',
          'Supplier claim',
          ...(manager ? ['Actions'] : []),
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
          return (
            <tr key={`${line.date}-${line.kind}-${line.usage_type}-${index}`}>
              <td className='whitespace-nowrap'>{line.date}</td>
              <td>
                <div className='font-medium'>
                  {usage || line.kind === 'claim'
                    ? line.usage_type
                    : t(balanceKinds[line.kind] ?? line.kind)}
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
              {manager && (
                <td>
                  <div className='flex gap-2'>
                    {usage && line.origin === 'sync' && (
                      <Button size='sm' onClick={() => props.onPrice?.(line)}>
                        {t('Set price')}
                      </Button>
                    )}
                    {(usage || line.kind === 'claim') && (
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
          )
        })}
      </DataTable>
    </div>
  )
}
