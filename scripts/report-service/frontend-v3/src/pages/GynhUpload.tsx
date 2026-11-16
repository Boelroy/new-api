import { useCallback, useEffect, useMemo, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import Layout from '../components/Layout'
import { Badge, Button, Card, Input, Select, Textarea } from '../components/ui'
import { confirmDialog, toast } from '../components/feedback'
import { api, apiErrorMessage, ROLE_ADMIN, type GynhAccount, type GynhOptions, type GynhSettings } from '../api'
import { getCachedRole, loadRole } from '../auth'

// 供给台上号 — uploads keys to the 供给台 (GYNH) portal through the
// report-service proxy, which holds one shared portal login. Keys are posted
// one request each so a long batch never hits the proxy timeout and every key
// gets its own result. Only 'keyonly' providers are offered: the others need
// per-account JSON configs that don't fit a one-key-per-line batch. OpenAI
// keys get a tier probe first, with a confirm dialog for anything below
// Tier 5 (such keys may be given a lower scheduling priority).

const STATE_LABEL: Record<string, string> = {
  pending: '提交处理中',
  success: '提交成功',
  uncertain: '待核对',
  failed: '提交失败',
}

const STATE_TONE: Record<string, 'success' | 'warning' | 'danger' | 'neutral'> = {
  success: 'success',
  pending: 'warning',
  uncertain: 'warning',
  failed: 'danger',
}

const STATUS_TONE: Record<string, 'success' | 'warning' | 'danger' | 'neutral'> = {
  ok: 'success',
  warn: 'warning',
  bad: 'danger',
}

type KeyResult = { key: string; ok: boolean; message: string; tier?: string }

// OpenAI usage tier at or above which a key is not flagged before upload.
const MIN_TIER = 5

function maskKey(key: string): string {
  return key.length > 12 ? `${key.slice(0, 8)}…${key.slice(-4)}` : '••••'
}

export default function GynhUpload() {
  const [role, setRole] = useState<number | null>(getCachedRole())
  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])
  const isAdmin = role !== null && role >= ROLE_ADMIN

  const [options, setOptions] = useState<GynhOptions | null>(null)
  const [optionsError, setOptionsError] = useState('')
  const [accounts, setAccounts] = useState<GynhAccount[]>([])
  const [loadingAccounts, setLoadingAccounts] = useState(false)

  const loadOptions = useCallback(() => {
    setOptionsError('')
    api
      .gynhOptions()
      .then(setOptions)
      .catch((e) => setOptionsError(apiErrorMessage(e)))
  }, [])

  const loadAccounts = useCallback(async () => {
    setLoadingAccounts(true)
    try {
      const res = await api.gynhAccounts()
      setAccounts(res.accounts ?? [])
    } catch (e) {
      toast.error(apiErrorMessage(e))
    } finally {
      setLoadingAccounts(false)
    }
  }, [])

  useEffect(() => {
    loadOptions()
    void loadAccounts()
  }, [loadOptions, loadAccounts])

  return (
    <Layout title="供给台上号" subtitle="批量上传 Key 到供给台，一行一个">
      {isAdmin && (
        <SettingsCard
          onSaved={() => {
            loadOptions()
            void loadAccounts()
          }}
        />
      )}
      {optionsError ? (
        <Card className="mb-4 p-4 text-sm text-destructive">供给台连接失败：{optionsError}</Card>
      ) : (
        options && <UploadForm options={options} onUploaded={loadAccounts} />
      )}
      <AccountsCard accounts={accounts} loading={loadingAccounts} isAdmin={isAdmin} onRefresh={loadAccounts} />
    </Layout>
  )
}

