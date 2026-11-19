// The supplier upload form runs in the legacy app. Exercise it with the Report
// Service Vitest/JSDOM harness shared by the frontend tests.
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import SupplierAccounts from '../../frontend/src/pages/SupplierAccounts'

const portalProxy = {
  proxy_type: 'bind_custom',
  proxy_region: '日本',
  proxy_url: 'socks5://fixture-user:fixture%40pass@proxy.invalid:1080',
}
const keyOne = 'synthetic-key-first'
const keyTwo = 'synthetic-key-second'
const uploads: Record<string, unknown>[] = []
let rejectKey = ''
const keyField = () => screen.getByPlaceholderText(/sk-xxxx/) as HTMLTextAreaElement
const proxyField = () => screen.getByLabelText('代理地址') as HTMLInputElement
const regionField = () => screen.getByLabelText('代理出口地区') as HTMLInputElement

function change(field: HTMLElement, value: string) {
  fireEvent.change(field, { target: { value } })
}

async function submit() {
  fireEvent.click(screen.getByRole('button', { name: '提交账号' }))
  await screen.findByRole('button', { name: '提交账号' })
}

beforeEach(async () => {
  uploads.length = 0
  rejectKey = ''
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, init) => {
    switch (String(url)) {
      case '/api/auth/me': return Response.json({ role: 4 })
      case '/api/auth/config': return Response.json({ supplier_account_openapi_ready: true })
      case '/api/supplier-account/provider-defaults':
        return Response.json({ defaults: { OpenAI: { models: ['fixture-model'], account_type: 0 } } })
      case '/api/supplier-account/providers':
        return Response.json({ list: [{ name: 'OpenAI', shape: 'keyonly', key_min_len: 8 }] })
      case '/api/supplier-account/models':
        return Response.json({ list: [{ value: 'fixture-model', provider: 'OpenAI' }] })
      case '/api/supplier-account/accounts':
        if (init?.method === 'POST') {
          const payload = JSON.parse(String(init.body))
          uploads.push(payload)
          if (payload.api_key === rejectKey) return Response.json({ error: '模拟门户拒绝' }, { status: 400 })
          return Response.json({ id: uploads.length, alias: 'fixture', msg: '已提交' })
        }
        return Response.json({ accounts: [] })
      case '/api/supplier-account/metrics': return Response.json({ accounts: [] })
      default: throw new Error('Unexpected local fixture request')
    }
  })
  render(<MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
    <SupplierAccounts />
  </MemoryRouter>)
  change((await screen.findByRole('option', { name: 'OpenAI' })).parentElement!, 'OpenAI')
  await waitFor(() => expect(screen.getByRole('checkbox', { name: 'fixture-model' })).toBeChecked())
})

afterEach(() => { vi.restoreAllMocks() })

test('single account omits all proxy fields when both inputs are blank', async () => {
  change(keyField(), keyOne)
  change(proxyField(), '   ')
  change(regionField(), '   ')
  await submit()
  expect(uploads).toEqual([{
    provider: 'OpenAI', model: 'fixture-model', api_key: keyOne, account_type: 0,
    tpm: '4000000000', rpm: '10000',
  }])
})

test('malformed proxy and incomplete pairs block the entire batch without exposing credentials', async () => {
  change(keyField(), `${keyOne}\n${keyTwo}`)
  expect(proxyField().type).toBe('password')
  for (const [url, region, message] of [
    ['fixture-user:fixture-password@proxy.invalid', '美国', '代理地址格式不正确'],
    [portalProxy.proxy_url, '', '请填写代理的实际出口地区'],
    ['', '美国', '请填写代理地址'],
  ]) {
    change(proxyField(), url)
    change(regionField(), region)
    await submit()
    expect(uploads).toHaveLength(0)
    expect(keyField().value).toBe(`${keyOne}\n${keyTwo}`)
    expect(document.body.textContent).toContain(message)
    expect(document.body.textContent).not.toContain('fixture-user')
  }
})

test('each distinct key gets the same proxy, including a single-key retry after partial failure', async () => {
  rejectKey = keyTwo
  change(keyField(), `${keyOne}\n ${keyTwo} \n${keyOne}\n`)
  change(proxyField(), portalProxy.proxy_url)
  change(regionField(), ` ${portalProxy.proxy_region} `)
  await submit()
  expect(uploads).toHaveLength(2)
  for (const [index, key] of [keyOne, keyTwo].entries()) {
    expect(uploads[index]).toMatchObject({ api_key: key, ...portalProxy })
  }
  expect(keyField().value).toBe(keyTwo)
  expect(proxyField().value).toBe(portalProxy.proxy_url)
  expect(document.body.textContent).toContain('模拟门户拒绝')
  expect(document.body.textContent).not.toContain('fixture-user')

  rejectKey = ''
  await submit()
  expect(uploads).toHaveLength(3)
  expect(uploads[2]).toEqual(uploads[1])
  expect(proxyField().value).toBe('')
  expect(regionField().value).toBe('')
  expect(keyField().value).toBe('')
})
