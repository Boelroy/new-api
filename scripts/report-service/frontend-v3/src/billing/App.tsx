import { Collapsible } from '@base-ui/react/collapsible'
import { Plus, RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  BrowserRouter,
  useLocation,
  useNavigate,
  useSearchParams,
} from 'react-router-dom'

import { Badge, Button, Card, Input, Select } from '../components/ui'
import {
  APIError,
  billingDate,
  errorMessage,
  request,
  setSession,
  setUnauthorizedHandler,
  type Overview,
  type Vendor,
  type Session,
  type Source,
  type Statement,
  type Supplier,
  type User,
} from './api'
import {
  BillingShell,
  DataTable,
  Empty,
  ErrorNotice,
  Loading,
  type Page,
} from './components'
import EntryForm, { type FormTarget } from './EntryForm'
import Login from './Login'
import { SettlementOverview, SettlementSheet } from './SettlementView'
import SourceConnections from './SourceConnections'
import StatementView from './StatementView'
import SupplierRelationships from './SupplierRelationships'

function useResource<T>(url: string | null, version: number) {
  const [state, setState] = useState<{
    url: string | null
    version: number
    loading: boolean
    data?: T
    error?: string
  }>({ url: null, version: -1, loading: false })
  useEffect(() => {
    if (!url) return
    const controller = new AbortController()
    setState({ url, version, loading: true })
    void request<T>(url, { signal: controller.signal })
      .then((data) => {
        if (!controller.signal.aborted) {
          setState({ url, version, loading: false, data })
        }
      })
      .catch((error) => {
        if (!controller.signal.aborted) {
          setState({ url, version, loading: false, error: errorMessage(error) })
        }
      })
    return () => controller.abort()
  }, [url, version])
  if (state.url !== url || state.version !== version) {
    return { loading: Boolean(url), data: undefined, error: undefined }
  }
  return state
}

export default function App() {
  const [session, updateSession] = useState<Session | null>()
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setError('')
    setUnauthorizedHandler(() => {
      setSession(null)
      updateSession(null)
    })
    void request<Session>('/session', { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) {
          setSession(result)
          updateSession(result)
        }
      })
      .catch((error) => {
        if (controller.signal.aborted) return
        if (error instanceof APIError && error.status === 401) {
          updateSession(null)
        } else setError(errorMessage(error))
      })
    return () => {
      controller.abort()
      setUnauthorizedHandler(undefined)
    }
  }, [attempt])
  if (error) {
    return (
      <div className='mx-auto max-w-lg p-8'>
        <ErrorNotice
          message={error}
          onRetry={() => setAttempt((value) => value + 1)}
        />
      </div>
    )
  }
  if (session === undefined) return <Loading />
  const acceptSession = (value: Session | null) => {
    setSession(value)
    updateSession(value)
  }
  if (!session) return <Login onLogin={acceptSession} />
  // The router owns only this deployment's prefix; outside it (for example a
  // misconfigured proxy) the workspace still renders from the site root.
  const prefix = import.meta.env.BASE_URL.replace(/\/+$/, '')
  const basename = window.location.pathname.startsWith(prefix) ? prefix : ''
  return (
    <BrowserRouter basename={basename}>
      <Workspace
        key={session.user.id}
        session={session}
        onSession={acceptSession}
      />
    </BrowserRouter>
  )
}

const pages: Page[] = ['settlement', 'sources', 'suppliers']
const datePattern = /^\d{4}-\d{2}-\d{2}$/

