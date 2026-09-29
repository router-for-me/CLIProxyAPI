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
import { useEffect } from 'react'
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
import { Spinner } from '@/components/ui/spinner'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { bindWeChat } from '@/features/profile/api'

import { useAccountSecurity } from '../../hooks/use-account-security'

const wechatBindingSchema = z.object({
  code: z
    .string()
    .trim()
    .min(1, 'Enter the verification code')
    .max(128, 'Invalid verification code'),
})

interface WeChatBindDialogProps {
  open: boolean
  qrCodeUrl: string
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}

export function WeChatBindDialog(props: WeChatBindDialogProps) {
  const { t } = useTranslation()
  const security = useAccountSecurity()
  const form = useForm<z.infer<typeof wechatBindingSchema>>({
    resolver: zodResolver(wechatBindingSchema),
    defaultValues: { code: '' },
  })
  const reset = form.reset
  const cancel = security.cancel
  useEffect(() => {
    reset({ code: '' })
  }, [reset, security.sessionKey])
  useEffect(() => {
    if (!props.open) {
      cancel()
      reset({ code: '' })
    }
  }, [props.open, cancel, reset])

  const handleOpenChange = (open: boolean) => {
    if (!open) {
      security.cancel()
      form.reset({ code: '' })
    }
    props.onOpenChange(open)
  }
  const submit = async (values: z.infer<typeof wechatBindingSchema>) => {
    const result = await security.run(async (signal) => {
      const proof = await security.verify(
        {
          scope: 'account.binding.bind',
          context: { provider: 'wechat', code: values.code },
        },
        signal
      )
      return bindWeChat(values.code, proof, signal)
    })
    if (!result) return
    toast.success(t('Binding successful!'))
    handleOpenChange(false)
    props.onSuccess()
  }

  return (
    <>
      <Dialog
        open={props.open && !security.showVerification}
        onOpenChange={handleOpenChange}
        title={t('Bind WeChat')}
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
            <Button
              type='submit'
              form='wechat-bind-form'
              disabled={security.pending}
            >
              {security.pending && <Spinner data-icon='inline-start' />}
              {t('Bind')}
            </Button>
          </>
        }
      >
        <Form {...form}>
          <form
            id='wechat-bind-form'
            onSubmit={form.handleSubmit(submit)}
            className='space-y-4'
          >
            {props.qrCodeUrl ? (
              <div className='flex justify-center'>
                <img
                  src={props.qrCodeUrl}
                  alt={t('WeChat login QR code')}
                  className='size-48 rounded-lg border object-contain'
                />
              </div>
            ) : (
              <p className='text-muted-foreground text-sm'>
                {t('QR code is not configured. Please contact support.')}
              </p>
            )}
            <FormField
              control={form.control}
              name='code'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Verification code')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      placeholder={t('Enter the verification code')}
                      autoComplete='one-time-code'
                      disabled={security.pending}
                      autoFocus
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
