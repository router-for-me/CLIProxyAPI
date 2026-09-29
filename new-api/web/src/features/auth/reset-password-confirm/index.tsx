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
import { useNavigate } from '@tanstack/react-router'
import { CheckIcon, CopyIcon } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCountdown } from '@/hooks/use-countdown'
import { api } from '@/lib/api'
import { copyToClipboard } from '@/lib/copy-to-clipboard'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import { createServerError } from '@/lib/server-error-message'

import { AuthLayout } from '../auth-layout'

export type ResetPasswordSearchParams = {
  email?: string
  token?: string
}

type ResetPasswordConfirmProps = ResetPasswordSearchParams

export function ResetPasswordConfirm({
  email,
  token,
}: ResetPasswordConfirmProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [newPassword, setNewPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [copied, setCopied] = useState(false)
  const {
    secondsLeft,
    isActive,
    start: startCountdown,
  } = useCountdown({ initialSeconds: 30 })

  const isValidResetLink = Boolean(email && token)

  async function handleSubmit() {
    if (!isValidResetLink || !email || !token) {
      toast.error(t('Invalid reset link, please request a new password reset'))
      return
    }

    startCountdown()
    setLoading(true)
    try {
      const res = await api.post('/api/user/reset', { email, token }, {
        skipBusinessError: true,
      } as Record<string, unknown>)

      if (res?.data?.success) {
        const password = res.data.data
        setNewPassword(password)
        const copySuccess = await copyToClipboard(password)
        if (copySuccess) {
          toast.success(
            t('Password reset and copied to clipboard: {{password}}', {
              password,
            })
          )
        } else {
          toast.success(t('Password reset: {{password}}', { password }))
        }
      } else {
        handleServerError(createServerError(res.data, t('Request failed')))
      }
    } catch (error) {
      handleServerError(AuthOperationError.from(error))
    } finally {
      setLoading(false)
    }
  }

  async function handleCopy() {
    if (!newPassword) return

    const copySuccess = await copyToClipboard(newPassword)
    if (copySuccess) {
      setCopied(true)
      toast.success(
        t('Password copied to clipboard: {{password}}', {
          password: newPassword,
        })
      )
      setTimeout(() => setCopied(false), 2000)
    }
  }

  return (
    <AuthLayout>
      <div className='w-full space-y-8'>
        <div className='space-y-2'>
          <h2 className='text-center text-2xl font-semibold tracking-tight sm:text-left'>
            {t('Reset password')}
          </h2>
          <p className='text-muted-foreground text-left text-sm sm:text-base'>
            {newPassword
              ? t('auth.resetPasswordConfirm.success')
              : t('auth.resetPasswordConfirm.description')}
          </p>
        </div>

        <div className='space-y-4'>
          {!isValidResetLink && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t('Invalid reset link, please request a new password reset.')}
              </AlertDescription>
            </Alert>
          )}

          <div className='space-y-2'>
            <Label htmlFor='email'>{t('Email')}</Label>
            <Input
              id='email'
              type='email'
              value={email || ''}
              disabled
              placeholder={t('Waiting for email...')}
            />
          </div>

          {newPassword && (
            <div className='space-y-2'>
              <Label htmlFor='password'>{t('New password')}</Label>
              <div className='flex gap-2'>
                <Input
                  id='password'
                  value={newPassword}
                  disabled
                  className='font-mono'
                />
                <Button
                  type='button'
                  size='icon'
                  variant='outline'
                  onClick={handleCopy}
                >
                  {copied ? (
                    <CheckIcon className='h-4 w-4' />
                  ) : (
                    <CopyIcon className='h-4 w-4' />
                  )}
                </Button>
              </div>
              <p className='text-muted-foreground text-xs'>
                {t('Password has been copied to clipboard')}
              </p>
            </div>
          )}

          <Button
            className='w-full'
            onClick={
              newPassword
                ? () => navigate({ to: '/sign-in', replace: true })
                : handleSubmit
            }
            disabled={
              newPassword ? false : loading || isActive || !isValidResetLink
            }
          >
            {newPassword && t('auth.resetPasswordConfirm.backToLogin')}
            {!newPassword &&
              isActive &&
              t('auth.resetPasswordConfirm.retry', {
                seconds: secondsLeft,
              })}
            {!newPassword &&
              !isActive &&
              t('auth.resetPasswordConfirm.confirm')}
          </Button>

          {!newPassword && (
            <Button
              variant='link'
              className='w-full'
              onClick={() => navigate({ to: '/sign-in', replace: true })}
            >
              {t('Back to login')}
            </Button>
          )}
        </div>
      </div>
    </AuthLayout>
  )
}
