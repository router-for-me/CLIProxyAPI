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
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import {
  startEmailBinding,
  resendEmailBinding,
  bindEmail,
} from '@/features/profile/api'
import type { EmailBindingFlow } from '@/features/profile/types'
import { useCountdown } from '@/hooks/use-countdown'

import { useAccountSecurity } from '../../hooks/use-account-security'

const emailBindingSchema = z.object({
  email: z
    .string()
    .email('Please enter a valid email address')
    .max(50, 'Please enter a valid email address'),
  newCode: z.string(),
  oldCode: z.string(),
})
type EmailBindingValues = z.infer<typeof emailBindingSchema>
const emptyEmailForm: EmailBindingValues = {
  email: '',
  newCode: '',
  oldCode: '',
}

interface EmailBindDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  currentEmail?: string
  onSuccess: () => void
}

export function EmailBindDialog(props: EmailBindDialogProps) {
  const { t } = useTranslation()
  const security = useAccountSecurity()
  const [flow, setFlow] = useState<EmailBindingFlow | null>(null)
  const form = useForm<EmailBindingValues>({
    resolver: zodResolver(emailBindingSchema),
    defaultValues: emptyEmailForm,
  })
  const resend = useCountdown({ initialSeconds: 60 })
  const deadline = useCountdown({ initialSeconds: 600 })
  const reset = form.reset
  const cancel = security.cancel
  const resetResend = resend.reset
  const resetDeadline = deadline.reset

  useEffect(() => {
    reset(emptyEmailForm)
    setFlow(null)
    resetResend()
    resetDeadline()
  }, [security.sessionKey, reset, resetResend, resetDeadline])
  useEffect(() => {
    if (!props.open) {
      cancel()
      reset(emptyEmailForm)
      setFlow(null)
      resetResend()
      resetDeadline()
    }
  }, [props.open, cancel, reset, resetResend, resetDeadline])

  const terminalFailure =
    security.error?.code === 'EMAIL_BINDING_LOCKED' ||
    security.error?.code === 'AUTH_FLOW_INVALID' ||
    security.error?.code === 'ACCOUNT_SECURITY_STATE_CHANGED' ||
    security.error?.code === 'EMAIL_BINDING_DELIVERY_FAILED'
  const expired = Boolean(flow && (!deadline.isActive || terminalFailure))
  const handleOpenChange = (open: boolean) => {
    if (!open) {
      security.cancel()
      form.reset(emptyEmailForm)
      setFlow(null)
      resend.reset()
      deadline.reset()
    }
    props.onOpenChange(open)
  }
  const applyFlow = (next: EmailBindingFlow) => {
    setFlow(next)
    form.setValue('email', next.email)
    form.setValue('newCode', '')
    form.setValue('oldCode', '')
    const now = Date.now() / 1000
    resend.start(Math.max(1, Math.ceil(next.resend_at - now)))
    deadline.start(Math.max(1, Math.ceil(next.expires_at - now)))
  }
  const submit = async (values: EmailBindingValues) => {
    if (!flow) {
      const email = values.email.trim().toLowerCase()
      const result = await security.run(async (signal) => {
        const proof = await security.verify(
          {
            scope: 'account.binding.bind',
            context: { provider: 'email', email },
          },
          signal
        )
        return startEmailBinding(email, proof, signal)
      })
      if (result) applyFlow(result)
      return
    }
    if (expired) return
    const newCode = values.newCode.trim()
    const oldCode = values.oldCode.trim()
    if (!/^\d{6}$/.test(newCode)) {
      form.setError(
        'newCode',
        { message: 'Enter the 6-digit email verification code.' },
        { shouldFocus: true }
      )
      return
    }
    if (flow.old_email_required && !/^\d{6}$/.test(oldCode)) {
      form.setError(
        'oldCode',
        { message: 'Enter the 6-digit email verification code.' },
        { shouldFocus: true }
      )
      return
    }
    const result = await security.run((signal) =>
      bindEmail(flow.flow_token, newCode, oldCode, signal)
    )
    if (!result) return
    toast.success(t('Email bound successfully!'))
    handleOpenChange(false)
    props.onSuccess()
  }
  const resendCodes = async () => {
    if (!flow || resend.isActive || expired) return
    const result = await security.run((signal) =>
      resendEmailBinding(flow.flow_token, signal)
    )
    if (result) {
      applyFlow(result)
      toast.success(t('Verification code sent! Please check your email.'))
    }
  }
  const restart = () => {
    security.cancel()
    setFlow(null)
    form.setValue('newCode', '')
    form.setValue('oldCode', '')
    form.clearErrors()
    resend.reset()
    deadline.reset()
  }

  return (
    <>
      <Dialog
        open={props.open && !security.showVerification}
        onOpenChange={handleOpenChange}
        title={t('Bind Email')}
        description={
          props.currentEmail
            ? t('Current email: {{email}}. Enter a new email to change.', {
                email: props.currentEmail,
              })
            : t('Bind an email address to your account.')
        }
        contentClassName='sm:max-w-md'
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => handleOpenChange(false)}
            >
              {t('Cancel')}
            </Button>
            {expired ? (
              <Button type='button' onClick={restart}>
                {t('Start again')}
              </Button>
            ) : (
              <Button
                type='submit'
                form='email-bind-form'
                disabled={security.pending}
              >
                {security.pending && (
                  <Loader2 className='size-4 animate-spin' />
                )}
                {t(flow ? 'Confirm email' : 'Continue')}
              </Button>
            )}
          </>
        }
      >
        <Form {...form}>
          <form
            id='email-bind-form'
            onSubmit={form.handleSubmit(submit)}
            className='space-y-4'
          >
            <FormField
              control={form.control}
              name='email'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Email Address')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      type='email'
                      autoComplete='email'
                      disabled={security.pending || Boolean(flow)}
                      placeholder={t('Enter your email')}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            {flow && (
              <>
                <p className='text-muted-foreground text-sm'>
                  {t(
                    flow.old_email_required
                      ? 'Confirm both your current and new email addresses to finish this change.'
                      : 'Confirm the verification code sent to your new email address.'
                  )}
                </p>
                <FormField
                  control={form.control}
                  name='newCode'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('New email verification code')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          inputMode='numeric'
                          autoComplete='one-time-code'
                          maxLength={6}
                          disabled={security.pending || expired}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                {flow.old_email_required && (
                  <FormField
                    control={form.control}
                    name='oldCode'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>
                          {t('Current email verification code')}
                        </FormLabel>
                        <p className='text-muted-foreground text-xs'>
                          {flow.current_email}
                        </p>
                        <FormControl>
                          <Input
                            {...field}
                            inputMode='numeric'
                            autoComplete='one-time-code'
                            maxLength={6}
                            disabled={security.pending || expired}
                          />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                )}
                {expired && (
                  <p role='alert' className='text-destructive text-sm'>
                    {t(
                      'Email verification has ended. Start again to continue.'
                    )}
                  </p>
                )}
                <Button
                  type='button'
                  variant='outline'
                  onClick={() => void resendCodes()}
                  disabled={security.pending || resend.isActive || expired}
                >
                  {resend.isActive
                    ? t('Resend in {{seconds}}s', {
                        seconds: resend.secondsLeft,
                      })
                    : t('Resend codes')}
                </Button>
              </>
            )}
          </form>
        </Form>
      </Dialog>
      {security.showVerification && (
        <SecureVerificationDialog {...security.verificationDialogProps} />
      )}
    </>
  )
}
