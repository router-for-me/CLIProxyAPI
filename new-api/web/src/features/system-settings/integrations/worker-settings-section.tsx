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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

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
import { Switch } from '@/components/ui/switch'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'
import { removeTrailingSlash } from './utils'

const createWorkerSchema = (t: (key: string) => string) =>
  z.object({
    WorkerUrl: z.string().refine((value) => {
      const trimmed = value.trim()
      if (!trimmed) return true
      return /^https?:\/\//.test(trimmed)
    }, t('Provide a valid URL starting with http:// or https://')),
    WorkerValidKey: z.string(),
    WorkerAllowHttpImageRequestEnabled: z.boolean(),
  })

type WorkerFormValues = z.infer<ReturnType<typeof createWorkerSchema>>

type WorkerSettingsSectionProps = {
  defaultValues: WorkerFormValues
}

export function WorkerSettingsSection({
  defaultValues,
}: WorkerSettingsSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const workerSchema = createWorkerSchema(t)

  const form = useForm<WorkerFormValues>({
    resolver: zodResolver(workerSchema),
    defaultValues,
  })

  useResetForm(form, defaultValues)

  const onSubmit = async (values: WorkerFormValues) => {
    const sanitizedUrl = removeTrailingSlash(values.WorkerUrl)
    const sanitizedKey = values.WorkerValidKey.trim()
    const initialUrl = removeTrailingSlash(defaultValues.WorkerUrl)
    const initialKey = defaultValues.WorkerValidKey.trim()

    const updates: Array<{ key: string; value: string | boolean }> = []

    if (sanitizedUrl !== initialUrl) {
      updates.push({ key: 'WorkerUrl', value: sanitizedUrl })
    }

    if (sanitizedKey !== initialKey || sanitizedUrl === '') {
      updates.push({ key: 'WorkerValidKey', value: sanitizedKey })
    }

    if (
      values.WorkerAllowHttpImageRequestEnabled !==
      defaultValues.WorkerAllowHttpImageRequestEnabled
    ) {
      updates.push({
        key: 'WorkerAllowHttpImageRequestEnabled',
        value: values.WorkerAllowHttpImageRequestEnabled,
      })
    }

    for (const update of updates) {
      await updateOption.mutateAsync(update)
    }
  }

  return (
    <SettingsSection title={t('Worker Proxy')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save Worker settings'
          />
          <FormField
            control={form.control}
            name='WorkerUrl'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Worker URL')}</FormLabel>
                <FormControl>
                  <Input
                    type='url'
                    inputMode='url'
                    placeholder={t('https://worker.example.workers.dev')}
                    autoComplete='off'
                    {...field}
                    onChange={(event) => field.onChange(event.target.value)}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Requests will be forwarded to this worker. Trailing slashes are removed automatically.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='WorkerValidKey'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Worker Access Key')}</FormLabel>
                <FormControl>
                  <Input
                    type='password'
                    placeholder={t('Enter new key to update')}
                    autoComplete='new-password'
                    {...field}
                    onChange={(event) => field.onChange(event.target.value)}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Used to authenticate with the worker. Leave blank to keep the existing secret.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='WorkerAllowHttpImageRequestEnabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Allow HTTP image requests')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Enable when proxying workers that fetch images over HTTP.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
