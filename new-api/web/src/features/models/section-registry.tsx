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
 * Models page section definitions
 */
const MODELS_SECTIONS = [
  {
    id: 'metadata',
    titleKey: 'Models',
    build: () => null, // Content is rendered directly in the page component
  },
  {
    id: 'vendors',
    titleKey: 'Vendors',
    build: () => null,
  },
  {
    id: 'deployments',
    titleKey: 'Deployments',
    build: () => null, // Content is rendered directly in the page component
  },
] as const

export type ModelsSectionId = (typeof MODELS_SECTIONS)[number]['id']

const modelsRegistry = createSectionRegistry<
  ModelsSectionId,
  Record<string, never>,
  []
>({
  sections: MODELS_SECTIONS,
  defaultSection: 'metadata',
  basePath: '/models',
  urlStyle: 'path',
})

export const MODELS_SECTION_IDS = modelsRegistry.sectionIds
export const MODELS_DEFAULT_SECTION = modelsRegistry.defaultSection
export const getModelsSectionNavItems = modelsRegistry.getSectionNavItems
