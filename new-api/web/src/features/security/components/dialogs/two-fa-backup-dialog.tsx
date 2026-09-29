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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { regenerate2FABackupCodes } from '@/lib/api'

import { useAccountSecurity } from '../../hooks/use-account-security'

interface TwoFABackupDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}

export function TwoFABackupDialog(props: TwoFABackupDialogProps) {
  const { t } = useTranslation()
  const [backupCodes, setBackupCodes] = useState<string[]>([])
  const security = useAccountSecurity()
  const cancel = security.cancel

  useEffect(() => {
    setBackupCodes([])
    if (!props.open) cancel()
  }, [props.open, security.sessionKey, cancel])

  const handleOpenChange = (open: boolean) => {
    const changed = !open && backupCodes.length > 0
    if (!open) {
      security.cancel()
      setBackupCodes([])
    }
    props.onOpenChange(open)
    if (changed) props.onSuccess()
  }
  const handleRegenerate = async () => {
    const result = await security.run(async (signal) => {
      const proof = await security.verify(
        { scope: '2fa.backup_codes.regenerate' },
        signal
      )
      return regenerate2FABackupCodes(proof, signal)
    })
    if (!result) return
    setBackupCodes(result.backup_codes)
    toast.success(t('Backup codes regenerated successfully'))
  }

  return (
    <>
      <ConfirmDialog
        open={
          props.open && backupCodes.length === 0 && !security.showVerification
        }
        onOpenChange={handleOpenChange}
        title={t('Regenerate Backup Codes')}
        desc={t(
          'Generating new codes will invalidate all existing backup codes.'
        )}
        confirmText={t('Generate New Codes')}
        isLoading={security.pending}
        handleConfirm={handleRegenerate}
      />
      <Dialog
        open={props.open && backupCodes.length > 0}
        onOpenChange={handleOpenChange}
        title={t('Regenerate Backup Codes')}
        description={t('Your new backup codes are ready')}
        contentClassName='sm:max-w-md'
        contentHeight='auto'
        bodyClassName='space-y-4'
        footer={
          <Button onClick={() => handleOpenChange(false)}>{t('Done')}</Button>
        }
      >
        <Alert>
          <AlertDescription>
            {t(
              'Save these codes in a safe place. Each code can only be used once.'
            )}
          </AlertDescription>
        </Alert>
        <div className='grid grid-cols-2 gap-2 rounded-lg border p-4'>
          {backupCodes.map((code) => (
            <div
              key={code}
              className='bg-muted rounded-md p-2 text-center font-mono text-sm'
            >
              {code}
            </div>
          ))}
        </div>
        <CopyButton
          value={backupCodes.join('\n')}
          variant='outline'
          size='default'
          className='w-full'
          iconClassName='mr-2 size-4'
          tooltip={t('Copy all backup codes')}
          aria-label={t('Copy all backup codes')}
        >
          {t('Copy All Codes')}
        </CopyButton>
      </Dialog>
      <SecureVerificationDialog {...security.verificationDialogProps} />
    </>
  )
}
