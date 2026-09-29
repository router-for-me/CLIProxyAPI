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
  createFileRoute,
  useNavigate,
  useParams,
  useSearch,
} from '@tanstack/react-router'
import type { AxiosRequestConfig } from 'axios'
import i18next from 'i18next'
import { useEffect, useRef } from 'react'
import { toast } from 'sonner'

import { OAuthCallbackScreen } from '@/features/auth/components/oauth-callback-screen'
import {
  OAUTH_POPUP_CALLBACK_MESSAGE,
  OAUTH_POPUP_RESULT_MESSAGE,
} from '@/features/auth/constants'
import { useAuthRedirect } from '@/features/auth/hooks/use-auth-redirect'
import { sanitizeAuthRedirect } from '@/features/auth/lib/auth-redirect'
import {
  parseTelegramBindCallback,
  postTelegramBindResult,
  startOAuthBindResponseDeadline,
} from '@/features/auth/lib/oauth-bind-window'
import {
  getOAuthSessionStorage,
  consumeOAuthLoginRedirect,
  resolveOAuthCallbackMode,
} from '@/features/auth/lib/oauth-callback-mode'
import type { LoginResponse } from '@/features/auth/types'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import { getServerErrorMessageKey } from '@/lib/server-error-message'

type OAuthRequestConfig = AxiosRequestConfig & {
  skipBusinessError?: boolean
  skipAuthRefresh?: boolean
}

interface OAuthPopupResult {
  intent: 'bind' | 'verify'
  type: typeof OAUTH_POPUP_RESULT_MESSAGE
  provider: string
  state: string
  success: boolean
  message?: string
}

function OAuthCallback() {
  const navigate = useNavigate()
  const { handleLoginResult } = useAuthRedirect()
  const loginExchange = useRef<{
    key: string
    request: Promise<{ data: LoginResponse }>
  } | null>(null)
  const completedLogin = useRef<string | null>(null)
  const { provider } = useParams({ from: '/oauth/$provider' }) as {
    provider: string
  }
  const search = useSearch({ from: '/oauth/$provider' }) as {
    code?: string
    state?: string
    error?: string
    error_description?: string
    redirect?: string
    telegram_bind?: string
    flow_token?: string
    error_code?: string
  }
  const callbackState = search.state ?? ''
  const isTelegramBindCallback =
    provider === 'telegram' &&
    (search.telegram_bind === 'success' || search.telegram_bind === 'error')
  let mode: 'login' | 'bind' | 'verify' = 'login'
  if (isTelegramBindCallback) {
    mode = 'bind'
  } else if (typeof window !== 'undefined') {
    mode = resolveOAuthCallbackMode(provider, callbackState, {
      opener: window.opener,
      storage: getOAuthSessionStorage(window),
    })
  }

  useEffect(() => {
    if (typeof window === 'undefined') return

    const code = search.code ?? ''
    const state = callbackState
    const telegramCallback =
      provider === 'telegram'
        ? parseTelegramBindCallback({
            telegram_bind: search.telegram_bind,
            flow_token: search.flow_token,
            error_code: search.error_code,
          })
        : null
    if (telegramCallback) {
      const opener = window.opener
      if (
        !postTelegramBindResult(
          telegramCallback,
          opener,
          window.location.origin
        )
      ) {
        toast.error(i18next.t('Telegram binding failed. Please try again.'))
        const closeTimeout = window.setTimeout(() => window.close(), 1500)
        return () => window.clearTimeout(closeTimeout)
      }
      window.close()
      return
    }

    if (mode === 'bind' || mode === 'verify') {
      const opener = window.opener
      if (!opener || opener.closed) {
        toast.error(i18next.t('OAuth window is no longer available.'))
        return
      }

      let cancelResultTimeout: () => void = () => undefined
      let delayedClose: number | undefined
      const handleBindingResult = (event: MessageEvent<unknown>) => {
        if (
          event.origin !== window.location.origin ||
          event.source !== opener
        ) {
          return
        }
        const result = event.data as Partial<OAuthPopupResult> | null
        if (
          !result ||
          result.type !== OAUTH_POPUP_RESULT_MESSAGE ||
          result.intent !== mode ||
          result.provider !== provider ||
          result.state !== state
        ) {
          return
        }
        cancelResultTimeout()
        if (result.success) {
          if (mode === 'bind') toast.success(i18next.t('Binding successful!'))
          window.close()
          return
        }
        handleServerError(result, i18next.t('OAuth failed'))
        delayedClose = window.setTimeout(() => window.close(), 1500)
      }

      window.addEventListener('message', handleBindingResult)
      cancelResultTimeout = startOAuthBindResponseDeadline(() => {
        toast.error(
          i18next.t('OAuth authorization timed out. Please try again.')
        )
        delayedClose = window.setTimeout(() => window.close(), 1500)
      })
      opener.postMessage(
        {
          type: OAUTH_POPUP_CALLBACK_MESSAGE,
          intent: mode,
          provider,
          code,
          state,
          error: search.error,
          errorDescription: search.error_description,
        },
        window.location.origin
      )
      return () => {
        window.removeEventListener('message', handleBindingResult)
        cancelResultTimeout()
        if (delayedClose !== undefined) window.clearTimeout(delayedClose)
      }
    }

    const safeNavigate = (target: unknown, fallback = '/dashboard') => {
      const href =
        sanitizeAuthRedirect(target, window.location.origin) ?? fallback
      void navigate({ href, replace: true })
    }

    if (!code && !search.error) {
      toast.error(i18next.t('Missing code'))
      safeNavigate('/sign-in', '/sign-in')
      return
    }

    const loginKey = `${provider}:${state}:${code}`
    if (completedLogin.current === loginKey) return
    let active = true
    void (async () => {
      try {
        const config: OAuthRequestConfig = {
          params: {
            code: code || undefined,
            state,
            error: search.error,
            error_description: search.error_description,
          },
          skipBusinessError: true,
          skipAuthRefresh: true,
        }
        if (loginExchange.current?.key !== loginKey) {
          loginExchange.current = {
            key: loginKey,
            request: api.get<LoginResponse>(`/api/oauth/${provider}`, config),
          }
        }
        const response = await loginExchange.current.request
        if (!active) return
        if (response.data?.success) {
          completedLogin.current = loginKey
          if (
            await handleLoginResult(
              response.data.data,
              search.redirect ?? consumeOAuthLoginRedirect(state) ?? undefined
            )
          ) {
            toast.success(i18next.t('Signed in successfully!'))
          }
          return
        }
        const messageKey = getServerErrorMessageKey(response.data)
        handleServerError(
          response.data,
          messageKey
            ? i18next.t(messageKey)
            : response.data?.message || i18next.t('OAuth failed')
        )
      } catch (error: unknown) {
        if (!active) return
        handleServerError(
          AuthOperationError.from(error, i18next.t('OAuth failed'))
        )
      }
      safeNavigate('/sign-in', '/sign-in')
    })()
    return () => {
      active = false
    }
  }, [
    callbackState,
    mode,
    navigate,
    handleLoginResult,
    provider,
    search.code,
    search.error,
    search.error_code,
    search.error_description,
    search.flow_token,
    search.redirect,
    search.telegram_bind,
  ])

  return <OAuthCallbackScreen provider={provider} mode={mode} />
}

export const Route = createFileRoute('/oauth/$provider')({
  component: OAuthCallback,
})
