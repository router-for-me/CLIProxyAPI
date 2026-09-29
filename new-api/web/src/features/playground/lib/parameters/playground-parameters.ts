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
import type { PlaygroundConfig, ParameterEnabled } from '../../types'

type ParameterValue = PlaygroundConfig[keyof PlaygroundConfig]

export type PlaygroundParameterKey = keyof ParameterEnabled

export type PlaygroundParameterControl = {
  key: PlaygroundParameterKey
  labelKey: string
  descriptionKey: string
  valueType: 'slider' | 'number'
  min: number
  max: number
  step: number
}

export const PLAYGROUND_PARAMETER_CONTROLS = [
  {
    key: 'temperature',
    labelKey: 'Temperature',
    descriptionKey: 'Controls randomness and creativity',
    valueType: 'slider',
    min: 0.1,
    max: 1,
    step: 0.1,
  },
  {
    key: 'top_p',
    labelKey: 'Top P',
    descriptionKey: 'Limits token selection to a probability mass',
    valueType: 'slider',
    min: 0.1,
    max: 1,
    step: 0.1,
  },
  {
    key: 'frequency_penalty',
    labelKey: 'Frequency Penalty',
    descriptionKey: 'Reduces repeated wording',
    valueType: 'slider',
    min: -2,
    max: 2,
    step: 0.1,
  },
  {
    key: 'presence_penalty',
    labelKey: 'Presence Penalty',
    descriptionKey: 'Encourages new topics',
    valueType: 'slider',
    min: -2,
    max: 2,
    step: 0.1,
  },
  {
    key: 'max_tokens',
    labelKey: 'Max Tokens',
    descriptionKey: 'Caps the response length',
    valueType: 'number',
    min: 0,
    max: 200000,
    step: 1,
  },
  {
    key: 'seed',
    labelKey: 'Seed',
    descriptionKey: 'Keeps compatible responses more repeatable',
    valueType: 'number',
    min: 0,
    max: 2147483647,
    step: 1,
  },
] as const satisfies readonly PlaygroundParameterControl[]

export const PLAYGROUND_PARAMETER_PANEL_SCROLL_CLASS =
  'max-h-[min(28rem,calc(100vh-10rem))] overflow-y-auto pr-1'

export function normalizeParameterNumberValue(
  key: PlaygroundParameterKey,
  value: string | number
): number | null {
  if (value === '') {
    return key === 'seed' ? null : 0
  }

  const control = PLAYGROUND_PARAMETER_CONTROLS.find((item) => item.key === key)
  const parsed = typeof value === 'number' ? value : Number.parseFloat(value)

  if (!control || Number.isNaN(parsed)) {
    return key === 'seed' ? null : 0
  }

  const clamped = Math.min(control.max, Math.max(control.min, parsed))

  if (control.step >= 1) {
    return Math.trunc(clamped)
  }

  const precision = Math.max(0, String(control.step).split('.')[1]?.length ?? 0)
  return Number(clamped.toFixed(precision))
}

export function getParameterControlValueText(
  key: PlaygroundParameterKey,
  value: ParameterValue
): string {
  if (key === 'seed' && value === null) {
    return 'Not set'
  }

  return String(value)
}
