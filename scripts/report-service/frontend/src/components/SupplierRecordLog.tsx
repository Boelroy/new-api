import { useEffect, useMemo, useState } from 'react'
import {
  api,
  type SupplierRecordLogAccount,
  type SupplierRecordLogAggregate,
  type SupplierRecordLogFilter,
  type SupplierRecordLogRow,
} from '../api'

// 使用日志 — the portal's 「使用日志查询」 proxied through report-service.
// The portal filters by one platform account id (ID1.0 / ID2.0). Suppliers can
// only pick their own accounts (enforced server-side); 「全部账号」 means all of
// their own accounts for a supplier (server fans out per account) and every
// account of the shared supplier login for an admin.

const PAGE_SIZE = 50

function fmtLocal(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function todayStart(): string {
  const d = new Date()
  d.setHours(0, 0, 0, 0)
  return fmtLocal(d)
}

// Default query end: an hour ahead, so a long-open page still covers new logs.
function hourAhead(): string {
  return fmtLocal(new Date(Date.now() + 3600_000))
}

function fmtTime(s: string): string {
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? s : fmtLocal(d)
}

function fmtMoney(cny: number, fx: number): string {
  return `$${(cny / fx).toFixed(2)} · ¥${cny.toFixed(2)}`
}

export default function SupplierRecordLog({ isAdmin }: { isAdmin: boolean }) {
  const [accounts, setAccounts] = useState<SupplierRecordLogAccount[]>([])
  const [platform, setPlatform] = useState<'1.0' | '2.0'>('2.0')
  const [accountId, setAccountId] = useState('')
  const [accountSearch, setAccountSearch] = useState('')
  const [startTime, setStartTime] = useState(todayStart())
  const [endTime, setEndTime] = useState(hourAhead())
  const [model, setModel] = useState('')
  const [page, setPage] = useState(1)

  const [rows, setRows] = useState<SupplierRecordLogRow[]>([])
  const [total, setTotal] = useState(0)
  const [agg, setAgg] = useState<SupplierRecordLogAggregate | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    api.supplierRecordLogAccounts()
      .then(r => setAccounts(r.accounts))
      .catch(e => setErr(e?.message || String(e)))
  }, [])

  const platformId = (a: SupplierRecordLogAccount) => (platform === '1.0' ? a.a1_id : a.a2_id)

  const options = useMemo(() => {
    const q = accountSearch.trim().toLowerCase()
    return accounts.filter(a => {
      if (platformId(a) <= 0) return false
      if (!q) return true
      return [a.alias, a.remark, a.provider, a.username ?? '', String(platformId(a))].some(v => v.toLowerCase().includes(q))
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accounts, accountSearch, platform])

  const pendingCount = useMemo(() => accounts.filter(a => platformId(a) <= 0).length,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [accounts, platform])

  async function runQuery(nextPage: number, account = accountId) {
    setErr(null)
    const filter: SupplierRecordLogFilter = {
      platform_type: platform,
      start_time: startTime.trim(),
      end_time: endTime.trim() || undefined,
      model: model.trim() || undefined,
      account_id: account ? Number(account) : undefined,
    }
    setLoading(true)
    try {
      const [list, sum] = await Promise.all([
        api.supplierRecordLogList({ ...filter, page: nextPage, page_size: PAGE_SIZE }),
        nextPage === 1 ? api.supplierRecordLogAggregate(filter) : Promise.resolve(agg),
      ])
      setRows(list.list)
      setTotal(list.total)
      setAgg(sum)
      setPage(nextPage)
    } catch (e: any) {
      setErr(e?.message || String(e))
    } finally {
      setLoading(false)
    }
  }

  function resetToToday() {
    setStartTime(todayStart())
    setEndTime(hourAhead())
  }

  const fx = agg?.fx_rate || 7.2
  // A supplier's all-accounts merge (agg.accounts set) only reaches 1000 rows deep.
  const maxPages = agg?.accounts ? Math.floor(1000 / PAGE_SIZE) : Infinity
  const pages = Math.max(1, Math.min(maxPages, Math.ceil(total / PAGE_SIZE)))

  return (
    <section className="bg-white border border-gray-200 rounded-lg p-4 sm:p-5">
      <div className="mb-4">
        <h2 className="text-base font-semibold">使用日志</h2>
        <p className="text-[11px] text-gray-400 mt-0.5">
          号池逐条请求日志（实时）。{isAdmin ? '「全部账号」查询号池下全部账号。' : '只能查询你上传的账号；「全部账号」最多翻看最新 1000 条。'}
          {pendingCount > 0 && ` 有 ${pendingCount} 个账号号池尚未分配平台账号 ID，暂不可选。`}
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-2 mb-4">
        <div>
          <label className="block text-[10px] text-gray-400 mb-0.5">平台</label>
          <select
            value={platform}
            onChange={e => { setPlatform(e.target.value as '1.0' | '2.0'); setAccountId('') }}
            className="border border-gray-300 rounded-md px-2 py-1.5 text-xs"
          >
            <option value="2.0">2.0</option>
            <option value="1.0">1.0</option>
          </select>
        </div>
        <div>
          <label className="block text-[10px] text-gray-400 mb-0.5">筛选账号</label>
          <input
            value={accountSearch}
            onChange={e => setAccountSearch(e.target.value)}
            placeholder="别名 / 备注 / ID"
            className="border border-gray-300 rounded-md px-2 py-1.5 text-xs w-32"
          />
        </div>
        <div>
          <label className="block text-[10px] text-gray-400 mb-0.5">账号（{options.length}）</label>
          <select
            value={accountId}
            onChange={e => setAccountId(e.target.value)}
            className="border border-gray-300 rounded-md px-2 py-1.5 text-xs w-72"
          >
            <option value="">全部账号</option>
            {options.map(a => (
              <option key={a.id} value={platformId(a)}>
                {platformId(a)} · {a.alias}{a.remark ? ` · ${a.remark}` : ''}{isAdmin && a.username ? ` · ${a.username}` : ''}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className="block text-[10px] text-gray-400 mb-0.5">模型</label>
          <input
            value={model}
            onChange={e => setModel(e.target.value)}
            placeholder="全部"
            className="border border-gray-300 rounded-md px-2 py-1.5 text-xs w-36"
          />
        </div>
        <div>
          <label className="block text-[10px] text-gray-400 mb-0.5">开始</label>
          <input value={startTime} onChange={e => setStartTime(e.target.value)} className="border border-gray-300 rounded-md px-2 py-1.5 text-xs w-40" />
        </div>
        <div>
          <label className="block text-[10px] text-gray-400 mb-0.5">结束</label>
          <input value={endTime} onChange={e => setEndTime(e.target.value)} className="border border-gray-300 rounded-md px-2 py-1.5 text-xs w-40" />
        </div>
        <button
          onClick={resetToToday}
          className="border border-gray-300 hover:bg-gray-50 text-xs rounded-md px-3 py-2 transition-colors"
        >
          今天
        </button>
        <button
          onClick={() => runQuery(1)}
          disabled={loading}
          className="bg-brand hover:bg-brand-700 disabled:opacity-60 text-white text-xs rounded-md px-3 py-2 transition-colors"
        >
          {loading ? '查询中…' : '查询'}
        </button>
      </div>

      {err && (
        <div className="mb-3 bg-rose-50 border border-rose-200 text-rose-700 text-xs rounded-md px-3 py-2">{err}</div>
      )}

      {agg && (
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-4">
          <div className="border border-gray-200 rounded-md px-3 py-2">
            <div className="text-[10px] text-gray-400">区间用量</div>
            <div className="text-lg font-semibold tabular-nums">{fmtMoney(agg.cost_cny, fx)}</div>
          </div>
          <div className="border border-gray-200 rounded-md px-3 py-2">
            <div className="text-[10px] text-gray-400">RPM（近 1 分钟）</div>
            <div className="text-lg font-semibold tabular-nums">{agg.rpm.toLocaleString()}</div>
          </div>
          <div className="border border-gray-200 rounded-md px-3 py-2">
            <div className="text-[10px] text-gray-400">TPM（近 1 分钟）</div>
            <div className="text-lg font-semibold tabular-nums">{agg.tpm.toLocaleString()}</div>
          </div>
        </div>
      )}

      {agg?.accounts && agg.accounts.length > 0 && (
        <div className="mb-4 overflow-x-auto max-h-72 overflow-y-auto border border-gray-200 rounded-md">
          <table className="w-full text-xs border-collapse">
            <thead className="sticky top-0 bg-white">
              <tr className="text-left border-b border-gray-200">
                <th className="py-1.5 px-3 mono-label">账号（{agg.accounts.length}，点击查看该号）</th>
                <th className="py-1.5 px-3 mono-label text-right">用量</th>
                <th className="py-1.5 px-3 mono-label text-right">RPM</th>
                <th className="py-1.5 px-3 mono-label text-right">TPM</th>
              </tr>
            </thead>
            <tbody>
              {agg.accounts.map(a => (
                <tr
                  key={a.account_id}
                  onClick={() => { setAccountId(String(a.account_id)); runQuery(1, String(a.account_id)) }}
                  className="border-b border-gray-100 hover:bg-gray-50 cursor-pointer"
                >
                  <td className="py-1.5 px-3">
                    {a.alias} <span className="text-gray-400">· ID {a.account_id}</span>
                  </td>
                  <td className="py-1.5 px-3 text-right tabular-nums whitespace-nowrap">{fmtMoney(a.cost_cny, fx)}</td>
                  <td className="py-1.5 px-3 text-right tabular-nums">{a.rpm.toLocaleString()}</td>
                  <td className="py-1.5 px-3 text-right tabular-nums">{a.tpm.toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {rows.length > 0 && (
        <>
          <div className="overflow-x-auto">
            <table className="w-full text-sm border-collapse">
              <thead>
                <tr className="text-left border-b border-gray-200">
                  <th className="py-2 pr-3 mono-label">时间</th>
                  <th className="py-2 pr-3 mono-label">账号</th>
                  <th className="py-2 pr-3 mono-label">模型</th>
                  <th className="py-2 pr-3 mono-label">流</th>
                  <th className="py-2 pr-3 mono-label text-right">Tokens 入 / 出</th>
                  <th className="py-2 pr-3 mono-label text-right">费用</th>
                  <th className="py-2 pr-3 mono-label text-right">耗时</th>
                  <th className="py-2 pr-3 mono-label">结果</th>
                </tr>
              </thead>
              <tbody>
                {rows.map(r => {
                  const failed = !!r.err_msg?.trim() || (r.status_code > 0 && r.status_code !== 200)
                  return (
                    <tr key={r.id} className="border-b border-gray-100 hover:bg-gray-50 align-top">
                      <td className="py-2 pr-3 text-xs whitespace-nowrap tabular-nums">{fmtTime(r.req_time)}</td>
                      <td className="py-2 pr-3 text-xs">
                        <div>{r.account_name}</div>
                        <div className="text-[10px] text-gray-400">ID {r.account_id}</div>
                      </td>
                      <td className="py-2 pr-3 text-xs">{r.model_name}</td>
                      <td className="py-2 pr-3 text-xs">{r.stream ? '流' : '非流'}</td>
                      <td className="py-2 pr-3 text-xs text-right tabular-nums">
                        {r.prompt_tokens.toLocaleString()} / {r.completion_tokens.toLocaleString()}
                      </td>
                      <td className="py-2 pr-3 text-xs text-right tabular-nums whitespace-nowrap">{fmtMoney(r.cost, fx)}</td>
                      <td className="py-2 pr-3 text-xs text-right tabular-nums">{(r.duration_ms / 1000).toFixed(1)}s</td>
                      <td className="py-2 pr-3 text-xs max-w-xs">
                        {failed ? (
                          <span className="text-rose-600 break-words" title={r.err_msg}>
                            {r.status_code || r.err_code} {r.err_msg?.slice(0, 80)}
                          </span>
                        ) : (
                          <span className="text-success">成功</span>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
          <div className="flex items-center justify-between mt-3 text-xs text-gray-500">
            <span>共 {total.toLocaleString()} 条</span>
            <div className="flex items-center gap-2">
              <button
                onClick={() => runQuery(page - 1)}
                disabled={loading || page <= 1}
                className="border border-gray-300 rounded-md px-2 py-1 disabled:opacity-40"
              >
                上一页
              </button>
              <span className="tabular-nums">{page} / {pages}</span>
              <button
                onClick={() => runQuery(page + 1)}
                disabled={loading || page >= pages}
                className="border border-gray-300 rounded-md px-2 py-1 disabled:opacity-40"
              >
                下一页
              </button>
            </div>
          </div>
        </>
      )}
      {agg && rows.length === 0 && !loading && (
        <div className="text-sm text-gray-400 border border-dashed border-gray-200 rounded-md px-3 py-6 text-center">该时间段没有日志</div>
      )}
    </section>
  )
}
