/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import {
  fireEvent,
  render,
  screen,
  waitFor,
  type RenderResult,
} from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type { Redemption } from '../../types'

const i18n = (await import('i18next')).default
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { Toaster, toast } = await import('sonner')
const { api } = await import('@/lib/api')
const { useSystemConfigStore } = await import('@/stores/system-config-store')
const { RedemptionsProvider } = await import('../redemptions-provider')
const { RedemptionsMutateDrawer } = await import('../redemptions-mutate-drawer')

await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: {
    en: {
      translation: {
        'Failed to load': 'Failed to load',
        'Loading...': 'Loading...',
        'Save changes': 'Save changes',
        'Something went wrong!': 'Something went wrong!',
      },
    },
  },
})

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = {
  get: ApiMethod
  put: ApiMethod
}
type RenderedDrawer = {
  result: RenderResult
}
type CurrencyFixture = {
  quotaDisplayType: 'USD' | 'CNY'
  usdExchangeRate: number
}

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPut = apiClient.put
const originalConsoleLog = Reflect.get(console, 'log')
let renderedDrawer: RenderedDrawer | null = null

function redemption(id: number, quota = 500001): Redemption {
  return {
    id,
    user_id: 1,
    name: `code-${id}`,
    key: `key-${id}`,
    status: 1,
    quota,
    created_time: 1,
    redeemed_time: 0,
    expired_time: 0,
    used_user_id: 0,
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, reject, resolve }
}

function drawerTree(currentRow: Redemption) {
  return (
    <I18nextProvider i18n={i18n}>
      <RedemptionsProvider>
        <RedemptionsMutateDrawer
          open
          currentRow={currentRow}
          onOpenChange={() => undefined}
        />
      </RedemptionsProvider>
      <Toaster duration={60_000} />
    </I18nextProvider>
  )
}

async function renderDrawer(
  currentRow: Redemption,
  currency: CurrencyFixture = {
    quotaDisplayType: 'USD',
    usdExchangeRate: 1,
  }
): Promise<void> {
  useSystemConfigStore.getState().setConfig({
    currency: {
      displayInCurrency: true,
      quotaDisplayType: currency.quotaDisplayType,
      quotaPerUnit: 500000,
      usdExchangeRate: currency.usdExchangeRate,
      customCurrencySymbol: '¤',
      customCurrencyExchangeRate: 1,
    },
  })

  renderedDrawer = { result: render(drawerTree(currentRow)) }
}

async function rerenderDrawer(currentRow: Redemption): Promise<void> {
  if (!renderedDrawer) {
    throw new Error('Expected a rendered redemption drawer')
  }
  renderedDrawer.result.rerender(drawerTree(currentRow))
}

function getSaveButton(): HTMLButtonElement {
  return screen.getByRole('button', { name: 'Save changes' })
}

function getControlByLabel(labelText: 'Name'): HTMLInputElement
function getControlByLabel(labelText: 'Quota (CNY)'): HTMLInputElement
function getControlByLabel(labelText: 'Quota (USD)'): HTMLInputElement
function getControlByLabel(labelText: string): HTMLElement {
  const label = [...document.querySelectorAll<HTMLLabelElement>('label')].find(
    (candidate) => candidate.textContent?.trim() === labelText
  )
  if (!label) {
    throw new Error(`Expected label "${labelText}"`)
  }
  const control =
    label.control ??
    label
      .closest('[data-slot="form-item"]')
      ?.querySelector<HTMLElement>('[data-slot="form-control"], input')
  if (!control) {
    throw new Error(`Expected control for label "${labelText}"`)
  }
  return control
}

function changeInput(input: HTMLInputElement, value: string): void {
  fireEvent.input(input, { target: { value } })
}

function submitForm(): void {
  const form = document.querySelector<HTMLFormElement>('#redemption-form')
  if (!form) {
    throw new Error('Expected redemption form')
  }
  fireEvent.submit(form)
}

async function waitForLoadedForm(): Promise<void> {
  await waitFor(() => expect(getSaveButton()).toBeEnabled())
}

afterEach(() => {
  apiClient.get = originalGet
  apiClient.put = originalPut
  Reflect.set(console, 'log', originalConsoleLog)
  toast.dismiss()
  localStorage.clear()
  renderedDrawer = null
})

