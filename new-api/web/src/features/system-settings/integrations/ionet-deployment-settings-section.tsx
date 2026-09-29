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
import { CheckCircle2, Loader2, XCircle } from 'lucide-react'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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
import { Switch } from '@/components/ui/switch'
import { testDeploymentConnectionWithKey } from '@/features/models/api'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

const schema = z.object({
  enabled: z.boolean(),
  apiKey: z.string().optional(),
})

// NOTE: react-hook-form resolver uses the schema input type
type Values = z.input<typeof schema>

export function IoNetDeploymentSettingsSection({
  defaultValues,
}: {
  defaultValues: {
    enabled: boolean
    apiKey: string
  }
}) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      enabled: defaultValues.enabled,
      apiKey: defaultValues.apiKey ?? '',
    },
  })

  const { isDirty, isSubmitting } = form.formState
  const enabled = form.watch('enabled')

  const [testState, setTestState] = useState<{
    loading: boolean
    ok: boolean | null
    error: string | null
  }>({ loading: false, ok: null, error: null })

  async function onSubmit(values: Values) {
    const updates: Array<{ key: string; value: string }> = []

    if (values.enabled !== defaultValues.enabled) {
      updates.push({
        key: 'model_deployment.ionet.enabled',
        value: String(values.enabled),
      })
    }

    if ((values.apiKey || '') !== (defaultValues.apiKey || '')) {
      updates.push({
        key: 'model_deployment.ionet.api_key',
        value: String(values.apiKey || ''),
      })
    }

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const update of updates) {
      await updateOption.mutateAsync(update)
    }

    form.reset(values)
  }

  const handleTestConnection = async () => {
    setTestState({ loading: true, ok: null, error: null })
    try {
      const apiKey = form.getValues('apiKey')
      const res = await testDeploymentConnectionWithKey(apiKey)
      if (res?.success) {
        setTestState({ loading: false, ok: true, error: null })
        return
      }
      setTestState({
        loading: false,
        ok: false,
        error: res?.message || t('Connection failed'),
      })
    } catch (err) {
      setTestState({
        loading: false,
        ok: false,
        error: err instanceof Error ? err.message : t('Connection failed'),
      })
    }
  }

  return (
    <SettingsSection title={t('io.net Deployments')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending || isSubmitting}
            isSaveDisabled={!isDirty}
            saveLabel='Save io.net settings'
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable io.net deployments')}</FormLabel>
                  <FormDescription>
                    {t('Enable io.net model deployment service in console')}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={(v) => field.onChange(v)}
                    disabled={updateOption.isPending || isSubmitting}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          {enabled ? (
            <>
              <FormField
                control={form.control}
                name='apiKey'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('io.net API Key')}</FormLabel>
                    <div className='flex gap-2'>
                      <FormControl>
                        <Input
                          type='password'
                          placeholder={t('Enter API Key')}
                          autoComplete='off'
                          {...field}
                        />
                      </FormControl>
                      <Button
                        type='button'
                        variant='secondary'
                        onClick={handleTestConnection}
                        disabled={testState.loading || updateOption.isPending}
                        className='shrink-0'
                      >
                        {testState.loading ? (
                          <Loader2 className='me-2 size-4 animate-spin' />
                        ) : null}
                        {t('Test Connection')}
                      </Button>
                    </div>
                    <FormDescription>
                      {t('Used to authenticate with io.net deployment API')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <Alert variant='default'>
                <AlertTitle>{t('How to get an io.net API Key')}</AlertTitle>
                <AlertDescription>
                  <div className='space-y-2'>
                    <ul className='list-disc space-y-1 pl-5'>
                      <li>{t('Open the io.net console API Keys page')}</li>
                      <li>
                        {t(
                          'Set Project to io.cloud when creating/selecting key'
                        )}
                      </li>
                      <li>{t('Copy the key and paste it here')}</li>
                    </ul>
                    <Button
                      type='button'
                      variant='outline'
                      onClick={() =>
                        window.open('https://ai.io.net/ai/api-keys', '_blank')
                      }
                    >
                      {t('Go to io.net API Keys')}
                    </Button>
                  </div>
                </AlertDescription>
              </Alert>

              {testState.ok === true ? (
                <Alert variant='default' className='flex items-center gap-2'>
                  <CheckCircle2 className='size-4 text-green-600' />
                  <div>
                    <AlertTitle>{t('Connection successful')}</AlertTitle>
                    <AlertDescription>
                      {t('Connected to io.net service normally.')}
                    </AlertDescription>
                  </div>
                </Alert>
              ) : null}

              {testState.ok === false && testState.error ? (
                <Alert
                  variant='destructive'
                  className='flex items-center gap-2'
                >
                  <XCircle className='size-4' />
                  <div>
                    <AlertTitle>{t('Connection failed')}</AlertTitle>
                    <AlertDescription>{t(testState.error)}</AlertDescription>
                  </div>
                </Alert>
              ) : null}
            </>
          ) : null}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
