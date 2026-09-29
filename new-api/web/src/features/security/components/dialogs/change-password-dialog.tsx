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
import { zodResolver } from '@hookform/resolvers/zod'
import { Loader2 } from 'lucide-react'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { changeAccountPassword } from '@/features/profile/api'
import { accountPasswordSchema } from '@/lib/password-policy'

import { useAccountSecurity } from '../../hooks/use-account-security'

const passwordChangeSchema = z
  .object({
    originalPassword: z.string(),
    newPassword: accountPasswordSchema,
    confirmPassword: z.string(),
  })
  .refine((data) => data.newPassword === data.confirmPassword, {
    message: 'Passwords do not match',
    path: ['confirmPassword'],
  })
type PasswordChangeValues = z.infer<typeof passwordChangeSchema>
const emptyPasswordForm: PasswordChangeValues = {
  originalPassword: '',
  newPassword: '',
  confirmPassword: '',
}

interface ChangePasswordDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  username: string
  hasPassword?: boolean
  onSuccess?: () => void
}

export function ChangePasswordDialog(props: ChangePasswordDialogProps) {
  const { t } = useTranslation()
  const security = useAccountSecurity()
  const hasPassword = props.hasPassword !== false
  const form = useForm<PasswordChangeValues>({
    resolver: zodResolver(passwordChangeSchema),
    defaultValues: emptyPasswordForm,
  })
  const reset = form.reset
  const cancel = security.cancel

  useEffect(() => {
    reset(emptyPasswordForm)
  }, [reset, security.sessionKey, hasPassword])
  useEffect(() => {
    if (!props.open) {
      cancel()
      reset(emptyPasswordForm)
    }
  }, [props.open, cancel, reset])

  const handleOpenChange = (open: boolean) => {
    if (!open) {
      security.cancel()
      form.reset(emptyPasswordForm)
    }
    props.onOpenChange(open)
  }
  const submit = async (values: PasswordChangeValues) => {
    if (hasPassword && !values.originalPassword) {
      form.setError(
        'originalPassword',
        { message: 'Please enter your current password' },
        { shouldFocus: true }
      )
      return
    }
    if (hasPassword && values.originalPassword === values.newPassword) {
      form.setError(
        'newPassword',
        { message: 'New password must be different from current password' },
        { shouldFocus: true }
      )
      return
    }
    const result = await security.run(async (signal) => {
      const proof = await security.verify(
        {
          scope: hasPassword
            ? 'account.password.change'
            : 'account.password.set',
        },
        signal,
        hasPassword ? values.originalPassword : undefined
      )
      return changeAccountPassword(
        {
          password: values.newPassword,
          ...(hasPassword
            ? { original_password: values.originalPassword }
            : {}),
        },
        proof,
        signal
      )
    })
    if (!result) return
    form.reset(emptyPasswordForm)
    toast.success(
      t(
        hasPassword
          ? 'Password changed successfully'
          : 'Password set successfully'
      )
    )
    props.onOpenChange(false)
    props.onSuccess?.()
  }
  const title = t(hasPassword ? 'Change Password' : 'Set Password')

  return (
    <>
      <Dialog
        open={props.open && !security.showVerification}
        onOpenChange={handleOpenChange}
        title={title}
        description={
          <>
            {t('Update your password for account:')}{' '}
            <strong>{props.username}</strong>
          </>
        }
        contentClassName='sm:max-w-md'
        contentHeight='auto'
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => handleOpenChange(false)}
            >
              {t('Cancel')}
            </Button>
            <Button
              type='submit'
              form='change-password-form'
              disabled={security.pending}
            >
              {security.pending && <Loader2 className='size-4 animate-spin' />}
              {title}
            </Button>
          </>
        }
      >
        <Form {...form}>
          <form
            id='change-password-form'
            onSubmit={form.handleSubmit(submit)}
            className='space-y-4'
          >
            {hasPassword && (
              <FormField
                control={form.control}
                name='originalPassword'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Current Password')}</FormLabel>
                    <FormControl>
                      <PasswordInput
                        {...field}
                        autoComplete='current-password'
                        disabled={security.pending}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}
            <FormField
              control={form.control}
              name='newPassword'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('New Password')}</FormLabel>
                  <FormControl>
                    <PasswordInput
                      {...field}
                      autoComplete='new-password'
                      disabled={security.pending}
                    />
                  </FormControl>
                  <p className='text-muted-foreground text-xs'>
                    {t('Use 8–128 characters.')}
                  </p>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='confirmPassword'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Confirm New Password')}</FormLabel>
                  <FormControl>
                    <PasswordInput
                      {...field}
                      autoComplete='new-password'
                      disabled={security.pending}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </form>
        </Form>
      </Dialog>
      {security.showVerification && (
        <SecureVerificationDialog {...security.verificationDialogProps} />
      )}
    </>
  )
}
