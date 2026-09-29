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
import { createSectionRegistry } from '../utils/section-registry'
import { ChannelHealthSection } from './channel-health-section'
import type { RequestPolicySettings } from './defaults'
import { RequestChecksSection } from './request-checks-section'
import { RoutingPolicySection } from './routing-section'

const POLICY_SECTIONS = [
  {
    id: 'filtering',
    titleKey: 'Request checks',
    build: (settings: RequestPolicySettings) => (
      <RequestChecksSection
        defaultValues={{
          CheckSensitiveEnabled: settings.CheckSensitiveEnabled,
          CheckSensitiveOnPromptEnabled: settings.CheckSensitiveOnPromptEnabled,
          SensitiveWords: settings.SensitiveWords,
        }}
      />
    ),
  },
  {
    id: 'routing',
    titleKey: 'Sessions and retries',
    build: () => <RoutingPolicySection />,
  },
  {
    id: 'health',
    titleKey: 'Channel health',
    build: (settings: RequestPolicySettings) => (
      <ChannelHealthSection defaultValues={settings} />
    ),
  },
] as const

export type PolicySectionId = (typeof POLICY_SECTIONS)[number]['id']
const registry = createSectionRegistry<PolicySectionId, RequestPolicySettings>({
  sections: POLICY_SECTIONS,
  defaultSection: 'routing',
  basePath: '/system-settings/request-policies',
  urlStyle: 'path',
})
export const POLICY_SECTION_IDS = registry.sectionIds
export const getPolicySectionNavItems = registry.getSectionNavItems
export const getPolicySectionContent = registry.getSectionContent
export const getPolicySectionMeta = registry.getSectionMeta
