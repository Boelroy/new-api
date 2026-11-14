import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge, Button, Input } from '../components/ui'
import { errorMessage, request, type MappingOption, type Supplier } from './api'
import { ErrorNotice, Money } from './components'

// Edits which upstream groups, tags or uploaders feed one supplier product
// line. Recent values come from the source itself, with their current owner,
// so a value already mapped elsewhere is visible before saving.
export default function MappingPicker(props: {
  kind: 'report_group' | 'report_studio' | 'remote_tag' | 'ccmax'
  sourceID?: number
  suppliers: Supplier[]
  selected: string[]
  exclude: boolean
  onSelected: (values: string[]) => void
  onExclude: (exclude: boolean) => void
  connection: () => Record<string, unknown>
}) {
  const { t } = useTranslation()
  const [options, setOptions] = useState<MappingOption[]>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [manual, setManual] = useState('')
  const label = {
    report_group: 'Gateway groups',
    report_studio: 'Studios',
    remote_tag: 'Channel tags',
    ccmax: 'CC Max users',
  }[props.kind]
  async function load() {
    setBusy(true)
    setError('')
    try {
      setOptions(
        await request<MappingOption[]>('/sources/discover', {
          method: 'POST',
          body: { ...props.connection(), kind: props.kind },
        })
      )
    } catch (error) {
      setError(errorMessage(error))
    } finally {
      setBusy(false)
    }
  }
  function toggle(value: string) {
    props.onSelected(
      props.selected.includes(value)
        ? props.selected.filter((item) => item !== value)
        : [...props.selected, value]
    )
  }
  const unseen = props.selected.filter(
    (value) => !options?.some((option) => option.value === value)
  )
  return (
    <fieldset className='space-y-3'>
      <legend className='mb-1.5'>{t(label)}</legend>
      {(props.kind === 'remote_tag' || props.kind === 'report_studio') && (
        <label className='flex items-center gap-2 text-sm'>
          <input
            type='checkbox'
            checked={props.exclude}
            onChange={(event) => props.onExclude(event.target.checked)}
          />
          {t('Map every tag except the selected ones')}
        </label>
      )}
      <p className='text-muted-foreground text-xs'>
        {t(
          'Mapping changes apply to later syncs. Usage already imported stays with this source.'
        )}
      </p>
      <Button
        variant='outline'
        size='sm'
        disabled={busy}
        onClick={() => void load()}
      >
        {busy ? t('Loading…') : t('Load recent values')}
      </Button>
      {error && <ErrorNotice message={error} />}
      {options && !options.length && (
        <p className='text-muted-foreground text-xs'>
          {t('No usage in the last 14 days')}
        </p>
      )}
      <div className='max-h-72 space-y-1 overflow-y-auto'>
        {options?.map((option) => {
          const elsewhere =
            option.source_id > 0 && option.source_id !== props.sourceID
          const owner = props.suppliers.find(
            (item) => item.id === option.supplier_id
          )
          return (
            <label
              key={option.value}
              className='flex items-center gap-2 rounded-md px-1 py-1 text-sm'
            >
              <input
                type='checkbox'
                checked={props.selected.includes(option.value)}
                onChange={() => toggle(option.value)}
              />
              <span className='min-w-0 flex-1 truncate'>{option.value}</span>
              <span className='text-muted-foreground text-xs'>
                <Money
                  value={option.quantity}
                  currency={option.unit === 'cny_list' ? 'CNY' : 'USD'}
                />
              </span>
              {elsewhere && (
                <Badge tone='warning'>
                  {owner?.name ?? ''} · {option.source_name}
                </Badge>
              )}
              {!elsewhere && option.source_id === 0 && (
                <Badge tone='neutral'>{t('Unmapped')}</Badge>
              )}
            </label>
          )
        })}
        {unseen.map((value) => (
          <label
            key={value}
            className='flex items-center gap-2 px-1 py-1 text-sm'
          >
            <input type='checkbox' checked onChange={() => toggle(value)} />
            <span className='flex-1'>{value}</span>
          </label>
        ))}
      </div>
      <div className='flex gap-2'>
        <Input
          aria-label={t('Add a value')}
          placeholder={t('Add a value')}
          value={manual}
          maxLength={120}
          onChange={(event) => setManual(event.target.value)}
        />
        <Button
          variant='outline'
          size='sm'
          disabled={!manual.trim() || props.selected.includes(manual.trim())}
          onClick={() => {
            props.onSelected([...props.selected, manual.trim()])
            setManual('')
          }}
        >
          {t('Add')}
        </Button>
      </div>
    </fieldset>
  )
}
