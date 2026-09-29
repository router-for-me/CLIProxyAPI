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
import type { SystemStatus } from '@/features/auth/types'

// ============================================================================
// OAuth URL Builders
// ============================================================================

export interface CustomOAuthBinding {
  provider_id: number
  provider_name: string
  provider_slug: string
  provider_icon: string
  provider_user_id: string
}

export function indexCustomOAuthBindings(
  bindings: CustomOAuthBinding[]
): Map<number, CustomOAuthBinding> {
  return new Map(bindings.map((binding) => [binding.provider_id, binding]))
}

/**
 * Build GitHub OAuth URL
 */
export function buildGitHubOAuthUrl(clientId: string, state: string): string {
  return `https://github.com/login/oauth/authorize?client_id=${clientId}&state=${state}&scope=user:email`
}

/**
 * Build Discord OAuth URL
 */
export function buildDiscordOAuthUrl(clientId: string, state: string): string {
  const url = new URL('https://discord.com/oauth2/authorize')
  url.searchParams.set('client_id', clientId)
  url.searchParams.set(
    'redirect_uri',
    `${window.location.origin}/oauth/discord`
  )
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('scope', 'identify+openid')
  url.searchParams.set('state', state)
  return url.toString()
}

/**
 * Build OIDC OAuth URL
 */
export function buildOIDCOAuthUrl(
  authUrl: string,
  clientId: string,
  state: string
): string {
  const url = new URL(authUrl)
  url.searchParams.set('client_id', clientId)
  url.searchParams.set('redirect_uri', `${window.location.origin}/oauth/oidc`)
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('scope', 'openid profile email')
  url.searchParams.set('state', state)
  return url.toString()
}

/**
 * Build LinuxDO OAuth URL
 */
export function buildLinuxDOOAuthUrl(clientId: string, state: string): string {
  return `https://connect.linux.do/oauth2/authorize?response_type=code&client_id=${clientId}&state=${state}`
}

export function buildOAuthAuthorizationUrl(
  provider: string,
  state: string,
  status: SystemStatus
): string {
  switch (provider) {
    case 'github':
      if (status.github_client_id) {
        return buildGitHubOAuthUrl(status.github_client_id, state)
      }
      break
    case 'discord':
      if (status.discord_client_id) {
        return buildDiscordOAuthUrl(status.discord_client_id, state)
      }
      break
    case 'oidc':
      if (status.oidc_authorization_endpoint && status.oidc_client_id) {
        return buildOIDCOAuthUrl(
          status.oidc_authorization_endpoint,
          status.oidc_client_id,
          state
        )
      }
      break
    case 'linuxdo':
      if (status.linuxdo_client_id) {
        return buildLinuxDOOAuthUrl(status.linuxdo_client_id, state)
      }
      break
    default: {
      const custom = status.custom_oauth_providers?.find(
        (candidate) => candidate.slug === provider
      )
      if (custom) {
        const url = new URL(custom.authorization_endpoint)
        url.searchParams.set('client_id', custom.client_id)
        url.searchParams.set(
          'redirect_uri',
          `${window.location.origin}/oauth/${provider}`
        )
        url.searchParams.set('response_type', 'code')
        url.searchParams.set('state', state)
        if (custom.scopes) url.searchParams.set('scope', custom.scopes)
        return url.toString()
      }
    }
  }
  throw new Error('No linked OAuth provider is available.')
}
