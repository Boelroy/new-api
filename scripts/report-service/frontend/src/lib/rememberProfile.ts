// rememberProfile 在 localStorage 里保留用户当前选中的 remote profile。
// 页面刷新或从其他页面回到列表页时能自动还原上次选择，避免每次都被
// 顶到第 1 个 profile。

const STORAGE_KEY = 'report:selectedProfileID'

export function readRememberedProfileID(): number | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const n = parseInt(raw, 10)
    return Number.isFinite(n) && n > 0 ? n : null
  } catch {
    return null
  }
}

export function writeRememberedProfileID(id: number | null): void {
  try {
    if (id == null) {
      localStorage.removeItem(STORAGE_KEY)
    } else {
      localStorage.setItem(STORAGE_KEY, String(id))
    }
  } catch {
    // ignore quota / privacy-mode errors — persistence is best-effort.
  }
}
