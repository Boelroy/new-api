import { useRef, type ReactNode } from 'react'
import { Dialog } from '@base-ui/react/dialog'
import { X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button, cx } from './ui'

// The standalone V3 app shares new-api's drawer structure and Base UI behavior.
// Keep the footer outside the scrolling form so actions remain reachable.
export function SidePanel(props: {
  title: ReactNode
  children: ReactNode
  footer?: ReactNode
  busy?: boolean
  onClose: () => void
}) {
  const { t } = useTranslation()
  const titleRef = useRef<HTMLHeadingElement>(null)
  return (
    <Dialog.Root
      open
      onOpenChange={open => {
        if (!open && !props.busy) props.onClose()
      }}
    >
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-50 bg-black/40 backdrop-blur-[2px]" />
        <Dialog.Popup
          initialFocus={titleRef}
          className="fixed inset-y-0 right-0 z-50 flex w-full max-w-2xl flex-col bg-background shadow-xl outline-none sm:inset-y-2 sm:right-2 sm:rounded-xl sm:border sm:border-border"
        >
          <header className="flex shrink-0 items-center justify-between gap-4 border-b border-border px-5 py-4 sm:px-6">
            <Dialog.Title ref={titleRef} tabIndex={-1} className="text-base font-semibold outline-none">
              {props.title}
            </Dialog.Title>
            <Dialog.Close
              disabled={props.busy}
              render={<Button variant="ghost" className="size-8 p-0" aria-label={t('Close dialog')} />}
            >
              <X className="size-4" aria-hidden="true" />
            </Dialog.Close>
          </header>
          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-5 py-5 sm:px-6">
            <fieldset
              disabled={props.busy}
              aria-busy={props.busy}
              className={cx('v3-form min-w-0 space-y-5', props.busy && 'opacity-70')}
            >
              {props.children}
            </fieldset>
          </div>
          {props.footer && (
            <footer className="shrink-0 border-t border-border bg-muted/30 px-5 py-4 sm:px-6 [&>div]:mt-0 [&_button]:min-w-20">
              {props.footer}
            </footer>
          )}
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
