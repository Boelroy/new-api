import { useEffect, useState, useSyncExternalStore } from 'react'
import { Dialog } from '@base-ui/react/dialog'
import { useTranslation } from 'react-i18next'
import { X } from 'lucide-react'
import type { ReactNode } from 'react'
import { Button, Input, cx } from './ui'

// ─────────────────────────────────────────────────────────────────────────
// Toast — a tiny module-level store with an imperative API so any code
// (including non-React fetch catch blocks) can call `toast.error(...)`.
// A single <Toaster/> mounted in App subscribes via useSyncExternalStore.
// ─────────────────────────────────────────────────────────────────────────

type ToastTone = 'error' | 'success' | 'info'
type ToastItem = { id: number; tone: ToastTone; message: string }

let toasts: ToastItem[] = []
let toastSeq = 1
const toastListeners = new Set<() => void>()

function emitToasts() {
  toasts = toasts.slice()
  toastListeners.forEach(l => l())
}

function pushToast(tone: ToastTone, message: string, ttlMs: number) {
  const id = toastSeq++
  toasts = [...toasts, { id, tone, message }]
  emitToasts()
  setTimeout(() => dismissToast(id), ttlMs)
  return id
}

function dismissToast(id: number) {
  const next = toasts.filter(t => t.id !== id)
  if (next.length !== toasts.length) {
    toasts = next
    emitToasts()
  }
}

function normalizeMessage(msg: unknown): string {
  if (msg instanceof Error) return msg.message || String(msg)
  if (typeof msg === 'string') return msg
  try {
    return JSON.stringify(msg)
  } catch {
    return String(msg)
  }
}

export const toast = {
  error(msg: unknown) {
    return pushToast('error', normalizeMessage(msg), 6000)
  },
  success(msg: unknown) {
    return pushToast('success', normalizeMessage(msg), 3500)
  },
  info(msg: unknown) {
    return pushToast('info', normalizeMessage(msg), 4000)
  },
  dismiss: dismissToast,
}

function subscribeToasts(cb: () => void) {
  toastListeners.add(cb)
  return () => toastListeners.delete(cb)
}

export function Toaster() {
  const items = useSyncExternalStore(
    subscribeToasts,
    () => toasts,
    () => toasts,
  )
  if (!items.length) return null
  return (
    <div className="fixed top-4 right-4 z-[100] flex w-[min(92vw,360px)] flex-col gap-2">
      {items.map(t => (
        <div
          key={t.id}
          role="status"
          className={cx(
            'animate-fadeUp flex items-start gap-3 rounded-xl bg-popover p-3 text-popover-foreground shadow-lg ring-1 ring-foreground/10',
            t.tone === 'error' && 'border-l-2 border-l-destructive',
            t.tone === 'success' && 'border-l-2 border-l-success',
            t.tone === 'info' && 'border-l-2 border-l-primary',
          )}
        >
          <span
            className={cx(
              'mt-0.5 h-2 w-2 shrink-0 rounded-full',
              t.tone === 'error' && 'bg-destructive',
              t.tone === 'success' && 'bg-success',
              t.tone === 'info' && 'bg-primary',
            )}
          />
          <div className="min-w-0 flex-1">
            <div className="mono-label mb-1 block">
              {t.tone === 'error' ? '出错' : t.tone === 'success' ? '成功' : '提示'}
            </div>
            <div className="break-words text-[13px] leading-snug text-foreground">{t.message}</div>
          </div>
          <button
            onClick={() => dismissToast(t.id)}
            aria-label="关闭"
            className="shrink-0 text-muted-foreground hover:text-foreground"
          >
            <svg
              width="15"
              height="15"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
            >
              <line x1="18" y1="6" x2="6" y2="18" />
              <line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>
      ))}
    </div>
  )
}

// ─────────────────────────────────────────────────────────────────────────
// Modal — declarative overlay for hosting a form/panel. Click-outside and
// Escape both close it. The panel passed as children brings its own card
// chrome; the modal only adds the dim backdrop, centering, and close button.
// ─────────────────────────────────────────────────────────────────────────

