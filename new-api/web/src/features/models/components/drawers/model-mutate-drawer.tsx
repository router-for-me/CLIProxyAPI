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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  SideDrawerSection,
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
  sideDrawerSwitchItemClassName,
} from '@/components/drawer-layout'
import { ErrorState } from '@/components/error-state'
import { JsonEditor } from '@/components/json-editor'
import { LoadingState } from '@/components/loading-state'
import { LobeIconField } from '@/components/lobe-icon-field'
import { TagInput } from '@/components/tag-input'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
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
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { ModelPricingPanel } from '@/features/model-pricing/model-pricing-panel'
import {
  requireServerSuccess,
  createServerError,
  getServerErrorMessage,
} from '@/lib/server-error-message'

import { createModel, updateModel, getModel, getVendors } from '../../api'
import { getNameRuleOptions, ENDPOINT_TEMPLATES } from '../../constants'
import { modelsQueryKeys, vendorsQueryKeys } from '../../lib'
import {
  modelFormSchema,
  transformModelToFormDefaults,
  transformFormDataToModelPayload,
  type ModelFormValues,
} from '../../lib/model-form'
import type { Model } from '../../types'
import { ModelConnections } from '../model-connections'

export function ModelMutateDrawer(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  currentRow?: Model | null
  initialSection?: 'metadata' | 'pricing'
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [createdModel, setCreatedModel] = useState<{
    source: Model | null | undefined
    model: Model
  } | null>(null)
  const currentRow =
    createdModel && createdModel.source === props.currentRow
      ? createdModel.model
      : props.currentRow
  const isEditing = Boolean(currentRow?.id)
  const hasModelName = Boolean(currentRow?.model_name)
  const [section, setSection] = useState<string>(
    props.initialSection ?? 'metadata'
  )
  const [pricingName, setPricingName] = useState('')
  const [pricingVisited, setPricingVisited] = useState(
    props.initialSection === 'pricing'
  )
  const [pricingDirty, setPricingDirty] = useState(false)
  const [pendingPricingName, setPendingPricingName] = useState<string | null>(
    null
  )
  const [closeConfirm, setCloseConfirm] = useState(false)
  const loadedKey = useRef('')
  const form = useForm({
    resolver: zodResolver(modelFormSchema),
    defaultValues: transformModelToFormDefaults({
      model_name: '',
      status: 1,
      sync_official: 1,
      name_rule: 0,
    } as Model),
  })
  const vendorsQuery = useQuery({
    queryKey: vendorsQueryKeys.list(),
    queryFn: async () =>
      requireServerSuccess(await getVendors({ page_size: 1000 })),
    enabled: props.open,
  })
  const vendors = vendorsQuery.data?.data?.items ?? []
  const selectedVendor = vendors.find(
    (vendor) => vendor.id === form.watch('vendor_id')
  )
  const modelQuery = useQuery({
    queryKey: modelsQueryKeys.detail(currentRow?.id ?? 0),
    queryFn: async () => {
      if (!currentRow?.id) throw new Error(t('Model ID is required'))
      const response = await getModel(currentRow.id)
      if (!response.success || !response.data) {
        throw createServerError(response, t('Failed to load model'))
      }
      return response.data
    },
    enabled: props.open && isEditing,
  })
  const savedModel = modelQuery.data ?? currentRow

  useEffect(() => {
    if (!props.open) return
    setSection(props.initialSection ?? 'metadata')
    setPricingVisited(props.initialSection === 'pricing')
    setPricingName('')
    setPricingDirty(false)
    setPendingPricingName(null)
    setCloseConfirm(false)
  }, [
    props.open,
    props.initialSection,
    props.currentRow?.id,
    props.currentRow?.model_name,
  ])

  useEffect(() => {
    if (!props.open) {
      setCreatedModel(null)
      loadedKey.current = ''
      return
    }
    const key = currentRow?.id
      ? `metadata:${currentRow.id}`
      : `channel:${currentRow?.model_name ?? ''}`
    if (loadedKey.current === key || (isEditing && !modelQuery.data)) return
    form.reset(
      transformModelToFormDefaults(
        (isEditing
          ? modelQuery.data
          : {
              model_name: currentRow?.model_name ?? '',
              status: 1,
              sync_official: 1,
              name_rule: 0,
            }) as Model
      )
    )
    loadedKey.current = key
  }, [props.open, currentRow, isEditing, modelQuery.data, form])

  const save = useMutation({
    meta: { errorToast: false },
    onMutate: () => form.clearErrors('root.server'),
    mutationFn: async (values: ModelFormValues) => {
      if (pricingDirty && values.model_name !== currentRow?.model_name) {
        throw new Error(
          t('Save or discard pricing changes before renaming metadata.')
        )
      }
      const payload = transformFormDataToModelPayload(values)
      const response = currentRow?.id
        ? await updateModel({ ...payload, id: currentRow.id })
        : await createModel(payload)
      if (!response.success) {
        throw createServerError(response, t('Operation failed'))
      }
      return response
    },
    onSuccess: async (response) => {
      form.reset(form.getValues())
      if (response.data?.id) {
        if (!currentRow?.id) {
          setCreatedModel({ source: props.currentRow, model: response.data })
        }
        queryClient.setQueryData(
          modelsQueryKeys.detail(response.data.id),
          response.data
        )
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: modelsQueryKeys.all }),
        queryClient.invalidateQueries({ queryKey: ['pricing'] }),
        queryClient.invalidateQueries({ queryKey: vendorsQueryKeys.all }),
      ])
      toast.success(t('Model metadata saved'))
      if (!pricingDirty) props.onOpenChange(false)
    },
    onError: (error) => {
      form.setError('root.server', {
        message: getServerErrorMessage(error, t('Operation failed')),
      })
    },
  })
  const isSubmitting = save.isPending
  const handleFillEndpointTemplate = (key: string) => {
    const template = ENDPOINT_TEMPLATES[key]
    if (template) {
      form.setValue('endpoints', JSON.stringify({ [key]: template }, null, 2), {
        shouldDirty: true,
      })
    }
  }
  const metadataDirty = form.formState.isDirty
  const close = (open: boolean) => {
    if (!open && isSubmitting) return
    if (!open && (metadataDirty || pricingDirty)) {
      setCloseConfirm(true)
      return
    }
    props.onOpenChange(open)
  }

  return (
    <>
      <Sheet open={props.open} onOpenChange={close}>
        <SheetContent
          className={sideDrawerContentClassName('sm:max-w-[1280px]')}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle className='pr-6 break-all'>
              {hasModelName ? currentRow?.model_name : t('Create Model')}
            </SheetTitle>
            <SheetDescription>
              {t(
                'Manage metadata, pricing, and channel connections. Each section saves separately.'
              )}
            </SheetDescription>
          </SheetHeader>
          <Tabs
            value={section}
            onValueChange={(value) => {
              setSection(value)
              if (value === 'pricing') setPricingVisited(true)
            }}
            className='shrink-0 px-4'
          >
            <TabsList className='grid w-full grid-cols-3 group-data-horizontal/tabs:h-auto'>
              <TabsTrigger
                value='metadata'
                className='h-auto min-w-0 whitespace-normal'
              >
                {t('Model metadata')}
              </TabsTrigger>
              <TabsTrigger
                value='pricing'
                disabled={!hasModelName}
                className='h-auto min-w-0 whitespace-normal'
              >
                {t('Pricing')}
              </TabsTrigger>
              <TabsTrigger
                value='connections'
                disabled={!hasModelName}
                className='h-auto min-w-0 whitespace-normal'
              >
                {t('Channels and groups')}
              </TabsTrigger>
            </TabsList>
          </Tabs>
          {props.open && section === 'metadata' && (
            <>
              {modelQuery.isError ? (
                <ErrorState
                  description={modelQuery.error.message}
                  onRetry={() => void modelQuery.refetch()}
                />
              ) : null}
              {isEditing && modelQuery.isPending && <LoadingState />}
              {!modelQuery.isError && !(isEditing && modelQuery.isPending) && (
                <Form {...form}>
                  <form
                    id='model-form'
                    onSubmit={form.handleSubmit((values) =>
                      save.mutate(values)
                    )}
                    className={sideDrawerFormClassName()}
                  >
                    {/* Basic Information */}
                    <SideDrawerSection>
                      <h3 className='text-sm font-semibold'>
                        {t('Basic Information')}
                      </h3>

                      <FormField
                        control={form.control}
                        name='model_name'
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel required>{t('Model Name')}</FormLabel>
                            <FormControl>
                              <Input
                                placeholder={t('gpt-4, claude-3-opus, etc.')}
                                {...field}
                              />
                            </FormControl>
                            <FormDescription>
                              {t('The unique identifier for this model')}
                              {isEditing &&
                                form.watch('model_name') !==
                                  currentRow?.model_name && (
                                  <span className='text-warning mt-1 block'>
                                    {t(
                                      'Renaming metadata does not rename channel models or move pricing. Existing prices stay with the original model name.'
                                    )}
                                  </span>
                                )}
                            </FormDescription>
                            <FormMessage />
                          </FormItem>
                        )}
                      />

                      <FormField
                        control={form.control}
                        name='description'
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel>{t('Description')}</FormLabel>
                            <FormControl>
                              <Textarea
                                placeholder={t('Describe this model...')}
                                rows={3}
                                {...field}
                              />
                            </FormControl>
                            <FormMessage />
                          </FormItem>
                        )}
                      />

                      <FormField
                        control={form.control}
                        name='icon'
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel>{t('Icon')}</FormLabel>
                            <FormControl>
                              <LobeIconField
                                key={`${currentRow?.id ?? 'new'}-${props.open}`}
                                value={field.value ?? ''}
                                onChange={field.onChange}
                                allowInheritance
                                inheritedIcon={selectedVendor?.icon}
                                inheritedName={selectedVendor?.name}
                              />
                            </FormControl>
                            <FormMessage />
                          </FormItem>
                        )}
                      />

                      <FormField
                        control={form.control}
                        name='vendor_id'
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel>{t('Vendor')}</FormLabel>
                            <FormControl>
                              <Combobox
                                options={vendors.map((vendor) => ({
                                  value: String(vendor.id),
                                  label: vendor.name,
                                }))}
                                onValueChange={(value) =>
                                  field.onChange(
                                    value ? Number.parseInt(value) : undefined
                                  )
                                }
                                value={field.value ? String(field.value) : null}
                                className='w-full'
                                placeholder={t('Select vendor')}
                              />
                            </FormControl>
                            <FormMessage />
                          </FormItem>
                        )}
                      />

                      <FormField
                        control={form.control}
                        name='tags'
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel>{t('Tags')}</FormLabel>
                            <FormControl>
                              <TagInput
                                value={field.value || []}
                                onChange={field.onChange}
                                placeholder={t('Add tags...')}
                              />
                            </FormControl>
                            <FormDescription>
                              {t('Press Enter or comma to add tags')}
                            </FormDescription>
                            <FormMessage />
                          </FormItem>
                        )}
                      />
                    </SideDrawerSection>

                    {/* Matching Configuration */}
                    <SideDrawerSection>
                      <h3 className='text-sm font-semibold'>
                        {t('Matching Rules')}
                      </h3>

                      <FormField
                        control={form.control}
                        name='name_rule'
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel>{t('Name Rule')}</FormLabel>
                            <FormControl>
                              <RadioGroup
                                onValueChange={(value) =>
                                  field.onChange(Number.parseInt(value))
                                }
                                value={String(field.value)}
                                className='grid grid-cols-2 gap-4'
                              >
                                {getNameRuleOptions(t).map((option) => (
                                  <div
                                    key={option.value}
                                    className='flex items-center space-x-2'
                                  >
                                    <RadioGroupItem
                                      value={String(option.value)}
                                      id={`rule-${option.value}`}
                                    />
                                    <Label
                                      htmlFor={`rule-${option.value}`}
                                      className='cursor-pointer font-normal'
                                    >
                                      {option.label}
                                    </Label>
                                  </div>
                                ))}
                              </RadioGroup>
                            </FormControl>
                            <FormDescription>
                              {t(
                                'Matching rules apply to metadata. Pricing is configured for each concrete model.'
                              )}
                            </FormDescription>
                            <FormMessage />
                          </FormItem>
                        )}
                      />
                    </SideDrawerSection>

                    {/* Endpoints Configuration */}
                    <SideDrawerSection>
                      <div className='flex items-center justify-between'>
                        <h3 className='text-sm font-semibold'>
                          {t('Endpoints')}
                        </h3>
                        <Combobox
                          options={Object.keys(ENDPOINT_TEMPLATES).map(
                            (key) => ({ value: key, label: key })
                          )}
                          onValueChange={(value: string | null) => {
                            if (value) handleFillEndpointTemplate(value)
                          }}
                          className='w-[200px]'
                          placeholder={t('Load template...')}
                          aria-label={t('Load template...')}
                        />
                      </div>

                      <FormField
                        control={form.control}
                        name='endpoints'
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel>{t('Endpoint Configuration')}</FormLabel>
                            <FormControl>
                              <JsonEditor
                                value={field.value || ''}
                                onChange={field.onChange}
                                keyPlaceholder='endpoint_type'
                                valuePlaceholder='{"path": "/v1/...", "method": "POST"}'
                                keyLabel='Endpoint Type'
                                valueLabel='Configuration'
                                valueType='any'
                                emptyMessage={t(
                                  'No endpoints configured. Switch to JSON mode or add rows to define endpoints.'
                                )}
                              />
                            </FormControl>
                            <FormDescription>
                              {t(
                                'Define API endpoints for this model (JSON format)'
                              )}
                            </FormDescription>
                            <FormMessage />
                          </FormItem>
                        )}
                      />
                    </SideDrawerSection>

                    {/* Status & Sync */}
                    <SideDrawerSection>
                      <h3 className='text-sm font-semibold'>
                        {t('Status & Sync')}
                      </h3>

                      <FormField
                        control={form.control}
                        name='status'
                        render={({ field }) => (
                          <FormItem className={sideDrawerSwitchItemClassName()}>
                            <div className='flex flex-col gap-0.5'>
                              <FormLabel className='text-base'>
                                {t('Model square visibility')}
                              </FormLabel>
                              <FormDescription>
                                {t(
                                  'Allow listing when a channel is available and the user has group access. This does not change API access.'
                                )}
                              </FormDescription>
                            </div>
                            <FormControl>
                              <Switch
                                checked={field.value}
                                onCheckedChange={field.onChange}
                              />
                            </FormControl>
                          </FormItem>
                        )}
                      />

                      <FormField
                        control={form.control}
                        name='sync_official'
                        render={({ field }) => (
                          <FormItem className={sideDrawerSwitchItemClassName()}>
                            <div className='flex flex-col gap-0.5'>
                              <FormLabel className='text-base'>
                                {t('Allow metadata sync')}
                              </FormLabel>
                              <FormDescription>
                                {t(
                                  'Allows selected fields to be overwritten after a sync preview. No automatic synchronization.'
                                )}
                              </FormDescription>
                            </div>
                            <FormControl>
                              <Switch
                                checked={field.value}
                                onCheckedChange={field.onChange}
                              />
                            </FormControl>
                          </FormItem>
                        )}
                      />
                    </SideDrawerSection>
                  </form>
                </Form>
              )}
              <SheetFooter className={sideDrawerFooterClassName('flex-wrap')}>
                {form.formState.errors.root?.server?.message && (
                  <Alert
                    variant='destructive'
                    className='col-span-2 basis-full'
                  >
                    <AlertDescription className='break-words'>
                      {form.formState.errors.root.server.message}
                    </AlertDescription>
                  </Alert>
                )}
                <Button
                  variant='outline'
                  onClick={() => close(false)}
                  disabled={isSubmitting}
                >
                  {t('Close')}
                </Button>
                <Button
                  form='model-form'
                  type='submit'
                  disabled={
                    isSubmitting ||
                    modelQuery.isError ||
                    (isEditing && modelQuery.isPending)
                  }
                >
                  {isSubmitting ? t('Saving...') : t('Save metadata')}
                </Button>
              </SheetFooter>
            </>
          )}
          {props.open && pricingVisited && savedModel && (
            <div
              className={
                section === 'pricing'
                  ? 'flex min-h-0 flex-1 flex-col'
                  : 'hidden'
              }
            >
              {savedModel.name_rule !== 0 && (
                <div className='space-y-2 p-4'>
                  <p className='text-muted-foreground text-sm'>
                    {t(
                      'Select a concrete model to configure pricing. Metadata matching does not propagate prices.'
                    )}
                  </p>
                  <Combobox
                    value={pricingName}
                    onValueChange={(value) => {
                      if (pricingDirty) {
                        setPendingPricingName(value ?? '')
                        setCloseConfirm(true)
                      } else {
                        setPricingName(value ?? '')
                      }
                    }}
                    options={(savedModel.matched_models ?? []).map((name) => ({
                      value: name,
                      label: name,
                    }))}
                    aria-label={t('Select model')}
                    className='w-full'
                    placeholder={t('Select model')}
                  />
                </div>
              )}
              {(savedModel.name_rule === 0 || pricingName) && (
                <ModelPricingPanel
                  onDirtyChange={setPricingDirty}
                  key={
                    savedModel.name_rule === 0
                      ? savedModel.model_name
                      : pricingName
                  }
                  modelName={
                    savedModel.name_rule === 0
                      ? savedModel.model_name
                      : pricingName
                  }
                />
              )}
            </div>
          )}
          {props.open && section === 'connections' && savedModel && (
            <ModelConnections model={savedModel} />
          )}
        </SheetContent>
      </Sheet>
      <ConfirmDialog
        open={closeConfirm}
        onOpenChange={setCloseConfirm}
        title={t('Discard unsaved changes?')}
        desc={t('Your changes have not been saved.')}
        confirmText={t('Discard changes')}
        handleConfirm={() => {
          setCloseConfirm(false)
          if (pendingPricingName !== null) {
            setPricingName(pendingPricingName)
            setPendingPricingName(null)
            setPricingDirty(false)
          } else {
            props.onOpenChange(false)
          }
        }}
      />
    </>
  )
}
