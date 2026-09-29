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
import type { TaskPluginOption } from '../api'
import {
  CHANNEL_TYPE_NEW_API,
  CHANNEL_TYPE_OPTIONS,
  CHANNEL_TYPE_TASK_PLUGIN,
} from '../constants'
import type { ChannelSettings } from '../types'

// These task-only legacy types are replaced by plugins, not extended by them.
export const LEGACY_TASK_PLUGIN_KEYS: Readonly<
  Partial<Record<number, string>>
> = {
  36: 'sunoapi',
  50: 'kling',
  51: 'jimeng',
  52: 'vidu',
  54: 'doubao',
  55: 'sora',
}

export function supportsChannelPluginExtensions(channelType: number): boolean {
  return (
    channelType !== CHANNEL_TYPE_TASK_PLUGIN &&
    !LEGACY_TASK_PLUGIN_KEYS[channelType] &&
    CHANNEL_TYPE_OPTIONS.some((option) => option.value === channelType)
  )
}

// A New API channel proxies an upstream gateway that may host many plugins,
// so the administrator lists the plugins the channel is extended with. The
// single task_plugin_key stays valid there and is shown together with the list.
export function readTaskExtendPluginKeys(
  channelType: number,
  setting:
    | Pick<ChannelSettings, 'task_plugin_key' | 'task_extend_plugin_keys'>
    | null
    | undefined
): string[] {
  if (channelType !== CHANNEL_TYPE_NEW_API || !setting) return []
  const keys = Array.isArray(setting.task_extend_plugin_keys)
    ? setting.task_extend_plugin_keys.filter(
        (key): key is string => typeof key === 'string' && key.trim() !== ''
      )
    : []
  const single =
    typeof setting.task_plugin_key === 'string'
      ? setting.task_plugin_key.trim()
      : ''
  return [...new Set(single ? [single, ...keys] : keys)]
}

// A New API channel proxies another gateway, which serves a plugin's wire
// format only on the plugin's own native routes. Only drivers that declare
// they address such a gateway can be bound there; the server rejects the rest.
export function supportsNewAPIUpstream(
  plugin: Pick<TaskPluginOption, 'upstreams'>
): boolean {
  return plugin.upstreams?.includes('new_api') ?? false
}

export function getChannelPluginExtensions(
  channelType: number,
  plugins: TaskPluginOption[],
  extendPluginKeys: readonly string[] = []
): TaskPluginOption[] {
  if (!supportsChannelPluginExtensions(channelType)) return []
  if (channelType === CHANNEL_TYPE_NEW_API) {
    return plugins.filter((plugin) => extendPluginKeys.includes(plugin.key))
  }
  return plugins.filter((plugin) => plugin.channelTypes?.includes(channelType))
}
