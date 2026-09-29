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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useEffect, useMemo } from 'react'
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getDeployment, updateDeployment } from '../../api'
import { deploymentsQueryKeys } from '../../lib'

const schema = z.object({
  image_url: z.string().optional(),
  traffic_port: z.coerce.number().int().min(1).max(65535).optional(),
  entrypoint: z.string().optional(),
  args: z.string().optional(),
  command: z.string().optional(),
  registry_username: z.string().optional(),
  registry_secret: z.string().optional(),
  env_json: z.string().optional(),
  secret_env_json: z.string().optional(),
})

type Values = z.input<typeof schema>

const UPDATE_CONFIG_FORM_ID = 'update-config-form'

function normalizeJsonObject(input?: string) {
  if (!input || !input.trim()) return undefined
  const parsed = JSON.parse(input)
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('JSON must be an object')
  }
  return Object.fromEntries(
    Object.entries(parsed as Record<string, unknown>).map(([k, v]) => [
      k,
      String(v),
    ])
  ) as Record<string, string>
}

export function UpdateConfigDialog({
  open,
  onOpenChange,
  deploymentId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  deploymentId: string | number | null
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const form = useForm<Values>({
    resolver: zodResolver(schema) as unknown as Resolver<Values>,
    defaultValues: {
      image_url: '',
      traffic_port: undefined,
      entrypoint: '',
      args: '',
      command: '',
      registry_username: '',
      registry_secret: '',
      env_json: '',
      secret_env_json: '',
    },
  })

  const { data: detailsRes, isLoading } = useQuery({
    queryKey: ['deployment-details-for-update', deploymentId],
    queryFn: async () =>
      requireServerSuccess(
        await (deploymentId ? getDeployment(deploymentId) : null)
      ),
    enabled: open && deploymentId !== null,
  })

  const details = detailsRes?.data

  useEffect(() => {
    if (!open || !details) return
    const containerConfig =
      details.container_config && typeof details.container_config === 'object'
        ? (details.container_config as Record<string, unknown>)
        : {}
    const imageUrl =
      typeof containerConfig.image_url === 'string'
        ? containerConfig.image_url
        : ''
    const trafficPort =
      typeof containerConfig.traffic_port === 'number'
        ? containerConfig.traffic_port
        : undefined
    const entrypointArr = Array.isArray(containerConfig.entrypoint)
      ? (containerConfig.entrypoint as unknown[])
          .map((x) => (typeof x === 'string' ? x : ''))
          .filter(Boolean)
      : []
    const envVars =
      containerConfig.env_variables &&
      typeof containerConfig.env_variables === 'object' &&
      !Array.isArray(containerConfig.env_variables)
        ? (containerConfig.env_variables as Record<string, unknown>)
        : {}

    form.reset({
      image_url: imageUrl,
      traffic_port: trafficPort,
      entrypoint: entrypointArr.join(' '),
      args: '',
      command: '',
      registry_username: '',
      registry_secret: '',
      env_json: Object.keys(envVars).length
        ? JSON.stringify(envVars, null, 2)
        : '',
      secret_env_json: '',
    })
  }, [open, details, form])

  const title = useMemo(
    () =>
      deploymentId
        ? `${t('Update configuration')} - ${deploymentId}`
        : t('Update configuration'),
    [deploymentId, t]
  )

  const onSubmit = async (values: Values) => {
    if (!deploymentId) return
    try {
      const env_variables = normalizeJsonObject(values.env_json)
      const secret_env_variables = normalizeJsonObject(values.secret_env_json)
      const entrypoint = values.entrypoint
        ? values.entrypoint
            .split(' ')
            .map((x) => x.trim())
            .filter(Boolean)
        : undefined
      const args = values.args
        ? values.args
            .split(' ')
            .map((x) => x.trim())
            .filter(Boolean)
        : undefined

      const res = await updateDeployment(deploymentId, {
        image_url: values.image_url?.trim() || undefined,
        traffic_port:
          typeof values.traffic_port === 'number'
            ? values.traffic_port
            : undefined,
        registry_username: values.registry_username?.trim() || undefined,
        registry_secret: values.registry_secret?.trim() || undefined,
        command: values.command?.trim() || undefined,
        ...(entrypoint?.length ? { entrypoint } : {}),
        ...(args?.length ? { args } : {}),
        ...(env_variables ? { env_variables } : {}),
        ...(secret_env_variables ? { secret_env_variables } : {}),
      })

      if (res.success) {
        toast.success(t('Updated successfully'))
        queryClient.invalidateQueries({
          queryKey: deploymentsQueryKeys.lists(),
        })
        queryClient.invalidateQueries({ queryKey: ['deployment-details'] })
        onOpenChange(false)
        return
      }
      handleServerError(res, t('Update failed'))
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t('Update failed')
      handleServerError(err, msg)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      contentClassName='max-h-(--dialog-available-height) overflow-y-auto max-sm:w-screen max-sm:max-w-none max-sm:rounded-none max-sm:p-4 sm:max-w-3xl'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        isLoading ? null : (
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => onOpenChange(false)}
            >
              {t('Cancel')}
            </Button>
            <Button
              type='submit'
              form={UPDATE_CONFIG_FORM_ID}
              disabled={form.formState.isSubmitting}
            >
              {form.formState.isSubmitting ? (
                <Loader2 className='mr-2 h-4 w-4 animate-spin' />
              ) : null}
              {t('Update')}
            </Button>
          </>
        )
      }
    >
      {isLoading ? (
        <div className='flex items-center justify-center py-10'>
          <Loader2 className='text-muted-foreground h-6 w-6 animate-spin' />
        </div>
      ) : (
        <div className='max-h-[calc(100dvh-8.5rem)] overflow-y-auto py-2 pr-1 sm:max-h-[72vh]'>
          <Form {...form}>
            <form
              id={UPDATE_CONFIG_FORM_ID}
              onSubmit={form.handleSubmit(onSubmit)}
              autoComplete='off'
              className='space-y-4'
            >
              <div className='grid gap-3 md:grid-cols-2 md:gap-4'>
                <FormField
                  control={form.control}
                  name='image_url'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Image')}</FormLabel>
                      <FormControl>
                        <Input placeholder='ollama/ollama:latest' {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='traffic_port'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Port')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={1}
                          max={65535}
                          value={
                            typeof field.value === 'number' ||
                            typeof field.value === 'string'
                              ? field.value
                              : ''
                          }
                          onChange={(e) => {
                            const v = e.target.value
                            field.onChange(v === '' ? undefined : Number(v))
                          }}
                          onBlur={field.onBlur}
                          name={field.name}
                          ref={field.ref}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>

              <div className='grid gap-3 md:grid-cols-2 md:gap-4'>
                <FormField
                  control={form.control}
                  name='entrypoint'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Entrypoint (space separated)')}</FormLabel>
                      <FormControl>
                        <Input placeholder='bash -lc' {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='args'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Args (space separated)')}</FormLabel>
                      <FormControl>
                        <Input placeholder='--foo bar' {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>

              <FormField
                control={form.control}
                name='command'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Command')}</FormLabel>
                    <FormControl>
                      <Input placeholder='Optional' {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <Collapsible className='rounded-md border p-3'>
                <CollapsibleTrigger className='cursor-pointer text-sm'>
                  {t('Registry (optional)')}
                </CollapsibleTrigger>
                <CollapsibleContent>
                  <div className='mt-3 grid gap-3 md:grid-cols-2 md:gap-4'>
                    <FormField
                      control={form.control}
                      name='registry_username'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Registry username')}</FormLabel>
                          <FormControl>
                            <Input autoComplete='off' {...field} />
                          </FormControl>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                    <FormField
                      control={form.control}
                      name='registry_secret'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Registry secret')}</FormLabel>
                          <FormControl>
                            <Input
                              type='password'
                              autoComplete='off'
                              {...field}
                            />
                          </FormControl>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>
                </CollapsibleContent>
              </Collapsible>

              <Collapsible className='rounded-md border p-3'>
                <CollapsibleTrigger className='cursor-pointer text-sm'>
                  {t('Environment variables')}
                </CollapsibleTrigger>
                <CollapsibleContent>
                  <div className='mt-3 grid gap-3 md:grid-cols-2 md:gap-4'>
                    <FormField
                      control={form.control}
                      name='env_json'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Env (JSON object)')}</FormLabel>
                          <FormControl>
                            <JsonCodeEditor
                              value={field.value || ''}
                              onChange={field.onChange}
                              name={field.name}
                              onBlur={field.onBlur}
                              textareaRef={field.ref}
                              placeholder='{"KEY":"VALUE"}'
                              heightClassName='h-40 min-h-40 max-h-40'
                            />
                          </FormControl>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                    <FormField
                      control={form.control}
                      name='secret_env_json'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Secret env (JSON object)')}</FormLabel>
                          <FormControl>
                            <JsonCodeEditor
                              value={field.value || ''}
                              onChange={field.onChange}
                              name={field.name}
                              onBlur={field.onBlur}
                              textareaRef={field.ref}
                              placeholder='{"SECRET":"VALUE"}'
                              heightClassName='h-40 min-h-40 max-h-40'
                            />
                          </FormControl>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  </div>
                </CollapsibleContent>
              </Collapsible>
            </form>
          </Form>
        </div>
      )}
    </Dialog>
  )
}
