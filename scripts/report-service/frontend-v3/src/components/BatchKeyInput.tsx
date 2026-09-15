import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, ListFilter } from 'lucide-react'
import { Button, Input, Textarea, Badge } from './ui'
import { parseKeyRows, serializeKeyRows, type KeyRow } from '../lib/batch-keys'
import { toast } from './feedback'

export function BatchKeyInput(props: { value: string; onChange: (value: string) => void; disabled?: boolean }) {
  const { t } = useTranslation()
  const id = useId()
  const [mode, setMode] = useState<'text' | 'table' | 'preview'>('text')
  const [rows, setRows] = useState<KeyRow[]>([])
  const parsed = parseKeyRows(props.value)
  const duplicateCount = parsed.length - new Set(parsed.map(row => row.key)).size

  const updateRows = (next: KeyRow[]) => {
    setRows(next)
    props.onChange(serializeKeyRows(next))
  }

  return (
    <section className="min-w-0 space-y-3 rounded-xl border border-border p-4" aria-label={t('API Keys')}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <label htmlFor={id} className="text-sm font-semibold">
          {t('API Keys')} <Badge>{parsed.length}</Badge>
        </label>
        <div className="flex rounded-lg bg-muted p-1" role="group" aria-label={t('Input mode')}>
          {(['text', 'table', 'preview'] as const).map(value => (
            <Button
              key={value}
              type="button"
              variant={mode === value ? 'outline' : 'ghost'}
              size="sm"
              aria-pressed={mode === value}
              disabled={props.disabled}
              onClick={() => {
                if (value === 'table') setRows(parsed.length ? parsed : [{ key: '', quota: '', note: '' }])
                setMode(value)
              }}
            >
              {value === 'text' && t('Text')}
              {value === 'table' && t('Table')}
              {value === 'preview' && t('Preview')}
            </Button>
          ))}
        </div>
      </div>
      <p id={`${id}-help`} className="text-xs leading-relaxed text-muted-foreground">
        {t('Enter one key per line for batch creation')} · {t('Quota')} (USD) / {t('Remark')} ({t('Optional')})
      </p>
      {mode === 'text' && (
        <Textarea
          id={id}
          aria-describedby={`${id}-help`}
          value={props.value}
          onChange={event => props.onChange(event.target.value)}
          disabled={props.disabled}
          rows={8}
          spellCheck={false}
          autoComplete="off"
          className="font-mono text-xs"
          placeholder={'sk-… 10\nsk-… 20 note'}
        />
      )}
      {mode === 'table' && (
        <div className="space-y-2">
          <div className="max-h-80 overflow-auto rounded-lg border border-border">
            <table className="w-full min-w-[420px] text-sm">
              <thead className="sticky top-0 bg-muted text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-2">Key</th>
                  <th className="w-24 p-2">USD</th>
                  <th className="p-2">{t('Remark')}</th>
                  <th className="w-10">
                    <span className="sr-only">{t('Actions')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row, index) => (
                  <tr key={index} className="border-t border-border">
                    {(['key', 'quota', 'note'] as const).map(field => (
                      <td key={field} className="p-1.5">
                        <Input
                          aria-label={`${field === 'key' ? 'Key' : t(field === 'quota' ? 'Quota' : 'Remark')} ${index + 1}`}
                          value={row[field]}
                          type={field === 'quota' ? 'number' : 'text'}
                          min={field === 'quota' ? 0 : undefined}
                          step={field === 'quota' ? 'any' : undefined}
                          autoComplete="off"
                          disabled={props.disabled}
                          onChange={event =>
                            updateRows(
                              rows.map((item, i) => (i === index ? { ...item, [field]: event.target.value } : item)),
                            )
                          }
                        />
                      </td>
                    ))}
                    <td>
                      <Button
                        variant="ghost"
                        className="size-8 p-0"
                        aria-label={`${t('Delete')} ${index + 1}`}
                        disabled={props.disabled}
                        onClick={() => updateRows(rows.filter((_, i) => i !== index))}
                      >
                        <Trash2 className="size-4" aria-hidden="true" />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Button
            variant="outline"
            size="sm"
            disabled={props.disabled}
            onClick={() => updateRows([...rows, { key: '', quota: '', note: '' }])}
          >
            <Plus className="size-4" aria-hidden="true" />
            {t('Add')}
          </Button>
        </div>
      )}
      {mode === 'preview' && (
        <div className="max-h-80 overflow-auto rounded-lg border border-border">
          <table className="w-full text-left text-xs">
            <thead className="sticky top-0 bg-muted">
              <tr>
                <th className="p-2">Key</th>
                <th className="p-2">USD</th>
                <th className="p-2">{t('Remark')}</th>
              </tr>
            </thead>
            <tbody>
              {parsed.slice(0, 100).map((row, index) => (
                <tr key={index} className="border-t border-border">
                  <td className="p-2 font-mono">••••{row.key.length > 8 ? row.key.slice(-4) : ''}</td>
                  <td className="p-2 tabular-nums">{row.quota || '—'}</td>
                  <td className="max-w-60 break-words p-2">{row.note || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {!parsed.length && <p className="p-6 text-center text-sm text-muted-foreground">{t('No data')}</p>}
          {parsed.length > 100 && (
            <p className="p-2 text-xs text-muted-foreground">{t('Showing first {{count}} items', { count: 100 })}</p>
          )}
        </div>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span role="status" className="text-xs text-muted-foreground">
          {t('Total')}: {parsed.length} · {t('Duplicate keys')}: {duplicateCount}
        </span>
        <Button
          variant="ghost"
          size="sm"
          disabled={props.disabled || duplicateCount === 0}
          onClick={() => {
            const seen = new Set<string>()
            const next = parsed.filter(row => {
              if (seen.has(row.key)) return false
              seen.add(row.key)
              return true
            })
            updateRows(next)
            toast.success(
              t('Removed {{removed}} duplicate key(s). Before: {{before}}, After: {{after}}', {
                removed: duplicateCount,
                before: parsed.length,
                after: next.length,
              }),
            )
          }}
        >
          <ListFilter className="size-4" aria-hidden="true" />
          {t('Remove Duplicates')}
        </Button>
      </div>
    </section>
  )
}
