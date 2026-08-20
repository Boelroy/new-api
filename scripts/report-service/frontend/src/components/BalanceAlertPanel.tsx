import { useCallback, useEffect, useState } from 'react'
import { api, type NotifyGroupState, type NotifyStatus, type NotifyThreshold } from '../api'

// "余额报警" tab on KeyCapacity. Shows the live per-group balance the Lark
// alerter judges, and lets an admin override the thresholds per group. Blank
// threshold inputs mean "inherit the server default" (NOTIFY_*_THRESHOLD),
// which is stored as NULL rather than 0 — 0 disables the check entirely.

function fmtETA(hours: number | null): { text: string; cls: string } {
  if (hours === null) return { text: '—', cls: 'text-gray-400' }
  if (hours < 0) return { text: '已超额', cls: 'text-rose-600' }
  const cls = hours > 48 ? 'text-emerald-600' : hours > 12 ? 'text-amber-600' : 'text-rose-600'
  if (hours >= 24 * 30) return { text: '>30天', cls }
  if (hours >= 24) return { text: `${Math.floor(hours / 24)}天${Math.floor(hours % 24)}小时`, cls }
  return { text: `${hours.toFixed(1)}小时`, cls }
}

// Threshold inputs are kept as strings so an empty box stays distinguishable
// from a typed 0. '' → null (inherit default), '0' → 0 (check disabled).
type Draft = { usd: string; hours: string; enabled: boolean; note: string }

function draftFrom(t: NotifyThreshold | undefined): Draft {
  return {
    usd: t?.usd_threshold != null ? String(t.usd_threshold) : '',
    hours: t?.hours_threshold != null ? String(t.hours_threshold) : '',
    enabled: t?.enabled ?? true,
    note: t?.note ?? '',
  }
}

function parseThreshold(v: string): { value: number | null; error: boolean } {
  const s = v.trim()
  if (s === '') return { value: null, error: false }
  const n = Number(s)
  if (!Number.isFinite(n) || n < 0) return { value: null, error: true }
  return { value: n, error: false }
}

