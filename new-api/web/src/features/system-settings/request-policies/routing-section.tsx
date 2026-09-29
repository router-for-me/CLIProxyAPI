import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo } from 'react'
import { useForm, useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Accordion } from '@/components/ui/accordion'
import { Alert, AlertDescription } from '@/components/ui/alert'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { handleServerError } from '@/lib/handle-server-error'
import { parseHttpStatusCodeRules } from '@/lib/http-status-code-rules'

import { SettingsAccordion } from '../components/settings-accordion'
import { SettingsCard } from '../components/settings-card'
import {
  SettingsControlChildren,
  SettingsSwitchField,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { ChannelAffinitySection } from '../general/channel-affinity'
import { safeNumberFieldProps } from '../utils/numeric-field'
import { getPolicyConfig, type PolicyConfig } from './api'
import { policyLabel } from './policy-label'
import { RetrySection } from './retry-section'
import {
  createRoutingPolicySchema,
  routingPolicyFormValues,
  routingPolicyOptions,
  type RoutingPolicyFormValues,
} from './routing-form'
import { useSavePolicy } from './use-save-policy'

export function RoutingPolicySection() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['request-policy'],
    queryFn: getPolicyConfig,
    staleTime: Infinity,
  })
  if (query.isPending) return <LoadingState />
  if (query.isError) {
    return (
      <ErrorState
        title={t('Failed to load settings')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return <RoutingPolicyEditor config={query.data} />
}

function RoutingPolicyEditor(props: { config: PolicyConfig }) {
  const { t } = useTranslation()
  const mutation = useSavePolicy()
  const schema = useMemo(() => createRoutingPolicySchema(t), [t])
  const defaults = useMemo(
    () => routingPolicyFormValues(props.config.options),
    [props.config.options]
  )
  const form = useForm<RoutingPolicyFormValues>({
    resolver: zodResolver(schema),
    defaultValues: defaults,
  })
  // Subscribe so refreshes can preserve every dirty field, including rule JSON.
  const { dirtyFields } = form.formState
  useEffect(() => {
    form.reset(defaults, { keepDirtyValues: true })
  }, [defaults, form])
  const values = useWatch({
    control: form.control,
    compute: (current) => current,
  })

  const save = async (submitted: RoutingPolicyFormValues) => {
    const next = routingPolicyOptions(submitted)
    next.AutomaticRetryStatusCodes = parseHttpStatusCodeRules(
      next.AutomaticRetryStatusCodes
    ).normalized
    const delta = Object.fromEntries(
      Object.entries(next).filter(([key, value]) => {
        const previous = props.config.options[key]
        if (key === 'channel_affinity_setting.rules') {
          return (
            JSON.stringify(JSON.parse(value)) !==
            JSON.stringify(JSON.parse(previous || '[]'))
          )
        }
        if (key === 'AutomaticRetryStatusCodes') {
          return value !== parseHttpStatusCodeRules(previous).normalized
        }
        return value !== previous
      })
    )
    if (Object.keys(delta).length === 0) {
      toast.info(t('No changes to save'))
      return
    }
    try {
      const data = await mutation.mutateAsync(delta)
      form.reset(routingPolicyFormValues(data.options))
    } catch (error) {
      handleServerError(error)
    }
  }
  const submit = form.handleSubmit((submitted) => save(submitted))

  // The session rules stay in the main column, between the defaults they
  // inherit and the retry budget.
  return (
    <Form {...form}>
      <div className='min-w-0 space-y-5'>
        <SettingsPageFormActions
          onSave={submit}
          isSaving={mutation.isPending}
        />
        <div className='min-w-0 space-y-4'>
          <AffinitySettings />
          <FormField
            control={form.control}
            name='channel_affinity_setting.rules'
            render={({ field }) => (
              <FormItem>
                <ChannelAffinitySection
                  rulesJson={field.value}
                  onRulesChange={field.onChange}
                  enabled={values.channel_affinity_setting.enabled}
                  globalSessionMode={
                    values.channel_affinity_setting.session_mode
                  }
                />
                <FormMessage />
              </FormItem>
            )}
          />
          <RetrySection />
          {mutation.isError ? (
            <p role='alert' className='text-destructive text-sm'>
              {mutation.error.message}
            </p>
          ) : null}
          {Object.keys(dirtyFields).length > 0 &&
          !schema.safeParse(values).success ? (
            <Alert>
              <AlertDescription>
                {t('Correct the invalid settings before saving.')}
              </AlertDescription>
            </Alert>
          ) : null}
        </div>
      </div>
    </Form>
  )
}

function AffinitySettings() {
  const { t } = useTranslation()
  const form = useFormContext<RoutingPolicyFormValues>()
  const enabled = useWatch({
    control: form.control,
    name: 'channel_affinity_setting.enabled',
  })
  return (
    <SettingsCard
      title={t('Session defaults')}
      description={t('Rules can inherit or override these defaults.')}
      className='shadow-none'
    >
      <FormField
        control={form.control}
        name='channel_affinity_setting.enabled'
        render={({ field }) => (
          <SettingsSwitchField
            controlId='channel_affinity_setting.enabled'
            checked={field.value}
            onCheckedChange={field.onChange}
            label={t('Enable session affinity')}
          />
        )}
      />
      <FormField
        control={form.control}
        name='channel_affinity_setting.session_mode'
        render={({ field }) => (
          <FormItem className='mt-4'>
            <FormControl>
              <RadioGroup
                aria-label={t('Session behavior')}
                value={field.value || 'prefer'}
                onValueChange={field.onChange}
                className='gap-2 sm:grid-cols-3'
              >
                {(['off', 'prefer', 'strict'] as const).map((mode) => (
                  <div key={mode} className='rounded-lg border'>
                    <Label
                      htmlFor={`global-session-${mode}`}
                      className='hover:bg-muted/30 flex h-full cursor-pointer items-center gap-3 rounded-lg px-4 py-3 font-normal'
                    >
                      <RadioGroupItem
                        id={`global-session-${mode}`}
                        aria-labelledby={`global-session-${mode}-label`}
                        aria-describedby={
                          mode === 'prefer'
                            ? 'global-session-prefer-description'
                            : undefined
                        }
                        value={mode}
                        onClick={() => field.onChange(mode)}
                      />
                      <span className='flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-1'>
                        <span id={`global-session-${mode}-label`}>
                          {policyLabel(t, mode)}
                        </span>
                        {mode === 'prefer' ? (
                          <span
                            id='global-session-prefer-description'
                            className='text-muted-foreground text-xs font-normal'
                          >
                            {t('Reduces cache hit rate')}
                          </span>
                        ) : null}
                      </span>
                    </Label>
                  </div>
                ))}
              </RadioGroup>
            </FormControl>
            <SettingsControlChildren
              role='group'
              aria-label={t('Channel switching options')}
              className='mt-3'
            >
              <FormField
                control={form.control}
                name='channel_affinity_setting.switch_on_success'
                render={({ field: switchField }) => (
                  <SettingsSwitchField
                    controlId='channel_affinity_setting.switch_on_success'
                    disabled={!enabled}
                    checked={switchField.value}
                    onCheckedChange={switchField.onChange}
                    label={t(
                      'Update the session binding after a successful switch'
                    )}
                    description={t('Later requests follow the new channel.')}
                  />
                )}
              />
            </SettingsControlChildren>
            <FormMessage />
          </FormItem>
        )}
      />
      <FormField
        control={form.control}
        name='channel_affinity_setting.keep_on_channel_disabled'
        render={({ field }) => (
          <SettingsSwitchField
            controlId='channel_affinity_setting.keep_on_channel_disabled'
            className='mt-4'
            disabled={!enabled}
            checked={field.value}
            onCheckedChange={field.onChange}
            label={t(
              'Keep the session binding when the channel is unavailable'
            )}
            description={t('Otherwise the binding is removed.')}
          />
        )}
      />
      <Accordion className='mt-4'>
        <SettingsAccordion
          value='session-advanced'
          title={t('Affinity cache settings')}
          keepMounted
        >
          <div className='space-y-4'>
            <div className='grid gap-4 sm:grid-cols-2'>
              {(
                [
                  {
                    name: 'channel_affinity_setting.max_entries',
                    label: t('Maximum cached sessions'),
                  },
                  {
                    name: 'channel_affinity_setting.default_ttl_seconds',
                    label: t('Default session lifetime (seconds)'),
                  },
                ] as const
              ).map((item) => (
                <FormField
                  key={item.name}
                  control={form.control}
                  name={item.name}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{item.label}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={0}
                          step={1}
                          {...safeNumberFieldProps(field)}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              ))}
            </div>
          </div>
        </SettingsAccordion>
      </Accordion>
    </SettingsCard>
  )
}
