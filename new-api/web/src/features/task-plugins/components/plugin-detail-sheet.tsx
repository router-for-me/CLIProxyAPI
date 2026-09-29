import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RotateCcw, X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  Sheet,
  SheetContent,
  SheetClose,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
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
import { handleServerError } from '@/lib/handle-server-error'
import { resolveLocalizedText } from '@/lib/localized-text'

import {
  activateTaskPlugin,
  getTaskPlugin,
  getTaskPluginVersions,
} from '../api'
import type { TaskPluginListItem } from '../types'
import { JavaScriptViewer } from './javascript-viewer'
import { PluginIcon } from './plugin-icon'
import { PluginMetadataCard } from './plugin-metadata-card'
import { PluginModelList } from './plugin-model-list'
import { PluginSandbox } from './plugin-sandbox'
import { SourceDiff } from './source-diff'
import { UsageSchemaTable } from './usage-schema-table'

type PluginDetailSheetProps = {
  plugin: TaskPluginListItem | null
  onOpenChange: (open: boolean) => void
}

export function PluginDetailSheet(props: PluginDetailSheetProps) {
  return (
    <Sheet open={Boolean(props.plugin)} onOpenChange={props.onOpenChange}>
      {props.plugin && (
        <PluginDetailContent
          key={props.plugin.meta.key}
          plugin={props.plugin}
        />
      )}
    </Sheet>
  )
}

