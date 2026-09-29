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
import { createFileRoute, useNavigate, useSearch } from '@tanstack/react-router'
import i18next from 'i18next'
import { useEffect } from 'react'

import { wechatLoginByCode } from '@/features/auth/api'
import { sanitizeAuthRedirect } from '@/features/auth/lib/auth-redirect'
import { applyAuthBundle, isAuthBundle } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import { createServerError } from '@/lib/server-error-message'

function OAuthComponent() {
  const navigate = useNavigate()
  const search = useSearch({ from: '/(auth)/oauth' }) as {
    redirect?: string
    provider?: 'github' | 'discord' | 'oidc' | 'linuxdo' | 'telegram' | 'wechat'
    code?: string
    state?: string
  }

  useEffect(() => {
    ;(async () => {
      try {
        if (search?.provider === 'wechat' && search.code) {
          const res = await wechatLoginByCode(search.code)
          if (res?.success && isAuthBundle(res.data)) {
            applyAuthBundle(res.data)
            const target =
              sanitizeAuthRedirect(search?.redirect, window.location.origin) ??
              '/dashboard'
            navigate({ href: target, replace: true })
            return
          }
          throw createServerError(res, i18next.t('OAuth failed'))
        }
        handleServerError(new AuthOperationError(i18next.t('OAuth failed')))
      } catch (error: unknown) {
        handleServerError(
          AuthOperationError.from(error, i18next.t('OAuth failed'))
        )
      }
      navigate({ to: '/sign-in', replace: true })
    })()
  }, [navigate, search])

  return null
}

export const Route = createFileRoute('/(auth)/oauth')({
  component: OAuthComponent,
})
