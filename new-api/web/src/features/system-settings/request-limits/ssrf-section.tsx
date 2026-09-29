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
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

const ssrfSchema = z.object({
  fetch_setting: z.object({
    enable_ssrf_protection: z.boolean(),
    allow_private_ip: z.boolean(),
    domain_filter_mode: z.boolean(),
    ip_filter_mode: z.boolean(),
    domain_list: z.string(),
    ip_list: z.string(),
    allowed_ports: z.string(),
    apply_ip_filter_for_domain: z.boolean(),
  }),
})

type SSRFFormValues = z.output<typeof ssrfSchema>
type SSRFFormInput = z.input<typeof ssrfSchema>

type NormalizedSSRFValues = {
  'fetch_setting.enable_ssrf_protection': boolean
  'fetch_setting.allow_private_ip': boolean
  'fetch_setting.domain_filter_mode': boolean
  'fetch_setting.ip_filter_mode': boolean
  'fetch_setting.domain_list': string[]
  'fetch_setting.ip_list': string[]
  'fetch_setting.allowed_ports': number[]
  'fetch_setting.apply_ip_filter_for_domain': boolean
}

type SSRFSectionProps = {
  defaultValues: {
    'fetch_setting.enable_ssrf_protection': boolean
    'fetch_setting.allow_private_ip': boolean
    'fetch_setting.domain_filter_mode': boolean
    'fetch_setting.ip_filter_mode': boolean
    'fetch_setting.domain_list': string[]
    'fetch_setting.ip_list': string[]
    'fetch_setting.allowed_ports': number[]
    'fetch_setting.apply_ip_filter_for_domain': boolean
  }
}

const splitLines = (value: string) =>
  value
    .split('\n')
    .map((entry) => entry.trim())
    .filter(Boolean)

const parsePorts = (value: string) =>
  value
    .split(',')
    .map((item) => Number.parseInt(item.trim(), 10))
    .filter((port) => Number.isFinite(port))

const buildFormDefaults = (
  defaults: SSRFSectionProps['defaultValues']
): SSRFFormInput => ({
  fetch_setting: {
    enable_ssrf_protection: defaults['fetch_setting.enable_ssrf_protection'],
    allow_private_ip: defaults['fetch_setting.allow_private_ip'],
    domain_filter_mode: defaults['fetch_setting.domain_filter_mode'],
    ip_filter_mode: defaults['fetch_setting.ip_filter_mode'],
    domain_list: defaults['fetch_setting.domain_list'].join('\n'),
    ip_list: defaults['fetch_setting.ip_list'].join('\n'),
    allowed_ports: defaults['fetch_setting.allowed_ports'].join(','),
    apply_ip_filter_for_domain:
      defaults['fetch_setting.apply_ip_filter_for_domain'],
  },
})

const normalizeDefaults = (
  defaults: SSRFSectionProps['defaultValues']
): NormalizedSSRFValues => ({
  'fetch_setting.enable_ssrf_protection':
    defaults['fetch_setting.enable_ssrf_protection'],
  'fetch_setting.allow_private_ip': defaults['fetch_setting.allow_private_ip'],
  'fetch_setting.domain_filter_mode':
    defaults['fetch_setting.domain_filter_mode'],
  'fetch_setting.ip_filter_mode': defaults['fetch_setting.ip_filter_mode'],
  'fetch_setting.domain_list': defaults['fetch_setting.domain_list'],
  'fetch_setting.ip_list': defaults['fetch_setting.ip_list'],
  'fetch_setting.allowed_ports': defaults['fetch_setting.allowed_ports'],
  'fetch_setting.apply_ip_filter_for_domain':
    defaults['fetch_setting.apply_ip_filter_for_domain'],
})

const normalizeFormValues = (values: SSRFFormValues): NormalizedSSRFValues => ({
  'fetch_setting.enable_ssrf_protection':
    values.fetch_setting.enable_ssrf_protection,
  'fetch_setting.allow_private_ip': values.fetch_setting.allow_private_ip,
  'fetch_setting.domain_filter_mode': values.fetch_setting.domain_filter_mode,
  'fetch_setting.ip_filter_mode': values.fetch_setting.ip_filter_mode,
  'fetch_setting.domain_list': splitLines(values.fetch_setting.domain_list),
  'fetch_setting.ip_list': splitLines(values.fetch_setting.ip_list),
  'fetch_setting.allowed_ports': parsePorts(values.fetch_setting.allowed_ports),
  'fetch_setting.apply_ip_filter_for_domain':
    values.fetch_setting.apply_ip_filter_for_domain,
})

const isEqual = (a: unknown, b: unknown) => {
  if (Array.isArray(a) && Array.isArray(b)) {
    return JSON.stringify(a) === JSON.stringify(b)
  }
  return a === b
}