export function Modal(props: {
  open: boolean
  onClose: () => void
  children: ReactNode
  title?: ReactNode
  className?: string
}) {
  const { t } = useTranslation()
  return (
    <Dialog.Root
      open={props.open}
      onOpenChange={open => {
        if (!open) props.onClose()
      }}
    >
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-[110] bg-black/40" />
        <Dialog.Popup
          className={cx(
            'fixed left-1/2 top-1/2 z-[110] max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-xl border border-border bg-popover p-6 text-popover-foreground shadow-xl outline-none',
            props.className,
          )}
        >
          <Dialog.Title className="mb-4 pr-8 text-base font-semibold">{props.title ?? t('Confirm')}</Dialog.Title>
          <Dialog.Close
            render={
              <Button variant="ghost" className="absolute right-3 top-3 size-8 p-0" aria-label={t('Close dialog')} />
            }
          >
            <X className="size-4" aria-hidden="true" />
          </Dialog.Close>
          {props.children}
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

// ─────────────────────────────────────────────────────────────────────────
// Confirm — async imperative dialog to replace native window.confirm.
// `await confirmDialog({ message })` resolves true/false. A single
// <ConfirmHost/> mounted in App renders the pending request.
// ─────────────────────────────────────────────────────────────────────────

type ConfirmOptions = {
  message: string
  title?: string
  confirmText?: string
  cancelText?: string
  danger?: boolean
}
type PendingConfirm = ConfirmOptions & { resolve: (ok: boolean) => void }

let pendingConfirm: PendingConfirm | null = null
const confirmListeners = new Set<() => void>()

function emitConfirm() {
  confirmListeners.forEach(l => l())
}

export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  if (pendingConfirm) pendingConfirm.resolve(false)
  return new Promise<boolean>(resolve => {
    pendingConfirm = { ...opts, resolve }
    emitConfirm()
  })
}

function settleConfirm(ok: boolean) {
  const p = pendingConfirm
  pendingConfirm = null
  emitConfirm()
  p?.resolve(ok)
}

function subscribeConfirm(cb: () => void) {
  confirmListeners.add(cb)
  return () => confirmListeners.delete(cb)
}

export function ConfirmHost() {
  const { t } = useTranslation()
  const p = useSyncExternalStore(
    subscribeConfirm,
    () => pendingConfirm,
    () => pendingConfirm,
  )
  if (!p) return null
  return (
    <Modal open onClose={() => settleConfirm(false)} title={p.title ?? t('Confirm')}>
      <Dialog.Description className="whitespace-pre-line text-sm leading-relaxed text-muted-foreground">
        {p.message}
      </Dialog.Description>
      <div className="mt-6 flex justify-end gap-2">
        <Button variant="outline" autoFocus onClick={() => settleConfirm(false)}>
          {p.cancelText ?? t('Cancel')}
        </Button>
        <Button variant={p.danger ? 'danger' : 'primary'} onClick={() => settleConfirm(true)}>
          {p.confirmText ?? t('Confirm')}
        </Button>
      </div>
    </Modal>
  )
}

// ─────────────────────────────────────────────────────────────────────────
// Prompt — async imperative single-field input dialog replacing window.prompt.
// `await promptDialog({ message })` resolves the entered string, or null on
// cancel. A single <PromptHost/> mounted in App renders the pending request.
// ─────────────────────────────────────────────────────────────────────────

type PromptOptions = {
  message: string
  title?: string
  placeholder?: string
  defaultValue?: string
  confirmText?: string
  cancelText?: string
}
type PendingPrompt = PromptOptions & { resolve: (value: string | null) => void }

let pendingPrompt: PendingPrompt | null = null
const promptListeners = new Set<() => void>()

function emitPrompt() {
  promptListeners.forEach(l => l())
}

export function promptDialog(opts: PromptOptions): Promise<string | null> {
  if (pendingPrompt) pendingPrompt.resolve(null)
  return new Promise<string | null>(resolve => {
    pendingPrompt = { ...opts, resolve }
    emitPrompt()
  })
}

function settlePrompt(value: string | null) {
  const p = pendingPrompt
  pendingPrompt = null
  emitPrompt()
  p?.resolve(value)
}

function subscribePrompt(cb: () => void) {
  promptListeners.add(cb)
  return () => promptListeners.delete(cb)
}

export function PromptHost() {
  const { t } = useTranslation()
  const p = useSyncExternalStore(
    subscribePrompt,
    () => pendingPrompt,
    () => pendingPrompt,
  )
  const [value, setValue] = useState('')
  useEffect(() => {
    if (p) setValue(p.defaultValue ?? '')
  }, [p])
  if (!p) return null
  return (
    <Modal open onClose={() => settlePrompt(null)} title={p.title ?? t('Confirm')}>
      <form
        onSubmit={event => {
          event.preventDefault()
          settlePrompt(value)
        }}
      >
        <Dialog.Description className="mb-4 whitespace-pre-line text-sm leading-relaxed text-muted-foreground">
          {p.message}
        </Dialog.Description>
        <Input
          autoFocus
          aria-label={p.title ?? t('Text')}
          value={value}
          placeholder={p.placeholder}
          onChange={event => setValue(event.target.value)}
        />
        <div className="mt-6 flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={() => settlePrompt(null)}>
            {p.cancelText ?? t('Cancel')}
          </Button>
          <Button type="submit">{p.confirmText ?? t('Confirm')}</Button>
        </div>
      </form>
    </Modal>
  )
}
