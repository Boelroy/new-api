import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { chromium, type Browser, type Page } from 'playwright'
import { createServer, type ViteDevServer } from 'vite'

let browser: Browser
let server: ViteDevServer
let origin: string
beforeAll(async () => {
  server = await createServer({ server: { host: '127.0.0.1', port: 0 } })
  await server.listen()
  origin = server.resolvedUrls!.local[0]
  browser = await chromium.launch({ headless: true })
})
afterAll(async () => {
  await browser?.close()
  await server?.close()
})

async function openPage(role: number, mobile = false): Promise<Page> {
  const page = await browser.newPage({
    viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 1000 },
  })
  await page.addInitScript(() => localStorage.setItem('i18nextLng', 'zh'))
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    // All API traffic is isolated from the user's configured remote servers.
    let data: unknown = {
      items: [],
      channels: [],
      buckets: [],
      total_errors: 0,
      sample_size: 0,
      data: {},
      latest: {},
      rpm: 0,
      tpm: 0,
      quota_last_hour: 0,
      total: 0,
      enabled: false,
      interval_sec: 30,
    }
    if (path === '/api/auth/me') data = { role, studio: 'demo', username: 'Preview' }
    if (path === '/api/remote-newapi/profiles')
      data = {
        profiles: [
          {
            id: 1,
            name: 'Demo New API',
            default_group: 'default',
            default_models: 'claude-sonnet-4-6',
          },
        ],
      }
    if (path === '/api/remote-newapi/channels/cached') data = { channels: [], total: 0, cached_at: 0, cached: true }
    await route.fulfill({ json: data })
  })
  await page.goto(origin + 'remote-channels')
  return page
}

describe('V3 channel workflows in Chromium', () => {
  it('preserves studio batch inputs on a failed request and submits the same quota and note on retry', async () => {
    const page = await openPage(3)
    try {
      await page.getByRole('button', { name: '批量添加（每行一个密钥）' }).click()
      const dialog = page.getByRole('dialog', { name: '批量上 Key', exact: true })
      const editor = dialog.getByRole('textbox', { name: 'API 密钥' })
      await editor.fill('test-key-one 12.5 first\ntest-key-two note without quota')
      await dialog.getByRole('button', { name: '表格', exact: true }).click()
      expect(await dialog.getByRole('textbox', { name: '备注 2' }).inputValue()).toBe('note without quota')
      await dialog.getByRole('button', { name: '文本', exact: true }).click()
      const submissions: unknown[] = []
      await page.route('**/api/remote-newapi/pending', async route => {
        submissions.push(route.request().postDataJSON())
        if (submissions.length === 1) await route.fulfill({ status: 503, body: 'Temporarily unavailable' })
        else await route.fulfill({ json: { inserted: 2, skipped: 0, total: 2 } })
      })
      await dialog.getByRole('button', { name: '上传', exact: true }).click()
      await dialog.getByRole('alert').waitFor()
      expect(await editor.inputValue()).toContain('test-key-two note without quota')
      await dialog.getByRole('button', { name: '上传', exact: true }).click()
      await dialog.waitFor({ state: 'detached' })
      expect(submissions).toHaveLength(2)
      expect(submissions[1]).toEqual(submissions[0])
      expect(submissions[0]).toMatchObject({
        profile_id: 1,
        type: 14,
        pool_size: 1,
        items: [
          { key: 'test-key-one', quota_usd: 12.5, note: 'first' },
          { key: 'test-key-two', note: 'note without quota' },
        ],
      })
    } finally {
      await page.close()
    }
  })

  it('supports keyboard provider selection and keeps mobile footer actions visible in both themes', async () => {
    const page = await openPage(3, true)
    try {
      await page.getByRole('button', { name: '上普通 Key', exact: true }).click()
      let dialog = page.getByRole('dialog')
      const provider = dialog.getByRole('combobox', { name: '渠道类型' })
      await provider.focus()
      await page.keyboard.press('Enter')
      await page.keyboard.press('ArrowDown')
      await page.keyboard.press('Enter')
      expect(await provider.innerText()).toContain('OpenAI')
      await dialog.getByRole('textbox', { name: 'API 密钥' }).fill('test-key-one 10')
      await dialog.getByRole('button', { name: '预览', exact: true }).click()
      expect(await dialog.getByRole('table').innerText()).not.toContain('test-key-one')
      const submit = dialog.getByRole('button', { name: '立即上传', exact: true })
      const box = await submit.boundingBox()
      expect(box!.y + box!.height).toBeLessThanOrEqual(844)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
      await page.screenshot({ path: '/private/tmp/report-v3-mobile-light.png' })
      await dialog.getByRole('button', { name: '关闭对话框' }).click()
      await page.getByRole('button', { name: '切换到深色' }).click()
      await page.getByRole('button', { name: '上普通 Key', exact: true }).click()
      dialog = page.getByRole('dialog')
      expect(await page.locator('html').getAttribute('class')).toContain('dark')
      await dialog.getByRole('button', { name: '表格', exact: true }).click()
      await page.screenshot({ path: '/private/tmp/report-v3-mobile-dark.png' })
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    } finally {
      await page.close()
    }
  })

  it('opens the admin batch form, hides queue options for Azure and retains advanced model defaults', async () => {
    const page = await openPage(100)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    try {
      await page.getByRole('button', { name: '+ 批量上 key', exact: true }).click()
      const dialog = page.getByRole('dialog')
      await dialog.getByRole('combobox', { name: '渠道类型' }).click()
      await page.getByRole('option', { name: 'Azure', exact: true }).click()
      expect(await dialog.getByText('使用队列（定时上传 / drip 池）', { exact: true }).count()).toBe(0)
      await dialog.getByText('高级设置', { exact: true }).click()
      expect(await dialog.locator('textarea').last().inputValue()).toBe('claude-sonnet-4-6')
      await page.screenshot({ path: '/private/tmp/report-v3-admin.png' })
      await page.keyboard.press('Escape')
      await dialog.waitFor({ state: 'detached' })
      expect(errors).toEqual([])
    } finally {
      await page.close()
    }
  })
})