function Workspace(props: {
  session: Session
  onSession: (session: Session | null) => void
}) {
  const { t } = useTranslation()
  // Page, supplier head and dates live in the URL, so a refresh, bookmark or
  // back/forward returns to the same view.
  const location = useLocation()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const routed = location.pathname.replace(/^\/+/, '').split('/')[0] as Page
  const page: Page =
    pages.includes(routed) &&
    (props.session.user.role === 'billing_manager' || routed === 'settlement')
      ? routed
      : 'settlement'
  const selected = Number(params.get('supplier')) || 0
  const routedStart = params.get('start') ?? ''
  const routedEnd = params.get('end') ?? ''
  const start = datePattern.test(routedStart)
    ? routedStart
    : `${billingDate().slice(0, 7)}-01`
  const end = datePattern.test(routedEnd) ? routedEnd : billingDate()
  const setParam = (key: string, value: string) =>
    setParams(
      (current) => {
        const next = new URLSearchParams(current)
        if (value) next.set(key, value)
        else next.delete(key)
        return next
      },
      { replace: true }
    )
  const setPage = (next: Page) =>
    navigate({ pathname: `/${next}`, search: location.search })
  const setSelected = (id: number) => setParam('supplier', id ? String(id) : '')
  const setStart = (value: string) => setParam('start', value)
  const setEnd = (value: string) => setParam('end', value)
  const [version, setVersion] = useState(0)
  const [target, setTarget] = useState<FormTarget>()
  const [error, setError] = useState('')
  const [logoutBusy, setLogoutBusy] = useState(false)
  const [connectionsOpen, setConnectionsOpen] = useState(false)
  const [financeOpen, setFinanceOpen] = useState(false)
  const manager = props.session.user.role === 'billing_manager'
  const suppliers = useResource<Supplier[]>('/suppliers', version)
  const available = suppliers.data ?? []
  const headID = manager ? selected : props.session.user.supplier_id
  const supplier = available.find((item) => item.id === headID)
  const scopedHeads =
    headID > 0 ? available.filter((item) => item.id === headID) : available
  const statement = useResource<Statement>(
    page === 'settlement' && supplier && start && end
      ? `/statement?${new URLSearchParams({ supplier_id: String(supplier.id), start, end })}`
      : null,
    version
  )
  const overview = useResource<Overview>(
    manager && page === 'settlement' && headID === 0 && start && end
      ? `/overview?${new URLSearchParams({ start, end })}`
      : null,
    version
  )
  const vendors = useResource<Vendor[]>(
    page === 'sources'
      ? `/vendors${headID > 0 ? `?supplier_id=${headID}` : ''}`
      : null,
    version
  )
  const sources = useResource<Source[]>(
    manager && page === 'sources' && connectionsOpen && supplier
      ? '/sources'
      : null,
    version
  )
  const users = useResource<User[]>(
    manager && page === 'suppliers' ? '/users' : null,
    version
  )
  const visibleSources = (sources.data ?? []).filter(
    (item) => item.supplier_id === supplier?.id
  )
  const title = {
    settlement: 'Settlement',
    sources: 'Supplier relationships',
    suppliers: 'Supplier heads and accounts',
  }[page]
  function refresh() {
    setError('')
    setVersion((value) => value + 1)
  }
  async function logout() {
    setLogoutBusy(true)
    try {
      await request('/logout', { method: 'POST', body: {} })
      props.onSession(null)
    } catch (error) {
      setError(errorMessage(error))
    } finally {
      setLogoutBusy(false)
    }
  }
  return (
    <BillingShell
      session={props.session}
      page={page}
      onPage={setPage}
      onLogout={() => void logout()}
      onPassword={() => setTarget({ kind: 'password' })}
      busy={logoutBusy}
    >
      <div className='mb-5 flex flex-col gap-3 sm:flex-row sm:items-center'>
        <div className='min-w-0'>
          <h1 className='display text-lg leading-tight sm:text-[22px]'>
            {t(title)}
          </h1>
          <p className='text-muted-foreground mt-1 text-xs sm:text-[13px]'>
            {t('Daily billing in Beijing time (UTC+8)')}
          </p>
        </div>
        <div className='flex flex-wrap items-center gap-2 sm:ml-auto'>
          <Button variant='outline' onClick={refresh} aria-label={t('Refresh')}>
            <RefreshCw className='size-3.5' aria-hidden='true' />
            {t('Refresh')}
          </Button>
          {page === 'settlement' && supplier && (
            <>
              <Button onClick={() => setTarget({ kind: 'usage' })}>
                <Plus className='size-3.5' aria-hidden='true' />
                {t('Add usage')}
              </Button>
              {manager && (
                <Button
                  variant='outline'
                  onClick={() => setTarget({ kind: 'recharge' })}
                >
                  {t('Record recharge')}
                </Button>
              )}
            </>
          )}
          {page === 'suppliers' && (
            <>
              <Button onClick={() => setTarget({ kind: 'supplier' })}>
                {t('Add supplier head')}
              </Button>
              <Button
                variant='outline'
                onClick={() => setTarget({ kind: 'user' })}
              >
                {t('Add account')}
              </Button>
            </>
          )}
        </div>
      </div>
      {(error || suppliers.error) && (
        <ErrorNotice
          message={error || suppliers.error || ''}
          onRetry={refresh}
        />
      )}
      {suppliers.loading && <Loading />}
      {page !== 'suppliers' && available.length > 0 && (
        <Card className='mb-5 flex flex-wrap items-end gap-4 p-4'>
          <label className='text-muted-foreground flex min-w-48 flex-col gap-1.5 text-xs'>
            {t('Supplier head')}
            <Select
              value={headID}
              disabled={!manager}
              onChange={(event) => {
                setSelected(Number(event.target.value))
                setTarget(undefined)
                setFinanceOpen(false)
                setConnectionsOpen(false)
              }}
            >
              {manager && <option value={0}>{t('All supplier heads')}</option>}
              {available.map((item) => (
                <option value={item.id} key={item.id}>
                  {item.name}
                </option>
              ))}
            </Select>
          </label>
          {page === 'settlement' && (
            <>
              <label className='text-muted-foreground flex flex-col gap-1.5 text-xs'>
                {t('Start date')}
                <Input
                  type='date'
                  value={start}
                  max={end}
                  onChange={(event) => setStart(event.target.value)}
                />
              </label>
              <label className='text-muted-foreground flex flex-col gap-1.5 text-xs'>
                {t('End date')}
                <Input
                  type='date'
                  value={end}
                  min={start}
                  max={billingDate()}
                  onChange={(event) => setEnd(event.target.value)}
                />
              </label>
            </>
          )}
        </Card>
      )}
      {!suppliers.loading && !suppliers.error && !available.length && (
        <Card>
          <Empty>
            <p>{t('No supplier heads yet')}</p>
            {manager && (
              <Button onClick={() => setTarget({ kind: 'supplier' })}>
                {t('Add supplier head')}
              </Button>
            )}
          </Empty>
        </Card>
      )}
      {page === 'settlement' && (
        <>
          {(overview.loading || (supplier && statement.loading)) && <Loading />}
          {(overview.error || (supplier && statement.error)) && (
            <ErrorNotice
              message={overview.error || statement.error || ''}
              onRetry={refresh}
            />
          )}
          {!supplier && overview.data && (
            <SettlementOverview
              key={`${start}-${end}`}
              overview={overview.data}
              onOpen={(item) => setSelected(item.id)}
              onInitial={(item, initial) =>
                setTarget({ kind: 'initial', supplier: item, initial })
              }
            />
          )}
          {supplier && statement.data && (
            <SettlementSheet
              key={`${supplier.id}-${start}-${end}`}
              statement={statement.data}
              onInitial={
                manager
                  ? () =>
                      setTarget({
                        kind: 'initial',
                        initial: statement.data?.initial_balance,
                      })
                  : undefined
              }
              onSavePrice={async (line, unitPrice, fxRate) => {
                await request('/usage/price', {
                  method: 'POST',
                  body: {
                    supplier_id: supplier.id,
                    date: line.date,
                    usage_type: line.usage_type,
                    unit_price: unitPrice,
                    currency:
                      line.settlement_status === 'settled'
                        ? line.currency
                        : 'CNY',
                    fx_rate: fxRate,
                  },
                })
                refresh()
              }}
              onClaim={
                manager
                  ? (line) => setTarget({ kind: 'claim', line })
                  : undefined
              }
            />
          )}
          {supplier && (
            <Collapsible.Root
              className='border-border mt-6 rounded-xl border'
              open={financeOpen}
              onOpenChange={setFinanceOpen}
            >
              <Collapsible.Trigger
                render={
                  <Button
                    variant='ghost'
                    className='w-full justify-start p-4'
                  />
                }
              >
                {t('Balance and payment details')}
              </Collapsible.Trigger>
              <Collapsible.Panel className='border-border border-t p-4'>
                {statement.loading && <Loading />}
                {statement.error && (
                  <ErrorNotice message={statement.error} onRetry={refresh} />
                )}
                {statement.data && (
                  <StatementView
                    key={`${supplier.id}-${start}-${end}`}
                    showDailyTotals={false}
                    statement={statement.data}
                    onReverse={
                      manager
                        ? (recharge) => setTarget({ kind: 'reverse', recharge })
                        : undefined
                    }
                  />
                )}
              </Collapsible.Panel>
            </Collapsible.Root>
          )}
        </>
      )}
      {page === 'sources' && (
        <>
          {vendors.loading && <Loading />}
          {vendors.error && (
            <ErrorNotice message={vendors.error} onRetry={refresh} />
          )}
          {vendors.data && (
            <SupplierRelationships heads={scopedHeads} vendors={vendors.data} />
          )}
          {manager && (
            <Collapsible.Root
              className='border-border mt-6 rounded-xl border'
              open={connectionsOpen}
              onOpenChange={setConnectionsOpen}
            >
              <Collapsible.Trigger
                render={
                  <Button
                    variant='ghost'
                    className='w-full justify-start p-4'
                  />
                }
              >
                {t('Connection settings')}
              </Collapsible.Trigger>
              <Collapsible.Panel className='border-border space-y-4 border-t p-4'>
                {!supplier && (
                  <p className='text-muted-foreground text-sm'>
                    {t('Select a supplier head')}
                  </p>
                )}
                {supplier && (
                  <Button onClick={() => setTarget({ kind: 'source' })}>
                    <Plus className='size-3.5' aria-hidden='true' />
                    {t('Add source')}
                  </Button>
                )}
                {sources.loading && <Loading />}
                {sources.error && (
                  <ErrorNotice message={sources.error} onRetry={refresh} />
                )}
                {sources.data && (
                  <SourceConnections
                    sources={visibleSources}
                    onTarget={setTarget}
                  />
                )}
              </Collapsible.Panel>
            </Collapsible.Root>
          )}
        </>
      )}
      {page === 'suppliers' && (
        <div className='space-y-6'>
          {!!available.length && (
            <DataTable headings={['Supplier head', 'Settlement currency']}>
              {available.map((item) => (
                <tr key={item.id}>
                  <td>{item.name}</td>
                  <td>{item.currency}</td>
                </tr>
              ))}
            </DataTable>
          )}
          <div>
            <h2 className='mb-3 text-sm font-semibold'>{t('Accounts')}</h2>
            {users.loading && <Loading />}
            {users.error && (
              <ErrorNotice message={users.error} onRetry={refresh} />
            )}
            {users.data && (
              <DataTable
                headings={[
                  'Username',
                  'Role',
                  'Supplier head',
                  'Status',
                  'Actions',
                ]}
                empty={!users.data.length}
              >
                {users.data.map((user) => (
                  <tr key={user.id}>
                    <td>{user.username}</td>
                    <td>
                      {t(
                        user.role === 'billing_manager'
                          ? 'Billing manager'
                          : 'Supplier head'
                      )}
                    </td>
                    <td>
                      {available.find((item) => item.id === user.supplier_id)
                        ?.name ?? '—'}
                    </td>
                    <td>
                      <Badge tone={user.disabled ? 'neutral' : 'success'}>
                        {t(user.disabled ? 'Disabled' : 'Active')}
                      </Badge>
                    </td>
                    <td>
                      <Button
                        size='sm'
                        variant='danger'
                        disabled={
                          user.disabled || user.id === props.session.user.id
                        }
                        onClick={() => setTarget({ kind: 'disable', user })}
                      >
                        {t('Disable account')}
                      </Button>
                    </td>
                  </tr>
                ))}
              </DataTable>
            )}
          </div>
        </div>
      )}
      {target && (
        <EntryForm
          key={`${target.kind}-${target.source?.id ?? target.user?.id ?? 0}-${(target.supplier ?? supplier)?.id ?? 0}`}
          target={target}
          supplier={target.supplier ?? supplier}
          suppliers={available}
          start={start}
          end={end}
          onClose={() => setTarget(undefined)}
          onSaved={(session) => {
            if (session) props.onSession(session)
            setTarget(undefined)
            refresh()
          }}
        />
      )}
    </BillingShell>
  )
}
