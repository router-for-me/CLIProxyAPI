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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { PluginIcon } from '@/features/task-plugins/components/plugin-icon'

import type { TaskPluginOption } from '../../api'
import { UpstreamModelSelection } from '../upstream-model-selection'

type ConfigureModelsDialogProps = {
  open: boolean
  models: string[]
  plugins?: TaskPluginOption[]
  initialPluginKey?: string
  onOpenChange: (open: boolean) => void
  onApply: (models: string[]) => void
  /** Called after the selection is applied when the user starts a redirect for a model. */
  onRedirect?: (model: string) => void
}

export function ConfigureModelsDialog(props: ConfigureModelsDialogProps) {
  const { t } = useTranslation()
  // Mounted for each opening. Keep unchecked candidates available until close.
  const [initialModels] = useState(props.models)
  const [selected, setSelected] = useState(initialModels)
  const [source, setSource] = useState(
    props.initialPluginKey ? `plugin:${props.initialPluginKey}` : 'all'
  )
  const plugins =
    props.plugins?.filter((plugin) => plugin.models.length > 0) ?? []
  const plugin = plugins.find((item) => `plugin:${item.key}` === source)
  const models = [
    ...new Set(
      plugin
        ? plugin.models
        : [...initialModels, ...plugins.flatMap((item) => item.models)]
    ),
  ]
  const onRedirect = props.onRedirect
  // Redirecting keeps the current picks and makes sure the redirected model is
  // published, otherwise the alias could never be called.
  const handleRedirect = onRedirect
    ? (model: string) => {
        props.onApply(
          selected.includes(model) ? selected : [...selected, model]
        )
        props.onOpenChange(false)
        onRedirect(model)
      }
    : undefined
  const selection = (
    <UpstreamModelSelection
      key={plugin?.key ?? 'all'}
      models={models}
      selected={selected}
      existingModels={initialModels}
      onChange={setSelected}
      onRedirectModel={handleRedirect}
      showChanges={false}
      summaryText={
        plugins.length > 0
          ? t('Selected {{selected}} / {{total}}', {
              selected: models.filter((model) => selected.includes(model))
                .length,
              total: models.length,
            })
          : t('Current models: {{count}}', { count: models.length })
      }
    />
  )

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Configure Models')}
      description={
        plugins.length > 0
          ? t('Select models and apply to channel models list.')
          : t('Select the models to keep in this channel.')
      }
      contentClassName='sm:max-w-3xl'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='button'
            onClick={() => {
              props.onApply(selected)
              props.onOpenChange(false)
            }}
          >
            {t('Apply')}
          </Button>
        </>
      }
    >
      {plugins.length > 0 ? (
        <Tabs
          value={plugin ? `plugin:${plugin.key}` : 'all'}
          onValueChange={(value) => setSource(String(value))}
          className='min-w-0 gap-3'
        >
          <div className='max-w-full overflow-x-auto'>
            <TabsList
              variant='line'
              aria-label={t('Models')}
              className='min-w-max'
            >
              <TabsTrigger value='all'>{t('All')}</TabsTrigger>
              {plugins.map((item) => (
                <TabsTrigger
                  key={item.key}
                  value={`plugin:${item.key}`}
                  title={item.name}
                >
                  <span aria-hidden='true'>
                    <PluginIcon plugin={item} size={16} />
                  </span>
                  <span className='max-w-40 truncate'>{item.name}</span>
                </TabsTrigger>
              ))}
            </TabsList>
          </div>
          <TabsContent value={plugin ? `plugin:${plugin.key}` : 'all'}>
            {selection}
          </TabsContent>
        </Tabs>
      ) : (
        selection
      )}
    </Dialog>
  )
}
