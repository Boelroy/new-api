import { cx } from './ui'

// Provider identity chip — a small light chip carrying the provider's logo in
// its brand colour (Gemini keeps its signature blue→violet gradient). Keyed by
// the newapi channel `type` integer (mirrors the PRESETS table in
// BatchCreatePanel). Used in the Key Capacity list and the batch-create
// provider dropdown so each channel's upstream is visually distinct.

type ProviderMeta = {
  key: string
  label: string
  // Brand colour for the glyph / monogram.
  color: string
  // Short monogram used when there's no dedicated glyph below.
  mono?: string
}

// channel type -> provider. Keep in sync with BatchCreatePanel PRESETS.
const BY_TYPE: Record<number, ProviderMeta> = {
  1: { key: 'openai', label: 'OpenAI', color: '#10A37F' },
  3: { key: 'azure', label: 'Azure', color: '#0078D4', mono: 'Az' },
  14: { key: 'anthropic', label: 'Anthropic', color: '#D97757' },
  20: { key: 'openrouter', label: 'OpenRouter', color: '#6467F2', mono: 'OR' },
  24: { key: 'gemini', label: 'Gemini', color: '#1C69FF' },
  33: { key: 'aws', label: 'AWS', color: '#FF9900', mono: 'aws' },
  41: { key: 'vertex', label: 'Vertex AI', color: '#34A853', mono: 'V' },
}

export function providerMeta(type: number): ProviderMeta {
  return BY_TYPE[type] ?? { key: 'unknown', label: `类型 ${type}`, color: '#687083', mono: '?' }
}

function Glyph({ k, color }: { k: string; color: string }) {
  switch (k) {
    case 'openai':
      return (
        <svg viewBox="0 0 24 24" width="100%" height="100%" fill={color} aria-hidden>
          <path d="M22.2819 9.8211a5.9847 5.9847 0 0 0-.5157-4.9108 6.0462 6.0462 0 0 0-6.5098-2.9A6.0651 6.0651 0 0 0 4.9807 4.1818a5.9847 5.9847 0 0 0-3.9977 2.9 6.0462 6.0462 0 0 0 .7427 7.0966 5.98 5.98 0 0 0 .511 4.9107 6.051 6.051 0 0 0 6.5146 2.9001A5.9847 5.9847 0 0 0 13.2599 24a6.0557 6.0557 0 0 0 5.7718-4.2058 5.9894 5.9894 0 0 0 3.9977-2.9001 6.0557 6.0557 0 0 0-.7475-7.0729zm-9.022 12.6081a4.4755 4.4755 0 0 1-2.8764-1.0408l.1419-.0804 4.7783-2.7582a.7948.7948 0 0 0 .3927-.6813v-6.7369l2.02 1.1686a.071.071 0 0 1 .038.052v5.5826a4.504 4.504 0 0 1-4.4945 4.4944zm-9.6607-4.1254a4.4708 4.4708 0 0 1-.5346-3.0137l.1419.0852 4.783 2.7582a.7712.7712 0 0 0 .7806 0l5.8428-3.3685v2.3324a.0804.0804 0 0 1-.0332.0615L9.74 19.9502a4.4992 4.4992 0 0 1-6.1408-1.6464zM2.3408 7.8956a4.485 4.485 0 0 1 2.3655-1.9728V11.6a.7664.7664 0 0 0 .3879.6765l5.8144 3.3543-2.0201 1.1685a.0757.0757 0 0 1-.071 0l-4.8303-2.7865A4.504 4.504 0 0 1 2.3408 7.872zm16.5963 3.8558L13.1038 8.364 15.1192 7.2a.0757.0757 0 0 1 .071 0l4.8303 2.7913a4.4944 4.4944 0 0 1-.6765 8.1042v-5.6772a.79.79 0 0 0-.407-.667zm2.0107-3.0231l-.142-.0852-4.7735-2.7818a.7759.7759 0 0 0-.7854 0L9.409 9.2297V6.8974a.0662.0662 0 0 1 .0284-.0615l4.8303-2.7866a4.4992 4.4992 0 0 1 6.6802 4.66zM8.3065 12.863l-2.02-1.1638a.0804.0804 0 0 1-.038-.0567V6.0742a4.4992 4.4992 0 0 1 7.3757-3.4537l-.142.0805L8.704 5.459a.7948.7948 0 0 0-.3927.6813zm1.0976-2.3654l2.602-1.4998 2.6069 1.4998v2.9994l-2.5974 1.4997-2.6067-1.4997Z" />
        </svg>
      )
    case 'anthropic':
      return (
        <svg viewBox="0 0 24 24" width="100%" height="100%" fill={color} aria-hidden>
          <path d="M17.3041 3.541h-3.6718l6.696 16.918H24Zm-10.6082 0L0 20.459h3.7442l1.3693-3.5527h7.0052l1.3693 3.5528h3.7442L10.5363 3.541Zm-.3712 10.2232 2.2914-5.9456 2.2914 5.9456Z" />
        </svg>
      )
    case 'gemini':
      // Signature blue→violet gradient. Gradient id is namespaced so multiple
      // marks on one page don't collide.
      return (
        <svg viewBox="0 0 24 24" width="100%" height="100%" aria-hidden>
          <defs>
            <linearGradient id="pm-gemini" x1="0" y1="0" x2="24" y2="24" gradientUnits="userSpaceOnUse">
              <stop offset="0" stopColor="#4285F4" />
              <stop offset="0.5" stopColor="#9B72CB" />
              <stop offset="1" stopColor="#D96570" />
            </linearGradient>
          </defs>
          <path fill="url(#pm-gemini)" d="M12 24A14.304 14.304 0 0 0 0 12 14.304 14.304 0 0 0 12 0a14.305 14.305 0 0 0 12 12 14.305 14.305 0 0 0-12 12" />
        </svg>
      )
    default:
      return null
  }
}

export function ProviderMark({
  type,
  size = 18,
  className,
}: {
  type: number
  size?: number
  className?: string
}) {
  const m = providerMeta(type)
  const hasGlyph = ['openai', 'anthropic', 'gemini'].includes(m.key)
  const pad = Math.round(size * 0.2)
  return (
    <span
      title={m.label}
      className={cx('inline-flex shrink-0 items-center justify-center rounded-[5px] border font-semibold leading-none', className)}
      style={{
        width: size,
        height: size,
        // Faint tint of the brand colour so the chip itself reads as coloured.
        background: hasGlyph ? '#ffffff' : m.color + '14',
        borderColor: m.color + '33',
        color: m.color,
      }}
    >
      {hasGlyph ? (
        <span style={{ width: size - pad, height: size - pad, display: 'inline-flex' }}>
          <Glyph k={m.key} color={m.color} />
        </span>
      ) : (
        <span style={{ fontSize: m.mono && m.mono.length > 2 ? Math.round(size * 0.34) : Math.round(size * 0.46) }}>
          {m.mono ?? '?'}
        </span>
      )}
    </span>
  )
}

// Icon + label, used inside the batch-create provider dropdown rows.
export function ProviderOption({ type, label, size = 20 }: { type: number; label?: string; size?: number }) {
  const m = providerMeta(type)
  return (
    <span className="inline-flex items-center gap-2 min-w-0">
      <ProviderMark type={type} size={size} />
      <span className="truncate">{label ?? m.label}</span>
    </span>
  )
}
