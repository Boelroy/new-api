import {
  CalendarDays,
  Database,
  LogOut,
  Moon,
  PanelLeft,
  Scale,
  Sun,
  Users,
  Wallet,
  KeyRound,
} from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '../../../../../web/src/i18n/languages'
import { formatNumber } from '../../../../../web/src/lib/number-format'
import { SidePanel } from '../components/SidePanel'
import { Button, Card, Select, cx } from '../components/ui'
import { useTheme } from '../context/theme-provider'
import type { Session } from './api'

export type Page = 'statement' | 'settlement' | 'sources' | 'suppliers'

export function LanguagePicker() {
  const { t, i18n } = useTranslation()
  return (
    <Select
      aria-label={t('Language')}
      value={i18n.language}
      className='h-8 max-w-32 text-xs'
      onChange={(event) => {
        localStorage.setItem('supplier-billing-language', event.target.value)
        void i18n.changeLanguage(event.target.value)
      }}
    >
      <option value='zh'>简体中文</option>
      <option value='en'>English</option>
      <option value='zh-TW'>繁體中文</option>
      <option value='fr'>Français</option>
      <option value='ja'>日本語</option>
      <option value='ru'>Русский</option>
      <option value='vi'>Tiếng Việt</option>
    </Select>
  )
}

// Report's shell embeds its own identity, navigation and logout API. This shell
// composes the same V3 primitives without importing Report authentication.
export function BillingShell(props: {
  session: Session
  page: Page
  onPage: (page: Page) => void
  onLogout: () => void
  onPassword: () => void
  children: ReactNode
  busy: boolean
}) {
  const { t } = useTranslation()
  const { theme, toggle } = useTheme()
  const [collapsed, setCollapsed] = useState(false)
  const [mobileOpen, setMobileOpen] = useState(false)
  const manager = props.session.user.role === 'billing_manager'
  const items = [
    {
      key: 'statement' as const,
      label: t('Daily usage'),
      icon: CalendarDays,
    },
    {
      key: 'settlement' as const,
      label: t('Settlement'),
      icon: Scale,
    },
    {
      key: 'sources' as const,
      label: t('Supplier relationships'),
      icon: Database,
    },
    ...(manager
      ? [
          {
            key: 'suppliers' as const,
            label: t('Supplier heads and accounts'),
            icon: Users,
          },
        ]
      : []),
  ]
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'b') {
        event.preventDefault()
        setCollapsed((value) => !value)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  const nav = (
    <nav
      aria-label={t('Billing navigation')}
      className='flex h-full flex-col gap-1 overflow-y-auto p-2'
    >
      {!collapsed && (
        <div className='text-muted-foreground/70 px-2 pb-2 text-[11px] font-medium tracking-wider uppercase'>
          {t('Supplier billing')}
        </div>
      )}
      {items.map((item) => (
        <Button
          key={item.key}
          variant='ghost'
          aria-current={props.page === item.key ? 'page' : undefined}
          title={item.label}
          onClick={() => {
            props.onPage(item.key)
            setMobileOpen(false)
          }}
          className={cx(
            'h-9 justify-start rounded-md p-2',
            collapsed && 'lg:justify-center',
            props.page === item.key &&
              'bg-sidebar-accent font-medium text-sidebar-accent-foreground'
          )}
        >
          <item.icon className='size-4 shrink-0' aria-hidden='true' />
          <span className={collapsed ? 'lg:hidden' : ''}>{item.label}</span>
        </Button>
      ))}
      <div className='border-sidebar-border text-muted-foreground mt-auto border-t px-2 pt-3 text-xs'>
        <span className={collapsed ? 'hidden' : ''}>new-api · QuantumNous</span>
      </div>
    </nav>
  )
  return (
    <div className='bg-background text-foreground flex h-svh flex-col'>
      <header className='sticky top-0 z-30 flex h-12 w-full shrink-0 items-center gap-2 bg-transparent px-2 sm:px-3'>
        <Button
          variant='ghost'
          className='size-8 p-0'
          aria-label={t('Toggle navigation')}
          onClick={() => {
            if (window.matchMedia('(min-width: 1024px)').matches) {
              setCollapsed((value) => !value)
            } else setMobileOpen((value) => !value)
          }}
        >
          <PanelLeft className='size-4' aria-hidden='true' />
        </Button>
        <span className='truncate px-1.5 text-sm font-bold tracking-[0.04em]'>
          {t('Supplier billing')}
        </span>
        <div className='ms-auto flex shrink-0 items-center gap-1 sm:gap-2'>
          <LanguagePicker />
          <Button
            variant='ghost'
            className='size-8 p-0'
            onClick={toggle}
            aria-label={t('Toggle theme')}
          >
            {theme === 'dark' ? (
              <Sun className='size-4' aria-hidden='true' />
            ) : (
              <Moon className='size-4' aria-hidden='true' />
            )}
          </Button>
          <Button
            variant='ghost'
            className='size-8 p-0'
            onClick={props.onPassword}
            aria-label={t('Change password')}
          >
            <KeyRound className='size-4' aria-hidden='true' />
          </Button>
          <span className='text-muted-foreground hidden max-w-32 truncate text-xs sm:inline'>
            {props.session.user.username}
          </span>
          <Button
            variant='ghost'
            disabled={props.busy}
            className='size-8 p-0'
            onClick={props.onLogout}
            aria-label={t('Sign out')}
          >
            <LogOut className='size-4' aria-hidden='true' />
          </Button>
        </div>
      </header>
      <div className='flex min-h-0 w-full flex-1'>
        <aside
          className={cx(
            'hidden shrink-0 p-2 lg:block',
            collapsed ? 'w-[4.25rem]' : 'w-56'
          )}
        >
          <div className='bg-sidebar text-sidebar-foreground ring-sidebar-border h-full rounded-lg shadow-sm ring-1'>
            {nav}
          </div>
        </aside>
        <main className='min-h-0 min-w-0 flex-1 overflow-y-auto'>
          <div className='mx-auto w-full max-w-[1600px] px-4 py-5 sm:px-6 sm:py-6 lg:px-8'>
            {props.children}
          </div>
        </main>
      </div>
      {mobileOpen && (
        <SidePanel
          title={t('Supplier billing')}
          onClose={() => setMobileOpen(false)}
        >
          {nav}
        </SidePanel>
      )}
    </div>
  )
}

