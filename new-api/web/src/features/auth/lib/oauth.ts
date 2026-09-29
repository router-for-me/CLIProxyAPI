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
import type { SystemStatus, OAuthProvider } from '../types'

export {
  buildGitHubOAuthUrl,
  buildDiscordOAuthUrl,
  buildOIDCOAuthUrl,
  buildLinuxDOOAuthUrl,
} from '@/lib/oauth'

// ============================================================================
// OAuth Providers Utilities
// ============================================================================

/**
 * Get available OAuth providers from system status
 */
export function getAvailableOAuthProviders(
  status: SystemStatus | null
): OAuthProvider[] {
  if (!status) return []

  const providers: OAuthProvider[] = []

  if (status.github_oauth) {
    providers.push({
      name: 'GitHub',
      type: 'github',
      enabled: true,
      clientId: status.github_client_id,
    })
  }

  if (status.discord_oauth) {
    providers.push({
      name: 'Discord',
      type: 'discord',
      enabled: true,
      clientId: status.discord_client_id,
    })
  }

  if (status.oidc_enabled) {
    providers.push({
      name: 'OIDC',
      type: 'oidc',
      enabled: true,
      clientId: status.oidc_client_id,
      authEndpoint: status.oidc_authorization_endpoint,
    })
  }

  if (status.linuxdo_oauth) {
    providers.push({
      name: 'LinuxDO',
      type: 'linuxdo',
      enabled: true,
      clientId: status.linuxdo_client_id,
    })
  }

  if (status.telegram_oauth) {
    providers.push({
      name: 'Telegram',
      type: 'telegram',
      enabled: true,
    })
  }

  return providers
}

/**
 * Check if any OAuth provider is available
 */
export function hasOAuthProviders(status: SystemStatus | null): boolean {
  if (!status) return false
  return !!(
    status.github_oauth ||
    status.discord_oauth ||
    status.oidc_enabled ||
    status.linuxdo_oauth ||
    status.telegram_oauth ||
    status.wechat_login
  )
}
