import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge, Button, Card } from '../components/ui'
import {
  billingDate,
  discountPercent,
  type Recharge,
  type Statement,
} from './api'
import {
  DataTable,
  Money,
  RequestCount,
  Timestamp,
  UsageMetrics,
} from './components'

export default function StatementView(props: {
  statement: Statement
  onReverse?: (row: Recharge) => void
  showDailyTotals?: boolean
}) {
  const { t } = useTranslation()
  const [tab, setTab] = useState<'days' | 'usage' | 'recharges' | 'ledger'>(
    props.showDailyTotals === false ? 'usage' : 'days'
  )
  const [page, setPage] = useState(0)
  const statement = props.statement
  const currency = statement.supplier.currency
  const today = billingDate()
  const pendingSources = statement.source_status.some(
    (source) => source.sync_failed || source.missing_days > 0
  )
  const count = statement[tab].length
  const rowsPerPage = 50
  const currentPage = Math.min(
    page,
    Math.max(0, Math.ceil(count / rowsPerPage) - 1)
  )
  const usage = statement.usage.slice(
    currentPage * rowsPerPage,
    (currentPage + 1) * rowsPerPage
  )
  const recharges = statement.recharges.slice(
    currentPage * rowsPerPage,
    (currentPage + 1) * rowsPerPage
  )
  const days = statement.days.slice(
    currentPage * rowsPerPage,
    (currentPage + 1) * rowsPerPage
  )
  const ledger = statement.ledger.slice(
    currentPage * rowsPerPage,
    (currentPage + 1) * rowsPerPage
  )
  const cards = [
    { label: 'Current balance', value: statement.balance, currency },
    { label: 'Opening balance', value: statement.opening_balance, currency },
    { label: 'Period recharges', value: statement.total_recharges, currency },
    { label: 'Posted expenses', value: statement.total_usage, currency },
    {
      label: 'Gateway usage (USD)',
      value: statement.gateway_usage,
      currency: 'USD',
    },
    {
      label: 'Unpriced usage (USD)',
      value: statement.unpriced_usage,
      currency: 'USD',
    },
  ]
  return (
    <div className='space-y-5'>
      <div className='grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3'>
        {cards.map((card) => (
          <Card key={card.label} className='p-5'>
            <div className='mono-label mb-3'>{t(card.label)}</div>
            <div className='text-xl font-semibold tracking-tight break-all'>
              <Money value={card.value} currency={card.currency} />
            </div>
          </Card>
        ))}
      </div>
      <Card className='space-y-3 p-4'>
        <h2 className='text-sm font-semibold'>{t('Data freshness')}</h2>
        {Number(statement.unpriced_usage) > 0 && (
          <p role='status' className='text-warning text-sm'>
            {t(
              'Unpriced usage does not reduce the prepaid balance. Configure pricing and sync again to settle it.'
            )}
          </p>
        )}
        {pendingSources && (
          <p role='status' className='text-warning text-sm'>
            {t('Unsynced sources may add expenses to this statement.')}
          </p>
        )}
        {statement.days.some((day) => day.date === today) && (
          <p className='text-muted-foreground text-xs'>
            {t("Today's usage may still change.")}
          </p>
        )}
        {!statement.source_status.length && (
          <p className='text-muted-foreground text-xs'>
            {t('Manual entries only')}
          </p>
        )}
        {statement.source_status.map((source) => (
          <div
            key={source.id}
            className='flex flex-wrap items-center gap-2 text-xs'
          >
            <span className='font-medium'>{source.name}</span>
            {source.sync_failed && (
              <Badge tone='danger'>{t('Sync failed')}</Badge>
            )}
            {source.missing_days > 0 && (
              <Badge tone='warning'>
                {t('Pending days: {{count}}', { count: source.missing_days })}
              </Badge>
            )}
            {source.last_sync_at > 0 ? (
              <span className='text-muted-foreground'>
                <Timestamp value={source.last_sync_at} />
              </span>
            ) : (
              <span className='text-muted-foreground'>{t('Not synced')}</span>
            )}
          </div>
        ))}
      </Card>
      <div
        className='flex flex-wrap gap-2'
        role='group'
        aria-label={t('Statement view')}
      >
        {(['days', 'usage', 'recharges', 'ledger'] as const)
          .filter(
            (value) => value !== 'days' || props.showDailyTotals !== false
          )
          .map((value) => (
            <Button
              key={value}
              variant={tab === value ? 'secondary' : 'ghost'}
              aria-pressed={tab === value}
              onClick={() => {
                setTab(value)
                setPage(0)
              }}
            >
              {t(
                {
                  days: 'Daily totals',
                  usage: 'Usage details',
                  recharges: 'Recharge history',
                  ledger: 'Ledger',
                }[value]
              )}
            </Button>
          ))}
      </div>
      {tab === 'days' && (
        <DataTable
          headings={[
            'Date',
            'Gateway usage (USD)',
            'Unpriced usage (USD)',
            'Requests',
            'Posted expenses',
            'Recharges',
            'Closing balance',
            'Status',
          ]}
          empty={!count}
        >
          {days.map((day) => (
            <tr key={day.date}>
              <td>{day.date}</td>
              <td>
                <Money value={day.gateway_usage} currency='USD' />
              </td>
              <td>
                <Money value={day.unpriced_usage} currency='USD' />
              </td>
              <td>
                <RequestCount value={day.request_count} />
              </td>
              <td>
                <Money value={day.usage} currency={currency} />
              </td>
              <td>
                <Money value={day.recharges} currency={currency} />
              </td>
              <td className='font-medium'>
                <Money value={day.closing_balance} currency={currency} />
              </td>
              <td>
                {Number(day.unpriced_usage) > 0 && (
                  <Badge tone='warning'>{t('Unpriced')}</Badge>
                )}
                {!day.sources_synced && (
                  <Badge tone='warning'>{t('Awaiting sync')}</Badge>
                )}
                {day.sources_synced && day.date !== today && (
                  <Badge tone='neutral'>
                    {t(
                      statement.source_status.length
                        ? 'Sources synced'
                        : 'Manual entries only'
                    )}
                  </Badge>
                )}
                {day.date === today && (
                  <Badge tone='neutral'>{t('In progress')}</Badge>
                )}
              </td>
            </tr>
          ))}
        </DataTable>
      )}
      {tab === 'usage' && (
        <>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Quantity × unit price × discount × exchange rate = expense. Imported amounts use gateway billing units.'
            )}
          </p>
          <DataTable
            headings={[
              'Date',
              'Source',
              'Usage type',
              'Description',
              'Quantity',
              'Unit price',
              'Discount (%)',
              'Exchange rate',
              'Expense',
            ]}
            empty={!count}
          >
            {usage.map((row) => (
              <tr key={row.id}>
                <td className='whitespace-nowrap'>{row.date}</td>
                <td>
                  <div className='min-w-28'>
                    {row.origin === 'manual'
                      ? t('Manual entry')
                      : row.source_name}
                  </div>
                </td>
                <td>{row.usage_type}</td>
                <td className='max-w-72 break-words'>
                  {row.description}
                  {row.origin === 'sync' && <UsageMetrics values={row} />}
                </td>
                <td className='whitespace-nowrap'>
                  {row.quantity} {row.unit}
                </td>
                <td>
                  {row.settlement_status === 'unpriced' ? (
                    '—'
                  ) : (
                    <Money value={row.unit_price} currency={row.currency} />
                  )}
                </td>
                <td>
                  {row.settlement_status === 'unpriced'
                    ? '—'
                    : `${discountPercent(row.discount)}%`}
                </td>
                <td>
                  {row.settlement_status === 'unpriced' ? '—' : row.fx_rate}
                </td>
                <td className='font-medium'>
                  {row.settlement_status === 'unpriced' ? (
                    <Badge tone='warning'>{t('Unpriced')}</Badge>
                  ) : (
                    <Money value={row.amount} currency={currency} />
                  )}
                </td>
              </tr>
            ))}
          </DataTable>
        </>
      )}
      {tab === 'recharges' && (
        <DataTable
          headings={[
            'Date',
            'Amount',
            'Payment reference',
            'Note',
            'Status',
            ...(props.onReverse ? ['Actions'] : []),
          ]}
          empty={!count}
        >
          {recharges.map((row) => (
            <tr key={row.id}>
              <td className='whitespace-nowrap'>{row.date}</td>
              <td>
                <Money value={row.amount} currency={currency} />
              </td>
              <td>{row.reference}</td>
              <td className='max-w-96 break-words'>{row.note}</td>
              <td>
                <Badge tone={row.reversed ? 'neutral' : 'success'}>
                  {t(row.reversed ? 'Reversed' : 'Posted')}
                </Badge>
              </td>
              {props.onReverse && (
                <td>
                  <Button
                    variant='danger'
                    size='sm'
                    disabled={row.reversed}
                    onClick={() => props.onReverse?.(row)}
                  >
                    {t('Reverse recharge')}
                  </Button>
                </td>
              )}
            </tr>
          ))}
        </DataTable>
      )}
      {tab === 'ledger' && (
        <DataTable
          headings={['Date', 'Type', 'Balance change', 'Note']}
          empty={!count}
        >
          {ledger.map((row) => (
            <tr key={row.id}>
              <td>{row.date}</td>
              <td>
                {t(
                  (
                    {
                      usage: 'Expense',
                      recharge: 'Recharge',
                      recharge_reversal: 'Reversal',
                      usage_adjustment: 'Usage correction',
                      adjustment: 'Usage correction',
                    } as Record<string, string>
                  )[row.kind] ?? row.kind
                )}
              </td>
              <td>
                <Money value={row.amount} currency={currency} />
              </td>
              <td className='max-w-xl break-words'>
                {row.note === 'Source quantity correction'
                  ? t('Usage correction')
                  : row.note}
              </td>
            </tr>
          ))}
        </DataTable>
      )}
      {count > rowsPerPage && (
        <div className='flex items-center justify-end gap-3 text-sm'>
          <Button
            variant='outline'
            disabled={!currentPage}
            onClick={() => setPage(currentPage - 1)}
          >
            {t('Previous')}
          </Button>
          <span>
            {currentPage + 1} / {Math.ceil(count / rowsPerPage)}
          </span>
          <Button
            variant='outline'
            disabled={(currentPage + 1) * rowsPerPage >= count}
            onClick={() => setPage(currentPage + 1)}
          >
            {t('Next')}
          </Button>
        </div>
      )}
    </div>
  )
}
