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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import { previewUpstreamDiff, syncUpstream } from '../../api'
import { getSyncLocaleOptions } from '../../constants'
import type {
  MetadataSyncCandidate,
  MetadataSyncField,
  MetadataSyncPreview,
  MetadataSyncSelection,
  SyncLocale,
} from '../../types'

const FIELD_LABELS: Record<MetadataSyncField, string> = {
  description: 'Description',
  icon: 'Icon',
  tags: 'Tags',
  vendor: 'Vendor',
  endpoints: 'Custom endpoints',
  name_rule: 'Match Type',
  status: 'Model square visibility',
}
const REASON_LABELS = {
  create: 'New metadata',
  update: 'Metadata changes',
  unchanged: 'No changes',
  blocked: 'Metadata sync disabled',
  missing_upstream: 'Not found upstream',
  missing_vendor: 'Upstream vendor missing',
}
const STEPS = [
  'Select models',
  'Preview fields',
  'Confirm changes',
  'Sync results',
]
const PAGE_SIZE = 20

function isMetadataSyncable(item: MetadataSyncCandidate) {
  return item.kind === 'create' || item.kind === 'update'
}

export function SyncWizardDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [locale, setLocale] = useState<SyncLocale>('zh')
  const [preview, setPreview] = useState<MetadataSyncPreview | null>(null)
  const [scope, setScope] = useState('site')
  const [search, setSearch] = useState('')
  const [onlySyncable, setOnlySyncable] = useState(false)
  const [step, setStep] = useState(0)
  const [page, setPage] = useState(0)
  const [selection, setSelection] = useState<
    Record<string, MetadataSyncField[]>
  >({})
  const load = useMutation({
    onMutate: () => {
      setPreview(null)
      setSelection({})
      setStep(0)
      setPage(0)
    },
    mutationFn: async () => {
      const response = await previewUpstreamDiff({ locale })
      if (!response.success || !response.data) {
        throw createServerError(response, t('Failed to preview metadata'))
      }
      return response.data
    },
    onSuccess: (data) => {
      setPreview(data)
    },
    onError: (error) => handleServerError(error),
  })

  const apply = useMutation({
    mutationFn: async (selections: MetadataSyncSelection[]) => {
      if (!preview) throw new Error(t('Preview metadata first'))
      const response = await syncUpstream({
        locale: preview.source.locale,
        source_version: preview.source.version,
        selections,
      })
      if (!response.success || !response.data) {
        throw createServerError(response, t('Metadata sync failed'))
      }
      return response.data
    },
    onSuccess: async () => {
      await Promise.all(
        ['models', 'vendors', 'pricing'].map((key) =>
          queryClient.invalidateQueries({ queryKey: [key] })
        )
      )
      setStep(3)
    },
    onError: (error) => handleServerError(error),
  })

  useEffect(() => {
    if (props.open) {
      setPreview(null)
      setSelection({})
      setStep(0)
      setSearch('')
      setPage(0)
      setScope('site')
      setOnlySyncable(false)
      load.reset()
      apply.reset()
    }
    // Reset only when the dialog opens; mutation objects change on each render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.open])

  const filteredCandidates = useMemo(() => {
    const keyword = search.trim().toLowerCase()
    return (preview?.candidates ?? []).filter(
      (item) =>
        (scope === 'catalog' || item.scope === 'site') &&
        item.model_name.toLowerCase().includes(keyword)
    )
  }, [preview, scope, search])
  const syncableCandidates = useMemo(
    () => filteredCandidates.filter(isMetadataSyncable),
    [filteredCandidates]
  )
  const candidates = useMemo(
    () =>
      onlySyncable
        ? syncableCandidates
        : [
            ...syncableCandidates,
            ...filteredCandidates.filter((item) => !isMetadataSyncable(item)),
          ],
    [filteredCandidates, syncableCandidates, onlySyncable]
  )
  const visibleNames = useMemo(
    () => new Set(candidates.map((item) => item.model_name)),
    [candidates]
  )
  const pageCount = Math.max(1, Math.ceil(candidates.length / PAGE_SIZE))
  const currentPage = Math.min(page, pageCount - 1)
  const pageCandidates = candidates.slice(
    currentPage * PAGE_SIZE,
    (currentPage + 1) * PAGE_SIZE
  )
  const pageSyncable = pageCandidates.filter(isMetadataSyncable)
  const pageSelectedCount = pageSyncable.filter((item) =>
    Object.hasOwn(selection, item.model_name)
  ).length
  const selected = useMemo(
    () =>
      (preview?.candidates ?? []).filter((item) =>
        Object.hasOwn(selection, item.model_name)
      ),
    [preview, selection]
  )
  const hiddenSelectedCount = selected.filter(
    (item) => !visibleNames.has(item.model_name)
  ).length
  const selectedExistingModels = selected.filter(
    (item) => item.kind === 'update'
  )
  const changedFieldCount = selectedExistingModels.reduce(
    (total, item) => total + item.fields.length,
    0
  )
  const selectedFieldCount = selectedExistingModels.reduce(
    (total, item) => total + selection[item.model_name].length,
    0
  )
  const updates: MetadataSyncSelection[] = selected
    .filter(
      (item) => item.kind === 'create' || selection[item.model_name].length > 0
    )
    .map((item) => ({
      model_name: item.model_name,
      record_version: item.record_version,
      create: item.kind === 'create',
      fields:
        item.kind === 'create'
          ? item.fields.map((field) => field.field)
          : selection[item.model_name],
    }))
  const vendors = [
    ...new Set(
      selected
        .filter(
          (item) =>
            item.kind === 'create' ||
            selection[item.model_name].includes('vendor')
        )
        .map((item) => item.vendor_to_create)
        .filter(Boolean)
    ),
  ]
  const busy = load.isPending || apply.isPending
  const chooseModels = (items: MetadataSyncCandidate[], checked: boolean) =>
    setSelection((previous) => {
      const eligible = items.filter(isMetadataSyncable)
      if (checked) {
        return {
          ...previous,
          ...Object.fromEntries(
            eligible.map((item) => [
              item.model_name,
              Object.hasOwn(previous, item.model_name)
                ? previous[item.model_name]
                : [],
            ])
          ),
        }
      }
      const next = { ...previous }
      for (const item of eligible) {
        delete next[item.model_name]
      }
      return next
    })
  const chooseField = (
    name: string,
    field: MetadataSyncField,
    checked: boolean
  ) =>
    setSelection((previous) => ({
      ...previous,
      [name]: checked
        ? [...previous[name], field]
        : previous[name].filter((value) => value !== field),
    }))
  const chooseAllFields = (checked: boolean) =>
    setSelection((previous) => {
      const next = { ...previous }
      for (const item of selectedExistingModels) {
        next[item.model_name] = checked
          ? item.fields.map((field) => field.field)
          : []
      }
      return next
    })
  const impact = (field: MetadataSyncField) => {
    if (field === 'name_rule') {
      return t(
        'Changes which model names inherit this metadata. Prices are not inherited.'
      )
    }
    if (field === 'status') {
      return t(
        'Changes visibility in the model square. Channel status and existing API access are unchanged.'
      )
    }
    if (field === 'endpoints') {
      return t(
        'Changes declared endpoint types and custom paths shown to users.'
      )
    }
    return ''
  }
  const display = (field: MetadataSyncField, value: string | number) => {
    if (field === 'status') {
      return value === 1 ? t('Shown') : t('Not shown')
    }
    if (field === 'name_rule') {
      return (
        [t('Exact'), t('Prefix'), t('Contains'), t('Suffix')][Number(value)] ??
        String(value)
      )
    }
    return String(value || '—')
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={(open) => !busy && props.onOpenChange(open)}
      title={t('Sync model metadata')}
      description={t(
        'Review every addition and overwrite before applying. Pricing, channels, and group access are not changed.'
      )}
      contentClassName='sm:max-w-5xl'
      contentHeight='min(68vh, 720px)'
      showCloseButton={!busy}
      footer={
        <>
          <div className='text-muted-foreground mr-auto min-w-0 text-sm'>
            <p>{t('{{count}} selected models', { count: selected.length })}</p>
            {step === 0 && hiddenSelectedCount > 0 && (
              <p className='text-xs'>
                {t('{{count}} selected outside the current filters', {
                  count: hiddenSelectedCount,
                })}
              </p>
            )}
          </div>
          <Button
            variant='outline'
            disabled={busy}
            onClick={() => props.onOpenChange(false)}
          >
            {t('Close')}
          </Button>
          {step > 0 && step < 3 && (
            <Button
              variant='outline'
              disabled={busy}
              onClick={() => setStep(step - 1)}
            >
              {t('Back')}
            </Button>
          )}
          {step < 2 && (
            <Button
              disabled={
                busy ||
                !preview ||
                (step === 0 ? selected.length === 0 : updates.length === 0)
              }
              onClick={() => setStep(step + 1)}
            >
              {step === 0
                ? t('Preview selected changes')
                : t('Review confirmation')}
            </Button>
          )}
          {step === 2 && (
            <Button
              disabled={busy || updates.length === 0}
              onClick={() => apply.mutate(updates)}
            >
              {busy
                ? t('Applying...')
                : t('Apply {{count}} model changes', { count: updates.length })}
            </Button>
          )}
        </>
      }
    >
      <ol
        className='mb-5 grid grid-cols-2 gap-2 sm:grid-cols-4'
        aria-label={t('Sync steps')}
      >
        {STEPS.map((label, index) => (
          <li
            key={label}
            aria-current={step === index ? 'step' : undefined}
            className={
              step === index
                ? 'border-primary bg-primary/5 rounded-lg border p-2 text-sm font-medium'
                : 'text-muted-foreground rounded-lg border p-2 text-sm'
            }
          >
            {index + 1}. {t(label)}
          </li>
        ))}
      </ol>
      {step === 0 && (
        <div className='space-y-4'>
          <div className='flex flex-wrap items-end gap-3'>
            <div className='space-y-1'>
              <Label>{t('Metadata language')}</Label>
              <Select
                value={locale}
                disabled={busy}
                items={getSyncLocaleOptions(t)}
                onValueChange={(value) => {
                  if (value) setLocale(value as SyncLocale)
                  setPreview(null)
                  setSelection({})
                  setPage(0)
                  load.reset()
                }}
              >
                <SelectTrigger
                  className='w-44'
                  aria-label={t('Metadata language')}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {getSyncLocaleOptions(t).map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button
              variant='outline'
              disabled={busy}
              onClick={() => load.mutate()}
            >
              {t('Load metadata preview')}
            </Button>
          </div>
          {load.isPending && <LoadingState />}
          {load.isError && (
            <ErrorState
              description={
                load.error instanceof Error
                  ? load.error.message
                  : t('Failed to preview metadata')
              }
              onRetry={() => load.mutate()}
            />
          )}
          {preview && (
            <>
              <div className='text-muted-foreground space-y-1 text-xs break-all'>
                <p>
                  {t('Models source')}: {preview.source.models_url}
                </p>
                <p>
                  {t('Vendors source')}: {preview.source.vendors_url}
                </p>
                <p>
                  {t('Metadata language')}: {preview.source.locale}
                </p>
              </div>
              <div className='flex flex-wrap gap-3'>
                <Select
                  value={scope}
                  onValueChange={(value) => {
                    setScope(value ?? 'site')
                    setPage(0)
                  }}
                  items={[
                    { value: 'site', label: t('Models used on this site') },
                    { value: 'catalog', label: t('Upstream model list') },
                  ]}
                >
                  <SelectTrigger
                    aria-label={t('Sync scope')}
                    className='w-full sm:w-64'
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value='site'>
                        {t('Models used on this site')}
                      </SelectItem>
                      <SelectItem value='catalog'>
                        {t('Upstream model list')}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <Input
                  className='w-full sm:w-64'
                  placeholder={t('Search models...')}
                  aria-label={t('Search models')}
                  value={search}
                  onChange={(event) => {
                    setSearch(event.target.value)
                    setPage(0)
                  }}
                />
              </div>
              <div className='space-y-3'>
                <div className='flex flex-wrap items-center gap-3'>
                  <label className='flex cursor-pointer items-center gap-2 text-sm'>
                    <Checkbox
                      checked={onlySyncable}
                      onCheckedChange={(checked) => {
                        setOnlySyncable(Boolean(checked))
                        setPage(0)
                      }}
                    />
                    {t('Only show syncable models')}
                  </label>
                  <p className='text-muted-foreground text-xs' role='status'>
                    {t(
                      '{{total}} models · {{syncable}} syncable · {{skipped}} skipped this time',
                      {
                        total: filteredCandidates.length,
                        syncable: syncableCandidates.length,
                        skipped:
                          filteredCandidates.length - syncableCandidates.length,
                      }
                    )}
                  </p>
                </div>
                <div className='flex flex-wrap gap-2'>
                  <Button
                    variant='outline'
                    size='sm'
                    className='h-auto min-h-7 max-w-full py-1.5 whitespace-normal'
                    disabled={busy || syncableCandidates.length === 0}
                    onClick={() => chooseModels(syncableCandidates, true)}
                  >
                    {t('Select all syncable models (all pages, {{count}})', {
                      count: syncableCandidates.length,
                    })}
                  </Button>
                  <Button
                    variant='ghost'
                    size='sm'
                    disabled={busy || selected.length === 0}
                    onClick={() => setSelection({})}
                  >
                    {t('Clear selection')}
                  </Button>
                </div>
                {syncableCandidates.length === 0 && candidates.length > 0 && (
                  <p className='text-muted-foreground text-sm'>
                    {t(
                      'No models can be synced with the current filters. Adjust the search or sync scope.'
                    )}
                  </p>
                )}
              </div>
              <StaticDataTable
                data={pageCandidates}
                getRowKey={(item) => item.model_name}
                emptyContent={
                  <EmptyState
                    className='min-h-36 p-4'
                    title={
                      onlySyncable
                        ? t('No syncable models')
                        : t('No models match these filters')
                    }
                    description={t(
                      'No models can be synced with the current filters. Adjust the search or sync scope.'
                    )}
                  />
                }
                columns={[
                  {
                    id: 'select',
                    header: (
                      <Checkbox
                        checked={
                          pageSyncable.length > 0 &&
                          pageSelectedCount === pageSyncable.length
                        }
                        indeterminate={
                          pageSelectedCount > 0 &&
                          pageSelectedCount < pageSyncable.length
                        }
                        disabled={busy || pageSyncable.length === 0}
                        aria-label={t('Select syncable models on this page')}
                        onCheckedChange={(checked) =>
                          chooseModels(pageSyncable, Boolean(checked))
                        }
                      />
                    ),
                    cell: (item) =>
                      isMetadataSyncable(item) ? (
                        <Checkbox
                          checked={Object.hasOwn(selection, item.model_name)}
                          disabled={busy}
                          aria-label={t('Select {{name}}', {
                            name: item.model_name,
                          })}
                          onCheckedChange={(checked) =>
                            chooseModels([item], Boolean(checked))
                          }
                        />
                      ) : (
                        <span className='text-muted-foreground'>—</span>
                      ),
                  },
                  {
                    id: 'name',
                    header: t('Model'),
                    cell: (item) => (
                      <span className='block max-w-64 font-mono text-sm break-all whitespace-normal'>
                        {item.model_name}
                      </span>
                    ),
                  },
                  {
                    id: 'kind',
                    header: t('Planned action'),
                    cell: (item) => t(REASON_LABELS[item.kind]),
                  },
                  {
                    id: 'vendor',
                    header: t('Vendor'),
                    cell: (item) => item.upstream?.vendor || '—',
                  },
                ]}
              />
              <div className='flex items-center justify-end gap-3 text-sm'>
                <span>
                  {t('{{count}} models', { count: candidates.length })}
                </span>
                <Button
                  variant='outline'
                  size='sm'
                  disabled={currentPage === 0}
                  onClick={() => setPage(currentPage - 1)}
                >
                  {t('Previous')}
                </Button>
                <span>
                  {currentPage + 1} / {pageCount}
                </span>
                <Button
                  variant='outline'
                  size='sm'
                  disabled={currentPage + 1 >= pageCount}
                  onClick={() => setPage(currentPage + 1)}
                >
                  {t('Next')}
                </Button>
              </div>
            </>
          )}
        </div>
      )}
      {step === 1 && (
        <div className='space-y-5'>
          <div className='space-y-3'>
            {selected.some((item) => item.kind === 'create') && (
              <p className='text-muted-foreground text-sm'>
                {t(
                  'New models: all metadata shown below will be imported automatically. No selection is needed.'
                )}
              </p>
            )}
            {changedFieldCount > 0 && (
              <>
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'Existing models: select fields to replace their current values with the values on the right. Unselected fields stay unchanged.'
                  )}
                </p>
                <div className='flex flex-wrap items-center gap-2'>
                  <Button
                    variant='outline'
                    size='sm'
                    className='h-auto min-h-7 max-w-full py-1.5 whitespace-normal'
                    disabled={busy || selectedFieldCount === changedFieldCount}
                    onClick={() => chooseAllFields(true)}
                  >
                    {t('Select all changed fields')}
                  </Button>
                  <Button
                    variant='ghost'
                    size='sm'
                    className='h-auto min-h-7 max-w-full py-1.5 whitespace-normal'
                    disabled={busy || selectedFieldCount === 0}
                    onClick={() => chooseAllFields(false)}
                  >
                    {t('Clear field selection')}
                  </Button>
                  <p className='text-muted-foreground text-xs' role='status'>
                    {t('{{selected}} of {{total}} changed fields selected', {
                      selected: selectedFieldCount,
                      total: changedFieldCount,
                    })}
                  </p>
                </div>
              </>
            )}
          </div>
          {selected.map((item) => (
            <section key={item.model_name} className='space-y-2'>
              <h3 className='font-mono text-sm font-semibold break-all'>
                {item.model_name} · {t(REASON_LABELS[item.kind])}
              </h3>
              <StaticDataTable
                data={item.fields}
                columns={[
                  {
                    id: 'select',
                    header: t('Apply'),
                    cell: (field) => (
                      <Checkbox
                        aria-label={t('Apply {{field}} for {{name}}', {
                          field: t(FIELD_LABELS[field.field]),
                          name: item.model_name,
                        })}
                        checked={
                          item.kind === 'create' ||
                          selection[item.model_name].includes(field.field)
                        }
                        disabled={item.kind === 'create'}
                        onCheckedChange={(checked) =>
                          chooseField(
                            item.model_name,
                            field.field,
                            Boolean(checked)
                          )
                        }
                      />
                    ),
                  },
                  {
                    id: 'field',
                    header: t('Field'),
                    cell: (field) => (
                      <div className='w-44 max-w-60 space-y-1 whitespace-normal'>
                        <span>{t(FIELD_LABELS[field.field])}</span>
                        {impact(field.field) && (
                          <p className='text-warning text-xs'>
                            {impact(field.field)}
                          </p>
                        )}
                      </div>
                    ),
                  },
                  {
                    id: 'local',
                    header: t('Current value'),
                    cell: (field) => (
                      <pre className='max-h-32 max-w-64 overflow-auto text-xs break-words whitespace-pre-wrap'>
                        {item.kind === 'create'
                          ? '—'
                          : display(field.field, field.local)}
                      </pre>
                    ),
                  },
                  {
                    id: 'upstream',
                    header: t('Change To'),
                    cell: (field) => (
                      <pre className='max-h-32 max-w-64 overflow-auto text-xs break-words whitespace-pre-wrap'>
                        {display(field.field, field.upstream)}
                      </pre>
                    ),
                  },
                ]}
              />
            </section>
          ))}
        </div>
      )}
      {step === 2 && (
        <div className='space-y-4'>
          <p className='text-sm'>
            {t(
              'Only the changes listed here will be committed. If any write fails, none of these changes are applied.'
            )}
          </p>
          <StaticDataTable
            data={updates}
            columns={[
              {
                id: 'model',
                header: t('Model'),
                cell: (item) => (
                  <span className='font-mono text-sm'>{item.model_name}</span>
                ),
              },
              {
                id: 'action',
                header: t('Planned action'),
                cell: (item) =>
                  item.create
                    ? t('Create metadata')
                    : t('Update selected fields'),
              },
              {
                id: 'fields',
                header: t('Fields'),
                cell: (item) =>
                  item.fields.map((field) => t(FIELD_LABELS[field])).join(', '),
              },
            ]}
          />
          <p className='text-sm'>
            {t('New vendors')}: {vendors.join(', ') || t('None')}
          </p>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Importing metadata does not add channels, enable model access, or configure prices.'
            )}
          </p>
          {apply.isError && (
            <ErrorState
              description={
                apply.error instanceof Error
                  ? apply.error.message
                  : t('Metadata sync failed')
              }
              action={
                <Button
                  variant='outline'
                  onClick={() => {
                    setStep(0)
                    setPreview(null)
                    setSelection({})
                    apply.reset()
                  }}
                >
                  {t('Preview again')}
                </Button>
              }
            />
          )}
        </div>
      )}
      {step === 3 && apply.data && (
        <div className='space-y-4' role='status'>
          <h3 className='font-semibold'>{t('Metadata sync completed')}</h3>
          <p>
            {t(
              '{{created}} models created, {{updated}} models updated, {{vendors}} vendors created.',
              {
                created: apply.data.created_models.length,
                updated: apply.data.updated_models.length,
                vendors: apply.data.created_vendors.length,
              }
            )}
          </p>
          <StaticDataTable
            data={[
              ...apply.data.created_models.map((name) => ({
                name,
                result: t('Create metadata'),
              })),
              ...apply.data.updated_models.map((item) => ({
                name: item.model_name,
                result: item.fields
                  .map((field) => t(FIELD_LABELS[field]))
                  .join(', '),
              })),
            ]}
            columns={[
              { id: 'model', header: t('Model'), cell: (item) => item.name },
              {
                id: 'result',
                header: t('Applied changes'),
                cell: (item) => item.result,
              },
            ]}
          />
          <p className='text-sm'>
            {t('New vendors')}:{' '}
            {apply.data.created_vendors.join(', ') || t('None')}
          </p>
        </div>
      )}
    </Dialog>
  )
}