export function Money(props: { value: string; currency: string }) {
  const { i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  // Main-console currency helpers use global gateway settings and can convert
  // USD again. Statements are already settled in each supplier's own currency.
  const [integer, fraction = ''] = props.value.split('.')
  const decimals = fraction.replace(/0+$/, '').padEnd(2, '0')
  const parts = new Intl.NumberFormat(locale, {
    style: 'currency',
    currency: props.currency,
    minimumFractionDigits: 2,
    maximumFractionDigits: 6,
  }).formatToParts(integer === '-0' ? -0 : BigInt(integer))
  return (
    <span className='tnum whitespace-nowrap'>
      {parts
        .map((part) => (part.type === 'fraction' ? decimals : part.value))
        .join('')}
    </span>
  )
}

export function Empty(props: { children: ReactNode }) {
  return (
    <div className='text-muted-foreground flex flex-col items-center gap-3 p-10 text-center text-sm'>
      <Wallet className='size-6' aria-hidden='true' />
      {props.children}
    </div>
  )
}

export function RequestCount(props: { value: string }) {
  const { i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const value = BigInt(props.value)
  // Ordinary counts share the console formatter. BigInt preserves exact
  // aggregate counts beyond the Number range accepted by that formatter.
  if (value <= BigInt(Number.MAX_SAFE_INTEGER)) {
    return <span className='tnum'>{formatNumber(Number(value), locale)}</span>
  }
  return (
    <span className='tnum'>{new Intl.NumberFormat(locale).format(value)}</span>
  )
}

export function Timestamp(props: { value: number }) {
  const { i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return (
    <time dateTime={new Date(props.value * 1000).toISOString()}>
      {new Intl.DateTimeFormat(locale, {
        timeZone: 'Asia/Shanghai',
        dateStyle: 'medium',
        timeStyle: 'short',
      }).format(new Date(props.value * 1000))}
    </time>
  )
}

export function UsageMetrics(props: {
  values: {
    request_count?: number
    input_tokens?: number
    output_tokens?: number
    cache_read_tokens?: number
    cache_write_tokens?: number
  }
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const rows = [
    ['Requests', props.values.request_count],
    ['Input tokens', props.values.input_tokens],
    ['Output tokens', props.values.output_tokens],
    ['Cache read tokens', props.values.cache_read_tokens],
    ['Cache write tokens', props.values.cache_write_tokens],
  ] as const
  return (
    <details className='text-muted-foreground mt-1 text-xs'>
      <summary className='text-primary cursor-pointer'>
        {t('Token usage')}
      </summary>
      <dl className='mt-2 space-y-1'>
        {rows.map(([label, value]) => (
          <div key={label} className='flex gap-3'>
            <dt>{t(label)}</dt>
            <dd className='tnum ml-auto'>{formatNumber(value ?? 0, locale)}</dd>
          </div>
        ))}
      </dl>
    </details>
  )
}

export function ErrorNotice(props: { message: string; onRetry?: () => void }) {
  const { t } = useTranslation()
  return (
    <div
      role='alert'
      className='border-destructive/25 bg-destructive/5 text-destructive my-3 flex flex-wrap items-center gap-3 rounded-lg border p-3 text-sm'
    >
      <span className='min-w-0 flex-1 break-words'>{t(props.message)}</span>
      {props.onRetry && (
        <Button variant='outline' onClick={props.onRetry}>
          {t('Retry')}
        </Button>
      )}
    </div>
  )
}

export function Loading() {
  const { t } = useTranslation()
  return (
    <div
      role='status'
      className='text-muted-foreground p-8 text-center text-sm'
    >
      {t('Loading…')}
    </div>
  )
}

export function DataTable(props: {
  headings: string[]
  children: ReactNode
  empty?: boolean
}) {
  const { t } = useTranslation()
  return (
    <Card className='overflow-hidden p-0'>
      <div className='overflow-x-auto'>
        <table className='w-full text-left text-xs sm:text-[13px]'>
          <thead className='bg-muted/40 text-muted-foreground'>
            <tr>
              {props.headings.map((heading) => (
                <th
                  key={heading}
                  scope='col'
                  className='px-4 py-3 font-medium whitespace-nowrap'
                >
                  {t(heading)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className='divide-border divide-y [&_td]:px-4 [&_td]:py-3'>
            {props.children}
          </tbody>
        </table>
      </div>
      {props.empty && <Empty>{t('No records in this period')}</Empty>}
    </Card>
  )
}
