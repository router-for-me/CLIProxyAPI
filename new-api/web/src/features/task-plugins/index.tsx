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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CircleHelp, Upload } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { handleServerError } from '@/lib/handle-server-error'

import {
  getTaskPluginEnabledOption,
  listTaskPlugins,
  setTaskPluginEnabledOption,
} from './api'
import { MarketplacePanel } from './components/marketplace-panel'
import { PluginDetailSheet } from './components/plugin-detail-sheet'
import { PluginsTable } from './components/plugins-table'
import { UploadDialog } from './components/upload-dialog'
import type { TaskPluginListItem } from './types'

export function TaskPlugins() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [detail, setDetail] = useState<TaskPluginListItem | null>(null)
  const [tab, setTab] = useState('installed')
  const [uploadKey, setUploadKey] = useState<string | null>(null)
  const [uploadOpen, setUploadOpen] = useState(false)
  const [confirmDisable, setConfirmDisable] = useState(false)
  const enabledQuery = useQuery({
    queryKey: ['task-plugin-enabled'],
    queryFn: getTaskPluginEnabledOption,
  })
  const pluginsQuery = useQuery({
    queryKey: ['task-plugins'],
    queryFn: listTaskPlugins,
  })
  const enabledMutation = useMutation({
    mutationFn: setTaskPluginEnabledOption,
    onSuccess: (_, enabled) => {
      queryClient.setQueryData(['task-plugin-enabled'], enabled)
      toast.success(t('Task plugin setting updated'))
    },
    onError: (error) => handleServerError(error),
  })
  const openUpload = (key?: string) => {
    setUploadKey(key ?? null)
    setUploadOpen(true)
  }
  return (
    <>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('Task Plugins')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <div className='flex items-center gap-2'>
            <Switch
              id='task-plugin-enabled-switch'
              checked={enabledQuery.data ?? false}
              disabled={enabledQuery.isLoading || enabledMutation.isPending}
              onCheckedChange={(checked) => {
                if (checked) enabledMutation.mutate(true)
                else setConfirmDisable(true)
              }}
            />
            <Label
              htmlFor='task-plugin-enabled-switch'
              className='hidden text-sm font-normal sm:inline'
            >
              {t('Enable task plugins')}
            </Label>
            <Popover>
              <PopoverTrigger
                render={
                  <Button
                    variant='ghost'
                    size='icon-sm'
                    aria-label={t('Factory and custom plugin behavior')}
                  />
                }
              >
                <CircleHelp className='text-muted-foreground' />
              </PopoverTrigger>
              <PopoverContent align='end' className='w-80'>
                <div className='space-y-2 text-sm'>
                  <p className='font-medium'>{t('Enable task plugins')}</p>
                  <p className='text-muted-foreground'>
                    {t(
                      'When disabled, the entire task plugin system stops serving, including factory and custom plugins.'
                    )}
                  </p>
                  <p className='font-medium'>
                    {t('Factory and custom plugin behavior')}
                  </p>
                  <p className='text-muted-foreground'>
                    {t(
                      'Factory plugins cannot be deleted or disabled individually. A custom version can override them; deleting or disabling that version restores the factory plugin. Third-party-only platforms become unavailable when their plugin is deleted or disabled.'
                    )}
                  </p>
                </div>
              </PopoverContent>
            </Popover>
          </div>
          {tab === 'installed' && (
            <Button onClick={() => openUpload()}>
              <Upload />
              {t('Upload plugin')}
            </Button>
          )}
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <Tabs
            value={tab}
            onValueChange={setTab}
            className='flex h-full min-h-0 flex-col gap-3'
          >
            <TabsList className='max-w-full flex-wrap justify-start group-data-horizontal/tabs:h-auto'>
              <TabsTrigger value='installed'>{t('Installed')}</TabsTrigger>
              <TabsTrigger value='marketplace'>{t('Marketplace')}</TabsTrigger>
            </TabsList>
            <TabsContent value='installed' className='min-h-0 flex-1'>
              <PluginsTable
                onDetails={setDetail}
                onUpload={(key) => openUpload(key)}
              />
            </TabsContent>
            <TabsContent value='marketplace' className='min-h-0 flex-1'>
              <MarketplacePanel />
            </TabsContent>
          </Tabs>
        </SectionPageLayout.Content>
      </SectionPageLayout>
      <PluginDetailSheet
        key={detail?.meta.key ?? ''}
        plugin={detail}
        onOpenChange={(open) => {
          if (!open) setDetail(null)
        }}
      />
      <UploadDialog
        open={uploadOpen}
        onOpenChange={setUploadOpen}
        initialKey={uploadKey ?? undefined}
      />
      <ConfirmDialog
        open={confirmDisable}
        onOpenChange={setConfirmDisable}
        title={t('Disable task plugins?')}
        desc={
          <div className='space-y-2'>
            <p>
              {t(
                'Factory and custom plugins all stop serving immediately. In-flight tasks will be handled by timeout cleanup.'
              )}
            </p>
            <ul className='list-disc pl-5'>
              {(pluginsQuery.data ?? [])
                .filter((plugin) => plugin.source === 'override')
                .map((plugin) => (
                  <li key={plugin.meta.key}>
                    {plugin.meta.name} ({plugin.meta.key}):{' '}
                    {t('{{channels}} channels, {{tasks}} in-flight tasks', {
                      channels: plugin.channel_count,
                      tasks: plugin.in_flight_count,
                    })}
                  </li>
                ))}
            </ul>
          </div>
        }
        destructive
        handleConfirm={() =>
          enabledMutation.mutate(false, {
            onSuccess: () => setConfirmDisable(false),
          })
        }
        confirmText={t('Disable')}
      />
    </>
  )
}
