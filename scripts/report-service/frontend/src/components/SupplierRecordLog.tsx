import { useEffect, useMemo, useState } from 'react'
import {
  api,
  type SupplierRecordLogAccount,
  type SupplierRecordLogAggregate,
  type SupplierRecordLogFilter,
  type SupplierRecordLogRow,
} from '../api'

// 使用日志 — the portal's 「使用日志查询」 request log, read from report-service's
// local copy (synced every minute). Accounts are platform account ids (ID1.0 /
// ID2.0). Suppliers can pick any of their own accounts (enforced server-side);
// picking none means all of them — or the whole log for an admin.

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
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [pickerOpen, setPickerOpen] = useState(false)
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

  const pickable = useMemo(() => accounts.filter(a => platformId(a) > 0),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [accounts, platform])

  const filtered = useMemo(() => {
    const q = accountSearch.trim().toLowerCase()
    if (!q) return pickable
    return pickable.filter(a =>
      [a.alias, a.remark, a.provider, a.username ?? '', String(platformId(a))].some(v => v.toLowerCase().includes(q)))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pickable, accountSearch])

  const pendingCount = accounts.length - pickable.length

  function toggle(id: number) {
    setSelected(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function selectFiltered() {
    setSelected(prev => new Set([...prev, ...filtered.map(platformId)]))
  }

  async function runQuery(nextPage: number, ids: Set<number> = selected) {
    setErr(null)
    const filter: SupplierRecordLogFilter = {
      platform_type: platform,
      start_time: startTime.trim(),
      end_time: endTime.trim() || undefined,
      model: model.trim() || undefined,
      account_ids: ids.size ? [...ids] : undefined,
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

  function focusAccount(id: number) {
    const only = new Set([id])
    setSelected(only)
    runQuery(1, only)
  }

  function resetToToday() {
    setStartTime(todayStart())
    setEndTime(hourAhead())
  }

  const fx = agg?.fx_rate || 7.2
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const pickerLabel = selected.size === 0 ? `全部账号（${pickable.length}）` : `已选 ${selected.size} 个账号`

  return (
    <section className="bg-white border border-gray-200 rounded-lg p-4 sm:p-5">
      <div className="mb-4">
        <h2 className="text-base font-semibold">使用日志</h2>
        <p className="text-[11px] text-gray-400 mt-0.5">
          号池逐条请求日志，每分钟同步到本地（延迟约 1–2 分钟），明细保留 30 天。
          {isAdmin ? '不选账号即查询全部日志。' : '只能查询你上传的账号，不选即为全部。'}
          {pendingCount > 0 && ` 有 ${pendingCount} 个账号号池尚未分配平台账号 ID，暂不可选。`}
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-2 mb-4">
        <div>
          <label className="block text-[10px] text-gray-400 mb-0.5">平台</label>
          <select
            value={platform}
            onChange={e => { setPlatform(e.target.value as '1.0' | '2.0'); setSelected(new Set()) }}
            className="border border-gray-300 rounded-md px-2 py-1.5 text-xs"
          >
            <option value="2.0">2.0</option>
            <option value="1.0">1.0</option>
          </select>
        </div>
        <div className="relative">
          <label className="block text-[10px] text-gray-400 mb-0.5">账号</label>
          <button
            type="button"
            onClick={() => setPickerOpen(o => !o)}
            className="border border-gray-300 rounded-md px-2 py-1.5 text-xs w-56 text-left flex items-center justify-between"
          >
            <span>{pickerLabel}</span>
            <span className="text-gray-400">{pickerOpen ? '▴' : '▾'}</span>
          </button>
          {pickerOpen && (
            <div className="absolute z-20 mt-1 w-[26rem] bg-white border border-gray-200 rounded-md shadow-lg p-2">
              <div className="flex items-center gap-2 mb-2">
                <input
                  value={accountSearch}
                  onChange={e => setAccountSearch(e.target.value)}
                  placeholder="搜索别名 / 备注 / ID"
                  className="flex-1 border border-gray-300 rounded-md px-2 py-1 text-xs"
                />
                <button type="button" onClick={selectFiltered} className="text-[11px] text-indigo-600 hover:text-indigo-800 whitespace-nowrap">
                  全选{accountSearch.trim() ? '搜索结果' : ''}
                </button>
                <button type="button" onClick={() => setSelected(new Set())} className="text-[11px] text-gray-500 hover:text-gray-800">
                  清空
                </button>
              </div>
              <div className="max-h-64 overflow-y-auto">
                {filtered.length === 0 ? (
                  <div className="text-xs text-gray-400 px-1 py-3 text-center">没有匹配的账号</div>
                ) : (
                  filtered.map(a => {
                    const id = platformId(a)
                    return (
                      <label key={a.id} className="flex items-center gap-2 px-1 py-1 text-xs hover:bg-gray-50 rounded cursor-pointer">
                        <input type="checkbox" checked={selected.has(id)} onChange={() => toggle(id)} />
                        <span className="tabular-nums text-gray-500">{id}</span>
                        <span className="truncate">{a.alias}</span>
                        {a.remark && <span className="text-gray-400 truncate">{a.remark}</span>}
                        {isAdmin && a.username && <span className="text-gray-400 ml-auto">{a.username}</span>}
                      </label>
                    )
                  })
                )}
              </div>
              <div className="flex justify-end mt-2">
                <button type="button" onClick={() => setPickerOpen(false)} className="text-[11px] border border-gray-300 rounded-md px-2 py-1 hover:bg-gray-50">
                  完成
                </button>
              </div>
            </div>
          )}
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
          onClick={() => { setPickerOpen(false); runQuery(1) }}
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
        <>
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-1">
            <div className="border border-gray-200 rounded-md px-3 py-2">
              <div className="text-[10px] text-gray-400">区间用量</div>
              <div className="text-lg font-semibold tabular-nums">{fmtMoney(agg.cost_cny, fx)}</div>
            </div>
            <div className="border border-gray-200 rounded-md px-3 py-2">
              <div className="text-[10px] text-gray-400">请求数（失败）</div>
              <div className="text-lg font-semibold tabular-nums">
                {agg.requests.toLocaleString()} <span className="text-sm text-rose-600">({agg.failed.toLocaleString()})</span>
              </div>
            </div>
            <div className="border border-gray-200 rounded-md px-3 py-2">
              <div className="text-[10px] text-gray-400">RPM（最近同步的 1 分钟）</div>
              <div className="text-lg font-semibold tabular-nums">{agg.rpm.toLocaleString()}</div>
            </div>
            <div className="border border-gray-200 rounded-md px-3 py-2">
              <div className="text-[10px] text-gray-400">TPM（最近同步的 1 分钟）</div>
              <div className="text-lg font-semibold tabular-nums">{agg.tpm.toLocaleString()}</div>
            </div>
          </div>
          <p className="text-[10px] text-gray-400 mb-4">
            {agg.synced_to > 0 ? `数据同步至 ${fmtLocal(new Date(agg.synced_to * 1000))}` : '尚未完成首次同步'}
          </p>
        </>
      )}

      {agg && agg.accounts.length > 1 && (
        <div className="mb-4 overflow-x-auto max-h-72 overflow-y-auto border border-gray-200 rounded-md">
          <table className="w-full text-xs border-collapse">
            <thead className="sticky top-0 bg-white">
              <tr className="text-left border-b border-gray-200">
                <th className="py-1.5 px-3 mono-label">账号（{agg.accounts.length}，点击只看该号）</th>
                <th className="py-1.5 px-3 mono-label text-right">请求（失败）</th>
                <th className="py-1.5 px-3 mono-label text-right">用量</th>
              </tr>
            </thead>
            <tbody>
              {agg.accounts.map(a => (
                <tr
                  key={a.account_id}
                  onClick={() => focusAccount(a.account_id)}
                  className="border-b border-gray-100 hover:bg-gray-50 cursor-pointer"
                >
                  <td className="py-1.5 px-3">
                    {a.alias || '—'} <span className="text-gray-400">· ID {a.account_id}</span>
                  </td>
                  <td className="py-1.5 px-3 text-right tabular-nums">
                    {a.requests.toLocaleString()} <span className="text-rose-600">({a.failed.toLocaleString()})</span>
                  </td>
                  <td className="py-1.5 px-3 text-right tabular-nums whitespace-nowrap">{fmtMoney(a.cost_cny, fx)}</td>
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
