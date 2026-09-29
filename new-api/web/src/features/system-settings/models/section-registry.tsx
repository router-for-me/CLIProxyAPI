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
import { IoNetDeploymentSettingsSection } from '../integrations/ionet-deployment-settings-section'
import type { ModelSettings } from '../types'
import { createSectionRegistry } from '../utils/section-registry'
import { ClaudeSettingsCard } from './claude-settings-card'
import { GeminiSettingsCard } from './gemini-settings-card'
import { GlobalSettingsCard } from './global-settings-card'
import { GrokSettingsCard } from './grok-settings-card'

function formatJsonForEditor(value: string, fallback: string) {
  const raw = (value ?? '').toString().trim()
  if (!raw) return fallback
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return fallback
  }
}

const MODELS_SECTIONS = [
  {
    id: 'global',
    titleKey: 'Global Model Configuration',
    build: (settings: ModelSettings) => (
      <GlobalSettingsCard
        defaultValues={{
          global: {
            pass_through_request_enabled:
              settings['global.pass_through_request_enabled'],
            thinking_model_blacklist: formatJsonForEditor(
              settings['global.thinking_model_blacklist'],
              '[]'
            ),
            chat_completions_to_responses_policy: formatJsonForEditor(
              settings['global.chat_completions_to_responses_policy'],
              '{}'
            ),
          },
          general_setting: {
            ping_interval_enabled:
              settings['general_setting.ping_interval_enabled'],
            ping_interval_seconds:
              settings['general_setting.ping_interval_seconds'],
          },
        }}
      />
    ),
  },

  {
    id: 'gemini',
    titleKey: 'Gemini',
    build: (settings: ModelSettings) => (
      <GeminiSettingsCard
        defaultValues={{
          gemini: {
            safety_settings: settings['gemini.safety_settings'],
            version_settings: settings['gemini.version_settings'],
            supported_imagine_models:
              settings['gemini.supported_imagine_models'],
            thinking_adapter_enabled:
              settings['gemini.thinking_adapter_enabled'],
            thinking_adapter_budget_tokens_percentage:
              settings['gemini.thinking_adapter_budget_tokens_percentage'],
            function_call_thought_signature_enabled:
              settings['gemini.function_call_thought_signature_enabled'],
            remove_function_response_id_enabled:
              settings['gemini.remove_function_response_id_enabled'],
          },
        }}
      />
    ),
  },
  {
    id: 'claude',
    titleKey: 'Claude',
    build: (settings: ModelSettings) => (
      <ClaudeSettingsCard
        defaultValues={{
          claude: {
            model_headers_settings: settings['claude.model_headers_settings'],
            default_max_tokens: settings['claude.default_max_tokens'],
            thinking_adapter_enabled:
              settings['claude.thinking_adapter_enabled'],
            thinking_adapter_budget_tokens_percentage:
              settings['claude.thinking_adapter_budget_tokens_percentage'],
          },
        }}
      />
    ),
  },
  {
    id: 'grok',
    titleKey: 'Grok',
    build: (settings: ModelSettings) => (
      <GrokSettingsCard
        defaultValues={{
          'grok.violation_deduction_enabled':
            settings['grok.violation_deduction_enabled'] ?? true,
          'grok.violation_deduction_amount':
            settings['grok.violation_deduction_amount'] ?? 0.05,
        }}
      />
    ),
  },

  {
    id: 'model-deployment',
    titleKey: 'Model Deployment',
    build: (settings: ModelSettings) => (
      <IoNetDeploymentSettingsSection
        defaultValues={{
          enabled: settings['model_deployment.ionet.enabled'],
          apiKey: settings['model_deployment.ionet.api_key'],
        }}
      />
    ),
  },
] as const

export type ModelSectionId = (typeof MODELS_SECTIONS)[number]['id']

const modelsRegistry = createSectionRegistry<ModelSectionId, ModelSettings>({
  sections: MODELS_SECTIONS,
  defaultSection: 'global',
  basePath: '/system-settings/models',
  urlStyle: 'path',
})

export const MODELS_SECTION_IDS = modelsRegistry.sectionIds
export const MODELS_DEFAULT_SECTION = modelsRegistry.defaultSection
export const getModelsSectionNavItems = modelsRegistry.getSectionNavItems
export const getModelsSectionContent = modelsRegistry.getSectionContent
export const getModelsSectionMeta = modelsRegistry.getSectionMeta
