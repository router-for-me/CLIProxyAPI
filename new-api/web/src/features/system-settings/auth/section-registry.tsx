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
import type { AuthSettings } from '../types'
import { createSectionRegistry } from '../utils/section-registry'
import { BasicAuthSection } from './basic-auth-section'
import { BotProtectionSection } from './bot-protection-section'
import { CustomOAuthSection } from './custom-oauth/custom-oauth-section'
import { OAuthSection } from './oauth-section'
import { PasskeySection } from './passkey-section'

const AUTH_SECTIONS = [
  {
    id: 'basic-auth',
    titleKey: 'Basic Authentication',
    build: (settings: AuthSettings) => (
      <BasicAuthSection
        defaultValues={{
          PasswordLoginEnabled: settings.PasswordLoginEnabled,
          PasswordRegisterEnabled: settings.PasswordRegisterEnabled,
          EmailVerificationEnabled: settings.EmailVerificationEnabled,
          RegisterEnabled: settings.RegisterEnabled,
          EmailDomainRestrictionEnabled: settings.EmailDomainRestrictionEnabled,
          EmailAliasRestrictionEnabled: settings.EmailAliasRestrictionEnabled,
          EmailDomainWhitelist: settings.EmailDomainWhitelist,
        }}
      />
    ),
  },
  {
    id: 'oauth',
    titleKey: 'OAuth Integrations',
    build: (settings: AuthSettings) => (
      <OAuthSection
        serverAddress={settings.ServerAddress}
        defaultValues={{
          GitHubOAuthEnabled: settings.GitHubOAuthEnabled,
          GitHubClientId: settings.GitHubClientId,
          GitHubClientSecret: settings.GitHubClientSecret,
          'discord.enabled': settings['discord.enabled'],
          'discord.client_id': settings['discord.client_id'],
          'discord.client_secret': settings['discord.client_secret'],
          'oidc.enabled': settings['oidc.enabled'],
          'oidc.display_name': settings['oidc.display_name'],
          'oidc.client_id': settings['oidc.client_id'],
          'oidc.client_secret': settings['oidc.client_secret'],
          'oidc.well_known': settings['oidc.well_known'],
          'oidc.authorization_endpoint':
            settings['oidc.authorization_endpoint'],
          'oidc.token_endpoint': settings['oidc.token_endpoint'],
          'oidc.user_info_endpoint': settings['oidc.user_info_endpoint'],
          TelegramOAuthEnabled: settings.TelegramOAuthEnabled,
          'telegram.client_id': settings['telegram.client_id'],
          'telegram.client_secret': settings['telegram.client_secret'],
          LinuxDOOAuthEnabled: settings.LinuxDOOAuthEnabled,
          LinuxDOClientId: settings.LinuxDOClientId,
          LinuxDOClientSecret: settings.LinuxDOClientSecret,
          LinuxDOMinimumTrustLevel: settings.LinuxDOMinimumTrustLevel,
          WeChatAuthEnabled: settings.WeChatAuthEnabled,
          WeChatServerAddress: settings.WeChatServerAddress,
          WeChatServerToken: settings.WeChatServerToken,
          WeChatAccountQRCodeImageURL: settings.WeChatAccountQRCodeImageURL,
        }}
      />
    ),
  },
  {
    id: 'passkey',
    titleKey: 'Passkey Authentication',
    build: (settings: AuthSettings) => (
      <PasskeySection
        defaultValues={{
          'passkey.enabled': settings['passkey.enabled'],
          'passkey.rp_display_name': settings['passkey.rp_display_name'],
          'passkey.rp_id': settings['passkey.rp_id'],
          'passkey.legacy_rp_ids': settings['passkey.legacy_rp_ids'],
          'passkey.origins': settings['passkey.origins'],
          'passkey.allow_insecure_origin':
            settings['passkey.allow_insecure_origin'],
          'passkey.user_verification': settings['passkey.user_verification'] as
            | 'required'
            | 'preferred'
            | 'discouraged',
          'passkey.attachment_preference':
            settings['passkey.attachment_preference'],
        }}
      />
    ),
  },
  {
    id: 'bot-protection',
    titleKey: 'Bot Protection',
    build: (settings: AuthSettings) => (
      <BotProtectionSection
        defaultValues={{
          TurnstileCheckEnabled: settings.TurnstileCheckEnabled,
          TurnstileSiteKey: settings.TurnstileSiteKey,
          TurnstileSecretKey: settings.TurnstileSecretKey,
        }}
      />
    ),
  },
  {
    id: 'custom-oauth',
    titleKey: 'Custom OAuth',
    build: (settings: AuthSettings) => (
      <CustomOAuthSection serverAddress={settings.ServerAddress} />
    ),
  },
] as const

export type AuthSectionId = (typeof AUTH_SECTIONS)[number]['id']

const authRegistry = createSectionRegistry<AuthSectionId, AuthSettings>({
  sections: AUTH_SECTIONS,
  defaultSection: 'basic-auth',
  basePath: '/system-settings/auth',
  urlStyle: 'path',
})

export const AUTH_SECTION_IDS = authRegistry.sectionIds
export const AUTH_DEFAULT_SECTION = authRegistry.defaultSection
export const getAuthSectionNavItems = authRegistry.getSectionNavItems
export const getAuthSectionContent = authRegistry.getSectionContent
export const getAuthSectionMeta = authRegistry.getSectionMeta
