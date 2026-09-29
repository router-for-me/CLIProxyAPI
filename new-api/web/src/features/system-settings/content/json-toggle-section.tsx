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
import { useEffect, useMemo, useRef } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { JsonCodeEditor } from '@/components/json-code-editor'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'

import { SettingsAccordion } from '../components/settings-accordion'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { useUpdateOption } from '../hooks/use-update-option'
import { formatJsonForEditor, normalizeJsonString } from './utils'

type JsonToggleSectionProps = {
  value: string
  title: string
  toggleDescription?: string
  optionKey: string
  enabledKey: string
  defaultEnabled: boolean
  defaultValue: string
  fallbackValue?: string
  textareaLabel: string
  textareaDescription?: string
  placeholder?: string
  example?: string
  submitLabel?: string
  validate?: (parsed: unknown) => { valid: boolean; message?: string }
}

type JsonToggleFormValues = {
  enabled: boolean
  json: string
}

export function JsonToggleSection({
  value,
  title,
  toggleDescription,
  optionKey,
  enabledKey,
  defaultEnabled,
  defaultValue,
  fallbackValue = '[]',
  textareaLabel,
  textareaDescription,
  placeholder,
  example,
  submitLabel = 'Save Changes',
  validate,
}: JsonToggleSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const formattedDefault = useMemo(
    () => formatJsonForEditor(defaultValue, fallbackValue),
    [defaultValue, fallbackValue]
  )

  const form = useForm<JsonToggleFormValues>({
    mode: 'onChange', // Enable real-time validation
    resolver: zodResolver(
      z.object({
        enabled: z.boolean(),
        json: z.string().superRefine((value, ctx) => {
          try {
            const normalized = normalizeJsonString(value, fallbackValue)
            const parsed = JSON.parse(normalized)
            if (validate) {
              const result = validate(parsed)
              if (!result.valid) {
                ctx.addIssue({
                  code: z.ZodIssueCode.custom,
                  message:
                    result.message ||
                    'JSON structure is invalid for this setting',
                })
              }
            }
          } catch (error: unknown) {
            ctx.addIssue({
              code: z.ZodIssueCode.custom,
              message:
                (error instanceof Error ? error.message : null) ||
                'Invalid JSON data',
            })
          }
        }),
      })
    ),
    defaultValues: {
      enabled: defaultEnabled,
      json: formattedDefault,
    },
  })

  const initialNormalizedRef = useRef(
    normalizeJsonString(defaultValue, fallbackValue)
  )
  const initialEnabledRef = useRef(defaultEnabled)

  useEffect(() => {
    initialNormalizedRef.current = normalizeJsonString(
      defaultValue,
      fallbackValue
    )
    initialEnabledRef.current = defaultEnabled
    form.reset({
      enabled: defaultEnabled,
      json: formatJsonForEditor(defaultValue, fallbackValue),
    })
  }, [defaultEnabled, defaultValue, fallbackValue, form])

  const onSubmit = async (values: JsonToggleFormValues) => {
    const updates: Array<{ key: string; value: string | boolean }> = []

    if (values.enabled !== initialEnabledRef.current) {
      updates.push({ key: enabledKey, value: values.enabled })
    }

    const normalized = normalizeJsonString(values.json, fallbackValue)
    if (normalized !== initialNormalizedRef.current) {
      updates.push({ key: optionKey, value: normalized })
    }

    for (const update of updates) {
      await updateOption.mutateAsync(update)
    }
  }

  return (
    <SettingsAccordion value={value} title={title}>
      <Form {...form}>
        {/* eslint-disable-next-line react-hooks/refs */}
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel={submitLabel}
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Module availability')}</FormLabel>
                  {toggleDescription && (
                    <FormDescription>{t(toggleDescription)}</FormDescription>
                  )}
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

          <FormField
            control={form.control}
            name='json'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{textareaLabel}</FormLabel>
                <FormControl>
                  <JsonCodeEditor
                    value={field.value}
                    onChange={field.onChange}
                    name={field.name}
                    onBlur={field.onBlur}
                    textareaRef={field.ref}
                    placeholder={placeholder}
                    heightClassName='h-72 min-h-72 max-h-72'
                    aria-invalid={Boolean(form.formState.errors.json)}
                  />
                </FormControl>
                {textareaDescription && (
                  <FormDescription>{t(textareaDescription)}</FormDescription>
                )}
                {example && (
                  <div className='text-muted-foreground text-xs'>{example}</div>
                )}
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsAccordion>
  )
}
