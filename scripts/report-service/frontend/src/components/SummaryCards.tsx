type Card = { label: string; value: string; color?: string }

export default function SummaryCards({ cards }: { cards: Card[] }) {
  return (
    <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-3 mb-6">
      {cards.map((c) => (
        <div
          key={c.label}
          className="bg-white border border-gray-200 rounded-xl px-4 py-3 hover:border-gray-300 transition-colors"
        >
          <div className="text-[10px] font-medium uppercase tracking-wider text-gray-400">{c.label}</div>
          <div className={`text-[19px] font-semibold mt-1 tabular-nums leading-tight ${c.color ?? 'text-gray-900'}`}>
            {c.value}
          </div>
        </div>
      ))}
    </div>
  )
}
