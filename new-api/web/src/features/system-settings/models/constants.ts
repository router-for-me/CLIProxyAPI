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
export const DEFAULT_ENDPOINT = '/api/pricing'

// ---------------------------------------------------------------------------
// Built-in upstream ratio presets
//
// The backend (`controller/ratio_sync.go`) synthesizes two virtual channels and
// returns them in the syncable channels response. The constants below mirror
// the backend literals one-to-one; do NOT translate the *_NAME values because
// they are wire-protocol identifiers, not user-facing labels.
//
// Identification on the frontend should rely on the stable negative ID alone.
// `*_NAME` and `*_BASE_URL` are kept for diagnostics and custom channel
// detection.
// ---------------------------------------------------------------------------

export const OFFICIAL_CHANNEL_ID = -100
export const OFFICIAL_CHANNEL_NAME = '官方倍率预设'
export const OFFICIAL_CHANNEL_BASE_URL = 'https://basellm.github.io'
export const OFFICIAL_CHANNEL_ENDPOINT =
  '/llm-metadata/api/newapi/ratio_config-v1-base.json'

export const MODELS_DEV_PRESET_ID = -101
export const MODELS_DEV_PRESET_NAME = 'models.dev 价格预设'
export const MODELS_DEV_PRESET_BASE_URL = 'https://models.dev'
export const MODELS_DEV_PRESET_ENDPOINT = 'https://models.dev/api.json'

export const OPENROUTER_ENDPOINT = 'openrouter'

// Backend channel type for OpenRouter (see constant/channel.go: ChannelTypeOpenRouter = 20)
export const OPENROUTER_CHANNEL_TYPE = 20

export const ENDPOINT_OPTIONS = [
  { label: 'pricing', value: '/api/pricing' },
  { label: 'ratio_config', value: '/api/ratio_config' },
  { label: 'OpenRouter', value: OPENROUTER_ENDPOINT },
  { label: 'custom', value: 'custom' },
] as const

// Labels reuse the existing sentence-case i18n keys defined for form fields
// (e.g. `Model ratio`, `Audio completion ratio`). Do NOT switch to Title Case
// here without updating the i18n catalog; otherwise we end up with two keys per
// ratio type that only differ in capitalization.
export const RATIO_TYPE_OPTIONS = [
  { label: 'Model ratio', value: 'model_ratio' },
  { label: 'Completion ratio', value: 'completion_ratio' },
  { label: 'Cache ratio', value: 'cache_ratio' },
  { label: 'Create cache ratio', value: 'create_cache_ratio' },
  { label: 'Image ratio', value: 'image_ratio' },
  { label: 'Audio ratio', value: 'audio_ratio' },
  { label: 'Audio completion ratio', value: 'audio_completion_ratio' },
  { label: 'Fixed price', value: 'model_price' },
  { label: 'Expression billing', value: 'billing_expr' },
] as const

export const CHANNEL_STATUS_CONFIG = {
  1: { label: 'Enabled', variant: 'success' as const },
  2: { label: 'Disabled', variant: 'danger' as const },
  3: { label: 'Auto Disabled', variant: 'warning' as const },
} as const
