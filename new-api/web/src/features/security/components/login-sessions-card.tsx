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
import { Logout01Icon, SmartPhone01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import {
  getLoginSessions,
  revokeLoginSession,
  revokeOtherLoginSessions,
} from '@/features/profile/api'
import { clearAuthenticatedClientState } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'
import type { LoginSession } from '@/stores/auth-store'

import { LoginSessionDialogs } from './login-session-dialogs'
import { LoginSessionItem } from './login-session-item'

const sessionQueryKey = ['profile', 'login-sessions'] as const

export function LoginSessionsCard() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [revokeTarget, setRevokeTarget] = useState<LoginSession | null>(null)
  const [confirmOthers, setConfirmOthers] = useState(false)

  const sessionsQuery = useQuery({
    queryKey: sessionQueryKey,
    queryFn: async () => {
      const response = await getLoginSessions()
      if (!response.success) {
        throw createServerError(response, t('Failed to load login sessions'))
      }
      return response.data ?? []
    },
  })

  const revokeMutation = useMutation({
    mutationFn: async (sid: string) => {
      const response = await revokeLoginSession(sid)
      if (!response.success) {
        throw createServerError(response, t('Failed to sign out session'))
      }
      return sid
    },
    onSuccess: async (sid) => {
      const revokedCurrent = sessionsQuery.data?.some(
        (session) => session.sid === sid && session.current
      )
      setRevokeTarget(null)
      if (revokedCurrent) {
        clearAuthenticatedClientState(queryClient)
        void navigate({ to: '/sign-in', replace: true })
        return
      }
      toast.success(t('Session signed out'))
      await queryClient.invalidateQueries({ queryKey: sessionQueryKey })
    },
    onError: (error: Error) => handleServerError(error),
  })

  const revokeOthersMutation = useMutation({
    mutationFn: async () => {
      const response = await revokeOtherLoginSessions()
      if (!response.success) {
        throw createServerError(
          response,
          t('Failed to sign out other sessions')
        )
      }
    },
    onSuccess: async () => {
      setConfirmOthers(false)
      toast.success(t('Other sessions signed out'))
      await queryClient.invalidateQueries({ queryKey: sessionQueryKey })
    },
    onError: (error: Error) => handleServerError(error),
  })

  const sessions = sessionsQuery.data ?? []
  const hasOtherSessions = sessions.some((session) => !session.current)
  let sessionsContent: ReactNode
  if (sessionsQuery.isLoading) {
    sessionsContent = (
      <div className='flex flex-col gap-3'>
        <Skeleton className='h-20 w-full' />
        <Skeleton className='h-20 w-full' />
      </div>
    )
  } else if (sessionsQuery.isError) {
    sessionsContent = (
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant='icon'>
            <HugeiconsIcon icon={SmartPhone01Icon} strokeWidth={2} />
          </EmptyMedia>
          <EmptyTitle>{t('Unable to load login sessions')}</EmptyTitle>
          <EmptyDescription>
            {t('Refresh the list and try again.')}
          </EmptyDescription>
        </EmptyHeader>
        <Button
          type='button'
          variant='outline'
          onClick={() => sessionsQuery.refetch()}
        >
          {t('Retry')}
        </Button>
      </Empty>
    )
  } else if (sessions.length === 0) {
    sessionsContent = (
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant='icon'>
            <HugeiconsIcon icon={SmartPhone01Icon} strokeWidth={2} />
          </EmptyMedia>
          <EmptyTitle>{t('No active login sessions')}</EmptyTitle>
        </EmptyHeader>
      </Empty>
    )
  } else {
    sessionsContent = (
      <div className='flex flex-col'>
        {sessions.map((session, index) => (
          <div key={session.sid}>
            {index > 0 && <Separator />}
            <LoginSessionItem session={session} onRevoke={setRevokeTarget} />
          </div>
        ))}
      </div>
    )
  }

  return (
    <>
      <Card data-card-hover='false'>
        <CardHeader>
          <CardTitle>{t('Login sessions')}</CardTitle>
          <CardDescription>
            {t('Review and sign out devices currently using your account.')}
          </CardDescription>
          <CardAction>
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={!hasOtherSessions || revokeOthersMutation.isPending}
              onClick={() => setConfirmOthers(true)}
            >
              <HugeiconsIcon
                icon={Logout01Icon}
                data-icon='inline-start'
                strokeWidth={2}
              />
              {t('Sign out other sessions')}
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent>{sessionsContent}</CardContent>
      </Card>

      <LoginSessionDialogs
        revokeTarget={revokeTarget}
        confirmOthers={confirmOthers}
        revoking={revokeMutation.isPending}
        revokingOthers={revokeOthersMutation.isPending}
        onRevokeTargetChange={setRevokeTarget}
        onConfirmOthersChange={setConfirmOthers}
        onRevoke={() => {
          if (revokeTarget) revokeMutation.mutate(revokeTarget.sid)
        }}
        onRevokeOthers={() => revokeOthersMutation.mutate()}
      />
    </>
  )
}
