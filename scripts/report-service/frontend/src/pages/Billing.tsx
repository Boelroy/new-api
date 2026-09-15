import { useState } from 'react'
import UserBill from './UserBill'
import SupplierBill from './SupplierBill'
import { cx } from '../components/ui'

type Tab = 'user' | 'supplier'

// Billing is a thin shell hosting two tabs — 用户账单 (per-user/key) and
// 供应商账单 (per-supplier-group). Each tab renders its own Layout (title,
// actions differ), so the shared tab bar is passed down and rendered at the
// top of each tab's content.
export default function Billing() {
  const [tab, setTab] = useState<Tab>('user')

  const tabBar = (
    <div className="flex items-center gap-1 mb-4 border-b border-gray-200">
      {([['user', '用户账单'], ['supplier', '供应商账单']] as [Tab, string][]).map(([key, label]) => (
        <button
          key={key}
          onClick={() => setTab(key)}
          className={cx(
            'px-4 py-2 text-sm -mb-px border-b-2 transition-colors',
            tab === key
              ? 'border-brand text-brand font-medium'
              : 'border-transparent text-gray-500 hover:text-gray-800',
          )}
        >
          {label}
        </button>
      ))}
    </div>
  )

  return tab === 'user'
    ? <UserBill tabBar={tabBar} />
    : <SupplierBill tabBar={tabBar} />
}
