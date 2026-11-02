import { ChevronDown, ChevronRight } from 'lucide-react'
import { Fragment, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge, Button, Card } from '../components/ui'
import { billingDate, type DailySummary } from './api'
import { DataTable, Money, RequestCount } from './components'

export default function DailyOverview(props: { summary: DailySummary }) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  const [page, setPage] = useState(0)
  const summary = props.summary
  const orderedDays = [...summary.days].reverse()
  const currentPage = Math.min(
    page,
    Math.max(0, Math.ceil(orderedDays.length / 50) - 1)
  )
  const days = orderedDays.slice(currentPage * 50, (currentPage + 1) * 50)
  return (
    <div className='space-y-5'>
      <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
        {summary.totals.map((head) => (
          <Card key={head.supplier_id} className='p-5'>
            <div className='mb-3 text-sm font-medium'>{head.supplier_name}</div>
            <div className='text-xl font-semibold'>
              <Money value={head.gateway_usage} currency='USD' />
            </div>
            <div className='text-muted-foreground mt-2 flex gap-2 text-xs'>
              <span>{t('Requests')}</span>
              <RequestCount value={head.request_count} />
            </div>
          </Card>
        ))}
      </div>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Daily gateway usage in USD. Open a day to see each supplier head and its suppliers.'
        )}
      </p>
      <DataTable
        headings={[
          'Date',
          ...summary.heads.map((head) => `${head.name} (USD)`),
          'Total usage (USD)',
          'Requests',
          'Supplier breakdown',
        ]}
        empty={!days.length}
      >
        {days.map((day) => (
          <Fragment key={day.date}>
            <tr>
              <td className='whitespace-nowrap'>
                {day.date}
                {day.date === billingDate() && (
                  <div className='mt-1'>
                    <Badge tone='warning'>{t('In progress')}</Badge>
                  </div>
                )}
              </td>
              {summary.heads.map((head) => {
                const usage = day.heads.find(
                  (item) => item.supplier_id === head.id
                )
                return (
                  <td key={head.id}>
                    <Money value={usage?.gateway_usage ?? '0'} currency='USD' />
                    {usage && !usage.sources_synced && (
                      <div className='mt-1'>
                        <Badge tone='warning'>{t('Awaiting sync')}</Badge>
                      </div>
                    )}
                  </td>
                )
              })}
              <td className='font-medium'>
                <Money value={day.gateway_usage} currency='USD' />
              </td>
              <td>
                <RequestCount value={day.request_count} />
              </td>
              <td>
                <Button
                  variant='ghost'
                  size='sm'
                  aria-label={t('View supplier breakdown for {{date}}', {
                    date: day.date,
                  })}
                  aria-expanded={expanded.has(day.date)}
                  onClick={() =>
                    setExpanded((current) => {
                      const next = new Set(current)
                      if (next.has(day.date)) next.delete(day.date)
                      else next.add(day.date)
                      return next
                    })
                  }
                >
                  {t('Supplier breakdown')}
                  {expanded.has(day.date) ? (
                    <ChevronDown className='size-3.5' aria-hidden='true' />
                  ) : (
                    <ChevronRight className='size-3.5' aria-hidden='true' />
                  )}
                </Button>
              </td>
            </tr>
            {expanded.has(day.date) && (
              <tr className='bg-muted/30'>
                <td colSpan={summary.heads.length + 4}>
                  <div className='grid gap-4 lg:grid-cols-2'>
                    {day.heads.map((head) => (
                      <section key={head.supplier_id} className='space-y-2'>
                        <div className='flex flex-wrap items-center gap-2'>
                          <h3 className='text-sm font-semibold'>
                            {head.supplier_name}
                          </h3>
                          <Money value={head.gateway_usage} currency='USD' />
                          {!head.sources_synced && (
                            <Badge tone='warning'>{t('Awaiting sync')}</Badge>
                          )}
                        </div>
                        <table className='w-full text-xs'>
                          <thead className='text-muted-foreground'>
                            <tr>
                              <th className='py-1 text-left font-medium'>
                                {t('Supplier')}
                              </th>
                              <th className='py-1 text-right font-medium'>
                                {t('Gateway usage (USD)')}
                              </th>
                              <th className='py-1 text-right font-medium'>
                                {t('Requests')}
                              </th>
                            </tr>
                          </thead>
                          <tbody>
                            {head.vendors.map((vendor) => (
                              <tr key={vendor.vendor_id}>
                                <td className='py-1'>
                                  {vendor.vendor_id === 0
                                    ? t('Unassigned supplier')
                                    : vendor.vendor_name}
                                </td>
                                <td className='py-1 text-right'>
                                  <Money
                                    value={vendor.gateway_usage}
                                    currency='USD'
                                  />
                                </td>
                                <td className='py-1 text-right'>
                                  <RequestCount value={vendor.request_count} />
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                        {!head.vendors.length && (
                          <p className='text-muted-foreground text-xs'>
                            {t('No records in this period')}
                          </p>
                        )}
                      </section>
                    ))}
                  </div>
                </td>
              </tr>
            )}
          </Fragment>
        ))}
      </DataTable>
      {orderedDays.length > 50 && (
        <div className='flex items-center justify-end gap-3 text-sm'>
          <Button
            variant='outline'
            disabled={currentPage === 0}
            onClick={() => setPage(currentPage - 1)}
          >
            {t('Previous')}
          </Button>
          <span>
            {currentPage + 1} / {Math.ceil(orderedDays.length / 50)}
          </span>
          <Button
            variant='outline'
            disabled={(currentPage + 1) * 50 >= orderedDays.length}
            onClick={() => setPage(currentPage + 1)}
          >
            {t('Next')}
          </Button>
        </div>
      )}
    </div>
  )
}
