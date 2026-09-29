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
import { afterEach, expect, it, vi } from 'vitest'

import { AuthOperationError } from '@/lib/secure-verification'

import { OAUTH_POPUP_CALLBACK_MESSAGE } from '../../constants'
import { openOAuthPopup } from '../oauth-popup'

function popupWindow() {
  const storage = new Map<string, string>()
  const popup = {
    closed: false,
    location: { replace: vi.fn() },
    sessionStorage: {
      setItem: (key: string, value: string) => storage.set(key, value),
      getItem: (key: string) => storage.get(key) ?? null,
    },
    close: vi.fn(() => {
      popup.closed = true
    }),
    postMessage: vi.fn(),
  }
  vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window)
  return popup
}

function callbackMessage(
  popup: unknown,
  overrides: Record<string, unknown> = {},
  origin = window.location.origin
) {
  const event = new MessageEvent('message', {
    origin,
    data: {
      type: OAUTH_POPUP_CALLBACK_MESSAGE,
      intent: 'verify',
      provider: 'github',
      state: 'state',
      code: 'code',
      ...overrides,
    },
  })
  Object.defineProperty(event, 'source', { value: popup })
  window.dispatchEvent(event)
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('accepts only the matching popup, origin, intent, provider and state', async () => {
  const popup = popupWindow()
  const controller = new AbortController()
  const prepared = Promise.resolve({
    state: 'state',
    url: 'https://example.com/authorize',
  })
  const resolved = vi.fn()
  const result = openOAuthPopup({
    provider: 'github',
    intent: 'verify',
    signal: controller.signal,
    prepare: () => prepared,
  }).then((exchange) => {
    resolved(exchange)
    return exchange
  })
  await prepared
  expect(popup.location.replace).toHaveBeenCalledWith(
    'https://example.com/authorize'
  )
  callbackMessage({}, {})
  callbackMessage(popup, {}, 'https://untrusted.example')
  callbackMessage(popup, { intent: 'bind' })
  callbackMessage(popup, { provider: 'discord' })
  callbackMessage(popup, { state: 'old-state' })
  await Promise.resolve()
  expect(resolved).not.toHaveBeenCalled()
  callbackMessage(popup)
  callbackMessage(popup)
  const exchange = await result
  expect(resolved).toHaveBeenCalledTimes(1)
  expect(exchange.callback).toEqual({
    provider: 'github',
    state: 'state',
    code: 'code',
    error: undefined,
    errorDescription: undefined,
  })
  exchange.finish({ success: true })
  expect(popup.closed).toBe(true)
})

it('closes an aborted popup and ignores a late authorization response', async () => {
  const popup = popupWindow()
  const controller = new AbortController()
  let complete!: (value: { state: string; url: string }) => void
  const prepared = new Promise<{ state: string; url: string }>((resolve) => {
    complete = resolve
  })
  const result = openOAuthPopup({
    provider: 'github',
    intent: 'bind',
    signal: controller.signal,
    prepare: () => prepared,
  })
  const rejected = expect(result).rejects.toBeInstanceOf(AuthOperationError)
  controller.abort()
  await rejected
  complete({ state: 'state', url: 'https://example.com/authorize' })
  await prepared
  expect(popup.closed).toBe(true)
  expect(popup.location.replace).not.toHaveBeenCalled()
})

it.each(['bind', 'verify'] as const)(
  'keeps the %s callback request alive after the popup closes',
  async (intent) => {
    vi.useFakeTimers()
    const popup = popupWindow()
    const prepared = Promise.resolve({
      state: 'state',
      url: 'https://example.com/authorize',
    })
    const result = openOAuthPopup({
      provider: 'github',
      intent,
      signal: new AbortController().signal,
      prepare: () => prepared,
    })
    await prepared
    callbackMessage(popup, { intent })
    const exchange = await result
    popup.closed = true
    await vi.advanceTimersByTimeAsync(11 * 60_000)
    expect(exchange.signal.aborted).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    exchange.finish({ success: true })
  }
)

it('still cancels when the caller aborts after receiving a callback', async () => {
  const popup = popupWindow()
  const controller = new AbortController()
  const prepared = Promise.resolve({
    state: 'state',
    url: 'https://example.com/authorize',
  })
  const result = openOAuthPopup({
    provider: 'github',
    intent: 'verify',
    signal: controller.signal,
    prepare: () => prepared,
  })
  await prepared
  callbackMessage(popup)
  const exchange = await result
  controller.abort(new AuthOperationError('Cancelled', 'AUTH_CANCELLED'))
  expect(exchange.signal.aborted).toBe(true)
  expect(popup.closed).toBe(true)
})

it('cancels when the popup closes before receiving a callback', async () => {
  vi.useFakeTimers()
  const popup = popupWindow()
  const result = openOAuthPopup({
    provider: 'github',
    intent: 'verify',
    signal: new AbortController().signal,
    prepare: async () => ({
      state: 'state',
      url: 'https://example.com/authorize',
    }),
  })
  const rejected = expect(result).rejects.toMatchObject({
    code: 'AUTH_CANCELLED',
  })
  popup.closed = true
  await vi.advanceTimersByTimeAsync(500)
  await rejected
  expect(vi.getTimerCount()).toBe(0)
})

it('reports a blocked popup without starting authorization', async () => {
  vi.spyOn(window, 'open').mockReturnValue(null)
  const prepare = vi.fn()
  await expect(
    openOAuthPopup({
      provider: 'github',
      intent: 'verify',
      signal: new AbortController().signal,
      prepare,
    })
  ).rejects.toThrow('OAuth pop-up was blocked')
  expect(prepare).not.toHaveBeenCalled()
})

it('times out an unfinished authorization and clears its listeners and timers', async () => {
  vi.useFakeTimers()
  const popup = popupWindow()
  const prepared = Promise.resolve({
    state: 'state',
    url: 'https://example.com/authorize',
  })
  const result = openOAuthPopup({
    provider: 'github',
    intent: 'verify',
    signal: new AbortController().signal,
    prepare: () => prepared,
  })
  const rejected = expect(result).rejects.toThrow(
    'OAuth authorization timed out. Please try again.'
  )
  await prepared
  await vi.advanceTimersByTimeAsync(10 * 60_000)
  await rejected
  expect(popup.closed).toBe(true)
  expect(vi.getTimerCount()).toBe(0)
})
