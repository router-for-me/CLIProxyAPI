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

import { SetupWizard } from '@/features/setup'
import { getSetupStatus } from '@/features/setup/api'

export const Route = createFileRoute('/setup/')({
  beforeLoad: async () => {
    const status = await getSetupStatus().catch((error) => {
      if (import.meta.env.DEV) {
        // eslint-disable-next-line no-console
        console.warn('[setup.beforeLoad] failed to fetch setup status', error)
      }
      return null
    })

    if (status?.success && status.data?.status) {
      throw redirect({ to: '/' })
    }
  },
  component: SetupWizard,
})
