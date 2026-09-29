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
import { createSectionRegistry } from '@/features/system-settings/utils/section-registry'

/**
 * Usage logs page section definitions
 */
const USAGE_LOGS_SECTIONS = [
  {
    id: 'common',
    titleKey: 'Common Logs',
    build: () => null, // Content is rendered directly in the page component
  },
  {
    id: 'drawing',
    titleKey: 'Drawing Logs',
    build: () => null, // Content is rendered directly in the page component
  },
  {
    id: 'task',
    titleKey: 'Task Logs',
    build: () => null, // Content is rendered directly in the page component
  },
] as const

export type UsageLogsSectionId = (typeof USAGE_LOGS_SECTIONS)[number]['id']

const usageLogsRegistry = createSectionRegistry<
  UsageLogsSectionId,
  Record<string, never>,
  []
>({
  sections: USAGE_LOGS_SECTIONS,
  defaultSection: 'common',
  basePath: '/usage-logs',
  urlStyle: 'path',
})

export const USAGE_LOGS_SECTION_IDS = usageLogsRegistry.sectionIds
export const USAGE_LOGS_DEFAULT_SECTION = usageLogsRegistry.defaultSection

/** Type guard for validating section IDs without casting. Use with z.string().refine() or params checks. */
export function isUsageLogsSectionId(s: string): s is UsageLogsSectionId {
  return (USAGE_LOGS_SECTION_IDS as readonly string[]).includes(s)
}
export const getUsageLogsSectionNavItems = usageLogsRegistry.getSectionNavItems
