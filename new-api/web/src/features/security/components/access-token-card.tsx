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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { AuditLogViewer } from '@/features/usage-logs/audit/components/audit-log-viewer'
import dayjs from '@/lib/dayjs'

import { useAccessToken } from '../hooks/use-access-token'
import { AccessTokenDialog } from './dialogs/access-token-dialog'

export function AccessTokenCard() {
  const { t } = useTranslation()
  const access = useAccessToken()
  const [confirmation, setConfirmation] = useState<'rotate' | 'revoke' | null>(
    null
  )
  const [historyOpen, setHistoryOpen] = useState(false)
  const pending = access.pending
  const status = access.status.data
  const ready = !access.status.isError && !access.status.isPending && !!status
  let lastUsed = t('Unknown')
  if (status?.last_used_at) {
    lastUsed = dayjs.unix(status.last_used_at).format('YYYY-MM-DD HH:mm:ss')
  } else if (status?.created_at) lastUsed = t('Not used yet')
  const confirm = () => {
    if (pending || !confirmation) return
    const operation = confirmation
    setConfirmation(null)
    if (operation === 'revoke') void access.revoke()
    else void access.generate()
  }
  return (
    <>
      <Card data-card-hover='false' className='gap-3 p-3 sm:p-4'>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <h4 className='text-sm font-semibold'>{t('Access Token')}</h4>
          <Button
            size='sm'
            variant='outline'
            onClick={() => setHistoryOpen(true)}
          >
            {t('Access records')}
          </Button>
        </div>
        {access.status.isPending && (
          <p role='status' className='text-muted-foreground text-xs'>
            {t('Loading...')}
          </p>
        )}
        {access.status.isError && (
          <div role='alert' className='flex flex-wrap items-center gap-2'>
            <span className='text-destructive text-sm'>
              {t('Failed to load token status')}
            </span>
            <Button
              size='sm'
              variant='outline'
              disabled={access.status.isFetching}
              onClick={() => void access.status.refetch()}
            >
              {t('Retry')}
            </Button>
          </div>
        )}
        {ready && (
          <>
            <dl className='grid grid-cols-2 gap-x-4 gap-y-2 text-xs sm:grid-cols-4'>
              <div>
                <dt className='text-muted-foreground'>{t('Status')}</dt>
                <dd className='mt-1 font-medium'>
                  {status.exists ? t('Generated') : t('Not generated')}
                </dd>
              </div>
              {status.exists && (
                <>
                  <div>
                    <dt className='text-muted-foreground'>{t('Created At')}</dt>
                    <dd className='mt-1'>
                      {status.created_at
                        ? dayjs
                            .unix(status.created_at)
                            .format('YYYY-MM-DD HH:mm:ss')
                        : t('Unknown')}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground'>{t('Last used')}</dt>
                    <dd className='mt-1'>{lastUsed}</dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground'>
                      {t('Last used IP')}
                    </dt>
                    <dd className='mt-1 break-all'>
                      {status.last_used_ip || '—'}
                    </dd>
                  </div>
                </>
              )}
            </dl>
            <div className='flex flex-wrap justify-end gap-2'>
              {status.exists ? (
                <>
                  <Button
                    size='sm'
                    variant='outline'
                    disabled={pending}
                    onClick={() => setConfirmation('rotate')}
                  >
                    {t('Regenerate')}
                  </Button>
                  <Button
                    size='sm'
                    variant='destructive'
                    disabled={pending}
                    onClick={() => setConfirmation('revoke')}
                  >
                    {t('Revoke')}
                  </Button>
                </>
              ) : (
                <Button
                  size='sm'
                  disabled={pending}
                  onClick={() => void access.generate()}
                >
                  {t('Generate')}
                </Button>
              )}
            </div>
          </>
        )}
      </Card>
      {access.token && (
        <AccessTokenDialog token={access.token} onClose={access.clearToken} />
      )}
      <SecureVerificationDialog {...access.verificationDialogProps} />
      <ConfirmDialog
        open={confirmation !== null}
        onOpenChange={(open) => {
          if (!open && !pending) setConfirmation(null)
        }}
        title={
          confirmation === 'revoke'
            ? t('Revoke access token?')
            : t('Regenerate access token?')
        }
        desc={t(
          'This will immediately invalidate your existing access token. Any applications or scripts using it will stop working.'
        )}
        confirmText={
          confirmation === 'revoke' ? t('Revoke') : t('Regenerate token')
        }
        destructive
        isLoading={pending}
        handleConfirm={() => void confirm()}
      />
      <Sheet open={historyOpen} onOpenChange={setHistoryOpen}>
        <SheetContent className='w-full sm:max-w-5xl' showCloseButton={false}>
          <SheetHeader className='border-b pr-20'>
            <SheetTitle>{t('Access records')}</SheetTitle>
            <SheetDescription>
              {t(
                'Audit records start after this feature was enabled. Earlier records remain in Common Logs.'
              )}
            </SheetDescription>
          </SheetHeader>
          <SheetClose
            render={
              <Button
                size='sm'
                variant='ghost'
                className='absolute top-3 right-3'
              />
            }
          >
            {t('Close')}
          </SheetClose>
          <div className='min-h-0 flex-1 px-4 pb-4'>
            {historyOpen && (
              <AuditLogViewer
                scope='self'
                accessOnly
                currentTokenRef={ready ? status.token_ref : undefined}
              />
            )}
          </div>
        </SheetContent>
      </Sheet>
    </>
  )
}