export function SSRFSection({ defaultValues }: SSRFSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const baselineRef = useRef<NormalizedSSRFValues>(
    normalizeDefaults(defaultValues)
  )

  const formDefaults = useMemo(
    () => buildFormDefaults(defaultValues),
    [defaultValues]
  )

  const form = useForm<SSRFFormInput, unknown, SSRFFormValues>({
    resolver: zodResolver(ssrfSchema),
    defaultValues: formDefaults,
  })

  useEffect(() => {
    baselineRef.current = normalizeDefaults(defaultValues)
    form.reset(buildFormDefaults(defaultValues))
  }, [defaultValues, form])

  const onSubmit = async (data: SSRFFormValues) => {
    const normalized = normalizeFormValues(data)
    const updates = (
      Object.keys(normalized) as Array<keyof NormalizedSSRFValues>
    ).filter((key) => !isEqual(normalized[key], baselineRef.current[key]))

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const key of updates) {
      const value = normalized[key]
      await updateOption.mutateAsync({
        key,
        value: Array.isArray(value) ? JSON.stringify(value) : value,
      })
    }

    baselineRef.current = normalized
  }

  const domainFilterMode = form.watch('fetch_setting.domain_filter_mode')
  const ipFilterMode = form.watch('fetch_setting.ip_filter_mode')

  return (
    <SettingsSection title={t('SSRF Protection')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save SSRF settings'
          />
          <FormField
            control={form.control}
            name='fetch_setting.enable_ssrf_protection'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable SSRF Protection')}</FormLabel>
                  <FormDescription>
                    {t('Prevent server-side request forgery attacks')}
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
            name='fetch_setting.allow_private_ip'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Allow Private IPs')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Allow requests to private IP ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)'
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
            name='fetch_setting.domain_filter_mode'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Domain Filter Mode')}</FormLabel>
                <Select
                  items={[
                    {
                      value: 'false',
                      label: t('Blacklist (Block listed domains)'),
                    },
                    {
                      value: 'true',
                      label: t('Whitelist (Only allow listed domains)'),
                    },
                  ]}
                  onValueChange={(value) => field.onChange(value === 'true')}
                  value={field.value ? 'true' : 'false'}
                >
                  <FormControl>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      <SelectItem value='false'>
                        {t('Blacklist (Block listed domains)')}
                      </SelectItem>
                      <SelectItem value='true'>
                        {t('Whitelist (Only allow listed domains)')}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <FormDescription>
                  {t('Choose how to filter domains')}
                </FormDescription>
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='fetch_setting.domain_list'
            render={({ field }) => (
              <FormItem>
                <FormLabel>
                  {t('Domain')}{' '}
                  {domainFilterMode ? t('Whitelist') : t('Blacklist')}
                </FormLabel>
                <FormControl>
                  <Textarea
                    placeholder={t('example.com&#10;blocked-site.com')}
                    rows={4}
                    {...field}
                  />
                </FormControl>
                <FormDescription>{t('One domain per line')}</FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='fetch_setting.ip_filter_mode'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('IP Filter Mode')}</FormLabel>
                <Select
                  items={[
                    {
                      value: 'false',
                      label: t('Blacklist (Block listed IPs)'),
                    },
                    {
                      value: 'true',
                      label: t('Whitelist (Only allow listed IPs)'),
                    },
                  ]}
                  onValueChange={(value) => field.onChange(value === 'true')}
                  value={field.value ? 'true' : 'false'}
                >
                  <FormControl>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      <SelectItem value='false'>
                        {t('Blacklist (Block listed IPs)')}
                      </SelectItem>
                      <SelectItem value='true'>
                        {t('Whitelist (Only allow listed IPs)')}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <FormDescription>
                  {t('Choose how to filter IP addresses')}
                </FormDescription>
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='fetch_setting.ip_list'
            render={({ field }) => (
              <FormItem>
                <FormLabel>
                  {t('IP')} {ipFilterMode ? t('Whitelist') : t('Blacklist')}
                </FormLabel>
                <FormControl>
                  <Textarea
                    placeholder={t('192.168.1.1&#10;10.0.0.0/8')}
                    rows={4}
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t('One IP or CIDR range per line')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='fetch_setting.allowed_ports'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Allowed Ports')}</FormLabel>
                <FormControl>
                  <Input placeholder={t('80,443,8080')} {...field} />
                </FormControl>
                <FormDescription>
                  {t(
                    'Comma-separated list of allowed ports (empty = all ports)'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='fetch_setting.apply_ip_filter_for_domain'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>
                    {t('Apply IP Filter to Resolved Domains')}
                  </FormLabel>
                  <FormDescription>
                    {t(
                      'Check resolved IPs against IP filters even when accessing by domain'
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
