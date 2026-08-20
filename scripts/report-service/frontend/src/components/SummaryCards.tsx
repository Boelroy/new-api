import { MonoLabel } from './ui'

type Card = { label: string; value: string; color?: string }

export default function SummaryCards({ cards }: { cards: Card[] }) {
  return (
    <div className="stagger grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-3 mb-6">
      {cards.map((c) => (
        <div
          key={c.label}
          className="card card-hover px-4 py-3"
        >
          <MonoLabel>{c.label}</MonoLabel>
          <div className={`text-[19px] font-semibold mt-1.5 tnum tracking-tightest leading-tight ${c.color ?? 'text-ink'}`}>
            {c.value}
          </div>
        </div>
      ))}
    </div>
  )
}
