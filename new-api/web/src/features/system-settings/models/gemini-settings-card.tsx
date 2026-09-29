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
import { toast } from 'sonner'
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
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsControlGroup,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  formatJsonForTextarea,
  normalizeJsonString,
  validateJsonString,
} from './utils'

const schema = z.object({
  gemini: z.object({
    safety_settings: z.string().superRefine((value, ctx) => {
      const result = validateJsonString(value)
      if (!result.valid) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          message: result.message || 'Invalid JSON',
        })
      }
    }),
    version_settings: z.string().superRefine((value, ctx) => {
      const result = validateJsonString(value)
      if (!result.valid) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          message: result.message || 'Invalid JSON',
        })
      }
    }),
    supported_imagine_models: z.string().superRefine((value, ctx) => {
      const result = validateJsonString(value, {
        predicate: (parsed) =>
          Array.isArray(parsed) &&
          parsed.every((item) => typeof item === 'string'),
        predicateMessage: 'Expected a JSON array of model identifiers',
      })
      if (!result.valid) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          message: result.message || 'Invalid JSON array',
        })
      }
    }),
    thinking_adapter_enabled: z.boolean(),
    thinking_adapter_budget_tokens_percentage: z.coerce
      .number()
      .min(0.002, { message: 'Must be at least 0.002' })
      .max(1, { message: 'Must be 1 or less' }),
    function_call_thought_signature_enabled: z.boolean(),
    remove_function_response_id_enabled: z.boolean(),
  }),
})

type GeminiSettingsFormValues = z.output<typeof schema>
type GeminiSettingsFormInput = z.input<typeof schema>

type FlatGeminiSettings = {
  'gemini.safety_settings': string
  'gemini.version_settings': string
  'gemini.supported_imagine_models': string
  'gemini.thinking_adapter_enabled': boolean
  'gemini.thinking_adapter_budget_tokens_percentage': number
  'gemini.function_call_thought_signature_enabled': boolean
  'gemini.remove_function_response_id_enabled': boolean
}

type GeminiSettingsCardProps = {
  defaultValues: GeminiSettingsFormInput
}

