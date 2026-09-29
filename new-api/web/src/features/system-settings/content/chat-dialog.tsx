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
import * as z from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

const createChatDialogSchema = (t: (key: string) => string) =>
  z.object({
    name: z.string().min(1, t('Chat client name is required')),
    url: z.string().min(1, t('URL is required')),
  })

type ChatDialogFormValues = z.infer<ReturnType<typeof createChatDialogSchema>>

const CHAT_DIALOG_FORM_ID = 'chat-dialog-form'

export type ChatEntryData = {
  name: string
  url: string
}

type ChatDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSave: (data: ChatEntryData) => void
  editData?: ChatEntryData | null
}

export function ChatDialog({
  open,
  onOpenChange,
  onSave,
  editData,
}: ChatDialogProps) {
  const { t } = useTranslation()
  const isEditMode = !!editData
  const chatDialogSchema = createChatDialogSchema(t)

  const form = useForm<ChatDialogFormValues>({
    resolver: zodResolver(chatDialogSchema),
    defaultValues: {
      name: '',
      url: '',
    },
  })

  useEffect(() => {
    if (editData) {
      form.reset(editData)
    } else {
      form.reset({
        name: '',
        url: '',
      })
    }
  }, [editData, form, open])

  const handleSubmit = (values: ChatDialogFormValues) => {
    onSave(values)
    form.reset()
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={isEditMode ? t('Edit chat preset') : t('Add chat preset')}
      description={t('Configure a predefined chat link for end users.')}
      contentClassName='sm:max-w-[500px]'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={CHAT_DIALOG_FORM_ID}>
            {isEditMode ? t('Update') : t('Add')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={CHAT_DIALOG_FORM_ID}
          onSubmit={form.handleSubmit(handleSubmit)}
          className='space-y-4'
        >
          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Chat Client Name')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={t('Please enter chat client name')}
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t('Display name for this chat client.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='url'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('URL')}</FormLabel>
                <FormControl>
                  <Input placeholder={t('Please enter the URL')} {...field} />
                </FormControl>
                <FormDescription>
                  {t('The URL for this chat client.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
