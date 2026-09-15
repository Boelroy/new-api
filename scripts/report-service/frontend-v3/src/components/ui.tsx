import { forwardRef, useEffect, useState } from 'react'
import type { ReactNode, ButtonHTMLAttributes, InputHTMLAttributes } from 'react'
import { cn } from '../lib/utils'

export { cn }

// cx is the v1 class joiner name kept for the ported code; it delegates to cn
// so Tailwind conflicts still resolve.
export function cx(...parts: Array<string | false | null | undefined>): string {
  return cn(parts)
}

export function MonoLabel({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cx('mono-label', className)}>{children}</span>
}

export function Card({ children, className, as: As = 'div' }: { children: ReactNode; className?: string; as?: any }) {
  return <As className={cx('card', className)}>{children}</As>
}

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost' | 'outline' | 'confirm' | 'danger'
  size?: 'sm' | 'md' | 'lg'
}

// Button matches the v1 API (primary/outline/danger/... + sm/md/lg) so ported
// call sites need no changes, but is styled with new-api semantic tokens.
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'primary', size = 'md', className, ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      type="button"
      className={cx(
        'inline-flex shrink-0 items-center justify-center gap-1.5 rounded-lg border border-transparent font-medium whitespace-nowrap transition-all outline-none select-none',
        'focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 active:translate-y-px',
        size === 'sm' && 'h-7 px-2.5 text-[0.8rem]',
        size === 'md' && 'h-8 px-3 text-sm',
        size === 'lg' && 'h-9 px-4 text-sm',
        variant === 'primary' && 'bg-primary text-primary-foreground hover:bg-primary/90',
        variant === 'secondary' && 'bg-secondary text-secondary-foreground hover:bg-secondary/80',
        variant === 'outline' &&
          'border-border bg-background hover:bg-muted hover:text-foreground dark:border-input dark:bg-input/30 dark:hover:bg-input/50',
        variant === 'ghost' && 'text-muted-foreground hover:bg-muted hover:text-foreground dark:hover:bg-muted/50',
        variant === 'confirm' && 'bg-success text-success-foreground hover:bg-success/90',
        variant === 'danger' &&
          'bg-destructive/10 text-destructive hover:bg-destructive/20 dark:bg-destructive/20 dark:hover:bg-destructive/30',
        className,
      )}
      {...props}
    />
  )
})

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function Input(
  { className, ...props },
  ref,
) {
  if (props.type === 'checkbox' || props.type === 'radio') {
    return (
      <input
        ref={ref}
        className={cx(
          'size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-50',
          className,
        )}
        {...props}
      />
    )
  }
  return (
    <input
      ref={ref}
      className={cx(
        'h-8 w-full min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1 text-sm transition-colors outline-none',
        'placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-inset focus-visible:ring-ring/50',
        'disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30',
        className,
      )}
      {...props}
    />
  )
})

export function Eyebrow({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={cx(
        'flex items-center gap-2 text-[10px] font-semibold uppercase tracking-wider text-primary',
        className,
      )}
    >
      {children}
    </div>
  )
}

export function Badge({
  children,
  tone = 'neutral',
  className,
}: {
  children: ReactNode
  tone?: 'neutral' | 'brand' | 'success' | 'warning' | 'lime' | 'danger'
  className?: string
}) {
  return (
    <span
      className={cx(
        'inline-flex h-5 w-fit items-center gap-1 rounded-full border border-transparent px-2 py-0.5 text-xs font-medium',
        tone === 'neutral' && 'bg-muted text-muted-foreground',
        tone === 'brand' && 'bg-primary text-primary-foreground',
        tone === 'success' && 'bg-success/10 text-success',
        tone === 'warning' && 'border-warning/40 bg-warning/10 text-warning',
        tone === 'lime' && 'bg-success/10 text-success',
        tone === 'danger' && 'bg-destructive/10 text-destructive',
        className,
      )}
    >
      {children}
    </span>
  )
}

export function ProgressBar({ value, className }: { value: number; className?: string }) {
  return (
    <div className={cx('h-1.5 w-full overflow-hidden rounded-full bg-muted', className)}>
      <div
        className="h-full rounded-full bg-primary transition-all duration-500"
        style={{ width: `${Math.max(0, Math.min(100, value))}%` }}
      />
    </div>
  )
}

export function Divider({ className }: { className?: string }) {
  return <div className={cx('h-px w-full bg-border', className)} />
}

export function LiveDot({ className }: { className?: string }) {
  return (
    <span className={cx('relative inline-flex h-2 w-2', className)}>
      <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-primary/60" />
      <span className="relative inline-flex h-2 w-2 rounded-full bg-primary" />
    </span>
  )
}

export function RelativeTime({ at, className }: { at: number | null; className?: string }) {
  const [, force] = useState(0)
  useEffect(() => {
    if (!at) return
    const id = setInterval(() => force(n => n + 1), 5000)
    return () => clearInterval(id)
  }, [at])
  if (!at) return null
  const secs = Math.max(0, Math.floor((Date.now() - at) / 1000))
  const label =
    secs < 5
      ? '刚刚'
      : secs < 60
        ? `${secs}s 前`
        : secs < 3600
          ? `${Math.floor(secs / 60)}m 前`
          : `${Math.floor(secs / 3600)}h 前`
  return <span className={cx('tnum', className)}>{label}</span>
}

export function LivePollChip({ at, className }: { at: number | null; className?: string }) {
  return (
    <span className={cx('inline-flex items-center gap-1.5 text-[11px] text-muted-foreground', className)}>
      <LiveDot />
      <span>自动刷新</span>
      {at != null && (
        <>
          <span className="text-border">·</span>
          <RelativeTime at={at} />
        </>
      )}
    </span>
  )
}

export function Check({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" className={cx('h-4 w-4', className)} fill="none">
      <path d="M3 8.5l3 3 7-7" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

export function Ring({ value, size = 132, label }: { value: number; size?: number; label?: string }) {
  const stroke = 8
  const r = (size - stroke) / 2
  const c = 2 * Math.PI * r
  const off = c - (Math.max(0, Math.min(100, value)) / 100) * c
  return (
    <div className="relative inline-flex items-center justify-center" style={{ width: size, height: size }}>
      <svg width={size} height={size} className="-rotate-90">
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          style={{ stroke: 'var(--border)' }}
          strokeWidth={stroke}
          fill="none"
        />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          style={{ stroke: 'var(--primary)' }}
          strokeWidth={stroke}
          fill="none"
          strokeDasharray={c}
          strokeDashoffset={off}
          strokeLinecap="round"
          className="transition-all duration-700"
        />
      </svg>
      <div className="absolute flex flex-col items-center">
        <span className="text-3xl font-semibold tracking-tight tnum">{value}%</span>
        {label && <span className="mono-label mt-0.5">{label}</span>}
      </div>
    </div>
  )
}

export function Textarea({ className, ...props }: import('react').TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      className={cx(
        'min-h-20 w-full min-w-0 rounded-lg border border-input bg-transparent px-3 py-2 text-sm outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/30 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30',
        className,
      )}
      {...props}
    />
  )
}

// Keep native select semantics (including mobile pickers) for the existing
// onChange API; centralize its focus, disabled, sizing and theme behavior.
export function Select({ className, ...props }: import('react').SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className={cx(
        'h-9 min-w-0 rounded-lg border border-input bg-background px-3 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/30 disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
      {...props}
    />
  )
}
