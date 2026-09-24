import { useEffect, useMemo, useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import Layout from '../components/Layout'
import { Button, Card, Input, Select, Textarea } from '../components/ui'
import { toast } from '../components/feedback'
import { api, ROLE_SUPPLIER_02, type KeyhubCategory, type KeyhubCategoryField } from '../api'
import { getCachedRole, loadRole } from '../auth'

// 上传 Key — imports credentials into KHub (pd-maas) via the report-service
// proxy. The category list (and each category's import profile) comes from
// pd-maas; multi-field categories (serialize 'pipe') join their fields with
// '|', one credential per line. Two entry modes are offered — a structured
// per-field table (default) and a raw-text area for bulk paste — and they
// round-trip when you toggle between them.

// Categories whose models field should be pre-populated with our standard
// OpenAI-compatible model set. Applied whenever the category is selected.
const DEFAULT_MODELS: Record<string, string> = {
  openai: 'gpt-5.6-sol,gpt-5.6-terra,gpt-5.6-luna,gpt-6-astra,gpt-6-sol,gpt-6-luna',
  azure_openai: 'gpt-5.6-sol,gpt-5.6-terra,gpt-5.6-luna,gpt-6-astra,gpt-6-sol,gpt-6-luna',
}

function profileOf(cat: KeyhubCategory | undefined) {
  return cat?.import_profile_json
}
function fieldsOf(cat: KeyhubCategory | undefined): KeyhubCategoryField[] {
  const f = profileOf(cat)?.fields
  return Array.isArray(f) && f.length > 0 ? f : []
}

// One-line format hint. Prefer the upstream formatHint (carries per-category
// quirks like "（ApiVersion 可选）"); fall back to joining field names.
function fieldHint(cat: KeyhubCategory | undefined): string {
  const prof = profileOf(cat)
  if (prof?.formatHint) return prof.formatHint
  const fields = fieldsOf(cat)
  if (fields.length === 0) return ''
  if (prof?.serialize === 'pipe') return fields.map((f) => f.name).join(' | ')
  return fields[0]?.name ?? ''
}

// Textarea placeholder for the selected category. Prefer upstream exampleLines
// (e.g. azure_openai → "https://example.openai.azure.com|api-key|2024-12-01-preview");
// fall back to joining each field's own placeholder, then a generic default.
function rawTextPlaceholder(cat: KeyhubCategory | undefined): string {
  const prof = profileOf(cat)
  if (prof?.exampleLines && prof.exampleLines.length > 0) return prof.exampleLines.join('\n')
  const fields = fieldsOf(cat)
  if (fields.length > 0) {
    const parts = fields.map((f) => f.placeholder || f.name)
    return prof?.serialize === 'pipe' ? parts.join('|') : parts[0]
  }
  return 'sk-...'
}

// Serialize table rows → raw_text (one credential per line). For 'pipe'
// categories fields join with '|', dropping trailing empties so optional
// tail columns (e.g. Azure ApiVersion) can be omitted. Fully-empty rows drop.
function serializeRows(rows: string[][], serialize: 'single' | 'pipe'): string {
  return rows
    .map((r) => r.map((v) => v.trim()))
    .filter((r) => r.some((v) => v !== ''))
    .map((r) => {
      if (serialize !== 'pipe') return r[0] ?? ''
      let end = r.length
      while (end > 1 && r[end - 1] === '') end--
      return r.slice(0, end).join('|')
    })
    .join('\n')
}

// Parse raw_text back into table rows, padded/truncated to the field count.
function parseTextToRows(text: string, fieldCount: number, serialize: 'single' | 'pipe'): string[][] {
  const lines = text.split('\n').map((l) => l.trim()).filter(Boolean)
  return lines.map((line) => {
    if (serialize !== 'pipe') return [line]
    const parts = line.split('|').map((p) => p.trim())
    return Array.from({ length: Math.max(1, fieldCount) }, (_, i) => parts[i] ?? '')
  })
}

function emptyRow(fieldCount: number): string[] {
  return Array.from({ length: Math.max(1, fieldCount) }, () => '')
}

export default function KeyhubUpload() {
  const [categories, setCategories] = useState<KeyhubCategory[]>([])
  const [loadingCats, setLoadingCats] = useState(true)
  const [categoryCode, setCategoryCode] = useState('')
  const [mode, setMode] = useState<'table' | 'text'>('table')
  const [rows, setRows] = useState<string[][]>([['']])
  const [rawText, setRawText] = useState('')
  const [models, setModels] = useState('')
  const [groupName, setGroupName] = useState('default')
  const [tag, setTag] = useState('')
  const [note, setNote] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState<unknown>(null)
  const [role, setRole] = useState<number | null>(getCachedRole())

  // Suppliers are pinned to a server-assigned private group ("sup_<id>"), so
  // the group field is locked for them — the server overrides it regardless.
  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])
  const isSupplier = role === ROLE_SUPPLIER_02

  useEffect(() => {
    let alive = true
    api
      .keyhubCategories()
      .then((res) => {
        if (!alive) return
        const list = Array.isArray(res?.data) ? res.data : []
        setCategories(list)
        if (list.length > 0) setCategoryCode((prev) => prev || list[0].code)
      })
      .catch((e) => toast.error(e))
      .finally(() => alive && setLoadingCats(false))
    return () => {
      alive = false
    }
  }, [])

  const currentCat = useMemo(
    () => categories.find((c) => c.code === categoryCode),
    [categories, categoryCode],
  )
  const fields = useMemo(() => fieldsOf(currentCat), [currentCat])
  const serialize = profileOf(currentCat)?.serialize === 'pipe' ? 'pipe' : 'single'
  const canTable = fields.length > 0
  const effectiveMode: 'table' | 'text' = canTable ? mode : 'text'
  const hint = fieldHint(currentCat)

  // On category change: reset the per-field rows to a single empty row of the
  // new width, and apply that category's default models (empty for others).
  useEffect(() => {
    setRows([emptyRow(fields.length)])
    setModels(DEFAULT_MODELS[categoryCode] ?? '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [categoryCode])

  const filledRows = useMemo(
    () => rows.filter((r) => r.some((v) => v.trim() !== '')).length,
    [rows],
  )
  const textLines = useMemo(
    () => rawText.split('\n').map((l) => l.trim()).filter(Boolean).length,
    [rawText],
  )

  const updateCell = (ri: number, fi: number, val: string) =>
    setRows((rs) => rs.map((r, i) => (i === ri ? r.map((c, ci) => (ci === fi ? val : c)) : r)))
  const addRow = () => setRows((rs) => [...rs, emptyRow(fields.length)])
  const removeRow = (ri: number) =>
    setRows((rs) => {
      const next = rs.filter((_, i) => i !== ri)
      return next.length > 0 ? next : [emptyRow(fields.length)]
    })

  // Toggle modes, carrying the entered credentials across so nothing is lost.
  const goTable = () => {
    const parsed = parseTextToRows(rawText, fields.length, serialize)
    if (parsed.length > 0) setRows(parsed)
    setMode('table')
  }
  const goText = () => {
    setRawText(serializeRows(rows, serialize))
    setMode('text')
  }

  const submit = async () => {
    const code = categoryCode.trim()
    if (!code) {
      toast.error('请选择类别')
      return
    }
    const text = effectiveMode === 'table' ? serializeRows(rows, serialize) : rawText.trim()
    if (!text) {
      toast.error('请填写至少一条 Key')
      return
    }
    setSubmitting(true)
    setResult(null)
    try {
      const res = await api.keyhubImport({
        category_code: code,
        raw_text: text,
        endpoint_url: '',
        models: models.trim() ? models.split(',').map((m) => m.trim()).filter(Boolean) : undefined,
        group_name: groupName.trim() || 'default',
        tag: tag.trim() || undefined,
        note: note.trim() || undefined,
      })
      setResult(res)
      if (res?.code === 'ok') {
        toast.success('导入成功')
        setRows([emptyRow(fields.length)])
        setRawText('')
      } else {
        toast.error(res?.message || '导入失败')
      }
    } catch (e) {
      toast.error(e)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Layout title="上传 Key" subtitle="导入 Key 到 KHub（pd-maas）">
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
        <Card className="space-y-4 p-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">类别 (category)</span>
              <Select
                className="w-full"
                value={categoryCode}
                disabled={loadingCats}
                onChange={(e) => setCategoryCode(e.target.value)}
              >
                {loadingCats && <option>加载中…</option>}
                {!loadingCats && categories.length === 0 && <option value="">无可用类别</option>}
                {categories.map((c) => (
                  <option key={c.code} value={c.code}>
                    {c.label || c.code} ({c.code})
                  </option>
                ))}
              </Select>
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">分组 (group_name)</span>
              {isSupplier ? (
                <>
                  <Input value="按供应商自动隔离" disabled readOnly />
                  <span className="block text-[11px] text-muted-foreground">
                    已按你的账号自动分配隔离分组，无需填写。
                  </span>
                </>
              ) : (
                <Input value={groupName} onChange={(e) => setGroupName(e.target.value)} placeholder="default" />
              )}
            </label>
          </div>

          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs font-medium text-muted-foreground">Key 内容 (raw_text)</span>
              <div className="flex items-center gap-2">
                <span className="text-[11px] text-muted-foreground tnum">
                  {effectiveMode === 'table' ? `${filledRows} 行` : `${textLines} 行`}
                </span>
                {canTable && (
                  <div className="inline-flex rounded-lg border border-border p-0.5 text-[11px]">
                    <button
                      type="button"
                      onClick={goTable}
                      className={
                        'rounded-md px-2 py-0.5 transition-colors ' +
                        (effectiveMode === 'table'
                          ? 'bg-primary text-primary-foreground'
                          : 'text-muted-foreground hover:text-foreground')
                      }
                    >
                      表格
                    </button>
                    <button
                      type="button"
                      onClick={goText}
                      className={
                        'rounded-md px-2 py-0.5 transition-colors ' +
                        (effectiveMode === 'text'
                          ? 'bg-primary text-primary-foreground'
                          : 'text-muted-foreground hover:text-foreground')
                      }
                    >
                      文本
                    </button>
                  </div>
                )}
              </div>
            </div>

            {hint && (
              <div className="rounded-md bg-muted px-2.5 py-1.5 font-mono text-[11px] text-muted-foreground">
                {hint}
              </div>
            )}

            {effectiveMode === 'table' ? (
              <div className="space-y-2">
                <div className="overflow-x-auto rounded-lg border border-border">
                  <table className="w-full text-left text-xs">
                    <thead className="bg-muted/50 text-muted-foreground">
                      <tr>
                        {fields.map((f) => (
                          <th key={f.name} className="whitespace-nowrap px-3 py-2 font-medium">
                            {f.name}
                            {f.required && <span className="text-destructive"> *</span>}
                          </th>
                        ))}
                        <th className="w-12 px-3 py-2 text-right font-medium">操作</th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((row, ri) => (
                        <tr key={ri} className="border-t border-border">
                          {fields.map((f, fi) => (
                            <td key={fi} className="px-2 py-1.5 align-top">
                              <Input
                                value={row[fi] ?? ''}
                                placeholder={f.placeholder || f.name}
                                onChange={(e) => updateCell(ri, fi, e.target.value)}
                                className="font-mono text-xs"
                                spellCheck={false}
                              />
                            </td>
                          ))}
                          <td className="px-2 py-1.5 text-right align-top">
                            <Button
                              variant="ghost"
                              size="sm"
                              className="px-1.5"
                              title="删除该行"
                              onClick={() => removeRow(ri)}
                            >
                              <Trash2 className="size-4" />
                            </Button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                <div className="flex items-center justify-end">
                  <Button variant="outline" size="sm" onClick={addRow}>
                    <Plus className="size-4" />
                    添加一行
                  </Button>
                </div>
              </div>
            ) : (
              <Textarea
                className="min-h-52 font-mono text-xs"
                value={rawText}
                onChange={(e) => setRawText(e.target.value)}
                placeholder={rawTextPlaceholder(currentCat)}
                spellCheck={false}
              />
            )}
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">模型 (models，逗号分隔，可留空)</span>
              <Input value={models} onChange={(e) => setModels(e.target.value)} placeholder="留空使用类别默认" />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">标签 (tag，可选)</span>
              <Input value={tag} onChange={(e) => setTag(e.target.value)} />
            </label>
          </div>

          <label className="block space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">备注 (note，可选)</span>
            <Input value={note} onChange={(e) => setNote(e.target.value)} />
          </label>

          <div className="flex items-center justify-end">
            <Button onClick={submit} disabled={submitting || loadingCats}>
              {submitting ? '导入中…' : '导入 Key'}
            </Button>
          </div>
        </Card>

        <Card className="space-y-3 p-4">
          <div className="text-sm font-semibold">导入结果</div>
          {!result && <p className="text-xs text-muted-foreground">提交后这里显示 KHub 返回的结果。</p>}
          {result != null && (
            <pre className="max-h-[520px] overflow-auto rounded-md bg-muted p-3 text-[11px] leading-relaxed">
              {JSON.stringify(result, null, 2)}
            </pre>
          )}
        </Card>
      </div>
    </Layout>
  )
}