function UploadForm(props: { options: GynhOptions; onUploaded: () => void }) {
  const { options } = props
  const providers = useMemo(
    () =>
      Object.keys(options.catalog)
        .filter((p) => (options.providers[p]?.shape ?? 'keyonly') === 'keyonly')
        .sort(),
    [options],
  )
  const [provider, setProvider] = useState(() => (providers.includes('openai') ? 'openai' : (providers[0] ?? '')))
  const [accountType, setAccountType] = useState(0)
  const [models, setModels] = useState<string[]>([])
  const [modelSearch, setModelSearch] = useState('')
  const [region, setRegion] = useState('')
  const [groupID, setGroupID] = useState('')
  const [url, setURL] = useState('')
  const [tpm, setTPM] = useState(options.default_tpm)
  const [rpm, setRPM] = useState(options.default_rpm)
  const [remark, setRemark] = useState('')
  const [keysText, setKeysText] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [progress, setProgress] = useState(0)
  const [checking, setChecking] = useState(false)
  const [results, setResults] = useState<KeyResult[]>([])

  const meta = options.providers[provider]
  const catalog = useMemo(() => options.catalog[provider] ?? [], [options, provider])
  const isQuick = accountType === 1
  const regions = options.account_regions ?? []

  // Provider change resets the provider-specific fields; auto_select_all
  // providers (openai / anthropic) start with every model ticked.
  useEffect(() => {
    setModels(options.providers[provider]?.auto_select_all ? catalog : [])
    setModelSearch('')
    setAccountType(0)
    setRegion(regions[0]?.value ?? '')
    setGroupID('')
    setURL('')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [provider])

  const keys = useMemo(() => {
    const seen = new Set<string>()
    return keysText
      .split(/\r?\n/)
      .map((k) => k.trim())
      .filter((k) => k !== '' && !seen.has(k) && seen.add(k))
  }, [keysText])

  const visibleModels = catalog.filter((m) => m.toLowerCase().includes(modelSearch.trim().toLowerCase()))
  const toggleModel = (m: string) =>
    setModels((cur) => (cur.includes(m) ? cur.filter((x) => x !== m) : [...cur, m]))

  async function submit() {
    if (!provider) return toast.error('请选择厂商')
    if (models.length === 0) return toast.error('请至少选择一个模型')
    if (keys.length === 0) return toast.error('请填写至少一个 Key')
    const minLen = meta?.key_min_len ?? 0
    const short = keys.find((k) => k.length < minLen)
    if (short) return toast.error(`Key 长度至少 ${minLen} 位：${maskKey(short)}`)
    if (!isQuick && (!/^\d+$/.test(tpm.trim()) || !/^\d+$/.test(rpm.trim()))) {
      return toast.error('TPM / RPM 须为非负整数')
    }

    setSubmitting(true)
    setProgress(0)
    setResults([])
    // OpenAI keys below Tier 5 hit rate limits under pool load, so they may get
    // a lower scheduling priority; warn before they go in.
    const tiers = new Map<string, string>()
    if (provider === 'openai') {
      setChecking(true)
      let warning = ''
      try {
        const { results: probed } = await api.gynhTierProbe(keys)
        const low: string[] = []
        keys.forEach((key, i) => {
          const r = probed[i]
          const label = r?.tier != null ? `Tier ${r.tier}` : `检测失败${r?.error ? `（${r.error}）` : ''}`
          tiers.set(key, label)
          if (r?.tier == null || r.tier < MIN_TIER) low.push(`${maskKey(key)}　${label}`)
        })
        if (low.length > 0) {
          const shown = low.slice(0, 20).join('\n')
          const more = low.length > 20 ? `\n… 另有 ${low.length - 20} 个` : ''
          warning = `${low.length} / ${keys.length} 个 Key 不是 Tier ${MIN_TIER} 及以上：\n\n${shown}${more}\n\n低 Tier 的号可能会被降低调度优先级，确定继续上传吗？`
        }
      } catch (e) {
        warning = `Tier 检测失败：${apiErrorMessage(e)}\n\n无法确认这些 Key 是否为 Tier ${MIN_TIER} 及以上，确定继续上传吗？`
      } finally {
        setChecking(false)
      }
      if (
        warning &&
        !(await confirmDialog({ title: 'Tier 检测提醒', message: warning, confirmText: '仍然上传', cancelText: '取消', danger: true }))
      ) {
        setSubmitting(false)
        return
      }
    }

    const done: KeyResult[] = []
    for (const key of keys) {
      try {
        const res = await api.gynhCreateAccount({
          provider,
          account_type: accountType,
          models,
          api_key: key,
          tpm: tpm.trim(),
          rpm: rpm.trim(),
          remark: remark.trim() || undefined,
          region: meta?.need_region ? region : undefined,
          group_id: provider === 'minimax' ? groupID.trim() || undefined : undefined,
          url: provider === 'ali' ? url.trim() || undefined : undefined,
        })
        done.push({ key, ok: true, message: `${res.msg || '提交成功'}，账号 ID ${res.id}`, tier: tiers.get(key) })
      } catch (e) {
        done.push({ key, ok: false, message: apiErrorMessage(e), tier: tiers.get(key) })
      }
      setProgress(done.length)
      setResults([...done])
    }
    setSubmitting(false)

    const failed = done.filter((r) => !r.ok)
    if (failed.length === 0) {
      toast.success(`${done.length} 个 Key 全部提交成功`)
      setKeysText('')
    } else {
      toast.error(`${failed.length} / ${done.length} 个 Key 提交失败`)
      // Keep only the failed keys so they can be fixed and resubmitted.
      setKeysText(failed.map((r) => r.key).join('\n'))
    }
    props.onUploaded()
  }

  return (
    <div className="mb-4 grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
      <Card className="space-y-4 p-4">
        {options.mode === 'mock' && (
          <div className="rounded-md bg-warning/10 px-3 py-2 text-xs text-warning">
            供给台当前为模拟环境，提交不会发送到真实平台。
          </div>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">厂商</span>
            <Select className="w-full" value={provider} onChange={(e) => setProvider(e.target.value)}>
              {providers.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </Select>
          </label>
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">账号类型</span>
            <Select className="w-full" value={accountType} onChange={(e) => setAccountType(Number(e.target.value))}>
              <option value={0}>普通号</option>
              <option value={1} disabled={provider !== 'anthropic'}>
                速刷号 · 仅 Anthropic
              </option>
            </Select>
          </label>
        </div>

        <div className="space-y-1.5">
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs font-medium text-muted-foreground">
              模型（已选 {models.length} / {catalog.length}）
            </span>
            <div className="flex items-center gap-1">
              <Button variant="ghost" size="sm" onClick={() => setModels(Array.from(new Set([...models, ...visibleModels])))}>
                全选
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setModels([])}>
                清空
              </Button>
            </div>
          </div>
          <Input value={modelSearch} placeholder="搜索模型…" onChange={(e) => setModelSearch(e.target.value)} />
          <div className="grid max-h-48 gap-1 overflow-y-auto rounded-lg border border-border p-2 sm:grid-cols-2">
            {visibleModels.map((m) => (
              <label key={m} className="flex min-w-0 items-center gap-2 rounded px-1 py-0.5 text-xs hover:bg-muted">
                <Input type="checkbox" checked={models.includes(m)} onChange={() => toggleModel(m)} />
                <span className="truncate font-mono" title={m}>
                  {m}
                </span>
              </label>
            ))}
            {visibleModels.length === 0 && <p className="p-2 text-xs text-muted-foreground">无匹配模型</p>}
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          {meta?.need_region && (
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">账号注册地</span>
              <Select className="w-full" value={region} onChange={(e) => setRegion(e.target.value)}>
                {regions.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </Select>
            </label>
          )}
          {provider === 'minimax' && (
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">Group ID（可选）</span>
              <Input value={groupID} onChange={(e) => setGroupID(e.target.value)} />
            </label>
          )}
          {provider === 'ali' && (
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">自定义 URL（可选）</span>
              <Input value={url} placeholder="https://…" onChange={(e) => setURL(e.target.value)} />
            </label>
          )}
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">TPM · 每分钟 Token 数{isQuick && '（可选）'}</span>
            <Input value={tpm} inputMode="numeric" onChange={(e) => setTPM(e.target.value)} />
          </label>
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">RPM · 每分钟请求数{isQuick && '（可选）'}</span>
            <Input value={rpm} inputMode="numeric" onChange={(e) => setRPM(e.target.value)} />
          </label>
          <label className="space-y-1.5 sm:col-span-2">
            <span className="text-xs font-medium text-muted-foreground">备注（可选，所有 Key 共用）</span>
            <Input value={remark} maxLength={500} onChange={(e) => setRemark(e.target.value)} />
          </label>
        </div>

        <label className="block space-y-1.5">
          <span className="flex items-center justify-between text-xs font-medium text-muted-foreground">
            <span>API Key（一行一个，自动去重）</span>
            <span className="tnum">{keys.length} 个</span>
          </span>
          <Textarea
            className="min-h-40 font-mono text-xs"
            value={keysText}
            disabled={submitting}
            spellCheck={false}
            autoComplete="off"
            placeholder="sk-..."
            onChange={(e) => setKeysText(e.target.value)}
          />
        </label>

        <div className="flex items-center justify-end gap-3">
          {submitting && !checking && (
            <span className="text-xs text-muted-foreground tnum">
              提交中 {progress} / {keys.length}
            </span>
          )}
          <Button onClick={submit} disabled={submitting}>
            {checking ? '检测 Tier…' : submitting ? '提交中…' : '批量上 Key'}
          </Button>
        </div>
      </Card>

      <Card className="space-y-3 p-4">
        <div className="text-sm font-semibold">提交结果</div>
        {results.length === 0 && <p className="text-xs text-muted-foreground">提交后这里逐个显示每个 Key 的结果。</p>}
        <ul className="max-h-[520px] space-y-1.5 overflow-auto text-xs">
          {results.map((r, i) => (
            <li key={i} className="rounded-md bg-muted px-2.5 py-1.5">
              <div className="flex items-center justify-between gap-2">
                <span className="font-mono">
                  {maskKey(r.key)}
                  {r.tier && <span className="ml-2 font-sans text-muted-foreground">{r.tier}</span>}
                </span>
                <Badge tone={r.ok ? 'success' : 'danger'}>{r.ok ? '成功' : '失败'}</Badge>
              </div>
              <div className={r.ok ? 'mt-0.5 text-muted-foreground' : 'mt-0.5 break-words text-destructive'}>
                {r.message}
              </div>
            </li>
          ))}
        </ul>
      </Card>
    </div>
  )
}

function AccountsCard(props: { accounts: GynhAccount[]; loading: boolean; isAdmin: boolean; onRefresh: () => void }) {
  const [keyword, setKeyword] = useState('')
  const term = keyword.trim().toLowerCase()
  const rows = props.accounts.filter((a) =>
    [a.upstream_id, a.alias, a.provider, a.model, a.remark, a.key_prefix, a.uploader]
      .join(' ')
      .toLowerCase()
      .includes(term),
  )

  return (
    <Card className="space-y-3 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm font-semibold">
          我的账号 <span className="text-muted-foreground tnum">/ {props.accounts.length}</span>
        </div>
        <div className="flex items-center gap-2">
          <Input
            className="w-56"
            value={keyword}
            placeholder="搜索 ID、模型、Key 前缀…"
            onChange={(e) => setKeyword(e.target.value)}
          />
          <Button variant="outline" size="sm" onClick={props.onRefresh} disabled={props.loading}>
            <RefreshCw className="size-4" />
            刷新
          </Button>
        </div>
      </div>
      <div className="overflow-x-auto rounded-lg border border-border">
        <table className="w-full text-left text-xs">
          <thead className="bg-muted/50 text-muted-foreground">
            <tr>
              <th className="px-3 py-2 font-medium">账号 / ID</th>
              <th className="px-3 py-2 font-medium">厂商与模型</th>
              <th className="px-3 py-2 font-medium">Key 前缀</th>
              {props.isAdmin && <th className="px-3 py-2 font-medium">上传人</th>}
              <th className="px-3 py-2 font-medium">接入状态</th>
              <th className="px-3 py-2 font-medium">平台状态</th>
              <th className="px-3 py-2 font-medium">提交时间</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => (
              <tr key={a.id} className="border-t border-border align-top">
                <td className="px-3 py-2">
                  <div className="font-medium">{a.alias || '等待接入'}</div>
                  <div className="font-mono text-muted-foreground">
                    {a.upstream_id ? `ID ${a.upstream_id}` : `本地记录 #${a.id}`}
                  </div>
                </td>
                <td className="max-w-72 px-3 py-2">
                  <div>
                    {a.provider} {a.account_type === 1 && <Badge>速刷号</Badge>}
                  </div>
                  <div className="truncate text-muted-foreground" title={a.model}>
                    {a.model}
                  </div>
                  {a.remark && <div className="truncate text-muted-foreground">{a.remark}</div>}
                </td>
                <td className="px-3 py-2 font-mono">{a.key_prefix ? `${a.key_prefix}…` : '—'}</td>
                {props.isAdmin && <td className="px-3 py-2">{a.uploader || '—'}</td>}
                <td className="px-3 py-2">
                  <Badge tone={STATE_TONE[a.state] ?? 'neutral'}>{STATE_LABEL[a.state] ?? a.state}</Badge>
                  {a.last_error && (
                    <div className="mt-1 max-w-60 break-words text-destructive" title={a.last_error}>
                      {a.last_error}
                    </div>
                  )}
                </td>
                <td className="px-3 py-2">
                  <Badge tone={STATUS_TONE[a.upstream_status?.tone ?? ''] ?? 'neutral'}>
                    {a.upstream_status?.label || '暂无数据'}
                  </Badge>
                </td>
                <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">
                  {a.created_at ? new Date(a.created_at).toLocaleString('zh-CN', { hour12: false }) : '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {rows.length === 0 && (
          <p className="p-6 text-center text-xs text-muted-foreground">{props.loading ? '加载中…' : '暂无账号记录'}</p>
        )}
      </div>
    </Card>
  )
}

// SettingsCard — admin-only shared portal login. Blank password leaves the
// stored one unchanged.
function SettingsCard(props: { onSaved: () => void }) {
  const [settings, setSettings] = useState<GynhSettings | null>(null)
  const [baseURL, setBaseURL] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api
      .getGynhSettings()
      .then((s) => {
        setSettings(s)
        setBaseURL(s.base_url)
        setUsername(s.username)
      })
      .catch((e) => toast.error(apiErrorMessage(e)))
  }, [])

  async function save() {
    setSaving(true)
    try {
      const s = await api.setGynhSettings({ base_url: baseURL, username, ...(password ? { password } : {}) })
      setSettings(s)
      setPassword('')
      toast.success('已保存')
      props.onSaved()
    } catch (e) {
      toast.error(apiErrorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card className="mb-4 space-y-3 p-4">
      <div className="flex items-center gap-2 text-sm font-semibold">
        供给台配置（管理员）
        {settings && <Badge tone={settings.configured ? 'success' : 'warning'}>{settings.configured ? '已配置' : '未配置'}</Badge>}
      </div>
      <div className="grid gap-4 sm:grid-cols-3">
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">供给台地址</span>
          <Input value={baseURL} placeholder="http://host:port" onChange={(e) => setBaseURL(e.target.value)} />
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">登录账号</span>
          <Input value={username} autoComplete="off" onChange={(e) => setUsername(e.target.value)} />
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">
            登录密码{settings?.password_set ? '（已设置）' : ''}
          </span>
          <Input
            type="password"
            value={password}
            autoComplete="new-password"
            placeholder={settings?.password_set ? '留空不修改' : ''}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
      </div>
      <div className="flex items-center justify-end">
        <Button onClick={save} disabled={saving || !settings}>
          {saving ? '保存中…' : '保存'}
        </Button>
      </div>
    </Card>
  )
}
