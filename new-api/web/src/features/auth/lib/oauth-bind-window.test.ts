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
import { describe, expect, test } from 'vitest'

import {
  parseTelegramBindCallback,
  postTelegramBindResult,
  startOAuthBindResponseDeadline,
  watchOAuthPopupClosed,
} from './oauth-bind-window'

function fakeTimerRuntime() {
  let callback: (() => void) | undefined
  let delay = 0
  const cancelled: unknown[] = []
  const handle = Symbol('timer')
  return {
    runtime: {
      schedule: (scheduled: () => void, scheduledDelay: number) => {
        callback = scheduled
        delay = scheduledDelay
        return handle
      },
      cancel: (cancelledHandle: unknown) => cancelled.push(cancelledHandle),
    },
    fire: () => callback?.(),
    get delay() {
      return delay
    },
    cancelled,
    handle,
  }
}

describe('OAuth bind popup lifecycle', () => {
  test('parses Telegram success and stable error callbacks', () => {
    expect(
      parseTelegramBindCallback({
        telegram_bind: 'success',
        flow_token: 'flow-success',
      })
    ).toEqual({
      kind: 'result',
      flowToken: 'flow-success',
      success: true,
    })
    expect(
      parseTelegramBindCallback({
        telegram_bind: 'error',
        flow_token: 'flow-error',
        error_code: 'TELEGRAM_BIND_ALREADY_BOUND',
      })
    ).toEqual({
      kind: 'result',
      flowToken: 'flow-error',
      success: false,
      code: 'TELEGRAM_BIND_ALREADY_BOUND',
    })
  })

  test('rejects Telegram callbacks without a flow token and ignores descriptions', () => {
    expect(parseTelegramBindCallback({ telegram_bind: 'error' })).toEqual({
      kind: 'invalid',
    })
    expect(
      parseTelegramBindCallback({
        telegram_bind: 'error',
        flow_token: 'flow-error',
        error_code: 'UNKNOWN_CODE',
        error_description: 'untrusted message',
      } as Parameters<typeof parseTelegramBindCallback>[0])
    ).toEqual({
      kind: 'result',
      flowToken: 'flow-error',
      success: false,
      code: 'UNKNOWN_CODE',
    })
    expect(parseTelegramBindCallback({})).toBe(null)
  })

  test('posts only complete Telegram bind results to an available opener', () => {
    const messages: Array<{ message: unknown; targetOrigin: string }> = []
    const opener = {
      closed: false,
      postMessage: (message: unknown, targetOrigin: string) => {
        messages.push({ message, targetOrigin })
      },
    } as Pick<Window, 'closed' | 'postMessage'>
    const callback = parseTelegramBindCallback({
      telegram_bind: 'error',
      flow_token: 'flow-error',
      error_code: 'UNKNOWN_CODE',
    })

    expect(
      postTelegramBindResult(callback, opener, 'https://dashboard.example.com')
    ).toBe(true)
    expect(messages).toEqual([
      {
        message: {
          type: 'telegram:binding:result',
          flow_token: 'flow-error',
          success: false,
          code: 'UNKNOWN_CODE',
        },
        targetOrigin: 'https://dashboard.example.com',
      },
    ])

    expect(
      postTelegramBindResult({ kind: 'invalid' }, opener, 'https://example.com')
    ).toBe(false)
    expect(
      postTelegramBindResult(
        callback,
        { ...opener, closed: true },
        'https://example.com'
      )
    ).toBe(false)
    expect(messages.length).toBe(1)
  })

  test('waits 30 seconds for the opener response and can be cancelled', () => {
    const timer = fakeTimerRuntime()
    let timedOut = false
    const cancel = startOAuthBindResponseDeadline(
      () => {
        timedOut = true
      },
      undefined,
      timer.runtime
    )

    expect(timer.delay).toBe(30_000)
    cancel()
    timer.fire()
    expect(timedOut).toBe(false)
    expect(timer.cancelled).toEqual([timer.handle])
  })

  test('reports a closed popup once and clears its poller', () => {
    const timer = fakeTimerRuntime()
    const popup = { closed: false }
    let closedCount = 0
    watchOAuthPopupClosed(
      popup,
      () => {
        closedCount += 1
      },
      undefined,
      timer.runtime
    )

    expect(timer.delay).toBe(500)
    timer.fire()
    expect(closedCount).toBe(0)
    popup.closed = true
    timer.fire()
    timer.fire()
    expect(closedCount).toBe(1)
    expect(timer.cancelled).toEqual([timer.handle])
  })
})
