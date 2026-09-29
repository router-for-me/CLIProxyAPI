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
import { useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  SideDrawerSection,
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { JsonEditor } from '@/components/json-editor'
import { StatusBadge } from '@/components/status-badge'
import { TagInput } from '@/components/tag-input'
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
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'

import { createPrefillGroup, updatePrefillGroup } from '../../api'
import { ENDPOINT_TEMPLATES } from '../../constants'
import { prefillGroupsQueryKeys } from '../../lib'
import {
  prefillGroupFormSchema,
  type PrefillGroup,
  type PrefillGroupFormValues,
} from '../../types'
import {
  DEFAULT_FORM_VALUES,
  PREFILL_GROUP_TYPE_META,
  PREFILL_GROUP_TYPES,
  type PrefillGroupType,
  parseStringItems,
  serializeEndpointItems,
} from '../prefill-group-shared'

type PrefillGroupFormDrawerProps = {
  open: boolean
  onClose: () => void
  currentGroup: PrefillGroup | null
}

export function PrefillGroupFormDrawer({
  open,
  onClose,
  currentGroup,
}: PrefillGroupFormDrawerProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const isEdit = Boolean(currentGroup?.id)
  const [isSaving, setIsSaving] = useState(false)

  const form = useForm<PrefillGroupFormValues>({
    resolver: zodResolver(prefillGroupFormSchema),
    defaultValues: DEFAULT_FORM_VALUES,
  })

  const selectedType = form.watch('type')

  useEffect(() => {
    if (open) {
      if (isEdit && currentGroup) {
        form.reset({
          id: currentGroup.id,
          name: currentGroup.name,
          description: currentGroup.description || '',
          type: currentGroup.type,
          items:
            currentGroup.type === 'endpoint'
              ? serializeEndpointItems(currentGroup.items)
              : parseStringItems(currentGroup.items),
        })
      } else {
        form.reset(DEFAULT_FORM_VALUES)
      }
    }
  }, [open, isEdit, currentGroup, form])

  useEffect(() => {
    const currentItems = form.getValues('items')
    if (selectedType === 'endpoint' && Array.isArray(currentItems)) {
      form.setValue('items', '', { shouldValidate: false })
    } else if (
      selectedType !== 'endpoint' &&
      typeof currentItems === 'string'
    ) {
      form.setValue('items', [], { shouldValidate: false })
    }
  }, [selectedType, form])

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      onClose()
    }
  }

  const handleSubmit = async (values: PrefillGroupFormValues) => {
    setIsSaving(true)
    let items: string | string[] = []
    if (values.type === 'endpoint') {
      items = typeof values.items === 'string' ? values.items : ''
    } else if (Array.isArray(values.items)) {
      items = values.items
    }
    const payload = {
      name: values.name.trim(),
      type: values.type,
      description: values.description?.trim() || '',
      items,
    }

    try {
      const response =
        isEdit && currentGroup
          ? await updatePrefillGroup({
              id: currentGroup.id,
              ...payload,
            })
          : await createPrefillGroup(payload)

      if (response.success) {
        toast.success(
          isEdit ? 'Prefill group updated' : 'Prefill group created'
        )
        queryClient.invalidateQueries({
          queryKey: prefillGroupsQueryKeys.lists(),
        })
        onClose()
      } else {
        handleServerError(response, t('Operation failed'))
      }
    } catch (err: unknown) {
      handleServerError(err, t('Operation failed'))
    } finally {
      setIsSaving(false)
    }
  }

  const meta =
    PREFILL_GROUP_TYPE_META[selectedType] || PREFILL_GROUP_TYPE_META.model

  return (
    <Sheet open={open} onOpenChange={handleOpenChange}>
      <SheetContent className={sideDrawerContentClassName('sm:max-w-2xl')}>
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>
            {isEdit ? t('Edit Prefill Group') : t('Create Prefill Group')}
          </SheetTitle>
          <SheetDescription>
            {isEdit
              ? t('Update the reusable bundle below.')
              : t('Capture a reusable bundle of models, tags, or endpoints.')}
          </SheetDescription>
        </SheetHeader>

        <Form {...form}>
          <form
            id='prefill-group-form'
            onSubmit={form.handleSubmit(handleSubmit)}
            className={sideDrawerFormClassName()}
          >
            <SideDrawerSection>
              <div className='flex flex-col gap-1'>
                <h3 className='text-sm font-semibold'>{t('Group details')}</h3>
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'Give the group a recognizable name and optional description.'
                  )}
                </p>
              </div>

              <FormField
                control={form.control}
                name='name'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Group Name')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('Premium chat models')}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Give this group a recognizable name.')}
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
                        placeholder={t(
                          'Optional notes about when to use this group'
                        )}
                        rows={3}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Make it easier for teammates to pick the right group.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SideDrawerSection>

            <SideDrawerSection>
              <div className='flex flex-col gap-1'>
                <h3 className='text-sm font-semibold'>{t('Configuration')}</h3>
                <p className='text-muted-foreground text-sm'>
                  {t('Choose the bundle type and define the items inside it.')}
                </p>
              </div>

              <FormField
                control={form.control}
                name='type'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Group Type</FormLabel>
                    <Select
                      items={PREFILL_GROUP_TYPES.map((type) => ({
                        value: type.value,
                        label: (
                          <div className='flex flex-col text-left'>
                            <span className='font-medium'>{type.label}</span>
                            <span
                              data-prefill-description
                              className='text-muted-foreground text-xs'
                            >
                              {type.description}
                            </span>
                          </div>
                        ),
                      }))}
                      value={field.value}
                      onValueChange={(value) =>
                        value !== null &&
                        field.onChange(value as PrefillGroupType)
                      }
                    >
                      <FormControl>
                        <SelectTrigger className='[&_[data-slot=select-value]_[data-prefill-description]]:hidden'>
                          <SelectValue placeholder={t('Select a group type')} />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          {PREFILL_GROUP_TYPES.map((type) => (
                            <SelectItem key={type.value} value={type.value}>
                              <div className='flex flex-col text-left'>
                                <span className='font-medium'>
                                  {type.label}
                                </span>
                                <span
                                  data-prefill-description
                                  className='text-muted-foreground text-xs'
                                >
                                  {type.description}
                                </span>
                              </div>
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {t('Determines how this group is applied elsewhere.')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <div className='border-border/60 flex flex-col gap-3 border-y py-4'>
                <div className='flex items-center gap-2'>
                  <h4 className='text-sm font-medium'>{t('Project')}</h4>
                  <StatusBadge
                    label={meta.label}
                    variant={meta.badge}
                    size='sm'
                    copyable={false}
                  />
                </div>
                <FormField
                  control={form.control}
                  name='items'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel className='sr-only'>{t('Items')}</FormLabel>
                      <FormControl>
                        {selectedType === 'endpoint' ? (
                          <JsonEditor
                            value={(field.value as string) || ''}
                            onChange={field.onChange}
                            keyPlaceholder='provider'
                            valuePlaceholder='{"path": "/v1/...","method": "POST"}'
                            keyLabel={t('Provider')}
                            valueLabel={t('Endpoint config')}
                            valueType='any'
                            template={ENDPOINT_TEMPLATES}
                            emptyMessage={t(
                              'Define endpoint mappings for each provider.'
                            )}
                          />
                        ) : (
                          <TagInput
                            value={
                              Array.isArray(field.value) ? field.value : []
                            }
                            onChange={field.onChange}
                            placeholder={t('Enter a value and press Enter')}
                          />
                        )}
                      </FormControl>
                      <FormDescription>
                        {selectedType === 'endpoint'
                          ? t(
                              'Provide a JSON object where each key maps to an endpoint definition.'
                            )
                          : t('Add each model or tag you want to include.')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>
            </SideDrawerSection>
          </form>
        </Form>

        <SheetFooter className={sideDrawerFooterClassName()}>
          <SheetClose
            render={
              <Button type='button' variant='outline' disabled={isSaving} />
            }
          >
            {t('Cancel')}
          </SheetClose>
          <Button type='submit' form='prefill-group-form' disabled={isSaving}>
            {isSaving && <Loader2 className='mr-2 h-4 w-4 animate-spin' />}
            {isSaving && t('Saving...')}
            {!isSaving && isEdit && t('Save changes')}
            {!isSaving && !isEdit && t('Create')}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
