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
import type { TFunction } from 'i18next'

export function sessionDevice(
  userAgent: string,
  unknownDevice: string,
  browserLabel: string,
  maxTouchPoints = 0
): string {
  if (!userAgent) return unknownDevice
  let browser = browserLabel
  if (userAgent.includes('Edg/')) browser = 'Edge'
  else if (userAgent.includes('Chrome/')) browser = 'Chrome'
  else if (userAgent.includes('Firefox/')) browser = 'Firefox'
  else if (userAgent.includes('Safari/')) browser = 'Safari'

  let system = ''
  const isIPad =
    userAgent.includes('iPad') ||
    (userAgent.includes('Macintosh') && maxTouchPoints > 1)
  if (userAgent.includes('iPhone') || isIPad) {
    system = 'iOS'
  } else if (userAgent.includes('Android')) system = 'Android'
  else if (userAgent.includes('Windows')) system = 'Windows'
  else if (userAgent.includes('Mac OS')) system = 'macOS'
  else if (userAgent.includes('Linux')) system = 'Linux'
  return system ? `${browser} · ${system}` : browser
}

export function loginMethodLabel(method: string, t: TFunction): string {
  const normalized = method.trim().toLowerCase()
  switch (normalized) {
    case 'password':
      return t('Password')
    case '2fa':
      return t('Two-factor Authentication')
    case 'passkey':
      return t('Passkey')
    case 'wechat':
      return t('WeChat')
    case 'telegram':
      return t('Telegram')
    case 'oauth':
      return t('OAuth')
    case 'unknown':
    case '':
      return t('Unknown')
    default:
      break
  }

  if (!normalized.startsWith('oauth:')) return method
  const provider = normalized.slice('oauth:'.length)
  const providerNames: Record<string, string> = {
    discord: 'Discord',
    github: 'GitHub',
    linuxdo: 'LinuxDO',
    oidc: 'OIDC',
  }
  return `${t('OAuth')} · ${providerNames[provider] || provider}`
}