function PluginDetailContent(props: { plugin: TaskPluginListItem }) {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const key = props.plugin?.meta.key ?? ''
  const [activeTab, setActiveTab] = useState('overview')
  const [sandboxVisited, setSandboxVisited] = useState(false)
  const [compareVersion, setCompareVersion] = useState('')
  const detailQuery = useQuery({
    queryKey: ['task-plugin', key],
    queryFn: () => getTaskPlugin(key),
    enabled: Boolean(key),
  })
  const versionsQuery = useQuery({
    queryKey: ['task-plugin-versions', key],
    queryFn: () => getTaskPluginVersions(key),
    enabled: Boolean(key),
  })
  const compareQuery = useQuery({
    queryKey: ['task-plugin', key, compareVersion],
    queryFn: () => getTaskPlugin(key, compareVersion),
    enabled: Boolean(key && compareVersion),
  })
  const activateMutation = useMutation({
    mutationFn: (version: string) => activateTaskPlugin(key, version),
    onSuccess: () => {
      setCompareVersion('')
      toast.success(t('Plugin version activated'))
      queryClient.invalidateQueries({ queryKey: ['task-plugins'] })
      queryClient.invalidateQueries({ queryKey: ['task-plugin', key] })
      queryClient.invalidateQueries({ queryKey: ['task-plugin-versions', key] })
    },
    onError: (error) => handleServerError(error),
  })
  const detail = detailQuery.data
  const versions = versionsQuery.data ?? []
  const description = resolveLocalizedText(
    detail?.meta.description ?? props.plugin?.meta.description,
    i18n.language
  )
  let detailState = null
  if (detailQuery.isPending) detailState = <LoadingState />
  else if (detailQuery.isError) {
    detailState = (
      <ErrorState
        description={detailQuery.error.message}
        onRetry={() => void detailQuery.refetch()}
      />
    )
  } else if (!detail) detailState = <EmptyState />

  let versionsState = null
  if (versionsQuery.isPending) versionsState = <LoadingState />
  else if (versionsQuery.isError) {
    versionsState = (
      <ErrorState
        description={versionsQuery.error.message}
        onRetry={() => void versionsQuery.refetch()}
      />
    )
  } else if (!versions.length) {
    versionsState = <EmptyState title={t('No version history')} />
  }

  const panelClassName =
    'min-h-0 min-w-0 flex-1 overflow-y-auto overscroll-contain p-4 sm:p-6 data-hidden:hidden'
  const usageProfiles = detail?.meta.usageProfiles ?? []
  const groupedUsageModels = new Set(
    usageProfiles.flatMap((profile) => profile.models)
  )
  const defaultUsageModels = (detail?.meta.models ?? []).filter(
    (model) => !groupedUsageModels.has(model)
  )
  const showDefaultUsage =
    Object.keys(detail?.meta.usageSchema ?? {}).length > 0 &&
    (usageProfiles.length === 0 || defaultUsageModels.length > 0)
  return (
    <SheetContent
      showCloseButton={false}
      className='w-full gap-0 overflow-hidden sm:max-w-4xl'
    >
      <SheetClose
        render={
          <Button
            variant='ghost'
            size='icon-sm'
            className='absolute top-3 right-3'
            aria-label={t('Close')}
          />
        }
      >
        <X aria-hidden='true' />
      </SheetClose>
      <SheetHeader className='shrink-0 gap-2 border-b p-4 pr-12 sm:p-6 sm:pr-14'>
        <SheetTitle className='flex min-w-0 items-start gap-3'>
          <span className='shrink-0'>
            <PluginIcon
              plugin={{
                ...(detail?.meta ?? props.plugin.meta),
                hasIcon: detail?.has_icon ?? props.plugin.has_icon,
              }}
              size={32}
            />
          </span>
          <span className='min-w-0 text-lg font-semibold break-words'>
            {detail?.meta.name ?? props.plugin.meta.name}
          </span>
        </SheetTitle>
        <div className='flex min-w-0 items-center gap-1'>
          <span className='text-muted-foreground min-w-0 font-mono text-xs break-all'>
            {key}
          </span>
          <CopyButton
            value={key}
            className='size-7'
            iconClassName='size-3.5'
            aria-label={t('Copy plugin key')}
          />
          <Badge
            variant='secondary'
            className='ml-auto h-auto max-w-[35%] shrink-0 font-mono break-all whitespace-normal'
          >
            {detail?.meta.version ?? props.plugin.meta.version}
          </Badge>
        </div>
        <SheetDescription className='max-h-24 overflow-y-auto text-xs leading-relaxed break-words'>
          {description || t('Not declared')}
        </SheetDescription>
      </SheetHeader>
      <Tabs
        value={activeTab}
        onValueChange={(value) => {
          setActiveTab(String(value))
          if (value === 'sandbox') setSandboxVisited(true)
        }}
        className='min-h-0 min-w-0 flex-1 gap-0'
      >
        <div className='shrink-0 overflow-x-auto border-b px-4 sm:px-6'>
          <TabsList
            variant='line'
            className='min-w-max justify-start gap-4 group-data-horizontal/tabs:h-11'
            aria-label={t('Plugin details')}
          >
            <TabsTrigger value='overview'>{t('Overview')}</TabsTrigger>
            <TabsTrigger value='billing'>{t('Billing parameters')}</TabsTrigger>
            <TabsTrigger value='source'>{t('Plugin source')}</TabsTrigger>
            <TabsTrigger value='versions'>{t('Version history')}</TabsTrigger>
            <TabsTrigger value='diff'>{t('Source diff')}</TabsTrigger>
            <TabsTrigger value='sandbox'>{t('Plugin sandbox')}</TabsTrigger>
          </TabsList>
        </div>
        <TabsContent value='overview' className={panelClassName}>
          {detailState ?? (detail && <PluginMetadataCard meta={detail.meta} />)}
        </TabsContent>
        <TabsContent value='billing' className={panelClassName}>
          {detailState ?? (
            <div className='space-y-6'>
              {showDefaultUsage && detail?.meta.usageSchema && (
                <section className='space-y-3' aria-label={t('Default')}>
                  {usageProfiles.length > 0 && (
                    <>
                      <h3 className='text-sm font-medium'>{t('Default')}</h3>
                      <PluginModelList models={defaultUsageModels} />
                    </>
                  )}
                  <UsageSchemaTable schema={detail.meta.usageSchema} />
                </section>
              )}
              {usageProfiles.map((profile) => (
                <section
                  key={profile.models[0]}
                  aria-label={profile.models.join(', ')}
                  className='space-y-3'
                >
                  <PluginModelList models={profile.models} />
                  <UsageSchemaTable schema={profile.schema} />
                </section>
              ))}
              {!showDefaultUsage && usageProfiles.length === 0 && (
                <EmptyState title={t('No billing parameters declared')} />
              )}
            </div>
          )}
        </TabsContent>
        <TabsContent value='source' className={panelClassName}>
          {detailState ??
            (detail?.source ? (
              <JavaScriptViewer
                value={detail.source}
                className='bg-muted/20 h-full min-h-48 overflow-hidden rounded-lg border'
              />
            ) : (
              <EmptyState title={t('No source available')} />
            ))}
        </TabsContent>
        <TabsContent value='versions' className={panelClassName}>
          {versionsState ?? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Version')}</TableHead>
                  <TableHead>{t('Remark')}</TableHead>
                  <TableHead>{t('Status')}</TableHead>
                  <TableHead className='text-right'>{t('Actions')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {versions.map((version) => (
                  <TableRow key={version.id}>
                    <TableCell>{version.version}</TableCell>
                    <TableCell className='max-w-64 break-words whitespace-normal'>
                      {version.remark || '—'}
                    </TableCell>
                    <TableCell>
                      {version.active ? <Badge>{t('Active')}</Badge> : '—'}
                    </TableCell>
                    <TableCell className='text-right'>
                      <Button
                        size='sm'
                        variant='outline'
                        disabled={version.active || activateMutation.isPending}
                        onClick={() => activateMutation.mutate(version.version)}
                      >
                        <RotateCcw />
                        {t('Activate / Roll back')}
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </TabsContent>
        <TabsContent value='diff' className={`${panelClassName} space-y-3`}>
          {detailState ?? versionsState ?? (
            <>
              <Combobox
                options={versions
                  .filter((version) => version.version !== detail?.meta.version)
                  .map((version) => ({
                    value: version.version,
                    label: version.version,
                  }))}
                value={compareVersion}
                onValueChange={(value) => setCompareVersion(value ?? '')}
                placeholder={t('Select a version to compare')}
                aria-label={t('Select a version to compare')}
              />
              {!compareVersion && (
                <EmptyState title={t('Select a version to compare')} />
              )}
              {compareVersion && compareQuery.isPending && <LoadingState />}
              {compareVersion && compareQuery.isError && (
                <ErrorState
                  description={compareQuery.error.message}
                  onRetry={() => void compareQuery.refetch()}
                />
              )}
              {compareVersion &&
                !compareQuery.isPending &&
                !compareQuery.isError &&
                !compareQuery.data && <EmptyState />}
              {compareVersion &&
                !compareQuery.isError &&
                compareQuery.data &&
                detail && (
                  <SourceDiff
                    before={compareQuery.data.source}
                    after={detail.source}
                  />
                )}
            </>
          )}
        </TabsContent>
        <TabsContent
          value='sandbox'
          keepMounted={sandboxVisited}
          className={panelClassName}
        >
          {sandboxVisited && <PluginSandbox pluginKey={key} />}
        </TabsContent>
      </Tabs>
    </SheetContent>
  )
}
