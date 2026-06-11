type Card = { label: string; value: string; color?: string }

export default function SummaryCards({ cards }: { cards: Card[] }) {
  return (
    <div className="flex gap-5 flex-wrap mb-5 pb-5 border-b border-gray-200">
      {cards.map((c) => (
        <div key={c.label} className="bg-white border border-gray-200 rounded-lg px-4 py-3 min-w-[110px]">
          <div className="text-[10px] font-medium uppercase tracking-wider text-gray-400">{c.label}</div>
          <div className={`text-xl font-semibold mt-0.5 tabular-nums ${c.color ?? 'text-gray-900'}`}>{c.value}</div>
        </div>
      ))}
    </div>
  )
}
