import { useTranslation } from 'react-i18next'

import { Badge, Button } from '../components/ui'
import { discountPercent, type Source } from './api'
import { DataTable, Timestamp } from './components'
import type { FormTarget } from './EntryForm'

export default function SourceConnections(props: {
  sources: Source[]
  onTarget: (target: FormTarget) => void
}) {
  const { t } = useTranslation()
  return (
    <DataTable
      headings={[
        'Source',
        'Usage type',
        'Mapping',
        'Gateway user ID',
        'Settlement mode',
        'Discount (%)',
        'Exchange rate',
        'Sync status',
        'Actions',
      ]}
      empty={!props.sources.length}
    >
      {props.sources.map((source) => (
        <tr key={source.id}>
          <td>
            <div className='font-medium'>{source.name}</div>
            <div className='text-muted-foreground mt-1 max-w-64 truncate text-xs'>
              {source.base_url}
            </div>
          </td>
          <td>{source.usage_type}</td>
          <td className='max-w-56 break-words'>
            {source.kind === 'report' ? (
              source.channel_ids
            ) : (
              <>
                <Badge tone='neutral'>
                  {t(
                    {
                      report_group: 'Gateway user and groups',
                      remote_tag: 'Remote channel tags',
                      ccmax: 'CC Max users',
                    }[source.kind]
                  )}
                </Badge>
                <div className='mt-1 text-xs'>
                  {source.exclude_selector
                    ? t('All except {{values}}', {
                        values: source.selector || '—',
                      })
                    : source.selector}
                </div>
              </>
            )}
          </td>
          <td>
            {source.gateway_user_id > 0
              ? source.gateway_user_id
              : t('All gateway users')}
          </td>
          <td>
            <Badge
              tone={
                source.settlement_mode === 'usage_only' ? 'warning' : 'neutral'
              }
            >
              {t(
                source.settlement_mode === 'usage_only'
                  ? 'Usage only'
                  : 'Settle expenses'
              )}
            </Badge>
          </td>
          <td>
            {source.settlement_mode === 'usage_only'
              ? '—'
              : `${discountPercent(source.discount)}%`}
          </td>
          <td>
            {source.settlement_mode === 'usage_only' ? '—' : source.fx_rate}
          </td>
          <td>
            {source.last_error ? (
              <span className='text-destructive'>{t(source.last_error)}</span>
            ) : (
              <Badge tone={source.last_sync_at ? 'success' : 'neutral'}>
                {t(source.last_sync_at ? 'Synced' : 'Not synced')}
              </Badge>
            )}
            {source.last_sync_at > 0 && (
              <div className='text-muted-foreground mt-1 text-xs'>
                <Timestamp value={source.last_sync_at} />
              </div>
            )}
          </td>
          <td>
            <div className='flex gap-2'>
              <Button
                size='sm'
                variant='outline'
                onClick={() => props.onTarget({ kind: 'source', source })}
              >
                {t('Edit')}
              </Button>
              <Button
                size='sm'
                onClick={() => props.onTarget({ kind: 'sync', source })}
              >
                {t('Sync usage')}
              </Button>
            </div>
          </td>
        </tr>
      ))}
    </DataTable>
  )
}
