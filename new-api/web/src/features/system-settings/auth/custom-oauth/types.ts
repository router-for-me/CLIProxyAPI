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
import * as z from 'zod'

// ============================================================================
// Custom OAuth Provider Types
// ============================================================================

export interface CustomOAuthProvider {
  id: number
  name: string
  slug: string
  icon: string
  enabled: boolean
  client_id: string
  client_secret: string
  authorization_endpoint: string
  token_endpoint: string
  user_info_endpoint: string
  scopes: string
  user_id_field: string
  username_field: string
  display_name_field: string
  email_field: string
  well_known: string
  auth_style: number // 0=auto, 1=params, 2=header
  access_policy: string
  access_denied_message: string
}

// ============================================================================
// Form Schema
// ============================================================================

export const customOAuthFormSchema = z.object({
  name: z.string().min(1, 'Name is required'),
  slug: z
    .string()
    .min(1, 'Slug is required')
    .regex(
      /^[a-z0-9-]+$/,
      'Slug must only contain lowercase letters, numbers, and hyphens'
    ),
  icon: z.string().optional().default(''),
  enabled: z.boolean().default(true),
  client_id: z.string().min(1, 'Client ID is required'),
  client_secret: z.string().optional().default(''),
  authorization_endpoint: z
    .string()
    .min(1, 'Authorization endpoint is required'),
  token_endpoint: z.string().min(1, 'Token endpoint is required'),
  user_info_endpoint: z.string().min(1, 'User info endpoint is required'),
  scopes: z.string().optional().default(''),
  user_id_field: z.string().min(1, 'User ID field is required'),
  username_field: z.string().optional().default(''),
  display_name_field: z.string().optional().default(''),
  email_field: z.string().optional().default(''),
  well_known: z.string().optional().default(''),
  auth_style: z.number().int().min(0).max(2).default(0),
  access_policy: z.string().optional().default(''),
  access_denied_message: z.string().optional().default(''),
})

export type CustomOAuthFormValues = z.infer<typeof customOAuthFormSchema>

// ============================================================================
// OIDC Discovery
// ============================================================================

export interface DiscoveryResponse {
  success: boolean
  message?: string
  data?: {
    well_known_url?: string
    discovery?: {
      authorization_endpoint?: string
      token_endpoint?: string
      userinfo_endpoint?: string
      scopes_supported?: string[]
    }
  }
}

// ============================================================================
// Preset Templates
// ============================================================================

export interface OAuthPreset {
  key: string
  name: string
  icon: string
  authorization_endpoint: string
  token_endpoint: string
  user_info_endpoint: string
  scopes: string
  user_id_field: string
  username_field: string
  display_name_field: string
  email_field: string
  needsBaseUrl: boolean
}

export const OAUTH_PRESETS: OAuthPreset[] = [
  {
    key: 'github-enterprise',
    name: 'GitHub Enterprise',
    icon: 'github',
    authorization_endpoint: '/login/oauth/authorize',
    token_endpoint: '/login/oauth/access_token',
    user_info_endpoint: '/api/v3/user',
    scopes: 'user:email',
    user_id_field: 'id',
    username_field: 'login',
    display_name_field: 'name',
    email_field: 'email',
    needsBaseUrl: true,
  },
  {
    key: 'gitlab',
    name: 'GitLab',
    icon: 'gitlab',
    authorization_endpoint: '/oauth/authorize',
    token_endpoint: '/oauth/token',
    user_info_endpoint: '/api/v4/user',
    scopes: 'openid profile email',
    user_id_field: 'id',
    username_field: 'username',
    display_name_field: 'name',
    email_field: 'email',
    needsBaseUrl: true,
  },
  {
    key: 'gitea',
    name: 'Gitea',
    icon: 'gitea',
    authorization_endpoint: '/login/oauth/authorize',
    token_endpoint: '/login/oauth/access_token',
    user_info_endpoint: '/api/v1/user',
    scopes: 'openid profile email',
    user_id_field: 'id',
    username_field: 'login',
    display_name_field: 'full_name',
    email_field: 'email',
    needsBaseUrl: true,
  },
  {
    key: 'nextcloud',
    name: 'Nextcloud',
    icon: 'nextcloud',
    authorization_endpoint: '/apps/oauth2/authorize',
    token_endpoint: '/apps/oauth2/api/v1/token',
    user_info_endpoint: '/ocs/v2.php/cloud/user?format=json',
    scopes: 'openid profile email',
    user_id_field: 'ocs.data.id',
    username_field: 'ocs.data.id',
    display_name_field: 'ocs.data.displayname',
    email_field: 'ocs.data.email',
    needsBaseUrl: true,
  },
  {
    key: 'keycloak',
    name: 'Keycloak',
    icon: 'keycloak',
    authorization_endpoint: '/realms/{realm}/protocol/openid-connect/auth',
    token_endpoint: '/realms/{realm}/protocol/openid-connect/token',
    user_info_endpoint: '/realms/{realm}/protocol/openid-connect/userinfo',
    scopes: 'openid profile email',
    user_id_field: 'sub',
    username_field: 'preferred_username',
    display_name_field: 'name',
    email_field: 'email',
    needsBaseUrl: true,
  },
  {
    key: 'authentik',
    name: 'Authentik',
    icon: 'authentik',
    authorization_endpoint: '/application/o/authorize/',
    token_endpoint: '/application/o/token/',
    user_info_endpoint: '/application/o/userinfo/',
    scopes: 'openid profile email',
    user_id_field: 'sub',
    username_field: 'preferred_username',
    display_name_field: 'name',
    email_field: 'email',
    needsBaseUrl: true,
  },
  {
    key: 'ory',
    name: 'ORY Hydra',
    icon: 'openid',
    authorization_endpoint: '/oauth2/auth',
    token_endpoint: '/oauth2/token',
    user_info_endpoint: '/userinfo',
    scopes: 'openid profile email',
    user_id_field: 'sub',
    username_field: 'preferred_username',
    display_name_field: 'name',
    email_field: 'email',
    needsBaseUrl: true,
  },
]

export const AUTH_STYLE_OPTIONS = [
  { value: 0, labelKey: 'Auto Detect' },
  { value: 1, labelKey: 'Params (in body)' },
  { value: 2, labelKey: 'Header (Basic Auth)' },
] as const
