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
import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getUserProfile } from '@/features/profile/api'
import {
  ADMIN_PERMISSION_RESOURCES,
  ADMIN_PERMISSION_ACTIONS,
  hasPermission,
} from '@/lib/admin-permissions'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { AuditLogViewer } from './components/audit-log-viewer'

export function AuditLogs() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const queryClient = useQueryClient()
  const [scope, setScope] = useState<'all' | 'self'>('all')
  const [accessRevoked, setAccessRevoked] = useState(false)
  const canReadAll =
    !!user &&
    user.role >= ROLE.ADMIN &&
    hasPermission(
      user,
      ADMIN_PERMISSION_RESOURCES.AUDIT,
      ADMIN_PERMISSION_ACTIONS.READ
    )
  const effectiveScope = canReadAll && !accessRevoked ? scope : 'self'
  const userId = user?.id
  const handleAccessDenied = useCallback(async () => {
    setAccessRevoked(true)
    setScope('self')
    const current = useAuthStore.getState().auth.user
    if (current && current.id === userId) {
      useAuthStore.getState().auth.setUser({
        ...current,
        permissions: {
          ...current.permissions,
          admin_permissions: {
            ...current.permissions?.admin_permissions,
            audit: { read: false },
          },
        },
      })
    }
    await queryClient.cancelQueries({ queryKey: ['audit', userId, 'all'] })
    queryClient.removeQueries({ queryKey: ['audit', userId, 'all'] })
    try {
      const profile = await getUserProfile()
      const latest = useAuthStore.getState().auth.user
      if (
        profile.success &&
        profile.data &&
        latest &&
        profile.data.id === userId &&
        latest.id === userId
      ) {
        useAuthStore.getState().auth.setUser({
          ...latest,
          role: profile.data.role,
          permissions: profile.data.permissions,
        })
      }
    } catch {
      // Keep the denied scope closed when refreshing capabilities fails.
    }
  }, [queryClient, userId])
  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Audit Logs')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        {canReadAll && !accessRevoked && (
          <Tabs
            value={scope}
            onValueChange={(value) =>
              setScope(value === 'all' ? 'all' : 'self')
            }
          >
            <TabsList aria-label={t('View scope')}>
              <TabsTrigger value='all'>{t('All')}</TabsTrigger>
              <TabsTrigger value='self'>{t('Only Mine')}</TabsTrigger>
            </TabsList>
          </Tabs>
        )}
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='flex h-full min-h-0 flex-col gap-3'>
          {accessRevoked && (
            <p role='status' className='text-muted-foreground shrink-0 text-xs'>
              {t('Audit access changed. Showing only your records.')}
            </p>
          )}
          <div className='min-h-0 flex-1'>
            <AuditLogViewer
              key={`${userId}:${effectiveScope}`}
              scope={effectiveScope}
              onAccessDenied={handleAccessDenied}
            />
          </div>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
