import { ChevronRight } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SidePanel } from '../components/SidePanel'
import { Badge, Button, Card } from '../components/ui'
import { billingDate, type DailySummary } from './api'
import { DataTable, Money, RequestCount } from './components'

export default function DailyOverview(props: { summary: DailySummary }) {
  const { t } = useTranslation()
  const [selectedDate, setSelectedDate] = useState<string>()
  const [page, setPage] = useState(0)
  const summary = props.summary
  const orderedDays = [...summary.days].reverse()
  const currentPage = Math.min(
    page,
    Math.max(0, Math.ceil(orderedDays.length / 50) - 1)
  )
  const selectedDay = summary.days.find((day) => day.date === selectedDate)
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
          <tr key={day.date}>
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
                aria-expanded={selectedDate === day.date}
                onClick={() => setSelectedDate(day.date)}
              >
                {t('Supplier breakdown')}
                <ChevronRight className='size-3.5' aria-hidden='true' />
              </Button>
            </td>
          </tr>
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
      {selectedDay && (
        <SidePanel
          title={`${selectedDay.date} · ${t('Supplier breakdown')}`}
          onClose={() => setSelectedDate(undefined)}
        >
          <div className='space-y-6'>
            {selectedDay.heads.map((head) => (
              <section key={head.supplier_id} className='space-y-3'>
                <div className='flex flex-wrap items-center justify-between gap-2'>
                  <h3 className='text-sm font-semibold'>
                    {head.supplier_name}
                  </h3>
                  <Money value={head.gateway_usage} currency='USD' />
                </div>
                {!head.sources_synced && (
                  <Badge tone='warning'>{t('Awaiting sync')}</Badge>
                )}
                <DataTable
                  headings={['Supplier', 'Gateway usage (USD)', 'Requests']}
                  empty={!head.vendors.length}
                >
                  {head.vendors.map((vendor) => (
                    <tr key={vendor.vendor_id}>
                      <td>
                        {vendor.vendor_id === 0
                          ? t('Unassigned supplier')
                          : vendor.vendor_name}
                      </td>
                      <td>
                        <Money value={vendor.gateway_usage} currency='USD' />
                      </td>
                      <td>
                        <RequestCount value={vendor.request_count} />
                      </td>
                    </tr>
                  ))}
                </DataTable>
              </section>
            ))}
          </div>
        </SidePanel>
      )}
    </div>
  )
}
