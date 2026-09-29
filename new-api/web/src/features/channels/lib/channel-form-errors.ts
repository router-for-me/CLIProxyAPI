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
import type { FieldPath } from 'react-hook-form'

import type { ChannelFormValues } from './channel-form'

type ChannelFormErrorMap = Partial<
  Record<FieldPath<ChannelFormValues>, unknown>
>

const ADVANCED_SETTINGS_FIELDS = new Set<FieldPath<ChannelFormValues>>([
  'priority',
  'weight',
  'test_model',
  'auto_ban',
  'tag',
  'remark',
  'param_override',
  'header_override',
  'status_code_mapping',
  'advanced_custom',
  'force_format',
  'thinking_to_content',
  'pass_through_body_enabled',
  'responses_websocket_enabled',
  'proxy',
  'http_protocol',
  'http2_connection_shards',
  'system_prompt',
  'system_prompt_override',
  'allow_service_tier',
  'disable_store',
  'allow_safety_identifier',
  'allow_include_obfuscation',
  'allow_inference_geo',
  'allow_speed',
  'claude_beta_query',
  'ollama_openai_chat',
  'disable_task_polling_sleep',
  'upstream_model_update_check_enabled',
  'upstream_model_update_auto_sync_enabled',
  'upstream_model_update_ignored_models',
])

export function isAdvancedSettingsField(
  fieldName: string
): fieldName is FieldPath<ChannelFormValues> {
  return ADVANCED_SETTINGS_FIELDS.has(fieldName as FieldPath<ChannelFormValues>)
}

export function hasAdvancedSettingsErrors(
  errors: ChannelFormErrorMap
): boolean {
  return Object.keys(errors).some((fieldName) =>
    isAdvancedSettingsField(fieldName)
  )
}
