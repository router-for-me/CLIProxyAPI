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
import type { TFunction } from 'i18next'
import type { ReactNode } from 'react'

/**
 * Section definition for settings pages
 */
export type SectionDefinition<TSettings, TExtraArgs extends unknown[] = []> = {
  id: string
  titleKey: string
  build: (settings: TSettings, ...extraArgs: TExtraArgs) => ReactNode
}

/**
 * Section registry configuration
 */
export type SectionRegistryConfig<
  TSectionId extends string,
  TSettings,
  TExtraArgs extends unknown[] = [],
> = {
  sections: readonly SectionDefinition<TSettings, TExtraArgs>[]
  defaultSection: TSectionId
  basePath: string
  /** 'query' = `${basePath}?section=${id}`, 'path' = `${basePath}/${id}` */
  urlStyle?: 'query' | 'path'
}

/**
 * Create a section registry with helper functions
 */
export function createSectionRegistry<
  TSectionId extends string,
  TSettings,
  TExtraArgs extends unknown[] = [],
>(config: SectionRegistryConfig<TSectionId, TSettings, TExtraArgs>) {
  const { sections, defaultSection, basePath, urlStyle = 'query' } = config

  type SectionId = TSectionId

  const sectionIds = sections.map((section) => section.id) as [
    SectionId,
    ...SectionId[],
  ]

  /**
   * Get navigation items for sidebar
   */
  function getSectionNavItems(t: TFunction) {
    return sections.map((section) => ({
      title: t(section.titleKey),
      url:
        urlStyle === 'path'
          ? `${basePath}/${section.id}`
          : `${basePath}?section=${section.id}`,
    }))
  }

  /**
   * Get section content by section ID
   */
  function getSectionContent(
    sectionId: SectionId,
    settings: TSettings,
    ...extraArgs: TExtraArgs
  ) {
    return getSectionMeta(sectionId).build(settings, ...extraArgs)
  }

  function getSectionMeta(sectionId: SectionId) {
    const section =
      sections.find((item) => item.id === sectionId) ?? sections[0]
    return section
  }

  return {
    sectionIds,
    defaultSection,
    getSectionNavItems,
    getSectionContent,
    getSectionMeta,
  }
}
