import { ArrowUpRight } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Button, Input } from '../components/ui'
import { errorMessage, request, type Session } from './api'
import { ErrorNotice, LanguagePicker } from './components'

export default function Login(props: { onLogin: (session: Session) => void }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    const fields = new FormData(event.currentTarget)
    setBusy(true)
    setError('')
    try {
      const result = await request<Session>('/login', {
        method: 'POST',
        body: {
          username: fields.get('username'),
          password: fields.get('password'),
        },
      })
      props.onLogin(result)
    } catch (error) {
      setError(errorMessage(error))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className='flex min-h-svh flex-col bg-[#F2F4EF] text-[#1b1c1e]'>
      <header className='border-b border-[#e1e2dc]'>
        <div className='mx-auto flex w-full max-w-[1280px] items-center justify-between px-6 py-4 sm:px-10'>
          <span className='text-sm font-bold tracking-[0.08em]'>
            new-api · {t('Supplier billing')}
          </span>
          <LanguagePicker />
        </div>
      </header>
      <main className='mx-auto grid w-full max-w-[1280px] flex-1 grid-cols-1 items-center gap-14 px-6 py-14 sm:px-10 lg:grid-cols-2 lg:gap-20'>
        <section className='order-2 lg:order-1'>
          <p className='mb-6 text-[13px] text-[#284F38]'>
            {t('Daily reconciliation')}
          </p>
          <h1 className='display text-[44px] leading-[1.1] sm:text-[56px]'>
            {t('Every expense, clearly accounted for.')}
          </h1>
          <p className='mt-6 max-w-[420px] text-[15px] leading-relaxed text-[#6f726a]'>
            {t(
              'Usage, discounts, exchange rates and prepaid balances in one place.'
            )}
          </p>
        </section>
        <section className='order-1 lg:order-2'>
          <div className='mx-auto w-full max-w-[420px] rounded-lg border border-[#e5e6e0] bg-white p-8 shadow-sm sm:p-9'>
            <h2 className='text-[26px] font-bold tracking-tight'>
              {t('Sign in')}
            </h2>
            <p className='mt-1.5 text-[13px] text-[#6f726a]'>
              {t('Use your supplier billing account.')}
            </p>
            {error && <ErrorNotice message={error} />}
            <form onSubmit={submit} className='mt-6 space-y-4'>
              <label className='block space-y-1.5 text-sm font-medium'>
                {t('Username')}
                <Input
                  name='username'
                  required
                  autoComplete='username'
                  maxLength={100}
                  className='h-11 bg-[#fafbf9] text-[#1b1c1e]'
                />
              </label>
              <label className='block space-y-1.5 text-sm font-medium'>
                {t('Password')}
                <Input
                  type='password'
                  name='password'
                  required
                  autoComplete='current-password'
                  maxLength={128}
                  className='h-11 bg-[#fafbf9] text-[#1b1c1e]'
                />
              </label>
              <Button
                type='submit'
                disabled={busy}
                className='h-12 w-full justify-between bg-[#284F38] px-4 text-white hover:bg-[#21402e]'
              >
                {busy ? t('Signing in…') : t('Sign in')}
                <ArrowUpRight className='size-4' aria-hidden='true' />
              </Button>
            </form>
          </div>
        </section>
      </main>
      <footer className='border-t border-[#e1e2dc] px-6 py-5 text-center text-xs text-[#6f726a]'>
        new-api · QuantumNous
      </footer>
    </div>
  )
}
