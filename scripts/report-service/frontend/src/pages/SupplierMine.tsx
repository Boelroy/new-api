import { useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { Supplier2AccountTable, Supplier2Usage } from '../components/SupplierSecondary'
import { api, type Supplier2Account } from '../api'

// 账号上号 · 我的账号: the read-only view for a user an admin assigned 账号 2
// accounts to. The server picks the accounts (by owner) and leaves cost out
// for non-admins; there is no settings, owner editing or other users' data.
export default function SupplierMine() {
  const [accounts, setAccounts] = useState<Supplier2Account[] | null>(null)
  const [canViewCost, setCanViewCost] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  async function load() {
    setErr(null)
    setLoading(true)
    try {
      const res = await api.supplier2Mine()
      setAccounts(res.accounts || [])
      setCanViewCost(res.can_view_cost)
    } catch (e: any) {
      setErr(e?.message || String(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  return (
    <Layout title="账号上号 / 我的账号" subtitle="只读：仅展示管理员分配给你的账号与用量">
      {accounts !== null && accounts.length === 0 && !err ? (
        <div className="bg-white border border-gray-200 rounded-lg p-5 text-sm text-gray-500">暂无分配给你的账号。</div>
      ) : (
        <>
          {accounts !== null && accounts.length > 0 && (
            <Supplier2Usage accounts={accounts} load={api.supplier2MineMetrics} showCost={canViewCost} />
          )}
          <Supplier2AccountTable accounts={accounts || []} loading={loading} error={err} onRefresh={() => void load()} />
        </>
      )}
    </Layout>
  )
}
