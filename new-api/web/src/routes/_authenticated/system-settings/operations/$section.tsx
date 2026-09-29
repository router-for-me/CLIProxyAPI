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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { OperationsSettings } from '@/features/system-settings/operations'
import {
  OPERATIONS_DEFAULT_SECTION,
  OPERATIONS_SECTION_IDS,
} from '@/features/system-settings/operations/section-registry.tsx'

export const Route = createFileRoute(
  '/_authenticated/system-settings/operations/$section'
)({
  beforeLoad: ({ params }) => {
    if (params.section === 'monitoring') {
      throw redirect({
        to: '/system-settings/request-policies/$section',
        params: { section: 'health' },
        replace: true,
      })
    }

    const validSections = OPERATIONS_SECTION_IDS as unknown as string[]
    if (!validSections.includes(params.section)) {
      throw redirect({
        to: '/system-settings/operations/$section',
        params: { section: OPERATIONS_DEFAULT_SECTION },
      })
    }
  },
  component: OperationsSettings,
})
