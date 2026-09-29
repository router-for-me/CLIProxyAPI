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
import { SettingsPage } from '../components/settings-page'
import type { ContentSettings, SystemOption } from '../types'
import {
  CONTENT_DEFAULT_SECTION,
  getContentSectionContent,
  getContentSectionMeta,
} from './section-registry.tsx'

const defaultContentSettings: ContentSettings = {
  'console_setting.api_info': '[]',
  'console_setting.announcements': '[]',
  'console_setting.faq': '[]',
  'console_setting.uptime_kuma_groups': '[]',
  'console_setting.api_info_enabled': true,
  'console_setting.announcements_enabled': true,
  'console_setting.faq_enabled': true,
  'console_setting.uptime_kuma_enabled': false,
  DataExportEnabled: false,
  DataExportDefaultTime: 'hour',
  DataExportInterval: 5,
  Chats: '[]',
  DrawingEnabled: false,
  MjNotifyEnabled: false,
  MjAccountFilterEnabled: false,
  MjForwardUrlEnabled: false,
  MjModeClearEnabled: false,
  MjActionCheckSuccessEnabled: false,
}

function resolveContentSettings(
  settings: ContentSettings,
  raw: SystemOption[] | undefined
): ContentSettings {
  if (!raw || raw.length === 0) return settings

  const optionMap = new Map(raw.map((item) => [item.key, item.value]))
  const next = { ...settings }

  const legacyMap = [
    { current: 'console_setting.announcements', legacy: 'Announcements' },
    { current: 'console_setting.api_info', legacy: 'ApiInfo' },
    { current: 'console_setting.faq', legacy: 'FAQ' },
  ] as const

  for (const { current, legacy } of legacyMap) {
    if (!optionMap.has(current)) {
      const legacyValue = optionMap.get(legacy)
      if (legacyValue !== undefined) {
        next[current] = legacyValue
      }
    }
  }

  if (!optionMap.has('console_setting.uptime_kuma_groups')) {
    const legacyUrl = optionMap.get('UptimeKumaUrl')
    const legacySlug = optionMap.get('UptimeKumaSlug')
    if (legacyUrl && legacySlug) {
      next['console_setting.uptime_kuma_groups'] = JSON.stringify([
        { id: 1, categoryName: 'Legacy', url: legacyUrl, slug: legacySlug },
      ])
    }
  }

  return next
}

export function ContentSettings() {
  return (
    <SettingsPage
      routePath='/_authenticated/system-settings/content/$section'
      defaultSettings={defaultContentSettings}
      defaultSection={CONTENT_DEFAULT_SECTION}
      getSectionContent={getContentSectionContent}
      getSectionMeta={getContentSectionMeta}
      loadingMessage='Loading content settings...'
      resolveSettings={resolveContentSettings}
    />
  )
}
