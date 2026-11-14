import { useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { FormSection } from '../components/FormSection'
import { SidePanel } from '../components/SidePanel'
import { Button, Input, Select, Textarea } from '../components/ui'
import {
  billingDate,
  discountPercent,
  errorMessage,
  request,
  type InitialBalance,
  type Recharge,
  type Session,
  type Source,
  type SourceKind,
  type StatementLine,
  type Supplier,
  type User,
} from './api'
import { ErrorNotice } from './components'
import MappingPicker from './MappingPicker'

export type FormKind =
  | 'usage'
  | 'recharge'
  | 'supplier'
  | 'source'
  | 'user'
  | 'password'
  | 'disable'
  | 'sync'
  | 'reverse'
  | 'initial'
  | 'claim'
export type FormTarget = {
  kind: FormKind
  source?: Source
  user?: User
  recharge?: Recharge
  // Overview actions name their own supplier instead of the selected one.
  supplier?: Supplier
  initial?: InitialBalance | null
  line?: StatementLine
}
const titles: Record<FormKind, string> = {
  usage: 'Add usage',
  recharge: 'Record recharge',
  supplier: 'Add supplier head',
  source: 'Data source',
  user: 'Add account',
  password: 'Change password',
  disable: 'Disable account',
  sync: 'Sync usage',
  reverse: 'Reverse recharge',
  initial: 'Set initial balance',
  claim: 'Supplier claim',
}

type Field = {
  name: string
  label: string
  value?: string
  type?: string
  required?: boolean
  min?: string
  max?: string
  step?: string
  hint?: string
  autoComplete?: string
}

export default function EntryForm(props: {
  target: FormTarget
  supplier?: Supplier
  suppliers: Supplier[]
  start: string
  end: string
  onClose: () => void
  onSaved: (session?: Session) => void
}) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [error, setError] = useState('')
  const [role, setRole] = useState('supplier')
  const [currency, setCurrency] = useState(props.supplier?.currency ?? 'CNY')
  const [settlementMode, setSettlementMode] = useState<
    'settlement' | 'usage_only'
  >(props.target.source?.settlement_mode ?? 'settlement')
  const [idempotencyKey] = useState(() => crypto.randomUUID())
  const [sourceKind, setSourceKind] = useState<SourceKind>(
    props.target.source?.kind ?? 'report'
  )
  const [mapped, setMapped] = useState<string[]>(() =>
    (props.target.source?.selector ?? '').split(',').filter(Boolean)
  )
  const [direction, setDirection] = useState<'expense' | 'purchase'>(
    props.target.source?.direction ?? 'expense'
  )
  const [excludeMapped, setExcludeMapped] = useState(
    props.target.source?.exclude_selector ?? false
  )
  const formRef = useRef<HTMLFormElement>(null)
  const kind = props.target.kind
  const source = props.target.source
  let sourceDiscount = '100'
  if (source) {
    sourceDiscount = source.discount ? discountPercent(source.discount) : ''
  }
  let fields: Field[] = []
  if (kind === 'supplier') {
    fields = [
      { name: 'name', label: 'Supplier head name' },
      {
        name: 'currency',
        label: 'Settlement currency',
        value: 'CNY',
        hint: 'ISO currency code, for example CNY or USD',
      },
    ]
  }
  if (kind === 'recharge') {
    fields = [
      { name: 'date', label: 'Date', type: 'date', value: billingDate() },
      {
        name: 'amount',
        label: 'Recharge amount',
        type: 'number',
        min: '0.000001',
        max: '1000000000000',
        step: '0.000001',
      },
      { name: 'reference', label: 'Payment reference' },
      { name: 'note', label: 'Note', required: false },
    ]
  }
  if (kind === 'usage') {
    fields = [
      { name: 'date', label: 'Date', type: 'date', value: billingDate() },
      { name: 'usage_type', label: 'Usage type' },
      { name: 'description', label: 'Description' },
      {
        name: 'quantity',
        label: 'Quantity',
        type: 'number',
        min: '0.000001',
        max: '1000000000000',
        step: '0.000001',
      },
      { name: 'unit', label: 'Unit' },
      {
        name: 'unit_price',
        label: 'Unit price',
        type: 'number',
        min: '0',
        max: '1000000000000',
        step: '0.000001',
      },
      {
        name: 'discount_percent',
        label: 'Discount (%)',
        type: 'number',
        value: '100',
        min: '0',
        max: '100',
        step: '0.0001',
        hint: '88 means paying 88% of the original amount',
      },
    ]
  }
  if (kind === 'source') {
    fields = [
      { name: 'name', label: 'Source name', value: source?.name },
      {
        name: 'base_url',
        label: 'Report Service URL',
        value: source?.base_url,
        type: 'url',
      },
      {
        name: 'credential_env',
        label: 'Credential environment variable',
        value: source?.credential_env,
        hint: 'Use a SUPPLIER_BILLING_SOURCE_ variable name, never the credential itself',
      },
      {
        name: 'channel_ids',
        label: 'Channel IDs',
        value: source?.channel_ids,
        hint: 'Comma-separated channel IDs belonging to this supplier',
      },
      {
        name: 'gateway_user_id',
        label: 'Gateway user ID',
        value: String(source?.gateway_user_id ?? 0),
        type: 'number',
        required: false,
        min: '0',
        max: '9007199254740991',
        step: '1',
        hint: 'Source gateway account ID, separate from billing accounts. 0 includes all users.',
      },
      { name: 'usage_type', label: 'Usage type', value: source?.usage_type },
      {
        name: 'discount_percent',
        label: 'Discount (%)',
        value: sourceDiscount,
        type: 'number',
        min: '0',
        max: '100',
        step: '0.0001',
        hint: 'Applied to gateway billing amounts, not official list prices',
      },
      {
        name: 'fx_rate',
        label: 'Exchange rate',
        value:
          source?.fx_rate ??
          (props.supplier?.currency === 'USD' ? '1' : undefined),
        type: 'number',
        min: '0.000001',
        max: '100000',
        step: '0.000001',
        hint: 'Settlement currency per 1 USD',
      },
    ]
  }
  if (kind === 'user') {
    fields = [
      { name: 'username', label: 'Username' },
      {
        name: 'password',
        label: 'New password',
        type: 'password',
        hint: 'Use 15–128 characters',
        autoComplete: 'new-password',
      },
      {
        name: 'current_password',
        label: 'Your current password',
        type: 'password',
      },
    ]
  }
  if (kind === 'source') {
    fields.splice(3, 0, {
      name: 'profile_id',
      label: 'Remote profile ID',
      value: String(source?.profile_id || ''),
      type: 'number',
      min: '1',
      step: '1',
      hint: 'Report Service remote supplier profile, for example 1 for aaaasl',
    })
    const hidden: Record<SourceKind, string[]> = {
      report: ['profile_id'],
      report_group: ['profile_id', 'channel_ids'],
      report_studio: ['profile_id', 'channel_ids'],
      remote_tag: ['channel_ids', 'gateway_user_id'],
      ccmax: ['profile_id', 'channel_ids', 'gateway_user_id', 'fx_rate'],
    }
    fields = fields.filter((field) => !hidden[sourceKind].includes(field.name))
  }
  if (kind === 'source' && settlementMode === 'usage_only') {
    fields = fields.filter(
      (field) => !['discount_percent', 'fx_rate'].includes(field.name)
    )
  }
  if (kind === 'password') {
    fields = [
      {
        name: 'current_password',
        label: 'Your current password',
        type: 'password',
      },
      {
        name: 'new_password',
        label: 'New password',
        type: 'password',
        hint: 'Use 15–128 characters',
        autoComplete: 'new-password',
      },
    ]
  }
  if (kind === 'disable') {
    fields = [
      {
        name: 'current_password',
        label: 'Your current password',
        type: 'password',
      },
    ]
  }
  if (kind === 'reverse') fields = [{ name: 'note', label: 'Reversal reason' }]
  const line = props.target.line
  if (kind === 'initial') {
    fields = [
      {
        name: 'date',
        label: 'Date',
        type: 'date',
        value: props.target.initial?.date ?? billingDate(),
      },
      {
        name: 'amount',
        label: 'Initial balance',
        type: 'number',
        value: props.target.initial?.amount,
        min: '-1000000000000',
        max: '1000000000000',
        step: '0.000001',
        hint: 'Prepaid amount left with the supplier before this ledger. Use a negative amount for an unpaid balance.',
      },
      {
        name: 'note',
        label: 'Note',
        value: props.target.initial?.note,
        required: false,
      },
    ]
  }
  if (kind === 'claim') {
    fields = [
      {
        name: 'quantity',
        label: 'Usage (USD)',
        type: 'number',
        value: line?.claim?.quantity,
        min: '0',
        max: '1000000000000',
        step: '0.000001',
      },
      {
        name: 'amount',
        label: 'Settlement amount',
        type: 'number',
        value: line?.claim?.amount,
        min: '0',
        max: '1000000000000',
        step: '0.000001',
      },
      {
        name: 'note',
        label: 'Note',
        value: line?.claim?.note,
        required: false,
      },
    ]
  }
  if (kind === 'sync') {
    fields = [
      { name: 'start', label: 'Start date', type: 'date', value: props.start },
      { name: 'end', label: 'End date', type: 'date', value: props.end },
    ]
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busyRef.current) return
    busyRef.current = true
    setBusy(true)
    setError('')
    const data = Object.fromEntries(
      new FormData(event.currentTarget)
    ) as Record<string, string | number | boolean>
    let path = ''
    let method = 'POST'
    if (kind === 'usage' || kind === 'recharge') {
      data.supplier_id = props.supplier?.id ?? 0
      data.idempotency_key = idempotencyKey
      path = kind === 'usage' ? '/usage' : '/recharges'
    }
    if (
      kind === 'usage' ||
      (kind === 'source' && settlementMode === 'settlement')
    ) {
      data.discount = (Number(data.discount_percent) / 100).toFixed(6)
      delete data.discount_percent
    }
    // Manual lines follow the settlement sheet: 6.7 means 6.7 original
    // currency units per settlement unit, and the expense divides by it.
    if (kind === 'usage') data.rate_mode = 'divide'
    if (kind === 'usage' && currency === props.supplier?.currency) {
      data.fx_rate = '1'
    }
    if (kind === 'supplier') {
      path = '/suppliers'
      data.currency = String(data.currency).toUpperCase()
    }
    if (kind === 'source') {
      data.supplier_id = source?.supplier_id ?? props.supplier?.id ?? 0
      data.gateway_user_id = Number(data.gateway_user_id || 0)
      data.kind = sourceKind
      data.direction = direction
      data.currency = sourceKind === 'ccmax' ? 'CNY' : 'USD'
      data.profile_id = Number(data.profile_id || 0)
      data.selector = mapped.join(',')
      data.exclude_selector =
        (sourceKind === 'remote_tag' || sourceKind === 'report_studio') &&
        excludeMapped
      if (sourceKind === 'ccmax' && settlementMode === 'settlement') {
        data.fx_rate = '1'
      }
      data.settlement_mode = settlementMode
      if (settlementMode === 'usage_only') {
        data.discount = ''
        data.fx_rate = ''
      }
      path = source ? `/sources/${source.id}` : '/sources'
      if (source) method = 'PUT'
    }
    if (kind === 'user') {
      path = '/users'
      data.role = role
      data.supplier_id = role === 'supplier' ? Number(data.supplier_id) : 0
    }
    if (kind === 'password') path = '/password'
    if (kind === 'disable') path = `/users/${props.target.user?.id}/disable`
    if (kind === 'sync') path = `/sources/${source?.id}/sync`
    if (kind === 'reverse') {
      path = `/recharges/${props.target.recharge?.id}/reverse`
    }
    if (kind === 'initial') {
      path = `/suppliers/${props.supplier?.id}/initial-balance`
      method = 'PUT'
      data.revision = props.target.initial?.revision ?? 0
    }
    if (kind === 'claim') {
      data.supplier_id = props.supplier?.id ?? 0
      data.date = line?.date ?? ''
      data.usage_type = line?.usage_type ?? ''
    }
    if (kind === 'claim') {
      path = '/claims'
      method = 'PUT'
    }
    try {
      const result = await request<Session | { ok: boolean }>(path, {
        method,
        body: data,
      })
      props.onSaved(kind === 'password' ? (result as Session) : undefined)
    } catch (error) {
      setError(errorMessage(error))
    } finally {
      busyRef.current = false
      setBusy(false)
    }
  }

  return (
    <SidePanel
      title={t(titles[kind])}
      onClose={props.onClose}
      busy={busy}
      footer={
        <div className='flex justify-end gap-2'>
          <Button variant='outline' disabled={busy} onClick={props.onClose}>
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form='billing-entry'
            disabled={busy}
            variant={
              kind === 'disable' || kind === 'reverse' ? 'danger' : 'primary'
            }
          >
            {busy ? t('Saving…') : t('Submit')}
          </Button>
        </div>
      }
    >
      <form
        id='billing-entry'
        ref={formRef}
        onSubmit={submit}
        className='space-y-5'
      >
        {props.supplier &&
          !['password', 'user', 'disable', 'supplier'].includes(kind) && (
            <p className='text-muted-foreground text-sm'>
              {props.supplier.name} · {props.supplier.currency}
            </p>
          )}
        {kind === 'usage' && (
          <p className='bg-primary/5 text-primary rounded-lg p-3 text-sm'>
            {t(
              'Submitted usage is charged immediately to the prepaid balance.'
            )}
          </p>
        )}
        {kind === 'disable' && (
          <p className='text-sm'>
            {t('Disable this account and revoke all its sessions?')}{' '}
            <strong>{props.target.user?.username}</strong>
          </p>
        )}
        {kind === 'reverse' && (
          <p className='text-sm'>
            {t(
              'This removes the recharge from the balance and records a reversal dated today.'
            )}{' '}
            {props.target.recharge?.reference}
          </p>
        )}
        {line && kind === 'claim' && (
          <p className='text-sm'>
            {line.date} · <strong>{line.usage_type}</strong>
          </p>
        )}
        {kind === 'initial' && props.target.initial && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'Saving replaces the current initial balance. The ledger keeps the previous amount and posts a correction on its date.'
            )}
          </p>
        )}
        {kind === 'claim' && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'Supplier claims are for reconciliation only and do not change the balance.'
            )}
          </p>
        )}
        {source && kind === 'source' && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'Settled entries retain their pricing. Unpriced entries can be priced on the next sync.'
            )}
          </p>
        )}
        {error && <ErrorNotice message={error} />}
        <FormSection>
          {kind === 'source' && (
            <label className='block space-y-1.5'>
              <span>{t('Mapping type')}</span>
              <Select
                className='w-full'
                value={sourceKind}
                disabled={Boolean(source)}
                onChange={(event) => {
                  setSourceKind(event.target.value as SourceKind)
                  setMapped([])
                  setExcludeMapped(false)
                }}
              >
                <option value='report'>{t('Report channels')}</option>
                <option value='report_group'>
                  {t('Gateway user and groups')}
                </option>
                <option value='report_studio'>{t('Report studios')}</option>
                <option value='remote_tag'>{t('Remote channel tags')}</option>
                <option value='ccmax'>{t('CC Max users')}</option>
              </Select>
            </label>
          )}
          {kind === 'source' && (
            <label className='block space-y-1.5'>
              <span>{t('Direction')}</span>
              <Select
                className='w-full'
                value={direction}
                disabled={Boolean(source)}
                onChange={(event) =>
                  setDirection(event.target.value as 'expense' | 'purchase')
                }
              >
                <option value='expense'>{t('Supplier sells to us')}</option>
                <option value='purchase'>{t('Supplier buys from us')}</option>
              </Select>
              {direction === 'purchase' && (
                <span className='text-muted-foreground block text-xs'>
                  {t(
                    'Purchased usage credits the supplier balance. Use a gateway user ID to separate it from the channel owner.'
                  )}
                </span>
              )}
            </label>
          )}
          {kind === 'source' && (
            <label className='block space-y-1.5'>
              <span>{t('Settlement mode')}</span>
              <Select
                className='w-full'
                value={settlementMode}
                onChange={(event) =>
                  setSettlementMode(
                    event.target.value as 'settlement' | 'usage_only'
                  )
                }
              >
                <option value='settlement'>{t('Settle expenses')}</option>
                <option value='usage_only'>{t('Usage only')}</option>
              </Select>
              {settlementMode === 'usage_only' && (
                <span className='text-muted-foreground block text-xs'>
                  {t(
                    'Usage only imports usage without deducting the prepaid balance.'
                  )}
                </span>
              )}
            </label>
          )}
          {fields.map((field) => (
            <label key={field.name} className='block space-y-1.5'>
              <span>{t(field.label)}</span>
              {field.name === 'note' || field.name === 'description' ? (
                <Textarea
                  name={field.name}
                  defaultValue={field.value}
                  required={field.required !== false}
                  maxLength={1000}
                />
              ) : (
                <Input
                  name={field.name}
                  defaultValue={field.value}
                  required={field.required !== false}
                  type={field.type ?? 'text'}
                  min={field.min}
                  max={
                    field.max ??
                    (field.type === 'date' ? billingDate() : undefined)
                  }
                  step={field.step}
                  readOnly={Boolean(
                    source &&
                    kind === 'source' &&
                    [
                      'base_url',
                      'channel_ids',
                      'gateway_user_id',
                      'profile_id',
                    ].includes(field.name)
                  )}
                  minLength={
                    field.name === 'password' || field.name === 'new_password'
                      ? 15
                      : undefined
                  }
                  maxLength={field.type === 'password' ? 128 : 500}
                  autoComplete={
                    field.name === 'current_password'
                      ? 'current-password'
                      : field.autoComplete
                  }
                />
              )}
              {field.hint && (
                <span className='text-muted-foreground block text-xs'>
                  {t(field.hint)}
                </span>
              )}
            </label>
          ))}
          {kind === 'source' && sourceKind !== 'report' && (
            <MappingPicker
              kind={sourceKind}
              sourceID={source?.id}
              suppliers={props.suppliers}
              selected={mapped}
              exclude={excludeMapped}
              onSelected={setMapped}
              onExclude={setExcludeMapped}
              connection={() => {
                const values = formRef.current
                  ? Object.fromEntries(new FormData(formRef.current))
                  : {}
                return {
                  base_url: values.base_url ?? '',
                  credential_env: values.credential_env ?? '',
                  profile_id: Number(values.profile_id || 0),
                  gateway_user_id: Number(values.gateway_user_id || 0),
                }
              }}
            />
          )}
          {kind === 'usage' && (
            <>
              <label className='block space-y-1.5'>
                <span>{t('Original currency')}</span>
                <Select
                  name='currency'
                  className='w-full'
                  value={currency}
                  onChange={(event) => setCurrency(event.target.value)}
                >
                  <option value={props.supplier?.currency ?? 'CNY'}>
                    {props.supplier?.currency ?? 'CNY'}
                  </option>
                  {props.supplier?.currency !== 'USD' && (
                    <option value='USD'>USD</option>
                  )}
                  {props.supplier?.currency !== 'CNY' && (
                    <option value='CNY'>CNY</option>
                  )}
                </Select>
              </label>
              <label className='block space-y-1.5'>
                <span>{t('Exchange rate')}</span>
                <Input
                  key={currency}
                  name='fx_rate'
                  type='number'
                  required
                  min='0.000001'
                  max='100000'
                  step='0.000001'
                  defaultValue={
                    currency === props.supplier?.currency ? '1' : ''
                  }
                  readOnly={currency === props.supplier?.currency}
                />
                <span className='text-muted-foreground block text-xs'>
                  {t(
                    'Original currency per 1 unit of settlement currency, for example 6.7 CNY per USD'
                  )}
                </span>
              </label>
            </>
          )}
          {kind === 'user' && (
            <>
              <label className='block space-y-1.5'>
                <span>{t('Role')}</span>
                <Select
                  className='w-full'
                  value={role}
                  onChange={(event) => setRole(event.target.value)}
                >
                  <option value='supplier'>{t('Supplier head')}</option>
                  <option value='billing_manager'>
                    {t('Billing manager')}
                  </option>
                </Select>
              </label>
              {role === 'supplier' && (
                <label className='block space-y-1.5'>
                  <span>{t('Supplier head')}</span>
                  <Select
                    name='supplier_id'
                    className='w-full'
                    required
                    defaultValue={props.supplier?.id ?? ''}
                  >
                    <option value='' disabled>
                      {t('Select a supplier head')}
                    </option>
                    {props.suppliers.map((supplier) => (
                      <option key={supplier.id} value={supplier.id}>
                        {supplier.name}
                      </option>
                    ))}
                  </Select>
                </label>
              )}
            </>
          )}
        </FormSection>
      </form>
    </SidePanel>
  )
}
