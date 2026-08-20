import type { ReactNode, ButtonHTMLAttributes } from 'react'

// Tiny local classnames joiner so we don't take a hard dependency on `clsx`
// (only present transitively in node_modules). Accepts strings, falsy values.
export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ')
}

export function MonoLabel({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cx('mono-label', className)}>{children}</span>
}

export function Card({
  children,
  className,
  as: As = 'div',
}: {
  children: ReactNode
  className?: string
  as?: any
}) {
  return <As className={cx('card', className)}>{children}</As>
}

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost' | 'outline' | 'confirm' | 'danger'
  size?: 'sm' | 'md' | 'lg'
}

export function Button({ variant = 'primary', size = 'md', className, ...props }: ButtonProps) {
  return (
    <button
      className={cx(
        'inline-flex items-center justify-center gap-2 rounded-lg font-medium tracking-display transition-all duration-150 disabled:cursor-not-allowed disabled:opacity-40',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-outline focus-visible:ring-offset-2 focus-visible:ring-offset-canvas',
        size === 'sm' && 'px-3 py-1.5 text-[13px]',
        size === 'md' && 'px-4 py-2.5 text-sm',
        size === 'lg' && 'px-5 py-3 text-[15px]',
        variant === 'primary' && 'bg-brand text-white shadow-[0_1px_2px_rgba(11,16,32,0.12)] hover:bg-brand-700',
        variant === 'secondary' && 'bg-brand-50 text-brand hover:bg-brand-100',
        variant === 'outline' && 'border border-border bg-white text-ink hover:border-ink/30 hover:bg-canvas',
        variant === 'ghost' && 'text-secondary hover:bg-canvas hover:text-ink',
        variant === 'confirm' && 'bg-lime text-lime-ink hover:brightness-95',
        variant === 'danger' && 'bg-red-600 text-white hover:bg-red-700',
        className,
      )}
      {...props}
    />
  )
}

/** Numbered section eyebrow with a trailing hairline (reference style). */
export function Eyebrow({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx('eyebrow', className)}>{children}</div>
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
        'inline-flex items-center gap-1 rounded-full px-2.5 py-1 font-mono text-[10px] uppercase tracking-label',
        tone === 'neutral' && 'bg-canvas text-secondary',
        tone === 'brand' && 'bg-brand-50 text-brand',
        tone === 'success' && 'bg-[#E6F4EE] text-success',
        tone === 'warning' && 'bg-[#FBF0DC] text-warning',
        tone === 'lime' && 'bg-lime text-lime-ink',
        tone === 'danger' && 'bg-red-50 text-red-700',
        className,
      )}
    >
      {children}
    </span>
  )
}

export function ProgressBar({ value, className }: { value: number; className?: string }) {
  return (
    <div className={cx('h-1.5 w-full overflow-hidden rounded-full bg-border', className)}>
      <div
        className="h-full rounded-full bg-brand transition-all duration-500"
        style={{ width: `${Math.max(0, Math.min(100, value))}%` }}
      />
    </div>
  )
}

export function Divider({ className }: { className?: string }) {
  return <div className={cx('h-px w-full bg-border', className)} />
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
        <circle cx={size / 2} cy={size / 2} r={r} stroke="#E1E4DC" strokeWidth={stroke} fill="none" />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          stroke="#2864FF"
          strokeWidth={stroke}
          fill="none"
          strokeDasharray={c}
          strokeDashoffset={off}
          strokeLinecap="round"
          className="transition-all duration-700"
        />
      </svg>
      <div className="absolute flex flex-col items-center">
        <span className="text-3xl font-semibold tracking-tightest tnum">{value}%</span>
        {label && <span className="mono-label mt-0.5">{label}</span>}
      </div>
    </div>
  )
}
