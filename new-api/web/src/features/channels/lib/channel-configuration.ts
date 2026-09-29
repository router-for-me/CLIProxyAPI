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
import type { FieldErrors, FieldPath } from 'react-hook-form'

import {
  CHANNEL_TYPE_OLLAMA,
  CLAUDE_FIELD_PASSTHROUGH_TYPES,
  MODEL_FETCHABLE_TYPES,
  OPENAI_FIELD_PASSTHROUGH_TYPES,
} from '../constants'
import { CHANNEL_TYPE_ADVANCED_CUSTOM } from './advanced-custom'
import { channelFormSchema, type ChannelFormValues } from './channel-form'
import { supportsResponsesWebSocket } from './responses-websocket'

export type ChannelProviderTarget =
  | { kind: 'builtin'; type: number }
  | { kind: 'plugin'; key: string }

export type ChannelConfigurationSection =
  | 'connection'
  | 'routing'
  | 'request'
  | 'other'

export type ChannelConfigurationStatus =
  | 'idle'
  | 'configured'
  | 'ready'
  | 'error'

const CONFIGURATION_BLOCKS = {
  modelMapping: { section: 'routing', fields: ['model_mapping'] },
  routingStrategy: {
    section: 'routing',
    fields: ['priority', 'weight', 'test_model', 'auto_ban'],
  },
  overrideRules: {
    section: 'request',
    fields: ['status_code_mapping', 'param_override', 'header_override'],
  },
  requestProcessing: {
    section: 'request',
    fields: [
      'force_format',
      'thinking_to_content',
      'pass_through_body_enabled',
      'responses_websocket_enabled',
      'ollama_openai_chat',
      'system_prompt',
      'system_prompt_override',
    ],
  },
  fieldPassthrough: {
    section: 'request',
    fields: [
      'allow_service_tier',
      'disable_store',
      'allow_safety_identifier',
      'allow_include_obfuscation',
      'allow_inference_geo',
      'allow_speed',
      'claude_beta_query',
    ],
  },
  extraSettings: {
    section: 'other',
    fields: [
      'proxy',
      'http_protocol',
      'http2_connection_shards',
      'disable_task_polling_sleep',
    ],
  },
  upstreamModelDetection: {
    section: 'other',
    fields: [
      'upstream_model_update_check_enabled',
      'upstream_model_update_auto_sync_enabled',
      'upstream_model_update_ignored_models',
    ],
  },
  internalNotes: { section: 'other', fields: ['tag', 'remark'] },
} as const satisfies Record<
  string,
  {
    section: ChannelConfigurationSection
    fields: readonly FieldPath<ChannelFormValues>[]
  }
>

export type ChannelConfigurationBlock = keyof typeof CONFIGURATION_BLOCKS

export function getChannelConfigurationSection(
  field: string
): ChannelConfigurationSection {
  for (const block of Object.values(CONFIGURATION_BLOCKS)) {
    if ((block.fields as readonly string[]).includes(field)) {
      return block.section
    }
  }
  return 'connection'
}

function hasConfiguredJson(value: string | undefined): boolean {
  const trimmed = value?.trim()
  if (!trimmed || trimmed === 'null') return false
  try {
    const parsed = JSON.parse(trimmed)
    if (Array.isArray(parsed)) return parsed.length > 0
    if (typeof parsed === 'object') return Object.keys(parsed).length > 0
  } catch {
    // Preserve the presence of invalid input; validation errors take precedence.
  }
  return true
}

export function getChannelConfigurationState(
  values: ChannelFormValues,
  errors: FieldErrors<ChannelFormValues>,
  isEditing: boolean
) {
  const openaiPassthrough = OPENAI_FIELD_PASSTHROUGH_TYPES.has(values.type)
  const claudePassthrough = CLAUDE_FIELD_PASSTHROUGH_TYPES.has(values.type)
  const configured: Record<ChannelConfigurationBlock, boolean> = {
    modelMapping: hasConfiguredJson(values.model_mapping),
    routingStrategy: Boolean(
      values.priority ||
      values.weight ||
      values.test_model?.trim() ||
      (values.auto_ban ?? 1) !== 1
    ),
    overrideRules:
      hasConfiguredJson(values.status_code_mapping) ||
      hasConfiguredJson(values.param_override) ||
      hasConfiguredJson(values.header_override),
    requestProcessing: Boolean(
      (values.type === 1 && values.force_format) ||
      values.thinking_to_content ||
      (values.type !== CHANNEL_TYPE_ADVANCED_CUSTOM &&
        values.pass_through_body_enabled) ||
      (supportsResponsesWebSocket(values.type) &&
        values.responses_websocket_enabled) ||
      (values.type === CHANNEL_TYPE_OLLAMA && values.ollama_openai_chat) ||
      values.system_prompt?.trim() ||
      values.system_prompt_override
    ),
    fieldPassthrough: Boolean(
      ((openaiPassthrough || claudePassthrough) &&
        (values.allow_service_tier || values.allow_inference_geo)) ||
      (openaiPassthrough &&
        (values.disable_store ||
          values.allow_safety_identifier ||
          values.allow_include_obfuscation)) ||
      (claudePassthrough &&
        (values.allow_speed ||
          (values.type === 14 && values.claude_beta_query)))
    ),
    extraSettings: Boolean(
      values.proxy?.trim() ||
      (values.http_protocol && values.http_protocol !== 'auto') ||
      (values.http2_connection_shards ?? 1) > 1 ||
      values.disable_task_polling_sleep
    ),
    upstreamModelDetection:
      MODEL_FETCHABLE_TYPES.has(values.type) &&
      Boolean(
        values.upstream_model_update_check_enabled ||
        values.upstream_model_update_auto_sync_enabled ||
        values.upstream_model_update_ignored_models?.trim()
      ),
    internalNotes: Boolean(values.tag?.trim() || values.remark?.trim()),
  }
  const blocks = {} as Record<
    ChannelConfigurationBlock,
    ChannelConfigurationStatus
  >
  const sections: Record<
    ChannelConfigurationSection,
    ChannelConfigurationStatus
  > = {
    connection: 'idle',
    routing: 'idle',
    request: 'idle',
    other: 'idle',
  }
  for (const id of Object.keys(
    CONFIGURATION_BLOCKS
  ) as ChannelConfigurationBlock[]) {
    const block = CONFIGURATION_BLOCKS[id]
    const hasError = block.fields.some((field) => Boolean(errors[field]))
    blocks[id] = configured[id] ? 'configured' : 'idle'
    if (hasError) blocks[id] = 'error'
    if (hasError || (sections[block.section] !== 'error' && configured[id])) {
      sections[block.section] = blocks[id]
    }
  }

  // Reuse provider-specific validation without displaying errors before submit.
  const validation = channelFormSchema.safeParse(values)
  const connectionInvalid =
    !validation.success &&
    validation.error.issues.some(
      (issue) =>
        getChannelConfigurationSection(String(issue.path[0])) === 'connection'
    )
  if (
    !connectionInvalid &&
    values.name.trim() &&
    values.type > 0 &&
    values.models.trim() &&
    (isEditing || values.key?.trim())
  ) {
    sections.connection = 'ready'
  }
  if (
    Object.keys(errors).some(
      (field) => getChannelConfigurationSection(field) === 'connection'
    )
  ) {
    sections.connection = 'error'
  }
  return { blocks, sections }
}
