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
import { TELEGRAM_BIND_RESULT_MESSAGE } from '@/features/auth/constants'

interface TimerRuntime {
  schedule: (callback: () => void, delay: number) => unknown
  cancel: (handle: unknown) => void
}

const timeoutRuntime: TimerRuntime = {
  schedule: (callback, delay) => globalThis.setTimeout(callback, delay),
  cancel: (handle) =>
    globalThis.clearTimeout(handle as ReturnType<typeof globalThis.setTimeout>),
}

const intervalRuntime: TimerRuntime = {
  schedule: (callback, delay) => globalThis.setInterval(callback, delay),
  cancel: (handle) =>
    globalThis.clearInterval(
      handle as ReturnType<typeof globalThis.setInterval>
    ),
}

interface TelegramBindCallbackSearch {
  telegram_bind?: string
  flow_token?: string
  error_code?: string
}

export type TelegramBindCallback =
  | {
      kind: 'result'
      flowToken: string
      success: boolean
      code?: string
    }
  | { kind: 'invalid' }
  | null

export function parseTelegramBindCallback(
  search: TelegramBindCallbackSearch
): TelegramBindCallback {
  if (search.telegram_bind !== 'success' && search.telegram_bind !== 'error') {
    return null
  }
  if (!search.flow_token) return { kind: 'invalid' }

  if (search.telegram_bind === 'success') {
    return {
      kind: 'result',
      flowToken: search.flow_token,
      success: true,
    }
  }
  return {
    kind: 'result',
    flowToken: search.flow_token,
    success: false,
    code: search.error_code,
  }
}

export function postTelegramBindResult(
  callback: TelegramBindCallback,
  opener: Pick<Window, 'closed' | 'postMessage'> | null,
  targetOrigin: string
): boolean {
  if (callback?.kind !== 'result' || !opener || opener.closed) return false

  opener.postMessage(
    {
      type: TELEGRAM_BIND_RESULT_MESSAGE,
      flow_token: callback.flowToken,
      success: callback.success,
      code: callback.code,
    },
    targetOrigin
  )
  return true
}

export function startOAuthBindResponseDeadline(
  onTimeout: () => void,
  delay = 30_000,
  runtime: TimerRuntime = timeoutRuntime
): () => void {
  let active = true
  const handle = runtime.schedule(() => {
    if (!active) return
    active = false
    onTimeout()
  }, delay)
  return () => {
    if (!active) return
    active = false
    runtime.cancel(handle)
  }
}

export function watchOAuthPopupClosed(
  popup: Pick<Window, 'closed'>,
  onClosed: () => void,
  interval = 500,
  runtime: TimerRuntime = intervalRuntime
): () => void {
  let active = true
  const handle = runtime.schedule(() => {
    if (!active || !popup.closed) return
    active = false
    runtime.cancel(handle)
    onClosed()
  }, interval)
  return () => {
    if (!active) return
    active = false
    runtime.cancel(handle)
  }
}
