import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown } from 'lucide-react'

export function FormSection(props: { children: ReactNode; advanced?: boolean }) {
  const { t } = useTranslation()
  if (props.advanced)
    return (
      <details className="group rounded-xl border border-border">
        <summary className="flex cursor-pointer list-none items-center justify-between p-4 text-sm font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring">
          {t('Advanced Settings')}
          <ChevronDown className="size-4 transition-transform group-open:rotate-180" aria-hidden="true" />
        </summary>
        <div className="space-y-4 border-t border-border p-4">{props.children}</div>
      </details>
    )
  return (
    <section className="space-y-4 rounded-xl border border-border p-4">
      <h3 className="text-sm font-semibold">{t('Basic Information')}</h3>
      {props.children}
    </section>
  )
}
