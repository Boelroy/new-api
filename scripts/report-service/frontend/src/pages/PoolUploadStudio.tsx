import { useEffect, useState } from 'react'
import Layout from '../components/Layout'
import LocalPoolPanel from '../components/LocalPoolPanel'
import { api } from '../api'

// Studio-operator's local pool upload page. KeyCapacity itself calls a
// bunch of admin-scoped endpoints (/api/keys/data, batch-priority,
// etc.) that 403 for role=2, so we render just LocalPoolPanel here with
// the studio locked to the JWT claim.
//
// LocalPoolPanel gates its own studios dropdown on `lockedStudio`; when
// set, the panel skips /api/studios (also admin-scoped) and drops the
// pool-config bar for non-admin callers.

export default function PoolUploadStudio() {
  const [studio, setStudio] = useState<string | null>(null)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    void (async () => {
      try {
        const me = await api.getAuthMe()
        const s = (me?.studio ?? '').trim()
        if (!s) {
          setErr('你的账号还没绑定工作室，请让管理员在 Users 里绑一个。')
          setStudio('')
          return
        }
        setStudio(s)
      } catch (e: any) {
        setErr(e?.message || String(e))
        setStudio('')
      }
    })()
  }, [])

  return (
    <Layout
      title="上 5刀 Key"
      subtitle={studio ? `工作室：${studio} · 每个 key 默认额度 5 USD` : '加载中…'}
    >
      {err ? (
        <div className="bg-rose-50 border border-rose-200 text-rose-700 text-sm rounded-md px-3 py-2">
          {err}
        </div>
      ) : studio == null ? (
        <div className="text-sm text-gray-500">加载中…</div>
      ) : (
        <LocalPoolPanel lockedStudio={studio} />
      )}
    </Layout>
  )
}