export default function BalanceAlertPanel() {
  const [status, setStatus] = useState<NotifyStatus | null>(null)
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const [loadErr, setLoadErr] = useState<string | null>(null)
  const [savingGroup, setSavingGroup] = useState<string | null>(null)
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [pushBusy, setPushBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      const [st, th] = await Promise.all([api.getNotifyStatus(), api.getNotifyThresholds()])
      const byGroup = new Map(th.thresholds.map(t => [t.group, t]))
      const next: Record<string, Draft> = {}
      for (const g of st.groups) next[g.group] = draftFrom(byGroup.get(g.group))
      // Overrides can outlive their group (all its channels disabled) — keep
      // them visible so a stale row can still be deleted.
      for (const t of th.thresholds) if (!next[t.group]) next[t.group] = draftFrom(t)
      setStatus(st)
      setDrafts(next)
      setLoadErr(null)
    } catch (e: any) {
      setLoadErr(e?.message || String(e))
    }
  }, [])

  useEffect(() => {
    void load()
    const t = setInterval(load, 60000)
    return () => clearInterval(t)
  }, [load])

  const setDraft = (group: string, patch: Partial<Draft>) =>
    setDrafts(prev => ({ ...prev, [group]: { ...prev[group], ...patch } }))

  const save = async (group: string) => {
    const d = drafts[group]
    const usd = parseThreshold(d.usd)
    const hours = parseThreshold(d.hours)
    if (usd.error || hours.error) {
      setMsg({ ok: false, text: '阈值必须是 ≥ 0 的数字，留空表示沿用默认值' })
      return
    }
    setSavingGroup(group)
    setMsg(null)
    try {
      await api.saveNotifyThresholds([
        { group, usd_threshold: usd.value, hours_threshold: hours.value, enabled: d.enabled, note: d.note },
      ])
      setMsg({ ok: true, text: `已保存分组 ${group} 的报警阈值` })
      await load()
    } catch (e: any) {
      setMsg({ ok: false, text: '保存失败: ' + (e?.message || e) })
    } finally {
      setSavingGroup(null)
    }
  }

  const reset = async (group: string) => {
    setSavingGroup(group)
    setMsg(null)
    try {
      await api.deleteNotifyThreshold(group)
      setMsg({ ok: true, text: `分组 ${group} 已恢复为默认阈值` })
      await load()
    } catch (e: any) {
      setMsg({ ok: false, text: '重置失败: ' + (e?.message || e) })
    } finally {
      setSavingGroup(null)
    }
  }

  const pushDigest = async () => {
    setPushBusy(true)
    setMsg(null)
    try {
      const res = await api.pushNotifyDigest()
      setMsg({ ok: true, text: `已推送 ${res.groups} 个分组的余额到 Lark` })
    } catch (e: any) {
      setMsg({ ok: false, text: '推送失败: ' + (e?.message || e) })
    } finally {
      setPushBusy(false)
    }
  }

  if (loadErr) {
    return (
      <div className="bg-white border border-gray-200 rounded-xl p-4 text-xs text-rose-600">
        加载失败：{loadErr}
      </div>
    )
  }
  if (!status) {
    return <div className="text-xs text-gray-400 px-1">加载中…</div>
  }

  const groups: NotifyGroupState[] = status.groups
  const orphanGroups = Object.keys(drafts).filter(g => !groups.some(x => x.group === g))

  return (
    <div className="space-y-4">
      <div className="bg-white border border-gray-200 rounded-xl p-4 flex items-start justify-between gap-4 flex-wrap">
        <div className="space-y-1">
          <div className="text-sm font-semibold">Lark 余额报警（按分组）</div>
          <div className="text-[11px] text-gray-500 leading-relaxed">
            每 10 分钟按 <span className="font-mono">channels."group"</span> 检查一次，命中阈值发 Lark，同一分组同类报警 1 小时内只发一条。
            <br />
            默认阈值：余额 &lt; ${status.default_thresholds.usd} · 预计剩余 &lt; {status.default_thresholds.hours} 小时（0 = 该项不检查）。
          </div>
          {!status.lark_configured && (
            <div className="text-[11px] text-amber-700 bg-amber-50 border border-amber-200 rounded-md px-2 py-1 inline-block">
              未配置 LARK_WEBHOOK，报警只会计算不会发送
            </div>
          )}
        </div>
        <button
          onClick={pushDigest}
          disabled={pushBusy || !status.lark_configured}
          className="bg-brand text-white rounded-md px-3 py-1.5 text-xs hover:bg-brand-700 disabled:opacity-40 disabled:cursor-not-allowed shrink-0"
        >
          {pushBusy ? '推送中…' : '推送各分组余额到 Lark'}
        </button>
      </div>

      {msg && (
        <div
          className={`text-[11px] rounded-md px-3 py-2 border ${
            msg.ok ? 'text-emerald-700 bg-emerald-50 border-emerald-200' : 'text-rose-700 bg-rose-50 border-rose-200'
          }`}
        >
          {msg.text}
        </div>
      )}

      <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
            <thead>
              <tr>
                {['分组', 'Key 数', '剩余 / 总额度', '最近1小时', '预计剩余时长', '余额阈值 ($)', '时长阈值 (h)', '备注', '状态', ''].map(h => (
                  <th
                    key={h}
                    className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200"
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {groups.map(g => {
                const d = drafts[g.group] ?? draftFrom(undefined)
                const etaF = fmtETA(g.eta_hours)
                const alerting = g.would_alert.usd || g.would_alert.hours
                const busy = savingGroup === g.group
                return (
                  <tr key={g.group} className={alerting ? 'bg-rose-50/40' : 'hover:bg-gray-50'}>
                    <td className="px-3 py-1.5 border-b border-gray-50 font-medium">{g.group}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">
                      {g.channels_with_quota}/{g.channels}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">
                      {g.channels_with_quota > 0 ? (
                        <>
                          ${g.total_remaining_usd.toFixed(2)}
                          <span className="text-gray-400"> / ${g.total_quota_usd.toFixed(2)}</span>
                        </>
                      ) : (
                        <span className="text-gray-300">未配额度</span>
                      )}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">
                      ${g.last_hour_usd.toFixed(4)}
                    </td>
                    <td className={`px-3 py-1.5 border-b border-gray-50 font-medium ${etaF.cls}`}>{etaF.text}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      <input
                        type="number"
                        min="0"
                        step="1"
                        value={d.usd}
                        onChange={e => setDraft(g.group, { usd: e.target.value })}
                        placeholder={`默认 ${status.default_thresholds.usd}`}
                        className="w-24 border border-gray-200 rounded-md px-2 py-1 text-xs bg-white focus:outline-none focus:border-gray-900"
                      />
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      <input
                        type="number"
                        min="0"
                        step="1"
                        value={d.hours}
                        onChange={e => setDraft(g.group, { hours: e.target.value })}
                        placeholder={`默认 ${status.default_thresholds.hours}`}
                        className="w-20 border border-gray-200 rounded-md px-2 py-1 text-xs bg-white focus:outline-none focus:border-gray-900"
                      />
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      <input
                        value={d.note}
                        onChange={e => setDraft(g.group, { note: e.target.value })}
                        placeholder="可选"
                        className="w-32 border border-gray-200 rounded-md px-2 py-1 text-xs bg-white focus:outline-none focus:border-gray-900"
                      />
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      <label className="flex items-center gap-1.5 text-[11px] text-gray-600">
                        <input
                          type="checkbox"
                          checked={d.enabled}
                          onChange={e => setDraft(g.group, { enabled: e.target.checked })}
                          className="rounded border-gray-300"
                        />
                        启用
                      </label>
                      {alerting && d.enabled && (
                        <span className="text-[10px] text-rose-600">
                          {g.would_alert.usd ? '余额触发' : '时长触发'}
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      <div className="flex items-center gap-2">
                        <button
                          onClick={() => save(g.group)}
                          disabled={busy}
                          className="bg-brand text-white rounded-md px-2.5 py-1 text-[11px] hover:bg-brand-700 disabled:opacity-40"
                        >
                          {busy ? '…' : '保存'}
                        </button>
                        {g.configured && (
                          <button
                            onClick={() => reset(g.group)}
                            disabled={busy}
                            className="text-[11px] text-gray-400 hover:text-gray-700 disabled:opacity-40"
                          >
                            恢复默认
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                )
              })}
              {orphanGroups.map(group => (
                <tr key={group} className="bg-gray-50/60">
                  <td className="px-3 py-1.5 border-b border-gray-50 font-medium text-gray-500">{group}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-[11px] text-gray-400" colSpan={8}>
                    该分组当前没有启用的 Key，阈值配置已失效
                  </td>
                  <td className="px-3 py-1.5 border-b border-gray-50">
                    <button
                      onClick={() => reset(group)}
                      disabled={savingGroup === group}
                      className="text-[11px] text-gray-400 hover:text-rose-600 disabled:opacity-40"
                    >
                      删除
                    </button>
                  </td>
                </tr>
              ))}
              {groups.length === 0 && orphanGroups.length === 0 && (
                <tr>
                  <td colSpan={10} className="px-3 py-6 text-center text-xs text-gray-400">
                    没有启用的 Key，暂无分组
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}
