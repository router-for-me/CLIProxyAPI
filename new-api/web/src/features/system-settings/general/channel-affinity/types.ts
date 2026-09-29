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
export interface KeySource {
  type: 'context_int' | 'context_string' | 'request_header' | 'gjson'
  key?: string
  path?: string
}

export type SessionMode = 'off' | 'prefer' | 'strict'

export interface AffinityRule {
  session_mode?: SessionMode | 'inherit'
  id?: number
  name: string
  model_regex: string[]
  path_regex: string[]
  user_agent_include?: string[]
  key_sources: KeySource[]
  value_regex?: string
  ttl_seconds: number
  skip_retry_on_failure: boolean
  include_using_group: boolean
  include_model_name: boolean
  include_rule_name: boolean
  param_override_template?: Record<string, unknown> | null
}

export interface CacheStats {
  enabled: boolean
  total: number
  unknown: number
  by_rule_name: Record<string, number>
  cache_capacity: number
  cache_algo: string
}

export interface ChannelAffinitySettings {
  'channel_affinity_setting.enabled': boolean
  'channel_affinity_setting.session_mode'?: '' | SessionMode
  'channel_affinity_setting.switch_on_success': boolean
  'channel_affinity_setting.keep_on_channel_disabled': boolean
  'channel_affinity_setting.max_entries': number
  'channel_affinity_setting.default_ttl_seconds': number
  'channel_affinity_setting.rules': string
}