describe('redemption drawer', () => {
  test('shows the reported CNY quota without floating-point noise', async () => {
    const original = redemption(1, 13888889)
    apiClient.get = async () => ({ data: { success: true, data: original } })

    await renderDrawer(original, {
      quotaDisplayType: 'CNY',
      usdExchangeRate: 7.2,
    })
    await waitForLoadedForm()

    expect(getControlByLabel('Quota (CNY)').value).toBe('200')
  })

  test('blocks updates and reports an error when loading rejects', async () => {
    const updates: unknown[] = []
    Reflect.set(console, 'log', () => undefined)
    apiClient.get = async () => {
      throw new Error('network failure')
    }
    apiClient.put = async (_url, data) => {
      updates.push(data)
      return { data: { success: true } }
    }

    await renderDrawer(redemption(1))
    await waitFor(() =>
      expect(document.body).toHaveTextContent('network failure')
    )

    expect(getSaveButton()).toBeDisabled()
    submitForm()
    expect(updates).toEqual([])
  })

  test.each([
    {
      message: 'Redemption code is no longer available',
      expected: 'Redemption code is no longer available',
    },
    { message: undefined, expected: 'Failed to load' },
  ])(
    'blocks updates and reports the server reason or localized fallback: $expected',
    async ({ message, expected }) => {
      apiClient.get = async () => ({
        data: { success: false, message },
      })
      await renderDrawer(redemption(1))
      await waitFor(() => expect(document.body).toHaveTextContent(expected))
      expect(getSaveButton()).toBeDisabled()
    }
  )

  test('keeps the original quota when another field changes', async () => {
    const original = redemption(1)
    const updates: Array<Record<string, unknown>> = []
    apiClient.get = async () => ({ data: { success: true, data: original } })
    apiClient.put = async (_url, data) => {
      expect(data && typeof data === 'object').toBeTruthy()
      updates.push(data as Record<string, unknown>)
      return { data: { success: true, data: original } }
    }

    await renderDrawer(original)
    await waitForLoadedForm()
    expect(getControlByLabel('Quota (USD)').value).toBe('1')

    changeInput(getControlByLabel('Name'), 'renamed')
    submitForm()
    await waitFor(() => expect(updates).toHaveLength(1))

    expect(updates[0]?.name).toBe('renamed')
    expect(updates[0]?.quota).toBe(500001)
  })

  test('recalculates quota when the quota field changes', async () => {
    const original = redemption(1)
    const updates: Array<Record<string, unknown>> = []
    apiClient.get = async () => ({ data: { success: true, data: original } })
    apiClient.put = async (_url, data) => {
      expect(data && typeof data === 'object').toBeTruthy()
      updates.push(data as Record<string, unknown>)
      return { data: { success: true, data: original } }
    }

    await renderDrawer(original)
    await waitForLoadedForm()
    changeInput(getControlByLabel('Quota (USD)'), '2')
    submitForm()
    await waitFor(() => expect(updates).toHaveLength(1))

    expect(updates[0]?.quota).toBe(1000000)
  })

  test('ignores an older response after switching records', async () => {
    const first = redemption(1, 500001)
    const second = redemption(2, 1000001)
    const firstRequest = deferred<{ data: unknown }>()
    const secondRequest = deferred<{ data: unknown }>()
    const requestedUrls: string[] = []
    const updates: Array<Record<string, unknown>> = []
    apiClient.get = (url) => {
      requestedUrls.push(url)
      if (url === '/api/redemption/1') return firstRequest.promise
      if (url === '/api/redemption/2') return secondRequest.promise
      throw new Error(`Unexpected GET ${url}`)
    }
    apiClient.put = async (_url, data) => {
      expect(data && typeof data === 'object').toBeTruthy()
      updates.push(data as Record<string, unknown>)
      return { data: { success: true, data: second } }
    }

    await renderDrawer(first)
    await rerenderDrawer(second)
    await waitFor(() => expect(requestedUrls).toContain('/api/redemption/2'))
    secondRequest.resolve({ data: { success: true, data: second } })
    await waitForLoadedForm()

    firstRequest.resolve({ data: { success: true, data: first } })
    expect(getControlByLabel('Name').value).toBe('code-2')

    changeInput(getControlByLabel('Name'), 'second')
    submitForm()
    await waitFor(() => expect(updates).toHaveLength(1))

    expect(updates[0]?.id).toBe(2)
    expect(updates[0]?.quota).toBe(1000001)
  })
})
