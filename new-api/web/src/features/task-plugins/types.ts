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
import type {
  BillingUsageSchema,
  BillingUsageExample,
} from '@/features/pricing/types'

export type TaskPluginProtocolClaim =
  | string
  | {
      name: string
      models?: string[]
      supports?: ('stream' | 'sync' | 'background')[]
    }

/**
 * One native route declared by `meta.routes` (backend `jsplugin.Route`). Only
 * the fields the admin UI renders are typed; hook bindings such as `decode`,
 * `render` and `action` are implementation detail of the plugin.
 */
export type TaskPluginRoute = {
  method: string
  path: string
  type: 'submit' | 'query' | 'dynamic'
  models?: string[]
}

export type TaskPluginMeta = {
  sortPriority?: number
  website?: string
  apiVersion: number
  key: string
  name: string
  icon?: string
  description?: Record<string, string>
  version: string
  author: {
    name: string
    url?: string
  }
  baseUrl?: string
  channelTypes?: number[] | null
  models: string[] | null
  fetchMode: string
  routes?: TaskPluginRoute[]
  protocols?: TaskPluginProtocolClaim[]
  usageSchema?: BillingUsageSchema
  usageProfiles?: {
    models: string[]
    schema: BillingUsageSchema
    examples?: BillingUsageExample[]
  }[]
}

export type TaskPluginRecord = {
  id: number
  key: string
  api_version: number
  version: string
  source: string
  source_hash: string
  enabled: boolean
  active: boolean
  created_at: number
  remark: string
}

export type TaskPluginListItem = {
  meta: TaskPluginMeta
  source: 'factory' | 'override' | 'override_over_factory'
  enabled: boolean
  active: boolean
  source_hash: string
  has_icon?: boolean
  remark: string
  runtime_status:
    | 'registered'
    | 'compile_failed'
    | 'disabled'
    | 'disabled_fallback'
    | 'not_registered'
  runtime_error?: string
  factory_meta?: TaskPluginMeta
  channel_count: number
  in_flight_count: number
}

export type TaskPluginUsage = {
  channels: Array<{ id: number; name: string }>
  in_flight_count: number
}

export type TaskPluginDetail = {
  plugin?: TaskPluginRecord
  meta: TaskPluginMeta
  source: string
  layer: 'factory' | 'override'
  has_icon?: boolean
}

export type ApiResponse<T> = {
  success: boolean
  message: string
  data: T
}

export type TaskPluginDryRunRequest = {
  hook: string
  member?: string
  args: unknown[]
}

export type MarketplaceSource = {
  name: string
  index_url: string
}

/**
 * A single installable version from a marketplace index. `allowedHosts`,
 * `baseUrl`, `auth` and `sha256` are optional: older or hand-rolled indexes may
 * omit them, and the confirmation dialog degrades to a warning rather than
 * refusing to render.
 */
export type MarketplaceIndexVersion = {
  version: string
  path: string
  sha256?: string
  minApiVersion?: number
  kind?: string
  allowedHosts?: string[]
  baseUrl?: string
  auth?: string
}

export type MarketplacePluginIcon = {
  /** Index-relative path of the sidecar icon.svg / icon.png, resolved like `path`. */
  path: string
  sha256?: string
}

export type MarketplacePlugin = {
  protocols?: TaskPluginProtocolClaim[]
  sortPriority?: number
  website?: string
  key: string
  name: string
  icon?: string
  /** Sidecar logo published by the index; rendered from the source repository. */
  iconFile?: MarketplacePluginIcon
  description?: string | Record<string, string>
  channelTypes?: number[]
  models?: string[]
  latest: string
  versions: MarketplaceIndexVersion[]
}

export type MarketplaceIndex = {
  indexVersion: number
  name: string
  plugins: MarketplacePlugin[]
}

/** A missing source property is distinct from an empty value or an unreadable expression. */
export type PluginPreviewField<T> =
  | { state: 'value'; value: T; origin: 'source' | 'index' }
  | { state: 'missing'; origin: 'source' }
  | { state: 'unknown' }

export type PluginPreviewValues = {
  models: string[]
  protocols: TaskPluginProtocolClaim[]
  routes: TaskPluginRoute[]
  channelTypes: number[]
  baseUrl: string
  allowedHosts: string[]
  auth: string
}

export type PluginMetaPreview = {
  status: 'parsed' | 'partial' | 'unavailable'
  fields: {
    [Key in keyof PluginPreviewValues]: PluginPreviewField<
      PluginPreviewValues[Key]
    >
  }
}