export function GeminiSettingsCard({ defaultValues }: GeminiSettingsCardProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const normalizedDefaultsRef = useRef<FlatGeminiSettings>({
    'gemini.safety_settings': normalizeJsonString(
      defaultValues.gemini.safety_settings
    ),
    'gemini.version_settings': normalizeJsonString(
      defaultValues.gemini.version_settings
    ),
    'gemini.supported_imagine_models': normalizeJsonString(
      defaultValues.gemini.supported_imagine_models
    ),
    'gemini.thinking_adapter_enabled':
      defaultValues.gemini.thinking_adapter_enabled,
    'gemini.thinking_adapter_budget_tokens_percentage': Number(
      defaultValues.gemini.thinking_adapter_budget_tokens_percentage
    ),
    'gemini.function_call_thought_signature_enabled':
      defaultValues.gemini.function_call_thought_signature_enabled ?? true,
    'gemini.remove_function_response_id_enabled':
      defaultValues.gemini.remove_function_response_id_enabled ?? true,
  })

  const buildFormDefaults = (
    values: GeminiSettingsFormInput
  ): GeminiSettingsFormInput => ({
    gemini: {
      safety_settings: formatJsonForTextarea(values.gemini.safety_settings),
      version_settings: formatJsonForTextarea(values.gemini.version_settings),
      supported_imagine_models: formatJsonForTextarea(
        values.gemini.supported_imagine_models
      ),
      thinking_adapter_enabled: values.gemini.thinking_adapter_enabled,
      thinking_adapter_budget_tokens_percentage:
        values.gemini.thinking_adapter_budget_tokens_percentage,
      function_call_thought_signature_enabled:
        values.gemini.function_call_thought_signature_enabled ?? true,
      remove_function_response_id_enabled:
        values.gemini.remove_function_response_id_enabled ?? true,
    },
  })

  const form = useForm<
    GeminiSettingsFormInput,
    unknown,
    GeminiSettingsFormValues
  >({
    resolver: zodResolver(schema),
    defaultValues: buildFormDefaults(defaultValues),
  })

  useEffect(() => {
    normalizedDefaultsRef.current = {
      'gemini.safety_settings': normalizeJsonString(
        defaultValues.gemini.safety_settings
      ),
      'gemini.version_settings': normalizeJsonString(
        defaultValues.gemini.version_settings
      ),
      'gemini.supported_imagine_models': normalizeJsonString(
        defaultValues.gemini.supported_imagine_models
      ),
      'gemini.thinking_adapter_enabled':
        defaultValues.gemini.thinking_adapter_enabled,
      'gemini.thinking_adapter_budget_tokens_percentage': Number(
        defaultValues.gemini.thinking_adapter_budget_tokens_percentage
      ),
      'gemini.function_call_thought_signature_enabled':
        defaultValues.gemini.function_call_thought_signature_enabled ?? true,
      'gemini.remove_function_response_id_enabled':
        defaultValues.gemini.remove_function_response_id_enabled ?? true,
    }

    form.reset(buildFormDefaults(defaultValues))
  }, [defaultValues, form])

  const isAdapterEnabled = form.watch('gemini.thinking_adapter_enabled')

  const onSubmit = async (values: GeminiSettingsFormValues) => {
    const normalized: FlatGeminiSettings = {
      'gemini.safety_settings': normalizeJsonString(
        values.gemini.safety_settings
      ),
      'gemini.version_settings': normalizeJsonString(
        values.gemini.version_settings
      ),
      'gemini.supported_imagine_models': normalizeJsonString(
        values.gemini.supported_imagine_models
      ),
      'gemini.thinking_adapter_enabled': values.gemini.thinking_adapter_enabled,
      'gemini.thinking_adapter_budget_tokens_percentage':
        values.gemini.thinking_adapter_budget_tokens_percentage,
      'gemini.function_call_thought_signature_enabled':
        values.gemini.function_call_thought_signature_enabled,
      'gemini.remove_function_response_id_enabled':
        values.gemini.remove_function_response_id_enabled,
    }

    const updates = (
      Object.keys(normalized) as Array<keyof FlatGeminiSettings>
    ).filter((key) => normalized[key] !== normalizedDefaultsRef.current[key])

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const key of updates) {
      await updateOption.mutateAsync({
        key,
        value: normalized[key],
      })
    }
  }

  const imaginePlaceholder = useMemo(
    () => JSON.stringify(['gemini-2.0-flash-exp-image-generation'], null, 2),
    []
  )

  return (
    <SettingsSection title={t('Gemini')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
          />
          <FormField
            control={form.control}
            name='gemini.safety_settings'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Safety Settings')}</FormLabel>
                <FormControl>
                  <JsonCodeEditor
                    value={field.value}
                    onChange={field.onChange}
                    name={field.name}
                    onBlur={field.onBlur}
                    textareaRef={field.ref}
                    aria-invalid={Boolean(
                      form.formState.errors.gemini?.safety_settings
                    )}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Provide per-category safety overrides as JSON. Use `default` for fallback values.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='gemini.version_settings'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Version Overrides')}</FormLabel>
                <FormControl>
                  <JsonCodeEditor
                    value={field.value}
                    onChange={field.onChange}
                    name={field.name}
                    onBlur={field.onBlur}
                    textareaRef={field.ref}
                    aria-invalid={Boolean(
                      form.formState.errors.gemini?.version_settings
                    )}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Map model identifiers to Gemini API versions. A `default` entry applies when no specific match is found.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='gemini.supported_imagine_models'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Supported Imagine Models')}</FormLabel>
                <FormControl>
                  <JsonCodeEditor
                    value={field.value}
                    onChange={field.onChange}
                    name={field.name}
                    onBlur={field.onBlur}
                    textareaRef={field.ref}
                    placeholder={imaginePlaceholder}
                    heightClassName='h-40 min-h-40 max-h-40'
                    aria-invalid={Boolean(
                      form.formState.errors.gemini?.supported_imagine_models
                    )}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Accepts a JSON array of model identifiers that support the Imagine API.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <SettingsControlGroup>
            <FormField
              control={form.control}
              name='gemini.thinking_adapter_enabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Thinking Suffix Adapter')}</FormLabel>
                    <FormDescription>
                      {t('Supports `-thinking`, `-thinking-')}
                      {'{{budget}}'}
                      {t(
                        '`, and `-nothinking` suffixes while routing to the correct Gemini variant.'
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

            <FormField
              control={form.control}
              name='gemini.thinking_adapter_budget_tokens_percentage'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Budget Tokens Ratio')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      value={String(field.value ?? '')}
                      onChange={(event) => field.onChange(event.target.value)}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Budget tokens = max tokens × ratio. Accepts a decimal between 0.002 and 1. Recommended to keep aligned with upstream billing.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            {!isAdapterEnabled && (
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Gemini will continue to auto-detect thinking mode even with the adapter disabled. Enable this only when you need finer control over pricing and budgeting.'
                )}
              </p>
            )}
          </SettingsControlGroup>

          <FormField
            control={form.control}
            name='gemini.function_call_thought_signature_enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>
                    {t('Enable FunctionCall thoughtSignature Fill')}
                  </FormLabel>
                  <FormDescription>
                    {t(
                      'Fill thoughtSignature only for Gemini/Vertex channels using the OpenAI format'
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

          <FormField
            control={form.control}
            name='gemini.remove_function_response_id_enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Remove functionResponse.id field')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Vertex AI does not support functionResponse.id. Enable this to remove the field automatically.'
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
