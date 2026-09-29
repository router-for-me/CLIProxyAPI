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
import { Loader2 } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import type { TwoFASetupData } from '../../api'

// ============================================================================
// Two-FA Setup Dialog Component
// ============================================================================

interface TwoFASetupDialogProps {
  open: boolean
  setupData: TwoFASetupData | null
  loading: boolean
  initializing: boolean
  error?: string
  onCancel: () => void
  onEnable: (code: string) => Promise<void>
}

export function TwoFASetupDialog(props: TwoFASetupDialogProps) {
  const { t } = useTranslation()
  const [step, setStep] = useState(0)
  const [code, setCode] = useState('')
  const stepLabels = [
    t('Scan QR Code'),
    t('Save Backup Codes'),
    t('Verify Setup'),
  ]
  return (
    <Dialog
      open={props.open}
      onOpenChange={(open) => {
        if (!open) props.onCancel()
      }}
      title={t('Setup Two-Factor Authentication')}
      description={
        <>
          {t('Step')}
          {step + 1}
          {t('of 3:')}
          {stepLabels[step]}
        </>
      }
      contentClassName='sm:max-w-lg'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          {step > 0 && (
            <Button
              variant='outline'
              onClick={() => setStep(step - 1)}
              disabled={props.initializing || props.loading}
            >
              {t('Back')}
            </Button>
          )}
          {step < 2 ? (
            <Button
              onClick={() => setStep(step + 1)}
              disabled={props.initializing || !props.setupData}
            >
              {t('Next')}
            </Button>
          ) : (
            <Button
              onClick={() => void props.onEnable(code)}
              disabled={props.initializing || props.loading || !code}
            >
              {props.loading && (
                <Loader2 className='mr-2 h-4 w-4 animate-spin' />
              )}
              {props.loading ? t('Enabling...') : t('Enable 2FA')}
            </Button>
          )}
        </>
      }
    >
      <div className='space-y-4 py-4'>
        {props.error && (
          <p role='alert' className='text-destructive text-sm'>
            {t(props.error)}
          </p>
        )}
        {props.initializing && (
          <div className='flex flex-col items-center justify-center gap-3 py-8'>
            <div className='border-primary h-8 w-8 animate-spin rounded-full border-4 border-t-transparent' />
            <div className='text-muted-foreground text-sm'>
              {t('Setting up 2FA...')}
            </div>
          </div>
        )}
        {!props.initializing && !props.setupData && (
          <div className='flex justify-center py-8'>
            <div className='text-muted-foreground'>
              {t('Failed to load setup data')}
            </div>
          </div>
        )}
        {!props.initializing && props.setupData && (
          <>
            {/* Step 0: QR Code */}
            {step === 0 && (
              <div className='space-y-4'>
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'Scan this QR code with your authenticator app (Google Authenticator, Microsoft Authenticator, etc.)'
                  )}
                </p>
                <div className='flex justify-center rounded-lg bg-white p-4'>
                  <QRCodeSVG value={props.setupData.qr_code_data} size={200} />
                </div>
                <div className='bg-muted rounded-lg p-3'>
                  <div className='flex items-center justify-between'>
                    <div>
                      <p className='text-muted-foreground text-xs'>
                        {t('Or enter this key manually:')}
                      </p>
                      <code className='font-mono text-sm'>
                        {props.setupData.secret}
                      </code>
                    </div>
                    <CopyButton
                      value={props.setupData.secret}
                      variant='ghost'
                      tooltip={t('Copy secret key')}
                      aria-label={t('Copy secret key')}
                    />
                  </div>
                </div>
              </div>
            )}

            {/* Step 1: Backup Codes */}
            {step === 1 && (
              <div className='space-y-4'>
                <Alert>
                  <AlertDescription>
                    {t(
                      'Save these backup codes in a safe place. Each code can only be used once.'
                    )}
                  </AlertDescription>
                </Alert>
                <div className='rounded-lg border p-4'>
                  <div className='grid grid-cols-2 gap-2'>
                    {props.setupData.backup_codes.map((code) => (
                      <div
                        key={code}
                        className='bg-muted rounded-md p-2 text-center font-mono text-sm'
                      >
                        {code}
                      </div>
                    ))}
                  </div>
                </div>
                <CopyButton
                  value={props.setupData.backup_codes.join('\n')}
                  variant='outline'
                  size='default'
                  className='w-full'
                  iconClassName='mr-2 size-4'
                  tooltip={t('Copy all backup codes')}
                  aria-label={t('Copy all backup codes')}
                >
                  {t('Copy All Codes')}
                </CopyButton>
              </div>
            )}

            {/* Step 2: Verify */}
            {step === 2 && (
              <div className='space-y-4'>
                <div className='space-y-2'>
                  <Label htmlFor='code'>{t('Verification Code')}</Label>
                  <Input
                    id='code'
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    placeholder={t('Enter 6-digit code')}
                    maxLength={6}
                    disabled={props.loading}
                  />
                  <p className='text-muted-foreground text-xs'>
                    {t('Enter the 6-digit code from your authenticator app')}
                  </p>
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </Dialog>
  )
}
