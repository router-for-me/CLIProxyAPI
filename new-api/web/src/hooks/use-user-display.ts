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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { getRoleLabel } from '@/lib/roles'
import type { AuthUser } from '@/stores/auth-store'

/**
 * Custom hook to format user display information
 * Centralizes user display logic used across ProfileDropdown and MobileDrawer
 */
export function useUserDisplay(user: AuthUser | null | undefined) {
  const { t } = useTranslation()
  return useMemo(() => {
    if (!user) {
      return {
        displayName: t('User'),
        secondaryText: '',
        initials: 'U',
        roleLabel: '',
      }
    }

    // Display name: priority order
    const displayName = user.display_name || user.username || t('User')

    // Secondary text: first available identifier
    const secondaryText = (() => {
      if (user.email) return user.email
      if (user.github_id) return `GitHub ID: ${user.github_id}`
      if (user.oidc_id) return `OIDC ID: ${user.oidc_id}`
      if (user.wechat_id) return `WeChat ID: ${user.wechat_id}`
      if (user.telegram_id) return `Telegram ID: ${user.telegram_id}`
      if (user.linux_do_id) return `LinuxDO ID: ${user.linux_do_id}`
      if (user.username) return user.username
      if (user.display_name) return user.display_name
      return ''
    })()

    // Generate initials from display name
    const initials = displayName
      .split(' ')
      .map((n: string) => n[0])
      .join('')
      .toUpperCase()
      .slice(0, 2)

    // Get role label
    const roleLabel = getRoleLabel(user.role)

    return {
      displayName,
      secondaryText,
      initials,
      roleLabel,
    }
  }, [user, t])
}
